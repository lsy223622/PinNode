package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCredentialRotationKeepsExistingSessionCleanupUsable(t *testing.T) {
	store := NewStore()
	defer store.Close()
	fake := &fakeTailscale{}
	service := NewService(testServiceConfig(), store, fake, nil)
	auth, credentialID := installTestAdminAndCredential(t, service, store)
	code := createTestPairingCode(t, service, auth, credentialID)
	started := startAdminTestSession(t, service, code, "192.168.1.1/32")
	before, _, _ := store.GetTailscaleCredential(credentialID)
	newToken := fakeTailscaleKey("api", "replacement")
	request := localRequest(http.MethodPut, "/v1/tailscale-credentials/"+credentialID, strings.NewReader(`{"name":"Rotated tailnet","type":"api_token","token":"`+newToken+`"}`))
	request.Header.Set("Content-Type", "application/json")
	authorizeTestAdmin(request, auth, true)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("rotation returned %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), newToken) {
		t.Fatal("rotation leaked token")
	}
	after, ok, err := store.GetTailscaleCredential(credentialID)
	if err != nil || !ok || after.Name != "Rotated tailnet" || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatal("rotation replaced credential identity")
	}
	if token, err := service.credentialToken(t.Context(), credentialID); err != nil || token != newToken {
		t.Fatal("rotation did not replace secret")
	}
	session, _, _ := store.GetSession(started.ID)
	if session.CredentialID != credentialID || session.Status != SessionProvisioning {
		t.Fatal("rotation changed session")
	}
	stop := newJSONRequest(http.MethodPost, "/v1/admin/sessions/"+started.ID+"/stop", `{}`)
	authorizeTestAdmin(stop, auth, true)
	stopped := httptest.NewRecorder()
	service.Handler().ServeHTTP(stopped, stop)
	if stopped.Code != http.StatusOK {
		t.Fatalf("cleanup after rotation failed: %d", stopped.Code)
	}
	session, _, _ = store.GetSession(started.ID)
	if session.Status != SessionStopped {
		t.Fatal("session cleanup not completed")
	}
}

func TestCredentialRotationRequiresAdminCSRFAndSecureTransport(t *testing.T) {
	for _, tc := range []struct {
		name               string
		admin, csrf, local bool
		want               int
	}{
		{"anonymous", false, false, true, 401},
		{"missing CSRF", true, false, true, 403},
		{"insecure remote", true, true, false, 426},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore()
			defer store.Close()
			service := NewService(testServiceConfig(), store, &fakeTailscale{}, nil)
			auth, id := installTestAdminAndCredential(t, service, store)
			before, _, _ := store.GetTailscaleCredential(id)
			request := newJSONRequest(http.MethodPut, "/v1/tailscale-credentials/"+id, `{"name":"Rotated","token":"`+fakeTailscaleKey("api", "replacement")+`"}`)
			if tc.local {
				request.RemoteAddr = "127.0.0.1:1234"
			}
			if tc.admin {
				authorizeTestAdmin(request, auth, tc.csrf)
			}
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("got %d, want %d", response.Code, tc.want)
			}
			after, _, _ := store.GetTailscaleCredential(id)
			if string(before.Ciphertext) != string(after.Ciphertext) {
				t.Fatal("rejected rotation changed secret")
			}
		})
	}
}

func TestOAuthRotationReplacesCachedTokenAndFailedValidationKeepsOldSecret(t *testing.T) {
	store := NewStore()
	defer store.Close()
	fake := &fakeTailscale{}
	service := NewService(testServiceConfig(), store, fake, nil)
	auth, id := installTestAdminAndCredential(t, service, store)
	service.cacheOAuthToken(id, OAuthAccessToken{Token: "stale-token", ExpiresAt: time.Now().Add(time.Hour)})
	rotate := func(body string) *httptest.ResponseRecorder {
		request := localRequest(http.MethodPut, "/v1/tailscale-credentials/"+id, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		authorizeTestAdmin(request, auth, true)
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, request)
		return response
	}
	fake.oauthToken = OAuthAccessToken{Token: "new-oauth-token", ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"auth_keys", "devices:core", "devices:routes"}}
	response := rotate(`{"name":"OAuth replacement","type":"oauth_client","clientId":"replacement-client","clientSecret":"replacement-secret"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("rotation failed: %d: %s", response.Code, response.Body.String())
	}
	if token, err := service.credentialToken(t.Context(), id); err != nil || token != "new-oauth-token" || fake.oauthCalls != 1 {
		t.Fatal("stale OAuth cache survived rotation")
	}
	before, _, _ := store.GetTailscaleCredential(id)
	fake.oauthToken.Scopes = []string{"auth_keys"}
	response = rotate(`{"name":"Rejected","type":"oauth_client","clientId":"other-client","clientSecret":"other-secret"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatal("insufficient OAuth scopes accepted")
	}
	after, _, _ := store.GetTailscaleCredential(id)
	if string(after.Ciphertext) != string(before.Ciphertext) || after.Name != before.Name {
		t.Fatal("failed rotation destroyed existing credential")
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
}
