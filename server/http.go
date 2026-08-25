package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	config    Config
	store     *Store
	tailscale TailscaleAPI
	limiter   *RateLimiter
	pow       *PoWManager
	cipher    *CredentialCipher
	dummyHash string
	logger    *log.Logger
	oauthMu   sync.Mutex
	oauth     map[string]cachedOAuthToken
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
	if config.HeartbeatTTL <= 0 {
		config.HeartbeatTTL = 5 * time.Minute
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
	return &Service{
		config:    config,
		store:     store,
		tailscale: tailscale,
		limiter:   NewRateLimiter(),
		pow:       NewPoWManager(),
		cipher:    credentialCipher,
		dummyHash: dummyHash,
		logger:    logger,
		oauth:     make(map[string]cachedOAuthToken),
	}
}

func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if debugBuild {
		recorder := &diagnosticResponseWriter{ResponseWriter: w, status: http.StatusOK}
		startedAt := time.Now()
		defer func() {
			s.logger.Printf(
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
		writeError(w, http.StatusMethodNotAllowed, "不支持 OPTIONS")
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
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
	case r.URL.Path == "/v1/sessions":
		s.handleStartSession(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
		s.handleSession(w, r)
	default:
		writeError(w, http.StatusNotFound, "路径不存在")
	}
}

type diagnosticResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *diagnosticResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
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
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	if !s.requireAdminAPI(w, r, true) {
		return
	}
	if !s.allowRate(w, "code:"+clientAddress(r, s.config.TrustedProxyCIDRs), 30, time.Minute) {
		return
	}
	request := pairingCodeRequest{}
	if !decodeOptionalJSON(w, r, &request) {
		return
	}
	if request.CredentialID == "" {
		writeError(w, http.StatusBadRequest, "请选择 Tailscale 管理凭据")
		return
	}
	if _, err := s.credentialToken(r.Context(), request.CredentialID); err != nil {
		writeError(w, http.StatusBadRequest, "所选 Tailscale 管理凭据不存在或不可用")
		return
	}
	config := DefaultRescueConfig()
	if request.Config != nil {
		config = *request.Config
	}
	var err error
	config, err = config.Normalize()
	if err != nil {
		writeError(w, http.StatusBadRequest, "配置无效: "+err.Error())
		return
	}
	if config.ExitPolicy.At != "" {
		fixedAt, _ := time.Parse(time.RFC3339, config.ExitPolicy.At)
		if !fixedAt.After(time.Now()) {
			writeError(w, http.StatusBadRequest, "配置无效: 固定退出时间必须晚于当前时间")
			return
		}
	}
	createdAt := time.Now()
	expiresAt := createdAt.Add(s.config.CodeTTL)
	var code string
	for range 10 {
		code, err = newPairingCode()
		if err != nil {
			break
		}
		err = s.store.PutCodeWithCredentialAt(
			hashPairingCode(s.config.CodePepper, code), request.CredentialID,
			createdAt, expiresAt, config,
		)
		if err == nil {
			break
		}
		if !isUniqueConstraintError(err) {
			break
		}
	}
	if err != nil {
		s.logger.Printf("保存配对代码失败: %v", err)
		writeError(w, http.StatusInternalServerError, "生成配对代码失败")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"code":      code,
		"expiresAt": expiresAt.UTC().Format(time.RFC3339),
		"config":    config,
	})
}

type pairingCodeRequest struct {
	CredentialID string        `json:"credentialId"`
	Config       *RescueConfig `json:"config"`
}

type startSessionRequest struct {
	Code            string `json:"code"`
	GatewayRoute    string `json:"gatewayRoute"`
	WiFiSubnetRoute string `json:"wifiSubnetRoute"`
}

