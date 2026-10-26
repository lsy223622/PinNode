// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package netstack

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/waiter"
)

// A real host listener makes an accidentally ignored binding error observable:
// forwarding would connect and accept the inbound client on the default network.
func TestForwardSocketBinderTCP(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var host netip.Addr
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err == nil && prefix.Addr().Is4() && !prefix.Addr().IsLoopback() && prefix.Addr().IsGlobalUnicast() {
			host = prefix.Addr()
			break
		}
	}
	if !host.IsValid() {
		t.Skip("requires a non-loopback IPv4 interface")
	}
	destination := netip.AddrPortFrom(host, listener.Addr().(*net.TCPAddr).AddrPort().Port())
	bindErr := errors.New("selected network unavailable")
	impl := &Impl{logf: t.Logf}
	var calls int
	binder := func(fd int, got netip.AddrPort) error {
		calls++
		if fd < 0 || got != destination {
			t.Errorf("binder received fd=%d, destination=%v; want valid fd and %v", fd, got, destination)
		}
		return bindErr
	}
	for _, tc := range []struct {
		name      string
		configure func()
		loopback  bool
		wantBind  bool
		wantDial  bool
	}{
		{name: "ordinary", wantDial: true},
		{name: "binding_error", configure: func() { impl.SetForwardSocketBinder(binder) }, wantBind: true},
		{name: "cleared", configure: func() { impl.SetForwardSocketBinder(nil) }, wantDial: true},
		{name: "replaced", configure: func() {
			impl.SetForwardSocketBinder(func(int, netip.AddrPort) error { return nil })
			impl.SetForwardSocketBinder(binder)
		}, wantBind: true},
		{name: "loopback", loopback: true, wantDial: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.configure != nil {
				tc.configure()
			}
			dst := destination
			if tc.loopback {
				dst = netip.AddrPortFrom(ipv4Loopback, destination.Port())
			}
			before := calls
			accepted := false
			getClient := func(...tcpip.SettableSocketOption) *gonet.TCPConn {
				accepted = true
				return nil // The backend dial is enough to test network selection.
			}
			var wq waiter.Queue
			handled := impl.forwardTCP(getClient, netip.MustParseAddr("100.64.0.1"), &wq, dst, false)
			if handled != tc.wantDial || accepted != tc.wantDial {
				t.Errorf("forwardTCP handled=%v, accepted=%v; want both %v", handled, accepted, tc.wantDial)
			}
			if got := calls - before; (got == 1) != tc.wantBind || got > 1 {
				t.Errorf("binder calls=%d; wantBind=%v", got, tc.wantBind)
			}
		})
	}
}

func TestForwardSocketBinderUDPRejectsUnavailableNetwork(t *testing.T) {
	const wantError = "selected network unavailable"
	destination := netip.MustParseAddrPort("192.0.2.1:12345")
	var logs []string
	impl := &Impl{logf: func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}}
	impl.atomicIsLocalIPFunc.Store(func(netip.Addr) bool { return false })
	called := false
	impl.SetForwardSocketBinder(func(fd int, got netip.AddrPort) error {
		called = true
		if fd < 0 || got != destination {
			t.Errorf("binder received fd=%d, destination=%v; want valid fd and %v", fd, got, destination)
		}
		return errors.New(wantError)
	})
	// Rejection must happen before the client is consumed or packet copies start.
	impl.forwardUDP(nil, netip.MustParseAddrPort("100.64.0.1:0"), destination)
	if !called {
		t.Fatal("forwarding did not try the configured network binder")
	}
	if got := strings.Join(logs, "\n"); !strings.Contains(got, wantError) {
		t.Fatalf("missing network binding failure in logs: %s", got)
	}
}
