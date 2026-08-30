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

type adminEventSubscription struct {
	kind   string
	events chan adminEvent
}

type adminEventHub struct {
	mu           sync.Mutex
	next         uint64
	stateEvents  []adminEvent
	logEvents    []adminEvent
	stateDropped bool
	logDropped   bool
	subscribers  map[*adminEventSubscription]struct{}
}

func newAdminEventHub() *adminEventHub {
	return &adminEventHub{subscribers: make(map[*adminEventSubscription]struct{})}
}

func (h *adminEventHub) publish(kind string, data any) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	event := adminEvent{Sequence: h.next, Kind: kind, Data: data}
	h.appendEventLocked(event)
	for subscriber := range h.subscribers {
		if subscriber.kind != "" && subscriber.kind != kind {
			continue
		}
		select {
		case subscriber.events <- event:
		default:
			delete(h.subscribers, subscriber)
			close(subscriber.events)
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
	h.appendEventLocked(adminEvent)
	for subscriber := range h.subscribers {
		if subscriber.kind != "" && subscriber.kind != "log" {
			continue
		}
		select {
		case subscriber.events <- adminEvent:
		default:
			delete(h.subscribers, subscriber)
			close(subscriber.events)
		}
	}
	return event
}

func (h *adminEventHub) appendEventLocked(event adminEvent) {
	if event.Kind == "log" {
		if len(h.logEvents) >= adminEventBufferSize {
			h.logEvents = h.logEvents[1:]
			h.logDropped = true
		}
		h.logEvents = append(h.logEvents, event)
		return
	}
	if len(h.stateEvents) >= adminEventBufferSize {
		h.stateEvents = h.stateEvents[1:]
		h.stateDropped = true
	}
	h.stateEvents = append(h.stateEvents, event)
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
	for _, event := range h.logEvents {
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
	history, dropped := h.historyLocked(kind)
	if dropped && len(history) > 0 && after < history[0].Sequence {
		missed = true
	}
	for _, event := range history {
		if event.Sequence > after && (kind == "" || event.Kind == kind) {
			replay = append(replay, event)
		}
	}
	subscriber := &adminEventSubscription{kind: kind, events: make(chan adminEvent, 64)}
	h.subscribers[subscriber] = struct{}{}
	var once sync.Once
	cancel = func() {
		once.Do(func() {
			h.mu.Lock()
			if _, ok := h.subscribers[subscriber]; ok {
				delete(h.subscribers, subscriber)
				close(subscriber.events)
			}
			h.mu.Unlock()
		})
	}
	return replay, subscriber.events, cancel, missed
}

func (h *adminEventHub) historyLocked(kind string) ([]adminEvent, bool) {
	switch kind {
	case "state":
		return h.stateEvents, h.stateDropped
	case "log":
		return h.logEvents, h.logDropped
	default:
		return nil, false
	}
}

var sensitiveLogPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)((?:authorization|cookie|set-cookie|x-api-key|x-auth-token)\s*[:=]\s*(?:bearer\s+)?)[^,\s;]+`),
	regexp.MustCompile(`(?i)(["']?\b(?:sessionToken|session_token|authKey|auth_key|password|clientSecret|client_secret|accessToken|access_token|oauthSecret|oauth_secret|apiKey|api_key|auth-key|pairingCode|pairing_code|oneTimeCode|one_time_code|cookie|pin|token|code|secret)\b["']?\s*[:=]\s*)(["'][^"']*["']|[^,\s}&]+)`),
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
	l.PrintfComponent("server", format, args...)
}

func (l *structuredLogger) PrintfComponent(component, format string, args ...any) {
	l.logf(inferLogLevel(fmt.Sprintf(format, args...)), component, format, args...)
}

func (l *structuredLogger) Infof(component, format string, args ...any) {
	l.logf("INFO", component, format, args...)
}

func (l *structuredLogger) Warnf(component, format string, args ...any) {
	l.logf("WARN", component, format, args...)
}

func (l *structuredLogger) Errorf(component, format string, args ...any) {
	l.logf("ERROR", component, format, args...)
}

func (l *structuredLogger) logf(level, component, format string, args ...any) {
	message := redactLogMessage(fmt.Sprintf(format, args...))
	component = strings.TrimSpace(component)
	if component == "" {
		component = "server"
	}
	l.base.Printf("%s", message)
	l.events.publishLog(LogEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Level:     level,
		Source:    "server",
		Component: component,
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
