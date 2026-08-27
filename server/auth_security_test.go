package main

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordHashUsesArgon2idAndRejectsWrongPassword(t *testing.T) {
	encoded, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$") ||
		!verifyPassword(encoded, "correct horse battery staple") ||
		verifyPassword(encoded, "wrong password") {
		t.Fatalf("Argon2id 密码验证结果错误: %q", encoded)
	}
}

func TestCredentialCipherBindsCiphertextToCredentialID(t *testing.T) {
	testToken := fakeTailscaleKey("api", "secret")
	cipher, err := NewCredentialCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := cipher.Seal("credential-a", testToken)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext, err := cipher.Open("credential-a", encoded); err != nil || plaintext != testToken {
		t.Fatalf("合法密文无法解密: plaintext=%q err=%v", plaintext, err)
	}
	if _, err := cipher.Open("credential-b", encoded); err == nil {
		t.Fatal("凭据密文可以在其他 ID 下解密")
	}
	encoded[len(encoded)-1] ^= 1
	if _, err := cipher.Open("credential-a", encoded); err == nil {
		t.Fatal("篡改后的凭据密文仍能解密")
	}
}

func TestPoWChallengeIsSourceBoundAndSingleUse(t *testing.T) {
	manager := NewPoWManager()
	now := time.Now()
	challenge, err := manager.Issue("127.0.0.1", 16, now)
	if err != nil {
		t.Fatal(err)
	}
	nonce := solveTestPoW(challenge)
	if manager.Verify(challenge.ID, "198.51.100.1", nonce, now) {
		t.Fatal("PoW challenge 可从其他来源使用")
	}
	if manager.Verify(challenge.ID, "127.0.0.1", nonce, now) {
		t.Fatal("失败验证后 PoW challenge 被错误复用")
	}
	challenge, err = manager.Issue("127.0.0.1", 16, now)
	if err != nil {
		t.Fatal(err)
	}
	nonce = solveTestPoW(challenge)
	if !manager.Verify(challenge.ID, "127.0.0.1", nonce, now) {
		t.Fatal("合法 PoW proof 被拒绝")
	}
	if manager.Verify(challenge.ID, "127.0.0.1", nonce, now) {
		t.Fatal("PoW challenge 可以重放")
	}
}
