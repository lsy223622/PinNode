package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type cachedOAuthToken struct {
	token     string
	expiresAt time.Time
}

type Service struct {
	config             Config
	store              *Store
	tailscale          TailscaleAPI
	limiter            *RateLimiter
	pow                *PoWManager
	cipher             *CredentialCipher
	dummyHash          string
	logger             *structuredLogger
	events             *adminEventHub
	oauthMu            sync.Mutex
	oauth              map[string]cachedOAuthToken
	consoleDeviceMu    sync.Mutex
	consoleDeviceCache map[string]cachedConsoleDevice
	startMu            sync.Mutex
}

func NewService(config Config, store *Store, tailscale TailscaleAPI, logger *log.Logger) *Service {
	if logger == nil {
		logger = log.Default()
	}
	if config.CodeTTL <= 0 {
		config.CodeTTL = 5 * time.Minute
	}
	if config.ProvisioningTTL <= 0 {
		config.ProvisioningTTL = 10 * time.Minute
	}
	if config.SyncLeaseTTL <= 0 {
		config.SyncLeaseTTL = 5 * time.Minute
	}
	if config.AdminSessionTTL <= 0 {
		config.AdminSessionTTL = 12 * time.Hour
	}
	if config.PoWDifficulty == 0 {
		config.PoWDifficulty = 18
	}
	credentialCipher, err := NewCredentialCipher(config.CredentialKey)
	if err != nil {
		panic(err)
	}
	dummyHash, err := hashPassword("PinNode dummy password verifier")
	if err != nil {
		panic(err)
	}
	events := newAdminEventHub()
	return &Service{
		config:             config,
		store:              store,
		tailscale:          tailscale,
		limiter:            NewRateLimiter(),
		pow:                NewPoWManager(),
		cipher:             credentialCipher,
		dummyHash:          dummyHash,
		logger:             &structuredLogger{base: logger, events: events},
		events:             events,
		oauth:              make(map[string]cachedOAuthToken),
		consoleDeviceCache: make(map[string]cachedConsoleDevice),
	}
}

func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	requestID, err := newURLToken(12)
	if err != nil {
		requestID = "unavailable"
	}
	w.Header().Set("X-Request-ID", requestID)
	if debugBuild {
		recorder := &diagnosticResponseWriter{ResponseWriter: w, status: http.StatusOK}
		startedAt := time.Now()
		defer func() {
			s.logger.Infof("http",
				"HTTP method=%s route=%s peer=%s client=%s status=%d duration=%s",
				r.Method, diagnosticRoute(r.URL.Path), parseRemoteAddress(r.RemoteAddr),
				clientAddress(r, s.config.TrustedProxyCIDRs), recorder.status,
				time.Since(startedAt).Round(time.Millisecond),
			)
		}()
		w = recorder
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	if isSecureRequest(r, s.config.TrustedProxyCIDRs) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	if r.Method == http.MethodOptions {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持 OPTIONS")
		return
	}
	if r.URL.Path != "/healthz" && !s.allowRate(
		w, "source:"+clientAddress(r, s.config.TrustedProxyCIDRs), 120, time.Minute,
	) {
		return
	}
	switch {
	case r.URL.Path == "/healthz":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case r.URL.Path == "/v1/meta":
		s.handleAPIMeta(w, r)
	case r.URL.Path == "/" || r.URL.Path == "/admin":
		s.serveAdminPage(w, r)
	case r.URL.Path == "/assets/mark.svg":
		s.serveAdminMark(w, r)
	case r.URL.Path == "/v1/auth/state":
		s.handleAdminAuthState(w, r)
	case r.URL.Path == "/v1/auth/pow":
		s.handlePoWChallenge(w, r)
	case r.URL.Path == "/v1/auth/setup":
		s.handleAdminSetup(w, r)
	case r.URL.Path == "/v1/auth/login":
		s.handleAdminLogin(w, r)
	case r.URL.Path == "/v1/auth/logout":
		s.handleAdminLogout(w, r)
	case r.URL.Path == "/v1/tailscale-credentials":
		s.handleTailscaleCredentials(w, r)
	case r.URL.Path == "/v1/pairing-codes":
		s.handlePairingCode(w, r)
	case r.URL.Path == "/v1/admin/console":
		s.handleAdminConsole(w, r)
	case r.URL.Path == "/v1/admin/console/stream":
		s.handleAdminEventStream(w, r, "state")
	case r.URL.Path == "/v1/admin/logs/recent":
		s.handleAdminRecentLogs(w, r)
	case r.URL.Path == "/v1/admin/logs/stream":
		s.handleAdminEventStream(w, r, "log")
	case r.URL.Path == "/v1/sessions":
		s.handleStartSession(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
		s.handleSession(w, r)
	default:
		writeError(w, http.StatusNotFound, "route_not_found", "路径不存在")
	}
}

func (s *Service) handleAPIMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"apiVersion":      apiVersion,
		"protocolVersion": protocolVersion,
		"features":        append([]string{}, serverFeatures...),
		"limits": map[string]any{
			"jsonBodyBytes":         maxJSONBodyBytes,
			"clientLogsBodyBytes":   maxClientLogBodyBytes,
			"clientLogEntries":      maxClientLogEntries,
			"clientLogMessageBytes": maxClientLogMessage,
		},
	})
}

type diagnosticResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *diagnosticResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *diagnosticResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func diagnosticRoute(requestPath string) string {
	if !strings.HasPrefix(requestPath, "/v1/sessions/") {
		return requestPath
	}
	parts := strings.Split(strings.Trim(requestPath, "/"), "/")
	if len(parts) == 4 {
		return "/v1/sessions/:id/" + parts[3]
	}
	return "/v1/sessions/:id"
}

func diagnosticIdentifier(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:6])
}

func (s *Service) handlePairingCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	if !s.requireAdminAPI(w, r, true) {
		return
	}
	if !s.allowRate(w, "code:"+clientAddress(r, s.config.TrustedProxyCIDRs), 30, time.Minute) {
		return
	}
	request := pairingCodeRequest{}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.CredentialID == "" {
		writeError(w, http.StatusBadRequest, "credential_required", "请选择 Tailscale 管理凭据")
		return
	}
	if _, err := s.credentialToken(r.Context(), request.CredentialID); err != nil {
		writeError(w, http.StatusBadRequest, "credential_unavailable", "所选 Tailscale 管理凭据不存在或不可用")
		return
	}
	config := DefaultSessionConfig()
	if len(request.Config) != 0 {
		if strings.TrimSpace(string(request.Config)) == "null" || decodeJSONBytes(request.Config, &config) != nil {
			writeError(w, http.StatusBadRequest, "session_config_invalid", "config 必须是有效的 JSON 对象")
			return
		}
	}
	var err error
	config, err = config.Normalize()
	if err != nil {
		writeError(w, http.StatusBadRequest, "session_config_invalid", "配置无效: "+err.Error())
		return
	}
	if config.ExitPolicy.At != "" {
		fixedAt, _ := time.Parse(time.RFC3339, config.ExitPolicy.At)
		if !fixedAt.After(time.Now()) {
			writeError(w, http.StatusBadRequest, "session_config_expired", "配置无效: 固定退出时间必须晚于当前时间")
			return
		}
	}
	createdAt := time.Now()
	expiresAt := createdAt.Add(s.config.CodeTTL)
	var code string
	var codeHash string
	for range 10 {
		code, err = newPairingCode()
		if err != nil {
			break
		}
		codeHash = hashPairingCode(s.config.CodePepper, code)
		codeCipher, cipherErr := s.cipher.Seal("pairing-code:"+codeHash, code)
		if cipherErr != nil {
			err = cipherErr
			break
		}
		err = s.store.PutCodeWithCredentialAtAndCipher(
			codeHash, request.CredentialID, createdAt, expiresAt, config, codeCipher,
		)
		if err == nil {
			break
		}
		if !isUniqueConstraintError(err) {
			break
		}
	}
	if err != nil {
		s.logger.Errorf("session", "保存配对代码失败: %v", err)
		writeError(w, http.StatusInternalServerError, "pairing_code_create_failed", "生成配对代码失败")
		return
	}
	s.events.publishState("pairing_code_created")
	writeJSON(w, http.StatusCreated, map[string]any{
		"code":      code,
		"expiresAt": expiresAt.UTC().Format(time.RFC3339),
		"config":    config,
	})
}

type pairingCodeRequest struct {
	CredentialID string          `json:"credentialId"`
	Config       json.RawMessage `json:"config"`
}

type startSessionRequest struct {
	Code            string `json:"code"`
	GatewayRoute    string `json:"gatewayRoute"`
	WiFiSubnetRoute string `json:"wifiSubnetRoute"`
}

