package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestLoadConfigCreatesStableInstanceSecret(t *testing.T) {
	clearSecretEnvironment(t)
	directory := t.TempDir()
	secretPath := filepath.Join(directory, "pinnode.secret")
	t.Setenv("PINNODE_DATABASE_PATH", filepath.Join(directory, "pinnode.db"))
	t.Setenv("PINNODE_SECRET_PATH", secretPath)

	first, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.CredentialKey, second.CredentialKey) || first.CodePepper != second.CodePepper {
		t.Fatal("重启后实例派生密钥发生变化")
	}
	if len(first.CredentialKey) != 32 || len(first.CodePepper) != 32 ||
		bytes.Equal(first.CredentialKey, []byte(first.CodePepper)) {
		t.Fatal("实例根密钥没有派生为相互独立的 32 字节子密钥")
	}
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("实例密钥权限过宽: %o", info.Mode().Perm())
	}
}

func TestLoadConfigUsesInstanceKeyWithoutCreatingFile(t *testing.T) {
	clearSecretEnvironment(t)
	directory := t.TempDir()
	secretPath := filepath.Join(directory, "must-not-exist.secret")
	root := bytes.Repeat([]byte{0x42}, 32)
	t.Setenv("PINNODE_INSTANCE_KEY", base64.RawStdEncoding.EncodeToString(root))
	t.Setenv("PINNODE_SECRET_PATH", secretPath)

	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	wantCredential, _ := deriveInstanceKey(root, "pinnode/credential-encryption/v1")
	wantPepper, _ := deriveInstanceKey(root, "pinnode/pairing-code-hmac/v1")
	if !bytes.Equal(config.CredentialKey, wantCredential) || config.CodePepper != string(wantPepper) {
		t.Fatal("PINNODE_INSTANCE_KEY 没有正确派生应用子密钥")
	}
	if _, err := os.Stat(secretPath); !os.IsNotExist(err) {
		t.Fatalf("环境根密钥模式不应创建密钥文件: %v", err)
	}
}

func TestConcurrentInstanceSecretCreationPublishesOneCompleteKey(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "pinnode.secret")
	var wait sync.WaitGroup
	results := make(chan []byte, 16)
	errors := make(chan error, 16)
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			key, err := loadOrCreateInstanceKey(secretPath)
			if err != nil {
				errors <- err
				return
			}
			results <- key
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	var first []byte
	for key := range results {
		if first == nil {
			first = key
		}
		if len(key) != 32 || !bytes.Equal(first, key) {
			t.Fatal("并发首次启动没有得到同一个完整实例密钥")
		}
	}
}

func TestLoadConfigPreservesLegacySecretOverrides(t *testing.T) {
	clearSecretEnvironment(t)
	directory := t.TempDir()
	secretPath := filepath.Join(directory, "must-not-exist.secret")
	credentialKey := bytes.Repeat([]byte{0x23}, 32)
	pepper := "legacy-pepper-with-at-least-32-characters"
	t.Setenv("PINNODE_CREDENTIAL_KEY", base64.RawStdEncoding.EncodeToString(credentialKey))
	t.Setenv("PINNODE_CODE_PEPPER", pepper)
	t.Setenv("PINNODE_SECRET_PATH", secretPath)

	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(config.CredentialKey, credentialKey) || config.CodePepper != pepper {
		t.Fatal("旧环境密钥覆盖没有保持原值")
	}
	if _, err := os.Stat(secretPath); !os.IsNotExist(err) {
		t.Fatalf("完整旧配置不应额外创建实例密钥: %v", err)
	}
}

func clearSecretEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"PINNODE_INSTANCE_KEY", "PINNODE_SECRET_PATH", "PINNODE_CREDENTIAL_KEY", "PINNODE_CODE_PEPPER",
	} {
		t.Setenv(name, "")
	}
}
