package main

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"time"
)

func (s *Service) handleAdminSessionStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	if !s.requireAdminAPI(w, r, true) {
		return
	}
	const prefix = "/v1/admin/sessions/"
	path := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "stop" {
		writeError(w, http.StatusNotFound, "session_path_not_found", "会话路径不存在")
		return
	}

	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if _, ok, err := s.store.GetSession(parts[0]); err != nil {
		s.logger.Errorf("cleanup", "读取管理员清理会话失败: sessionRef=%s error=%s", diagnosticIdentifier(parts[0]), safeDiagnosticError(err))
		writeError(w, http.StatusInternalServerError, "session_read_failed", "读取会话失败")
		return
	} else if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "会话不存在")
		return
	}
	status, responseStatus, startErr, cleanupErr := s.stopSessionResult(r.Context(), parts[0], "admin_stop")
	if startErr != nil {
		s.logger.Errorf("cleanup", "管理员开始清理会话失败: sessionRef=%s error=%s", diagnosticIdentifier(parts[0]), safeDiagnosticError(startErr))
		writeError(w, http.StatusInternalServerError, "session_cleanup_start_failed", "开始清理会话失败")
		return
	}
	if cleanupErr != nil {
		writeError(w, http.StatusBadGateway, "session_cleanup_failed", "清理 Tailscale 节点失败，请稍后重试")
		return
	}
	if responseStatus == http.StatusConflict {
		writeError(w, responseStatus, "session_state_conflict", "会话当前不能开始清理")
		return
	}
	writeJSON(w, responseStatus, map[string]any{
		"sessionId": parts[0],
		"status":    status,
	})
}

