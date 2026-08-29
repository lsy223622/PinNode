package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const adminEventBufferSize = 2048

type LogEvent struct {
	Sequence  uint64 `json:"sequence"`
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Source    string `json:"source"`
	Component string `json:"component"`
	Message   string `json:"message"`
	SessionID string `json:"sessionId,omitempty"`
	NodeID    string `json:"nodeId,omitempty"`
}

type adminEvent struct {
	Sequence uint64
	Kind     string
	Data     any
}

type adminEventHub struct {
	mu          sync.Mutex
	next        uint64
	events      []adminEvent
	subscribers map[chan adminEvent]struct{}
}

func newAdminEventHub() *adminEventHub {
	return &adminEventHub{subscribers: make(map[chan adminEvent]struct{})}
}

func (h *adminEventHub) publish(kind string, data any) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	event := adminEvent{Sequence: h.next, Kind: kind, Data: data}
	h.events = append(h.events, event)
	if len(h.events) > adminEventBufferSize {
		h.events = h.events[len(h.events)-adminEventBufferSize:]
	}
	for subscriber := range h.subscribers {
		select {
		case subscriber <- event:
		default:
			delete(h.subscribers, subscriber)
			close(subscriber)
		}
	}
	return event.Sequence
}

func (h *adminEventHub) publishState(reason string) uint64 {
	return h.publish("state", map[string]string{"reason": reason})
}

func (h *adminEventHub) publishLog(event LogEvent) LogEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	event.Sequence = h.next
	adminEvent := adminEvent{Sequence: h.next, Kind: "log", Data: event}
	h.events = append(h.events, adminEvent)
	if len(h.events) > adminEventBufferSize {
		h.events = h.events[len(h.events)-adminEventBufferSize:]
	}
	for subscriber := range h.subscribers {
		select {
		case subscriber <- adminEvent:
		default:
			delete(h.subscribers, subscriber)
			close(subscriber)
		}
	}
	return event
}

func (h *adminEventHub) latest() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.next
}

func (h *adminEventHub) recentLogs(limit int) (logs []LogEvent, latest, oldest uint64) {
	if limit < 1 {
		limit = 1
	}
	if limit > adminEventBufferSize {
		limit = adminEventBufferSize
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	latest = h.next
	for _, event := range h.events {
		if event.Kind != "log" {
			continue
		}
		value, ok := event.Data.(LogEvent)
		if !ok {
			continue
		}
		if oldest == 0 {
			oldest = event.Sequence
		}
		logs = append(logs, value)
	}
	if len(logs) > limit {
		logs = logs[len(logs)-limit:]
	}
	return logs, latest, oldest
}

func (h *adminEventHub) subscribe(kind string, after uint64) (replay []adminEvent, events <-chan adminEvent, cancel func(), missed bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.events) > 0 && after+1 < h.events[0].Sequence {
		missed = true
	}
	for _, event := range h.events {
		if event.Sequence > after && (kind == "" || event.Kind == kind) {
			replay = append(replay, event)
		}
	}
	subscriber := make(chan adminEvent, 64)
	h.subscribers[subscriber] = struct{}{}
	var once sync.Once
	cancel = func() {
		once.Do(func() {
			h.mu.Lock()
			if _, ok := h.subscribers[subscriber]; ok {
				delete(h.subscribers, subscriber)
				close(subscriber)
			}
			h.mu.Unlock()
		})
	}
	return replay, subscriber, cancel, missed
}

var sensitiveLogPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+)?)[^,\s]+`),
	regexp.MustCompile(`(?i)(["']?\b(?:sessionToken|session_token|authKey|auth_key|password|clientSecret|client_secret|token|code|secret)\b["']?\s*[:=]\s*)(["'][^"']*["']|[^,\s}&]+)`),
}

func redactLogMessage(message string) string {
	for _, pattern := range sensitiveLogPatterns {
		message = pattern.ReplaceAllString(message, "$1[REDACTED]")
	}
	if len(message) > 4096 {
		message = message[:4096] + "…"
	}
	return message
}

func inferLogLevel(message string) string {
	if strings.Contains(message, "失败") || strings.Contains(message, "错误") ||
		strings.Contains(strings.ToLower(message), " error") || strings.Contains(strings.ToLower(message), "error=") {
		return "ERROR"
	}
	if strings.Contains(message, "警告") || strings.Contains(strings.ToLower(message), "warn") {
		return "WARN"
	}
	return "INFO"
}

type structuredLogger struct {
	base   *log.Logger
	events *adminEventHub
}

func (l *structuredLogger) Printf(format string, args ...any) {
	message := redactLogMessage(fmt.Sprintf(format, args...))
	l.base.Printf("%s", message)
	l.events.publishLog(LogEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Level:     inferLogLevel(message),
		Source:    "server",
		Component: "server",
		Message:   message,
	})
}

type clientStateReport struct {
	BackendState      string   `json:"backendState"`
	TailscaleRunning  bool     `json:"tailscaleRunning"`
	VPNEnabled        bool     `json:"vpnEnabled"`
	NetworkMode       string   `json:"networkMode"`
	TailscalePath     string   `json:"tailscalePath"`
	WiFiConnected     bool     `json:"wifiConnected"`
	CellularConnected bool     `json:"cellularConnected"`
	InternetAvailable bool     `json:"internetAvailable"`
	InterfaceName     string   `json:"interfaceName"`
	TailscaleIPs      []string `json:"tailscaleIps"`
	AllowedIPs        []string `json:"allowedIps"`
	AdvertisedRoutes  []string `json:"advertisedRoutes"`
	DeviceName        string   `json:"deviceName"`
	DeviceModel       string   `json:"deviceModel"`
	OS                string   `json:"os"`
	OSVersion         string   `json:"osVersion"`
	Health            []string `json:"health"`
	LastError         string   `json:"lastError"`
}

type clientLogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Component string `json:"component"`
	Message   string `json:"message"`
}

type sessionLogsRequest struct {
	Logs []clientLogEntry `json:"logs"`
}

func writeSSEEvent(w http.ResponseWriter, event adminEvent) error {
	data, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Kind, data); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func eventID(r *http.Request) uint64 {
	value := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if value == "" {
		value = strings.TrimSpace(r.URL.Query().Get("after"))
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}