type startSessionResponse struct {
	ProtocolVersion     int           `json:"protocolVersion"`
	ServerFeatures      []string      `json:"serverFeatures"`
	SessionID           string        `json:"sessionId"`
	SessionToken        string        `json:"sessionToken"`
	AuthKey             string        `json:"authKey"`
	ProvisioningName    string        `json:"provisioningHostname"`
	ConfigRevision      int64         `json:"configRevision"`
	SyncIntervalSeconds int64         `json:"syncIntervalSeconds"`
	GatewayRoute        string        `json:"gatewayRoute"`
	Routes              []string      `json:"routes"`
	WiFiRoutes          []string      `json:"wifiRoutes"`
	Config              SessionConfig `json:"config"`
	ExpiresAt           *string       `json:"expiresAt"`
}

func (s *Service) handleStartSession(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.handleListSessions(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	client := clientAddress(r, s.config.TrustedProxyCIDRs)
	if !s.allowRate(w, "start-source:"+client, 10, 5*time.Minute) ||
		!s.allowRate(w, "start-global", 300, 5*time.Minute) {
		return
	}
	var request startSessionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if !validPairingCode(request.Code) {
		writeError(w, http.StatusBadRequest, "pairing_code_format_invalid", "code 必须是六位数字")
		return
	}
	codeHash := hashPairingCode(s.config.CodePepper, request.Code)
	if !s.allowRate(w, "start-code:"+codeHash, 5, 5*time.Minute) {
		return
	}
	if request.GatewayRoute != "" {
		if err := validateGatewayRoute(request.GatewayRoute); err != nil {
			writeError(w, http.StatusBadRequest, "gateway_route_invalid", err.Error())
			return
		}
	}
	if request.WiFiSubnetRoute != "" {
		if err := validateWiFiSubnetRoute(request.WiFiSubnetRoute, request.GatewayRoute); err != nil {
			writeError(w, http.StatusBadRequest, "wifi_subnet_route_invalid", err.Error())
			return
		}
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !validIdempotencyKey(idempotencyKey) {
		writeError(w, http.StatusBadRequest, "idempotency_key_invalid", "Idempotency-Key 必须是 16 到 128 个不含空白的可见 ASCII 字符")
		return
	}
	encodedRequest, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_create_failed", "创建会话失败")
		return
	}
	idempotencyKeyHash := sha256Bytes([]byte(idempotencyKey))
	requestHash := sha256Bytes(encodedRequest)

	s.startMu.Lock()
	defer s.startMu.Unlock()
	if err := s.store.DeleteExpiredSessionStartReplays(time.Now()); err != nil {
		s.logger.Errorf("session", "清理会话创建重放记录失败: %v", err)
	}
	if replay, ok, err := s.store.GetSessionStartReplay(idempotencyKeyHash); err != nil {
		writeError(w, http.StatusInternalServerError, "session_create_failed", "读取会话创建状态失败")
		return
	} else if ok {
		if replay.RequestHash != requestHash {
			writeError(w, http.StatusConflict, "idempotency_key_conflict", "Idempotency-Key 已用于不同的请求")
			return
		}
		plaintext, err := s.cipher.Open("session-start:"+idempotencyKeyHash, replay.Ciphertext)
		if err != nil {
			s.logger.Errorf("session", "解密会话创建重放响应失败: sessionRef=%s error=%v", diagnosticIdentifier(replay.SessionID), err)
			writeError(w, http.StatusInternalServerError, "session_replay_failed", "读取会话创建响应失败")
			return
		}
		var response startSessionResponse
		if err := json.Unmarshal([]byte(plaintext), &response); err != nil {
			writeError(w, http.StatusInternalServerError, "session_replay_failed", "读取会话创建响应失败")
			return
		}
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusCreated, response)
		return
	}

	now := time.Now()
	config, configCreatedAt, credentialID, ok, err := s.store.GetRedeemableCodeWithCredential(codeHash, now)
	if err != nil {
		s.logger.Errorf("session", "读取配对代码失败: %v", err)
		writeError(w, http.StatusInternalServerError, "pairing_code_read_failed", "读取配对代码失败")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "pairing_code_invalid", "code 无效、已使用或已过期")
		return
	}
	accessToken, err := s.credentialToken(r.Context(), credentialID)
	if err != nil {
		s.logger.Errorf("tailscale", "读取 Tailscale 凭据失败: credentialRef=%s error=%v", diagnosticIdentifier(credentialID), err)
		writeError(w, http.StatusBadGateway, "credential_unavailable", "该授权码关联的 Tailscale 管理凭据不可用")
		return
	}
	if config.RequiresWiFi() && request.GatewayRoute == "" {
		writeError(w, http.StatusBadRequest, "wifi_gateway_required", "当前配置需要连接带 IPv4 网关的 Wi-Fi")
		return
	}
	if config.AutoWiFiSubnetRoute && request.WiFiSubnetRoute == "" {
		writeError(w, http.StatusBadRequest, "wifi_subnet_required", "当前配置需要可识别的 Wi-Fi IPv4 子网")
		return
	}
	logoutAt := config.LogoutAt(configCreatedAt, now)
	if !logoutAt.IsZero() && !logoutAt.After(now) {
		writeError(w, http.StatusGone, "session_config_expired", "该配置的自动退出时间已经到达")
		return
	}

	authKey, err := s.tailscale.CreateAuthKey(r.Context(), accessToken, s.config.ProvisioningTTL, false)
	if err != nil {
		s.logger.Errorf("tailscale", "创建 Tailscale auth key 失败: %v", err)
		code, message := tailscaleFailure(err, "创建 Tailscale auth key 失败")
		writeError(w, http.StatusBadGateway, code, message)
		return
	}
	sessionToken, tokenHash, err := newSecretToken()
	if err != nil {
		_ = s.tailscale.DeleteAuthKey(r.Context(), accessToken, authKey.ID)
		writeError(w, http.StatusInternalServerError, "session_create_failed", "创建会话失败")
		return
	}
	session := Session{
		ID:                   sessionToken[:16],
		TokenHash:            tokenHash,
		CredentialID:         credentialID,
		AuthKeyID:            authKey.ID,
		ProvisioningName:     provisioningHostname(sessionToken[:16]),
		Route:                request.GatewayRoute,
		Routes:               config.EffectiveRoutes(request.GatewayRoute, request.WiFiSubnetRoute),
		WiFiRoutes:           config.EffectiveWiFiRoutes(request.GatewayRoute, request.WiFiSubnetRoute),
		Config:               config,
		PairingCodeHash:      codeHash,
		ConfigRevision:       1,
		CreatedAt:            now,
		ProvisioningDeadline: now.Add(s.config.ProvisioningTTL),
		ExpiresAt:            logoutAt,
		Status:               SessionProvisioning,
		UpdatedAt:            now,
	}
	response := startSessionResponse{
		ProtocolVersion:     protocolVersion,
		ServerFeatures:      append([]string{}, serverFeatures...),
		SessionID:           session.ID,
		SessionToken:        sessionToken,
		AuthKey:             authKey.Secret,
		ProvisioningName:    session.ProvisioningName,
		ConfigRevision:      session.ConfigRevision,
		SyncIntervalSeconds: int64(syncInterval(s.config.SyncLeaseTTL) / time.Second),
		GatewayRoute:        session.Route,
		Routes:              append([]string{}, session.Routes...),
		WiFiRoutes:          append([]string{}, session.WiFiRoutes...),
		Config:              cloneSessionConfig(session.Config),
		ExpiresAt:           formatOptionalTime(session.ExpiresAt),
	}
	encodedResponse, err := json.Marshal(response)
	if err != nil {
		_ = s.tailscale.DeleteAuthKey(r.Context(), accessToken, authKey.ID)
		writeError(w, http.StatusInternalServerError, "session_create_failed", "创建会话失败")
		return
	}
	responseCipher, err := s.cipher.Seal("session-start:"+idempotencyKeyHash, string(encodedResponse))
	if err != nil {
		_ = s.tailscale.DeleteAuthKey(r.Context(), accessToken, authKey.ID)
		writeError(w, http.StatusInternalServerError, "session_create_failed", "创建会话失败")
		return
	}
	created, err := s.store.CreateSessionFromCode(
		codeHash, now, session, idempotencyKeyHash, requestHash, responseCipher,
		session.ProvisioningDeadline,
	)
	if err != nil || !created {
		_ = s.tailscale.DeleteAuthKey(r.Context(), accessToken, authKey.ID)
		if err != nil {
			s.logger.Errorf("session", "保存会话失败: %v", err)
			writeError(w, http.StatusInternalServerError, "session_create_failed", "创建会话失败")
		} else {
			writeError(w, http.StatusUnauthorized, "pairing_code_invalid", "code 无效、已使用或已过期")
		}
		return
	}
	_ = s.store.TouchTailscaleCredential(credentialID, now)
	s.events.publishState("session_created")
	writeJSON(w, http.StatusCreated, response)
}

