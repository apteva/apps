package main

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

// Real-Docker tests for the 0.6.0 sandbox. Opt in with RUN_CONTAINERS_TESTS=1.
// The registry_only tests need internet access to registry.npmjs.org.

const sandboxTestImage = "oven/bun:1-debian"

func sandboxApp(t *testing.T) (*App, *sql.DB, context.Context) {
	t.Helper()
	if os.Getenv("RUN_CONTAINERS_TESTS") != "1" {
		t.Skip("set RUN_CONTAINERS_TESTS=1 to run Docker sandbox tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	return &App{backend: LocalDocker{}}, testDB(t), ctx
}

func sandboxWorkload(t *testing.T, a *App, db *sql.DB, ctx context.Context, spec RunSpec) *Workload {
	t.Helper()
	if spec.Name == "" {
		spec.Name = "sbx-" + strings.ToLower(newExecutionID()[4:12])
	}
	if spec.Image == "" {
		spec.Image = sandboxTestImage
	}
	if spec.Command == nil {
		spec.Command = []string{"sleep", "infinity"}
	}
	spec.UseLocal = true
	spec.RestartPolicy = "no"
	w, err := a.createWorkload(ctx, nil, db, spec)
	if err != nil {
		t.Fatalf("create %s: %v", spec.Name, err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		if err := a.destroyWorkload(c, nil, db, w.ID, true); err != nil {
			t.Errorf("destroy %s: %v", w.ID, err)
		}
	})
	return w
}

// dexec runs a command in the workload and returns combined output and success.
func dexec(ctx context.Context, w *Workload, user string, argv ...string) (string, bool) {
	args := []string{"exec"}
	if user != "" {
		args = append(args, "--user", user)
	}
	args = append(args, w.ContainerName)
	out, err := dockerCombined(ctx, append(args, argv...)...)
	return strings.TrimSpace(out), err == nil
}

// probeScript prints one line per target: "<target> ok <status>" or "<target> blocked <reason>".
// Plain fetch honours the proxy env; "tcp:" targets open a raw socket and bypass any proxy.
const probeScript = `
for (const target of process.argv.slice(1)) {
  try {
    if (target.startsWith("tcp:")) {
      const [host, port] = target.slice(4).split(":");
      await new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error("timeout")), 4000);
        Bun.connect({ hostname: host, port: Number(port), socket: {
          open(s) { clearTimeout(timer); s.end(); resolve(); },
          error(_s, e) { clearTimeout(timer); reject(e); },
          connectError(_s, e) { clearTimeout(timer); reject(e); },
          data() {},
        }}).catch((e) => { clearTimeout(timer); reject(e); });
      });
      console.log(target, "ok");
    } else {
      const r = await fetch(target, { signal: AbortSignal.timeout(10000) });
      console.log(target, r.ok ? "ok" : "blocked", r.status);
    }
  } catch (e) {
    console.log(target, "blocked", e.code || e.message);
  }
}`

func probe(t *testing.T, ctx context.Context, w *Workload, targets ...string) map[string]bool {
	t.Helper()
	out, _ := dexec(ctx, w, "", append([]string{"bun", "-e", probeScript}, targets...)...)
	got := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			got[fields[0]] = fields[1] == "ok"
		}
	}
	for _, target := range targets {
		if _, ok := got[target]; !ok {
			t.Fatalf("probe printed nothing for %s: %s", target, out)
		}
	}
	return got
}

func expectReach(t *testing.T, label string, got map[string]bool, want map[string]bool) {
	t.Helper()
	for target, reachable := range want {
		if got[target] != reachable {
			t.Fatalf("%s: %s reachable=%v, want %v (all: %v)", label, target, got[target], reachable, got)
		}
	}
}

