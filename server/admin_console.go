package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxClientLogEntries   = 32
	maxClientLogMessage   = 2048
	maxClientStateItems   = 32
	maxClientLogBodyBytes = 128 << 10
	consoleDeviceCacheTTL = 5 * time.Second
	consoleDeviceTimeout  = 5 * time.Second
)

type cachedConsoleDevice struct {
	device    Device
	err       error
	fetchedAt time.Time
}

func (s *Service) handleAdminConsole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	if !s.requireAdminAPI(w, r, false) {
		return
	}
	now := time.Now()
	codes, err := s.store.ListPendingPairingCodes(now)
	if err != nil {
		s.logger.Errorf("admin-console", "读取待兑换授权码失败: %v", err)
		writeError(w, http.StatusInternalServerError, "console_read_failed", "读取控制台状态失败")
		return
	}
	sessions, err := s.store.ListActiveSessions()
	if err != nil {
		s.logger.Errorf("admin-console", "读取控制台会话失败: %v", err)
		writeError(w, http.StatusInternalServerError, "console_read_failed", "读取控制台状态失败")
		return
	}

	pending := make([]map[string]any, 0, len(codes))
	for _, code := range codes {
		pending = append(pending, s.consolePairingCode(code, now))
	}
	active := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		if session.Status == SessionStopped {
			continue
		}
		active = append(active, s.consoleSession(r, session, now))
	}

	counts := map[string]int{
		"pending":  len(pending),
		"sessions": len(active),
		"healthy":  0,
		"warning":  0,
		"offline":  0,
		"unknown":  0,
		"ending":   0,
		"cleaning": 0,
	}
	for _, session := range active {
		switch session["health"] {
		case "Healthy":
			counts["healthy"]++
		case "Warning":
			counts["warning"]++
		case "Offline":
			counts["offline"]++
		case "Unknown":
			counts["unknown"]++
		case "Ending":
			counts["ending"]++
		case "Cleaning":
			counts["cleaning"]++
		}
	}
	counts["attention"] = counts["sessions"] - counts["healthy"]
	writeJSON(w, http.StatusOK, map[string]any{
		"serverTime":    now.UTC().Format(time.RFC3339Nano),
		"eventSequence": s.events.latest(),
		"pending":       pending,
		"sessions":      active,
		"counts":        counts,
	})
}

func (s *Service) consolePairingCode(code PairingCode, now time.Time) map[string]any {
	value := ""
	if len(code.CodeCipher) != 0 {
		if plaintext, err := s.cipher.Open("pairing-code:"+code.Hash, code.CodeCipher); err == nil && validPairingCode(plaintext) {
			value = plaintext
		}
	}
	remaining := int64(0)
	if code.ExpiresAt.After(now) {
		remaining = int64((code.ExpiresAt.Sub(now) + time.Second - 1) / time.Second)
	}
	return map[string]any{
		"code":             value,
		"codeAvailable":    value != "",
		"codeRef":          diagnosticIdentifier(code.Hash),
		"createdAt":        code.CreatedAt.UTC().Format(time.RFC3339),
		"expiresAt":        code.ExpiresAt.UTC().Format(time.RFC3339),
		"remainingSeconds": remaining,
		"status":           "pending",
		"configSummary":    sessionConfigSummary(code.Config),
		"config":           cloneSessionConfig(code.Config),
	}
}

