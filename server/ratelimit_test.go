package main

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestClientAddressOnlyTrustsConfiguredProxy(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "10.0.0.5:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.4")
	if got := clientAddress(request, trusted); got != "198.51.100.9" {
		t.Fatalf("可信代理来源地址=%q", got)
	}

	request.RemoteAddr = "203.0.113.7:4321"
	if got := clientAddress(request, trusted); got != "203.0.113.7" {
		t.Fatalf("不可信来源伪造了代理头: %q", got)
	}
}

func TestRateLimiterReturnsRetryAfterAndRecovers(t *testing.T) {
	limiter := NewRateLimiter()
	now := time.Now()
	if allowed, _ := limiter.Allow("key", 1, time.Minute, now); !allowed {
		t.Fatal("首次请求被拒绝")
	}
	if allowed, retryAfter := limiter.Allow("key", 1, time.Minute, now); allowed || retryAfter <= 0 {
		t.Fatalf("重复请求未被正确限制: allowed=%v retryAfter=%v", allowed, retryAfter)
	}
	if allowed, _ := limiter.Allow("key", 1, time.Minute, now.Add(time.Minute+time.Second)); !allowed {
		t.Fatal("窗口结束后仍被限制")
	}
}
