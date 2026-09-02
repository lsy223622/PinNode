package main

import (
	"strings"
	"testing"
)

func TestCompileSessionPolicySeparatesRescueRoutesFromExitRoutes(t *testing.T) {
	config := DefaultSessionConfig()
	policy, err := CompileSessionPolicy(config, "192.168.1.1/32", "192.168.1.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Routes) != 1 || policy.Routes[0] != "192.168.1.1/32" {
		t.Fatalf("救援配置生成了错误的完整路由: %v", policy.Routes)
	}
	if len(policy.WiFiRoutes) != 1 || policy.WiFiRoutes[0] != "192.168.1.1/32" {
		t.Fatalf("救援配置生成了错误的 Wi-Fi 路由: %v", policy.WiFiRoutes)
	}
	if containsString(policy.Routes, "0.0.0.0/0") || containsString(policy.Routes, "::/0") {
		t.Fatalf("救援网关配置意外生成默认路由: %v", policy.Routes)
	}
	if !containsString(policy.RequiredClientCapabilities, clientCapabilitySessionSync) ||
		!containsString(policy.RequiredClientCapabilities, clientCapabilityRescueRouting) ||
		!containsString(policy.RequiredClientCapabilities, clientCapabilityRouteAdvert) {
		t.Fatalf("救援配置缺少必要能力: %v", policy.RequiredClientCapabilities)
	}
}

func TestCompileSessionPolicyDoesNotPublishLanForOrdinaryNode(t *testing.T) {
	policy, err := CompileSessionPolicy(SessionConfig{NetworkMode: NetworkModeDefault}, "192.168.1.1/32", "192.168.1.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Routes) != 0 || len(policy.WiFiRoutes) != 0 {
		t.Fatalf("普通节点意外生成 LAN 路由: routes=%v wifiRoutes=%v", policy.Routes, policy.WiFiRoutes)
	}
}

func TestCompileSessionPolicyMarksHighRiskBoundaries(t *testing.T) {
	config := DefaultSessionConfig()
	config.NetworkMode = NetworkModeDefault
	config.SubnetRouter = false
	config.AutoGatewayRoute = false
	config.AdvertiseExitNode = true
	config.RemoteConfig = true
	config.NetfilterMode = "off"
	policy, err := CompileSessionPolicy(config, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if policy.RiskLevel != SessionRiskHigh || !policy.RequiresApproval {
		t.Fatalf("高风险策略未标记: %+v", policy)
	}
	if !containsString(policy.RiskReasons, "advertise_exit_node") ||
		!containsString(policy.RiskReasons, "advanced_preferences") {
		t.Fatalf("高风险原因不完整: %v", policy.RiskReasons)
	}
	if !containsString(policy.RequiredClientCapabilities, clientCapabilityAdvancedPrefs) {
		t.Fatalf("高级偏好缺少能力门槛: %v", policy.RequiredClientCapabilities)
	}
	if !containsString(policy.Routes, "0.0.0.0/0") || !containsString(policy.Routes, "::/0") {
		t.Fatalf("Exit Node 默认路由未编译: %v", policy.Routes)
	}
}

func TestCompileSessionPolicyFailsClosedForIgnoredLanRoutes(t *testing.T) {
	config := DefaultSessionConfig()
	config.SubnetRouter = false
	config.AutoGatewayRoute = true
	if _, err := CompileSessionPolicy(config, "192.168.1.1/32", ""); err == nil ||
		!strings.Contains(err.Error(), "subnetRouter") {
		t.Fatalf("关闭 subnetRouter 后仍静默忽略 LAN 路由: %v", err)
	}
}

func TestCompileSessionPolicyRejectsIgnoredExitNodeLanAccess(t *testing.T) {
	config := SessionConfig{NetworkMode: NetworkModeDefault, ExitNodeAllowLANAccess: true}
	if _, err := CompileSessionPolicy(config, "", ""); err == nil ||
		!strings.Contains(err.Error(), "exitNodeAllowLanAccess") {
		t.Fatalf("关闭 Exit Node 后仍静默接受 LAN 放行字段: %v", err)
	}
}

func TestMissingClientCapabilitiesIsStable(t *testing.T) {
	missing := missingClientCapabilities(
		[]string{clientCapabilitySessionSync},
		[]string{clientCapabilitySessionSync, clientCapabilityRescueRouting, clientCapabilityRouteAdvert},
	)
	if len(missing) != 2 || missing[0] != clientCapabilityRescueRouting || missing[1] != clientCapabilityRouteAdvert {
		t.Fatalf("能力缺失计算不稳定: %v", missing)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
