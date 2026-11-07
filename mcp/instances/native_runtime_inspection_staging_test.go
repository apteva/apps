package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Explicit opt-in: creates only a uniquely named listener fixture service and temporary
// data tree on the supplied staging host; removes both. Never targets prod.
func TestNativeInspectionStaging(t *testing.T) {
	file := os.Getenv("INSTANCES_INSPECTION_STAGING_DB")
	if file == "" {
		t.Skip("opt-in staging inspection comparison")
	}
	db, e := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	id, e := strconv.ParseInt(os.Getenv("INSTANCES_INSPECTION_STAGING_ID"), 10, 64)
	if e != nil {
		t.Fatal(e)
	}
	inst, e := dbGetInstance(db, id)
	if e != nil || inst == nil || inst.Name != "test-render" || inst.IsLocal() || inst.SSHHostKey == "" {
		t.Fatal("pinned test-render staging host required", e)
	}
	c, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out, e := nativeRun(c, inst, `cat /etc/machine-id; cat /etc/ssh/ssh_host_ed25519_key.pub; getconf _NPROCESSORS_ONLN; uname -m`)
	if e != nil {
		t.Fatal(e)
	}
	facts := strings.Split(strings.TrimSpace(out), "\n")
	if len(facts) != 4 || facts[2] != "1" {
		t.Fatal("one-core staging host required", facts)
	}
	token := nativeHash(strconv.FormatInt(time.Now().UnixNano(), 10))[:16]
	base := "/tmp/apteva-inspect-test-" + token
	d := nativeRecord{ID: "inspect-" + token, Fingerprint: nativeHash("inspect-owner-" + token), ReleaseDir: base + "/release", DataDir: base + "/data", Unit: "apteva-inspect-test-" + token + ".service", Started: true, Artifact: nativeArtifact{Executable: "app"}, Execution: nativeExecution{HostKey: "linux-systemd:" + nativeHash(facts[0]+"\n"+facts[1])}}
	d.Restore = &nativeRestore{SHA256: nativeHash("restored-data"), Key: nativeHash(token)}
	expected := d.Fingerprint + ":" + d.Restore.Key + ":" + d.Restore.SHA256
	unit := "# apteva-native-owner:" + d.Fingerprint + "\n[Service]\nExecStart=" + d.ReleaseDir + "/app " + d.ReleaseDir + "/port\nKillMode=control-group\n"
	defer func() {
		_, e := nativeRun(context.Background(), inst, `set -eu; unit=`+quoteShellArg(d.Unit)+`; unitfile=/etc/systemd/system/"$unit"; if test -f "$unitfile"; then test "$(head -n1 "$unitfile")" = `+quoteShellArg("# apteva-native-owner:"+d.Fingerprint)+`; systemctl stop "$unit"; rm -- "$unitfile"; systemctl daemon-reload; fi; rm -rf -- `+quoteShellArg(base))
		if e != nil {
			t.Error("staging cleanup failed", e)
		}
	}()
	fixturePath := os.Getenv("INSTANCES_INSPECTION_FIXTURE")
	if fixturePath == "" {
		arch := "amd64"
		if facts[3] == "aarch64" {
			arch = "arm64"
		} else if facts[3] != "x86_64" {
			t.Fatal("unsupported fixture architecture")
		}
		fixturePath = filepath.Join(t.TempDir(), "app")
		build := exec.CommandContext(c, "go", "build", "-o", fixturePath, "./testdata/inspection-fixture")
		build.Env = append(os.Environ(), "GOWORK=off", "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
		if output, e := build.CombinedOutput(); e != nil {
			t.Fatal(string(output), e)
		}
	}
	fixture, e := os.ReadFile(fixturePath)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = nativeRun(c, inst, "umask 077; mkdir -p "+quoteShellArg(d.ReleaseDir)+" "+quoteShellArg(d.DataDir)); e != nil {
		t.Fatal(e)
	}
	if _, e = uploadSSH(inst, d.ReleaseDir+"/app", base64.StdEncoding.EncodeToString(fixture)); e != nil {
		t.Fatal(e)
	}
	setup := `set -eu; umask 077; mkdir -p ` + quoteShellArg(d.ReleaseDir) + ` ` + quoteShellArg(d.DataDir) + `; chmod 0755 ` + quoteShellArg(d.ReleaseDir+"/app") + `; printf '%s' ` + quoteShellArg(d.Fingerprint) + ` > ` + quoteShellArg(d.ReleaseDir+"/.owner") + `; stat -Lc '%d:%i:%s:%y:%z' ` + quoteShellArg(d.ReleaseDir+"/app") + ` > ` + quoteShellArg(d.ReleaseDir+"/.exe-stat") + `; touch ` + quoteShellArg(d.ReleaseDir+"/.prepared") + ` ` + quoteShellArg(d.ReleaseDir+"/.start-intent") + `; printf '%s' ` + quoteShellArg(expected) + ` > ` + quoteShellArg(d.DataDir+"/"+nativeStateMarker) + `; printf '%s' ` + quoteShellArg(unit) + ` > ` + quoteShellArg("/etc/systemd/system/"+d.Unit) + `; systemctl daemon-reload; systemctl start ` + quoteShellArg(d.Unit)
	if _, e = nativeRun(c, inst, setup); e != nil {
		t.Fatal(e)
	}
	portOut, e := nativeRun(c, inst, `set -eu; for n in 1 2 3 4 5; do if test -f `+quoteShellArg(d.ReleaseDir+"/port")+`; then cat `+quoteShellArg(d.ReleaseDir+"/port")+`; exit 0; fi; sleep 0.2; done; exit 1`)
	if e != nil {
		t.Fatal(e)
	}
	port, e := strconv.Atoi(strings.TrimSpace(portOut))
	if e != nil {
		t.Fatal(e)
	}
	d.Execution.Ports = []nativePort{{Protocol: "tcp", Start: port, End: port}}
	// BEGIN TRANSPORT
	p := newNativeInspectionPool(c)
	defer p.close()
	sessions := 0
	p.dial = func(i *Instance, d time.Duration) (*ssh.Client, error) { sessions++; return dialSSH(i, d) }
	transport := nativeSSH{inspections: p}
	poll := func(first bool) (nativeObservation, error) {
		return transport.InspectState(c, inst, d, []nativeRecord{d}, first)
	}
	// END TRANSPORT
	sampler, e := dialSSH(inst, 10*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer sampler.Close()
	session, e := sampler.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	stdout, e := session.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	session.Stderr = io.Discard
	sampleScript := `while :; do IFS=' ' read -r cpu user nice system idle wait irq soft steal rest < /proc/stat; printf '%s %s %s %s %s %s %s %s\n' "$user" "$nice" "$system" "$idle" "$wait" "$irq" "$soft" "$steal"; sleep 0.25; done`
	if e = session.Start(sampleScript); e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	var samples []float64
	var rawSamples [][]uint64
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(stdout)
		var prev []uint64
		for scanner.Scan() {
			v := strings.Fields(scanner.Text())
			if len(v) != 8 {
				continue
			}
			n := make([]uint64, 8)
			for k := range n {
				n[k], _ = strconv.ParseUint(v[k], 10, 64)
			}
			mu.Lock()
			rawSamples = append(rawSamples, n)
			if prev != nil {
				var total uint64
				for k := range n {
					total += n[k] - prev[k]
				}
				idle := n[3] - prev[3] + n[4] - prev[4]
				if total > 0 {
					samples = append(samples, 100*float64(total-idle)/float64(total))
				}
			}
			mu.Unlock()
			prev = n
		}
	}()
	// Allow setup/daemon-reload and sampler login activity to settle.
	time.Sleep(3 * time.Second)
	mu.Lock()
	samples = nil
	rawSamples = nil
	mu.Unlock()
	durations := []float64{}
	idle := os.Getenv("INSTANCES_INSPECTION_IDLE") == "1"
	for n := 0; n < 20; n++ {
		started := time.Now()
		obs := nativeObservation{State: "running"}
		var e error
		if !idle {
			obs, e = poll(n == 0)
		}
		durations = append(durations, float64(time.Since(started).Microseconds())/1000)
		if e != nil || obs.State != "running" {
			t.Fatal("normal inspection", obs, e)
		}
		if delay := 2*time.Second - time.Since(started); delay > 0 {
			time.Sleep(delay)
		}
	}
	sampler.Close()
	<-done
	mu.Lock()
	cpu := append([]float64{}, samples...)
	raw := append([][]uint64{}, rawSamples...)
	mu.Unlock()
	if len(cpu) < 100 {
		t.Fatal("insufficient 250ms CPU samples", len(cpu))
	}
	sorted := append([]float64{}, cpu...)
	sort.Float64s(sorted)
	sort.Float64s(durations)
	var mean float64
	for _, v := range cpu {
		mean += v
	}
	mean /= float64(len(cpu))
	requests := 20
	if idle {
		requests = 0
	}
	report := map[string]any{"idle": idle, "cpu_sample_ms": 250, "cpu_samples": len(cpu), "cpu_mean_pct": mean, "cpu_p95_pct": sorted[len(sorted)*95/100], "cpu_peak_pct": sorted[len(sorted)-1], "inspection_p50_ms": durations[len(durations)/2], "inspection_p95_ms": durations[len(durations)*95/100], "inspections": requests, "ssh_sessions": sessions, "cpu_pct": cpu, "proc_stat": raw, "inspection_ms": durations}
	if output := os.Getenv("INSTANCES_INSPECTION_REPORT"); output != "" {
		body, _ := json.MarshalIndent(report, "", "  ")
		if e = os.WriteFile(output, body, 0600); e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("%d inspections: SSH sessions=%d duration p50=%.1fms p95=%.1fms; CPU mean=%.1f%% p95=%.1f%% peak=%.1f%% (%d samples at 250ms)", requests, sessions, durations[len(durations)/2], durations[len(durations)*95/100], mean, sorted[len(sorted)*95/100], sorted[len(sorted)-1], len(cpu))
	if idle {
		t.Log("idle host baseline; no inspection requests")
		return
	}
	// Failure detection remains live; no cached healthy observation.
	if _, e = nativeRun(c, inst, "systemctl stop "+quoteShellArg(d.Unit)); e != nil {
		t.Fatal(e)
	}
	obs, e := poll(false)
	if e == nil && obs.State == "running" {
		t.Fatal("stopped application still reported running")
	}
	// BEGIN RESTART
	p.close()
	p = newNativeInspectionPool(c)
	defer p.close()
	transport.inspections = p
	obs, e = poll(true)
	if e != nil || obs.SnapshotSHA256 != d.Restore.SHA256 {
		t.Fatal("controller recovery lost receipt", obs, e)
	}
	// An interrupted restore at the old-data-renamed boundary must recover in
	// the same inspection operation before reporting the verified receipt.
	d.Restore.SHA256 = nativeHash("next-state")
	expected = d.Fingerprint + ":" + d.Restore.Key + ":" + d.Restore.SHA256
	if _, e = nativeRun(c, inst, `set -eu; mkdir `+quoteShellArg(base+"/.restore-stage")+`; printf '%s' `+quoteShellArg(expected)+` > `+quoteShellArg(base+"/.restore-intent")+`; printf '%s' `+quoteShellArg(expected)+` > `+quoteShellArg(base+"/.restore-stage/"+nativeStateMarker)+`; printf recovered > `+quoteShellArg(base+"/.restore-stage/proof")+`; mv `+quoteShellArg(d.DataDir)+` `+quoteShellArg(base+"/.restore-previous")); e != nil {
		t.Fatal(e)
	}
	obs, e = poll(true)
	if e != nil || obs.SnapshotSHA256 != d.Restore.SHA256 {
		t.Fatal("interrupted restore not recovered", obs, e)
	}
	if _, e = nativeRun(c, inst, `test ! -e `+quoteShellArg(base+"/.restore-intent")+`; test "$(cat `+quoteShellArg(d.DataDir+"/proof")+`)" = recovered; systemctl start `+quoteShellArg(d.Unit)); e != nil {
		t.Fatal(e)
	}
	portOut, e = nativeRun(c, inst, "cat "+quoteShellArg(d.ReleaseDir+"/port"))
	if e != nil {
		t.Fatal(e)
	}
	port, e = strconv.Atoi(strings.TrimSpace(portOut))
	if e != nil {
		t.Fatal(e)
	}
	d.Execution.Ports = []nativePort{{Protocol: "tcp", Start: port, End: port}}
	obs, e = poll(false)
	if e != nil || obs.State != "running" {
		t.Fatal("normal operation after restore", obs, e)
	}
	// Missing listeners still fail; healthy process status is never enough.
	d.Execution.Ports = []nativePort{{Protocol: "tcp", Start: 65534, End: 65534}}
	obs, e = poll(false)
	if e == nil && obs.State == "running" {
		t.Fatal("missing required listener accepted")
	}
	// An unmarked partial upload retains its original data and remains
	// retryable while ordinary execution inspection reports recovery pending.
	d.Execution.Ports = nil
	d.Restore.SHA256 = nativeHash("retry-data")
	d.Restore.Key = nativeHash("retry-key-" + token)
	expected = d.Fingerprint + ":" + d.Restore.Key + ":" + d.Restore.SHA256
	if _, e = nativeRun(c, inst, `set -eu; systemctl stop `+quoteShellArg(d.Unit)+`; touch `+quoteShellArg(d.ReleaseDir+"/.stopped")+`; mkdir `+quoteShellArg(base+"/.restore-stage")+`; printf partial > `+quoteShellArg(base+"/.restore-stage/partial")+`; printf '%s' `+quoteShellArg(expected)+` > `+quoteShellArg(base+"/.restore-intent")); e != nil {
		t.Fatal(e)
	}
	obs, e = poll(true)
	if e != nil || !obs.Stopped || !obs.RestorePending || obs.SnapshotSHA256 != "" {
		t.Fatal("partial restore not safely retryable", obs, e)
	}
	var retry bytes.Buffer
	tw := tar.NewWriter(&retry)
	tw.WriteHeader(&tar.Header{Name: "proof", Mode: 0600, Size: 7, Typeflag: tar.TypeReg})
	tw.Write([]byte("retried"))
	tw.Close()
	d.Restore.Bytes = int64(retry.Len())
	if e = transport.Restore(c, inst, d, []nativeRecord{d}, &retry); e != nil {
		t.Fatal("partial upload retry", e)
	}
	obs, e = poll(true)
	if e != nil || !obs.Stopped || obs.RestorePending || obs.SnapshotSHA256 != d.Restore.SHA256 {
		t.Fatal("retry completion not verified", obs, e)
	}
	// Work only on a copy of PAM configuration; the opt-in setting is not applied.
	copy := base + "/pam-copy"
	if _, e = nativeRun(c, inst, `cp /etc/pam.d/sshd `+quoteShellArg(copy)+`; `+automationMOTDCommand(copy)); e != nil {
		t.Fatal("opt-in PAM transform failed", e)
	}
	t.Log("failure detection, controller restart, interrupted/partial restore, listener validation and PAM-copy provisioning passed")
}