func (s *Service) handleAdminSessionConfig(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	if !s.requireAdminAPI(w, r, true) {
		return
	}
	var request adminSessionConfigUpdateRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.ExpectedRevision < 1 {
		writeError(w, http.StatusBadRequest, "config_patch_invalid", "expectedRevision 必须是正整数")
		return
	}

	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		s.logger.Errorf("session", "读取管理员配置会话失败: sessionRef=%s error=%s", diagnosticIdentifier(sessionID), safeDiagnosticError(err))
		writeError(w, http.StatusInternalServerError, "session_read_failed", "读取会话失败")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "会话不存在")
		return
	}
	if request.ExpectedRevision != session.ConfigRevision {
		writeConfigRevisionConflict(w, session)
		return
	}
	if session.Status != SessionProvisioning && session.Status != SessionActive {
		writeError(w, http.StatusConflict, "session_state_conflict", "会话当前不能编辑配置")
		return
	}
	now := time.Now()
	if sessionConfigExpired(session, now) {
		writeError(w, http.StatusGone, "session_expired", "会话已过期，不能通过配置修改重新激活")
		return
	}
	if session.Status == SessionActive && session.RouteReconcilePending {
		writeError(w, http.StatusBadGateway, "tailscale_route_update_failed", "会话路由正在恢复，请稍后重试")
		return
	}

	config := applySessionConfigPatch(session.Config, request.Config)
	policy, err := CompileSessionPolicy(config, session.Route, session.WiFiSubnetRoute)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_patch_invalid", "配置无效: "+err.Error())
		return
	}
	config = policy.Config
	if session.Status == SessionActive && config.TailscaleIP != session.Config.TailscaleIP {
		writeError(w, http.StatusConflict, "active_tailscale_ip_change_unsupported", "活动会话不支持修改或清空 tailscaleIp")
		return
	}
	if config.AutoWiFiSubnetRoute && strings.TrimSpace(session.WiFiSubnetRoute) == "" {
		writeError(w, http.StatusConflict, "session_route_context_missing", "该历史会话缺少原始 Wi-Fi 子网信息，无法安全重算路由")
		return
	}
	loginAt := session.ConnectedAt
	if loginAt.IsZero() {
		loginAt = session.CreatedAt
	}
	configCreatedAt := s.sessionConfigCreatedAt(session)
	expiresAt := config.LogoutAt(configCreatedAt, loginAt)
	if !expiresAt.IsZero() && !expiresAt.After(now) {
		writeError(w, http.StatusGone, "session_expired", "配置的自动退出时间已经到达")
		return
	}
	noChange := reflect.DeepEqual(config, session.Config) &&
		reflect.DeepEqual(policy.Routes, session.Routes) &&
		reflect.DeepEqual(policy.WiFiRoutes, session.WiFiRoutes) &&
		expiresAt.Equal(session.ExpiresAt)
	if noChange {
		writeJSON(w, http.StatusOK, adminSessionConfigSnapshot(session, policy))
		return
	}
	missing := missingClientCapabilities(session.ClientCapabilities, policy.RequiredClientCapabilities)
	if len(missing) != 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": apiErrorBody{
				Code:      "client_capability_missing",
				Message:   "客户端缺少必要能力: " + strings.Join(missing, ", "),
				Retryable: false,
			},
			"requestId":            w.Header().Get("X-Request-ID"),
			"missingCapabilities":  missing,
			"requiredCapabilities": policy.RequiredClientCapabilities,
		})
		return
	}
	if policy.RequiresApproval && !request.ConfirmHighRisk {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": apiErrorBody{
				Code:      "high_risk_confirmation_required",
				Message:   "该配置属于高风险变更，请确认后重试",
				Retryable: false,
			},
			"requestId":   w.Header().Get("X-Request-ID"),
			"riskLevel":   policy.RiskLevel,
			"riskReasons": policy.RiskReasons,
		})
		return
	}

	updatedSession := session
	updatedSession.Config = config
	syncDeadline := time.Time{}
	if session.Status == SessionActive {
		syncDeadline = s.syncDeadline(updatedSession, now)
	}
	routesChanged := session.Status == SessionActive && !reflect.DeepEqual(policy.Routes, session.Routes)
	routesUpdated := false
	if routesChanged {
		if session.DeviceID == "" {
			writeError(w, http.StatusBadGateway, "tailscale_route_update_failed", "活动会话没有可更新的 Tailscale 设备")
			return
		}
		accessToken, credentialErr := s.credentialToken(r.Context(), session.CredentialID)
		if credentialErr != nil {
			s.logger.Errorf("tailscale", "读取配置更新凭据失败: sessionRef=%s error=%s", diagnosticIdentifier(session.ID), safeDiagnosticError(credentialErr))
			writeError(w, http.StatusBadGateway, "tailscale_route_update_failed", "Tailscale 控制面路由更新失败，请稍后重试")
			return
		}
		marked, markErr := s.store.MarkSessionRouteReconcile(session.ID, request.ExpectedRevision, now)
		if markErr != nil {
			s.logger.Errorf("session", "标记活动会话路由恢复失败: sessionRef=%s error=%s", diagnosticIdentifier(session.ID), safeDiagnosticError(markErr))
			writeError(w, http.StatusInternalServerError, "session_config_update_failed", "保存会话配置失败")
			return
		}
		if !marked {
			current, found, readErr := s.store.GetSession(session.ID)
			if readErr == nil && found && current.ConfigRevision == request.ExpectedRevision {
				writeError(w, http.StatusBadGateway, "tailscale_route_update_failed", "会话路由正在恢复，请稍后重试")
				return
			}
			if readErr == nil && found {
				writeConfigRevisionConflict(w, current)
				return
			}
			writeError(w, http.StatusConflict, "config_revision_conflict", "会话配置 revision 已变化，请刷新后重试")
			return
		}
		if err := s.tailscale.SetDeviceRoutes(r.Context(), accessToken, session.DeviceID, policy.Routes); err != nil {
			s.logger.Errorf("tailscale", "更新活动会话路由失败: sessionRef=%s nodeRef=%s error=%s", diagnosticIdentifier(session.ID), diagnosticIdentifier(session.DeviceID), safeDiagnosticError(err))
			if restoreErr := s.restoreSessionRoutesWithToken(r.Context(), accessToken, session); restoreErr == nil {
				s.clearSessionRouteReconcile(session.ID)
			} else {
				s.logger.Errorf("tailscale", "路由更新失败后恢复旧路由失败: sessionRef=%s nodeRef=%s error=%s", diagnosticIdentifier(session.ID), diagnosticIdentifier(session.DeviceID), safeDiagnosticError(restoreErr))
			}
			writeError(w, http.StatusBadGateway, "tailscale_route_update_failed", "Tailscale 控制面路由更新失败，请稍后重试")
			return
		}
		routesUpdated = true
	}
	updated, err := s.store.UpdateSessionConfigCAS(
		session.ID, request.ExpectedRevision, config, policy.Routes, policy.WiFiRoutes,
		expiresAt, syncDeadline, now,
	)
	if err != nil {
		if routesUpdated {
			if restoreErr := s.restoreSessionRoutes(r.Context(), session); restoreErr == nil {
				s.clearSessionRouteReconcile(session.ID)
			} else {
				s.logger.Errorf("tailscale", "保存配置失败后恢复旧路由失败: sessionRef=%s nodeRef=%s error=%s", diagnosticIdentifier(session.ID), diagnosticIdentifier(session.DeviceID), safeDiagnosticError(restoreErr))
			}
		}
		s.logger.Errorf("session", "保存管理员配置失败: sessionRef=%s error=%s", diagnosticIdentifier(session.ID), safeDiagnosticError(err))
		writeError(w, http.StatusInternalServerError, "session_config_update_failed", "保存会话配置失败")
		return
	}
	if !updated {
		if routesUpdated {
			if restoreErr := s.restoreSessionRoutes(r.Context(), session); restoreErr == nil {
				s.clearSessionRouteReconcile(session.ID)
			} else {
				s.logger.Errorf("tailscale", "revision 冲突后恢复旧路由失败: sessionRef=%s nodeRef=%s error=%s", diagnosticIdentifier(session.ID), diagnosticIdentifier(session.DeviceID), safeDiagnosticError(restoreErr))
			}
		}
		current, found, readErr := s.store.GetSession(session.ID)
		if readErr == nil && found {
			writeConfigRevisionConflict(w, current)
			return
		}
		writeError(w, http.StatusConflict, "config_revision_conflict", "会话配置 revision 已变化，请刷新后重试")
		return
	}
	updatedSession, found, err := s.store.GetSession(session.ID)
	if err != nil || !found {
		writeError(w, http.StatusInternalServerError, "session_config_update_failed", "读取更新后的会话配置失败")
		return
	}
	s.events.publishState("config_updated")
	updatedPolicy, _ := CompileSessionPolicy(updatedSession.Config, updatedSession.Route, updatedSession.WiFiSubnetRoute)
	writeJSON(w, http.StatusOK, adminSessionConfigSnapshot(updatedSession, updatedPolicy))
}