func (s *Service) handleStartSession(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.handleListSessions(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	client := clientAddress(r, s.config.TrustedProxyCIDRs)
	if !s.allowRate(w, "start-source:"+client, 10, 5*time.Minute) ||
		!s.allowRate(w, "start-global", 300, 5*time.Minute) {
		return
	}
	var request startSessionRequest
	if !decodeJSON(w, r, &request) || !validPairingCode(request.Code) {
		writeError(w, http.StatusBadRequest, "code 必须是六位数字")
		return
	}
	codeHash := hashPairingCode(s.config.CodePepper, request.Code)
	if !s.allowRate(w, "start-code:"+codeHash, 5, 5*time.Minute) {
		return
	}
	if request.GatewayRoute != "" {
		if err := validateGatewayRoute(request.GatewayRoute); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if request.WiFiSubnetRoute != "" {
		if err := validateWiFiSubnetRoute(request.WiFiSubnetRoute, request.GatewayRoute); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	now := time.Now()
	config, configCreatedAt, credentialID, ok, err := s.store.RedeemCodeWithCredential(codeHash, now)
	if err != nil {
		s.logger.Printf("读取配对代码失败: %v", err)
		writeError(w, http.StatusInternalServerError, "读取配对代码失败")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "code 无效、已使用或已过期")
		return
	}
	accessToken, err := s.credentialToken(r.Context(), credentialID)
	if err != nil {
		s.logger.Printf("读取 Tailscale 凭据失败: credentialRef=%s error=%v", diagnosticIdentifier(credentialID), err)
		writeError(w, http.StatusBadGateway, "该授权码关联的 Tailscale 管理凭据不可用")
		return
	}
	if config.RequiresWiFi() && request.GatewayRoute == "" {
		writeError(w, http.StatusBadRequest, "当前模板需要连接带 IPv4 网关的 Wi-Fi")
		return
	}
	if config.AutoWiFiSubnetRoute && request.WiFiSubnetRoute == "" {
		writeError(w, http.StatusBadRequest, "当前模板需要可识别的 Wi-Fi IPv4 子网")
		return
	}
	logoutAt := config.LogoutAt(configCreatedAt, now)
	if !logoutAt.IsZero() && !logoutAt.After(now) {
		writeError(w, http.StatusGone, "该配置的自动退出时间已经到达")
		return
	}

	authKey, err := s.tailscale.CreateAuthKey(r.Context(), accessToken, s.config.ProvisioningTTL, false)
	if err != nil {
		// code 已经消耗，避免攻击者通过上游故障反复重放；客户端需要申请新 code。
		s.logger.Printf("创建 Tailscale auth key 失败: %v", err)
		writeError(w, http.StatusBadGateway, "Tailscale 暂时不可用，请重新申请 code")
		return
	}
	sessionToken, tokenHash, err := newSecretToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建会话失败")
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
		CreatedAt:            now,
		ProvisioningDeadline: now.Add(s.config.ProvisioningTTL),
		ExpiresAt:            logoutAt,
		Status:               SessionProvisioning,
		UpdatedAt:            now,
	}
	if err := s.store.PutSession(session); err != nil {
		_ = s.tailscale.DeleteAuthKey(r.Context(), accessToken, authKey.ID)
		s.logger.Printf("保存会话失败: %v", err)
		writeError(w, http.StatusInternalServerError, "创建会话失败")
		return
	}
	_ = s.store.TouchTailscaleCredential(credentialID, now)
	heartbeatSeconds := int64(0)
	if session.Config.ExitPolicy.OnAppClose {
		heartbeatSeconds = int64(heartbeatInterval(s.config.HeartbeatTTL) / time.Second)
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"sessionId":                session.ID,
		"sessionToken":             sessionToken,
		"authKey":                  authKey.Secret,
		"provisioningHostname":     session.ProvisioningName,
		"heartbeatIntervalSeconds": heartbeatSeconds,
		"gatewayRoute":             session.Route,
		"routes":                   session.Routes,
		"wifiRoutes":               session.WiFiRoutes,
		"config":                   session.Config,
		"expiresAt":                formatOptionalTime(session.ExpiresAt),
	})
}

