package main

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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