func (s *Service) restoreSessionRoutes(ctx context.Context, session Session) error {
	accessToken, err := s.credentialToken(ctx, session.CredentialID)
	if err != nil {
		s.logger.Errorf("tailscale", "恢复旧会话路由时读取凭据失败: sessionRef=%s error=%s", diagnosticIdentifier(session.ID), safeDiagnosticError(err))
		return err
	}
	return s.restoreSessionRoutesWithToken(ctx, accessToken, session)
}

func (s *Service) restoreSessionRoutesWithToken(ctx context.Context, accessToken string, session Session) error {
	if err := s.tailscale.SetDeviceRoutes(ctx, accessToken, session.DeviceID, session.Routes); err != nil {
		s.logger.Errorf("tailscale", "恢复旧会话路由失败: sessionRef=%s nodeRef=%s error=%s", diagnosticIdentifier(session.ID), diagnosticIdentifier(session.DeviceID), safeDiagnosticError(err))
		return err
	}
	return nil
}

func (s *Service) clearSessionRouteReconcile(sessionID string) {
	if _, err := s.store.ClearSessionRouteReconcile(sessionID, time.Now()); err != nil {
		s.logger.Errorf("session", "清除会话路由恢复标记失败: sessionRef=%s error=%s", diagnosticIdentifier(sessionID), safeDiagnosticError(err))
	}
}

