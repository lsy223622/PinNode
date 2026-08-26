package main

import (
	"testing"
	"time"
)

func TestValidateGatewayRoute(t *testing.T) {
	for _, route := range []string{"192.168.1.1/32", "10.0.0.254/32"} {
		if err := validateGatewayRoute(route); err != nil {
			t.Fatalf("合法路由被拒绝 %q: %v", route, err)
		}
	}
	for _, route := range []string{"192.168.1.0/24", "2001:db8::1/128", "not-a-route"} {
		if err := validateGatewayRoute(route); err == nil {
			t.Fatalf("非法路由未被拒绝 %q", route)
		}
	}
}

func TestPairingCodeIsSixDigits(t *testing.T) {
	code, err := newPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	if !validPairingCode(code) {
		t.Fatalf("代码格式错误: %q", code)
	}
}

func TestDefaultSessionConfigIsGatewayOnly(t *testing.T) {
	config, err := DefaultSessionConfig().Normalize()
	if err != nil {
		t.Fatal(err)
	}
	routes := config.EffectiveRoutes("192.168.1.1/32", "192.168.1.0/24")
	if len(routes) != 1 || routes[0] != "192.168.1.1/32" {
		t.Fatalf("默认配置不是网关 /32: %v", routes)
	}
}

func TestSessionConfigRejectsUnapprovedDefaultRoute(t *testing.T) {
	config := DefaultSessionConfig()
	config.AdvertiseRoutes = []string{"0.0.0.0/0"}
	if _, err := config.Normalize(); err == nil {
		t.Fatal("未开启 advertiseExitNode 时接受了默认路由")
	}
}

func TestExitNodeRoutesStayOnCellularBindingSide(t *testing.T) {
	config := DefaultSessionConfig()
	config.AdvertiseExitNode = true
	config.SubnetRouter = true
	config, err := config.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got := config.EffectiveWiFiRoutes("192.168.1.1/32", "192.168.1.0/24"); len(got) != 1 || got[0] != "192.168.1.1/32" {
		t.Fatalf("Exit Node 默认路由错误地进入 Wi-Fi 绑定列表: %v", got)
	}
	routes := config.EffectiveRoutes("192.168.1.1/32", "192.168.1.0/24")
	if len(routes) != 3 || routes[1] != "0.0.0.0/0" || routes[2] != "::/0" {
		t.Fatalf("广告路由没有包含 Exit Node 默认路由: %v", routes)
	}
}

func TestWiFiSubnetTemplatePublishesDetectedSubnet(t *testing.T) {
	config := SessionConfig{
		NetworkMode:         NetworkModeDefault,
		VPNEnabled:          true,
		SubnetRouter:        true,
		AutoWiFiSubnetRoute: true,
	}
	config, err := config.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	routes := config.EffectiveRoutes("192.168.8.1/32", "192.168.8.0/24")
	if len(routes) != 1 || routes[0] != "192.168.8.0/24" {
		t.Fatalf("未发布自动识别的 Wi-Fi 子网: %v", routes)
	}
}

func TestLogoutAtUsesEarliestConfiguredPolicy(t *testing.T) {
	createdAt := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	loginAt := createdAt.Add(10 * time.Minute)
	config := SessionConfig{ExitPolicy: ExitPolicy{AfterConfigSeconds: 3600, AfterLoginSeconds: 60}}
	if got, want := config.LogoutAt(createdAt, loginAt), loginAt.Add(time.Minute); !got.Equal(want) {
		t.Fatalf("退出时间=%v，期望最早策略时间 %v", got, want)
	}
}
