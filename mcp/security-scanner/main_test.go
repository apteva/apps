package main

import (
	"bufio"
	"context"
	"errors"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func newTestApp(t *testing.T) (*App, *sdk.AppCtx) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	// Keep tests off real DNS: no reverse names unless a test sets them.
	app.scans.reverse = func(context.Context, string) ([]string, error) { return nil, errors.New("no PTR in tests") }
	t.Cleanup(func() { _ = app.OnUnmount(ctx) })
	return app, ctx
}

func allow(t *testing.T, db *sql.DB, pid string, values ...string) {
	t.Helper()
	for _, v := range values {
		if _, err := addTarget(db, pid, "allow", v, "test"); err != nil {
			t.Fatal(err)
		}
	}
}

// ---- scope ----

func TestScopeExpandAllowlistAndExclusions(t *testing.T) {
	s, err := newScope([]string{"10.0.0.0/29", "app.example.com"}, []string{"10.0.0.3"})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "app.example.com":
			return []netip.Addr{netip.MustParseAddr("203.0.113.7")}, nil
		case "other.example.com":
			return []netip.Addr{netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("198.51.100.1")}, nil
		}
		return nil, fmt.Errorf("nxdomain")
	}
	res, err := s.Expand(context.Background(), []string{"10.0.0.0/29", "192.168.1.1", "app.example.com", "other.example.com", "missing.example.com"}, 1024, resolve)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, a := range res.Allowed {
		got[a.Address] = true
	}
	// /29 minus network (.0), broadcast (.7) and excluded .3 = .1 .2 .4 .5 .6
	for _, want := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.4", "10.0.0.5", "10.0.0.6", "203.0.113.7"} {
		if !got[want] {
			t.Errorf("expected %s allowed, got %v", want, got)
		}
	}
	for _, bad := range []string{"10.0.0.3", "192.168.1.1", "198.51.100.1", "10.0.0.0", "10.0.0.7"} {
		if got[bad] {
			t.Errorf("%s must not be allowed", bad)
		}
	}
	reasons := map[string]string{}
	for _, d := range res.Dropped {
		reasons[d.Address+d.Target] = d.Reason
	}
	if !strings.Contains(reasons["10.0.0.310.0.0.0/29"], "excluded") {
		t.Errorf("10.0.0.3 should be dropped as excluded: %v", reasons)
	}
	if !strings.Contains(reasons["198.51.100.1other.example.com"], "not on the allowlist") {
		t.Errorf("third-party IP behind a hostname must be dropped: %v", reasons)
	}
	if !strings.Contains(reasons["missing.example.com"], "did not resolve") {
		t.Errorf("unresolvable hostname should be reported: %v", reasons)
	}
}

func TestScopeRejectsOversizedRanges(t *testing.T) {
	s, _ := newScope([]string{"10.0.0.0/8"}, nil)
	if _, err := s.Expand(context.Background(), []string{"10.0.0.0/16"}, 1024, nil); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected range limit error, got %v", err)
	}
}

func TestEmptyAllowlistRefusesScan(t *testing.T) {
	app, ctx := newTestApp(t)
	_, err := app.startScan(ctx, map[string]any{"targets": []any{"127.0.0.1"}}, "test")
	if err == nil || !strings.Contains(err.Error(), "allowlist is empty") {
		t.Fatalf("expected empty-allowlist refusal, got %v", err)
	}
}

func TestOutOfScopeTargetRefused(t *testing.T) {
	app, ctx := newTestApp(t)
	allow(t, ctx.AppDB(), ctx.CurrentProject(), "127.0.0.1")
	_, err := app.startScan(ctx, map[string]any{"targets": []any{"127.0.0.2"}}, "test")
	if err == nil || !strings.Contains(err.Error(), "no target address is on the allowlist") {
		t.Fatalf("expected out-of-scope refusal, got %v", err)
	}
}

func TestPortSpecAndProfiles(t *testing.T) {
	ports, err := parsePortSpec("22, 80,8000-8002,80")
	if err != nil || fmt.Sprint(ports) != "[22 80 8000 8001 8002]" {
		t.Fatalf("parsePortSpec = %v, %v", ports, err)
	}
	if _, err := parsePortSpec("70000"); err == nil {
		t.Fatal("expected invalid port error")
	}
	quick, _ := planForProfile("quick", "", "")
	if len(quick.TCP) != 100 {
		t.Fatalf("quick profile has %d ports, want 100", len(quick.TCP))
	}
	std, _ := planForProfile("standard", "", "")
	if !containsSorted(std.TCP, 6379) || !containsSorted(std.TCP, 1024) || len(std.UDP) != 3 {
		t.Fatalf("standard profile missing expected ports")
	}
	if _, err := planForProfile("custom", "", "500"); err == nil {
		t.Fatal("custom UDP port without a probe must be rejected")
	}
}