func sessionConfigExpired(session Session, now time.Time) bool {
	if !session.ExpiresAt.IsZero() && !now.Before(session.ExpiresAt) {
		return true
	}
	if session.Status == SessionProvisioning {
		return !session.ProvisioningDeadline.IsZero() && !now.Before(session.ProvisioningDeadline)
	}
	return session.Status == SessionActive && session.Config.ExitPolicy.OnAppClose &&
		!session.SyncDeadline.IsZero() && !now.Before(session.SyncDeadline)
}

func (s *Service) sessionConfigCreatedAt(session Session) time.Time {
	if session.PairingCodeHash != "" {
		if code, ok, err := s.store.GetPairingCode(session.PairingCodeHash); err == nil && ok && !code.CreatedAt.IsZero() {
			return code.CreatedAt
		}
	}
	return session.CreatedAt
}

func sessionApplyState(session Session) string {
	switch session.Status {
	case SessionCleaning:
		return "cleanup_in_progress"
	case SessionCleanupFailed:
		return "cleanup_failed"
	case SessionProvisioning:
		return "pending_client_attach"
	case SessionActive:
		if session.RouteReconcilePending {
			return "route_reconcile_pending"
		}
		if session.ConfigRevision < 1 || session.AppliedConfigRevision < 0 || session.AppliedConfigRevision > session.ConfigRevision {
			return "unknown"
		}
		if session.ConfigRevision == session.AppliedConfigRevision {
			return "synced"
		}
		return "pending_client_sync"
	default:
		return "unknown"
	}
}

func routeContextAvailable(session Session) bool {
	if session.Config.AutoWiFiSubnetRoute && strings.TrimSpace(session.WiFiSubnetRoute) == "" {
		return false
	}
	return true
}

func adminSessionConfigSnapshot(session Session, policy CompiledSessionPolicy) map[string]any {
	return map[string]any{
		"sessionId":             session.ID,
		"status":                session.Status,
		"configRevision":        session.ConfigRevision,
		"appliedConfigRevision": session.AppliedConfigRevision,
		"applyState":            sessionApplyState(session),
		"config":                cloneSessionConfig(session.Config),
		"gatewayRoute":          session.Route,
		"routes":                append([]string{}, session.Routes...),
		"wifiRoutes":            append([]string{}, session.WiFiRoutes...),
		"expiresAt":             formatOptionalTime(session.ExpiresAt),
		"policy":                publicSessionPolicy(policy),
		"routeContextAvailable": routeContextAvailable(session),
	}
}

func publicSessionPolicy(policy CompiledSessionPolicy) map[string]any {
	return map[string]any{
		"riskLevel":                  policy.RiskLevel,
		"riskReasons":                append([]string{}, policy.RiskReasons...),
		"requiredClientCapabilities": append([]string{}, policy.RequiredClientCapabilities...),
		"optionalClientCapabilities": append([]string{}, policy.OptionalClientCapabilities...),
		"requiresApproval":           policy.RequiresApproval,
	}
}

func writeConfigRevisionConflict(w http.ResponseWriter, session Session) {
	policy, err := CompileSessionPolicy(session.Config, session.Route, session.WiFiSubnetRoute)
	if err != nil {
		policy = CompiledSessionPolicy{Config: cloneSessionConfig(session.Config), Routes: session.Routes, WiFiRoutes: session.WiFiRoutes}
	}
	response := adminSessionConfigSnapshot(session, policy)
	response["error"] = apiErrorBody{
		Code:      "config_revision_conflict",
		Message:   "会话配置 revision 已变化，请刷新后重试",
		Retryable: false,
	}
	response["requestId"] = w.Header().Get("X-Request-ID")
	writeJSON(w, http.StatusConflict, response)
}
