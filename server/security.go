package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/netip"
	"regexp"
)

var sixDigitCode = regexp.MustCompile(`^[0-9]{6}$`)

func newPairingCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("生成配对代码失败: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func newSecretToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("生成会话令牌失败: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256(raw)
	return token, hex.EncodeToString(hash[:]), nil
}

func hashPairingCode(pepper, code string) string {
	h := hmac.New(sha256.New, []byte(pepper))
	_, _ = h.Write([]byte(code))
	return hex.EncodeToString(h.Sum(nil))
}

func equalSecretHash(expected, actual string) bool {
	return hmac.Equal([]byte(expected), []byte(actual))
}

func validPairingCode(code string) bool {
	return sixDigitCode.MatchString(code)
}

func validateGatewayRoute(route string) error {
	prefix, err := netip.ParsePrefix(route)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 32 {
		return fmt.Errorf("gateway route 必须是 IPv4 /32: %q", route)
	}
	return nil
}

func validateWiFiSubnetRoute(route, gatewayRoute string) error {
	prefix, err := netip.ParsePrefix(route)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() <= 0 || prefix.Bits() >= 32 || prefix != prefix.Masked() {
		return fmt.Errorf("wifi subnet route 必须是规范的 IPv4 子网: %q", route)
	}
	if gatewayRoute != "" {
		gateway, gatewayErr := netip.ParsePrefix(gatewayRoute)
		if gatewayErr != nil || !prefix.Contains(gateway.Addr()) {
			return fmt.Errorf("wifi subnet route 不包含当前 Wi-Fi 网关")
		}
	}
	return nil
}

func validNodeID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if r <= 0x20 || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}
