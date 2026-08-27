package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExchangeOAuthTokenUsesClientCredentialsForm(t *testing.T) {
	testToken := fakeTailscaleKey("oauth", "short")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/oauth/token" {
			t.Errorf("OAuth token 请求错误: %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "client_credentials" ||
			r.Form.Get("client_id") != "client-id" || r.Form.Get("client_secret") != "client-secret" ||
			r.Form.Get("scope") != "auth_keys devices:core devices:routes" ||
			r.Form.Get("tags") != managedDeviceTag {
			t.Errorf("OAuth client credentials 表单错误: %#v", r.Form)
		}
		if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" {
			t.Error("OAuth client secret 不应进入 URL 或认证头")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + testToken + `","token_type":"Bearer","expires_in":3600,"scope":"auth_keys devices:core devices:routes"}`))
	}))
	defer server.Close()

	client := NewTailscaleClient(Config{TailscaleBaseURL: server.URL, TailscaleTailnet: "-"})
	before := time.Now()
	token, err := client.ExchangeOAuthToken(t.Context(), "client-id", "client-secret")
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != testToken || len(token.Scopes) != 3 ||
		token.ExpiresAt.Before(before.Add(59*time.Minute)) {
		t.Fatalf("OAuth token 响应解析错误: %#v", token)
	}
}

func TestValidateCredentialUsesBearerHeaderAndConfiguredTailnet(t *testing.T) {
	testToken := fakeTailscaleKey("api", "test-secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/tailnet/-/keys" {
			t.Errorf("验证凭据请求错误: %s %s", r.Method, r.URL.Path)
		}
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer "+testToken {
			t.Errorf("验证凭据认证头错误: %q", authorization)
		}
		if r.URL.RawQuery != "" || r.URL.User != nil {
			t.Errorf("令牌不应进入 URL: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer server.Close()

	client := NewTailscaleClient(Config{TailscaleBaseURL: server.URL, TailscaleTailnet: "-"})
	if err := client.ValidateCredential(t.Context(), testToken); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAuthKeyUsesManagedDeviceTag(t *testing.T) {
	testAccessToken := fakeTailscaleKey("oauth", "test")
	testAuthKey := fakeTailscaleKey("auth", "test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/tailnet/-/keys" {
			t.Errorf("创建 auth key 请求错误: %s %s", r.Method, r.URL.Path)
		}
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer "+testAccessToken {
			t.Errorf("创建 auth key 认证头错误: %q", authorization)
		}
		var payload struct {
			Capabilities struct {
				Devices struct {
					Create struct {
						Tags []string `json:"tags"`
					} `json:"create"`
				} `json:"devices"`
			} `json:"capabilities"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Capabilities.Devices.Create.Tags) != 1 ||
			payload.Capabilities.Devices.Create.Tags[0] != managedDeviceTag {
			t.Errorf("auth key tag = %v, want %q", payload.Capabilities.Devices.Create.Tags, managedDeviceTag)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"` + testAuthKey + `","id":"key-test"}`))
	}))
	defer server.Close()

	client := NewTailscaleClient(Config{TailscaleBaseURL: server.URL, TailscaleTailnet: "-"})
	if _, err := client.CreateAuthKey(t.Context(), testAccessToken, 5*time.Minute, false); err != nil {
		t.Fatal(err)
	}
}

func TestSetDeviceRoutesEncodesEmptyArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/device/node-test/routes" {
			t.Errorf("设置设备路由请求错误: %s %s", r.Method, r.URL.Path)
		}
		var payload struct {
			Routes []string `json:"routes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Routes == nil || len(payload.Routes) != 0 {
			t.Errorf("空路由必须编码为 []: %#v", payload.Routes)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewTailscaleClient(Config{TailscaleBaseURL: server.URL, TailscaleTailnet: "-"})
	if err := client.SetDeviceRoutes(t.Context(), fakeTailscaleKey("api", "test"), "node-test", nil); err != nil {
		t.Fatal(err)
	}
}
