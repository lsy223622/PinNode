package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const adminSessionCookie = "pinnode_admin_session"

type oauthClientSecret struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

type adminAuthRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	PoWID    string `json:"powId"`
	PoWNonce string `json:"powNonce"`
}

func (s *Service) handleAdminAuthState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	exists, err := s.store.AdminExists()
	if err != nil {
		s.logger.Errorf("auth", "读取管理员初始化状态失败: %v", err)
		writeError(w, http.StatusInternalServerError, "auth_state_failed", "读取登录状态失败")
		return
	}
	response := map[string]any{
		"setupRequired": !exists,
		"setupAllowed":  s.config.AllowRemoteSetup || isLoopbackClient(r, s.config.TrustedProxyCIDRs),
		"authenticated": false,
	}
	if session, ok, err := s.adminSession(r); err != nil {
		s.logger.Errorf("auth", "读取管理员会话失败: %v", err)
		writeError(w, http.StatusInternalServerError, "auth_state_failed", "读取登录状态失败")
		return
	} else if ok {
		response["authenticated"] = true
		response["username"] = session.Username
		response["csrfToken"] = session.CSRFToken
		response["expiresAt"] = session.ExpiresAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Service) handlePoWChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	client := clientAddress(r, s.config.TrustedProxyCIDRs)
	if !s.allowRate(w, "pow-source:"+client, 12, 5*time.Minute) ||
		!s.allowRate(w, "pow-global", 600, 5*time.Minute) {
		return
	}
	challenge, err := s.pow.Issue(client, s.config.PoWDifficulty, time.Now())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "pow_unavailable", "安全验证暂时繁忙")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         challenge.ID,
		"challenge":  challenge.Value,
		"difficulty": challenge.Difficulty,
		"expiresAt":  challenge.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *Service) handleAdminSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	if !s.requireSafeAuthRequest(w, r) {
		return
	}
	client := clientAddress(r, s.config.TrustedProxyCIDRs)
	if !s.allowRate(w, "setup-source:"+client, 5, 15*time.Minute) ||
		!s.allowRate(w, "setup-global", 30, 15*time.Minute) {
		return
	}
	var request adminAuthRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if !s.pow.Verify(request.PoWID, client, request.PoWNonce, time.Now()) {
		writeError(w, http.StatusBadRequest, "pow_invalid", "安全验证已失效，请重试")
		return
	}
	if !s.config.AllowRemoteSetup && !isLoopbackClient(r, s.config.TrustedProxyCIDRs) {
		writeError(w, http.StatusForbidden, "admin_setup_forbidden", "首次管理员注册默认只允许从服务端本机完成")
		return
	}
	username, err := normalizeAdminUsername(request.Username)
	if err != nil {
		writeError(w, http.StatusBadRequest, "admin_username_invalid", err.Error())
		return
	}
	if err := validateAdminPassword(request.Password); err != nil {
		writeError(w, http.StatusBadRequest, "admin_password_invalid", err.Error())
		return
	}
	passwordHash, err := hashPassword(request.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_setup_failed", "创建管理员账号失败")
		return
	}
	created, err := s.store.CreateAdmin(username, passwordHash, time.Now())
	if err != nil {
		s.logger.Errorf("auth", "创建管理员账号失败: %v", err)
		writeError(w, http.StatusInternalServerError, "admin_setup_failed", "创建管理员账号失败")
		return
	}
	if !created {
		writeError(w, http.StatusConflict, "admin_already_initialized", "管理员账号已经完成初始化")
		return
	}
	admin, ok, err := s.store.GetAdminByUsername(username)
	if err != nil || !ok {
		writeError(w, http.StatusInternalServerError, "admin_session_create_failed", "创建管理员会话失败")
		return
	}
	session, err := s.issueAdminSession(w, r, admin)
	if err != nil {
		s.logger.Errorf("auth", "创建管理员会话失败: %v", err)
		writeError(w, http.StatusInternalServerError, "admin_session_create_failed", "创建管理员会话失败")
		return
	}
	writeJSON(w, http.StatusCreated, adminSessionResponse(session))
}

