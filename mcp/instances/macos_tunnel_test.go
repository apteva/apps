package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func assertShellParses(t *testing.T, label, script string) {
	t.Helper()
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s does not parse: %v %s", label, err, out)
	}
}

func TestEnrollmentCommandDetectsMacOSAndLinux(t *testing.T) {
	_, public, err := generateSSHKeypair()
	if err != nil {
		t.Fatal(err)
	}
	inst := &Instance{ID: 9, SSHUser: "apteva", SSHPublicKey: public, Setup: &InstanceSetup{Requested: SetupOptions{Runtimes: true}}}
	command, err := hostEnrollmentCommand(inst, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertShellParses(t, "enrollment", command)
	mac := command[strings.Index(command, `if [ "$os_name" = Darwin ]`):strings.Index(command, "\nelse\n")]
	for _, want := range []string{"sysadminctl -addUser", "IsHidden 1", "com.apple.access_ssh", "systemsetup -setremotelogin on", "Full Disk Access", "'autorestart 1'", "AutomaticallyInstallMacOSUpdates -bool false", "-t user admin", "/etc/sudoers.d/apteva-instance-9", "visudo -cf"} {
		if !strings.Contains(mac, want) {
			t.Fatalf("macOS branch missing %q", want)
		}
	}
	if !strings.Contains(command, "case \"$ID\" in ubuntu|debian)") || !strings.Contains(command, "SSH authorized.") {
		t.Fatal("Linux branch or shared success line missing")
	}
	// Without requested setup the Mac account gets no administrator rights.
	inst.Setup = &InstanceSetup{}
	plain, _ := hostEnrollmentCommand(inst, "", nil)
	if strings.Contains(plain, "-t user admin") || strings.Contains(plain, "sudoers.d") {
		t.Fatal("administrator access granted without requested setup")
	}
	// A VPN configuration is refused on macOS, not half-applied.
	withVPN, err := hostEnrollmentCommand(inst, "[Interface]\nPrivateKey = k\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertShellParses(t, "vpn enrollment", withVPN)
	macVPN := withVPN[strings.Index(withVPN, `if [ "$os_name" = Darwin ]`):strings.Index(withVPN, "\nelse\n")]
	if !strings.Contains(macVPN, "connect=reverse_tunnel") || strings.Contains(macVPN, "wg-quick") {
		t.Fatalf("macOS VPN branch: %s", macVPN)
	}
}

// Fakes the server side reached through local host 0.
type fakeTunnelServer struct {
	mu      sync.Mutex
	scripts []string
	keys    string
	fail    bool
}

func (f *fakeTunnelServer) run(script string, _ time.Duration) (string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, script)
	if f.fail {
		return "sudo: a password is required", 1, nil
	}
	if strings.Contains(script, "Match User "+reverseTunnelUser) {
		return "HOSTKEYS\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGx0ZXN0a2V5\necdsa-sha2-nistp256 AAAAE2VjZHNh\n", 0, nil
	}
	if i := strings.Index(script, "printf '%s' '"); i >= 0 {
		rest := script[i+len("printf '%s' '"):]
		f.keys = rest[:strings.Index(rest, "' > \"$tmp\"")]
	}
	return "", 0, nil
}

func useFakeTunnelServer(t *testing.T) *fakeTunnelServer {
	t.Helper()
	f := &fakeTunnelServer{}
	oldRun, oldContainer, oldFree := reverseTunnelRunLocal, reverseTunnelInContainer, reverseTunnelPortFree
	reverseTunnelRunLocal = f.run
	reverseTunnelInContainer = func() bool { return false }
	reverseTunnelPortFree = func(int) bool { return true }
	t.Cleanup(func() {
		reverseTunnelRunLocal, reverseTunnelInContainer, reverseTunnelPortFree = oldRun, oldContainer, oldFree
	})
	t.Setenv("APTEVA_INSTANCES_TUNNEL_HOST", "tunnel.example.test")
	t.Setenv("APTEVA_INSTANCES_TUNNEL_SSH_PORT", "2201")
	t.Setenv("APTEVA_INSTANCES_TUNNEL_PORTS", "22000-22001")
	return f
}

func registerTunnel(t *testing.T, ctx *sdk.AppCtx, name string) (*Instance, map[string]any) {
	t.Helper()
	result, err := registerHost(ctx, map[string]any{"name": name, "ssh_user": "apteva", "connect": "reverse_tunnel", "platform": "macos", "setup": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	r := result.(map[string]any)
	return r["instance"].(*Instance), r["authorization"].(map[string]any)
}

func TestReverseTunnelRegistrationKeysPortsResumeAndDestroy(t *testing.T) {
	server := useFakeTunnelServer(t)
	ctx := newInstancesTestCtx(t, "apteva.yaml", tk.WithPlatform(&onboardingPlatform{}))
	if err := ensureLocalInstance(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}

	first, auth := registerTunnel(t, ctx, "Studio Mac")
	if first.SSHHost != "127.0.0.1" || first.SSHPort != 22000 || first.ConnectMode != "reverse_tunnel" || first.Platform != "macos" {
		t.Fatalf("registered %+v", first)
	}
	if auth["contains_credentials"] != true || auth["tunnel_port"] != 22000 || auth["tunnel_server"] != "tunnel.example.test:2201" || !strings.Contains(auth["next_step"].(string), "Remote Login") {
		t.Fatalf("authorization %+v", auth)
	}
	command := auth["command"].(string)
	assertShellParses(t, "tunnel enrollment", command)
	for _, want := range []string{"<string>-R</string><string>127.0.0.1:22000:127.0.0.1:22</string>", "-R 127.0.0.1:22000:127.0.0.1:22 ", "apteva-tunnel@tunnel.example.test", "StrictHostKeyChecking=yes", "HostKeyAlias=apteva-tunnel-server", "ExitOnForwardFailure=yes", "/Library/LaunchDaemons/com.apteva.apteva-tunnel-", "/etc/systemd/system/apteva-tunnel-", "Restart=always"} {
		if !strings.Contains(command, want) {
			t.Fatalf("tunnel command missing %q", want)
		}
	}
	tunnel, err := getReverseTunnel(ctx.AppDB(), first.ID)
	if err != nil || tunnel == nil || !strings.Contains(tunnel.ServerKnownHosts, "apteva-tunnel-server ssh-ed25519 ") {
		t.Fatalf("tunnel row %+v %v", tunnel, err)
	}
	wantLine := `restrict,port-forwarding,command="/bin/false",permitlisten="127.0.0.1:22000" ` + tunnel.PublicKey
	if !strings.Contains(server.keys, wantLine) {
		t.Fatalf("server keys %q", server.keys)
	}
	// The tunnel key is a different key from the management key and is encrypted at rest.
	if tunnel.PublicKey == first.SSHPublicKey || !validSSHCredentialRef(tunnel.CredentialRef) {
		t.Fatal("tunnel key must be separate and vault-stored")
	}

	second, _ := registerTunnel(t, ctx, "Office Mac")
	if second.SSHPort != 22001 || strings.Count(server.keys, "permitlisten") != 2 {
		t.Fatalf("second port %d keys %q", second.SSHPort, server.keys)
	}
	if _, err = registerHost(ctx, map[string]any{"name": "Third", "ssh_user": "apteva", "connect": "reverse_tunnel"}); err == nil || !strings.Contains(err.Error(), "no free reverse tunnel port") {
		t.Fatalf("exhausted range: %v", err)
	}
	if rows, _ := dbListInstances(ctx.AppDB(), "", ""); len(rows) != 3 { // local + two
		t.Fatalf("failed registration left a row: %d", len(rows))
	}

	again, err := registerHost(ctx, map[string]any{"id": first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if again.(map[string]any)["authorization"].(map[string]any)["command"] != command {
		t.Fatal("resume changed the enrollment command")
	}

	stored, _ := dbGetInstance(ctx.AppDB(), first.ID)
	server.scripts = nil
	if err = destroyProviderInstance(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(server.keys, ":22000\"") || !strings.Contains(server.keys, ":22001\"") {
		t.Fatalf("keys after destroy %q", server.keys)
	}
	if len(server.scripts) != 1 || !strings.Contains(server.scripts[0], "sport = :22000") {
		t.Fatal("destroy did not close the live tunnel session")
	}
	assertShellParses(t, "key sync", server.scripts[0])
	if _, err = loadSSHCredential(ctx.AppDB(), reverseTunnelCredentialOwner(first.ID), tunnel.CredentialRef); err == nil {
		t.Fatal("tunnel key left in the vault")
	}
	if err = dbDeleteInstance(ctx.AppDB(), first.ID); err != nil {
		t.Fatal(err)
	}
	reused, _ := registerTunnel(t, ctx, "Replacement")
	if reused.SSHPort != 22000 {
		t.Fatalf("freed port not reused: %d", reused.SSHPort)
	}
}

func TestReverseTunnelDestroyRetriesWhenServerCleanupFails(t *testing.T) {
	server := useFakeTunnelServer(t)
	ctx := newInstancesTestCtx(t, "apteva.yaml", tk.WithPlatform(&onboardingPlatform{}))
	inst, _ := registerTunnel(t, ctx, "Mac")
	server.fail = true
	stored, _ := dbGetInstance(ctx.AppDB(), inst.ID)
	if err := destroyProviderInstance(ctx, stored); err == nil {
		t.Fatal("destroy succeeded although the server key was not removed")
	}
	if tunnel, _ := getReverseTunnel(ctx.AppDB(), inst.ID); tunnel == nil {
		t.Fatal("tunnel record dropped before the server was cleaned up")
	}
	server.fail = false
	if err := destroyProviderInstance(ctx, stored); err != nil || strings.Contains(server.keys, "permitlisten") {
		t.Fatalf("retry: %v %q", err, server.keys)
	}
}

func TestReverseTunnelConfigurationAndRefusals(t *testing.T) {
	useFakeTunnelServer(t)
	ctx := newInstancesTestCtx(t, "apteva.yaml", tk.WithPlatform(&onboardingPlatform{}))
	cases := []struct {
		args map[string]any
		env  map[string]string
		want string
	}{
		{map[string]any{"connect": "reverse_tunnel"}, map[string]string{"APTEVA_INSTANCES_TUNNEL_HOST": ""}, "set reverse_tunnel_host"},
		{map[string]any{"connect": "reverse_tunnel"}, map[string]string{"APTEVA_INSTANCES_TUNNEL_HOST": "127.0.0.1"}, "must be a public hostname"},
		{map[string]any{"connect": "reverse_tunnel"}, map[string]string{"APTEVA_INSTANCES_TUNNEL_PORTS": "80-90"}, "reverse_tunnel_ports"},
		{map[string]any{"connect": "reverse_tunnel", "ssh_host": "10.0.0.5"}, nil, "ssh_host is not used"},
		{map[string]any{"connect": "vpn", "platform": "macos"}, nil, "register Macs behind a router with connect=reverse_tunnel"},
		{map[string]any{"connect": "direct", "ssh_host": "mac.local", "platform": "macos", "setup": map[string]any{"docker": true}}, nil, "Docker setup isn't available on macOS"},
		{map[string]any{"connect": "carrier-pigeon"}, nil, "connect must be"},
		{map[string]any{"vpn": true, "connect": "direct"}, nil, "conflicts"},
	}
	for _, c := range cases {
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		args := map[string]any{"name": "m", "ssh_user": "apteva"}
		for k, v := range c.args {
			args[k] = v
		}
		if _, err := registerHost(ctx, args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%v: got %v, want %q", c.args, err, c.want)
		}
		t.Setenv("APTEVA_INSTANCES_TUNNEL_HOST", "tunnel.example.test")
		t.Setenv("APTEVA_INSTANCES_TUNNEL_PORTS", "22000-22001")
	}
	reverseTunnelInContainer = func() bool { return true }
	if _, err := registerHost(ctx, map[string]any{"name": "m", "ssh_user": "apteva", "connect": "reverse_tunnel"}); err == nil || !strings.Contains(err.Error(), "not in a container") {
		t.Fatalf("container: %v", err)
	}
}

func TestReverseTunnelServerScriptsParseAndRestrictAccount(t *testing.T) {
	script := reverseTunnelServerSetupScript()
	assertShellParses(t, "server setup", script)
	for _, want := range []string{"AuthorizedKeysFile " + reverseTunnelKeysFile, "AllowTcpForwarding remote", "PermitTTY no", "ForceCommand /bin/false", "GatewayPorts no", "ClientAliveInterval 30", `-t -f "$candidate"`, "allowtcpforwarding remote"} {
		if !strings.Contains(script, want) {
			t.Fatalf("server setup missing %q", want)
		}
	}
	for _, s := range []string{macBaselineSetupScript, macRuntimesSetupScript, macRuntimesPathCheck} {
		assertShellParses(t, "mac setup", s)
	}
}

func TestReverseTunnelServerRefusesLoginRestrictions(t *testing.T) {
	old := reverseTunnelRunLocal
	oldContainer := reverseTunnelInContainer
	t.Cleanup(func() { reverseTunnelRunLocal, reverseTunnelInContainer = old, oldContainer })
	reverseTunnelInContainer = func() bool { return false }
	reverseTunnelRunLocal = func(string, time.Duration) (string, int, error) {
		return "WARNING allowusers root deploy\nHOSTKEYS\nssh-ed25519 AAAA\n", 0, nil
	}
	if _, err := ensureReverseTunnelServer(); err == nil || !strings.Contains(err.Error(), "allow the apteva-tunnel account") {
		t.Fatalf("AllowUsers not refused: %v", err)
	}
}

func TestMacOSSetupUsesHomebrewAndChecksNewSessionPath(t *testing.T) {
	var mu sync.Mutex
	ran := []string{}
	mockHost(t, func(_ *Instance, script string, _ time.Duration) (string, int, error) {
		mu.Lock()
		ran = append(ran, script)
		mu.Unlock()
		switch script {
		case hostDiscoveryScript:
			return "Darwin\narm64\nmacos\n", 0, nil
		case macRuntimesSetupScript:
			return "node=v24.1.0\nnpm=11.3.0\ngo=go version go1.26.1 darwin/arm64\n", 0, nil
		}
		return "", 0, nil
	})
	ctx := newInstancesTestCtx(t, "apteva.yaml", tk.WithPlatform(&onboardingPlatform{}))
	result, err := registerHost(ctx, map[string]any{"name": "Mac", "ssh_host": "mac.example.test", "ssh_user": "apteva", "setup": map[string]any{"baseline": true, "runtimes": true}})
	if err != nil {
		t.Fatal(err)
	}
	inst := result.(map[string]any)["instance"].(*Instance)
	inst.workContext = context.Background()
	if err = prepareInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(ran, "\n#---\n")
	if !strings.Contains(joined, macBaselineSetupScript) || !strings.Contains(joined, macRuntimesPathCheck) || strings.Contains(joined, "apt-get") {
		t.Fatal("macOS host did not get the macOS setup steps")
	}
	got, _ := dbGetInstance(ctx.AppDB(), inst.ID)
	if got.Platform != "macos" || got.Setup.Versions["go"] == "" || !got.Setup.Verified.Runtimes {
		t.Fatalf("setup %+v", got.Setup)
	}
}

func sshGreeter(t *testing.T, greeting string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte(greeting))
			_ = c.Close()
		}
	}()
	return l.Addr().String()
}

func TestPresenceProbeReadsSSHGreetingOnly(t *testing.T) {
	inst := func(addr string) *Instance {
		host, port, _ := net.SplitHostPort(addr)
		p, _ := strconv.Atoi(port)
		return &Instance{ID: 4242, SSHHost: host, SSHPort: p}
	}
	if !probeInstancePresence(inst(sshGreeter(t, "SSH-2.0-OpenSSH_9.6\r\n"))) {
		t.Fatal("SSH server reported offline")
	}
	if probeInstancePresence(inst(sshGreeter(t, "HTTP/1.1 400 Bad Request\r\n"))) {
		t.Fatal("non-SSH listener reported online")
	}
	closed := sshGreeter(t, "")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	unused := l.Addr().String()
	l.Close()
	_ = closed
	if probeInstancePresence(inst(unused)) {
		t.Fatal("closed port reported online")
	}
}

func TestPresenceRecordsTransitionsEventsAndFilters(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newInstancesTestCtx(t, "apteva.yaml", tk.WithEmitter(rec), tk.WithPlatform(&onboardingPlatform{}))
	if err := ensureLocalInstance(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	register := func(host string) *Instance {
		result, err := registerHost(ctx, map[string]any{"name": host, "ssh_host": host, "ssh_user": "apteva"})
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]any)["instance"].(*Instance)
	}
	a, b := register("10.0.0.1"), register("10.0.0.2")
	online := map[int64]bool{a.ID: true}
	old := presenceProbe
	presenceProbe = func(inst *Instance) bool { return online[inst.ID] }
	t.Cleanup(func() { presenceProbe = old })

	checkPresenceOnce(ctx, context.Background())
	got, _ := dbGetInstance(ctx.AppDB(), a.ID)
	if !got.Connected || got.LastSeenAt == "" {
		t.Fatalf("a: %+v", got)
	}
	gotB, _ := dbGetInstance(ctx.AppDB(), b.ID)
	if gotB.Connected || gotB.LastSeenAt != "" {
		t.Fatalf("b: connected=%v seen=%q", gotB.Connected, gotB.LastSeenAt)
	}
	checkPresenceOnce(ctx, context.Background()) // no change, no event
	online[a.ID] = false
	checkPresenceOnce(ctx, context.Background())
	got, _ = dbGetInstance(ctx.AppDB(), a.ID)
	if got.Connected || got.LastSeenAt == "" {
		t.Fatal("disconnect must keep last_seen_at")
	}
	topics := []string{}
	for _, e := range rec.Events() {
		if e.Topic == instanceConnectedTopic || e.Topic == instanceDisconnectedTopic {
			topics = append(topics, e.Topic)
		}
	}
	if strings.Join(topics, ",") != "instance.connected,instance.disconnected" {
		t.Fatalf("events %v", topics)
	}

	online[b.ID] = true
	checkPresenceOnce(ctx, context.Background())
	listed, err := (&App{}).toolList(ctx, map[string]any{"connected": true})
	if err != nil {
		t.Fatal(err)
	}
	rows := listed.(map[string]any)["instances"].([]*Instance)
	ids := []int64{}
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	// The local host (id 0) is always connected; b is now online, a is not.
	if len(ids) != 2 || ids[0] != 0 || ids[1] != b.ID {
		t.Fatalf("connected=true listed %v", ids)
	}
	if _, err = (&App{}).toolList(ctx, map[string]any{"connected": "maybe"}); err == nil {
		t.Fatal("bad filter accepted")
	}

	// A destroy in progress is never overwritten by a late presence result.
	if _, err = ctx.AppDB().Exec(`UPDATE instances SET status='destroying' WHERE id=?`, b.ID); err != nil {
		t.Fatal(err)
	}
	if err = recordPresence(ctx, gotB, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	after, _ := dbGetInstance(ctx.AppDB(), b.ID)
	if !after.Connected {
		t.Fatal("presence changed a row being destroyed")
	}
}

func TestMigrationLabelsExistingVPNEnrollments(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("migrations", "015_connect_modes_and_presence.sql"))
	if err != nil || !strings.Contains(string(body), "connect_mode = 'vpn' WHERE enrollment_peer != ''") {
		t.Fatal("existing VPN hosts must keep their connect mode")
	}
}
