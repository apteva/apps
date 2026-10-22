package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeServerStateTransferContract(t *testing.T) {
	b, _, r := nativeSmallStateBackend(t)
	raw := nativeJSON(nativeCapabilitiesFor(b.snapshotLimits(), b.snapshotTimeout()))
	// Exact Server wire fields, independent of its unreleased SDK additions.
	var caps struct {
		StateTransfer *struct {
			Format   string `json:"format"`
			Archive  int64  `json:"max_archive_bytes"`
			Expanded int64  `json:"max_expanded_bytes"`
			Entries  int64  `json:"max_entries"`
			Timeout  int64  `json:"timeout_seconds"`
		} `json:"state_transfer"`
	}
	if json.Unmarshal([]byte(raw), &caps) != nil || caps.StateTransfer == nil || caps.StateTransfer.Format != "tar.gz" || caps.StateTransfer.Archive != 1<<20 || caps.StateTransfer.Expanded != 1<<20 || caps.StateTransfer.Entries != 100000 || caps.StateTransfer.Timeout != 7200 {
		t.Fatal(raw)
	}
	r.StateTransfer = &nativeStatePolicy{Format: "tar.gz", MaxArchiveBytes: 1 << 30, MaxExpandedBytes: 1 << 30, MaxEntries: 10, TimeoutSeconds: 1200}
	_, w := nativeCall(t, b, "inspect-target", r)
	if w.Code == 200 {
		t.Fatal("unsupported lifecycle policy accepted")
	}
	var n int
	b.db.QueryRow(`SELECT count(*) FROM native_deployments`).Scan(&n)
	if n != 1 {
		t.Fatal("policy check mutated deployments", n)
	}
}
func TestNativeServerPolicyEnforcedOnSnapshotAndRestore(t *testing.T) {
	for _, tc := range []string{"archive", "expanded", "entries", "unsupported", "missing_field", "format"} {
		t.Run(tc, func(t *testing.T) {
			b, f, r := nativeSmallStateBackend(t)
			f.data = map[string][]byte{"a": []byte("12345678"), "b": []byte("x")}
			policy := b.statePolicy()
			switch tc {
			case "archive":
				policy.MaxArchiveBytes = 16
			case "expanded":
				policy.MaxExpandedBytes = 4
			case "entries":
				policy.MaxEntries = 1
			case "unsupported":
				policy.MaxExpandedBytes = 2 << 20
			case "missing_field":
				policy.TimeoutSeconds = 0
			case "format":
				policy.Format = "zip"
			}
			for _, action := range []string{"snapshot", "restore"} {
				payload := archiveFixture(t, []*tar.Header{{Name: "a", Typeflag: tar.TypeReg, Size: 8}, {Name: "b", Typeflag: tar.TypeReg, Size: 1}}, [][]byte{[]byte("12345678"), []byte("x")})
				method := "GET"
				if action == "restore" {
					method = "POST"
				}
				req := httptest.NewRequest(method, nativePrefix+"/"+action+"?deployment_id="+r.DeploymentID, bytes.NewReader(payload))
				if action == "snapshot" {
					req = httptest.NewRequest(method, req.URL.String(), nil)
				}
				req.Header.Set("Idempotency-Key", tc+action)
				req.Header.Set("X-Apteva-State-Transfer-Policy", nativeJSON(policy))
				req.Header.Set("X-Apteva-Snapshot-SHA256", nativeHash(string(payload)))
				w := httptest.NewRecorder()
				b.serveHTTP(w, req)
				want := 413
				if tc == "unsupported" || tc == "missing_field" || tc == "format" {
					want = 409
				}
				if w.Code != want {
					t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
				}
			}
			if f.restores != 0 {
				t.Fatal("policy violation changed target")
			}
		})
	}
	b, _, _ := nativeSmallStateBackend(t)
	b.stateLimits = defaultNativeSnapshotLimits()
	p, e := b.stateHTTPPolicy(httptest.NewRequest("GET", nativePrefix+"/snapshot", nil))
	if e != nil || p.MaxArchiveBytes != 512<<20 || p.MaxExpandedBytes != 512<<20 || p.MaxEntries != 100000 || p.TimeoutSeconds != 1200 {
		t.Fatal("legacy fallback differs from Server", p, e)
	}
}
func TestNativeServerPolicyEntryBoundaryAndRetry(t *testing.T) {
	limits := nativeSnapshotLimits{ArchiveBytes: 1 << 20, StateBytes: 8, MaxEntries: 1}
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	tw.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir})
	tw.WriteHeader(&tar.Header{Name: "./.apteva-native-restore", Typeflag: tar.TypeReg, Size: 1})
	tw.Write([]byte("x"))
	tw.WriteHeader(&tar.Header{Name: "db", Typeflag: tar.TypeReg, Size: 8})
	tw.Write([]byte("database"))
	tw.Close()
	var stats nativeArchiveStats
	if e := canonicalNativeTarLimited(&raw, io.Discard, true, limits, &stats); e != nil || stats.Bytes != 8 || stats.Entries != 1 {
		t.Fatal("root/receipt consumed file budget", stats, e)
	}
	b, f, r := nativeSmallStateBackend(t)
	f.data = map[string][]byte{"db": []byte("database")}
	snap := stateRequest(b, "snapshot", r.DeploymentID, "stable-policy", "", nil)
	if snap.Code != 200 {
		t.Fatal(snap.Body.String())
	}
	record, _ := b.record(r.DeploymentID)
	if record.Snapshot.Policy == nil {
		t.Fatal("policy not persisted")
	}
	policy := b.statePolicy()
	policy.MaxEntries = 1
	req := httptest.NewRequest("GET", nativePrefix+"/snapshot?deployment_id="+r.DeploymentID, nil)
	req.Header.Set("Idempotency-Key", "stable-policy")
	req.Header.Set("X-Apteva-State-Transfer-Policy", nativeJSON(policy))
	w := httptest.NewRecorder()
	b.serveHTTP(w, req)
	if w.Code != 409 {
		t.Fatal("conflicting policy retry accepted", w.Code)
	}
}
func TestNativeServerPolicyDeadlineOnChunkedUpload(t *testing.T) {
	b, _, r := nativeSmallStateBackend(t)
	server := httptest.NewServer(http.HandlerFunc(b.serveHTTP))
	defer server.Close()
	conn, e := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	policy := b.statePolicy()
	policy.TimeoutSeconds = 1
	fmt.Fprintf(conn, "POST %s/restore?deployment_id=%s HTTP/1.1\r\nHost: local\r\nTransfer-Encoding: chunked\r\nIdempotency-Key: deadline\r\nX-Apteva-State-Transfer-Policy: %s\r\nX-Apteva-Snapshot-SHA256: %s\r\n\r\n7\r\npartial\r\n", nativePrefix, r.DeploymentID, nativeJSON(policy), nativeHash("partial"))
	resp, e := http.ReadResponse(bufio.NewReader(conn), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 400 || !bytes.Contains(body, []byte("incomplete_transfer")) {
		t.Fatalf("deadline not enforced: %d %s", resp.StatusCode, body)
	}
}

func TestNativeServerMetadataCacheAndReadOnlyMeasurement(t *testing.T) {
	b, _, r := nativeSmallStateBackend(t)
	d, _ := b.record(r.DeploymentID)
	i, _ := dbGetInstance(b.db, d.InstanceID)
	d.Started = true
	d.Execution.State = "running"
	_, measure := b.metadataContext(context.Background(), i, d, false)
	if !measure {
		t.Fatal("first estimate missing")
	}
	n, entries, free := int64(4380866642), int64(2), int64(32<<30)
	obs := nativeObservation{StateBytes: &n, StateEntries: &entries, StateAvailableBytes: &free}
	b.mergeStateMetadata(i, d, &obs, true)
	_, measure = b.metadataContext(context.Background(), i, d, false)
	if measure {
		t.Fatal("normal poll rescans data")
	}
	cached := nativeObservation{StateAvailableBytes: &free}
	b.mergeStateMetadata(i, d, &cached, false)
	if cached.StateBytes == nil || *cached.StateBytes != n {
		t.Fatal("live estimate missing")
	}
	_, measure = b.metadataContext(context.Background(), i, d, true)
	if !measure {
		t.Fatal("negotiated check did not refresh")
	}
	d.Execution.StoppedConfirmed = true
	_, measure = b.metadataContext(context.Background(), i, d, false)
	if !measure {
		t.Fatal("stopped check reused running estimate")
	}
	script := "set -eu\ndata=" + quoteShellArg(t.TempDir()) + "\nstat(){ printf '10000000 4096'; };\ntimeout(){ shift;shift;shift;eval \"$1\"; };\nfind(){ printf 'f 4380866642 1\\nf 0 1\\n'; };\n" + nativeInspectionMetadataScript(true) + "printf '%s %s %s' \"$metadata_bytes\" \"$metadata_entries\" \"$metadata_available\""
	out, e := exec.Command("sh", "-c", script).CombinedOutput()
	if e != nil || string(out) != "4380866642 2 40960000000" {
		t.Fatal(string(out), e)
	}
	if strings.Contains(nativeInspectionMetadataScript(false), "find ") {
		t.Fatal("routine metadata does a directory walk")
	}
	probe := "x86_64\n" + strings.Repeat("a", 32) + "\nssh-ed25519 test\n1024\n4096\nABI=gnu\nSTATE_AVAILABLE=" + strconv.FormatInt(free, 10)
	parsed, e := parseNativeTargetProbe(probe)
	if e != nil || parsed.StateAvailableBytes == nil || *parsed.StateAvailableBytes != free {
		t.Fatal("state filesystem space lost", parsed, e)
	}
}

func TestNativeStateMetadataDoesNotHideUnsafeRestoreMarker(t *testing.T) {
	dir := t.TempDir()
	outside := dir + "/outside"
	if e := os.WriteFile(outside, []byte("private"), 0600); e != nil {
		t.Fatal(e)
	}
	data := dir + "/data"
	if e := os.Mkdir(data, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, data+"/"+nativeStateMarker); e != nil {
		t.Fatal(e)
	}
	script := "set -eu\ndata=" + quoteShellArg(data) + "\nstat(){ printf '10000000 4096'; };\ntimeout(){ shift;shift;shift;eval \"$1\"; };\nfind(){ printf 'f 0 1\\n'; };\n" + nativeInspectionMetadataScript(true) + "printf '%s %s' \"$metadata_bytes\" \"$metadata_entries\""
	out, e := exec.Command("sh", "-c", script).CombinedOutput()
	if e != nil || string(out) != "null null" {
		t.Fatal("unsafe state presented as empty", string(out), e)
	}
}
