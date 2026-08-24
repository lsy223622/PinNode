package main

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type rateEntry struct {
	timestamps []time.Time
	interval   time.Duration
}

type RateLimiter struct {
	mu        sync.Mutex
	entries   map[string]rateEntry
	lastSweep time.Time
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{entries: make(map[string]rateEntry)}
}

func (r *RateLimiter) Allow(key string, max int, interval time.Duration, now time.Time) (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastSweep.IsZero() || now.Sub(r.lastSweep) >= time.Minute {
		for key, entry := range r.entries {
			if len(entry.timestamps) == 0 || !entry.timestamps[len(entry.timestamps)-1].After(now.Add(-entry.interval)) {
				delete(r.entries, key)
			}
		}
		r.lastSweep = now
	}
	entry := r.entries[key]
	cutoff := now.Add(-interval)
	kept := entry.timestamps[:0]
	for _, timestamp := range entry.timestamps {
		if timestamp.After(cutoff) {
			kept = append(kept, timestamp)
		}
	}
	entry.timestamps = kept
	entry.interval = interval
	if len(entry.timestamps) >= max {
		r.entries[key] = entry
		retryAfter := entry.timestamps[0].Add(interval).Sub(now)
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		return false, retryAfter
	}
	entry.timestamps = append(entry.timestamps, now)
	r.entries[key] = entry
	return true, 0
}

func clientAddress(r *http.Request, trustedProxies []netip.Prefix) string {
	peer := parseRemoteAddress(r.RemoteAddr)
	if !peer.IsValid() {
		return r.RemoteAddr
	}
	if !addressInPrefixes(peer, trustedProxies) {
		return peer.String()
	}
	forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if forwarded == "" {
		return peer.String()
	}
	parts := strings.Split(forwarded, ",")
	chain := make([]netip.Addr, 0, len(parts)+1)
	for _, part := range parts {
		address, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil {
			return peer.String()
		}
		chain = append(chain, address.Unmap())
	}
	chain = append(chain, peer)
	for index := len(chain) - 1; index >= 0; index-- {
		if !addressInPrefixes(chain[index], trustedProxies) {
			return chain[index].String()
		}
	}
	return chain[0].String()
}

func parseRemoteAddress(value string) netip.Addr {
	host, _, err := net.SplitHostPort(value)
	if err == nil {
		address, _ := netip.ParseAddr(host)
		return address.Unmap()
	}
	address, _ := netip.ParseAddr(value)
	return address.Unmap()
}

func addressInPrefixes(address netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