func (s *Service) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAPI(w, r, false) {
		return
	}
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 1000 {
			writeError(w, http.StatusBadRequest, "limit 必须在 1 到 1000 之间")
			return
		}
		limit = parsed
	}
	sessions, err := s.store.ListSessions(limit)
	if err != nil {
		s.logger.Printf("读取历史会话失败: %v", err)
		writeError(w, http.StatusInternalServerError, "读取历史会话失败")
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
		writeError(w, http.StatusNotFound, "会话路径不存在")
		return
	}
	sessionID := parts[0]
	if !s.allowRate(w, "session-auth:"+credentialFingerprint(bearerToken(r)), 30, 5*time.Minute) {
		return
	}
	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		s.logger.Printf("读取会话失败: %v", err)
		writeError(w, http.StatusInternalServerError, "读取会话失败")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "会话不存在")
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
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	switch parts[1] {
	case "device":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
			return
		}
		s.handleAttachDevice(w, r, session)
	case "heartbeat":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
			return
		}
		s.handleHeartbeat(w, session)
	case "stop":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
			return
		}
		s.stopSession(w, r.Context(), session.ID)
	default:
		writeError(w, http.StatusNotFound, "会话操作不存在")
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
	if !decodeJSON(w, r, &request) || !validNodeID(request.NodeID) {
		writeError(w, http.StatusBadRequest, "nodeId 无效")
		return
	}
	now := time.Now()
	if !session.ExpiresAt.IsZero() && !now.Before(session.ExpiresAt) {
		writeError(w, http.StatusGone, "会话已过期")
		return
	}
	accessToken, err := s.credentialToken(r.Context(), session.CredentialID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Tailscale 管理凭据不可用")
		return
	}
	if session.Status == SessionActive && session.DeviceID == request.NodeID {
		if err := s.tailscale.SetDeviceRoutes(r.Context(), accessToken, request.NodeID, session.Routes); err != nil {
			s.logger.Printf("重新确认设备路由失败: nodeRef=%s error=%v", diagnosticIdentifier(request.NodeID), err)
			writeError(w, http.StatusBadGateway, "启用救援路由失败")
			return
		}
		if _, err := s.store.Heartbeat(session.ID, now, s.heartbeatDeadline(session, now)); err != nil {
			writeError(w, http.StatusInternalServerError, "刷新设备绑定失败")
			return
		}
		writeJSON(w, http.StatusOK, attachedDeviceResponse(session, request.NodeID))
		return
	}
	if session.Status != SessionProvisioning {
		writeError(w, http.StatusConflict, "会话已绑定其他设备或正在清理")
		return
	}
	device, err := s.tailscale.GetDevice(r.Context(), accessToken, request.NodeID)
	if err != nil {
		writeError(w, http.StatusConflict, "设备尚未在 Tailscale 控制面出现，请稍后重试")
		return
	}
	if device.NodeID != "" && device.NodeID != request.NodeID {
		writeError(w, http.StatusForbidden, "设备身份与请求不一致")
		return
	}
	if device.IsEphemeral || !hasTag(device.Tags, managedDeviceTag) ||
		(!device.Created.IsZero() && device.Created.Before(session.CreatedAt.Add(-30*time.Second))) {
		writeError(w, http.StatusForbidden, "设备不是本次 PinNode 持久节点")
		return
	}
	if device.Created.IsZero() || !strings.EqualFold(device.Hostname, session.ProvisioningName) {
		writeError(w, http.StatusConflict, "设备注册信息尚未同步，请稍后重试")
		return
	}
	attached, err := s.store.AttachDevice(
		session.ID, request.NodeID, now, s.heartbeatDeadline(session, now),
	)
	if err != nil {
		s.logger.Printf("绑定设备失败: sessionRef=%s error=%v", diagnosticIdentifier(session.ID), err)
		writeError(w, http.StatusInternalServerError, "绑定设备失败")
		return
	}
	if !attached {
		writeError(w, http.StatusConflict, "设备已绑定到其他会话")
		return
	}
	if err := s.tailscale.SetDeviceRoutes(r.Context(), accessToken, request.NodeID, session.Routes); err != nil {
		_ = s.store.DetachDevice(session.ID, request.NodeID, now)
		s.logger.Printf("启用设备精确路由失败: nodeRef=%s error=%v", diagnosticIdentifier(request.NodeID), err)
		writeError(w, http.StatusBadGateway, "启用救援路由失败")
		return
	}
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

func (s *Service) handleHeartbeat(w http.ResponseWriter, session Session) {
	if session.Status != SessionActive {
		writeError(w, http.StatusConflict, "会话尚未绑定设备或正在清理")
		return
	}
	if !session.Config.ExitPolicy.OnAppClose {
		writeError(w, http.StatusConflict, "该会话未启用应用关闭心跳租约")
		return
	}
	now := time.Now()
	if !session.ExpiresAt.IsZero() && !now.Before(session.ExpiresAt) {
		writeError(w, http.StatusGone, "会话已过期")
		return
	}
	deadline := now.Add(s.config.HeartbeatTTL)
	updated, err := s.store.Heartbeat(session.ID, now, deadline)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "记录心跳失败")
		return
	}
	if !updated {
		writeError(w, http.StatusConflict, "会话正在清理")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "active",
		"heartbeatDeadline": deadline.UTC().Format(time.RFC3339),
	})
}

