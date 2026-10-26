package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// This exercises both HTTP boundaries and SQLite recovery. Android's actual
// VPN/network behavior remains the responsibility of device integration tests.
func TestProvisioningHTTPRoundTripAcrossRestart(t *testing.T) {
	for _, tc := range []struct {
		name               string
		config             SessionConfig
		routes, wifiRoutes []string
	}{
		{"normal", SessionConfig{NetworkMode: "default", VPNEnabled: true, AcceptDNS: true, AcceptRoutes: true}, []string{}, []string{}},
		{"rescue", SessionConfig{NetworkMode: "cellular", VPNEnabled: true, SubnetRouter: true, AutoGatewayRoute: true}, []string{"172.20.0.1/32"}, []string{"172.20.0.1/32"}},
		{"subnet", SessionConfig{NetworkMode: "default", VPNEnabled: true, SubnetRouter: true, AutoWiFiSubnetRoute: true}, []string{"172.20.0.0/24"}, []string{"172.20.0.0/24"}},
		{"exit", SessionConfig{NetworkMode: "default", VPNEnabled: true, AdvertiseExitNode: true}, []string{"0.0.0.0/0", "::/0"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var device Device
			var enabledRoutes []string
			var cleanup []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer "+fakeTailscaleKey("api", "http-integration") {
					t.Error("wrong upstream credential")
					w.WriteHeader(401)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/v2/tailnet/test/keys":
					io.WriteString(w, `{"keys":[]}`)
				case r.Method == "POST" && r.URL.Path == "/api/v2/tailnet/test/keys":
					var payload struct {
						Capabilities struct {
							Devices struct {
								Create struct {
									Reusable, Preauthorized, Ephemeral bool
									Tags                               []string
								}
							}
						}
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					create := payload.Capabilities.Devices.Create
					if create.Reusable || !create.Preauthorized || create.Ephemeral || !reflect.DeepEqual(create.Tags, []string{managedDeviceTag}) {
						t.Error("unsafe auth key policy")
					}
					json.NewEncoder(w).Encode(map[string]string{"id": "k-http", "key": fakeTailscaleKey("auth", "http-integration")})
				case r.Method == "GET" && r.URL.Path == "/api/v2/device/n-http":
					json.NewEncoder(w).Encode(device)
				case r.Method == "POST" && r.URL.Path == "/api/v2/device/n-http/routes":
					var payload struct {
						Routes []string `json:"routes"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					enabledRoutes = payload.Routes
					if len(enabledRoutes) == 0 {
						cleanup = append(cleanup, "routes")
					}
					io.WriteString(w, `{}`)
				case r.Method == "DELETE" && r.URL.Path == "/api/v2/device/n-http":
					cleanup = append(cleanup, "device")
					w.WriteHeader(204)
				case r.Method == "DELETE" && r.URL.Path == "/api/v2/tailnet/test/keys/k-http":
					cleanup = append(cleanup, "key")
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected upstream operation: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer upstream.Close()
			config := testServiceConfig()
			config.TailscaleBaseURL, config.TailscaleTailnet = upstream.URL, "test"
			path := filepath.Join(t.TempDir(), "pinnode.db")
			store, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { store.Close() }()
			service := NewService(config, store, NewTailscaleClient(config), nil)
			server := httptest.NewServer(service.Handler())
			defer func() { server.Close() }()
			jar, _ := cookiejar.New(nil)
			client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
			csrf := ""
			do := func(method, path string, body any, bearer string, want int, result any) {
				t.Helper()
				var encoded []byte
				if body != nil {
					encoded, err = json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
				}
				request, err := http.NewRequest(method, server.URL+path, bytes.NewReader(encoded))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-CSRF-Token", csrf)
				if bearer != "" {
					request.Header.Set("Authorization", "Bearer "+bearer)
				}
				if path == "/v1/sessions" && method == "POST" {
					request.Header.Set("Idempotency-Key", "http-integration-start-key")
				}
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != want {
					t.Fatalf("%s %s: status %d, want %d", method, path, response.StatusCode, want)
				}
				if result != nil {
					if err := json.NewDecoder(response.Body).Decode(result); err != nil {
						t.Fatal(err)
					}
				}
			}
			var pow PoWChallenge
			do("GET", "/v1/auth/pow", nil, "", 200, &pow)
			var authenticated struct {
				CSRFToken string `json:"csrfToken"`
			}
			do("POST", "/v1/auth/setup", adminAuthRequest{Username: "owner", Password: "http integration administrator password", PoWID: pow.ID, PoWNonce: solveTestPoW(pow)}, "", 201, &authenticated)
			csrf = authenticated.CSRFToken
			var credential struct {
				ID string `json:"id"`
			}
			do("POST", "/v1/tailscale-credentials", map[string]string{"name": "HTTP integration", "type": "api_token", "token": fakeTailscaleKey("api", "http-integration")}, "", 201, &credential)
			var pin struct {
				Code string `json:"code"`
			}
			do("POST", "/v1/pairing-codes", map[string]any{"credentialId": credential.ID, "config": tc.config}, "", 201, &pin)
			var started startSessionResponse
			start := startSessionRequest{Code: pin.Code, GatewayRoute: "172.20.0.1/32", WiFiSubnetRoute: "172.20.0.0/24", ProtocolVersion: 1, ClientVersion: "integration", ClientCapabilities: []string{clientCapabilitySessionSync, clientCapabilityRescueRouting, clientCapabilityRouteAdvert, clientCapabilityExitNode, clientCapabilityAdvancedPrefs}}
			do("POST", "/v1/sessions", start, "", 201, &started)
			if !reflect.DeepEqual(started.Routes, tc.routes) || !reflect.DeepEqual(started.WiFiRoutes, tc.wifiRoutes) {
				t.Fatalf("incorrect routing policy: routes=%v wifi=%v", started.Routes, started.WiFiRoutes)
			}
			var replay startSessionResponse
			do("POST", "/v1/sessions", start, "", 201, &replay)
			if replay.SessionID != started.SessionID || replay.SessionToken != started.SessionToken {
				t.Fatal("lost response retry created a different session")
			}
			mu.Lock()
			device = Device{ID: "device-http", NodeID: "n-http", Hostname: started.ProvisioningName, Created: time.Now(), Tags: []string{managedDeviceTag}}
			mu.Unlock()
			do("POST", "/v1/sessions/"+started.SessionID+"/device", attachDeviceRequest{NodeID: "n-http"}, started.SessionToken, 200, nil)
			mu.Lock()
			matched := reflect.DeepEqual(enabledRoutes, tc.routes)
			mu.Unlock()
			if !matched {
				t.Fatal("control-plane routes differ from session policy")
			}
			do("PATCH", "/v1/sessions/"+started.SessionID+"/config", map[string]any{"expectedRevision": 1, "confirmHighRisk": true, "config": map[string]any{"hostname": "updated-node"}}, "", 200, nil)
			var synced sessionSyncResponse
			syncBody := sessionSyncRequest{ProtocolVersion: 1, AppliedConfigRevision: 1, ClientVersion: "integration", ClientCapabilities: start.ClientCapabilities}
			do("POST", "/v1/sessions/"+started.SessionID+"/sync", syncBody, started.SessionToken, 200, &synced)
			if synced.DesiredConfig == nil || synced.DesiredConfig.Revision != 2 || synced.DesiredConfig.Config.Hostname != "updated-node" {
				t.Fatal("desired revision did not reach client")
			}
			syncBody.AppliedConfigRevision = 2
			do("POST", "/v1/sessions/"+started.SessionID+"/sync", syncBody, started.SessionToken, 200, nil)
			server.Close()
			store.Close()
			store, err = OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			service = NewService(config, store, NewTailscaleClient(config), nil)
			if err := service.validateStoredCredentials(); err != nil {
				t.Fatal(err)
			}
			server = httptest.NewServer(service.Handler())
			// Cookie identity is host-bound, not port-bound: existing admin/session
			// credentials must continue to work after the server process restarts.
			do("POST", "/v1/admin/sessions/"+started.SessionID+"/stop", map[string]any{}, "", 200, nil)
			mu.Lock()
			effects := strings.Join(cleanup, ",")
			mu.Unlock()
			if !strings.HasSuffix(effects, "routes,device,key") {
				t.Fatalf("cleanup did not revoke precise resources in order: %s", effects)
			}
			session, ok, err := store.GetSession(started.SessionID)
			if err != nil || !ok || session.Status != SessionStopped || session.AppliedConfigRevision != 2 {
				t.Fatal("restart lost revision, cleanup state or history")
			}
			do("POST", "/v1/admin/sessions/"+started.SessionID+"/stop", map[string]any{}, "", 200, nil)
		})
	}
}
