package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	passwordMemory      = 19 * 1024
	passwordIterations  = 2
	passwordParallelism = 1
	passwordSaltLength  = 16
	passwordKeyLength   = 32
)

func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成密码盐: %w", err)
	}
	hash := argon2.IDKey(
		[]byte(password), salt, passwordIterations, passwordMemory,
		passwordParallelism, passwordKeyLength,
	)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, passwordMemory, passwordIterations, passwordParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(
		parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism,
	); err != nil || memory < 8*1024 || memory > 256*1024 || iterations < 1 || iterations > 10 || parallelism < 1 || parallelism > 8 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) < 16 || len(expected) > 64 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(expected, actual) == 1
}

func validateAdminPassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < 15 || length > 128 || len(password) > 1024 {
		return errors.New("密码长度必须在 15 到 128 个字符之间")
	}
	return nil
}

func normalizeAdminUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	length := utf8.RuneCountInString(username)
	if length < 1 || length > 64 || len(username) > 256 {
		return "", errors.New("用户名长度必须在 1 到 64 个字符之间")
	}
	for _, character := range username {
		if unicode.IsControl(character) {
			return "", errors.New("用户名不能包含控制字符")
		}
	}
	return username, nil
}

type CredentialCipher struct {
	aead cipher.AEAD
}

func NewCredentialCipher(key []byte) (*CredentialCipher, error) {
	if len(key) != 32 {
		return nil, errors.New("凭据加密密钥必须是 32 字节")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &CredentialCipher{aead: aead}, nil
}

func (c *CredentialCipher) Seal(credentialID, plaintext string) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, errors.New("凭据加密不可用")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := c.aead.Seal(nil, nonce, []byte(plaintext), []byte(credentialID))
	result := make([]byte, 1, 1+len(nonce)+len(sealed))
	result[0] = 1
	result = append(result, nonce...)
	result = append(result, sealed...)
	return result, nil
}

func (c *CredentialCipher) Open(credentialID string, encoded []byte) (string, error) {
	if c == nil || c.aead == nil {
		return "", errors.New("凭据加密不可用")
	}
	nonceSize := c.aead.NonceSize()
	if len(encoded) <= 1+nonceSize || encoded[0] != 1 {
		return "", errors.New("凭据密文格式无效")
	}
	plaintext, err := c.aead.Open(
		nil, encoded[1:1+nonceSize], encoded[1+nonceSize:], []byte(credentialID),
	)
	if err != nil {
		return "", errors.New("凭据无法解密")
	}
	return string(plaintext), nil
}

func newURLToken(byteLength int) (string, error) {
	value := make([]byte, byteLength)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

type PoWChallenge struct {
	ID         string    `json:"id"`
	Value      string    `json:"challenge"`
	Difficulty int       `json:"difficulty"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type storedPoWChallenge struct {
	PoWChallenge
	client string
}

type PoWManager struct {
	mu         sync.Mutex
	challenges map[string]storedPoWChallenge
}

func NewPoWManager() *PoWManager {
	return &PoWManager{challenges: make(map[string]storedPoWChallenge)}
}

func (m *PoWManager) Issue(client string, difficulty int, now time.Time) (PoWChallenge, error) {
	id, err := newURLToken(16)
	if err != nil {
		return PoWChallenge{}, err
	}
	value, err := newURLToken(32)
	if err != nil {
		return PoWChallenge{}, err
	}
	challenge := PoWChallenge{ID: id, Value: value, Difficulty: difficulty, ExpiresAt: now.Add(2 * time.Minute)}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, existing := range m.challenges {
		if !existing.ExpiresAt.After(now) {
			delete(m.challenges, key)
		}
	}
	if len(m.challenges) >= 4096 {
		return PoWChallenge{}, errors.New("待处理的安全验证过多")
	}
	m.challenges[id] = storedPoWChallenge{PoWChallenge: challenge, client: client}
	return challenge, nil
}

func (m *PoWManager) Verify(id, client, nonce string, now time.Time) bool {
	m.mu.Lock()
	challenge, ok := m.challenges[id]
	if ok {
		delete(m.challenges, id)
	}
	m.mu.Unlock()
	if !ok || !challenge.ExpiresAt.After(now) || !hmac.Equal([]byte(challenge.client), []byte(client)) {
		return false
	}
	if len(nonce) == 0 || len(nonce) > 20 {
		return false
	}
	if _, err := strconv.ParseUint(nonce, 10, 64); err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(challenge.Value + ":" + nonce))
	return hasLeadingZeroBits(digest[:], challenge.Difficulty)
}

func hasLeadingZeroBits(value []byte, count int) bool {
	for _, current := range value {
		if count <= 0 {
			return true
		}
		if count >= 8 {
			if current != 0 {
				return false
			}
			count -= 8
			continue
		}
		return current>>(8-count) == 0
	}
	return count <= 0
}
