package main

import (
	"fmt"
	"net/netip"
	"sort"
)

const (
	clientCapabilitySessionSync   = "session-sync-v1"
	clientCapabilityRescueRouting = "rescue-routing-v1"
	clientCapabilityRouteAdvert   = "route-advertisement-v1"
	clientCapabilityExitNode      = "exit-node-v1"
	clientCapabilityAdvancedPrefs = "advanced-prefs-v1"
	clientCapabilityStateReport   = "client-state-report-v1"
	clientCapabilityLogs          = "client-logs-v1"
)

type SessionRiskLevel string

const (
	SessionRiskLow      SessionRiskLevel = "low"
	SessionRiskElevated SessionRiskLevel = "elevated"
	SessionRiskHigh     SessionRiskLevel = "high"
)

// CompiledSessionPolicy is the small server-side boundary between the
// normalized admin configuration and the values sent to the client/control
// plane.  It intentionally does not model the full Tailscale preference set.
type CompiledSessionPolicy struct {
	Config                     SessionConfig
	Routes                     []string
	WiFiRoutes                 []string
	RiskLevel                  SessionRiskLevel
	RiskReasons                []string
	RequiredClientCapabilities []string
	OptionalClientCapabilities []string
	RequiresApproval           bool
}

func CompileSessionPolicy(
	config SessionConfig,
	gatewayRoute string,
	wifiSubnetRoute string,
) (CompiledSessionPolicy, error) {
	normalized, err := config.Normalize()
	if err != nil {
		return CompiledSessionPolicy{}, err
	}
	if !normalized.SubnetRouter &&
		(normalized.AutoGatewayRoute || normalized.AutoWiFiSubnetRoute || len(normalized.AdvertiseRoutes) != 0) {
		return CompiledSessionPolicy{}, fmt.Errorf("关闭 subnetRouter 时不能配置要发布的 LAN 路由")
	}

	routes := normalized.EffectiveRoutes(gatewayRoute, wifiSubnetRoute)
	wifiRoutes := normalized.EffectiveWiFiRoutes(gatewayRoute, wifiSubnetRoute)
	if err := validateCompiledRoutes(normalized, routes, wifiRoutes); err != nil {
		return CompiledSessionPolicy{}, err
	}

	policy := CompiledSessionPolicy{
		Config:                     normalized,
		Routes:                     append([]string{}, routes...),
		WiFiRoutes:                 append([]string{}, wifiRoutes...),
		RiskLevel:                  SessionRiskLow,
		OptionalClientCapabilities: []string{clientCapabilityStateReport, clientCapabilityLogs},
	}
	policy.addRequired(clientCapabilitySessionSync)

	if normalized.NetworkMode == NetworkModeCellular || len(wifiRoutes) != 0 {
		policy.addRequired(clientCapabilityRescueRouting)
		policy.addRisk(SessionRiskElevated, "network_binding")
	}
	if len(wifiRoutes) != 0 || normalized.AdvertiseExitNode {
		policy.addRequired(clientCapabilityRouteAdvert)
		policy.addRisk(SessionRiskElevated, "route_advertisement")
	}
	if normalized.UseExitNode {
		policy.addRequired(clientCapabilityExitNode)
		policy.addRisk(SessionRiskHigh, "exit_node")
	}
	if normalized.AdvertiseExitNode {
		policy.addRisk(SessionRiskHigh, "advertise_exit_node")
	}
	if hasAdvancedPreference(normalized) {
		policy.addRequired(clientCapabilityAdvancedPrefs)
		policy.addRisk(SessionRiskHigh, "advanced_preferences")
	}
	policy.RequiresApproval = policy.RiskLevel == SessionRiskHigh
	return policy, nil
}

func (p *CompiledSessionPolicy) addRequired(capability string) {
	for _, existing := range p.RequiredClientCapabilities {
		if existing == capability {
			return
		}
	}
	p.RequiredClientCapabilities = append(p.RequiredClientCapabilities, capability)
	sort.Strings(p.RequiredClientCapabilities)
}

func (p *CompiledSessionPolicy) addRisk(level SessionRiskLevel, reason string) {
	if riskRank(level) > riskRank(p.RiskLevel) {
		p.RiskLevel = level
	}
	for _, existing := range p.RiskReasons {
		if existing == reason {
			return
		}
	}
	p.RiskReasons = append(p.RiskReasons, reason)
	sort.Strings(p.RiskReasons)
}

func riskRank(level SessionRiskLevel) int {
	switch level {
	case SessionRiskHigh:
		return 2
	case SessionRiskElevated:
		return 1
	default:
		return 0
	}
}

func hasAdvancedPreference(config SessionConfig) bool {
	return config.DisableSNAT ||
		config.NoStatefulFiltering ||
		config.ShieldsUp ||
		config.RunSSHServer ||
		config.RunWebClient ||
		config.PostureChecking ||
		config.RemoteConfig ||
		config.NetfilterMode != "" ||
		config.AppConnector ||
		(config.UseExitNode && config.ExitNodeAllowLANAccess)
}

func validateCompiledRoutes(config SessionConfig, routes, wifiRoutes []string) error {
	for _, route := range routes {
		prefix, err := netip.ParsePrefix(route)
		if err != nil || !prefix.IsValid() || prefix != prefix.Masked() {
			return fmt.Errorf("策略编译生成了非法路由")
		}
		if prefix.Bits() == 0 && !config.AdvertiseExitNode {
			return fmt.Errorf("未开启 advertiseExitNode 时不能生成默认路由")
		}
	}
	for _, route := range wifiRoutes {
		prefix, err := netip.ParsePrefix(route)
		if err != nil || !prefix.IsValid() || prefix.Bits() == 0 {
			return fmt.Errorf("Wi-Fi 路由不能是默认路由")
		}
	}
	return nil
}

func missingClientCapabilities(have, required []string) []string {
	set := make(map[string]struct{}, len(have))
	for _, capability := range have {
		set[capability] = struct{}{}
	}
	missing := make([]string, 0)
	for _, capability := range required {
		if _, ok := set[capability]; !ok {
			missing = append(missing, capability)
		}
	}
	return missing
}
