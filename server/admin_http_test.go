package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFirstRunSetupLoginAndEncryptedCredential(t *testing.T) {
	testToken := fakeTailscaleKey("api", "test-secret")
	config := testServiceConfig()
	store := NewStore()
	defer store.Close()
	service := NewService(config, store, &fakeTailscale{}, nil)

	stateRequest := localRequest(http.MethodGet, "/v1/auth/state", nil)
	stateResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(stateResponse, stateRequest)
	if stateResponse.Code != http.StatusOK ||
		!strings.Contains(stateResponse.Body.String(), `"setupRequired":true`) ||
		!strings.Contains(stateResponse.Body.String(), `"setupAllowed":true`) {
		t.Fatalf("首次启动状态错误: %d %s", stateResponse.Code, stateResponse.Body.String())
	}

	setupResponse := performPasswordAuth(
		t, service, "/v1/auth/setup", "owner", "correct horse battery staple", nil,
	)
	if setupResponse.Code != http.StatusCreated {
		t.Fatalf("首次注册返回 %d: %s", setupResponse.Code, setupResponse.Body.String())
	}
	var authenticated struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(setupResponse.Body.Bytes(), &authenticated); err != nil {
		t.Fatal(err)
	}
	if authenticated.CSRFToken == "" {
		t.Fatal("首次注册没有返回 CSRF token")
	}
	cookie := sessionCookie(t, setupResponse)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("管理员 cookie 属性不安全: %+v", cookie)
	}
	admin, ok, err := store.GetAdminByUsername("owner")
	if err != nil || !ok || strings.Contains(admin.PasswordHash, "correct horse") || !verifyPassword(admin.PasswordHash, "correct horse battery staple") {
		t.Fatalf("管理员密码未正确哈希: ok=%v err=%v hash=%q", ok, err, admin.PasswordHash)
	}

	credentialRequest := localRequest(
		http.MethodPost, "/v1/tailscale-credentials",
		strings.NewReader(`{"name":"家庭 Tailnet","token":"`+testToken+`"}`),
	)
	credentialRequest.AddCookie(cookie)
	credentialRequest.Header.Set("X-CSRF-Token", authenticated.CSRFToken)
	credentialResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(credentialResponse, credentialRequest)
	if credentialResponse.Code != http.StatusCreated {
		t.Fatalf("保存 Tailscale 令牌返回 %d: %s", credentialResponse.Code, credentialResponse.Body.String())
	}
	var saved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(credentialResponse.Body.Bytes(), &saved); err != nil || saved.ID == "" {
		t.Fatalf("保存凭据响应无效: id=%q err=%v", saved.ID, err)
	}
	stored, ok, err := store.GetTailscaleCredential(saved.ID)
	if err != nil || !ok || strings.Contains(string(stored.Ciphertext), testToken) {
		t.Fatalf("Tailscale 令牌未安全加密: ok=%v err=%v ciphertext=%q", ok, err, stored.Ciphertext)
	}
	plaintext, err := service.cipher.Open(stored.ID, stored.Ciphertext)
	if err != nil || plaintext != testToken {
		t.Fatalf("加密凭据不能正确解密: token=%q err=%v", plaintext, err)
	}

	listRequest := localRequest(http.MethodGet, "/v1/tailscale-credentials", nil)
	listRequest.AddCookie(cookie)
	listResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK ||
		!strings.Contains(listResponse.Body.String(), "家庭 Tailnet") ||
		strings.Contains(listResponse.Body.String(), testToken) {
		t.Fatalf("凭据列表泄露或缺少数据: %d %s", listResponse.Code, listResponse.Body.String())
	}

	logoutRequest := localRequest(http.MethodPost, "/v1/auth/logout", nil)
	logoutRequest.AddCookie(cookie)
	logoutRequest.Header.Set("X-CSRF-Token", authenticated.CSRFToken)
	logoutResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("退出登录返回 %d: %s", logoutResponse.Code, logoutResponse.Body.String())
	}

	loginResponse := performPasswordAuth(
		t, service, "/v1/auth/login", "owner", "correct horse battery staple", nil,
	)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("再次登录返回 %d: %s", loginResponse.Code, loginResponse.Body.String())
	}
}