func (s *Service) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAPI(w, r, false) {
		return
	}
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 1000 {
			writeError(w, http.StatusBadRequest, "limit_invalid", "limit 必须在 1 到 1000 之间")
			return
		}
		limit = parsed
	}
	sessions, err := s.store.ListSessions(limit)
	if err != nil {
		s.logger.Errorf("session", "读取历史会话失败: %v", err)
		writeError(w, http.StatusInternalServerError, "session_list_failed", "读取历史会话失败")
		return
	}
	items := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		items = append(items, historicalSession(session))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

func (s *Service) handleSession(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 2 {
		writeError(w, http.StatusNotFound, "session_path_not_found", "会话路径不存在")
		return
	}
	sessionID := parts[0]
	if !s.allowRate(w, "session-auth:"+credentialFingerprint(bearerToken(r)), 30, 5*time.Minute) {
		return
	}
	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		s.logger.Errorf("session", "读取会话失败: %v", err)
		writeError(w, http.StatusInternalServerError, "session_read_failed", "读取会话失败")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "会话不存在")
		return
	}
	if !s.requireSession(w, r, session) {
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, publicSession(session))
		return
	}
	if len(parts) != 2 {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	switch parts[1] {
	case "device":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
			return
		}
		s.handleAttachDevice(w, r, session)
	case "sync":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
			return
		}
		s.handleSessionSync(w, r, session)
	case "logs":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
			return
		}
		s.handleSessionLogs(w, r, session)
	case "stop":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
			return
		}
		s.stopSession(w, r.Context(), session.ID)
	default:
		writeError(w, http.StatusNotFound, "session_operation_not_found", "会话操作不存在")
	}
}