// ---- fingerprinting ----

func TestParseBanners(t *testing.T) {
	cases := []struct {
		banner             []byte
		service, product   string
		version            string
	}{
		{[]byte("SSH-2.0-OpenSSH_8.9p1 Ubuntu-3ubuntu0.6\r\n"), "ssh", "OpenSSH", "8.9p1"},
		{[]byte("SSH-2.0-dropbear_2020.81\r\n"), "ssh", "Dropbear SSH", "2020.81"},
		{[]byte("220 (vsFTPd 3.0.3)\r\n"), "ftp", "vsftpd", "3.0.3"},
		{[]byte("220 mail.example.com ESMTP Exim 4.94.2 Mon, 1 Jan 2024\r\n"), "smtp", "Exim", "4.94.2"},
		{[]byte("RFB 003.008\n"), "vnc", "VNC", "003.008"},
		{[]byte{0xff, 0xfd, 0x18, 0xff, 0xfd, 0x20}, "telnet", "Telnet", ""},
		{mysqlGreeting("5.5.5-10.6.12-MariaDB-0ubuntu0.22.04.1"), "mysql", "MariaDB", "10.6.12"},
		{mysqlGreeting("8.0.32"), "mysql", "MySQL", "8.0.32"},
	}
	for _, c := range cases {
		info := ServiceInfo{Service: "unknown"}
		parseBanner(c.banner, &info)
		if info.Service != c.service || info.Product != c.product || info.Version != c.version {
			t.Errorf("banner %q: got %s/%s/%s, want %s/%s/%s", c.banner, info.Service, info.Product, info.Version, c.service, c.product, c.version)
		}
	}
}

func mysqlGreeting(version string) []byte {
	payload := append([]byte{10}, []byte(version)...)
	payload = append(payload, 0, 1, 0, 0, 0)
	n := len(payload)
	return append([]byte{byte(n), byte(n >> 8), byte(n >> 16), 0}, payload...)
}

func TestSNMPv3DiscoveryIsWellFormed(t *testing.T) {
	p := snmpV3Discovery()
	if p[0] != 0x30 || int(p[1]) != len(p)-2 {
		t.Fatalf("outer sequence length mismatch: % x", p)
	}
	if strings.Contains(string(p), "public") {
		t.Fatal("SNMP probe must not carry a community string")
	}
}

func TestSeverityMapping(t *testing.T) {
	cases := map[string]string{"9.8|false": "critical", "5.0|true": "critical", "7.5|false": "high", "4.0|false": "medium", "2.1|false": "low", "0|false": "info"}
	for in, want := range cases {
		var score float64
		var kev bool
		fmt.Sscanf(strings.Replace(in, "|", " ", 1), "%g %t", &score, &kev)
		if got := severityFor(score, kev); got != want {
			t.Errorf("severityFor(%s) = %s, want %s", in, got, want)
		}
	}
}

// ---- end to end against fake services on 127.0.0.1 ----

type fakeServices struct {
	sshPort, redisPort, httpPort int
	redis                        net.Listener
	closers                      []func()
}

func startFakeServices(t *testing.T) *fakeServices {
	t.Helper()
	f := &fakeServices{}
	listen := func() net.Listener {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		f.closers = append(f.closers, func() { l.Close() })
		return l
	}
	serve := func(l net.Listener, handle func(net.Conn)) {
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() { defer c.Close(); handle(c) }()
			}
		}()
	}
	ssh := listen()
	f.sshPort = ssh.Addr().(*net.TCPAddr).Port
	serve(ssh, func(c net.Conn) { fmt.Fprint(c, "SSH-2.0-OpenSSH_7.4\r\n"); time.Sleep(50 * time.Millisecond) })

	f.redis = listen()
	f.redisPort = f.redis.Addr().(*net.TCPAddr).Port
	serve(f.redis, func(c net.Conn) {
		r := bufio.NewReader(c)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch strings.TrimSpace(line) {
			case "PING":
				fmt.Fprint(c, "+PONG\r\n")
			case "INFO server":
				body := "# Server\r\nredis_version:6.0.9\r\n"
				fmt.Fprintf(c, "$%d\r\n%s\r\n", len(body), body)
			}
		}
	})

	httpL := listen()
	f.httpPort = httpL.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "nginx/1.18.0 (Ubuntu)")
		fmt.Fprint(w, "<html><title>Welcome</title></html>")
	})}
	go srv.Serve(httpL)
	f.closers = append(f.closers, func() { srv.Close() })
	t.Cleanup(func() {
		for _, c := range f.closers {
			c()
		}
	})
	return f
}

