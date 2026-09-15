package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type adminTestSession struct {
	ID       string `json:"sessionId"`
	Token    string `json:"sessionToken"`
	Hostname string `json:"provisioningHostname"`
}

func createConfiguredPairingCode(t *testing.T, service *Service, authentication testAdminAuthentication, credentialID, config string) string {
	t.Helper()
	request := newJSONRequest(
		http.MethodPost,
		"/v1/pairing-codes",
		`{"credentialId":"`+credentialID+`","config":`+config+`}`,
	)
	authorizeTestAdmin(request, authentication, true)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("申请配置 pairing code 返回 %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Code == "" {
		t.Fatal("配置 pairing code 没有返回 code")
	}
	return result.Code
}

func startAdminTestSession(t *testing.T, service *Service, code string, gatewayRoute string) adminTestSession {
	t.Helper()
	request := newSessionStartRequest(`{"code":"` + code + `","gatewayRoute":"` + gatewayRoute + `"}`)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("启动测试会话返回 %d: %s", response.Code, response.Body.String())
	}
	var result adminTestSession
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID == "" || result.Token == "" || result.Hostname == "" {
		t.Fatalf("测试会话响应不完整: %+v", result)
	}
	return result
}

func adminPatchRequest(sessionID, body string, authentication testAdminAuthentication, write bool) *http.Request {
	request := newJSONRequest(http.MethodPatch, "/v1/sessions/"+sessionID+"/config", body)
	authorizeTestAdmin(request, authentication, write)
	return request
}