type attachDeviceRequest struct {
	NodeID string `json:"nodeId"`
}

func (s *Service) handleAttachDevice(w http.ResponseWriter, r *http.Request, session Session) {
	if !s.allowRate(w, "device:"+session.ID, 30, 5*time.Minute) {
		return
	}
	var request attachDeviceRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if !validNodeID(request.NodeID) {
		writeError(w, http.StatusBadRequest, "node_id_invalid", "nodeId 无效")
		return
	}
	now := time.Now()
	if !session.ExpiresAt.IsZero() && !now.Before(session.ExpiresAt) {
		writeError(w, http.StatusGone, "session_expired", "会话已过期")
		return
	}
	accessToken, err := s.credentialToken(r.Context(), session.CredentialID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credential_unavailable", "Tailscale 管理凭据不可用")
		return
	}
	if session.Status == SessionActive && session.DeviceID == request.NodeID {
		if session.Config.TailscaleIP != "" {
			if err := s.tailscale.SetDeviceIPv4(r.Context(), accessToken, request.NodeID, session.Config.TailscaleIP); err != nil {
				s.logger.Errorf("tailscale", "重新确认设备 Tailscale IP 失败: nodeRef=%s error=%v", diagnosticIdentifier(request.NodeID), err)
				code, message := tailscaleFailure(err, "设置客户端 Tailscale IP 失败")
				writeError(w, http.StatusBadGateway, code, message)
				return
			}
		}
		if err := s.tailscale.SetDeviceRoutes(r.Context(), accessToken, request.NodeID, session.Routes); err != nil {
			s.logger.Errorf("tailscale", "重新确认设备路由失败: nodeRef=%s error=%v", diagnosticIdentifier(request.NodeID), err)
			code, message := tailscaleFailure(err, "启用会话路由失败")
			writeError(w, http.StatusBadGateway, code, message)
			return
		}
		if _, err := s.store.TouchSession(session.ID, now, s.syncDeadline(session, now)); err != nil {
			writeError(w, http.StatusInternalServerError, "device_binding_refresh_failed", "刷新设备绑定失败")
			return
		}
		_ = s.store.DeleteSessionStartReplay(session.ID)
		writeJSON(w, http.StatusOK, attachedDeviceResponse(session, request.NodeID))
		return
	}
	if session.Status != SessionProvisioning {
		writeError(w, http.StatusConflict, "session_state_conflict", "会话已绑定其他设备或正在清理")
		return
	}
	device, err := s.tailscale.GetDevice(r.Context(), accessToken, request.NodeID)
	if err != nil {
		writeError(w, http.StatusConflict, "device_not_ready", "设备尚未在 Tailscale 控制面出现，请稍后重试")
		return
	}
	if device.NodeID != "" && device.NodeID != request.NodeID {
		writeError(w, http.StatusForbidden, "device_identity_mismatch", "设备身份与请求不一致")
		return
	}
	if device.IsEphemeral || !hasTag(device.Tags, managedDeviceTag) ||
		(!device.Created.IsZero() && device.Created.Before(session.CreatedAt.Add(-30*time.Second))) {
		writeError(w, http.StatusForbidden, "device_not_managed", "设备不是本次 PinNode 持久节点")
		return
	}
	if device.Created.IsZero() || !strings.EqualFold(device.Hostname, session.ProvisioningName) {
		writeError(w, http.StatusConflict, "device_not_ready", "设备注册信息尚未同步，请稍后重试")
		return
	}
	attached, err := s.store.AttachDevice(
		session.ID, request.NodeID, now, s.syncDeadline(session, now),
	)
	if err != nil {
		s.logger.Errorf("session", "绑定设备失败: sessionRef=%s error=%v", diagnosticIdentifier(session.ID), err)
		writeError(w, http.StatusInternalServerError, "device_attach_failed", "绑定设备失败")
		return
	}
	if !attached {
		writeError(w, http.StatusConflict, "device_already_bound", "设备已绑定到其他会话")
		return
	}
	if session.Config.TailscaleIP != "" {
		if err := s.tailscale.SetDeviceIPv4(r.Context(), accessToken, request.NodeID, session.Config.TailscaleIP); err != nil {
			_ = s.store.DetachDevice(session.ID, request.NodeID, now)
			s.logger.Errorf("tailscale", "设置设备 Tailscale IP 失败: nodeRef=%s error=%v", diagnosticIdentifier(request.NodeID), err)
			code, message := tailscaleFailure(err, "设置客户端 Tailscale IP 失败")
			writeError(w, http.StatusBadGateway, code, message)
			return
		}
	}
	if err := s.tailscale.SetDeviceRoutes(r.Context(), accessToken, request.NodeID, session.Routes); err != nil {
		_ = s.store.DetachDevice(session.ID, request.NodeID, now)
		s.logger.Errorf("tailscale", "启用设备精确路由失败: nodeRef=%s error=%v", diagnosticIdentifier(request.NodeID), err)
		code, message := tailscaleFailure(err, "启用会话路由失败")
		writeError(w, http.StatusBadGateway, code, message)
		return
	}
	_ = s.store.DeleteSessionStartReplay(session.ID)
	s.events.publishState("device_attached")
	writeJSON(w, http.StatusOK, attachedDeviceResponse(session, request.NodeID))
}

