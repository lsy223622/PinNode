package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
)

// Config 是服务端唯一需要的运行配置；Tailscale credential 只从加密数据库读取。
type Config struct {
	ListenAddr        string
	TailscaleBaseURL  string
	TailscaleTailnet  string
	CodePepper        string
	CredentialKey     []byte
	CodeTTL           time.Duration
	DatabasePath      string
	ProvisioningTTL   time.Duration
	HeartbeatTTL      time.Duration
	AdminSessionTTL   time.Duration
	PoWDifficulty     int
	AllowRemoteSetup  bool
	TrustedProxyCIDRs []netip.Prefix
}

func LoadConfig() (Config, error) {
	databasePath := getenv("PINNODE_DATABASE_PATH", "data/pinnode.db")
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
	adminSessionTTL, err := getenvDuration("PINNODE_ADMIN_SESSION_TTL", 12*time.Hour)
	if err != nil {
		return Config{}, err
	}
	credentialKey, codePepper, err := loadApplicationSecrets(databasePath)
	if err != nil {
		return Config{}, err
	}
	powDifficulty, err := getenvInt("PINNODE_POW_DIFFICULTY", 18)
	if err != nil {
		return Config{}, err
	}
	allowRemoteSetup, err := getenvBool("PINNODE_ALLOW_REMOTE_SETUP", false)
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
		CodePepper:        codePepper,
		CredentialKey:     credentialKey,
		CodeTTL:           codeTTL,
		DatabasePath:      databasePath,
		ProvisioningTTL:   provisioningTTL,
		HeartbeatTTL:      heartbeatTTL,
		AdminSessionTTL:   adminSessionTTL,
		PoWDifficulty:     powDifficulty,
		AllowRemoteSetup:  allowRemoteSetup,
		TrustedProxyCIDRs: trustedProxyCIDRs,
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
	if c.AdminSessionTTL < 15*time.Minute || c.AdminSessionTTL > 7*24*time.Hour {
		return Config{}, fmt.Errorf("PINNODE_ADMIN_SESSION_TTL 必须在 15 分钟到 7 天之间")
	}
	if c.PoWDifficulty < 16 || c.PoWDifficulty > 24 {
		return Config{}, fmt.Errorf("PINNODE_POW_DIFFICULTY 必须在 16 到 24 之间")
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

func getenvInt(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s 不是有效整数: %w", key, err)
	}
	return parsed, nil
}

func getenvBool(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s 不是有效布尔值: %w", key, err)
	}
	return parsed, nil
}

func loadApplicationSecrets(databasePath string) ([]byte, string, error) {
	credentialOverride := strings.TrimSpace(os.Getenv("PINNODE_CREDENTIAL_KEY"))
	pepperOverride := os.Getenv("PINNODE_CODE_PEPPER")
	var credentialKey, pepperKey []byte

	if credentialOverride == "" || pepperOverride == "" {
		var rootKey []byte
		var err error
		if encoded := strings.TrimSpace(os.Getenv("PINNODE_INSTANCE_KEY")); encoded != "" {
			rootKey, err = decode32ByteKey(encoded, "PINNODE_INSTANCE_KEY")
		} else {
			secretPath := getenv("PINNODE_SECRET_PATH", defaultSecretPath(databasePath))
			rootKey, err = loadOrCreateInstanceKey(secretPath)
		}
		if err != nil {
			return nil, "", err
		}
		credentialKey, err = deriveInstanceKey(rootKey, "pinnode/credential-encryption/v1")
		if err != nil {
			return nil, "", err
		}
		pepperKey, err = deriveInstanceKey(rootKey, "pinnode/pairing-code-hmac/v1")
		if err != nil {
			return nil, "", err
		}
	}

	if credentialOverride != "" {
		var err error
		credentialKey, err = decode32ByteKey(credentialOverride, "PINNODE_CREDENTIAL_KEY")
		if err != nil {
			return nil, "", err
		}
	}
	codePepper := string(pepperKey)
	if pepperOverride != "" {
		if len(pepperOverride) < 32 {
			return nil, "", fmt.Errorf("PINNODE_CODE_PEPPER 必须至少 32 个字符")
		}
		codePepper = pepperOverride
	}
	return credentialKey, codePepper, nil
}

func decode32ByteKey(value, name string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding,
	} {
		if decoded, err := encoding.DecodeString(value); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("%s 必须是 base64 编码的 32 字节随机密钥", name)
}

func defaultSecretPath(databasePath string) string {
	if databasePath == ":memory:" || strings.HasPrefix(databasePath, "file:") {
		return filepath.Join("data", "pinnode.secret")
	}
	return filepath.Join(filepath.Dir(databasePath), "pinnode.secret")
}

func loadOrCreateInstanceKey(secretPath string) ([]byte, error) {
	secretPath = strings.TrimSpace(secretPath)
	if secretPath == "" {
		return nil, fmt.Errorf("PINNODE_SECRET_PATH 不能为空")
	}
	key, err := readInstanceKey(secretPath)
	if err == nil {
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(secretPath), 0o700); err != nil {
		return nil, fmt.Errorf("创建实例密钥目录: %w", err)
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成实例密钥: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(secretPath), ".pinnode-secret-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("创建实例密钥临时文件: %w", err)
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	encoded := base64.RawStdEncoding.EncodeToString(key) + "\n"
	if _, err := io.WriteString(file, encoded); err != nil {
		file.Close()
		return nil, fmt.Errorf("写入实例密钥: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return nil, fmt.Errorf("同步实例密钥: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("关闭实例密钥: %w", err)
	}
	// Hard-link publication is atomic and never replaces a secret created by a
	// concurrent server process. The temporary file is in the same directory.
	if err := os.Link(temporaryPath, secretPath); os.IsExist(err) {
		return readInstanceKey(secretPath)
	} else if err != nil {
		return nil, fmt.Errorf("发布实例密钥文件: %w", err)
	}
	return key, nil
}

func readInstanceKey(secretPath string) ([]byte, error) {
	info, err := os.Stat(secretPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, fmt.Errorf("实例密钥文件无效: %s", secretPath)
	}
	encoded, err := os.ReadFile(secretPath)
	if err != nil {
		return nil, fmt.Errorf("读取实例密钥: %w", err)
	}
	key, err := decode32ByteKey(strings.TrimSpace(string(encoded)), "实例密钥文件")
	if err != nil {
		return nil, fmt.Errorf("解析实例密钥: %w", err)
	}
	return key, nil
}

func deriveInstanceKey(rootKey []byte, purpose string) ([]byte, error) {
	reader := hkdf.New(sha256.New, rootKey, nil, []byte(purpose))
	derived := make([]byte, 32)
	if _, err := io.ReadFull(reader, derived); err != nil {
		return nil, fmt.Errorf("派生实例密钥: %w", err)
	}
	return derived, nil
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
