package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestNativeAsRootLeavesRootLoginsAlone(t *testing.T) {
	cmd := "set -eu\nid -u"
	for _, user := range []string{"", "root"} {
		i := &Instance{SSHUser: user}
		if got := nativeAsRoot(i, cmd); got != cmd {
			t.Fatalf("user %q: command changed to %q", user, got)
		}
		if got := nativeInspectionShell(i); got != "sh" {
			t.Fatalf("user %q: inspection shell %q", user, got)
		}
	}
	if got := nativeAsRoot(nil, cmd); got != cmd {
		t.Fatalf("nil instance: %q", got)
	}
}

func TestNativeAsRootUsesNonInteractiveSudo(t *testing.T) {
	i := &Instance{SSHUser: "apteva"}
	if got := nativeAsRoot(i, "id -u"); got != "sudo -n /bin/sh -c 'id -u'" {
		t.Fatalf("wrapped = %q", got)
	}
	if got := nativeInspectionShell(i); got != "sudo -n sh" {
		t.Fatalf("inspection shell = %q", got)
	}
}

// The wrapped script must run exactly as the original: quotes, variables,
// newlines and stdin all survive. A stand-in sudo checks -n and runs the rest.
func TestNativeAsRootPreservesScript(t *testing.T) {
	bin := t.TempDir()
	fake := "#!/bin/sh\n[ \"$1\" = -n ] || { echo 'sudo without -n' >&2; exit 99; }\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `set -eu
root='/var/lib/it'"'"'s here'
name="$(printf '%s' "a b")"
printf '%s|%s|' "$root" "$name"
cat
exit 3`
	run := func(cmd string) (string, int) {
		c := exec.Command("/bin/sh", "-c", cmd)
		c.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		c.Stdin = strings.NewReader("from-stdin")
		out, err := c.Output()
		code := 0
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	wantOut, wantCode := run(script)
	gotOut, gotCode := run(nativeAsRoot(&Instance{SSHUser: "pi"}, script))
	if gotOut != wantOut || gotCode != wantCode {
		t.Fatalf("wrapped (%q, %d) != direct (%q, %d)", gotOut, gotCode, wantOut, wantCode)
	}
	if wantOut != "/var/lib/it's here|a b|from-stdin" || wantCode != 3 {
		t.Fatalf("direct run = (%q, %d)", wantOut, wantCode)
	}
}

// Ticking "Run Apteva apps on this machine" is what grants root access on a
// registered machine, and setup proves it before the machine is ready.
func TestAppsSetupGrantsSudoAndIsVerified(t *testing.T) {
	var ranApps bool
	mockHost(t, func(i *Instance, script string, d time.Duration) (string, int, error) {
		if script == appsSetupScript {
			ranApps = true
		}
		return healthyHost(i, script, d)
	})
	ctx := newInstancesTestCtx(t, "apteva.yaml", tk.WithPlatform(&onboardingPlatform{}))
	defer stopInstanceWorkers(ctx)

	plain, err := registerHost(ctx, map[string]any{"name": "Plain", "ssh_host": "192.168.1.21", "ssh_user": "apteva", "setup": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if cmd := plain.(map[string]any)["authorization"].(map[string]any)["command"].(string); strings.Contains(cmd, "/etc/sudoers.d/") {
		t.Fatal("discovery-only registration must not grant sudo")
	}

	result, err := registerHost(ctx, map[string]any{"name": "Pi", "ssh_host": "192.168.1.20", "ssh_user": "apteva", "setup": map[string]any{"apps": true}})
	if err != nil {
		t.Fatal(err)
	}
	inst := result.(map[string]any)["instance"].(*Instance)
	cmd := result.(map[string]any)["authorization"].(map[string]any)["command"].(string)
	if !strings.Contains(cmd, fmt.Sprintf("/etc/sudoers.d/apteva-instance-%d", inst.ID)) || !strings.Contains(cmd, "NOPASSWD: ALL") {
		t.Fatal("apps registration must grant the management account passwordless sudo")
	}
	if _, err = waitReadyWithOptions(context.Background(), ctx, map[string]any{"id": inst.ID, "timeout_s": 2}); err != nil {
		t.Fatal(err)
	}
	fresh, err := dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ranApps || !fresh.Setup.Verified.Apps || !slices.Contains(fresh.Setup.Completed, "App hosting") {
		t.Fatalf("app hosting not verified: ran=%v setup=%+v", ranApps, fresh.Setup)
	}
}

func TestAppsSetupRefusedOnMac(t *testing.T) {
	ctx := newInstancesTestCtx(t, "apteva.yaml", tk.WithPlatform(&onboardingPlatform{}))
	_, err := registerHost(ctx, map[string]any{"name": "Mac", "ssh_user": "apteva", "connect": "reverse_tunnel", "platform": "macos", "setup": map[string]any{"apps": true}})
	if err == nil || !strings.Contains(err.Error(), "Linux machines only") {
		t.Fatalf("err = %v", err)
	}
}

// The deploy check failing on missing sudo must reach the person deploying
// with the fix, not the generic "validation failed".
type noRootTransport struct{ nativeTransport }

func (noRootTransport) Probe(context.Context, *Instance, string) (nativeProbe, error) {
	return nativeProbe{}, errNativeNoRoot
}

func TestNativeTargetInspectionExplainsMissingSudo(t *testing.T) {
	b, _, req := newNativeTest(t)
	b.transport = noRootTransport{b.transport}
	req.Options = map[string]any{"gateway_url": "https://gateway.test"}
	_, w := nativeCall(t, b, "inspect-target", req)
	if w.Code == 200 || !strings.Contains(w.Body.String(), "Run Apteva apps on this machine") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