func attachedDeviceResponse(session Session, nodeID string) map[string]any {
	return map[string]any{
		"nodeId":       nodeID,
		"gatewayRoute": session.Route,
		"routes":       session.Routes,
		"wifiRoutes":   session.WiFiRoutes,
		"enabled":      true,
	}
}

type sessionSyncRequest struct {
	ProtocolVersion       int                `json:"protocolVersion"`
	AppliedConfigRevision int64              `json:"appliedConfigRevision"`
	ClientVersion         string             `json:"clientVersion"`
	ClientCapabilities    []string           `json:"clientCapabilities"`
	ClientState           *clientStateReport `json:"clientState"`
}

type sessionConfigSnapshot struct {
	Revision     int64         `json:"revision"`
	Config       SessionConfig `json:"config"`
	GatewayRoute string        `json:"gatewayRoute"`
	Routes       []string      `json:"routes"`
	WiFiRoutes   []string      `json:"wifiRoutes"`
	ExpiresAt    *string       `json:"expiresAt"`
}

type sessionSyncResponse struct {
	ProtocolVersion      int                    `json:"protocolVersion"`
	ServerFeatures       []string               `json:"serverFeatures"`
	Status               SessionStatus          `json:"status"`
	ServerTime           string                 `json:"serverTime"`
	NextSyncAfterSeconds int64                  `json:"nextSyncAfterSeconds"`
	SyncDeadline         *string                `json:"syncDeadline"`
	DesiredConfig        *sessionConfigSnapshot `json:"desiredConfig"`
}