func TestOAuthClientCredentialIsEncryptedAndAccessTokenIsCached(t *testing.T) {
	clientSecret := fakeTailscaleKey("client", "secret")
	config := testServiceConfig()
	store := NewStore()
	defer store.Close()
	fake := &fakeTailscale{}
	service := NewService(config, store, fake, nil)
	authentication, _ := installTestAdminAndCredential(t, service, store)

	request := localRequest(
		http.MethodPost, "/v1/tailscale-credentials",
		strings.NewReader(`{"name":"长期 OAuth","type":"oauth_client","clientId":"client-id","clientSecret":"`+clientSecret+`"}`),
	)
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: authentication.CookieValue})
	request.Header.Set("X-CSRF-Token", authentication.CSRFToken)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("保存 OAuth client 返回 %d: %s", response.Code, response.Body.String())
	}
	var saved struct {
		ID   string                  `json:"id"`
		Type TailscaleCredentialKind `json:"type"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil ||
		saved.ID == "" || saved.Type != TailscaleCredentialOAuthClient {
		t.Fatalf("OAuth client 响应错误: %#v err=%v", saved, err)
	}
	stored, ok, err := store.GetTailscaleCredential(saved.ID)
	if err != nil || !ok || stored.Kind != TailscaleCredentialOAuthClient ||
		strings.Contains(string(stored.Ciphertext), "client-id") ||
		strings.Contains(string(stored.Ciphertext), clientSecret) {
		t.Fatalf("OAuth client 未正确加密: %#v ok=%v err=%v", stored, ok, err)
	}
	plaintext, err := service.cipher.Open(stored.ID, stored.Ciphertext)
	if err != nil || !strings.Contains(plaintext, "client-id") || !strings.Contains(plaintext, clientSecret) {
		t.Fatalf("OAuth client 密文不能正确解密: plaintext=%q err=%v", plaintext, err)
	}
	for range 2 {
		token, err := service.credentialToken(t.Context(), saved.ID)
		if err != nil || token != fakeTailscaleKey("oauth", "test") {
			t.Fatalf("读取 OAuth access token 错误: token=%q err=%v", token, err)
		}
	}
	fake.mu.Lock()
	oauthCalls := fake.oauthCalls
	fake.mu.Unlock()
	if oauthCalls != 1 {
		t.Fatalf("保存后重复读取不应重新交换 OAuth token: calls=%d", oauthCalls)
	}
	service.oauthMu.Lock()
	service.oauth[saved.ID] = cachedOAuthToken{token: "expiring", expiresAt: time.Now().Add(30 * time.Second)}
	service.oauthMu.Unlock()
	fake.mu.Lock()
	fake.oauthToken = OAuthAccessToken{
		Token: fakeTailscaleKey("oauth", "refreshed"), ExpiresAt: time.Now().Add(time.Hour),
		Scopes: []string{"auth_keys", "devices:core", "devices:routes"},
	}
	fake.mu.Unlock()
	refreshed, err := service.credentialToken(t.Context(), saved.ID)
	if err != nil || refreshed != fakeTailscaleKey("oauth", "refreshed") {
		t.Fatalf("临近过期的 OAuth token 没有刷新: token=%q err=%v", refreshed, err)
	}
	fake.mu.Lock()
	oauthCalls = fake.oauthCalls
	fake.mu.Unlock()
	if oauthCalls != 2 {
		t.Fatalf("OAuth token 刷新调用次数错误: calls=%d", oauthCalls)
	}
	if strings.Contains(response.Body.String(), "client-id") || strings.Contains(response.Body.String(), clientSecret) {
		t.Fatalf("OAuth client 响应泄露 secret: %s", response.Body.String())
	}
}

func TestOAuthClientRequiresPinNodeScopes(t *testing.T) {
	config := testServiceConfig()
	store := NewStore()
	defer store.Close()
	fake := &fakeTailscale{oauthToken: OAuthAccessToken{
		Token: fakeTailscaleKey("oauth", "limited"), ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"auth_keys"},
	}}
	service := NewService(config, store, fake, nil)
	authentication, _ := installTestAdminAndCredential(t, service, store)
	request := localRequest(
		http.MethodPost, "/v1/tailscale-credentials",
		strings.NewReader(`{"name":"权限不足","type":"oauth_client","clientId":"client-id","clientSecret":"client-secret"}`),
	)
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: authentication.CookieValue})
	request.Header.Set("X-CSRF-Token", authentication.CSRFToken)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "devices:core") ||
		!strings.Contains(response.Body.String(), "devices:routes") {
		t.Fatalf("权限不足的 OAuth client 返回 %d: %s", response.Code, response.Body.String())
	}
}

func TestAdminWriteRequiresCSRF(t *testing.T) {
	config := testServiceConfig()
	store := NewStore()
	defer store.Close()
	service := NewService(config, store, &fakeTailscale{}, nil)
	authentication, credentialID := installTestAdminAndCredential(t, service, store)

	request := localRequest(
		http.MethodPost, "/v1/pairing-codes",
		strings.NewReader(`{"credentialId":"`+credentialID+`"}`),
	)
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: authentication.CookieValue})
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("缺少 CSRF token 的管理员写请求返回 %d: %s", response.Code, response.Body.String())
	}
}

func TestRemoteFirstRunSetupIsDeniedByDefault(t *testing.T) {
	config := testServiceConfig()
	config.AllowRemoteSetup = false
	store := NewStore()
	defer store.Close()
	service := NewService(config, store, &fakeTailscale{}, nil)

	challenge, err := service.pow.Issue("198.51.100.10", config.PoWDifficulty, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	nonce := solveTestPoW(challenge)
	request := httptest.NewRequest(
		http.MethodPost, "/v1/auth/setup",
		strings.NewReader(`{"username":"owner","password":"correct horse battery staple","powId":"`+challenge.ID+`","powNonce":"`+nonce+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "198.51.100.10:1234"
	request.TLS = &tls.ConnectionState{}
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("远程首次注册返回 %d: %s", response.Code, response.Body.String())
	}
}