func (s *Service) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	if !s.requireSafeAuthRequest(w, r) {
		return
	}
	client := clientAddress(r, s.config.TrustedProxyCIDRs)
	if !s.allowRate(w, "login-source:"+client, 10, 15*time.Minute) ||
		!s.allowRate(w, "login-global", 300, 15*time.Minute) {
		return
	}
	var request adminAuthRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if !s.pow.Verify(request.PoWID, client, request.PoWNonce, time.Now()) {
		writeError(w, http.StatusBadRequest, "pow_invalid", "安全验证已失效，请重试")
		return
	}
	username, usernameErr := normalizeAdminUsername(request.Username)
	if !s.allowRate(w, "login-account:"+credentialFingerprint(strings.ToLower(username)), 20, time.Hour) {
		return
	}
	admin, ok, err := s.store.GetAdminByUsername(username)
	if err != nil {
		s.logger.Errorf("auth", "读取管理员账号失败: %v", err)
		writeError(w, http.StatusInternalServerError, "admin_login_failed", "登录失败")
		return
	}
	passwordHash := s.dummyHash
	if ok {
		passwordHash = admin.PasswordHash
	}
	passwordMatches := verifyPassword(passwordHash, request.Password)
	now := time.Now()
	if ok && !admin.LockedUntil.IsZero() && admin.LockedUntil.After(now) {
		retryAfter := int64((admin.LockedUntil.Sub(now) + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
		writeError(w, http.StatusTooManyRequests, "admin_login_limited", "登录暂时受限，请稍后重试")
		return
	}
	if usernameErr != nil || !ok || !passwordMatches {
		if ok {
			if _, failureErr := s.store.RecordAdminLoginFailure(admin.ID, now); failureErr != nil {
				s.logger.Errorf("auth", "记录管理员登录失败状态: %v", failureErr)
			}
		}
		writeError(w, http.StatusUnauthorized, "admin_credentials_invalid", "用户名或密码错误")
		return
	}
	if err := s.store.ResetAdminLoginFailures(admin.ID, now); err != nil {
		s.logger.Errorf("auth", "重置管理员登录失败状态: %v", err)
		writeError(w, http.StatusInternalServerError, "admin_login_failed", "登录失败")
		return
	}
	session, err := s.issueAdminSession(w, r, admin)
	if err != nil {
		s.logger.Errorf("auth", "创建管理员会话失败: %v", err)
		writeError(w, http.StatusInternalServerError, "admin_login_failed", "登录失败")
		return
	}
	writeJSON(w, http.StatusOK, adminSessionResponse(session))
}

func (s *Service) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	if !sameOriginRequest(r, s.config.TrustedProxyCIDRs) {
		writeError(w, http.StatusForbidden, "origin_invalid", "请求来源无效")
		return
	}
	session, ok := s.requireAdminSession(w, r)
	if !ok {
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRFToken)) != 1 {
		writeError(w, http.StatusForbidden, "csrf_invalid", "CSRF 验证失败")
		return
	}
	if tokenHash, ok := adminSessionHash(r); ok {
		_ = s.store.DeleteAdminSession(tokenHash)
	}
	s.clearAdminSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleTailscaleCredentials(w http.ResponseWriter, r *http.Request) {
	writeRequired := r.Method != http.MethodGet
	if !s.requireAdminAPI(w, r, writeRequired) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		credentials, err := s.store.ListTailscaleCredentials()
		if err != nil {
			s.logger.Errorf("tailscale", "读取 Tailscale 凭据列表失败: %v", err)
			writeError(w, http.StatusInternalServerError, "credential_list_failed", "读取凭据列表失败")
			return
		}
		items := make([]map[string]any, 0, len(credentials))
		for _, credential := range credentials {
			items = append(items, publicTailscaleCredential(credential))
		}
		writeJSON(w, http.StatusOK, map[string]any{"credentials": items})
	case http.MethodPost:
		if !secureAuthTransport(r, s.config.TrustedProxyCIDRs) {
			writeError(w, http.StatusUpgradeRequired, "secure_transport_required", "保存 Tailscale 凭据需要 HTTPS；仅服务端本机可使用 HTTP")
			return
		}
		var request struct {
			Name         string `json:"name"`
			Type         string `json:"type"`
			Token        string `json:"token"`
			ClientID     string `json:"clientId"`
			ClientSecret string `json:"clientSecret"`
		}
		if !decodeJSON(w, r, &request) {
			return
		}
		name := strings.TrimSpace(request.Name)
		if name == "" || utf8.RuneCountInString(name) > 64 || len(name) > 256 || strings.ContainsAny(name, "\r\n\x00") {
			writeError(w, http.StatusBadRequest, "credential_name_invalid", "令牌名称必须为 1 到 64 个字符")
			return
		}
		kind := TailscaleCredentialKind(strings.TrimSpace(request.Type))
		if kind == "" {
			kind = TailscaleCredentialAPIToken
		}
		var plaintext string
		var oauthToken OAuthAccessToken
		switch kind {
		case TailscaleCredentialAPIToken:
			token := strings.TrimSpace(request.Token)
			if !strings.HasPrefix(token, "tskey-api-") || len(token) > 512 {
				writeError(w, http.StatusBadRequest, "api_token_invalid", "请输入以 tskey-api- 开头的 Tailscale API access token")
				return
			}
			if err := s.tailscale.ValidateCredential(r.Context(), token); err != nil {
				s.logger.Errorf("tailscale", "验证 Tailscale API token 失败: error=%s", safeDiagnosticError(err))
				code, message := tailscaleFailure(err, "Tailscale API token 验证失败，请检查有效期和 tailnet")
				writeError(w, http.StatusBadRequest, code, message)
				return
			}
			plaintext = token
		case TailscaleCredentialOAuthClient:
			clientID := strings.TrimSpace(request.ClientID)
			clientSecret := strings.TrimSpace(request.ClientSecret)
			if clientID == "" || len(clientID) > 512 || strings.ContainsAny(clientID, "\r\n\x00") ||
				clientSecret == "" || len(clientSecret) > 2048 || strings.ContainsAny(clientSecret, "\r\n\x00") {
				writeError(w, http.StatusBadRequest, "oauth_client_invalid", "请输入有效的 OAuth client ID 和 client secret")
				return
			}
			var err error
			oauthToken, err = s.tailscale.ExchangeOAuthToken(r.Context(), clientID, clientSecret)
			if err != nil {
				s.logger.Errorf("tailscale", "交换 Tailscale OAuth token 失败: error=%s", safeDiagnosticError(err))
				code, message := tailscaleFailure(err, "Tailscale OAuth client 验证失败，请检查 ID、secret 和权限")
				writeError(w, http.StatusBadRequest, code, message)
				return
			}
			if err := validateOAuthScopes(oauthToken.Scopes); err != nil {
				writeError(w, http.StatusBadRequest, "oauth_scope_invalid", err.Error())
				return
			}
			if err := s.tailscale.ValidateCredential(r.Context(), oauthToken.Token); err != nil {
				s.logger.Errorf("tailscale", "验证 Tailscale OAuth 权限失败: error=%s", safeDiagnosticError(err))
				code, message := tailscaleFailure(err, "OAuth client 缺少 auth_keys 权限或 tailnet 不匹配")
				writeError(w, http.StatusBadRequest, code, message)
				return
			}
			encoded, err := json.Marshal(oauthClientSecret{ClientID: clientID, ClientSecret: clientSecret})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "credential_save_failed", "保存 OAuth client 失败")
				return
			}
			plaintext = string(encoded)
		default:
			writeError(w, http.StatusBadRequest, "credential_type_unsupported", "不支持的 Tailscale 凭据类型")
			return
		}
		id, err := newURLToken(16)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "credential_save_failed", "保存凭据失败")
			return
		}
		ciphertext, err := s.cipher.Seal(id, plaintext)
		if err != nil {
			s.logger.Errorf("tailscale", "加密 Tailscale 凭据失败: %v", err)
			writeError(w, http.StatusInternalServerError, "credential_save_failed", "保存凭据失败")
			return
		}
		now := time.Now()
		credential := TailscaleCredential{
			ID: id, Name: name, Kind: kind, Ciphertext: ciphertext, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.store.PutTailscaleCredential(credential); err != nil {
			if isUniqueConstraintError(err) {
				writeError(w, http.StatusConflict, "credential_name_conflict", "令牌名称已经存在")
				return
			}
			s.logger.Errorf("tailscale", "保存 Tailscale 凭据失败: %v", err)
			writeError(w, http.StatusInternalServerError, "credential_save_failed", "保存凭据失败")
			return
		}
		if kind == TailscaleCredentialOAuthClient {
			s.cacheOAuthToken(id, oauthToken)
		}
		writeJSON(w, http.StatusCreated, publicTailscaleCredential(credential))
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
	}
}

