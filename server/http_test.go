package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTailscale struct {
	mu                 sync.Mutex
	device             Device
	tailscaleIPs       map[string]string
	ipCalls            map[string]int
	routes             map[string][]string
	routeCalls         map[string]int
	deleted            []string
	deletedAuthKeys    []string
	ephemeralRequested []bool
	accessTokens       []string
	oauthCalls         int
	deviceCalls        int
	oauthToken         OAuthAccessToken
	oauthErr           error
	createAuthKeyErr   error
	setDeviceIPv4Err   error
}

func fakeTailscaleKey(parts ...string) string {
	return "tskey-" + strings.Join(parts, "-")
}

func (f *fakeTailscale) ExchangeOAuthToken(context.Context, string, string) (OAuthAccessToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.oauthCalls++
	if f.oauthErr != nil {
		return OAuthAccessToken{}, f.oauthErr
	}
	if f.oauthToken.Token != "" {
		return f.oauthToken, nil
	}
	return OAuthAccessToken{
		Token: fakeTailscaleKey("oauth", "test"), ExpiresAt: time.Now().Add(time.Hour),
		Scopes: []string{"auth_keys", "devices:core", "devices:routes"},
	}, nil
}

func (f *fakeTailscale) ValidateCredential(context.Context, string) error { return nil }

func (f *fakeTailscale) CreateAuthKey(_ context.Context, accessToken string, _ time.Duration, ephemeral bool) (AuthKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createAuthKeyErr != nil {
		return AuthKey{}, f.createAuthKeyErr
	}
	f.ephemeralRequested = append(f.ephemeralRequested, ephemeral)
	f.accessTokens = append(f.accessTokens, accessToken)
	return AuthKey{Secret: fakeTailscaleKey("test"), ID: "k-test"}, nil
}

func (f *fakeTailscale) DeleteAuthKey(_ context.Context, _ string, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedAuthKeys = append(f.deletedAuthKeys, id)
	return nil
}

func (f *fakeTailscale) GetDevice(context.Context, string, string) (Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deviceCalls++
	return f.device, nil
}

func (f *fakeTailscale) SetDeviceIPv4(_ context.Context, _ string, id, ipv4 string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setDeviceIPv4Err != nil {
		return f.setDeviceIPv4Err
	}
	if f.tailscaleIPs == nil {
		f.tailscaleIPs = make(map[string]string)
	}
	if f.ipCalls == nil {
		f.ipCalls = make(map[string]int)
	}
	f.tailscaleIPs[id] = ipv4
	f.ipCalls[id]++
	return nil
}

func (f *fakeTailscale) SetDeviceRoutes(_ context.Context, _ string, id string, routes []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.routes == nil {
		f.routes = make(map[string][]string)
	}
	if f.routeCalls == nil {
		f.routeCalls = make(map[string]int)
	}
	f.routes[id] = append([]string(nil), routes...)
	f.routeCalls[id]++
	return nil
}