func (s *Service) handleSessionSync(w http.ResponseWriter, r *http.Request, session Session) {
	if session.Status != SessionActive {
		writeError(w, http.StatusConflict, "session_state_conflict", "会话尚未绑定设备或正在清理")
		return
	}
	var request sessionSyncRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.ProtocolVersion != protocolVersion {
		writeError(w, http.StatusConflict, "protocol_version_unsupported", "客户端协议版本不受支持")
		return
	}
	if request.AppliedConfigRevision < 0 || request.AppliedConfigRevision > session.ConfigRevision {
		writeError(w, http.StatusConflict, "config_revision_invalid", "客户端配置 revision 无效")
		return
	}
	if len(request.ClientVersion) > 128 || strings.ContainsAny(request.ClientVersion, "\r\n\x00") ||
		!validCapabilities(request.ClientCapabilities) {
		writeError(w, http.StatusBadRequest, "client_metadata_invalid", "客户端版本或能力列表无效")
		return
	}
	now := time.Now()
	if !session.ExpiresAt.IsZero() && !now.Before(session.ExpiresAt) {
		writeError(w, http.StatusGone, "session_expired", "会话已过期")
		return
	}
	deadline := s.syncDeadline(session, now)
	clientStateJSON := ""
	if request.ClientState != nil {
		if err := validateClientState(*request.ClientState); err != nil {
			writeError(w, http.StatusBadRequest, "client_state_invalid", err.Error())
			return
		}
		encoded, err := json.Marshal(request.ClientState)
		if err != nil {
			writeError(w, http.StatusBadRequest, "client_state_invalid", "客户端状态无效")
			return
		}
		clientStateJSON = string(encoded)
	}
	updated, err := s.store.SyncSessionWithState(
		session.ID, now, deadline, request.AppliedConfigRevision, clientStateJSON,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_sync_failed", "同步会话状态失败")
		return
	}
	if !updated {
		writeError(w, http.StatusConflict, "session_state_conflict", "会话正在清理或配置 revision 已变化")
		return
	}
	s.events.publishState("session_sync")
	response := sessionSyncResponse{
		ProtocolVersion:      protocolVersion,
		ServerFeatures:       append([]string{}, serverFeatures...),
		Status:               SessionActive,
		ServerTime:           now.UTC().Format(time.RFC3339),
		NextSyncAfterSeconds: int64(syncInterval(s.config.SyncLeaseTTL) / time.Second),
		SyncDeadline:         formatOptionalTime(deadline),
	}
	if request.AppliedConfigRevision < session.ConfigRevision {
		response.DesiredConfig = &sessionConfigSnapshot{
			Revision:     session.ConfigRevision,
			Config:       cloneSessionConfig(session.Config),
			GatewayRoute: session.Route,
			Routes:       append([]string{}, session.Routes...),
			WiFiRoutes:   append([]string{}, session.WiFiRoutes...),
			ExpiresAt:    formatOptionalTime(session.ExpiresAt),
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Service) syncDeadline(session Session, now time.Time) time.Time {
	if !session.Config.ExitPolicy.OnAppClose {
		return time.Time{}
	}
	return now.Add(s.config.SyncLeaseTTL)
}

func (s *Service) stopSession(w http.ResponseWriter, ctx context.Context, id string) {
	_, ok, err := s.store.BeginCleanup(id, time.Now(), true, "client_stop")
	if err != nil {
		s.logger.Errorf("cleanup", "开始清理会话失败: sessionRef=%s error=%v", diagnosticIdentifier(id), err)
		writeError(w, http.StatusInternalServerError, "session_cleanup_start_failed", "开始清理会话失败")
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"status": "already-stopped"})
		return
	}
	s.events.publishState("session_cleanup_started")
	err = s.cleanupSession(ctx, id)
	if err != nil {
		writeError(w, http.StatusBadGateway, "session_cleanup_failed", "清理 Tailscale 节点失败，请稍后重试")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Service) cleanupSession(ctx context.Context, id string) error {
	session, ok, err := s.store.GetSession(id)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	var cleanupErr error
	accessToken, credentialErr := s.credentialToken(ctx, session.CredentialID)
	if credentialErr != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("读取 Tailscale 凭据: %w", credentialErr))
	}
	if credentialErr == nil && session.DeviceID != "" {
		if err := s.tailscale.SetDeviceRoutes(ctx, accessToken, session.DeviceID, []string{}); err != nil {
			// Android may have logged out before the server cleanup reaches
			// the control plane, so a missing node is idempotent success.
			if !isHTTPStatus(err, http.StatusNotFound) {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("撤销设备路由: %w", err))
			}
		}
		if err := s.tailscale.DeleteDevice(ctx, accessToken, session.DeviceID); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("删除受管设备: %w", err))
		}
	}
	if credentialErr == nil {
		if err := s.tailscale.DeleteAuthKey(ctx, accessToken, session.AuthKeyID); err != nil && !isHTTPStatus(err, http.StatusNotFound) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("撤销 auth key: %w", err))
		}
	}
	if err := s.store.FinishCleanup(id, time.Now(), cleanupErr); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("保存清理状态: %w", err))
	}
	s.events.publishState("session_cleanup_finished")
	if cleanupErr == nil {
		_ = s.store.DeleteSessionStartReplay(id)
	}
	return cleanupErr
}

