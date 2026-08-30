package main

import (
	"io"
	"log"
	"strings"
	"testing"
)

func TestRedactLogMessageRemovesSensitiveFields(t *testing.T) {
	message := `authorization: Bearer bearer-secret sessionToken="session-secret" password=pass-secret`
	redacted := redactLogMessage(message)
	for _, secret := range []string{"bearer-secret", "session-secret", "pass-secret"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("日志仍包含敏感值 %q: %s", secret, redacted)
		}
	}
	if strings.Count(redacted, "[REDACTED]") != 3 {
		t.Fatalf("敏感字段替换数量错误: %s", redacted)
	}
}

func TestRedactLogMessageCoversCredentialAndPINForms(t *testing.T) {
	message := `accessToken=access-secret oauthSecret="oauth-secret" apiKey=api-secret pairingCode=123456 cookie=session-cookie`
	redacted := redactLogMessage(message)
	for _, secret := range []string{"access-secret", "oauth-secret", "api-secret", "123456", "session-cookie"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("日志仍包含敏感值 %q: %s", secret, redacted)
		}
	}
}

func TestStructuredLoggerSupportsExplicitComponentAndLevel(t *testing.T) {
	hub := newAdminEventHub()
	logger := &structuredLogger{base: log.New(io.Discard, "", 0), events: hub}
	logger.Errorf("tailscale", "控制面访问失败")

	logs, _, _ := hub.recentLogs(1)
	if len(logs) != 1 || logs[0].Component != "tailscale" || logs[0].Level != "ERROR" {
		t.Fatalf("结构化日志 component/level 错误: %+v", logs)
	}
}

func TestAdminEventHubReplaysAndReportsMissedWindow(t *testing.T) {
	hub := newAdminEventHub()
	first := hub.publishLog(LogEvent{Message: "first"})
	replay, events, cancel, missed := hub.subscribe("log", first.Sequence-1)
	defer cancel()
	if missed || len(replay) != 1 || replay[0].Sequence != first.Sequence {
		t.Fatalf("日志事件重放错误: missed=%v replay=%+v", missed, replay)
	}
	second := hub.publishLog(LogEvent{Message: "second"})
	select {
	case event := <-events:
		if event.Sequence != second.Sequence || event.Kind != "log" {
			t.Fatalf("实时日志事件错误: %+v", event)
		}
	default:
		t.Fatal("没有收到实时日志事件")
	}

	for index := 0; index < adminEventBufferSize+1; index++ {
		hub.publishLog(LogEvent{Message: "advance"})
	}
	_, _, cancelOld, missedOld := hub.subscribe("log", first.Sequence-1)
	cancelOld()
	if !missedOld {
		t.Fatal("超出事件环窗口后没有报告需要重置")
	}
}

func TestAdminEventHubKeepsStateAndLogWindowsIndependent(t *testing.T) {
	hub := newAdminEventHub()
	firstLog := hub.publishLog(LogEvent{Message: "first"})
	for index := 0; index < adminEventBufferSize+1; index++ {
		hub.publishState("advance")
	}

	replay, _, cancel, missed := hub.subscribe("log", firstLog.Sequence)
	cancel()
	if missed || len(replay) != 0 {
		t.Fatalf("状态事件不应挤出日志恢复窗口: missed=%v replay=%+v", missed, replay)
	}

	stateReplay, _, cancelState, stateMissed := hub.subscribe("state", firstLog.Sequence)
	cancelState()
	if !stateMissed || len(stateReplay) == 0 {
		t.Fatalf("状态事件窗口没有独立淘汰: missed=%v replay=%d", stateMissed, len(stateReplay))
	}
}

func TestAdminEventHubDoesNotDeliverOtherKindsToSubscriber(t *testing.T) {
	hub := newAdminEventHub()
	_, events, cancel, _ := hub.subscribe("log", 0)
	defer cancel()
	hub.publishState("not-a-log")
	select {
	case event := <-events:
		t.Fatalf("日志订阅收到状态事件: %+v", event)
	default:
	}

	want := hub.publishLog(LogEvent{Message: "log"})
	select {
	case event := <-events:
		if event.Sequence != want.Sequence || event.Kind != "log" {
			t.Fatalf("日志订阅收到错误事件: %+v", event)
		}
	default:
		t.Fatal("日志订阅没有收到日志事件")
	}
}