func (s *Service) consoleSession(r *http.Request, session Session, now time.Time) map[string]any {
	clientState := decodeClientState(session.ClientStateJSON)
	device, tailscaleErr := s.consoleDevice(r, session)
	health, healthReason := consoleHealth(s.config.SyncLeaseTTL, session, clientState, device, tailscaleErr, now)
	item := map[string]any{
		"sessionId":             session.ID,
		"authorizationCode":     s.sessionPairingCode(session),
		"authorizationCodeRef":  diagnosticIdentifier(session.PairingCodeHash),
		"status":                session.Status,
		"health":                health,
		"healthReason":          healthReason,
		"provisioningHostname":  session.ProvisioningName,
		"createdAt":             session.CreatedAt.UTC().Format(time.RFC3339),
		"connectedAt":           formatOptionalTime(session.ConnectedAt),
		"lastSeenAt":            formatOptionalTime(session.LastSeenAt),
		"syncDeadline":          formatOptionalTime(session.SyncDeadline),
		"provisioningDeadline":  formatOptionalTime(session.ProvisioningDeadline),
		"expiresAt":             formatOptionalTime(session.ExpiresAt),
		"configSummary":         sessionConfigSummary(session.Config),
		"config":                cloneSessionConfig(session.Config),
		"configRevision":        session.ConfigRevision,
		"appliedConfigRevision": session.AppliedConfigRevision,
		"routes":                append([]string{}, session.Routes...),
		"wifiRoutes":            append([]string{}, session.WiFiRoutes...),
		"clientState":           clientState,
		"tailscaleStatus":       "not-attached",
	}
	if tailscaleErr != nil {
		item["tailscaleStatus"] = "unavailable"
		item["tailscaleError"] = "Tailscale 状态暂时不可用"
	} else if device != nil {
		item["tailscaleStatus"] = "available"
		item["tailscale"] = publicConsoleDevice(*device)
	}
	return item
}

func (s *Service) consoleDevice(r *http.Request, session Session) (*Device, error) {
	if session.DeviceID == "" {
		return nil, nil
	}
	cacheKey := session.CredentialID + "\x00" + session.DeviceID
	now := time.Now()
	s.consoleDeviceMu.Lock()
	if cached, ok := s.consoleDeviceCache[cacheKey]; ok && now.Before(cached.fetchedAt.Add(consoleDeviceCacheTTL)) {
		device, err := cached.device, cached.err
		s.consoleDeviceMu.Unlock()
		if err != nil {
			return nil, err
		}
		return &device, nil
	}
	s.consoleDeviceMu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), consoleDeviceTimeout)
	defer cancel()
	accessToken, err := s.credentialToken(ctx, session.CredentialID)
	if err != nil {
		s.cacheConsoleDevice(cacheKey, Device{}, err, now)
		return nil, err
	}
	device, err := s.tailscale.GetDevice(ctx, accessToken, session.DeviceID)
	s.cacheConsoleDevice(cacheKey, device, err, now)
	if err != nil {
		return nil, err
	}
	return &device, nil
}

func (s *Service) cacheConsoleDevice(cacheKey string, device Device, err error, fetchedAt time.Time) {
	s.consoleDeviceMu.Lock()
	s.consoleDeviceCache[cacheKey] = cachedConsoleDevice{device: device, err: err, fetchedAt: fetchedAt}
	s.consoleDeviceMu.Unlock()
}

func (s *Service) sessionPairingCode(session Session) string {
	if session.PairingCodeHash == "" {
		return ""
	}
	code, ok, err := s.store.GetPairingCode(session.PairingCodeHash)
	if err != nil || !ok || len(code.CodeCipher) == 0 {
		return ""
	}
	value, err := s.cipher.Open("pairing-code:"+session.PairingCodeHash, code.CodeCipher)
	if err != nil || !validPairingCode(value) {
		return ""
	}
	return value
}

func decodeClientState(encoded string) map[string]any {
	var state map[string]any
	if err := json.Unmarshal([]byte(encoded), &state); err != nil || state == nil {
		return map[string]any{}
	}
	return state
}

func publicConsoleDevice(device Device) map[string]any {
	return map[string]any{
		"id":               device.ID,
		"nodeId":           device.NodeID,
		"name":             device.Name,
		"hostname":         device.Hostname,
		"createdAt":        formatRequiredDeviceTime(device.Created),
		"lastSeenAt":       formatOptionalDeviceTime(device.LastSeen),
		"addresses":        append([]string{}, device.Addresses...),
		"tags":             append([]string{}, device.Tags...),
		"online":           device.Online,
		"authorized":       device.Authorized,
		"isEphemeral":      device.IsEphemeral,
		"os":               device.OS,
		"clientVersion":    device.ClientVersion,
		"advertisedRoutes": append([]string{}, device.AdvertisedRoutes...),
		"enabledRoutes":    append([]string{}, device.EnabledRoutes...),
		"isExitNode":       device.IsExitNode,
		"isSubnetRouter":   device.IsSubnetRouter,
	}
}

func formatRequiredDeviceTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatOptionalDeviceTime(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

func sessionConfigSummary(config SessionConfig) string {
	switch {
	case config.NetworkMode == NetworkModeCellular && config.AutoGatewayRoute:
		return "救援连接"
	case config.AutoWiFiSubnetRoute:
		return "子网路由"
	case config.AdvertiseExitNode:
		return "Exit Node"
	case config.UseExitNode:
		return "使用 Exit Node 的普通节点"
	default:
		return "普通节点"
	}
}

func consoleHealth(
	leaseTTL time.Duration,
	session Session,
	clientState map[string]any,
	device *Device,
	tailscaleErr error,
	now time.Time,
) (string, string) {
	switch session.Status {
	case SessionCleaning, SessionCleanupFailed:
		return "Cleaning", "会话正在清理"
	case SessionProvisioning:
		if !session.ProvisioningDeadline.IsZero() && !now.Before(session.ProvisioningDeadline) {
			return "Ending", "设备绑定期限已到"
		}
		return "Unknown", "等待客户端绑定 Tailscale 节点"
	}
	if !session.ExpiresAt.IsZero() && !now.Before(session.ExpiresAt) {
		return "Ending", "会话已到自动退出时间"
	}
	if session.LastSeenAt.IsZero() {
		return "Unknown", "尚未收到客户端状态"
	}
	if leaseTTL <= 0 {
		leaseTTL = 5 * time.Minute
	}
	age := now.Sub(session.LastSeenAt)
	if age >= leaseTTL {
		return "Offline", "PinNode 客户端状态上报已超时"
	}
	if backend := strings.ToLower(clientStateString(clientState, "backendState")); backend != "" &&
		backend != "running" && backend != "active" {
		return "Warning", "PinNode 客户端未处于 Running 状态"
	}
	if len(clientStateStringList(clientState, "health")) != 0 {
		return "Warning", "客户端报告了 Tailscale 健康问题"
	}
	if clientStateString(clientState, "lastError") != "" {
		return "Warning", "客户端报告了最近错误"
	}
	if tailscaleErr != nil {
		return "Unknown", "Tailscale 控制面状态暂时不可用"
	}
	if device == nil {
		return "Unknown", "尚未取得 Tailscale 节点状态"
	}
	if device.IsEphemeral {
		return "Warning", "Tailscale 节点被标记为 ephemeral"
	}
	if !device.Online {
		return "Warning", "Tailscale 节点离线，但 PinNode 最近仍有上报"
	}
	if age >= leaseTTL/2 {
		return "Warning", "客户端状态上报延迟"
	}
	return "Healthy", "PinNode 客户端和 Tailscale 节点均有近期状态"
}

func clientStateString(state map[string]any, key string) string {
	value, _ := state[key].(string)
	return value
}

func clientStateStringList(state map[string]any, key string) []string {
	values, ok := state[key].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func (s *Service) handleAdminRecentLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	if !s.requireAdminAPI(w, r, false) {
		return
	}
	limit := 200
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > adminEventBufferSize {
			writeError(w, http.StatusBadRequest, "limit_invalid", "limit 必须在 1 到 2048 之间")
			return
		}
		limit = parsed
	}
	logs, latest, oldest := s.events.recentLogs(limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"logs":           logs,
		"latestSequence": latest,
		"oldestSequence": oldest,
	})
}

