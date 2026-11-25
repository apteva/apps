package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// End-to-end reverse tunnel against real OpenSSH, using Docker:
//   - "server" plays the Apteva server: Instances' real setup and key-sync
//     scripts run there (instead of on local host 0);
//   - "machine" plays a Linux box behind a router: it runs the real enrollment
//     command. Containers have no systemd, so a small systemctl shim starts the
//     generated unit's ExecStart as its User.
//
// RUN_TUNNEL_E2E=1 go test -run TestReverseTunnelEndToEnd -v
func TestReverseTunnelEndToEnd(t *testing.T) {
	if os.Getenv("RUN_TUNNEL_E2E") == "" {
		t.Skip("set RUN_TUNNEL_E2E=1 (needs Docker and network access for apt)")
	}
	suffix := fmt.Sprint(time.Now().UnixNano() % 1_000_000)
	network, server, machine := "apteva-tunnel-e2e-"+suffix, "tunnel-server-"+suffix, "tunnel-machine-"+suffix
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	execIn := func(container, script string) (string, int) {
		cmd := exec.Command("docker", "exec", "-i", container, "sh")
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", server, machine).Run()
		_ = exec.Command("docker", "network", "rm", network).Run()
	})
	docker("network", "create", network)
	// The server publishes one port so this test process (playing Instances)
	// can reach the server's loopback tunnel listener, as Instances would.
	docker("run", "-d", "--name", server, "--hostname", server, "--network", network, "-p", "127.0.0.1::2400", "ubuntu:24.04", "sleep", "infinity")
	docker("run", "-d", "--name", machine, "--hostname", machine, "--network", network, "ubuntu:24.04", "sleep", "infinity")
	prepare := `set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq openssh-server openssh-client sudo iproute2 procps netcat-openbsd socat curl >/dev/null
mkdir -p /run/sshd
/usr/sbin/sshd
`
	for _, name := range []string{server, machine} {
		if out, code := execIn(name, prepare); code != 0 {
			t.Fatalf("prepare %s: %s", name, out)
		}
	}
	shim := `cat > /usr/local/bin/systemctl <<'EOF'
#!/bin/sh
case "$1" in
 restart)
  unit=/etc/systemd/system/$2
  user=$(sed -n 's/^User=//p' "$unit"); cmd=$(sed -n 's/^ExecStart=//p' "$unit")
  pkill -f "apteva-tunnel@" 2>/dev/null || true
  su -s /bin/sh "$user" -c "nohup $cmd >/tmp/tunnel.log 2>&1 &" ;;
 disable) pkill -f "apteva-tunnel@" 2>/dev/null || true ;;
 *) exit 0 ;;
esac
EOF
chmod +x /usr/local/bin/systemctl
`
	if out, code := execIn(machine, shim); code != 0 {
		t.Fatal(out)
	}

	// The server already has the 0.18.0 block: setup must upgrade it in place.
	legacy := "useradd --system --create-home --home-dir /var/lib/apteva-tunnel --shell /usr/sbin/nologin apteva-tunnel\n" +
		"printf '\\n%s' " + quoteShellArg(reverseTunnelConfigMarker+` (managed by Instances; remove this block to disable)
Match User apteva-tunnel
    AuthorizedKeysFile /etc/apteva-tunnel/authorized_keys
    AuthenticationMethods publickey
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    AllowTcpForwarding remote
    AllowAgentForwarding no
    AllowStreamLocalForwarding no
    X11Forwarding no
    PermitTTY no
    GatewayPorts no
    ForceCommand /bin/false
    ClientAliveInterval 30
    ClientAliveCountMax 3
`) + " >> /etc/ssh/sshd_config\n"
	if out, code := execIn(server, legacy); code != 0 {
		t.Fatal(out)
	}

	// Point Instances' server-side commands at the server container.
	oldRun, oldContainer, oldFree := reverseTunnelRunLocal, reverseTunnelInContainer, reverseTunnelPortFree
	t.Cleanup(func() {
		reverseTunnelRunLocal, reverseTunnelInContainer, reverseTunnelPortFree = oldRun, oldContainer, oldFree
	})
	reverseTunnelRunLocal = func(script string, _ time.Duration) (string, int, error) {
		out, code := execIn(server, script)
		return out, code, nil
	}
	reverseTunnelInContainer = func() bool { return false }
	reverseTunnelPortFree = func(port int) bool {
		out, _ := execIn(server, fmt.Sprintf("ss -Htln 'sport = :%d'", port))
		return strings.TrimSpace(out) == ""
	}
	t.Setenv("APTEVA_INSTANCES_TUNNEL_HOST", server)
	t.Setenv("APTEVA_INSTANCES_TUNNEL_SSH_PORT", "22")
	t.Setenv("APTEVA_INSTANCES_TUNNEL_PORTS", "22000-22010")

	ctx := newInstancesTestCtx(t, "apteva.yaml", tk.WithPlatform(&onboardingPlatform{}))
	result, err := registerHost(ctx, map[string]any{"name": "behind-router", "ssh_user": "apteva", "connect": "reverse_tunnel"})
	if err != nil {
		t.Fatal(err)
	}
	inst := result.(map[string]any)["instance"].(*Instance)
	command := result.(map[string]any)["authorization"].(map[string]any)["command"].(string)
	if out, code := execIn(machine, command); code != 0 || !strings.Contains(out, "SSH authorized.") {
		t.Fatalf("enrollment command failed (%d):\n%s", code, out)
	}

	// The block was upgraded once, and setup is idempotent afterwards.
	config := func() string {
		out, _ := execIn(server, "md5sum /etc/ssh/sshd_config; grep -c 'apteva-instances reverse tunnel' /etc/ssh/sshd_config; grep -c 'PermitOpen none' /etc/ssh/sshd_config")
		return out
	}
	upgraded := config()
	if !strings.HasSuffix(strings.TrimSpace(upgraded), "1\n1") {
		t.Fatalf("sshd block not upgraded exactly once: %s", upgraded)
	}
	if _, err = ensureReverseTunnelServer(); err != nil {
		t.Fatal(err)
	}
	if again := config(); again != upgraded {
		t.Fatalf("second setup changed sshd_config:\n%s\n%s", upgraded, again)
	}

	// Instances' view: the machine's sshd answers on the server's loopback port.
	stored, err := dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	keyScript := fmt.Sprintf("umask 077; printf '%%s' %s > /tmp/mgmt.key\n", quoteShellArg(stored.SSHPrivateKey))
	execIn(server, keyScript)
	var out string
	var code int
	for i := 0; i < 30; i++ {
		out, code = execIn(server, fmt.Sprintf("ssh -i /tmp/mgmt.key -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %d apteva@127.0.0.1 'hostname'", stored.SSHPort))
		if code == 0 {
			break
		}
		time.Sleep(time.Second)
	}
	if code != 0 || strings.TrimSpace(lastLine(out)) != machine {
		log, _ := execIn(machine, "cat /tmp/tunnel.log")
		t.Fatalf("SSH through the tunnel failed (%d): %s\ntunnel log:\n%s", code, out, log)
	}
	if out, _ = execIn(server, fmt.Sprintf("ss -Htln 'sport = :%d'", stored.SSHPort)); !strings.Contains(out, "127.0.0.1:") {
		t.Fatalf("tunnel listener must be loopback only: %q", out)
	}

	// The tunnel key can do nothing else.
	tunnelSSH := "key=$(ls /etc/apteva-tunnel/*.key); kh=$(ls /etc/apteva-tunnel/*.known_hosts); ssh -i \"$key\" -o BatchMode=yes -o HostKeyAlias=apteva-tunnel-server -o UserKnownHostsFile=\"$kh\" -o ConnectTimeout=10 "
	for label, script := range map[string]string{
		"run a command":        tunnelSSH + "apteva-tunnel@" + server + " id",
		"listen on other port": tunnelSSH + "-N -o ExitOnForwardFailure=yes -R 127.0.0.1:22005:127.0.0.1:22 apteva-tunnel@" + server,
		"forward outbound":     tunnelSSH + "-W 127.0.0.1:22 apteva-tunnel@" + server + " </dev/null",
		"listen publicly":      tunnelSSH + fmt.Sprintf("-N -o ExitOnForwardFailure=yes -R 0.0.0.0:%d:127.0.0.1:22 apteva-tunnel@", stored.SSHPort+1) + server,
	} {
		out, code := execIn(machine, "su -s /bin/sh apteva -c "+quoteShellArg("timeout 15 "+script))
		if code == 0 || strings.Contains(out, "uid=") {
			t.Fatalf("tunnel key could %s (exit %d): %s", label, code, out)
		}
	}

	// Native backend through the tunnel, logged in as the non-root account the
	// enrollment created: deploy scripts must run as root via sudo.
	testNativeBackendOverTunnel(t, docker, execIn, server, machine, stored)

	// Public SSH: the machine's sshd is made key-only even when a later
	// drop-in (cloud-init style) turns passwords on, and the check repeats
	// cleanly.
	execIn(machine, "printf 'PasswordAuthentication yes\\n' > /etc/ssh/sshd_config.d/50-cloud-init.conf")
	for i := 0; i < 2; i++ {
		if out, code := execIn(machine, keysOnlySSHDRemote); code != 0 || !strings.Contains(out, "SSH server accepts keys only.") {
			t.Fatalf("key-only setup (%d): %s", code, out)
		}
	}
	if out, _ := execIn(machine, "sshd -T -C user=someone,host=x,addr=203.0.113.9 | grep -E '^(passwordauthentication|kbdinteractiveauthentication) '"); strings.Count(out, " no") != 2 {
		t.Fatalf("machine still accepts passwords: %s", out)
	}

	// Another install's keys file is never replaced.
	original, _ := execIn(server, "cat "+reverseTunnelKeysFile)
	execIn(server, "printf '%s\\n%s\\n' '"+reverseTunnelKeysHeader+"someone-else' 'ssh-ed25519 AAAA other' > "+reverseTunnelKeysFile)
	if err = syncReverseTunnelKeys(ctx.AppDB()); err == nil || !strings.Contains(err.Error(), "another Instances install") {
		t.Fatalf("foreign keys file: %v", err)
	}
	execIn(server, "printf '%s' "+quoteShellArg(original)+" > "+reverseTunnelKeysFile)

	// Destroy: the key leaves the server, the live session closes, the port frees.
	cleanup := machineCleanupFor(ctx.AppDB(), stored)
	if err = destroyProviderInstance(ctx, stored); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if out, _ = execIn(server, fmt.Sprintf("ss -Htln 'sport = :%d'", stored.SSHPort)); strings.TrimSpace(out) != "" {
		diag, _ := execIn(server, fmt.Sprintf("ss -Htlnp 'sport = :%d'; id -u apteva-tunnel; ps -eo pid,uid,user:20,args | grep -i sshd", stored.SSHPort))
		t.Fatalf("tunnel still listening after destroy: %q\n%s", out, diag)
	}
	if out, _ = execIn(server, "cat "+reverseTunnelKeysFile); strings.Contains(out, "permitlisten") {
		t.Fatalf("key still authorized: %q", out)
	}
	time.Sleep(12 * time.Second) // the machine's job retries; it must stay locked out
	if out, _ = execIn(server, fmt.Sprintf("ss -Htln 'sport = :%d'", stored.SSHPort)); strings.TrimSpace(out) != "" {
		t.Fatalf("machine reconnected after destroy: %q", out)
	}

	// The cleanup command stops the retrying job and removes what setup installed.
	if out, code := execIn(machine, cleanup); code != 0 || !strings.Contains(out, "Reverse tunnel removed.") {
		t.Fatalf("cleanup (%d): %s", code, out)
	}
	if out, _ := execIn(machine, "ls /etc/systemd/system/ | grep -c apteva-tunnel; ls /etc/apteva-tunnel 2>/dev/null | wc -l; grep -c "+quoteShellArg(stored.SSHPublicKey)+" /home/apteva/.ssh/authorized_keys; pgrep -fc 'apteva-tunnel@'"); strings.Join(strings.Fields(out), ",") != "0,0,0,0" {
		t.Fatalf("after cleanup (units, tunnel files, management key, tunnel processes): %q", out)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func testNativeBackendOverTunnel(t *testing.T, docker func(...string) string, execIn func(string, string) (string, int), server, machine string, stored *Instance) {
	t.Helper()
	// Container stand-ins for what a Pi has: a systemd runtime dir, a machine
	// id, and the gateway reachable on loopback (an SSH -R forward in practice).
	health := `cat > /usr/local/bin/apteva-health <<'EOF'
#!/bin/sh
while IFS= read -r line; do case "$line" in "$(printf '\r')"|"") break ;; esac; done
printf 'HTTP/1.0 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok'
EOF
chmod +x /usr/local/bin/apteva-health
mkdir -p /run/systemd/system
printf '0123456789abcdef0123456789abcdef\n' > /etc/machine-id
nohup socat TCP-LISTEN:18080,bind=127.0.0.1,fork,reuseaddr EXEC:/usr/local/bin/apteva-health >/dev/null 2>&1 &
`
	if out, code := execIn(machine, health); code != 0 {
		t.Fatalf("machine fixtures: %s", out)
	}
	forward := fmt.Sprintf("nohup socat TCP-LISTEN:2400,fork,reuseaddr TCP:127.0.0.1:%d >/dev/null 2>&1 &\n", stored.SSHPort)
	if out, code := execIn(server, forward); code != 0 {
		t.Fatalf("server forward: %s", out)
	}
	published := strings.TrimSpace(docker("port", server, "2400/tcp"))
	_, portText, ok := strings.Cut(strings.Split(published, "\n")[0], ":")
	if !ok {
		t.Fatalf("published port: %q", published)
	}
	target := *stored
	target.SSHHost = "127.0.0.1"
	if _, err := fmt.Sscan(portText, &target.SSHPort); err != nil {
		t.Fatalf("published port %q: %v", published, err)
	}
	if target.SSHUser == "root" || target.SSHUser == "" {
		t.Fatalf("enrollment must use a non-root account, got %q", target.SSHUser)
	}
	c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// The login itself is not root.
	var whoami string
	for i := 0; i < 20; i++ {
		out, code, err := runSSH(&target, "id -u", 10*time.Second)
		if err == nil && code == 0 {
			whoami = strings.TrimSpace(out)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if whoami == "" || whoami == "0" {
		t.Fatalf("management login uid = %q, want a non-root account", whoami)
	}

	// Registered without "apps", the account has no root access: the deploy
	// check says how to fix it instead of failing generically.
	if _, err := (nativeSSH{}).Probe(c, &target, "http://127.0.0.1:18080"); !errors.Is(err, errNativeNoRoot) {
		t.Fatalf("probe without sudo: %v", err)
	}
	// Grant it exactly as an "apps" enrollment does.
	if out, code := execIn(machine, "set -eu\nuser="+quoteShellArg(target.SSHUser)+"\n"+enrollmentSudoers(target.ID)); code != 0 {
		t.Fatalf("sudoers: %s", out)
	}

	// The pre-deploy check that used to stop at "must be root".
	probe, err := nativeSSH{}.Probe(c, &target, "http://127.0.0.1:18080")
	if err != nil {
		diag, code, _ := runSSH(&target, nativeAsRoot(&target, `for check in 'test "$(uname -s)" = Linux' 'test "$(id -u)" = 0' 'test -d /run/systemd/system' \
 'command -v systemctl ss flock tar sha256sum curl stat' \
 'curl -sS -o /dev/null -w "%{http_code}" --max-time 5 http://127.0.0.1:18080/health' \
 'cat /etc/machine-id' 'ls /etc/ssh/ssh_host_ed25519_key.pub' 'getconf GNU_LIBC_VERSION' "df -Pm /var/lib" 'stat -f -c "%a %S" /var/lib'; do
 out=$(sh -c "$check" 2>&1); printf '[%s] rc=%s %s\n' "$check" "$?" "$out"; done`), 30*time.Second)
		t.Fatalf("native probe as %s: %v\ndiagnostics (exit %d):\n%s", target.SSHUser, err, code, diag)
	}
	if probe.Platform != "linux-amd64" && probe.Platform != "linux-arm64" || probe.HostKey == "" || probe.ABI != "gnu" {
		t.Fatalf("probe = %+v", probe)
	}

	// Credentials stream over stdin into a root-owned file.
	if err = nativeRunInput(c, &target, `install -d -m 0700 /var/lib/apteva-native-e2e; umask 077; cat > /var/lib/apteva-native-e2e/env; test "$(id -u)" = 0`, strings.NewReader("SECRET=1\n")); err != nil {
		t.Fatalf("stdin to root: %v", err)
	}
	if out, _ := execIn(machine, "stat -c '%U %a' /var/lib/apteva-native-e2e/env; cat /var/lib/apteva-native-e2e/env"); strings.Join(strings.Fields(out), " ") != "root 600 SECRET=1" {
		t.Fatalf("streamed file: %q", out)
	}

	// The long-lived inspection shell and the state transport run as root.
	pool := newNativeInspectionPool(c)
	defer pool.close()
	for i := 0; i < 2; i++ {
		if out, err := pool.run(c, &target, "id -u"); err != nil || out != "0" {
			t.Fatalf("inspection shell run %d: %q %v", i, out, err)
		}
	}
	var state bytes.Buffer
	if err = nativeStateSSH(c, &target, "id -u", nil, &state); err != nil || strings.TrimSpace(state.String()) != "0" {
		t.Fatalf("state transport: %q %v", state.String(), err)
	}

	// The private control port: Instances listens on its own loopback and
	// forwards through the reverse tunnel to the app's port on the machine.
	tunnels := newTunnelRegistry(dialInstanceLoopback)
	tunnel, err := tunnels.open(&target, 18080)
	if err != nil {
		t.Fatalf("control tunnel: %v", err)
	}
	defer tunnel.close()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", tunnel.localPort))
	if err != nil {
		t.Fatalf("control tunnel request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("control tunnel answered %d %q", resp.StatusCode, body)
	}
}
