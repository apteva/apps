package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Runs the generated execution classification and process facts against
// stubbed systemd properties, without host-specific ownership commands.
func runNativeExecutionFragment(t *testing.T, root string, vars string) (string, *nativeProcessObservation) {
	t.Helper()
	cmd := nativeInspectionCommand(nativeRecord{ReleaseDir: root, DataDir: filepath.Join(root, "data"), Unit: "apteva-test.service", Artifact: nativeArtifact{Executable: "app"}}, nil, false)
	start := strings.Index(cmd, " process_group_empty() {")
	end := strings.Index(cmd, "metadata_bytes=")
	if start < 0 || end < start {
		t.Fatal("inspection command layout changed")
	}
	script := "set -eu\nroot=" + quoteShellArg(root) + "\nunitfile=/etc/systemd/system/apteva-test.service\nrestore_pending=false\n" +
		"date() { printf 1762160000; }\n" + vars + "\n" + strings.ReplaceAll(cmd[start:end], "/sys/fs/cgroup", filepath.Join(root, "cgroup")) + `printf '%s\n%s' "$observed" "$process_json"`
	out, e := exec.Command("sh", "-c", script).CombinedOutput()
	if e != nil {
		t.Fatal(string(out), e)
	}
	observed, raw, _ := strings.Cut(string(out), "\n")
	if raw == "null" {
		return observed, nil
	}
	var p nativeProcessObservation
	if e = json.Unmarshal([]byte(raw), &p); e != nil {
		t.Fatal(raw, e)
	}
	return observed, &p
}

func TestNativeInspectionReportsConfirmedExit(t *testing.T) {
	root := t.TempDir()
	if e := os.WriteFile(filepath.Join(root, ".start-intent"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	owned := "fragment=/etc/systemd/system/apteva-test.service; cg=; pid=0; main_pid=4242; main_start='Mon 2026-11-03 09:00:00 UTC'; main_exit='Mon 2026-11-03 09:15:00 UTC'\n"

	observed, p := runNativeExecutionFragment(t, root, owned+"state=failed; result=signal; main_code=2; main_status=9")
	if observed != "exited" || p == nil || p.PID != 4242 || p.Result != "signal" || p.ExitSignal == nil || *p.ExitSignal != 9 || p.ExitCode != nil || p.Exited != 1762160000 {
		t.Fatalf("killed process: %s %+v", observed, p)
	}
	observed, p = runNativeExecutionFragment(t, root, owned+"state=failed; result=exit-code; main_code=1; main_status=3")
	if observed != "exited" || p.ExitCode == nil || *p.ExitCode != 3 || p.ExitSignal != nil {
		t.Fatalf("failed exit: %s %+v", observed, p)
	}
	// A clean exit is still an unrequested exit: there is no .stopped intent.
	observed, p = runNativeExecutionFragment(t, root, owned+"state=inactive; result=success; main_code=1; main_status=0")
	if observed != "exited" || p.ExitCode == nil || *p.ExitCode != 0 {
		t.Fatalf("clean exit: %s %+v", observed, p)
	}
	// Hostile property values never reach the JSON verbatim.
	observed, p = runNativeExecutionFragment(t, root, owned+`state=failed; result='x","pid":1'; main_code=1; main_status='1,"y":2'`)
	if observed != "exited" || p.Result != "" || p.ExitCode != nil {
		t.Fatalf("unsanitized properties: %s %+v", observed, p)
	}
}

func TestNativeInspectionKeepsUnverifiableExitUnknown(t *testing.T) {
	root := t.TempDir()
	if e := os.WriteFile(filepath.Join(root, ".start-intent"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(filepath.Join(root, "cgroup", "unit"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "cgroup", "unit", "cgroup.procs"), []byte("777\n"), 0600); e != nil {
		t.Fatal(e)
	}
	base := "fragment=/etc/systemd/system/apteva-test.service; cg=; pid=0; result=signal; main_pid=1; main_code=2; main_status=9; main_start=; main_exit=\n"
	for name, vars := range map[string]string{
		"transitional":    base + "state=deactivating",
		"main pid alive":  base + "state=failed; pid=88",
		"foreign unit":    base + "state=failed; fragment=/run/systemd/other.service",
		"restore pending": base + "state=failed; restore_pending=true",
	} {
		if observed, p := runNativeExecutionFragment(t, root, vars); observed != "unknown" || p != nil {
			t.Fatalf("%s: %s %+v", name, observed, p)
		}
	}
	// Remaining cgroup members mean the process tree is not gone.
	if observed, _ := runNativeExecutionFragment(t, root, strings.Replace(base, "cg=;", "cg=/unit;", 1)+"state=failed"); observed != "unknown" {
		t.Fatal("live cgroup member reported as exited:", observed)
	}
	if observed, _ := runNativeExecutionFragment(t, root, strings.Replace(base, "cg=;", "cg=/empty;", 1)+"state=failed"); observed != "exited" {
		t.Fatal("absent cgroup not reported as exited:", observed)
	}
}

func TestNativeInspectionWithoutStartIntentIsNotExited(t *testing.T) {
	root := t.TempDir()
	observed, p := runNativeExecutionFragment(t, root, "fragment=; cg=; pid=0; state=inactive; result=success; main_pid=0; main_code=0; main_status=0; main_start=; main_exit=")
	if observed != "preparing" || p != nil {
		t.Fatal(observed, p)
	}
}

func TestNativeProcessObservationBounds(t *testing.T) {
	code, bad := 3, 300
	if p, e := nativeProcessFromObservation(&nativeProcessObservation{PID: 5, Result: "exit-code", ExitCode: &code, Started: 1762150000, Exited: 1762160000}); e != nil || p.ExitedAt == nil || p.ExitedAt.Unix() != 1762160000 || *p.ExitCode != 3 {
		t.Fatal(p, e)
	}
	for _, p := range []nativeProcessObservation{{PID: -1}, {Result: "Exit_Code"}, {ExitCode: &bad}, {Exited: -5}, {Started: 1 << 40}} {
		if _, e := nativeProcessFromObservation(&p); e == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
}
