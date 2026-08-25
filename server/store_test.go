package main

import (
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLegacyCredentialMigrationDefaultsToAPIToken(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE tailscale_credentials (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL COLLATE NOCASE UNIQUE,
		token_cipher BLOB NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		last_used_at INTEGER
	)`)
	if err == nil {
		_, err = db.Exec(
			`INSERT INTO tailscale_credentials(id, name, token_cipher, created_at, updated_at)
			 VALUES ('legacy', '旧 API token', X'0102', 1, 1)`,
		)
	}
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	credential, ok, err := store.GetTailscaleCredential("legacy")
	if err != nil || !ok || credential.Kind != TailscaleCredentialAPIToken {
		t.Fatalf("旧凭据迁移错误: kind=%q ok=%v err=%v", credential.Kind, ok, err)
	}
}

func TestConsumeCodeIsAtomicAndSingleUse(t *testing.T) {
	store := NewStore()
	now := time.Now()
	if err := store.PutCode("code-hash", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if ok, err := store.ConsumeCode("code-hash", now); err != nil {
				t.Errorf("消费配对代码: %v", err)
			} else if ok {
				successes.Add(1)
			}
		}()
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("并发消费成功次数=%d，期望 1", successes.Load())
	}
	if ok, err := store.ConsumeCode("code-hash", now); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("已消费代码再次消费成功")
	}
}

func TestExpiredCodeCannotBeConsumed(t *testing.T) {
	store := NewStore()
	now := time.Now()
	if err := store.PutCode("expired", now); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ConsumeCode("expired", now); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("过期代码消费成功")
	}
}

func TestRescueConfigKeepsEmptyRoutesAsArray(t *testing.T) {
	store := NewStore()
	now := time.Now()
	if err := store.PutCodeWithConfig("empty-routes", now.Add(time.Minute), RescueConfig{}); err != nil {
		t.Fatal(err)
	}
	config, ok, err := store.RedeemCode("empty-routes", now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("配对代码兑换失败")
	}
	if config.AdvertiseRoutes == nil {
		t.Fatal("空的 advertiseRoutes 不应被复制成 nil")
	}
}

func TestCleanupFailedSessionIsRetriedAfterExpiry(t *testing.T) {
	store := NewStore()
	now := time.Now()
	if err := store.PutSession(Session{
		ID:        "retry-me",
		ExpiresAt: now.Add(-time.Minute),
		Status:    SessionCleanupFailed,
	}); err != nil {
		t.Fatal(err)
	}
	expired, err := store.ReapableSessions(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].ID != "retry-me" {
		t.Fatalf("清理失败会话未进入重试队列: %+v", expired)
	}
}

func TestPersistentSessionWithoutExitPolicyIsNotReaped(t *testing.T) {
	store := NewStore()
	if err := store.PutSession(Session{ID: "persistent", Status: SessionActive}); err != nil {
		t.Fatal(err)
	}
	if expired, err := store.ReapableSessions(time.Now().Add(10 * 365 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	} else if len(expired) != 0 {
		t.Fatalf("无退出策略的持久会话进入了回收队列: %+v", expired)
	}
}

func TestSessionHistorySurvivesDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinnode.db")
	now := time.Now().UTC().Truncate(time.Millisecond)
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutSession(Session{
		ID:               "persisted",
		TokenHash:        "token-hash",
		AuthKeyID:        "k-test",
		ProvisioningName: "pinnode-test",
		Routes:           []string{"192.0.2.0/24"},
		WiFiRoutes:       []string{},
		CreatedAt:        now,
		Status:           SessionProvisioning,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	session, ok, err := reopened.GetSession("persisted")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || session.AuthKeyID != "k-test" || len(session.Routes) != 1 || !session.CreatedAt.Equal(now) {
		t.Fatalf("重启后会话历史不完整: %+v", session)
	}
}

func TestFirstStoredCredentialBackfillsExistingProvisioningState(t *testing.T) {
	store := NewStore()
	defer store.Close()
	now := time.Now()
	if err := store.PutCode("legacy-code", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSession(Session{
		ID: "legacy-session", CreatedAt: now, Status: SessionProvisioning, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTailscaleCredential(TailscaleCredential{
		ID: "credential", Name: "default", Ciphertext: []byte("ciphertext"),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	_, _, credentialID, ok, err := store.RedeemCodeWithCredential("legacy-code", now)
	if err != nil || !ok || credentialID != "credential" {
		t.Fatalf("既有配对代码未绑定首个凭据: id=%q ok=%v err=%v", credentialID, ok, err)
	}
	session, ok, err := store.GetSession("legacy-session")
	if err != nil || !ok || session.CredentialID != "credential" {
		t.Fatalf("既有会话未绑定首个凭据: %+v ok=%v err=%v", session, ok, err)
	}
}

func TestDeviceCanOnlyBelongToOneSession(t *testing.T) {
	store := NewStore()
	now := time.Now()
	for _, id := range []string{"first", "second"} {
		if err := store.PutSession(Session{
			ID:               id,
			ProvisioningName: "pinnode-" + id,
			CreatedAt:        now,
			Status:           SessionProvisioning,
			UpdatedAt:        now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := store.AttachDevice("first", "n-device", now, now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("首次绑定失败: ok=%v err=%v", ok, err)
	}
	if ok, err := store.AttachDevice("second", "n-device", now, now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("同一设备被重复绑定: ok=%v err=%v", ok, err)
	}
}

func TestHeartbeatDeadlineMakesSessionReapable(t *testing.T) {
	store := NewStore()
	now := time.Now()
	if err := store.PutSession(Session{
		ID:                "heartbeat-expired",
		ProvisioningName:  "pinnode-heartbeat",
		Config:            RescueConfig{ExitPolicy: ExitPolicy{OnAppClose: true}},
		CreatedAt:         now.Add(-time.Hour),
		HeartbeatDeadline: now.Add(-time.Second),
		Status:            SessionActive,
		UpdatedAt:         now.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	sessions, err := store.ReapableSessions(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "heartbeat-expired" {
		t.Fatalf("心跳超时会话未进入清理队列: %+v", sessions)
	}
}

func TestHeartbeatDeadlineIgnoredWithoutAppClose(t *testing.T) {
	store := NewStore()
	now := time.Now()
	if err := store.PutSession(Session{
		ID:                "persistent-session",
		ProvisioningName:  "pinnode-persistent",
		CreatedAt:         now.Add(-time.Hour),
		HeartbeatDeadline: now.Add(-time.Second),
		Status:            SessionActive,
		UpdatedAt:         now.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	sessions, err := store.ReapableSessions(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("长期会话不应因心跳超时进入清理队列: %+v", sessions)
	}
}
