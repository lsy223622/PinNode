package main

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

func TestLegacyHeartbeatLeaseMigratesToSyncDeadline(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "legacy-sessions.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		token_hash TEXT NOT NULL,
		auth_key_id TEXT NOT NULL,
		provisioning_name TEXT NOT NULL UNIQUE,
		route TEXT NOT NULL,
		routes_json TEXT NOT NULL,
		wifi_routes_json TEXT NOT NULL,
		config_json TEXT NOT NULL,
		device_id TEXT UNIQUE,
		created_at INTEGER NOT NULL,
		provisioning_deadline INTEGER,
		expires_at INTEGER,
		last_seen_at INTEGER,
		heartbeat_deadline INTEGER,
		status TEXT NOT NULL,
		cleanup_error TEXT NOT NULL DEFAULT '',
		cleanup_after INTEGER,
		stopped_at INTEGER,
		stop_reason TEXT NOT NULL DEFAULT '',
		updated_at INTEGER NOT NULL
	)`)
	if err == nil {
		_, err = db.Exec(`CREATE INDEX sessions_reap_idx
			ON sessions(status, provisioning_deadline, expires_at, heartbeat_deadline, cleanup_after)`)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	deadline := now.Add(-time.Second)
	if err == nil {
		_, err = db.Exec(`INSERT INTO sessions(
			id, token_hash, auth_key_id, provisioning_name, route, routes_json,
			wifi_routes_json, config_json, created_at, last_seen_at, heartbeat_deadline,
			status, updated_at
		) VALUES
			('leased', 'hash-1', 'key-1', 'pinnode-leased', '', '[]', '[]',
			 '{"advertiseRoutes":[],"exitPolicy":{"onAppClose":true}}', ?, ?, ?, 'active', ?),
			('persistent', 'hash-2', 'key-2', 'pinnode-persistent', '', '[]', '[]',
			 '{"advertiseRoutes":[],"exitPolicy":{"onAppClose":false}}', ?, ?, ?, 'active', ?)`,
			toMillis(now.Add(-time.Hour)), toMillis(now), toMillis(deadline), toMillis(now),
			toMillis(now.Add(-time.Hour)), toMillis(now), toMillis(deadline), toMillis(now),
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
	leased, ok, err := store.GetSession("leased")
	if err != nil || !ok || !leased.SyncDeadline.Equal(deadline) || leased.ConfigRevision != 1 {
		t.Fatalf("旧租约迁移错误: session=%+v ok=%v err=%v", leased, ok, err)
	}
	persistent, ok, err := store.GetSession("persistent")
	if err != nil || !ok || !persistent.SyncDeadline.IsZero() {
		t.Fatalf("长期会话错误保留旧租约: session=%+v ok=%v err=%v", persistent, ok, err)
	}
	reapable, err := store.ReapableSessions(now)
	if err != nil || len(reapable) != 1 || reapable[0].ID != "leased" {
		t.Fatalf("迁移后的回收集合错误: sessions=%+v err=%v", reapable, err)
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

func TestListActiveSessionsIsNotLimitedByHistoryWindow(t *testing.T) {
	store := NewStore()
	defer store.Close()
	now := time.Now()
	config := DefaultSessionConfig()

	for index := 0; index < 1001; index++ {
		status := SessionStopped
		if index == 0 {
			status = SessionActive
		}
		if err := store.PutSession(Session{
			ID:               "history-session-" + formatTestIndex(index),
			TokenHash:        "history-token-" + formatTestIndex(index),
			AuthKeyID:        "history-key-" + formatTestIndex(index),
			ProvisioningName: "history-pinnode-" + formatTestIndex(index),
			Config:           config,
			CreatedAt:        now.Add(time.Duration(index) * time.Millisecond),
			UpdatedAt:        now.Add(time.Duration(index) * time.Millisecond),
			Status:           status,
		}); err != nil {
			t.Fatal(err)
		}
	}

	sessions, err := store.ListActiveSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "history-session-"+formatTestIndex(0) {
		t.Fatalf("活动会话查询被历史窗口截断: len=%d sessions=%+v", len(sessions), sessions)
	}
}

func formatTestIndex(index int) string {
	return fmt.Sprintf("%04d", index)
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

func TestSessionConfigKeepsEmptyRoutesAsArray(t *testing.T) {
	store := NewStore()
	now := time.Now()
	if err := store.PutCodeWithConfig("empty-routes", now.Add(time.Minute), SessionConfig{}); err != nil {
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

func TestSyncDeadlineMakesSessionReapable(t *testing.T) {
	store := NewStore()
	now := time.Now()
	if err := store.PutSession(Session{
		ID:               "sync-expired",
		ProvisioningName: "pinnode-sync",
		Config:           SessionConfig{ExitPolicy: ExitPolicy{OnAppClose: true}},
		CreatedAt:        now.Add(-time.Hour),
		SyncDeadline:     now.Add(-time.Second),
		Status:           SessionActive,
		UpdatedAt:        now.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	sessions, err := store.ReapableSessions(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "sync-expired" {
		t.Fatalf("同步超时会话未进入清理队列: %+v", sessions)
	}
}

func TestSyncDeadlineIgnoredWithoutAppClose(t *testing.T) {
	store := NewStore()
	now := time.Now()
	if err := store.PutSession(Session{
		ID:               "persistent-session",
		ProvisioningName: "pinnode-persistent",
		CreatedAt:        now.Add(-time.Hour),
		SyncDeadline:     now.Add(-time.Second),
		Status:           SessionActive,
		UpdatedAt:        now.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	sessions, err := store.ReapableSessions(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("长期会话不应因同步超时进入清理队列: %+v", sessions)
	}
}

func TestCleanupClaimLeaseAndGenerationFence(t *testing.T) {
	store := NewStore()
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.PutSession(Session{
		ID: "claim-session", Status: SessionActive, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		ProvisioningName: "pinnode-claim-session", AuthKeyID: "key-claim",
	}); err != nil {
		t.Fatal(err)
	}

	first, claimed, err := store.BeginCleanupWithLease("claim-session", now, true, "client_stop", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("首次领取清理失败: claimed=%v err=%v", claimed, err)
	}
	if first.Status != SessionCleaning || first.CleanupGeneration != 1 ||
		!first.CleanupLeaseUntil.After(now) {
		t.Fatalf("首次 claim 状态错误: %+v", first)
	}
	second, claimed, err := store.BeginCleanupWithLease(
		"claim-session", now.Add(30*time.Second), true, "client_stop", time.Minute,
	)
	if err != nil || claimed || second.CleanupGeneration != first.CleanupGeneration {
		t.Fatalf("有效租约被重复领取: session=%+v claimed=%v err=%v", second, claimed, err)
	}

	recovered, claimed, err := store.BeginCleanupWithLease(
		"claim-session", now.Add(2*time.Minute), false, "", time.Minute,
	)
	if err != nil || !claimed || recovered.CleanupGeneration != 2 {
		t.Fatalf("过期租约未生成新 claim: session=%+v claimed=%v err=%v", recovered, claimed, err)
	}
	if _, err := store.FinishCleanupClaim("claim-session", first.CleanupGeneration, now.Add(2*time.Minute), nil); !errors.Is(err, ErrStaleCleanupClaim) {
		t.Fatalf("旧 generation 错误覆盖新 claim: err=%v", err)
	}
	if applied, err := store.FinishCleanupClaim("claim-session", recovered.CleanupGeneration, now.Add(2*time.Minute), nil); err != nil || !applied {
		t.Fatalf("当前 claim 完成失败: applied=%v err=%v", applied, err)
	}
	final, ok, err := store.GetSession("claim-session")
	if err != nil || !ok || final.Status != SessionStopped || !final.CleanupLeaseUntil.IsZero() {
		t.Fatalf("清理完成状态错误: session=%+v ok=%v err=%v", final, ok, err)
	}
}

func TestExpiredCleanupClaimRecoversAfterStoreReopen(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "cleanup-reopen.db")
	now := time.Now().UTC().Truncate(time.Millisecond)
	store, err := OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutSession(Session{
		ID: "reopen-cleaning", Status: SessionActive, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		ProvisioningName: "pinnode-reopen-cleaning", AuthKeyID: "key-reopen",
	}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	claimed, ok, err := store.BeginCleanupWithLease("reopen-cleaning", now, true, "client_stop", time.Minute)
	if err != nil || !ok || claimed.CleanupGeneration != 1 {
		store.Close()
		t.Fatalf("重开前领取清理失败: session=%+v ok=%v err=%v", claimed, ok, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recoveryTime := now.Add(2 * time.Minute)
	reapable, err := reopened.ReapableSessions(recoveryTime)
	if err != nil || len(reapable) != 1 || reapable[0].ID != "reopen-cleaning" {
		t.Fatalf("重开后过期 cleaning 未进入回收队列: sessions=%+v err=%v", reapable, err)
	}
	recovered, ok, err := reopened.BeginCleanupWithLease(
		"reopen-cleaning", recoveryTime, false, "", time.Minute,
	)
	if err != nil || !ok || recovered.CleanupGeneration != 2 {
		t.Fatalf("重开后未生成新的 cleanup claim: session=%+v ok=%v err=%v", recovered, ok, err)
	}
	if applied, err := reopened.FinishCleanupClaim(
		"reopen-cleaning", recovered.CleanupGeneration, recoveryTime, nil,
	); err != nil || !applied {
		t.Fatalf("重开后的 cleanup claim 无法完成: applied=%v err=%v", applied, err)
	}
}

func TestCleaningWithoutLeaseIsReapableAndRecoverable(t *testing.T) {
	store := NewStore()
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.PutSession(Session{
		ID: "legacy-cleaning", Status: SessionCleaning, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		ProvisioningName: "pinnode-legacy-cleaning", AuthKeyID: "key-legacy",
	}); err != nil {
		t.Fatal(err)
	}
	reapable, err := store.ReapableSessions(now)
	if err != nil || len(reapable) != 1 || reapable[0].ID != "legacy-cleaning" {
		t.Fatalf("无租约 cleaning 未进入回收队列: sessions=%+v err=%v", reapable, err)
	}
	claimed, ok, err := store.BeginCleanupWithLease("legacy-cleaning", now, false, "", time.Minute)
	if err != nil || !ok || claimed.CleanupGeneration != 1 || claimed.CleanupLeaseUntil.IsZero() {
		t.Fatalf("遗留 cleaning 未能安全重领: session=%+v ok=%v err=%v", claimed, ok, err)
	}
}

func TestAppliedConfigRevisionIsMonotonic(t *testing.T) {
	store := NewStore()
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.PutSession(Session{
		ID: "revision-monotonic", Status: SessionActive, CreatedAt: now, UpdatedAt: now,
		ProvisioningName: "pinnode-revision-monotonic", ConfigRevision: 3, AppliedConfigRevision: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if updated, err := store.SyncSession("revision-monotonic", now.Add(time.Second), time.Time{}, 2); err != nil || !updated {
		t.Fatalf("旧 revision 同步失败: updated=%v err=%v", updated, err)
	}
	session, _, err := store.GetSession("revision-monotonic")
	if err != nil || session.AppliedConfigRevision != 3 {
		t.Fatalf("applied revision 被回退: session=%+v err=%v", session, err)
	}
	if updated, err := store.SyncSession("revision-monotonic", now.Add(2*time.Second), time.Time{}, 3); err != nil || !updated {
		t.Fatalf("重复 revision 同步失败: updated=%v err=%v", updated, err)
	}
	if updated, err := store.SyncSession("revision-monotonic", now.Add(3*time.Second), time.Time{}, 3); err != nil || !updated {
		t.Fatalf("重复 revision ACK 失败: updated=%v err=%v", updated, err)
	}
	if updated, err := store.SyncSession("revision-monotonic", now.Add(4*time.Second), time.Time{}, 2); err != nil || !updated {
		t.Fatalf("配置 revision 未变化时旧 ACK 失败: updated=%v err=%v", updated, err)
	}
	session, _, err = store.GetSession("revision-monotonic")
	if err != nil || session.AppliedConfigRevision != 3 {
		t.Fatalf("多次旧 ACK 后 applied revision 错误: session=%+v err=%v", session, err)
	}
	if updated, err := store.SyncSession("revision-monotonic", now.Add(5*time.Second), time.Time{}, 4); err != nil || updated {
		t.Fatalf("超过当前 config revision 的 ACK 未被拒绝: updated=%v err=%v", updated, err)
	}
}

func TestCleanupErrorDoesNotPersistRawSecret(t *testing.T) {
	store := NewStore()
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.PutSession(Session{
		ID: "safe-cleanup-error", Status: SessionActive, CreatedAt: now, UpdatedAt: now,
		ProvisioningName: "pinnode-safe-cleanup-error", AuthKeyID: "key-safe",
	}); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.BeginCleanupWithLease("safe-cleanup-error", now, true, "client_stop", time.Minute)
	if err != nil || !ok {
		t.Fatalf("领取清理失败: claimed=%v err=%v", ok, err)
	}
	secret := "session-token=secret auth-key=also-secret"
	if _, err := store.FinishCleanupClaim("safe-cleanup-error", claimed.CleanupGeneration, now, errors.New(secret)); err != nil {
		t.Fatal(err)
	}
	failed, _, err := store.GetSession("safe-cleanup-error")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(failed.CleanupErr, secret) || failed.CleanupErr == "" {
		t.Fatalf("清理错误摘要泄露或为空: %q", failed.CleanupErr)
	}
}