func (s *Service) requireAdminAPI(w http.ResponseWriter, r *http.Request, requireCSRF bool) bool {
	session, ok := s.requireAdminSession(w, r)
	if !ok {
		return false
	}
	if requireCSRF {
		if !sameOriginRequest(r, s.config.TrustedProxyCIDRs) {
			writeError(w, http.StatusForbidden, "origin_invalid", "请求来源无效")
			return false
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRFToken)) != 1 {
			writeError(w, http.StatusForbidden, "csrf_invalid", "CSRF 验证失败")
			return false
		}
	}
	return true
}

func (s *Service) requireAdminSession(w http.ResponseWriter, r *http.Request) (AdminSession, bool) {
	session, ok, err := s.adminSession(r)
	if err != nil {
		s.logger.Errorf("auth", "读取管理员会话失败: %v", err)
		writeError(w, http.StatusInternalServerError, "admin_auth_failed", "管理员认证失败")
		return AdminSession{}, false
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "admin_auth_required", "需要管理员登录")
		return AdminSession{}, false
	}
	return session, true
}

func (s *Service) adminSession(r *http.Request) (AdminSession, bool, error) {
	tokenHash, ok := adminSessionHash(r)
	if !ok {
		return AdminSession{}, false, nil
	}
	return s.store.GetAdminSession(tokenHash, time.Now())
}

