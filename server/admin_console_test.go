package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