func isHTTPStatus(err error, status int) bool {
	var apiErr *HTTPError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

func (s *Service) ReapOnce(ctx context.Context, now time.Time) {
	sessions, err := s.store.ReapableSessions(now)
	if err != nil {
		s.logger.Errorf("cleanup", "读取待清理会话失败: %v", err)
		return
	}
	for _, session := range sessions {
		if _, ok, err := s.store.BeginCleanup(session.ID, now, false, ""); err != nil {
			s.logger.Errorf("cleanup", "锁定待清理会话失败: sessionRef=%s error=%v", diagnosticIdentifier(session.ID), err)
			continue
		} else if !ok {
			continue
		}
		if err := s.cleanupSession(ctx, session.ID); err != nil {
			s.logger.Errorf("cleanup", "会话自动清理失败: sessionRef=%s error=%v", diagnosticIdentifier(session.ID), err)
		}
	}
}

func (s *Service) allowRate(w http.ResponseWriter, key string, max int, interval time.Duration) bool {
	allowed, retryAfter := s.limiter.Allow(key, max, interval, time.Now())
	if allowed {
		return true
	}
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁")
	return false
}

func credentialFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:8])
}

func (s *Service) requireSession(w http.ResponseWriter, r *http.Request, session Session) bool {
	token := bearerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "session_auth_required", "需要会话认证")
		return false
	}
	_, actualHash, err := hashTokenForCheck(token)
	if err != nil || !equalSecretHash(session.TokenHash, actualHash) {
		writeError(w, http.StatusUnauthorized, "session_auth_invalid", "会话认证失败")
		return false
	}
	return true
}

func hashTokenForCheck(token string) (string, string, error) {
	// 令牌是 base64url 编码的 32 字节随机值；重新解码可拒绝格式错误令牌。
	raw, err := base64RawURLDecode(token)
	if err != nil || len(raw) != 32 {
		return "", "", errors.New("invalid token")
	}
	hash := sha256Bytes(raw)
	return token, hash, nil
}

func bearerToken(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if len(value) < 7 || !strings.EqualFold(value[:6], "Bearer") || value[6] != ' ' {
		return ""
	}
	return strings.TrimSpace(value[7:])
}

func hasTag(tags []string, expected string) bool {
	for _, tag := range tags {
		if strings.EqualFold(tag, expected) {
			return true
		}
	}
	return false
}

func validIdempotencyKey(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func validCapabilities(values []string) bool {
	if len(values) > 32 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if len(value) < 1 || len(value) > 64 {
			return false
		}
		for _, character := range value {
			if !((character >= 'a' && character <= 'z') ||
				(character >= '0' && character <= '9') || character == '-' || character == '.') {
				return false
			}
		}
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func tailscaleFailure(err error, fallback string) (string, string) {
	var apiErr *HTTPError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "tailscale_permission_denied", "Tailscale 凭据权限不足或已失效"
		case http.StatusTooManyRequests:
			return "tailscale_rate_limited", "Tailscale 请求受限，请稍后重试"
		}
	}
	return "tailscale_unavailable", fallback
}

func publicSession(session Session) map[string]any {
	return map[string]any{
		"sessionId":             session.ID,
		"gatewayRoute":          session.Route,
		"routes":                session.Routes,
		"wifiRoutes":            session.WiFiRoutes,
		"config":                session.Config,
		"configRevision":        session.ConfigRevision,
		"appliedConfigRevision": session.AppliedConfigRevision,
		"deviceId":              session.DeviceID,
		"status":                session.Status,
		"createdAt":             session.CreatedAt.UTC().Format(time.RFC3339),
		"expiresAt":             formatOptionalTime(session.ExpiresAt),
	}
}

func historicalSession(session Session) map[string]any {
	item := publicSession(session)
	item["authKeyId"] = session.AuthKeyID
	item["provisioningHostname"] = session.ProvisioningName
	item["provisioningDeadline"] = formatOptionalTime(session.ProvisioningDeadline)
	item["lastSeenAt"] = formatOptionalTime(session.LastSeenAt)
	item["syncDeadline"] = formatOptionalTime(session.SyncDeadline)
	item["stoppedAt"] = formatOptionalTime(session.StoppedAt)
	item["stopReason"] = session.StopReason
	item["cleanupError"] = session.CleanupErr
	return item
}

func provisioningHostname(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return "pinnode-" + hex.EncodeToString(digest[:12])
}

func syncInterval(ttl time.Duration) time.Duration {
	interval := ttl / 3
	if interval < 30*time.Second {
		return 30 * time.Second
	}
	if interval > time.Minute {
		return time.Minute
	}
	return interval.Round(time.Second)
}

// 这些小包装让安全校验逻辑集中在此文件，避免把原始 token 放进 Session。
func base64RawURLDecode(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(value)
}

func sha256Bytes(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}
