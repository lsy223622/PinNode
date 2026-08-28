package main

import (
	"fmt"
	"net/netip"
	"strings"
	"time"
)

const maxConfiguredRoutes = 16

var (
	tailscaleIPv4Range          = netip.MustParsePrefix("100.64.0.0/10")
	tailscaleReservedIPv4Ranges = [...]netip.Prefix{
		netip.MustParsePrefix("100.100.0.0/24"),
		netip.MustParsePrefix("100.100.100.0/24"),
		netip.MustParsePrefix("100.115.92.0/23"),
	}
)

const (
	NetworkModeDefault  = "default"
	NetworkModeCellular = "cellular"

	NetworkExitNone         = ""
	NetworkExitAnyChange    = "any-change"
	NetworkExitWiFiLost     = "wifi-lost"
	NetworkExitCellularLost = "cellular-lost"
)

type ExitPolicy struct {
	OnAppClose         bool   `json:"onAppClose"`
	NetworkChange      string `json:"networkChange"`
	AfterConfigSeconds int64  `json:"afterConfigSeconds"`
	AfterLoginSeconds  int64  `json:"afterLoginSeconds"`
	At                 string `json:"at"`
}

// SessionConfig 是一次服务端受管会话的客户端偏好。
//
// 这个结构只描述管理员允许本次会话使用的配置，不包含 Tailscale
// API access token 或 auth key。默认值刻意保持为“仅发布当前 Wi-Fi 网关 /32”。
type SessionConfig struct {
	NetworkMode            string     `json:"networkMode"`
	VPNEnabled             bool       `json:"vpnEnabled"`
	AcceptRoutes           bool       `json:"acceptRoutes"`
	AcceptDNS              bool       `json:"acceptDNS"`
	TailscaleIP            string     `json:"tailscaleIp"`
	UseExitNode            bool       `json:"useExitNode"`
	ExitNodeID             string     `json:"exitNodeId"`
	ExitNodeIP             string     `json:"exitNodeIp"`
	AutoExitNode           string     `json:"autoExitNode"`
	ExitNodeAllowLANAccess bool       `json:"exitNodeAllowLanAccess"`
	SubnetRouter           bool       `json:"subnetRouter"`
	AutoGatewayRoute       bool       `json:"autoGatewayRoute"`
	AutoWiFiSubnetRoute    bool       `json:"autoWiFiSubnetRoute"`
	AdvertiseRoutes        []string   `json:"advertiseRoutes"`
	AdvertiseExitNode      bool       `json:"advertiseExitNode"`
	DisableSNAT            bool       `json:"disableSNAT"`
	NoStatefulFiltering    bool       `json:"noStatefulFiltering"`
	ShieldsUp              bool       `json:"shieldsUp"`
	RunSSHServer           bool       `json:"runSSHServer"`
	RunWebClient           bool       `json:"runWebClient"`
	PostureChecking        bool       `json:"postureChecking"`
	RemoteConfig           bool       `json:"remoteConfig"`
	Hostname               string     `json:"hostname"`
	NetfilterMode          string     `json:"netfilterMode"`
	AppConnector           bool       `json:"appConnector"`
	ExitPolicy             ExitPolicy `json:"exitPolicy"`
}

// DefaultSessionConfig 返回安全的默认会话配置。
func DefaultSessionConfig() SessionConfig {
	return SessionConfig{
		NetworkMode:      NetworkModeCellular,
		VPNEnabled:       true,
		AcceptRoutes:     true,
		AcceptDNS:        true,
		SubnetRouter:     true,
		AutoGatewayRoute: true,
		AdvertiseRoutes:  []string{},
	}
}

