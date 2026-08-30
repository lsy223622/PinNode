package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAdminConsoleReportsPendingAndActiveClientState(t *testing.T) {
	fake := &fakeTailscale{
		device: Device{
			NodeID:     "n-console",
			Tags:       []string{managedDeviceTag},
			Online:     true,
			Authorized: true,
			Name:       "console-device.example.ts.net",
			Addresses:  []string{"100.64.0.42"},
			OS:         "android",
		},
	}
	store := NewStore()
	defer store.Close()
	service := NewService(testServiceConfig(), store, fake, nil)
	authentication, credentialID := installTestAdminAndCredential(t, service, store)
	code := createTestPairingCode(t, service, authentication, credentialID)

	console := getAdminConsole(t, service, authentication)
	if len(console.Pending) != 1 || console.Pending[0].Code != code ||
		!console.Pending[0].CodeAvailable || console.Pending[0].RemainingSeconds < 1 {
		t.Fatalf("待兑换 PIN 状态错误: %+v", console.Pending)
	}

	startRequest := newSessionStartRequest(`{"code":"` + code + `","gatewayRoute":"192.168.1.1/32"}`)
	startResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(startResponse, startRequest)
	if startResponse.Code != http.StatusCreated {
		t.Fatalf("启动会话返回 %d: %s", startResponse.Code, startResponse.Body.String())
	}
	var started struct {
		ID       string `json:"sessionId"`
		Token    string `json:"sessionToken"`
		Hostname string `json:"provisioningHostname"`
	}
	if err := json.Unmarshal(startResponse.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.device.Hostname = started.Hostname
	fake.device.Created = time.Now()
	fake.device.LastSeen = time.Now()
	fake.mu.Unlock()

	attachRequest := newJSONRequest(
		http.MethodPost,
		"/v1/sessions/"+started.ID+"/device",
		`{"nodeId":"n-console"}`,
	)
	attachRequest.Header.Set("Authorization", "Bearer "+started.Token)
	attachResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(attachResponse, attachRequest)
	if attachResponse.Code != http.StatusOK {
		t.Fatalf("绑定设备返回 %d: %s", attachResponse.Code, attachResponse.Body.String())
	}

	syncRequest := newJSONRequest(
		http.MethodPost,
		"/v1/sessions/"+started.ID+"/sync",
		`{"protocolVersion":1,"appliedConfigRevision":1,"clientVersion":"test","clientCapabilities":["session-sync-v1"],"clientState":{"backendState":"running","tailscaleRunning":true,"vpnEnabled":true,"networkMode":"default","tailscalePath":"default","wifiConnected":true,"cellularConnected":false,"internetAvailable":true,"interfaceName":"wlan0","tailscaleIps":["100.64.0.42"],"allowedIps":["100.64.0.42/32"],"advertisedRoutes":["192.168.1.1/32"],"deviceName":"phone","deviceModel":"test","os":"android","osVersion":"16","health":[],"lastError":""}}`,
	)
	syncRequest.Header.Set("Authorization", "Bearer "+started.Token)
	syncResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(syncResponse, syncRequest)
	if syncResponse.Code != http.StatusOK {
		t.Fatalf("会话同步返回 %d: %s", syncResponse.Code, syncResponse.Body.String())
	}

	logsRequest := newJSONRequest(
		http.MethodPost,
		"/v1/sessions/"+started.ID+"/logs",
		`{"logs":[{"timestamp":"2026-08-29T00:00:00Z","level":"ERROR","component":"test","message":"sessionToken=super-secret password=also-secret"}]}`,
	)
	logsRequest.Header.Set("Authorization", "Bearer "+started.Token)
	logsResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(logsResponse, logsRequest)
	if logsResponse.Code != http.StatusOK {
		t.Fatalf("客户端日志上传返回 %d: %s", logsResponse.Code, logsResponse.Body.String())
	}

	console = getAdminConsole(t, service, authentication)
	if len(console.Sessions) != 1 || console.Sessions[0].ID != started.ID {
		t.Fatalf("活动会话没有进入 Console: %+v", console.Sessions)
	}
	if console.Sessions[0].Health != "Healthy" ||
		console.Sessions[0].ClientState["interfaceName"] != "wlan0" ||
		console.Sessions[0].Tailscale["online"] != true {
		t.Fatalf("活动会话健康状态或快照错误: %+v", console.Sessions[0])
	}

	recentRequest := httptest.NewRequest(http.MethodGet, "/v1/admin/logs/recent?limit=10", nil)
	authorizeTestAdmin(recentRequest, authentication, false)
	recentResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(recentResponse, recentRequest)
	if recentResponse.Code != http.StatusOK {
		t.Fatalf("读取最近日志返回 %d: %s", recentResponse.Code, recentResponse.Body.String())
	}
	var recent struct {
		Logs []LogEvent `json:"logs"`
	}
	if err := json.Unmarshal(recentResponse.Body.Bytes(), &recent); err != nil {
		t.Fatal(err)
	}
	var found *LogEvent
	for index := range recent.Logs {
		if recent.Logs[index].SessionID == started.ID {
			found = &recent.Logs[index]
			break
		}
	}
	if found == nil || found.Level != "ERROR" || found.Source != "client" ||
		strings.Contains(found.Message, "super-secret") || strings.Contains(found.Message, "also-secret") ||
		!strings.Contains(found.Message, "[REDACTED]") {
		t.Fatalf("客户端日志没有正确结构化或脱敏: %+v", found)
	}
}

type adminConsoleTestResponse struct {
	Counts  map[string]int `json:"counts"`
	Pending []struct {
		Code             string `json:"code"`
		CodeAvailable    bool   `json:"codeAvailable"`
		RemainingSeconds int64  `json:"remainingSeconds"`
	} `json:"pending"`
	Sessions []struct {
		ID          string         `json:"sessionId"`
		Health      string         `json:"health"`
		ClientState map[string]any `json:"clientState"`
		Tailscale   map[string]any `json:"tailscale"`
	} `json:"sessions"`
}

func TestAdminConsoleCountsEveryNonHealthyStateAsAttention(t *testing.T) {
	now := time.Now()
	fake := &fakeTailscale{device: Device{
		NodeID: "n-healthy", Hostname: "healthy.example.ts.net", Online: true,
		Authorized: true, Tags: []string{managedDeviceTag}, Created: now,
	}}
	store := NewStore()
	defer store.Close()
	service := NewService(testServiceConfig(), store, fake, nil)
	authentication, credentialID := installTestAdminAndCredential(t, service, store)
	config := DefaultSessionConfig()
	put := func(session Session) {
		t.Helper()
		if session.CreatedAt.IsZero() {
			session.CreatedAt = now
		}
		if session.UpdatedAt.IsZero() {
			session.UpdatedAt = now
		}
		if session.ConfigRevision == 0 {
			session.ConfigRevision = 1
		}
		session.Config = config
		if err := store.PutSession(session); err != nil {
			t.Fatal(err)
		}
	}
	put(Session{ID: "healthy", TokenHash: "hash-healthy", AuthKeyID: "key-healthy", ProvisioningName: "pinnode-healthy", CredentialID: credentialID, DeviceID: "n-healthy", Status: SessionActive, LastSeenAt: now, ClientStateJSON: `{"backendState":"running"}`})
	put(Session{ID: "warning", TokenHash: "hash-warning", AuthKeyID: "key-warning", ProvisioningName: "pinnode-warning", Status: SessionActive, LastSeenAt: now, ClientStateJSON: `{"backendState":"stopped"}`})
	put(Session{ID: "offline", TokenHash: "hash-offline", AuthKeyID: "key-offline", ProvisioningName: "pinnode-offline", Status: SessionActive, LastSeenAt: now.Add(-10 * time.Minute)})
	put(Session{ID: "unknown", TokenHash: "hash-unknown", AuthKeyID: "key-unknown", ProvisioningName: "pinnode-unknown", Status: SessionActive})
	put(Session{ID: "ending", TokenHash: "hash-ending", AuthKeyID: "key-ending", ProvisioningName: "pinnode-ending", Status: SessionActive, ExpiresAt: now.Add(-time.Second)})
	put(Session{ID: "cleaning", TokenHash: "hash-cleaning", AuthKeyID: "key-cleaning", ProvisioningName: "pinnode-cleaning", Status: SessionCleaning})
	put(Session{ID: "cleanup-failed", TokenHash: "hash-cleanup-failed", AuthKeyID: "key-cleanup-failed", ProvisioningName: "pinnode-cleanup-failed", Status: SessionCleanupFailed})
	put(Session{ID: "stopped", TokenHash: "hash-stopped", AuthKeyID: "key-stopped", ProvisioningName: "pinnode-stopped", Status: SessionStopped})

	console := getAdminConsole(t, service, authentication)
	if len(console.Sessions) != 7 {
		t.Fatalf("活动会话数量错误: %d", len(console.Sessions))
	}
	for key, want := range map[string]int{
		"sessions": 7, "healthy": 1, "warning": 1, "offline": 1,
		"unknown": 1, "ending": 1, "cleaning": 2, "attention": 6,
	} {
		if got := console.Counts[key]; got != want {
			t.Fatalf("Console 统计 %s=%d, want %d; counts=%v", key, got, want, console.Counts)
		}
	}
	_ = getAdminConsole(t, service, authentication)
	fake.mu.Lock()
	deviceCalls := fake.deviceCalls
	fake.mu.Unlock()
	if deviceCalls != 1 {
		t.Fatalf("短 TTL 内重复读取 Console 不应重复请求 Tailscale 设备: calls=%d", deviceCalls)
	}
}

func TestAdminMonitoringRequiresAdminSession(t *testing.T) {
	service := NewService(testServiceConfig(), NewStore(), &fakeTailscale{}, nil)
	for _, path := range []string{
		"/v1/admin/console",
		"/v1/admin/logs/recent",
		"/v1/admin/console/stream",
		"/v1/admin/logs/stream",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("未登录访问 %s 返回 %d: %s", path, response.Code, response.Body.String())
		}
	}
}