func (s *Service) heartbeatDeadline(session Session, now time.Time) time.Time {
	if !session.Config.ExitPolicy.OnAppClose {
		return time.Time{}
	}
	return now.Add(s.config.HeartbeatTTL)
}

func (s *Service) stopSession(w http.ResponseWriter, ctx context.Context, id string) {
	_, ok, err := s.store.BeginCleanup(id, time.Now(), true, "client_stop")
	if err != nil {
		s.logger.Printf("开始清理会话失败: sessionRef=%s error=%v", diagnosticIdentifier(id), err)
		writeError(w, http.StatusInternalServerError, "开始清理会话失败")
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"status": "already-stopped"})
		return
	}
	err = s.cleanupSession(ctx, id)
	if err != nil {
		writeError(w, http.StatusBadGateway, "清理 Tailscale 节点失败，请稍后重试")
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
		if err := s.tailscale.SetDeviceRoutes(ctx, accessToken, session.DeviceID, nil); err != nil {
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
	return cleanupErr
}

func isHTTPStatus(err error, status int) bool {
	var apiErr *HTTPError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

func (s *Service) ReapOnce(ctx context.Context, now time.Time) {
	sessions, err := s.store.ReapableSessions(now)
	if err != nil {
		s.logger.Printf("读取待清理会话失败: %v", err)
		return
	}
	for _, session := range sessions {
		if _, ok, err := s.store.BeginCleanup(session.ID, now, false, ""); err != nil {
			s.logger.Printf("锁定待清理会话失败: sessionRef=%s error=%v", diagnosticIdentifier(session.ID), err)
			continue
		} else if !ok {
			continue
		}
		if err := s.cleanupSession(ctx, session.ID); err != nil {
			s.logger.Printf("会话自动清理失败: sessionRef=%s error=%v", diagnosticIdentifier(session.ID), err)
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
	writeError(w, http.StatusTooManyRequests, "请求过于频繁")
	return false
}

func credentialFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:8])
}

func (s *Service) requireSession(w http.ResponseWriter, r *http.Request, session Session) bool {
	token := bearerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "需要会话认证")
		return false
	}
	_, actualHash, err := hashTokenForCheck(token)
	if err != nil || !equalSecretHash(session.TokenHash, actualHash) {
		writeError(w, http.StatusUnauthorized, "会话认证失败")
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

func publicSession(session Session) map[string]any {
	return map[string]any{
		"sessionId":    session.ID,
		"gatewayRoute": session.Route,
		"routes":       session.Routes,
		"wifiRoutes":   session.WiFiRoutes,
		"config":       session.Config,
		"deviceId":     session.DeviceID,
		"status":       session.Status,
		"createdAt":    session.CreatedAt.UTC().Format(time.RFC3339),
		"expiresAt":    formatOptionalTime(session.ExpiresAt),
	}
}

func historicalSession(session Session) map[string]any {
	item := publicSession(session)
	item["authKeyId"] = session.AuthKeyID
	item["provisioningHostname"] = session.ProvisioningName
	item["provisioningDeadline"] = formatOptionalTime(session.ProvisioningDeadline)
	item["lastSeenAt"] = formatOptionalTime(session.LastSeenAt)
	item["heartbeatDeadline"] = formatOptionalTime(session.HeartbeatDeadline)
	item["stoppedAt"] = formatOptionalTime(session.StoppedAt)
	item["stopReason"] = session.StopReason
	item["cleanupError"] = session.CleanupErr
	return item
}

func provisioningHostname(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return "pinnode-" + hex.EncodeToString(digest[:12])
}

func heartbeatInterval(ttl time.Duration) time.Duration {
	interval := ttl / 3
	if interval < 30*time.Second {
		return 30 * time.Second
	}
	if interval > time.Minute {
		return time.Minute
	}
	return interval.Round(time.Second)
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "JSON 请求无效")
		return false
	}
	return true
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeError(w, http.StatusBadRequest, "JSON 请求无效")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// 这些小包装让安全校验逻辑集中在此文件，避免把原始 token 放进 Session。
func base64RawURLDecode(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(value)
}

func sha256Bytes(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}