func adminSessionHash(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return "", false
	}
	raw, err := base64RawURLDecode(cookie.Value)
	if err != nil || len(raw) != 32 {
		return "", false
	}
	return sha256Bytes(raw), true
}

func (s *Service) issueAdminSession(w http.ResponseWriter, r *http.Request, admin AdminUser) (AdminSession, error) {
	token, err := newURLToken(32)
	if err != nil {
		return AdminSession{}, err
	}
	csrfToken, err := newURLToken(24)
	if err != nil {
		return AdminSession{}, err
	}
	raw, _ := base64RawURLDecode(token)
	now := time.Now()
	session := AdminSession{
		TokenHash: sha256Bytes(raw), AdminID: admin.ID, Username: admin.Username,
		CSRFToken: csrfToken, CreatedAt: now, ExpiresAt: now.Add(s.config.AdminSessionTTL),
	}
	if err := s.store.PutAdminSession(session); err != nil {
		return AdminSession{}, err
	}
	http.SetCookie(w, &http.Cookie{
		Name: adminSessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: isSecureRequest(r, s.config.TrustedProxyCIDRs), SameSite: http.SameSiteStrictMode,
	})
	return session, nil
}

func (s *Service) clearAdminSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: adminSessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: isSecureRequest(r, s.config.TrustedProxyCIDRs), SameSite: http.SameSiteStrictMode,
		MaxAge: -1, Expires: time.Unix(1, 0),
	})
}