func performPasswordAuth(t *testing.T, service *Service, path, username, password string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	powRequest := localRequest(http.MethodGet, "/v1/auth/pow", nil)
	powResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(powResponse, powRequest)
	if powResponse.Code != http.StatusOK {
		t.Fatalf("申请 PoW 返回 %d: %s", powResponse.Code, powResponse.Body.String())
	}
	var challenge PoWChallenge
	if err := json.Unmarshal(powResponse.Body.Bytes(), &challenge); err != nil {
		t.Fatal(err)
	}
	nonce := solveTestPoW(challenge)
	body, _ := json.Marshal(adminAuthRequest{
		Username: username, Password: password, PoWID: challenge.ID, PoWNonce: nonce,
	})
	request := localRequest(http.MethodPost, path, strings.NewReader(string(body)))
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	return response
}

func solveTestPoW(challenge PoWChallenge) string {
	for nonce := uint64(0); ; nonce++ {
		value := strconv.FormatUint(nonce, 10)
		digest := sha256.Sum256([]byte(challenge.Value + ":" + value))
		if hasLeadingZeroBits(digest[:], challenge.Difficulty) {
			return value
		}
	}
}

func localRequest(method, path string, body *strings.Reader) *http.Request {
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, body)
		request.Header.Set("Content-Type", "application/json")
	}
	request.RemoteAddr = "127.0.0.1:1234"
	return request
}

func sessionCookie(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == adminSessionCookie {
			return cookie
		}
	}
	t.Fatal("响应没有管理员会话 cookie")
	return nil
}