func TestDockerNetworkNoneBlocksAllTraffic(t *testing.T) {
	a, db, ctx := sandboxApp(t)
	w := sandboxWorkload(t, a, db, ctx, RunSpec{NetworkMode: NetworkNone})
	nets, err := containerNetworks(ctx, docker, w.ContainerName)
	if err != nil || len(nets) != 0 {
		t.Fatalf("none-mode container networks=%v err=%v", nets, err)
	}
	expectReach(t, "none", probe(t, ctx, w, "https://registry.npmjs.org/", "tcp:1.1.1.1:443", "tcp:8.8.8.8:53"),
		map[string]bool{"https://registry.npmjs.org/": false, "tcp:1.1.1.1:443": false, "tcp:8.8.8.8:53": false})

	// Switching between commands: open up, then lock down again.
	if _, err := a.setNetworkMode(ctx, nil, db, w.ID, NetworkDefault, nil); err != nil {
		t.Fatal(err)
	}
	expectReach(t, "default", probe(t, ctx, w, "tcp:1.1.1.1:443"), map[string]bool{"tcp:1.1.1.1:443": true})
	got, err := a.setNetworkMode(ctx, nil, db, w.ID, NetworkNone, nil)
	if err != nil || got.NetworkMode != NetworkNone {
		t.Fatalf("set none: %v %+v", err, got)
	}
	expectReach(t, "none again", probe(t, ctx, w, "tcp:1.1.1.1:443"), map[string]bool{"tcp:1.1.1.1:443": false})
}

func TestDockerRegistryOnlyBlocksOtherHosts(t *testing.T) {
	a, db, ctx := sandboxApp(t)
	w := sandboxWorkload(t, a, db, ctx, RunSpec{NetworkMode: NetworkRegistryOnly})
	reg := "https://registry.npmjs.org/left-pad"
	expectReach(t, "registry_only", probe(t, ctx, w, reg, "https://example.com/", "http://example.com/", "tcp:1.1.1.1:443", "https://registry.npmjs.org.example.com/"),
		map[string]bool{reg: true, "https://example.com/": false, "http://example.com/": false, "tcp:1.1.1.1:443": false, "https://registry.npmjs.org.example.com/": false})

	// A real install goes through the proxy; it is what the prepare phase does.
	if out, ok := dexec(ctx, w, "", "sh", "-c", "mkdir -p /work && cd /work && bun add left-pad@1.3.0 && test -f node_modules/left-pad/package.json"); !ok {
		t.Fatalf("install through proxy failed: %s", out)
	}

	// Then the test phase runs with no network at all.
	if _, err := a.setNetworkMode(ctx, nil, db, w.ID, NetworkNone, nil); err != nil {
		t.Fatal(err)
	}
	expectReach(t, "test phase", probe(t, ctx, w, reg, "tcp:1.1.1.1:443"), map[string]bool{reg: false, "tcp:1.1.1.1:443": false})
	_, proxy := sandboxNames(w.ContainerName, w.NetworkName)
	if _, err := docker(ctx, "container", "inspect", proxy); !dockerMissing(err) {
		t.Fatalf("egress proxy left behind after leaving registry_only: %v", err)
	}

	// A narrower allow list applied to a running workload replaces the proxy.
	if _, err := a.setNetworkMode(ctx, nil, db, w.ID, NetworkRegistryOnly, []string{"example.com"}); err != nil {
		t.Fatal(err)
	}
	expectReach(t, "custom allow", probe(t, ctx, w, "https://example.com/", reg),
		map[string]bool{"https://example.com/": true, reg: false})
}

func TestDockerRegistryOnlyCleanupOnDestroy(t *testing.T) {
	a, db, ctx := sandboxApp(t)
	w, err := a.createWorkload(ctx, nil, db, RunSpec{Name: "sbx-cleanup", Image: sandboxTestImage, Command: []string{"sleep", "infinity"}, UseLocal: true, RestartPolicy: "no", NetworkMode: NetworkRegistryOnly})
	if err != nil {
		t.Fatal(err)
	}
	internal, proxy := sandboxNames(w.ContainerName, w.NetworkName)
	if err := a.destroyWorkload(ctx, nil, db, w.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"container", "inspect", proxy}, {"container", "inspect", w.ContainerName}, {"network", "inspect", internal}, {"network", "inspect", w.NetworkName}} {
		if _, err := docker(ctx, args...); !dockerMissing(err) {
			t.Fatalf("%v still exists after destroy (err=%v)", args, err)
		}
	}
}

