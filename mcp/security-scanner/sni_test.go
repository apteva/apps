package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func stubResolve(m map[string][]string) resolver {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, a := range m[host] {
			out = append(out, netip.MustParseAddr(a))
		}
		if len(out) == 0 {
			return nil, errors.New("no such host")
		}
		return out, nil
	}
}

func stubReverse(m map[string][]string) reverseResolver {
	return func(_ context.Context, addr string) ([]string, error) {
		if n, ok := m[addr]; ok {
			return n, nil
		}
		return nil, errors.New("no PTR")
	}
}

func TestTLSNamesPicksAllowlistThenConfirmedPTR(t *testing.T) {
	scope, err := newScope(
		[]string{"10.0.0.0/24", "www.example.test", "hidden.example.test"},
		[]string{"hidden.example.test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	resolve := stubResolve(map[string][]string{
		"www.example.test":    {"10.0.0.1"},
		"hidden.example.test": {"10.0.0.4"},
		"ptr.example.test":    {"10.0.0.2"},
		"liar.example.test":   {"192.0.2.9"},
	})
	reverse := stubReverse(map[string][]string{
		"10.0.0.1": {"ignored.example.test."},
		"10.0.0.2": {"ptr.example.test."},
		"10.0.0.3": {"liar.example.test."},
		"10.0.0.4": {"hidden.example.test."},
	})
	addrs := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5", "10.0.0.6"}
	have := map[string]string{"10.0.0.6": "given.example.test"}

	got := tlsNames(context.Background(), scope, addrs, have, resolve, reverse)
	want := map[string]string{
		"10.0.0.1": "www.example.test", // allowlisted hostname beats PTR
		"10.0.0.2": "ptr.example.test", // PTR that resolves back
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("tlsNames = %v, want %v", got, want)
	}
	// 10.0.0.3: PTR does not resolve back. 10.0.0.4: excluded name.
	// 10.0.0.5: no name at all. 10.0.0.6: already named by the target.
}

// A virtual-hosting TLS server (like Caddy) that rejects handshakes without
// SNI must still be fingerprinted when the target is a bare IP.
func TestBareIPScanSendsSNI(t *testing.T) {
	app, ctx := newTestApp(t)
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	allow(t, db, pid, "127.0.0.1", "site.scanner.test")
	app.scans.resolve = stubResolve(map[string][]string{"site.scanner.test": {"127.0.0.1"}})

	var sawHost string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHost = r.Host
		fmt.Fprint(w, "<html><title>Vhost</title></html>")
	}))
	srv.TLS = &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		if h.ServerName == "" {
			return nil, errors.New("no SNI")
		}
		return nil, nil
	}}
	srv.StartTLS()
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	s := startAndWait(t, app, ctx, map[string]any{"targets": []any{"127.0.0.1"}, "profile": "custom", "tcp_ports": fmt.Sprint(port), "cve_lookup": false, "timeout_ms": 500})
	if s.Status != "completed" || s.OpenPorts != 1 {
		t.Fatalf("unexpected scan: %+v", s)
	}
	hosts, err := searchHosts(db, pid, "", false, 10)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("hosts = %+v, %v", hosts, err)
	}
	h := hosts[0]
	if h.Hostname != "site.scanner.test" {
		t.Fatalf("hostname = %q, want site.scanner.test", h.Hostname)
	}
	if len(h.Services) != 1 || !h.Services[0].TLS || h.Services[0].Service != "https" {
		t.Fatalf("service = %+v, want TLS https", h.Services)
	}
	if v, _ := h.Services[0].Details["tls_version"].(string); v == "" {
		t.Fatalf("no TLS details: %+v", h.Services[0].Details)
	}
	if sawHost != "site.scanner.test" {
		t.Fatalf("HTTP Host = %q, want site.scanner.test", sawHost)
	}
}
