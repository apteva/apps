package main

import (
	"bufio"
	"io"
	"net"
	"testing"
	"time"
)

func TestTunnelRegistryBindsLoopbackAndReusesExactTarget(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	r := newTunnelRegistry(func(_ *Instance, target string) (net.Conn, error) {
		if target != "127.0.0.1:7200" {
			t.Fatalf("target = %q", target)
		}
		return net.Dial("tcp", backend.Addr().String())
	})
	defer r.closeAll()
	inst := &Instance{ID: 7, Provider: "hetzner", Status: "ready"}
	first, err := r.open(inst, 7200)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.open(inst, 7200)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("same instance and target port must reuse one tunnel")
	}
	if host := first.listener.Addr().(*net.TCPAddr).IP.String(); host != "127.0.0.1" {
		t.Fatalf("listener host = %q, want loopback", host)
	}

	conn, err := net.DialTimeout("tcp", first.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("fleet\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "fleet\n" {
		t.Fatalf("echo = %q", line)
	}
}

func TestTunnelRegistrySeparatesPortsAndClosesByInstance(t *testing.T) {
	r := newTunnelRegistry(func(_ *Instance, _ string) (net.Conn, error) {
		return nil, io.EOF
	})
	inst := &Instance{ID: 9, Provider: "hetzner", Status: "ready"}
	a, err := r.open(inst, 7100)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.open(inst, 7101)
	if err != nil {
		t.Fatal(err)
	}
	if a.localPort == b.localPort {
		t.Fatal("different target ports must use different listeners")
	}
	r.closeInstance(inst.ID)
	if _, err := net.DialTimeout("tcp", a.listener.Addr().String(), 100*time.Millisecond); err == nil {
		t.Fatal("first listener still accepts after closeInstance")
	}
	if _, err := net.DialTimeout("tcp", b.listener.Addr().String(), 100*time.Millisecond); err == nil {
		t.Fatal("second listener still accepts after closeInstance")
	}
}