func TestDockerReadOnlyMountsAndHardening(t *testing.T) {
	a, db, ctx := sandboxApp(t)
	// Prepare phase: a normal workload writes the project into its volume.
	src := sandboxWorkload(t, a, db, ctx, RunSpec{User: "1000:1000", Volumes: []VolumeSpec{{Name: "project", MountPath: "/project"}}})
	if out, ok := dexec(ctx, src, "0", "sh", "-c", "chown 1000:1000 /project"); !ok {
		t.Fatal(out)
	}
	if out, ok := dexec(ctx, src, "", "sh", "-c", "echo hello > /project/file"); !ok {
		t.Fatalf("write in rw phase: %s", out)
	}

	// Test phase: a hardened workload borrows the same volume read-only.
	test := sandboxWorkload(t, a, db, ctx, RunSpec{
		User: "1000:1000", Hardened: true, NetworkMode: NetworkNone,
		Volumes: []VolumeSpec{{Name: "project", MountPath: "/project", ReadOnly: true, SharedFrom: src.ID}, {Name: "cache", MountPath: "/cache", ReadOnly: true}},
		Tmpfs:   []TmpfsSpec{{MountPath: "/scratch", SizeMB: 1}},
	})
	if out, ok := dexec(ctx, test, "", "cat", "/project/file"); !ok || out != "hello" {
		t.Fatalf("read shared volume: %q %v", out, ok)
	}
	for _, target := range []string{"/project/new", "/project/file", "/cache/x", "/etc/x", "/usr/local/x"} {
		if out, ok := dexec(ctx, test, "", "sh", "-c", "echo x > "+target); ok || !strings.Contains(strings.ToLower(out), "read-only") {
			t.Fatalf("write to %s: ok=%v out=%q", target, ok, out)
		}
	}
	if out, ok := dexec(ctx, test, "", "sh", "-c", "echo x > /tmp/x && cat /tmp/x"); !ok || out != "x" {
		t.Fatalf("write to tmpfs /tmp: %q %v", out, ok)
	}
	if out, ok := dexec(ctx, test, "", "sh", "-c", "head -c 2097152 /dev/zero > /scratch/big"); ok || !strings.Contains(out, "No space") {
		t.Fatalf("tmpfs size limit not enforced: ok=%v %q", ok, out)
	}
	status, _ := dexec(ctx, test, "", "sh", "-c", "id -u; grep -E '^(CapEff|CapBnd|NoNewPrivs):' /proc/self/status")
	for _, want := range []string{"1000", "CapEff:\t0000000000000000", "CapBnd:\t0000000000000000", "NoNewPrivs:\t1"} {
		if !strings.Contains(status, want) {
			t.Fatalf("hardening: missing %q in\n%s", want, status)
		}
	}
	// Even an explicit root exec has no capabilities left.
	if out, ok := dexec(ctx, test, "0", "sh", "-c", "chown 0 /tmp/x"); ok {
		t.Fatalf("root exec kept CAP_CHOWN: %s", out)
	}
	pids, _ := docker(ctx, "container", "inspect", "--format", "{{.HostConfig.PidsLimit}} {{.HostConfig.ReadonlyRootfs}}", test.ContainerName)
	if strings.TrimSpace(pids) != "256 true" {
		t.Fatalf("pids/readonly = %q", pids)
	}
	if out, _ := dexec(ctx, test, "", "sh", "-c", "ls -la /var/run/docker.sock /run/docker.sock 2>&1; env"); strings.Contains(out, "srw") || strings.Contains(out, "APTEVA_") || strings.Contains(out, "DOCKER_HOST") {
		t.Fatalf("docker socket or platform env inside sandbox:\n%s", out)
	}

	// Executions still work on a read-only root (control files live in /tmp).
	id := newExecutionID()
	runtimeID, err := (LocalDocker{}).StartExecution(ctx, executionRuntimeSpec{ExecutionID: id, ContainerName: test.ContainerName, User: test.User, Argv: []string{"sh", "-c", "cat /project/file"}})
	if err != nil {
		t.Fatalf("execution on hardened workload: %v", err)
	}
	e := &Execution{ID: id, RuntimeContainerID: runtimeID, RuntimeContainerName: test.ContainerName}
	waitDockerExecution(t, ctx, e)
	if out, _, _, err := (LocalDocker{}).ExecutionOutput(ctx, e); err != nil || !strings.Contains(out, "hello") {
		t.Fatalf("execution output %q %v", out, err)
	}

	// Destroying the borrower must not delete the source's volume.
	if err := a.destroyWorkload(ctx, nil, db, test.ID, true); err != nil {
		t.Fatal(err)
	}
	if out, ok := dexec(ctx, src, "", "cat", "/project/file"); !ok || out != "hello" {
		t.Fatalf("source volume damaged by borrower destroy: %q", out)
	}
}