func waitForScan(t *testing.T, db *sql.DB, pid string, id int64) *Scan {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		s, err := getScan(db, pid, id)
		if err != nil {
			t.Fatal(err)
		}
		if s.Status == "completed" || s.Status == "failed" || s.Status == "cancelled" {
			return s
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("scan %d did not finish", id)
	return nil
}

func startAndWait(t *testing.T, app *App, ctx *sdk.AppCtx, args map[string]any) *Scan {
	t.Helper()
	out, err := app.startScan(ctx, args, "test")
	if err != nil {
		t.Fatal(err)
	}
	s := out.(map[string]any)["scan"].(*Scan)
	return waitForScan(t, ctx.AppDB(), ctx.CurrentProject(), s.ID)
}

func TestLocalScanFindsServicesAndExposures(t *testing.T) {
	app, ctx := newTestApp(t)
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	allow(t, db, pid, "127.0.0.1")
	f := startFakeServices(t)
	ports := fmt.Sprintf("%d,%d,%d", f.sshPort, f.redisPort, f.httpPort)

	s := startAndWait(t, app, ctx, map[string]any{"targets": []any{"127.0.0.1"}, "profile": "custom", "tcp_ports": ports, "cve_lookup": false, "timeout_ms": 500})
	if s.Status != "completed" || s.HostsUp != 1 || s.OpenPorts != 3 {
		t.Fatalf("unexpected scan result: %+v", s)
	}

	hosts, err := searchHosts(db, pid, "", false, 10)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("hosts = %v, %v", hosts, err)
	}
	byPort := map[int]Service{}
	for _, svc := range hosts[0].Services {
		byPort[svc.Port] = svc
	}
	if svc := byPort[f.sshPort]; svc.Product != "OpenSSH" || svc.Version != "7.4" {
		t.Errorf("ssh fingerprint = %+v", svc)
	}
	if svc := byPort[f.redisPort]; svc.Service != "redis" || svc.Version != "6.0.9" {
		t.Errorf("redis fingerprint = %+v", svc)
	}
	if svc := byPort[f.httpPort]; svc.Service != "http" || svc.Product != "nginx" || svc.Version != "1.18.0" {
		t.Errorf("http fingerprint = %+v", svc)
	}

	findings, _, err := searchFindings(db, pid, findingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string]Finding{}
	for _, fd := range findings {
		rules[fd.RuleID] = fd
	}
	if fd, ok := rules["exposure:redis-noauth"]; !ok || fd.Severity != "high" || fd.Port != f.redisPort {
		t.Errorf("missing unauthenticated Redis finding: %v", rules)
	}
	if _, ok := rules["http:version-disclosure"]; !ok {
		t.Errorf("missing version disclosure finding: %v", rules)
	}

	// Close Redis and rescan: its finding must be marked fixed and the diff must show the closed port.
	f.redis.Close()
	time.Sleep(50 * time.Millisecond)
	s2 := startAndWait(t, app, ctx, map[string]any{"targets": []any{"127.0.0.1"}, "profile": "custom", "tcp_ports": ports, "cve_lookup": false, "timeout_ms": 500})
	if s2.OpenPorts != 2 {
		t.Fatalf("second scan open ports = %d, want 2", s2.OpenPorts)
	}
	all, _, _ := searchFindings(db, pid, findingFilter{Status: []string{"all"}})
	for _, fd := range all {
		if fd.RuleID == "exposure:redis-noauth" && (fd.Status != "fixed" || fd.FixedScan != s2.ID) {
			t.Errorf("redis finding should be fixed by scan %d: %+v", s2.ID, fd)
		}
	}
	diff, err := diffScans(db, pid, s2.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := diff["closed_ports"].([]serviceKey)
	if len(closed) != 1 || closed[0].Port != f.redisPort {
		t.Errorf("diff closed ports = %+v", closed)
	}
	if fixed := diff["fixed_findings"].([]Finding); len(fixed) != 1 {
		t.Errorf("diff fixed findings = %+v", fixed)
	}

	report, err := buildReport(db, pid, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	md := report["markdown"].(string)
	if !strings.Contains(md, "## Summary") || !strings.Contains(md, "Web server discloses its version") {
		t.Errorf("report missing content:\n%s", md)
	}
}

func TestCVEMatchingWithKEV(t *testing.T) {
	app, ctx := newTestApp(t)
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	allow(t, db, pid, "127.0.0.1")
	f := startFakeServices(t)

	var nvdQueries []string
	nvd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nvdQueries = append(nvdQueries, r.URL.Query().Get("virtualMatchString"))
		if !strings.Contains(r.URL.Query().Get("virtualMatchString"), "openbsd:openssh:7.4") {
			fmt.Fprint(w, `{"totalResults":0,"vulnerabilities":[]}`)
			return
		}
		fmt.Fprint(w, `{"totalResults":2,"vulnerabilities":[
		  {"cve":{"id":"CVE-2018-15473","descriptions":[{"lang":"en","value":"user enumeration"}],"metrics":{"cvssMetricV31":[{"cvssData":{"baseScore":5.3}}]}}},
		  {"cve":{"id":"CVE-2023-38408","descriptions":[{"lang":"en","value":"agent forwarding RCE"}],"metrics":{"cvssMetricV31":[{"cvssData":{"baseScore":9.8}}]}}}]}`)
	}))
	defer nvd.Close()
	kev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"vulnerabilities":[{"cveID":"CVE-2018-15473"}]}`)
	}))
	defer kev.Close()
	epss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"cve":"CVE-2023-38408","epss":"0.41"}]}`)
	}))
	defer epss.Close()
	app.scans.vulns = func(db *sql.DB) *vulnSource {
		v := newVulnSource(db, "")
		v.nvdURL, v.kevURL, v.epssURL, v.nvdSpacing = nvd.URL, kev.URL, epss.URL, 0
		return v
	}

	s := startAndWait(t, app, ctx, map[string]any{"targets": []any{"127.0.0.1"}, "profile": "custom", "tcp_ports": fmt.Sprint(f.sshPort), "timeout_ms": 500})
	if s.Status != "completed" || s.Error != "" {
		t.Fatalf("scan = %+v", s)
	}
	findings, _, _ := searchFindings(db, pid, findingFilter{CVE: "CVE-2023-38408"})
	if len(findings) != 1 {
		t.Fatalf("expected one CVE finding, got %+v (queries %v)", findings, nvdQueries)
	}
	fd := findings[0]
	if fd.Severity != "critical" || !fd.KEV || fd.CVSS != 9.8 || fd.EPSS != 0.41 || fd.CVEs[0] != "CVE-2018-15473" {
		t.Errorf("unexpected CVE finding: %+v", fd)
	}
	// A second scan reuses the NVD cache.
	before := len(nvdQueries)
	startAndWait(t, app, ctx, map[string]any{"targets": []any{"127.0.0.1"}, "profile": "custom", "tcp_ports": fmt.Sprint(f.sshPort), "timeout_ms": 500})
	if len(nvdQueries) != before {
		t.Errorf("expected cached NVD results, saw %d new queries", len(nvdQueries)-before)
	}
}