// Normalize 校验并规范化管理端提交的配置，避免把未规范的 CIDR 直接交给
// Android 或 Tailscale 控制面。返回值是可以安全序列化给客户端的副本。
func (c SessionConfig) Normalize() (SessionConfig, error) {
	c.NetworkMode = strings.TrimSpace(strings.ToLower(c.NetworkMode))
	if c.NetworkMode == "" {
		c.NetworkMode = NetworkModeDefault
	}
	if c.NetworkMode != NetworkModeDefault && c.NetworkMode != NetworkModeCellular {
		return SessionConfig{}, fmt.Errorf("networkMode 必须是 default 或 cellular")
	}
	if c.AutoGatewayRoute && c.AutoWiFiSubnetRoute {
		return SessionConfig{}, fmt.Errorf("不能同时自动发布 Wi-Fi 网关和整个 Wi-Fi 子网")
	}
	c.ExitNodeID = strings.TrimSpace(c.ExitNodeID)
	c.ExitNodeIP = strings.TrimSpace(c.ExitNodeIP)
	c.TailscaleIP = strings.TrimSpace(c.TailscaleIP)
	c.AutoExitNode = strings.TrimSpace(c.AutoExitNode)
	c.Hostname = strings.TrimSpace(c.Hostname)
	c.NetfilterMode = strings.TrimSpace(strings.ToLower(c.NetfilterMode))

	if c.TailscaleIP != "" {
		address, err := netip.ParseAddr(c.TailscaleIP)
		if err != nil || !address.Is4() || !tailscaleIPv4Range.Contains(address) {
			return SessionConfig{}, fmt.Errorf("tailscaleIp 不是合法的 Tailscale IPv4 地址")
		}
		for _, reserved := range tailscaleReservedIPv4Ranges {
			if reserved.Contains(address) {
				return SessionConfig{}, fmt.Errorf("tailscaleIp 不是可分配的 Tailscale IPv4 地址")
			}
		}
		c.TailscaleIP = address.String()
	}

	if c.UseExitNode {
		selectors := 0
		if c.ExitNodeID != "" {
			selectors++
		}
		if c.ExitNodeIP != "" {
			selectors++
		}
		if c.AutoExitNode != "" {
			selectors++
		}
		if selectors != 1 {
			return SessionConfig{}, fmt.Errorf("启用 Exit Node 时必须且只能填写一个 exitNodeId、exitNodeIp 或 autoExitNode")
		}
	} else {
		// 关闭 Exit Node 时清除旧选择，避免受管节点复用时留下隐式状态。
		c.ExitNodeID = ""
		c.ExitNodeIP = ""
		c.AutoExitNode = ""
	}

	if c.ExitNodeIP != "" {
		address, err := netip.ParseAddr(c.ExitNodeIP)
		if err != nil || !address.IsValid() {
			return SessionConfig{}, fmt.Errorf("exitNodeIp 不是合法 IP 地址")
		}
		c.ExitNodeIP = address.String()
	}
	if c.AutoExitNode != "" && c.AutoExitNode != "auto:any" {
		return SessionConfig{}, fmt.Errorf("当前版本只支持 autoExitNode=auto:any")
	}
	if c.NetfilterMode != "" && c.NetfilterMode != "on" && c.NetfilterMode != "off" && c.NetfilterMode != "nodivert" {
		return SessionConfig{}, fmt.Errorf("netfilterMode 必须是 on、off、nodivert 或空值")
	}
	if len(c.Hostname) > 255 || strings.ContainsAny(c.Hostname, "\r\n") {
		return SessionConfig{}, fmt.Errorf("hostname 过长或包含换行符")
	}

	c.ExitPolicy.NetworkChange = strings.TrimSpace(strings.ToLower(c.ExitPolicy.NetworkChange))
	switch c.ExitPolicy.NetworkChange {
	case NetworkExitNone, NetworkExitAnyChange, NetworkExitWiFiLost, NetworkExitCellularLost:
	default:
		return SessionConfig{}, fmt.Errorf("exitPolicy.networkChange 无效")
	}
	const maxPolicySeconds = int64((365 * 24 * time.Hour) / time.Second)
	if c.ExitPolicy.AfterConfigSeconds < 0 || c.ExitPolicy.AfterConfigSeconds > maxPolicySeconds {
		return SessionConfig{}, fmt.Errorf("exitPolicy.afterConfigSeconds 必须在 0 到 %d 之间", maxPolicySeconds)
	}
	if c.ExitPolicy.AfterLoginSeconds < 0 || c.ExitPolicy.AfterLoginSeconds > maxPolicySeconds {
		return SessionConfig{}, fmt.Errorf("exitPolicy.afterLoginSeconds 必须在 0 到 %d 之间", maxPolicySeconds)
	}
	if c.ExitPolicy.At != "" {
		at, err := time.Parse(time.RFC3339, c.ExitPolicy.At)
		if err != nil {
			return SessionConfig{}, fmt.Errorf("exitPolicy.at 必须是 RFC3339 时间")
		}
		c.ExitPolicy.At = at.UTC().Format(time.RFC3339)
	}

	if c.AdvertiseRoutes == nil {
		c.AdvertiseRoutes = []string{}
	}
	if len(c.AdvertiseRoutes) > maxConfiguredRoutes {
		return SessionConfig{}, fmt.Errorf("advertiseRoutes 最多允许 %d 条", maxConfiguredRoutes)
	}
	routes := make([]string, 0, len(c.AdvertiseRoutes))
	seen := make(map[string]struct{}, len(c.AdvertiseRoutes))
	for _, raw := range c.AdvertiseRoutes {
		route := strings.TrimSpace(raw)
		prefix, err := netip.ParsePrefix(route)
		if err != nil || !prefix.IsValid() || prefix != prefix.Masked() {
			return SessionConfig{}, fmt.Errorf("advertiseRoutes 包含非法或非网络地址 CIDR: %q", raw)
		}
		if prefix.Bits() == 0 && !c.AdvertiseExitNode {
			return SessionConfig{}, fmt.Errorf("默认路由只能在开启 advertiseExitNode 时使用")
		}
		canonical := prefix.String()
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		routes = append(routes, canonical)
	}
	c.AdvertiseRoutes = routes
	return c, nil
}