func (f *fakeTailscale) DeleteDevice(_ context.Context, _ string, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

type testAdminAuthentication struct {
	CookieValue string
	CSRFToken   string
}

func testServiceConfig() Config {
	return Config{
		CodePepper:       "test-pepper-used-only-by-unit-tests",
		CredentialKey:    []byte("0123456789abcdef0123456789abcdef"),
		CodeTTL:          5 * time.Minute,
		AdminSessionTTL:  time.Hour,
		PoWDifficulty:    16,
		AllowRemoteSetup: true,
	}
}

func installTestAdminAndCredential(t *testing.T, service *Service, store *Store) (testAdminAuthentication, string) {
	t.Helper()
	now := time.Now()
	passwordHash, err := hashPassword("unit test administrator password")
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateAdmin("admin", passwordHash, now)
	if err != nil || !created {
		t.Fatalf("创建测试管理员: created=%v err=%v", created, err)
	}
	token, err := newURLToken(32)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64RawURLDecode(token)
	csrfToken := "test-csrf-token"
	if err := store.PutAdminSession(AdminSession{
		TokenHash: sha256Bytes(raw), AdminID: 1, CSRFToken: csrfToken,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	credentialID := "test-credential"
	ciphertext, err := service.cipher.Seal(credentialID, fakeTailscaleKey("api", "test"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTailscaleCredential(TailscaleCredential{
		ID: credentialID, Name: "测试 Tailnet", Ciphertext: ciphertext,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return testAdminAuthentication{CookieValue: token, CSRFToken: csrfToken}, credentialID
}

func authorizeTestAdmin(request *http.Request, authentication testAdminAuthentication, write bool) {
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: authentication.CookieValue})
	if write {
		request.Header.Set("X-CSRF-Token", authentication.CSRFToken)
	}
}

func newJSONRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func newSessionStartRequest(body string) *http.Request {
	request := newJSONRequest(http.MethodPost, "/v1/sessions", body)
	request.Header.Set("Idempotency-Key", "unit-test-session-start-key")
	return request
}

func TestSessionProvisionAndCleanup(t *testing.T) {
	fake := &fakeTailscale{device: Device{NodeID: "n-test", Tags: []string{managedDeviceTag}}}
	config := testServiceConfig()
	store := NewStore()
	service := NewService(config, store, fake, nil)
	adminAuthentication, credentialID := installTestAdminAndCredential(t, service, store)

	codeRequest := newJSONRequest(
		http.MethodPost,
		"/v1/pairing-codes",
		`{"credentialId":"`+credentialID+`","config":{"networkMode":"default","exitPolicy":{"onAppClose":true}}}`,
	)
	authorizeTestAdmin(codeRequest, adminAuthentication, true)
	codeResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(codeResponse, codeRequest)
	if codeResponse.Code != http.StatusCreated {
		t.Fatalf("申请 code 返回 %d: %s", codeResponse.Code, codeResponse.Body.String())
	}
	var codeBody struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(codeResponse.Body.Bytes(), &codeBody); err != nil {
		t.Fatal(err)
	}

	startBody := `{"code":"` + codeBody.Code + `","gatewayRoute":"192.168.1.1/32"}`
	startRequest := newSessionStartRequest(startBody)
	startResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(startResponse, startRequest)
	if startResponse.Code != http.StatusCreated {
		t.Fatalf("启动会话返回 %d: %s", startResponse.Code, startResponse.Body.String())
	}
	var startResult struct {
		SessionID            string `json:"sessionId"`
		SessionToken         string `json:"sessionToken"`
		AuthKey              string `json:"authKey"`
		ProvisioningHostname string `json:"provisioningHostname"`
		SyncIntervalSeconds  int64  `json:"syncIntervalSeconds"`
	}
	if err := json.Unmarshal(startResponse.Body.Bytes(), &startResult); err != nil {
		t.Fatal(err)
	}
	if startResult.SessionID == "" || startResult.SessionToken == "" || startResult.AuthKey != fakeTailscaleKey("test") {
		t.Fatalf("会话响应缺少必要字段: %+v", startResult)
	}
	if startResult.SyncIntervalSeconds <= 0 {
		t.Fatalf("会话没有下发同步间隔: %+v", startResult)
	}
	fake.mu.Lock()
	fake.device.Hostname = startResult.ProvisioningHostname
	fake.device.Created = time.Now()
	fake.mu.Unlock()
	fake.mu.Lock()
	if len(fake.ephemeralRequested) != 1 || fake.ephemeralRequested[0] {
		fake.mu.Unlock()
		t.Fatalf("默认节点不应使用 ephemeral auth key: %v", fake.ephemeralRequested)
	}
	if len(fake.accessTokens) != 1 || fake.accessTokens[0] != fakeTailscaleKey("api", "test") {
		fake.mu.Unlock()
		t.Fatalf("供应未使用所选的加密凭据: %v", fake.accessTokens)
	}
	fake.mu.Unlock()

	attachBody := `{"nodeId":"n-test"}`
	attachRequest := newJSONRequest(http.MethodPost, "/v1/sessions/"+startResult.SessionID+"/device", attachBody)
	attachRequest.Header.Set("Authorization", "Bearer "+startResult.SessionToken)
	attachResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(attachResponse, attachRequest)
	if attachResponse.Code != http.StatusOK {
		t.Fatalf("绑定设备返回 %d: %s", attachResponse.Code, attachResponse.Body.String())
	}
	syncRequest := newJSONRequest(
		http.MethodPost, "/v1/sessions/"+startResult.SessionID+"/sync",
		`{"protocolVersion":1,"appliedConfigRevision":1,"clientVersion":"test","clientCapabilities":["session-sync-v1"]}`,
	)
	syncRequest.Header.Set("Authorization", "Bearer "+startResult.SessionToken)
	syncResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(syncResponse, syncRequest)
	if syncResponse.Code != http.StatusOK {
		t.Fatalf("会话同步返回 %d: %s", syncResponse.Code, syncResponse.Body.String())
	}

	stopRequest := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+startResult.SessionID+"/stop", nil)
	stopRequest.Header.Set("Authorization", "Bearer "+startResult.SessionToken)
	stopResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(stopResponse, stopRequest)
	if stopResponse.Code != http.StatusOK {
		t.Fatalf("停止会话返回 %d: %s", stopResponse.Code, stopResponse.Body.String())
	}
	historyRequest := httptest.NewRequest(http.MethodGet, "/v1/sessions?limit=10", nil)
	authorizeTestAdmin(historyRequest, adminAuthentication, false)
	historyResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(historyResponse, historyRequest)
	if historyResponse.Code != http.StatusOK ||
		!strings.Contains(historyResponse.Body.String(), startResult.SessionID) ||
		!strings.Contains(historyResponse.Body.String(), `"status":"stopped"`) {
		t.Fatalf("历史会话未保留停止记录: %d %s", historyResponse.Code, historyResponse.Body.String())
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.routes["n-test"]; len(got) != 0 {
		t.Fatalf("清理后路由仍存在: %v", got)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != "n-test" {
		t.Fatalf("删除设备调用异常: %v", fake.deleted)
	}
}

func TestAttachRejectsDeviceWithoutProvisioningChallenge(t *testing.T) {
	fake := &fakeTailscale{
		device: Device{
			NodeID:   "n-existing",
			Hostname: "another-pinnode",
			Created:  time.Now().Add(-time.Hour),
			Tags:     []string{managedDeviceTag},
		},
	}
	config := testServiceConfig()
	store := NewStore()
	service := NewService(config, store, fake, nil)
	adminAuthentication, credentialID := installTestAdminAndCredential(t, service, store)
	code := createTestPairingCode(t, service, adminAuthentication, credentialID)

	startRequest := newSessionStartRequest(`{"code":"` + code + `","gatewayRoute":"192.168.1.1/32"}`)
	startResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(startResponse, startRequest)
	if startResponse.Code != http.StatusCreated {
		t.Fatalf("启动会话返回 %d: %s", startResponse.Code, startResponse.Body.String())
	}
	var started struct {
		ID    string `json:"sessionId"`
		Token string `json:"sessionToken"`
	}
	if err := json.Unmarshal(startResponse.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	attachRequest := newJSONRequest(
		http.MethodPost,
		"/v1/sessions/"+started.ID+"/device",
		`{"nodeId":"n-existing"}`,
	)
	attachRequest.Header.Set("Authorization", "Bearer "+started.Token)
	attachResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(attachResponse, attachRequest)
	if attachResponse.Code != http.StatusForbidden {
		t.Fatalf("既有同标签设备被错误绑定: %d %s", attachResponse.Code, attachResponse.Body.String())
	}
}

func createTestPairingCode(t *testing.T, service *Service, authentication testAdminAuthentication, credentialID string) string {
	t.Helper()
	request := newJSONRequest(
		http.MethodPost, "/v1/pairing-codes",
		`{"credentialId":"`+credentialID+`"}`,
	)
	authorizeTestAdmin(request, authentication, true)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("申请 code 返回 %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Code
}

func TestConfiguredRoutesAndPrefsFlowFromPairingCodeToSession(t *testing.T) {
	fake := &fakeTailscale{device: Device{NodeID: "n-configured", Tags: []string{managedDeviceTag}}}
	config := testServiceConfig()
	store := NewStore()
	service := NewService(config, store, fake, nil)
	adminAuthentication, credentialID := installTestAdminAndCredential(t, service, store)

	codeRequest := newJSONRequest(http.MethodPost, "/v1/pairing-codes", `{"credentialId":"`+credentialID+`","config":{"vpnEnabled":false,"acceptRoutes":false,"acceptDNS":false,"tailscaleIp":"100.64.0.42","subnetRouter":true,"autoGatewayRoute":false,"advertiseRoutes":["192.168.1.0/24"]}}`)
	authorizeTestAdmin(codeRequest, adminAuthentication, true)
	codeResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(codeResponse, codeRequest)
	if codeResponse.Code != http.StatusCreated {
		t.Fatalf("带配置申请 code 返回 %d: %s", codeResponse.Code, codeResponse.Body.String())
	}
	var codeBody struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(codeResponse.Body.Bytes(), &codeBody); err != nil {
		t.Fatal(err)
	}

	startRequest := newSessionStartRequest(`{"code":"` + codeBody.Code + `","gatewayRoute":"192.168.1.1/32"}`)
	startResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(startResponse, startRequest)
	if startResponse.Code != http.StatusCreated {
		t.Fatalf("带配置启动会话返回 %d: %s", startResponse.Code, startResponse.Body.String())
	}
	var startResult struct {
		SessionID            string        `json:"sessionId"`
		Token                string        `json:"sessionToken"`
		Config               SessionConfig `json:"config"`
		Routes               []string      `json:"routes"`
		ProvisioningHostname string        `json:"provisioningHostname"`
		SyncIntervalSeconds  int64         `json:"syncIntervalSeconds"`
	}
	if err := json.Unmarshal(startResponse.Body.Bytes(), &startResult); err != nil {
		t.Fatal(err)
	}
	if startResult.Config.VPNEnabled || startResult.Config.AcceptRoutes || startResult.Config.AcceptDNS {
		t.Fatalf("客户端配置未从 pairing code 传递: %+v", startResult.Config)
	}
	if startResult.Config.TailscaleIP != "100.64.0.42" {
		t.Fatalf("客户端 Tailscale IP 未从 pairing code 传递: %q", startResult.Config.TailscaleIP)
	}
	if len(startResult.Routes) != 1 || startResult.Routes[0] != "192.168.1.0/24" {
		t.Fatalf("实际路由错误: %v", startResult.Routes)
	}
	if startResult.SyncIntervalSeconds <= 0 {
		t.Fatalf("长期会话也必须下发同步间隔: %d", startResult.SyncIntervalSeconds)
	}
	fake.mu.Lock()
	fake.device.Hostname = startResult.ProvisioningHostname
	fake.device.Created = time.Now()
	fake.mu.Unlock()

	attachRequest := newJSONRequest(http.MethodPost, "/v1/sessions/"+startResult.SessionID+"/device", `{"nodeId":"n-configured"}`)
	attachRequest.Header.Set("Authorization", "Bearer "+startResult.Token)
	attachResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(attachResponse, attachRequest)
	if attachResponse.Code != http.StatusOK {
		t.Fatalf("绑定配置设备返回 %d: %s", attachResponse.Code, attachResponse.Body.String())
	}
	retryRequest := newJSONRequest(http.MethodPost, "/v1/sessions/"+startResult.SessionID+"/device", `{"nodeId":"n-configured"}`)
	retryRequest.Header.Set("Authorization", "Bearer "+startResult.Token)
	retryResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(retryResponse, retryRequest)
	if retryResponse.Code != http.StatusOK {
		t.Fatalf("重复绑定设备返回 %d: %s", retryResponse.Code, retryResponse.Body.String())
	}
	syncRequest := newJSONRequest(http.MethodPost, "/v1/sessions/"+startResult.SessionID+"/sync", `{"protocolVersion":1,"appliedConfigRevision":1,"clientVersion":"test","clientCapabilities":["session-sync-v1"]}`)
	syncRequest.Header.Set("Authorization", "Bearer "+startResult.Token)
	syncResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(syncResponse, syncRequest)
	if syncResponse.Code != http.StatusOK {
		t.Fatalf("长期会话同步失败: %d %s", syncResponse.Code, syncResponse.Body.String())
	}
	stored, ok, err := store.GetSession(startResult.SessionID)
	if err != nil || !ok || !stored.SyncDeadline.IsZero() {
		t.Fatalf("长期会话错误建立同步租约: ok=%v deadline=%v err=%v", ok, stored.SyncDeadline, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.routes["n-configured"]; len(got) != 1 || got[0] != "192.168.1.0/24" {
		t.Fatalf("控制面收到的路由错误: %v", got)
	}
	if fake.routeCalls["n-configured"] != 2 {
		t.Fatalf("重复绑定没有重新确认控制面路由: %d", fake.routeCalls["n-configured"])
	}
	if fake.tailscaleIPs["n-configured"] != "100.64.0.42" || fake.ipCalls["n-configured"] != 2 {
		t.Fatalf("重复绑定没有重新确认控制面 IP: ip=%q calls=%d", fake.tailscaleIPs["n-configured"], fake.ipCalls["n-configured"])
	}
}

func TestAdminPageIsEmbedded(t *testing.T) {
	service := NewService(testServiceConfig(), NewStore(), &fakeTailscale{}, nil)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "创建管理员账号") ||
		!strings.Contains(response.Body.String(), "Tailscale 管理凭据") ||
		!strings.Contains(response.Body.String(), "Console 状态") ||
		!strings.Contains(response.Body.String(), "实时日志") ||
		!strings.Contains(response.Body.String(), "/v1/admin/console/stream") ||
		!strings.Contains(response.Body.String(), "/v1/admin/logs/stream") ||
		!strings.Contains(response.Body.String(), `id="tailscale-ip"`) ||
		!strings.Contains(response.Body.String(), managedDeviceTag) ||
		!strings.Contains(response.Body.String(), "https://console.tailscale.com/admin/settings/trust-credentials/add") ||
		!strings.Contains(response.Body.String(), `id="credential-add-form" class="field-row" hidden`) ||
		!strings.Contains(response.Body.String(), `id="sidebar-toggle" class="brand"`) ||
		!strings.Contains(response.Body.String(), `data-nav-group="config"`) ||
		!strings.Contains(response.Body.String(), `class="nav-label">生成授权码</span>`) ||
		!strings.Contains(response.Body.String(), `class="nav-submenu" data-nav-submenu="config"`) ||
		!strings.Contains(response.Body.String(), `data-nav-group="console"`) ||
		!strings.Contains(response.Body.String(), `class="nav-label">控制台</span>`) ||
		!strings.Contains(response.Body.String(), `data-nav-group="logs"`) ||
		!strings.Contains(response.Body.String(), `class="nav-label">日志</span>`) ||
		!strings.Contains(response.Body.String(), "/assets/mark.svg") ||
		strings.Contains(response.Body.String(), "本地 PoW 验证 · 不依赖第三方验证码服务") ||
		strings.Contains(response.Body.String(), "admin-token") ||
		strings.Contains(response.Body.String(), "{{CSP_NONCE}}") ||
		strings.Contains(response.Body.String(), "{{MANAGED_DEVICE_TAG}}") ||
		strings.Contains(response.Body.String(), "{{BUILD_BADGE}}") ||
		!strings.Contains(response.Header().Get("Content-Security-Policy"), "'nonce-") ||
		!strings.Contains(response.Header().Get("Content-Security-Policy"), "img-src 'self'") ||
		strings.Contains(response.Header().Get("Content-Security-Policy"), "unsafe-inline") {
		t.Fatalf("管理页面没有正确嵌入: code=%d body=%q", response.Code, response.Body.String())
	}
	if hasBadge := strings.Contains(response.Body.String(), `class="build-badge">DEBUG</span>`); hasBadge != debugBuild {
		t.Fatalf("管理页面构建标识错误: debug=%v badge=%v", debugBuild, hasBadge)
	}
	markRequest := httptest.NewRequest(http.MethodGet, "/assets/mark.svg", nil)
	markResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(markResponse, markRequest)
	if markResponse.Code != http.StatusOK ||
		markResponse.Header().Get("Content-Type") != "image/svg+xml" ||
		markResponse.Header().Get("Cache-Control") != adminMarkCacheControl ||
		!strings.Contains(markResponse.Body.String(), "<circle") {
		t.Fatalf("管理页面图标没有正确嵌入或缓存策略错误: code=%d contentType=%q cacheControl=%q", markResponse.Code, markResponse.Header().Get("Content-Type"), markResponse.Header().Get("Cache-Control"))
	}
}

func TestDiagnosticRouteRedactsSessionID(t *testing.T) {
	for input, want := range map[string]string{
		"/v1/sessions/secret-session/device": "/v1/sessions/:id/device",
		"/v1/sessions/secret-session/sync":   "/v1/sessions/:id/sync",
		"/v1/sessions/secret-session/stop":   "/v1/sessions/:id/stop",
		"/v1/sessions/secret-session":        "/v1/sessions/:id",
		"/healthz":                           "/healthz",
	} {
		if got := diagnosticRoute(input); got != want {
			t.Errorf("diagnosticRoute(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestDiagnosticIdentifierIsStableAndRedacted(t *testing.T) {
	const identifier = "secret-session-id"
	first := diagnosticIdentifier(identifier)
	second := diagnosticIdentifier(identifier)
	if first != second || len(first) != 12 || strings.Contains(first, identifier) {
		t.Fatalf("diagnosticIdentifier()=%q is not a stable redacted reference", first)
	}
	if first == diagnosticIdentifier("another-session-id") {
		t.Fatal("different identifiers produced the same diagnostic reference")
	}
}

func TestAPIMetaAndStructuredErrors(t *testing.T) {
	service := NewService(testServiceConfig(), NewStore(), &fakeTailscale{}, nil)
	metaRequest := httptest.NewRequest(http.MethodGet, "/v1/meta", nil)
	metaResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(metaResponse, metaRequest)
	if metaResponse.Code != http.StatusOK ||
		!strings.Contains(metaResponse.Body.String(), `"protocolVersion":1`) ||
		!strings.Contains(metaResponse.Body.String(), `"session-sync-v1"`) ||
		!strings.Contains(metaResponse.Body.String(), `"client-state-report-v1"`) ||
		!strings.Contains(metaResponse.Body.String(), `"client-logs-v1"`) {
		t.Fatalf("API meta 响应错误: %d %s", metaResponse.Code, metaResponse.Body.String())
	}

	invalidRequest := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{}`))
	invalidResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(invalidResponse, invalidRequest)
	var envelope apiErrorResponse
	if err := json.Unmarshal(invalidResponse.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if invalidResponse.Code != http.StatusUnsupportedMediaType ||
		envelope.Error.Code != "content_type_invalid" || envelope.Error.Retryable ||
		envelope.RequestID == "" || envelope.RequestID != invalidResponse.Header().Get("X-Request-ID") {
		t.Fatalf("结构化错误响应错误: code=%d header=%q body=%s", invalidResponse.Code, invalidResponse.Header().Get("X-Request-ID"), invalidResponse.Body.String())
	}
}

func TestJSONRequestsRejectUnknownTrailingAndOversizedBodies(t *testing.T) {
	service := NewService(testServiceConfig(), NewStore(), &fakeTailscale{}, nil)
	for name, body := range map[string]string{
		"unknown":  `{"code":"123456","unknown":true}`,
		"trailing": `{"code":"123456"}{}`,
		"oversized": `{"code":"123456","gatewayRoute":"` +
			strings.Repeat("x", maxJSONBodyBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := newSessionStartRequest(body)
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest ||
				!strings.Contains(response.Body.String(), `"code":"json_invalid"`) {
				t.Fatalf("严格 JSON 校验返回 %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestSessionStartIsIdempotent(t *testing.T) {
	fake := &fakeTailscale{}
	store := NewStore()
	service := NewService(testServiceConfig(), store, fake, nil)
	authentication, credentialID := installTestAdminAndCredential(t, service, store)
	code := createTestPairingCode(t, service, authentication, credentialID)
	body := `{"code":"` + code + `","gatewayRoute":"192.168.1.1/32"}`

	firstRequest := newSessionStartRequest(body)
	firstResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(firstResponse, firstRequest)
	if firstResponse.Code != http.StatusCreated {
		t.Fatalf("首次创建会话返回 %d: %s", firstResponse.Code, firstResponse.Body.String())
	}
	secondRequest := newSessionStartRequest(body)
	secondResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(secondResponse, secondRequest)
	if secondResponse.Code != http.StatusCreated || secondResponse.Header().Get("Idempotent-Replayed") != "true" ||
		secondResponse.Body.String() != firstResponse.Body.String() {
		t.Fatalf("幂等重放不一致: first=%s second=%s", firstResponse.Body.String(), secondResponse.Body.String())
	}
	fake.mu.Lock()
	createdKeys := len(fake.ephemeralRequested)
	fake.mu.Unlock()
	if createdKeys != 1 {
		t.Fatalf("幂等重放重复创建了 auth key: %d", createdKeys)
	}

	conflictRequest := newSessionStartRequest(`{"code":"` + code + `","gatewayRoute":"192.168.1.2/32"}`)
	conflictResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(conflictResponse, conflictRequest)
	if conflictResponse.Code != http.StatusConflict ||
		!strings.Contains(conflictResponse.Body.String(), `"code":"idempotency_key_conflict"`) {
		t.Fatalf("不同请求复用幂等键未被拒绝: %d %s", conflictResponse.Code, conflictResponse.Body.String())
	}
}

func TestUpstreamFailureDoesNotConsumePairingCode(t *testing.T) {
	fake := &fakeTailscale{createAuthKeyErr: errors.New("temporary upstream failure")}
	store := NewStore()
	service := NewService(testServiceConfig(), store, fake, nil)
	authentication, credentialID := installTestAdminAndCredential(t, service, store)
	code := createTestPairingCode(t, service, authentication, credentialID)
	body := `{"code":"` + code + `","gatewayRoute":"192.168.1.1/32"}`

	failedRequest := newSessionStartRequest(body)
	failedResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(failedResponse, failedRequest)
	if failedResponse.Code != http.StatusBadGateway {
		t.Fatalf("上游失败返回 %d: %s", failedResponse.Code, failedResponse.Body.String())
	}
	fake.mu.Lock()
	fake.createAuthKeyErr = nil
	fake.mu.Unlock()
	retryRequest := newSessionStartRequest(body)
	retryResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(retryResponse, retryRequest)
	if retryResponse.Code != http.StatusCreated {
		t.Fatalf("上游恢复后同一 PIN 不能重试: %d %s", retryResponse.Code, retryResponse.Body.String())
	}
}

func TestSessionSyncReturnsRevisionedConfig(t *testing.T) {
	store := NewStore()
	token, tokenHash, err := newSecretToken()
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultSessionConfig()
	config.NetworkMode = NetworkModeDefault
	now := time.Now()
	if err := store.PutSession(Session{
		ID: "revisioned", TokenHash: tokenHash, ProvisioningName: "pinnode-revisioned",
		Config: config, ConfigRevision: 2, AppliedConfigRevision: 1,
		CreatedAt: now, Status: SessionActive, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(testServiceConfig(), store, &fakeTailscale{}, nil)
	request := newJSONRequest(http.MethodPost, "/v1/sessions/revisioned/sync", `{"protocolVersion":1,"appliedConfigRevision":1,"clientVersion":"test","clientCapabilities":["session-sync-v1"]}`)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"desiredConfig":{"revision":2`) ||
		!strings.Contains(response.Body.String(), `"syncDeadline":null`) ||
		!strings.Contains(response.Body.String(), `"routes":[]`) {
		t.Fatalf("revisioned sync 响应错误: %d %s", response.Code, response.Body.String())
	}
}