func TestDockerPidsLimitIsEnforced(t *testing.T) {
	a, db, ctx := sandboxApp(t)
	w := sandboxWorkload(t, a, db, ctx, RunSpec{PidsLimit: 32})
	out, _ := dexec(ctx, w, "", "sh", "-c", "for i in $(seq 1 64); do sleep 30 & done 2>&1; wait")
	if !strings.Contains(out, "fork") && !strings.Contains(out, "Resource temporarily unavailable") {
		t.Fatalf("64 processes started under pids_limit 32: %q", out)
	}
}

// The kill must reach every process the command started, including ones that
// left the process group (setsid) or were re-parented after a double fork.
// One-shot executions are killed on cancel. In a persistent session, cancel is
// Ctrl-C (background jobs are meant to outlive it), so closing the session is
// the operation that must reach everything.
func TestDockerCancellationKillsDetachedDescendants(t *testing.T) {
	for _, session := range []string{"", "detach-session"} {
		t.Run("session="+session, func(t *testing.T) { testCancellationKillsDetached(t, session) })
	}
}

func testCancellationKillsDetached(t *testing.T, session string) {
	persistentShells.CloseAll()
	t.Cleanup(persistentShells.CloseAll)
	ctx, name := auditContainer(t, "sleep", "infinity")
	d := LocalDocker{}
	id := newExecutionID()
	script := `sleep 1001 & setsid sleep 1002 & (setsid sh -c 'sleep 1003' &) ; (sleep 1004 &) ; sh -c 'trap "" TERM; sleep 1005' & wait`
	if session != "" {
		script = `sleep 1001 >/dev/null 2>&1 & setsid sleep 1002 >/dev/null 2>&1 & (setsid sh -c 'sleep 1003' >/dev/null 2>&1 &) ; (sleep 1004 >/dev/null 2>&1 &) ; sh -c 'trap "" TERM; sleep 1005' >/dev/null 2>&1 & true`
	}
	runtimeID, err := d.StartExecution(ctx, executionRuntimeSpec{ExecutionID: id, ContainerName: name, SessionKey: session, Argv: []string{"sh", "-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	e := &Execution{ID: id, RuntimeContainerID: runtimeID, RuntimeContainerName: name, SessionKey: session}
	sleepers := func() string {
		out, _ := dockerCombined(ctx, "exec", name, "sh", "-c", `for p in /proc/[0-9]*; do [ "$(cat $p/comm 2>/dev/null)" = sleep ] && tr '\0' ' ' < $p/cmdline 2>/dev/null && echo; done | grep '^sleep 100' | sort | tr '\n' ' '`)
		return strings.TrimSpace(out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(sleepers(), "sleep") < 5 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := sleepers(); strings.Count(got, "sleep") != 5 {
		t.Fatalf("expected 5 sleepers before cancel, got %q", got)
	}
	if session == "" {
		if err := d.StopExecution(ctx, e); err != nil {
			t.Fatal(err)
		}
	} else {
		waitDockerExecution(t, ctx, e)
		if err := persistentShells.CloseSession(name, session); err != nil {
			t.Fatal(err)
		}
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sleepers() == "" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("processes survived cancellation: %q", sleepers())
}