func TestAdminEventStreamStopsAfterSessionRevocation(t *testing.T) {
	store := NewStore()
	defer store.Close()
	service := NewService(testServiceConfig(), store, &fakeTailscale{}, nil)
	authentication, _ := installTestAdminAndCredential(t, service, store)
	server := httptest.NewServer(service.Handler())
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/admin/logs/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	authorizeTestAdmin(request, authentication, false)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("建立管理员日志流返回 %d", response.StatusCode)
	}
	initial := make([]byte, 128)
	if _, err := response.Body.Read(initial); err != nil {
		t.Fatalf("读取日志流初始响应: %v", err)
	}

	raw, err := base64RawURLDecode(authentication.CookieValue)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAdminSession(sha256Bytes(raw)); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan string, 1)
	go func() {
		body, _ := io.ReadAll(response.Body)
		readDone <- string(body)
	}()
	service.events.publishLog(LogEvent{Message: "after-revocation"})

	select {
	case body := <-readDone:
		if !strings.Contains(body, "event: auth-expired") {
			t.Fatalf("session 失效后日志流没有发出 auth-expired: %q", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session 失效后日志流仍未关闭")
	}
}

func TestAdminEventStreamUsesLastEventIDBeforeAfter(t *testing.T) {
	store := NewStore()
	defer store.Close()
	service := NewService(testServiceConfig(), store, &fakeTailscale{}, nil)
	authentication, _ := installTestAdminAndCredential(t, service, store)
	first := service.events.publishLog(LogEvent{Message: "first"})
	second := service.events.publishLog(LogEvent{Message: "second"})
	server := httptest.NewServer(service.Handler())
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/admin/logs/stream?after=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	authorizeTestAdmin(request, authentication, false)
	request.Header.Set("Last-Event-ID", strconv.FormatUint(first.Sequence, 10))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("建立带断点的管理员日志流返回 %d", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)
	foundSecond := false
	for index := 0; index < 16; index++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("读取日志流断点事件: %v", err)
		}
		if strings.TrimSpace(line) == "id: "+strconv.FormatUint(second.Sequence, 10) {
			foundSecond = true
			break
		}
		if strings.TrimSpace(line) == "id: "+strconv.FormatUint(first.Sequence, 10) {
			t.Fatalf("Last-Event-ID 被 after=0 覆盖，错误重放 first 事件")
		}
	}
	cancel()
	if !foundSecond {
		t.Fatalf("没有按 Last-Event-ID=%d 重放 second=%d", first.Sequence, second.Sequence)
	}
}