func adminSessionResponse(session AdminSession) map[string]any {
	return map[string]any{
		"authenticated": true,
		"username":      session.Username,
		"csrfToken":     session.CSRFToken,
		"expiresAt":     session.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

func publicTailscaleCredential(credential TailscaleCredential) map[string]any {
	return map[string]any{
		"id":         credential.ID,
		"name":       credential.Name,
		"type":       credential.Kind,
		"createdAt":  credential.CreatedAt.UTC().Format(time.RFC3339),
		"lastUsedAt": formatOptionalTime(credential.LastUsedAt),
	}
}

func (s *Service) credentialToken(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("凭据 ID 为空")
	}
	credential, ok, err := s.store.GetTailscaleCredential(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("凭据不存在")
	}
	plaintext, err := s.cipher.Open(credential.ID, credential.Ciphertext)
	if err != nil {
		return "", err
	}
	if credential.Kind == "" || credential.Kind == TailscaleCredentialAPIToken {
		return plaintext, nil
	}
	if credential.Kind != TailscaleCredentialOAuthClient {
		return "", fmt.Errorf("不支持的凭据类型 %q", credential.Kind)
	}
	var oauthCredential oauthClientSecret
	if err := json.Unmarshal([]byte(plaintext), &oauthCredential); err != nil ||
		oauthCredential.ClientID == "" || oauthCredential.ClientSecret == "" {
		return "", fmt.Errorf("OAuth client 凭据无效")
	}

	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	if cached, ok := s.oauth[id]; ok && time.Now().Add(time.Minute).Before(cached.expiresAt) {
		return cached.token, nil
	}
	token, err := s.tailscale.ExchangeOAuthToken(ctx, oauthCredential.ClientID, oauthCredential.ClientSecret)
	if err != nil {
		return "", err
	}
	if !time.Now().Before(token.ExpiresAt) {
		return "", fmt.Errorf("OAuth access token 已过期")
	}
	s.oauth[id] = cachedOAuthToken{token: token.Token, expiresAt: token.ExpiresAt}
	return token.Token, nil
}

func (s *Service) cacheOAuthToken(id string, token OAuthAccessToken) {
	s.oauthMu.Lock()
	s.oauth[id] = cachedOAuthToken{token: token.Token, expiresAt: token.ExpiresAt}
	s.oauthMu.Unlock()
}

func validateOAuthScopes(scopes []string) error {
	if len(scopes) == 0 {
		return fmt.Errorf("Tailscale OAuth token 未返回权限范围")
	}
	available := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		available[scope] = true
	}
	if available["all"] {
		return nil
	}
	missing := make([]string, 0, 3)
	if !available["auth_keys"] && !available["devices"] {
		missing = append(missing, "auth_keys")
	}
	if !available["devices:core"] && !available["devices"] {
		missing = append(missing, "devices:core")
	}
	if !available["devices:routes"] && !available["routes"] {
		missing = append(missing, "devices:routes")
	}
	if len(missing) != 0 {
		return fmt.Errorf("OAuth client 缺少必要权限: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (s *Service) requireSafeAuthRequest(w http.ResponseWriter, r *http.Request) bool {
	if !sameOriginRequest(r, s.config.TrustedProxyCIDRs) {
		writeError(w, http.StatusForbidden, "origin_invalid", "请求来源无效")
		return false
	}
	if !secureAuthTransport(r, s.config.TrustedProxyCIDRs) {
		writeError(w, http.StatusUpgradeRequired, "secure_transport_required", "管理登录需要 HTTPS；仅服务端本机可使用 HTTP")
		return false
	}
	return true
}

func secureAuthTransport(r *http.Request, trustedProxies []netip.Prefix) bool {
	return isSecureRequest(r, trustedProxies) || isLoopbackClient(r, trustedProxies)
}

func isSecureRequest(r *http.Request, trustedProxies []netip.Prefix) bool {
	if r.TLS != nil {
		return true
	}
	peer := parseRemoteAddress(r.RemoteAddr)
	if !peer.IsValid() || !addressInPrefixes(peer, trustedProxies) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

func isLoopbackClient(r *http.Request, trustedProxies []netip.Prefix) bool {
	address, err := netip.ParseAddr(clientAddress(r, trustedProxies))
	return err == nil && address.IsLoopback()
}

func sameOriginRequest(r *http.Request, _ []netip.Prefix) bool {
	var protection http.CrossOriginProtection
	return protection.Check(r) == nil
}
