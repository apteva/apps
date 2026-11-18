package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"golang.org/x/crypto/ssh"
)

// A TCP server that writes an SSH-style greeting, standing in for the
// machine's sshd at the far end of its tunnel.
func tunnelEndpoint(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = c.Write([]byte("SSH-2.0-machine\r\n"))
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func freeLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

type publicSSHFixture struct {
	server   *fakeTunnelServer
	ctx      *sdk.AppCtx
	rec      *tk.EmitRecorder
	checks   int
	checkOut func(*Instance) (string, int, error)
	first    int
}

// Fake tunnel server plus a real public listener on 127.0.0.1. Tunnel ports
// start at a live greeting endpoint, so the proxy has something to reach.
func usePublicSSH(t *testing.T) *publicSSHFixture {
	t.Helper()
	f := &publicSSHFixture{server: useFakeTunnelServer(t), rec: tk.NewEmitRecorder()}
	target := tunnelEndpoint(t)
	t.Setenv("APTEVA_INSTANCES_TUNNEL_PORTS", fmt.Sprintf("%d-%d", target, target+1))
	for f.first = freeLocalPort(t); f.first <= target+1 && target <= f.first+3; f.first = freeLocalPort(t) {
	}
	t.Setenv("APTEVA_INSTANCES_TUNNEL_PUBLIC_PORTS", fmt.Sprintf("%d-%d", f.first, f.first+3))
	fakeRun := reverseTunnelRunLocal
	reverseTunnelRunLocal = func(script string, timeout time.Duration) (string, int, error) {
		out, code, err := fakeRun(script, timeout)
		if code == 0 && strings.Contains(script, "echo FIREWALL=ufw") {
			return "FIREWALL=ufw\n", 0, nil
		}
		return out, code, err
	}
	oldHost, oldCheck := publicSSHListenHost, publicSSHCheckMachine
	publicSSHListenHost = "127.0.0.1"
	publicSSHCheckMachine = func(inst *Instance) (string, int, error) {
		f.checks++
		if f.checkOut != nil {
			return f.checkOut(inst)
		}
		return "SSH server accepts keys only.", 0, nil
	}
	oldGlobal := globalCtx
	t.Cleanup(func() {
		for id := range publicSSHStatesSnapshot() {
			stopPublicSSHProxy(id)
			setPublicSSHState(id, nil)
		}
		publicSSHListenHost, publicSSHCheckMachine, globalCtx = oldHost, oldCheck, oldGlobal
	})
	f.ctx = newInstancesTestCtx(t, "apteva.yaml", tk.WithEmitter(f.rec), tk.WithPlatform(&onboardingPlatform{}))
	globalCtx = f.ctx
	return f
}

func publicSSHStatesSnapshot() map[int64]bool {
	publicSSHStateMu.Lock()
	defer publicSSHStateMu.Unlock()
	out := map[int64]bool{}
	for id := range publicSSHStates {
		out[id] = true
	}
	for id := range publicSSHProxies {
		out[id] = true
	}
	return out
}

// Reads the greeting through the public port, or reports why it could not.
func dialPublic(port int) (string, error) {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	return string(buf[:n]), err
}

func publicEvents(rec *tk.EmitRecorder) int {
	n := 0
	for _, e := range rec.Events() {
		if e.Topic == publicSSHChangedTopic {
			n++
		}
	}
	return n
}

func TestPublicSSHEnableIsIdempotentLimitsAndSurvivesRestart(t *testing.T) {
	f := usePublicSSH(t)
	inst, _ := registerTunnel(t, f.ctx, "Office Mac")
	on, err := setPublicSSH(f.ctx, map[string]any{"id": inst.ID, "enabled": true, "max_connections": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	state := on.(map[string]any)["public_ssh"].(*PublicSSH)
	if !state.Enabled || state.Port != f.first || state.Host != "tunnel.example.test" || state.Firewall != "ufw" || f.checks != 1 {
		t.Fatalf("enable: %+v checks=%d", state, f.checks)
	}
	if got, err := dialPublic(state.Port); err != nil || !strings.HasPrefix(got, "SSH-2.0-machine") {
		t.Fatalf("through public port: %q %v", got, err)
	}
	firewall := strings.Join(f.server.scripts, "\n")
	if !strings.Contains(firewall, `ufw allow "$port/tcp"`) || !strings.Contains(firewall, fmt.Sprintf("port=%d", state.Port)) {
		t.Fatal("public port not opened in the server firewall")
	}

	// Connection limit: a second connection while the first is open is closed.
	held, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", state.Port))
	if err != nil {
		t.Fatal(err)
	}
	_ = held.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err = held.Read(make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	if got, _ := dialPublic(state.Port); strings.HasPrefix(got, "SSH-") {
		t.Fatal("connection limit not enforced")
	}
	held.Close()

	// Calling again while on keeps the port, skips the machine check and
	// applies new allowed sources at once.
	again, err := setPublicSSH(f.ctx, map[string]any{"id": inst.ID, "enabled": true, "allowed_cidrs": []any{"192.0.2.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	if s := again.(map[string]any)["public_ssh"].(*PublicSSH); s.Port != state.Port || f.checks != 1 || strings.Join(s.AllowedCIDRs, ",") != "192.0.2.0/24" {
		t.Fatalf("second enable: %+v checks=%d", s, f.checks)
	}
	time.Sleep(50 * time.Millisecond)
	if got, _ := dialPublic(state.Port); strings.HasPrefix(got, "SSH-") {
		t.Fatal("source outside allowed_cidrs was let through")
	}
	if _, err = setPublicSSH(f.ctx, map[string]any{"id": inst.ID, "enabled": true, "allowed_cidrs": []any{"127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}

	// instance_get reports it.
	got, _ := (&App{}).toolGet(f.ctx, map[string]any{"id": inst.ID})
	if p := got.(map[string]any)["instance"].(*Instance).PublicSSH; p == nil || !p.Enabled || p.Port != state.Port {
		t.Fatalf("instance_get public_ssh %+v", p)
	}

	// Restart: the proxy comes back from the database.
	stopPublicSSHProxy(inst.ID)
	setPublicSSHState(inst.ID, nil)
	if _, err := dialPublic(state.Port); err == nil {
		t.Fatal("proxy still listening after stop")
	}
	restorePublicSSH(f.ctx)
	if got, err := dialPublic(state.Port); err != nil || !strings.HasPrefix(got, "SSH-") {
		t.Fatalf("after restore: %q %v", got, err)
	}

	// A second machine never gets the same public port.
	other, _ := registerTunnel(t, f.ctx, "Studio Mac")
	second, err := setPublicSSH(f.ctx, map[string]any{"id": other.ID, "enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	if p := second.(map[string]any)["public_ssh"].(*PublicSSH).Port; p == state.Port || p < f.first || p > f.first+3 {
		t.Fatalf("second machine port %d", p)
	}

	// Off is idempotent, closes the port and the firewall rule.
	f.server.scripts = nil
	for i := 0; i < 2; i++ {
		if _, err = setPublicSSH(f.ctx, map[string]any{"id": inst.ID, "enabled": false}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dialPublic(state.Port); err == nil {
		t.Fatal("public port still open after turning off")
	}
	if n := strings.Count(strings.Join(f.server.scripts, "\n"), fmt.Sprintf("ufw delete allow '%d/tcp'", state.Port)); n != 1 {
		t.Fatalf("firewall rule removed %d times", n)
	}
	if tunnel, _ := getReverseTunnel(f.ctx.AppDB(), inst.ID); tunnel.PublicPort != 0 {
		t.Fatal("public port still recorded")
	}
	if n := publicEvents(f.rec); n != 5 { // on, update, update, second machine on, off
		t.Fatalf("public_ssh_changed events: %d", n)
	}
}

func TestPublicSSHDestroyFreesPortAndRefusals(t *testing.T) {
	f := usePublicSSH(t)
	inst, _ := registerTunnel(t, f.ctx, "Mac")
	on, err := setPublicSSH(f.ctx, map[string]any{"id": inst.ID, "enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	port := on.(map[string]any)["public_ssh"].(*PublicSSH).Port
	stored, _ := dbGetInstance(f.ctx.AppDB(), inst.ID)
	result, err := (&App{}).toolDestroy(f.ctx, map[string]any{"id": inst.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dialPublic(port); err == nil {
		t.Fatal("public port open after destroy")
	}
	cleanup, _ := result.(map[string]any)["cleanup_command"].(string)
	assertShellParses(t, "cleanup", cleanup)
	for _, want := range []string{"launchctl bootout \"system/com.apteva.$name\"", "systemctl disable --now \"$name.service\"", "/etc/apteva-tunnel/$name.key", "grep -vxF -- \"$public_key\"", fmt.Sprintf("/etc/sudoers.d/apteva-instance-%d", inst.ID), quoteShellArg(stored.SSHPublicKey)} {
		if !strings.Contains(cleanup, want) {
			t.Fatalf("cleanup command missing %q:\n%s", want, cleanup)
		}
	}

	// The freed port is reused.
	next, _ := registerTunnel(t, f.ctx, "Replacement")
	again, err := setPublicSSH(f.ctx, map[string]any{"id": next.ID, "enabled": true})
	if err != nil || again.(map[string]any)["public_ssh"].(*PublicSSH).Port != port {
		t.Fatalf("port not reused: %v %v", again, err)
	}

	// Refusals.
	direct, err := registerHost(f.ctx, map[string]any{"name": "Direct", "ssh_host": "10.0.0.9", "ssh_user": "apteva"})
	if err != nil {
		t.Fatal(err)
	}
	directID := direct.(map[string]any)["instance"].(*Instance).ID
	if _, err = setPublicSSH(f.ctx, map[string]any{"id": directID, "enabled": true}); err == nil {
		t.Fatal("public SSH allowed for a direct machine")
	}
	if _, err = registerHost(f.ctx, map[string]any{"name": "X", "ssh_host": "10.0.0.8", "ssh_user": "apteva", "public_ssh": true}); err == nil {
		t.Fatal("public_ssh accepted without a tunnel")
	}
	if _, err = parsePublicSSHOptions([]any{"not-an-ip"}, nil); err == nil {
		t.Fatal("bad CIDR accepted")
	}
	if _, err = parsePublicSSHOptions(nil, float64(500)); err == nil {
		t.Fatal("max_connections above the limit accepted")
	}
	t.Setenv("APTEVA_INSTANCES_TUNNEL_PUBLIC_PORTS", "22000-22100")
	t.Setenv("APTEVA_INSTANCES_TUNNEL_PORTS", "22050-22060")
	if _, err = loadPublicSSHConfig(nil); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlapping ranges: %v", err)
	}
}

func TestPublicSSHRequiresKeyOnlyMachine(t *testing.T) {
	f := usePublicSSH(t)
	inst, _ := registerTunnel(t, f.ctx, "Mac")
	f.checkOut = func(*Instance) (string, int, error) { return "NOSUDO", 3, nil }
	_, err := setPublicSSH(f.ctx, map[string]any{"id": inst.ID, "enabled": true})
	if err == nil || !strings.Contains(err.Error(), "APTEVA_KEYS_ONLY") {
		t.Fatalf("no-sudo machine: %v", err)
	}
	f.checkOut = func(*Instance) (string, int, error) { return "", -1, fmt.Errorf("dial tcp: connection refused") }
	if _, err = setPublicSSH(f.ctx, map[string]any{"id": inst.ID, "enabled": true}); err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("offline machine: %v", err)
	}
	if tunnel, _ := getReverseTunnel(f.ctx.AppDB(), inst.ID); tunnel.PublicPort != 0 {
		t.Fatal("refused enable allocated a port")
	}
	assertShellParses(t, "keys-only remote", keysOnlySSHDRemote)
	assertShellParses(t, "keys-only command", keysOnlySSHDCommand())

	// Registering with public_ssh: the command itself makes the machine
	// key-only before the tunnel starts; no SSH check.
	f.checks = 0
	result, err := registerHost(f.ctx, map[string]any{"name": "Public", "ssh_user": "apteva", "connect": "reverse_tunnel", "public_ssh": true, "public_ssh_allowed_cidrs": []any{"198.51.100.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	r := result.(map[string]any)
	command := r["authorization"].(map[string]any)["command"].(string)
	assertShellParses(t, "public enrollment", command)
	keysOnly, tunnelStart := strings.Index(command, "00-apteva-keys-only.conf"), strings.Index(command, "# Reverse tunnel:")
	if keysOnly < 0 || tunnelStart < 0 || keysOnly > tunnelStart || f.checks != 0 {
		t.Fatalf("key-only step must run before the tunnel (%d, %d), checks=%d", keysOnly, tunnelStart, f.checks)
	}
	if p := r["instance"].(*Instance).PublicSSH; p == nil || !p.Enabled || strings.Join(p.AllowedCIDRs, ",") != "198.51.100.0/24" {
		t.Fatalf("registered public_ssh %+v", p)
	}
}

func TestRegistrationRollbackForgetsTunnelWhenKeySyncFails(t *testing.T) {
	server := useFakeTunnelServer(t)
	ctx := newInstancesTestCtx(t, "apteva.yaml", tk.WithPlatform(&onboardingPlatform{}))
	fakeRun := reverseTunnelRunLocal
	failSync := true
	reverseTunnelRunLocal = func(script string, timeout time.Duration) (string, int, error) {
		if failSync && strings.Contains(script, reverseTunnelKeysHeader) {
			return "install: cannot create regular file", 1, nil
		}
		return fakeRun(script, timeout)
	}
	vault, err := sshVault(ctx.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	countKeys := func() int {
		entries, _ := os.ReadDir(vault.dir)
		return len(entries)
	}
	before := countKeys()
	if _, err = registerHost(ctx, map[string]any{"name": "Mac", "ssh_user": "apteva", "connect": "reverse_tunnel"}); err == nil {
		t.Fatal("registration succeeded although the key was not installed")
	}
	if tunnels, _ := listReverseTunnels(ctx.AppDB()); len(tunnels) != 0 {
		t.Fatalf("orphaned tunnel rows: %+v", tunnels)
	}
	if after := countKeys(); after != before {
		t.Fatalf("credentials left behind: %d -> %d", before, after)
	}
	failSync = false
	inst, _ := registerTunnel(t, ctx, "Mac")
	if inst.SSHPort != 22000 || strings.Count(server.keys, "permitlisten") != 1 {
		t.Fatalf("port %d keys %q", inst.SSHPort, server.keys)
	}
}

func TestTunnelKeysFileBelongsToOneInstall(t *testing.T) {
	server := useFakeTunnelServer(t)
	ctx := newInstancesTestCtx(t, "apteva.yaml", tk.WithPlatform(&onboardingPlatform{}))
	inst, _ := registerTunnel(t, ctx, "Mac")
	identity, err := installIdentity(ctx.AppDB())
	if err != nil || len(identity) != 24 {
		t.Fatalf("identity %q %v", identity, err)
	}
	if !strings.HasPrefix(server.keys, reverseTunnelKeysHeader+identity+"\n") {
		t.Fatalf("keys file header: %q", server.keys)
	}
	sync := server.scripts[len(server.scripts)-1]
	assertShellParses(t, "key sync", sync)
	if !strings.Contains(sync, "exit 7") {
		t.Fatal("key sync does not refuse another install's file")
	}
	fakeRun := reverseTunnelRunLocal
	reverseTunnelRunLocal = func(script string, timeout time.Duration) (string, int, error) {
		if strings.Contains(script, reverseTunnelKeysHeader) {
			return "FOREIGN", 7, nil
		}
		return fakeRun(script, timeout)
	}
	stored, _ := dbGetInstance(ctx.AppDB(), inst.ID)
	if err = destroyProviderInstance(ctx, stored); err == nil || !strings.Contains(err.Error(), "another Instances install") {
		t.Fatalf("foreign keys file: %v", err)
	}
}

func TestEnrollmentCommandFixes(t *testing.T) {
	inst := &Instance{ID: 9, SSHUser: "apteva", SSHPublicKey: "ssh-ed25519 AAAA test", SSHPort: 2222, Platform: "macos"}
	direct, err := hostEnrollmentCommand(inst, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(direct, "nc -z -G 3 127.0.0.1 2222") {
		t.Fatal("Remote Login check ignores the registered SSH port")
	}
	tunnel := &tunnelEnrollment{Port: 22000, LocalSSHPort: 2022, ServerHost: "h.example", ServerPort: 22, KnownHosts: "x", PrivateKey: "k", PublicKey: "ssh-ed25519 BBBB"}
	withTunnel, err := hostEnrollmentCommand(inst, "", tunnel)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"nc -z -G 3 127.0.0.1 2022", `launchctl print "system/$label"`, "sleep 0.5", "ssh-keygen -lf", "expected_host_key"} {
		if !strings.Contains(withTunnel, want) {
			t.Fatalf("enrollment missing %q", want)
		}
	}
	if strings.Contains(withTunnel, "00-apteva-keys-only.conf") {
		t.Fatal("key-only change made without public SSH")
	}
	script := reverseTunnelServerSetupScript()
	for _, want := range []string{"PermitOpen none", "'permitopen none'", "'x11forwarding no'", "'allowagentforwarding no'", "cmp -s"} {
		if !strings.Contains(script, want) {
			t.Fatalf("server setup missing %q", want)
		}
	}
}

func TestPresenceAfterUpgradeRecordsFirstStateSilently(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newInstancesTestCtx(t, "apteva.yaml", tk.WithEmitter(rec), tk.WithPlatform(&onboardingPlatform{}))
	result, err := registerHost(ctx, map[string]any{"name": "old", "ssh_host": "10.0.0.5", "ssh_user": "apteva"})
	if err != nil {
		t.Fatal(err)
	}
	inst := result.(map[string]any)["instance"].(*Instance)
	// As migration 016 leaves a row that existed before and was never checked.
	if _, err = ctx.AppDB().Exec(`UPDATE instances SET presence_known=0 WHERE id=?`, inst.ID); err != nil {
		t.Fatal(err)
	}
	old := presenceProbe
	online := true
	presenceProbe = func(*Instance) bool { return online }
	t.Cleanup(func() { presenceProbe = old })
	checkPresenceOnce(ctx, context.Background())
	if got, _ := dbGetInstance(ctx.AppDB(), inst.ID); !got.Connected {
		t.Fatal("first state not recorded")
	}
	online = false
	checkPresenceOnce(ctx, context.Background())
	topics := []string{}
	for _, e := range rec.Events() {
		if e.Topic == instanceConnectedTopic || e.Topic == instanceDisconnectedTopic {
			topics = append(topics, e.Topic)
		}
	}
	if strings.Join(topics, ",") != instanceDisconnectedTopic {
		t.Fatalf("events %v", topics)
	}
}

// A minimal SSH server with ed25519 and ECDSA host keys.
func hostKeyServer(t *testing.T) (port int, ed, ec ssh.PublicKey) {
	t.Helper()
	_, edPriv, _ := ed25519.GenerateKey(rand.Reader)
	ecPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	edSigner, _ := ssh.NewSignerFromKey(edPriv)
	ecSigner, _ := ssh.NewSignerFromKey(ecPriv)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(edSigner)
	cfg.AddHostKey(ecSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					_ = ch.Reject(ssh.Prohibited, "test")
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, edSigner.PublicKey(), ecSigner.PublicKey()
}

func TestExpectedHostKeyIsCheckedInsteadOfTrustOnFirstUse(t *testing.T) {
	ctx := newInstancesTestCtx(t, "apteva.yaml", tk.WithPlatform(&onboardingPlatform{}))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	port, ed, ec := hostKeyServer(t)
	result, err := registerHost(ctx, map[string]any{"name": "m", "ssh_host": "127.0.0.1", "ssh_port": port, "ssh_user": "apteva"})
	if err != nil {
		t.Fatal(err)
	}
	id := result.(map[string]any)["instance"].(*Instance).ID
	inst, _ := dbGetInstance(ctx.AppDB(), id)

	if err = expectHostKey(ctx.AppDB(), inst, "nonsense"); err == nil {
		t.Fatal("malformed fingerprint accepted")
	}
	// A different machine's keys: the connection is refused and nothing is pinned.
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(otherPriv)
	if err = expectHostKey(ctx.AppDB(), inst, ssh.FingerprintSHA256(otherSigner.PublicKey())); err != nil {
		t.Fatal(err)
	}
	if _, err = dialSSH(inst, 3*time.Second); err == nil || !strings.Contains(err.Error(), "does not match expected_host_key") {
		t.Fatalf("mismatched key: %v", err)
	}
	if again, _ := dbGetInstance(ctx.AppDB(), id); again.SSHHostKey != "" {
		t.Fatal("mismatched key was pinned")
	}
	// The printed list (every host key): the negotiated key is in it and pinned.
	if err = expectHostKey(ctx.AppDB(), inst, ssh.FingerprintSHA256(ed)+","+ssh.FingerprintSHA256(ec)); err != nil {
		t.Fatal(err)
	}
	client, err := dialSSH(inst, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	pinned, _ := dbGetInstance(ctx.AppDB(), id)
	if pinned.SSHHostKey == "" {
		t.Fatal("expected key not pinned")
	}
	// Later connections negotiate the pinned key's type.
	raw, _ := base64.StdEncoding.DecodeString(pinned.SSHHostKey)
	key, _ := ssh.ParsePublicKey(raw)
	if algos := hostKeyAlgorithms(pinned.SSHHostKey); len(algos) != 1 || algos[0] != key.Type() {
		t.Fatalf("algorithms %v for %s", algos, key.Type())
	}
	if client, err = dialSSH(pinned, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	client.Close()
	// Checked against a pinned key after the fact.
	if err = expectHostKey(ctx.AppDB(), pinned, ssh.FingerprintSHA256(otherSigner.PublicKey())); err == nil || !strings.Contains(err.Error(), "not the machine you set up") {
		t.Fatalf("pinned mismatch: %v", err)
	}
}
