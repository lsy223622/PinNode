package main

import (
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"
)

// Config 是服务端唯一需要的运行配置；OAuth secret 不应写入 APK 或响应日志。
type Config struct {
	ListenAddr        string
	TailscaleBaseURL  string
	TailscaleTailnet  string
	OAuthClientID     string
	OAuthClientSecret string
	CodePepper        string
	AdminToken        string
	CodeTTL           time.Duration
	DatabasePath      string
	ProvisioningTTL   time.Duration
	HeartbeatTTL      time.Duration
	TrustedProxyCIDRs []netip.Prefix
}

func LoadConfig() (Config, error) {
	codeTTL, err := getenvDuration("PINNODE_CODE_TTL", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	provisioningTTL, err := getenvDuration("PINNODE_PROVISIONING_TTL", 10*time.Minute)
	if err != nil {
		return Config{}, err
	}
	heartbeatTTL, err := getenvDuration("PINNODE_HEARTBEAT_TTL", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	trustedProxyCIDRs, err := parsePrefixes(os.Getenv("PINNODE_TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return Config{}, err
	}
	c := Config{
		ListenAddr:        getenv("PINNODE_LISTEN_ADDR", defaultListenAddr),
		TailscaleBaseURL:  getenv("PINNODE_TAILSCALE_BASE_URL", "https://api.tailscale.com"),
		TailscaleTailnet:  getenv("PINNODE_TAILNET", "-"),
		OAuthClientID:     os.Getenv("PINNODE_OAUTH_CLIENT_ID"),
		OAuthClientSecret: os.Getenv("PINNODE_OAUTH_CLIENT_SECRET"),
		CodePepper:        os.Getenv("PINNODE_CODE_PEPPER"),
		AdminToken:        os.Getenv("PINNODE_ADMIN_TOKEN"),
		CodeTTL:           codeTTL,
		DatabasePath:      getenv("PINNODE_DATABASE_PATH", "data/pinnode.db"),
		ProvisioningTTL:   provisioningTTL,
		HeartbeatTTL:      heartbeatTTL,
		TrustedProxyCIDRs: trustedProxyCIDRs,
	}
	if c.OAuthClientID == "" || c.OAuthClientSecret == "" {
		return Config{}, fmt.Errorf("PINNODE_OAUTH_CLIENT_ID 和 PINNODE_OAUTH_CLIENT_SECRET 必须设置")
	}
	if c.CodePepper == "" || len(c.CodePepper) < 32 {
		return Config{}, fmt.Errorf("PINNODE_CODE_PEPPER 必须设置且至少 32 个字符")
	}
	if c.AdminToken == "" || len(c.AdminToken) < 32 {
		return Config{}, fmt.Errorf("PINNODE_ADMIN_TOKEN 必须设置且至少 32 个字符")
	}
	if c.CodeTTL <= 0 {
		return Config{}, fmt.Errorf("PINNODE_CODE_TTL 必须大于 0")
	}
	if c.ProvisioningTTL < time.Minute {
		return Config{}, fmt.Errorf("PINNODE_PROVISIONING_TTL 不能小于 1 分钟")
	}
	if c.HeartbeatTTL < 2*time.Minute {
		return Config{}, fmt.Errorf("PINNODE_HEARTBEAT_TTL 不能小于 2 分钟")
	}
	return c, nil
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s 不是有效时长: %w", key, err)
	}
	return parsed, nil
}

func parsePrefixes(value string) ([]netip.Prefix, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var prefixes []netip.Prefix
	for _, item := range strings.Split(value, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(item))
		if err != nil {
			return nil, fmt.Errorf("PINNODE_TRUSTED_PROXY_CIDRS 包含无效 CIDR %q", item)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}
