package main

import (
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
		hub.publishState("advance")
	}
	_, _, cancelOld, missedOld := hub.subscribe("log", first.Sequence-1)
	cancelOld()
	if !missedOld {
		t.Fatal("超出事件环窗口后没有报告需要重置")
	}
}
