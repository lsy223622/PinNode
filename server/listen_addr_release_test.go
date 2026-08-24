//go:build !debug

package main

import "testing"

func TestDefaultListenAddrRelease(t *testing.T) {
	if defaultListenAddr != ":6633" {
		t.Fatalf("default release listen address = %q, want %q", defaultListenAddr, ":6633")
	}
}