func (s *Service) handleAdminEventStream(w http.ResponseWriter, r *http.Request, kind string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	if !s.requireAdminAPI(w, r, false) {
		return
	}
	adminTokenHash, ok := adminSessionHash(r)
	if !ok {
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", "当前响应不支持实时事件流")
		return
	}
	replay, events, cancel, missed := s.events.subscribe(kind, eventID(r))
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if _, err := fmt.Fprint(w, ": PinNode event stream\n\n"); err != nil {
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if missed {
		if _, err := fmt.Fprint(w, "event: reset\ndata: {\"reason\":\"resume_window_exceeded\"}\n\n"); err != nil {
			return
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	if !s.adminSessionValid(adminTokenHash) {
		writeSSEAuthExpired(w)
		return
	}
	for _, event := range replay {
		if err := writeSSEEvent(w, event); err != nil {
			return
		}
	}
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.Kind != kind {
				continue
			}
			if !s.adminSessionValid(adminTokenHash) {
				writeSSEAuthExpired(w)
				return
			}
			if writeSSEEvent(w, event) != nil {
				return
			}
		case <-keepAlive.C:
			if !s.adminSessionValid(adminTokenHash) {
				writeSSEAuthExpired(w)
				return
			}
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Service) adminSessionValid(tokenHash string) bool {
	_, ok, err := s.store.GetAdminSession(tokenHash, time.Now())
	return err == nil && ok
}

func writeSSEAuthExpired(w http.ResponseWriter) {
	if _, err := fmt.Fprint(w, "event: auth-expired\ndata: {\"reason\":\"admin_session_invalid\"}\n\n"); err != nil {
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func validateClientState(state clientStateReport) error {
	for name, value := range map[string]string{
		"backendState":  state.BackendState,
		"networkMode":   state.NetworkMode,
		"tailscalePath": state.TailscalePath,
		"interfaceName": state.InterfaceName,
		"deviceName":    state.DeviceName,
		"deviceModel":   state.DeviceModel,
		"os":            state.OS,
		"osVersion":     state.OSVersion,
		"lastError":     state.LastError,
	} {
		if len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("客户端状态字段 %s 无效", name)
		}
	}
	if len(state.TailscaleIPs) > maxClientStateItems || len(state.AllowedIPs) > maxClientStateItems ||
		len(state.AdvertisedRoutes) > maxClientStateItems || len(state.Health) > maxClientStateItems {
		return fmt.Errorf("客户端状态列表过长")
	}
	for _, values := range [][]string{state.TailscaleIPs, state.AllowedIPs, state.AdvertisedRoutes, state.Health} {
		for _, value := range values {
			if len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
				return fmt.Errorf("客户端状态列表项无效")
			}
		}
	}
	return nil
}

func (s *Service) handleSessionLogs(w http.ResponseWriter, r *http.Request, session Session) {
	if session.Status != SessionActive {
		writeError(w, http.StatusConflict, "session_state_conflict", "会话尚未绑定设备或正在清理")
		return
	}
	var request sessionLogsRequest
	if !decodeJSONWithLimit(w, r, &request, maxClientLogBodyBytes) {
		return
	}
	if len(request.Logs) > maxClientLogEntries {
		writeError(w, http.StatusBadRequest, "client_logs_invalid", "一次最多上传 32 条日志")
		return
	}
	accepted := 0
	for _, entry := range request.Logs {
		level := strings.ToUpper(strings.TrimSpace(entry.Level))
		if level == "VERBOSE" {
			level = "DEBUG"
		}
		component := strings.TrimSpace(entry.Component)
		message := redactLogMessage(strings.TrimSpace(entry.Message))
		if !validLogLevel(level) || component == "" || len(component) > 128 ||
			strings.ContainsAny(component, "\r\n\x00") || message == "" || len(message) > maxClientLogMessage {
			writeError(w, http.StatusBadRequest, "client_logs_invalid", "客户端日志字段无效")
			return
		}
		value := strings.TrimSpace(entry.Timestamp)
		if value == "" {
			writeError(w, http.StatusBadRequest, "client_logs_invalid", "客户端日志时间戳无效")
			return
		}
		stamp, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "client_logs_invalid", "客户端日志时间戳无效")
			return
		}
		s.events.publishLog(LogEvent{
			Timestamp: stamp.UTC().Format(time.RFC3339Nano),
			Level:     level,
			Source:    "client",
			Component: component,
			Message:   message,
			SessionID: session.ID,
			NodeID:    session.DeviceID,
		})
		accepted++
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": accepted})
}

func validLogLevel(level string) bool {
	switch level {
	case "DEBUG", "INFO", "WARN", "ERROR":
		return true
	default:
		return false
	}
}