// EffectiveRoutes 计算本次会话实际交给 Tailscale 的 route 列表。
// autoGatewayRoute 是默认最小权限路径；管理员显式配置的 subnet 和 exit
// node 默认路由会在此基础上合并并去重。
func (c SessionConfig) EffectiveRoutes(gatewayRoute, wifiSubnetRoute string) []string {
	routes := make([]string, 0, len(c.AdvertiseRoutes)+3)
	seen := make(map[string]struct{})
	add := func(route string) {
		if route == "" {
			return
		}
		if _, exists := seen[route]; exists {
			return
		}
		seen[route] = struct{}{}
		routes = append(routes, route)
	}
	for _, route := range c.EffectiveWiFiRoutes(gatewayRoute, wifiSubnetRoute) {
		add(route)
	}
	if c.AdvertiseExitNode {
		add("0.0.0.0/0")
		add("::/0")
	}
	return routes
}

// EffectiveWiFiRoutes 只返回应该绑定到家庭 Wi-Fi 的目标前缀。
// Exit Node 的默认路由属于远程 peer 到互联网的转发目标，必须留在蜂窝
// Network 上，因此不会出现在这个列表中。
func (c SessionConfig) EffectiveWiFiRoutes(gatewayRoute, wifiSubnetRoute string) []string {
	if !c.SubnetRouter {
		return []string{}
	}
	routes := make([]string, 0, len(c.AdvertiseRoutes)+1)
	seen := make(map[string]struct{})
	add := func(route string) {
		if route == "" {
			return
		}
		if _, exists := seen[route]; exists {
			return
		}
		seen[route] = struct{}{}
		routes = append(routes, route)
	}
	if c.AutoGatewayRoute {
		add(gatewayRoute)
	}
	if c.AutoWiFiSubnetRoute {
		add(wifiSubnetRoute)
	}
	for _, route := range c.AdvertiseRoutes {
		prefix, err := netip.ParsePrefix(route)
		if err == nil && prefix.Bits() != 0 {
			add(route)
		}
	}
	return routes
}

func (c SessionConfig) RequiresWiFi() bool {
	return c.SubnetRouter && (c.AutoGatewayRoute || c.AutoWiFiSubnetRoute)
}

func (c SessionConfig) LogoutAt(configCreatedAt, loginAt time.Time) time.Time {
	var candidates []time.Time
	if c.ExitPolicy.AfterConfigSeconds > 0 {
		candidates = append(candidates, configCreatedAt.Add(time.Duration(c.ExitPolicy.AfterConfigSeconds)*time.Second))
	}
	if c.ExitPolicy.AfterLoginSeconds > 0 {
		candidates = append(candidates, loginAt.Add(time.Duration(c.ExitPolicy.AfterLoginSeconds)*time.Second))
	}
	if c.ExitPolicy.At != "" {
		if at, err := time.Parse(time.RFC3339, c.ExitPolicy.At); err == nil {
			candidates = append(candidates, at)
		}
	}
	var earliest time.Time
	for _, candidate := range candidates {
		if earliest.IsZero() || candidate.Before(earliest) {
			earliest = candidate
		}
	}
	return earliest
}
