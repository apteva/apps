//go:build linux

package main

import (
	"net"
	"os"
	"testing"
)

// TestPidOwnsPort_HappyPath: when the test process holds a LISTEN
// socket, pidOwnsPort(os.Getpid(), port) must report true. This is
// the positive case the watchdog + Adopt rely on every tick — if it
// were flaky, every release would be falsely demoted.
func TestPidOwnsPort_HappyPath(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if !pidOwnsPort(os.Getpid(), port) {
		t.Fatalf("pidOwnsPort(self=%d, %d) = false; want true (procfs scan missed our own LISTEN)", os.Getpid(), port)
	}
}

// TestPidOwnsPort_WrongPid: a port we hold must NOT show as owned by
// another pid. The bug this guards against — pid 1 (init) "owning"
// our port — was the exact false positive that let an orphaned
// adopt route marcoschwartz.com to flexylead's frontend.
func TestPidOwnsPort_WrongPid(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// pid 1 is init/systemd; if it owns this random ephemeral port,
	// something is very wrong with the host.
	if pidOwnsPort(1, port) {
		t.Fatalf("pidOwnsPort(1, %d) = true; init can't own our test port", port)
	}
}

// TestPidOwnsPort_ClosedPort: after we close the listener, no pid
// owns the port. (Brief check — the kernel may keep TIME_WAIT/
// CLOSE_WAIT entries, but LISTEN rows for the port must be gone.)
func TestPidOwnsPort_ClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if pidOwnsPort(os.Getpid(), port) {
		t.Fatalf("pidOwnsPort(self, %d) = true after close; LISTEN row should be gone", port)
	}
}

// TestPidOwnsPort_InvalidInputs: pid<=0 / port<=0 must short-circuit
// false. Allocator + adopt rely on this to skip uninitialised rows.
func TestPidOwnsPort_InvalidInputs(t *testing.T) {
	for _, c := range []struct{ pid, port int }{{0, 8080}, {-1, 8080}, {1234, 0}, {1234, -1}} {
		if pidOwnsPort(c.pid, c.port) {
			t.Errorf("pidOwnsPort(%d, %d) = true; want false on invalid input", c.pid, c.port)
		}
	}
}

// TestSystemListeningPorts_IncludesOurs: after we bind, the global
// scan must include our port. The allocator uses this as a cross-
// instance check before bind-probing — if a co-located apteva-server
// holds 7100, ours must skip it.
func TestSystemListeningPorts_IncludesOurs(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	listening := systemListeningPorts()
	if !listening[port] {
		t.Fatalf("systemListeningPorts() missing :%d that we just bound", port)
	}
}

// TestParseHexPort: minimal coverage of the procfs port-decoding,
// since a misread there silently turns "port 7100 in use" into
// "nothing listening" and the allocator hands out the wrong port.
func TestParseHexPort(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"0100007F:1BBC", 7100, true},      // 0x1BBC = 7100
		{"00000000:0050", 80, true},        // :80
		{"00000000000000000000000000000000:0050", 80, true}, // ipv6 form
		{"deadbeef", 0, false},             // no colon
		{"00000000:", 0, false},            // missing port half
		{"00000000:zzzz", 0, false},        // non-hex
	}
	for _, tc := range cases {
		got, ok := parseHexPort(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseHexPort(%q) = (%d, %v); want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
