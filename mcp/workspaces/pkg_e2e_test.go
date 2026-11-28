package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestContainerModePackagesEndToEnd installs real Alpine packages, so it needs
// network and runs only on request, as root in a throwaway Alpine container:
//
//	RUN_PKG_E2E=1 go test -run TestContainerModePackagesEndToEnd -v
func TestContainerModePackagesEndToEnd(t *testing.T) {
	if os.Getenv("RUN_PKG_E2E") == "" {
		t.Skip("set RUN_PKG_E2E=1 (installs packages into this container)")
	}
	d := requireContainerMode(t)
	if _, err := os.Stat(apkPath); err != nil {
		t.Skip("no apk")
	}
	d.setLimits(256, true)
	a := localRun(t, d, map[string]any{"name": "a", "env": map[string]string{"HOME": "/workspace/.home"}})
	b := localRun(t, d, map[string]any{"name": "b", "env": map[string]string{"HOME": "/workspace/.home"}})
	sh := func(id, script string) (string, int) {
		t.Helper()
		var started executionResponse
		if err := d.Call("containers_exec_start", map[string]any{"workload_id": id, "timeout_s": 900, "argv": []string{"/bin/sh", "-c", script}}, &started); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(15 * time.Minute)
		for {
			var got executionResponse
			if err := d.Call("containers_exec_get", map[string]any{"execution_id": started.ExecutionID}, &got); err != nil {
				t.Fatal(err)
			}
			if commandTerminal(got.Execution.Status) {
				var logs executionLogsResponse
				_ = d.Call("containers_exec_logs", map[string]any{"execution_id": started.ExecutionID, "tail": 400}, &logs)
				code := -1
				if got.Execution.ExitCode != nil {
					code = *got.Execution.ExitCode
				}
				t.Logf("$ %s\n%s(exit %d)", script, logs.Logs, code)
				return logs.Logs, code
			}
			if time.Now().After(deadline) {
				t.Fatalf("still %s: %s", got.Execution.Status, script)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	must := func(id, script string, wants ...string) string {
		t.Helper()
		out, code := sh(id, script)
		if code != 0 {
			t.Fatalf("exit %d: %s\n%s", code, script, out)
		}
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Fatalf("%q missing from:\n%s", want, out)
			}
		}
		return out
	}
	uid := loadUID(t, d, a)
	must(a, "whoami; id -un; echo $USER", workspaceUserName(uid)+"\n")
	if out := must(a, "ulimit -u"); strings.TrimSpace(out) != "256" {
		t.Fatalf("ulimit -u = %q", out)
	}
	if out, code := sh(a, "pkg add --allow-untrusted foo"); code != 2 || !strings.Contains(out, "not a package name") {
		t.Fatalf("flag accepted: %d %s", code, out)
	}
	if out, code := sh(a, "pkg add definitely-not-a-real-package-xyz"); code == 0 {
		t.Fatalf("unknown package succeeded: %s", out)
	}
	must(a, "pkg add nodejs npm && node -e 'console.log(\"node\", process.version)' && npm --version", "node v")
	must(a, "npm install -g cowsay >/dev/null 2>&1 && cowsay moo", "moo")
	must(a, "pkg add python3 && python3 -m venv /workspace/.venv && /workspace/.venv/bin/pip install -q six && /workspace/.venv/bin/python -c 'import six; print(\"six\", six.__version__)'", "six ")
	must(a, "pkg add git && git clone -q --depth 1 https://github.com/octocat/Hello-World /workspace/hw && cat /workspace/hw/README", "Hello")
	must(a, "pkg add build-base && mkdir -p /workspace/app && cd /workspace/app && npm init -y >/dev/null && npm install --build-from-source bufferutil 2>&1 | tail -3 && node -e 'require(\"bufferutil\"); console.log(\"native ok\")'", "native ok")
	must(a, "printf 'int main(){return 0;}' > $TMPDIR/x.c && cc -o $TMPDIR/x $TMPDIR/x.c && $TMPDIR/x && echo c ok", "c ok")
	// setuid binaries lose the bit.
	out := must(a, "pkg add sudo; ls -l /usr/bin/sudo; sudo -n true && echo ROOT || echo no-root", "no-root")
	if strings.Contains(out, "ROOT\n") {
		t.Fatal("sudo gave root")
	}
	if info, err := os.Stat("/usr/bin/sudo"); err == nil && info.Mode()&os.ModeSetuid != 0 {
		t.Fatal("sudo kept setuid")
	}
	must(a, "pkg list", "nodejs-", "git-", "sudo-")
	if out, code := sh(b, "pkg del nodejs"); code == 0 || !strings.Contains(out, "was not added by this workspace") {
		t.Fatalf("b removed a's package: %d %s", code, out)
	}
	must(b, "node --version", "v")

	// A reset machine: packages gone, a fresh Workspaces process.
	if out, err := exec.Command(apkPath, "del", "--no-scripts", "nodejs", "npm").CombinedOutput(); err != nil {
		t.Fatalf("apk del: %v %s", err, out)
	}
	d2 := newLocalDriver(d.root, sandboxContainer, "open")
	d = d2
	must(a, "node --version", "installing this workspace's packages again", "v")

	// Processes: a daemon that left its group is listed and killed.
	must(a, "setsid sleep 300 >/dev/null 2>&1 & echo started", "started")
	var listed struct {
		Processes []localProcess `json:"processes"`
	}
	if err := d.Call("containers_processes", map[string]any{"workload_id": a}, &listed); err != nil {
		t.Fatal(err)
	}
	pid := 0
	for _, p := range listed.Processes {
		if strings.HasPrefix(p.Command, "sleep 300") {
			pid = p.PID
		}
	}
	if pid == 0 {
		t.Fatalf("sleep not listed: %+v", listed.Processes)
	}
	var killed map[string]any
	if err := d.Call("containers_process_kill", map[string]any{"workload_id": a, "pid": pid, "signal": "KILL"}, &killed); err != nil {
		t.Fatal(err)
	}
	if err := d.Call("containers_process_kill", map[string]any{"workload_id": b, "pid": pid}, &killed); err == nil {
		t.Fatal("b signalled a's process")
	}
	var gone executionResponse
	_ = d.Call("containers_destroy", map[string]any{"workload_id": a}, &gone)
	if raw, _ := os.ReadFile(etcPasswd); strings.Contains(string(raw), workspaceUserName(uid)+":") {
		t.Fatal("destroy left the passwd entry")
	}
}
