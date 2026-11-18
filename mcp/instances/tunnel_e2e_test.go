package main

import (
	"fmt"
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
	for _, name := range []string{server, machine} {
		docker("run", "-d", "--name", name, "--hostname", name, "--network", network, "ubuntu:24.04", "sleep", "infinity")
	}
	prepare := `set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq openssh-server openssh-client sudo iproute2 procps netcat-openbsd >/dev/null
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
 *) exit 0 ;;
esac
EOF
chmod +x /usr/local/bin/systemctl
`
	if out, code := execIn(machine, shim); code != 0 {
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

	// Destroy: the key leaves the server, the live session closes, the port frees.
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
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
