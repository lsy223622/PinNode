package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdminPasswordRecoveryInvalidatesSessionsAndPreservesCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinnode.db")
	t.Setenv("PINNODE_DATABASE_PATH", path)
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(testServiceConfig(), store, &fakeTailscale{}, nil)
	_, credentialID := installTestAdminAndCredential(t, service, store)
	before, _, _ := store.GetTailscaleCredential(credentialID)
	for range 5 {
		if _, err := store.RecordAdminLoginFailure(1, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := runMaintenance([]string{"admin-reset-password", "--username", "admin", "--password-stdin"}, strings.NewReader("a replacement administrator password\r\n"), &output); err != nil {
		t.Fatal(err)
	}
	admin, ok, err := store.GetAdminByUsername("admin")
	if err != nil || !ok || !verifyPassword(admin.PasswordHash, "a replacement administrator password") || verifyPassword(admin.PasswordHash, "unit test administrator password") {
		t.Fatal("password was not replaced")
	}
	if admin.FailedAttempts != 0 || !admin.LockedUntil.IsZero() {
		t.Fatal("login lockout was not reset")
	}
	var sessions int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM admin_sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("old administrator sessions remain: %d, %v", sessions, err)
	}
	after, _, err := store.GetTailscaleCredential(credentialID)
	if err != nil || !bytes.Equal(before.Ciphertext, after.Ciphertext) {
		t.Fatal("recovery modified Tailscale credentials")
	}
	if strings.Contains(output.String(), "replacement") {
		t.Fatal("password leaked to output")
	}
}

func TestAdminPasswordRecoveryRejectsInvalidInputWithoutChangingAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinnode.db")
	t.Setenv("PINNODE_DATABASE_PATH", path)
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	installTestAdminAndCredential(t, NewService(testServiceConfig(), store, &fakeTailscale{}, nil), store)
	for _, tc := range []struct {
		name, username, password string
		stdin                    bool
	}{
		{"short", "admin", "short\n", true},
		{"oversize", "admin", strings.Repeat("x", 2048), true},
		{"missing account", "other", "a replacement administrator password", true},
		{"missing stdin flag", "admin", "a replacement administrator password", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"admin-reset-password", "--username", tc.username}
			if tc.stdin {
				args = append(args, "--password-stdin")
			}
			if err := runMaintenance(args, strings.NewReader(tc.password), &bytes.Buffer{}); err == nil {
				t.Fatal("invalid recovery accepted")
			}
			admin, _, _ := store.GetAdminByUsername("admin")
			if !verifyPassword(admin.PasswordHash, "unit test administrator password") {
				t.Fatal("failed recovery changed password")
			}
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM admin_sessions`).Scan(&count); err != nil || count != 1 {
				t.Fatal("failed recovery revoked sessions")
			}
		})
	}
}

func TestMaintenanceDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	t.Setenv("PINNODE_DATABASE_PATH", path)
	if err := runMaintenance([]string{"backup", "--output", filepath.Join(t.TempDir(), "backup.db")}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("maintenance created a database")
	}
}

func TestBackupDoesNotMigrateSourceDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	t.Setenv("PINNODE_DATABASE_PATH", path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE legacy_data (value TEXT); INSERT INTO legacy_data VALUES ('preserved')`); err != nil {
		t.Fatal(err)
	}
	if err := runMaintenance([]string{"backup", "--output", filepath.Join(t.TempDir(), "backup.db")}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("backup migrated source schema: tables=%d, err=%v", tables, err)
	}
}

func TestBackupCapturesWALAndRestoresEncryptedSessionState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinnode.db")
	t.Setenv("PINNODE_DATABASE_PATH", path)
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	config := testServiceConfig()
	service := NewService(config, store, &fakeTailscale{}, nil)
	admin, credentialID := installTestAdminAndCredential(t, service, store)
	code := createTestPairingCode(t, service, admin, credentialID)
	started := startAdminTestSession(t, service, code, "192.168.1.1/32")
	outputPath := filepath.Join(t.TempDir(), "backup ' snapshot.db")
	if err := runMaintenance([]string{"backup", "--output", outputPath}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenStore(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var integrity string
	if err := restored.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("invalid snapshot: %s, %v", integrity, err)
	}
	session, ok, err := restored.GetSession(started.ID)
	if err != nil || !ok || session.Status != SessionProvisioning || session.CredentialID != credentialID {
		t.Fatal("backup lost live session")
	}
	restoredService := NewService(config, restored, &fakeTailscale{}, nil)
	if err := restoredService.validateStoredCredentials(); err != nil {
		t.Fatal(err)
	}
	if _, err := restoredService.credentialToken(t.Context(), credentialID); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := runMaintenance([]string{"backup", "--output", outputPath}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("existing backup overwritten")
	}
	after, err := os.ReadFile(outputPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("existing backup changed")
	}
}