func TestCancelStopsScan(t *testing.T) {
	app, ctx := newTestApp(t)
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	allow(t, db, pid, "127.0.0.1")
	if _, err := saveSettings(db, pid, map[string]any{"rate_pps": 5}); err != nil {
		t.Fatal(err)
	}
	out, err := app.startScan(ctx, map[string]any{"targets": []any{"127.0.0.1"}, "profile": "full", "treat_all_up": true, "cve_lookup": false}, "test")
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["scan"].(*Scan).ID
	if _, err := app.startScan(ctx, map[string]any{"targets": []any{"127.0.0.1"}}, "test"); err == nil {
		t.Fatal("a second concurrent scan in the same project must be refused")
	}
	time.Sleep(200 * time.Millisecond)
	if n := app.scans.cancelAll(db, pid); n != 1 {
		t.Fatalf("cancelAll = %d", n)
	}
	if s := waitForScan(t, db, pid, id); s.Status != "cancelled" {
		t.Fatalf("status = %s", s.Status)
	}
}

func TestAllowlistEditsNeedOperator(t *testing.T) {
	app, ctx := newTestApp(t)
	globalCtx = ctx
	body := `{"kind":"allow","value":"10.1.0.0/24"}`
	post := func(mut func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/targets", strings.NewReader(body))
		if mut != nil {
			mut(req)
		}
		rec := httptest.NewRecorder()
		app.handleTargets(rec, req)
		return rec
	}

	if rec := post(nil); rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous: status %d", rec.Code)
	}
	// A bare cookie is not proof of anything; only the server-set operator counts.
	if rec := post(func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "session", Value: "s1"}) }); rec.Code != http.StatusForbidden {
		t.Fatalf("cookie only: status %d", rec.Code)
	}
	for _, h := range []string{"X-Apteva-Caller-Agent", "X-Apteva-Bound-Caller-Install-ID", "X-Apteva-Subject-Type"} {
		h := h
		if rec := post(func(r *http.Request) { r.Header.Set("X-Apteva-Operator-ID", "1"); r.Header.Set(h, "7") }); rec.Code != http.StatusForbidden {
			t.Fatalf("operator + %s: status %d", h, rec.Code)
		}
	}

	rec := post(func(r *http.Request) { r.Header.Set("X-Apteva-Operator-ID", "1") })
	if rec.Code != http.StatusOK {
		t.Fatalf("operator: status %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Added []Target `json:"added"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Added) != 1 || out.Added[0].Value != "10.1.0.0/24" {
		t.Fatalf("added = %+v", out.Added)
	}

	rec = httptest.NewRecorder()
	app.handleSettings(rec, httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"rate_pps":900}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("settings without operator: status %d", rec.Code)
	}
}