func TestAdminSessionConfigAuthRevisionAndAttachSnapshot(t *testing.T) {
	fake := &fakeTailscale{}
	store := NewStore()
	defer store.Close()
	service := NewService(testServiceConfig(), store, fake, nil)
	authentication, credentialID := installTestAdminAndCredential(t, service, store)
	code := createTestPairingCode(t, service, authentication, credentialID)
	started := startAdminTestSession(t, service, code, "192.168.1.1/32")

	unauthorized := adminPatchRequest(started.ID, `{"expectedRevision":1,"config":{"vpnEnabled":false}}`, authentication, false)
	unauthorized.Header.Del("Authorization")
	unauthorizedResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusForbidden ||
		!strings.Contains(unauthorizedResponse.Body.String(), `"code":"csrf_invalid"`) {
		t.Fatalf("缺少 CSRF 的管理员配置请求未被拒绝: %d %s", unauthorizedResponse.Code, unauthorizedResponse.Body.String())
	}

	bearerOnly := adminPatchRequest(started.ID, `{"expectedRevision":1,"config":{"vpnEnabled":false}}`, authentication, false)
	bearerOnly.Header.Del("Cookie")
	bearerOnly.Header.Set("Authorization", "Bearer "+started.Token)
	bearerOnlyResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(bearerOnlyResponse, bearerOnly)
	if bearerOnlyResponse.Code != http.StatusUnauthorized ||
		!strings.Contains(bearerOnlyResponse.Body.String(), `"code":"admin_auth_required"`) {
		t.Fatalf("session bearer 被错误地当作管理员配置认证: %d %s", bearerOnlyResponse.Code, bearerOnlyResponse.Body.String())
	}

	originInvalid := adminPatchRequest(started.ID, `{"expectedRevision":1,"config":{"vpnEnabled":false}}`, authentication, true)
	originInvalid.Header.Set("Origin", "https://attacker.example")
	originResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(originResponse, originInvalid)
	if originResponse.Code != http.StatusForbidden ||
		!strings.Contains(originResponse.Body.String(), `"code":"origin_invalid"`) {
		t.Fatalf("跨源管理员配置请求未被拒绝: %d %s", originResponse.Code, originResponse.Body.String())
	}

	update := adminPatchRequest(started.ID, `{"expectedRevision":1,"config":{"vpnEnabled":false,"exitPolicy":{"onAppClose":true}}}`, authentication, true)
	updateResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(updateResponse, update)
	if updateResponse.Code != http.StatusOK ||
		!strings.Contains(updateResponse.Body.String(), `"configRevision":2`) ||
		!strings.Contains(updateResponse.Body.String(), `"applyState":"pending_client_attach"`) {
		t.Fatalf("provisioning 配置更新响应错误: %d %s", updateResponse.Code, updateResponse.Body.String())
	}
	var snapshot struct {
		Config SessionConfig `json:"config"`
	}
	if err := json.Unmarshal(updateResponse.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Config.VPNEnabled || !snapshot.Config.ExitPolicy.OnAppClose {
		t.Fatalf("partial patch 没有保留显式 false/嵌套字段: %+v", snapshot.Config)
	}

	stale := adminPatchRequest(started.ID, `{"expectedRevision":1,"config":{"acceptDNS":false}}`, authentication, true)
	staleResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(staleResponse, stale)
	if staleResponse.Code != http.StatusConflict ||
		!strings.Contains(staleResponse.Body.String(), `"code":"config_revision_conflict"`) ||
		!strings.Contains(staleResponse.Body.String(), `"configRevision":2`) {
		t.Fatalf("旧 revision 未返回安全冲突快照: %d %s", staleResponse.Code, staleResponse.Body.String())
	}

	fake.mu.Lock()
	fake.device = Device{NodeID: "n-attach-admin", Hostname: started.Hostname, Created: time.Now(), Tags: []string{managedDeviceTag}}
	fake.mu.Unlock()
	attach := newJSONRequest(http.MethodPost, "/v1/sessions/"+started.ID+"/device", `{"nodeId":"n-attach-admin"}`)
	attach.Header.Set("Authorization", "Bearer "+started.Token)
	attachResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(attachResponse, attach)
	if attachResponse.Code != http.StatusOK ||
		!strings.Contains(attachResponse.Body.String(), `"configRevision":2`) ||
		!strings.Contains(attachResponse.Body.String(), `"vpnEnabled":false`) {
		t.Fatalf("attach 没有返回管理员更新后的完整配置: %d %s", attachResponse.Code, attachResponse.Body.String())
	}
}

func TestAdminSessionStopIsIdempotentAndKeepsHistory(t *testing.T) {
	fake := &fakeTailscale{}
	store := NewStore()
	defer store.Close()
	service := NewService(testServiceConfig(), store, fake, nil)
	authentication, credentialID := installTestAdminAndCredential(t, service, store)
	started := startAdminTestSession(t, service, createTestPairingCode(t, service, authentication, credentialID), "192.168.1.1/32")

	stop := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/admin/sessions/"+started.ID+"/stop", nil)
		authorizeTestAdmin(request, authentication, true)
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, request)
		return response
	}
	first := stop()
	if first.Code != http.StatusOK ||
		!strings.Contains(first.Body.String(), `"sessionId":"`+started.ID+`"`) ||
		!strings.Contains(first.Body.String(), `"status":"stopped"`) {
		t.Fatalf("管理员首次停止会话失败: %d %s", first.Code, first.Body.String())
	}
	second := stop()
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"status":"already-stopped"`) {
		t.Fatalf("重复管理员停止没有返回 already-stopped: %d %s", second.Code, second.Body.String())
	}
	stored, ok, err := store.GetSession(started.ID)
	if err != nil || !ok || stored.Status != SessionStopped || stored.StopReason != "admin_stop" {
		t.Fatalf("管理员停止没有保留正确历史状态: %+v ok=%v err=%v", stored, ok, err)
	}
	fake.mu.Lock()
	deletedKeys := append([]string{}, fake.deletedAuthKeys...)
	fake.mu.Unlock()
	if len(deletedKeys) != 1 {
		t.Fatalf("重复管理员 stop 不应重复撤销 auth key: %v", deletedKeys)
	}

	console := getAdminConsole(t, service, authentication)
	if len(console.History) != 1 || console.History[0].ID != started.ID {
		t.Fatalf("停止会话未进入 Console 历史: %+v", console.History)
	}
}

func TestAdminActiveConfigUpdatesRoutesAndDoesNotCommitOnTailscaleFailure(t *testing.T) {
	fake := &fakeTailscale{}
	store := NewStore()
	defer store.Close()
	service := NewService(testServiceConfig(), store, fake, nil)
	authentication, credentialID := installTestAdminAndCredential(t, service, store)
	code := createConfiguredPairingCode(t, service, authentication, credentialID,
		`{"networkMode":"default","subnetRouter":true,"autoGatewayRoute":false,"advertiseRoutes":["192.168.2.0/24"]}`)
	started := startAdminTestSession(t, service, code, "")
	fake.mu.Lock()
	fake.device = Device{NodeID: "n-route-admin", Hostname: started.Hostname, Created: time.Now(), Tags: []string{managedDeviceTag}}
	fake.mu.Unlock()
	attach := newJSONRequest(http.MethodPost, "/v1/sessions/"+started.ID+"/device", `{"nodeId":"n-route-admin"}`)
	attach.Header.Set("Authorization", "Bearer "+started.Token)
	attachResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(attachResponse, attach)
	if attachResponse.Code != http.StatusOK {
		t.Fatalf("活动路由测试绑定设备失败: %d %s", attachResponse.Code, attachResponse.Body.String())
	}
	stored, ok, err := store.GetSession(started.ID)
	if err != nil || !ok {
		t.Fatalf("读取活动路由测试会话能力失败: %+v ok=%v err=%v", stored, ok, err)
	}
	knownCapabilities := cloneClientCapabilities(stored.ClientCapabilities)
	if updated, err := store.SyncSessionWithStateAndCapabilities(
		started.ID, time.Now(), time.Now().Add(time.Minute), 1, "{}", []string{"route-advertisement-v1"},
	); err != nil || !updated {
		t.Fatalf("写入缺失 baseline 能力失败: updated=%v err=%v", updated, err)
	}
	missingBaseline := adminPatchRequest(started.ID, `{"expectedRevision":1,"config":{"advertiseRoutes":["192.168.3.0/24"]}}`, authentication, true)
	missingBaselineResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(missingBaselineResponse, missingBaseline)
	if missingBaselineResponse.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(missingBaselineResponse.Body.String(), `"code":"client_capability_missing"`) ||
		!strings.Contains(missingBaselineResponse.Body.String(), `session-sync-v1`) {
		t.Fatalf("缺失 session-sync baseline 能力未被拒绝: %d %s", missingBaselineResponse.Code, missingBaselineResponse.Body.String())
	}
	if updated, err := store.SyncSessionWithStateAndCapabilities(
		started.ID, time.Now(), time.Now().Add(time.Minute), 1, "{}", knownCapabilities,
	); err != nil || !updated {
		t.Fatalf("恢复活动路由测试能力失败: updated=%v err=%v", updated, err)
	}

	update := adminPatchRequest(started.ID, `{"expectedRevision":1,"config":{"advertiseRoutes":["192.168.3.0/24"]}}`, authentication, true)
	updateResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(updateResponse, update)
	if updateResponse.Code != http.StatusOK || !strings.Contains(updateResponse.Body.String(), `"configRevision":2`) {
		t.Fatalf("活动路由更新失败: %d %s", updateResponse.Code, updateResponse.Body.String())
	}
	stored, ok, err = store.GetSession(started.ID)
	if err != nil || !ok || stored.ConfigRevision != 2 || len(stored.Routes) != 1 || stored.Routes[0] != "192.168.3.0/24" {
		t.Fatalf("活动路由更新未原子写入 session: %+v ok=%v err=%v", stored, ok, err)
	}
	fake.mu.Lock()
	updatedRoutes := append([]string{}, fake.routes["n-route-admin"]...)
	fake.setDeviceRoutesErr = errTestAdminRouteUpdate
	fake.mu.Unlock()
	if len(updatedRoutes) != 1 || updatedRoutes[0] != "192.168.3.0/24" {
		t.Fatalf("活动路由更新没有触达精确设备: %v", updatedRoutes)
	}

	failure := adminPatchRequest(started.ID, `{"expectedRevision":2,"config":{"advertiseRoutes":["192.168.4.0/24"]}}`, authentication, true)
	failureResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(failureResponse, failure)
	if failureResponse.Code != http.StatusBadGateway ||
		!strings.Contains(failureResponse.Body.String(), `"code":"tailscale_route_update_failed"`) {
		t.Fatalf("Tailscale 路由失败未阻止配置提交: %d %s", failureResponse.Code, failureResponse.Body.String())
	}
	stored, ok, err = store.GetSession(started.ID)
	if err != nil || !ok || stored.ConfigRevision != 2 || stored.Routes[0] != "192.168.3.0/24" {
		t.Fatalf("Tailscale 失败后 session 被错误提交: %+v ok=%v err=%v", stored, ok, err)
	}
}

var errTestAdminRouteUpdate = errorString("test route update failure")

type errorString string

func (e errorString) Error() string { return string(e) }

func TestAdminSessionConfigRejectsNullUnknownAndDuplicateFields(t *testing.T) {
	store := NewStore()
	defer store.Close()
	service := NewService(testServiceConfig(), store, &fakeTailscale{}, nil)
	authentication, credentialID := installTestAdminAndCredential(t, service, store)
	started := startAdminTestSession(t, service, createTestPairingCode(t, service, authentication, credentialID), "192.168.1.1/32")

	for _, body := range []string{
		`{"expectedRevision":1,"config":{"vpnEnabled":null}}`,
		`{"expectedRevision":1,"config":{"unknown":true}}`,
		`{"expectedRevision":1,"config":{"vpnEnabled":false,"vpnEnabled":true}}`,
		`{"expectedRevision":1,"config":{"vpnEnabled":false},"unknown":true}`,
	} {
		request := adminPatchRequest(started.ID, body, authentication, true)
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"json_invalid"`) {
			t.Fatalf("非法 patch 没有被严格拒绝: body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	stored, ok, err := store.GetSession(started.ID)
	if err != nil || !ok || stored.ConfigRevision != 1 {
		t.Fatalf("非法 patch 改变了 session revision: %+v ok=%v err=%v", stored, ok, err)
	}
}

func TestRouteReconcileRestoresDatabaseRoutesAfterRecovery(t *testing.T) {
	fake := &fakeTailscale{}
	store := NewStore()
	defer store.Close()
	service := NewService(testServiceConfig(), store, fake, nil)
	_, credentialID := installTestAdminAndCredential(t, service, store)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.PutSession(Session{
		ID: "route-reconcile", CredentialID: credentialID, AuthKeyID: "key-reconcile",
		ProvisioningName: "pinnode-route-reconcile", DeviceID: "n-route-reconcile",
		Routes: []string{"192.0.2.0/24"}, Status: SessionActive, ConfigRevision: 3,
		RouteReconcilePending: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	service.ReapOnce(t.Context(), now)
	stored, ok, err := store.GetSession("route-reconcile")
	if err != nil || !ok || stored.RouteReconcilePending {
		t.Fatalf("路由恢复后未清除持久化标记: %+v ok=%v err=%v", stored, ok, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.routes["n-route-reconcile"]; len(got) != 1 || got[0] != "192.0.2.0/24" {
		t.Fatalf("路由恢复没有回到数据库权威值: %v", got)
	}
}
