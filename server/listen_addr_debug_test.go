//go:build debug

package main

import "testing"

func TestDefaultListenAddrDebug(t *testing.T) {
	if defaultListenAddr != ":6634" {
		t.Fatalf("default debug listen address = %q, want %q", defaultListenAddr, ":6634")
	}
}