func TestSessionLogsAcceptTheDocumentedMaximumBatch(t *testing.T) {
	store := NewStore()
	defer store.Close()
	token, tokenHash, err := newSecretToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.PutSession(Session{
		ID: "large-log-batch", TokenHash: tokenHash, AuthKeyID: "key-large-log-batch",
		ProvisioningName: "pinnode-large-log-batch", Config: DefaultSessionConfig(),
		CreatedAt: now, UpdatedAt: now, Status: SessionActive,
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(testServiceConfig(), store, &fakeTailscale{}, nil)
	requestLogs := make([]clientLogEntry, maxClientLogEntries)
	for index := range requestLogs {
		requestLogs[index] = clientLogEntry{
			Timestamp: "2026-08-29T00:00:00Z",
			Level:     "INFO",
			Component: "test",
			Message:   strings.Repeat("x", maxClientLogMessage),
		}
	}
	body, err := json.Marshal(sessionLogsRequest{Logs: requestLogs})
	if err != nil {
		t.Fatal(err)
	}
	request := newJSONRequest(http.MethodPost, "/v1/sessions/large-log-batch/logs", string(body))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("文档声明的最大日志批量无法上传: bodyBytes=%d status=%d body=%s", len(body), response.Code, response.Body.String())
	}
}

func getAdminConsole(t *testing.T, service *Service, authentication testAdminAuthentication) adminConsoleTestResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/admin/console", nil)
	authorizeTestAdmin(request, authentication, false)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("读取 Console 返回 %d: %s", response.Code, response.Body.String())
	}
	var result adminConsoleTestResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
