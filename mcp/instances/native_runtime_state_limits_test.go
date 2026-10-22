package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeSnapshotLimitConfiguration(t *testing.T) {
	for _, name := range []string{"APTEVA_NATIVE_SNAPSHOT_MAX_ARCHIVE_BYTES", "APTEVA_NATIVE_SNAPSHOT_MAX_STATE_BYTES", "APTEVA_NATIVE_SNAPSHOT_TIMEOUT_SECONDS"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	l, timeout, e := loadNativeSnapshotLimits()
	if e != nil || l.ArchiveBytes != 8<<30 || l.StateBytes != 8<<30 || timeout != 2*time.Hour {
		t.Fatal(l, timeout, e)
	}
	t.Setenv("APTEVA_NATIVE_SNAPSHOT_MAX_ARCHIVE_BYTES", "1073741824")
	t.Setenv("APTEVA_NATIVE_SNAPSHOT_MAX_STATE_BYTES", "8589934592")
	t.Setenv("APTEVA_NATIVE_SNAPSHOT_TIMEOUT_SECONDS", "3600")
	l, timeout, e = loadNativeSnapshotLimits()
	if e != nil || l.ArchiveBytes != 1<<30 || l.StateBytes != 8<<30 || timeout != time.Hour {
		t.Fatal(l, timeout, e)
	}
	caps := nativeCapabilitiesFor(l, timeout).(map[string]any)
	limits := caps["snapshot_limits"].(map[string]any)
	if limits["max_archive_bytes"] != l.ArchiveBytes || limits["max_expanded_bytes"] != l.StateBytes || limits["max_file_bytes"] != l.StateBytes || limits["hard_max_bytes"] != nativeStateHardLimit {
		t.Fatal(limits)
	}
	for _, raw := range []string{"", "-1", "0", "17179869185", "9223372036854775807", "NaN"} {
		t.Setenv("APTEVA_NATIVE_SNAPSHOT_MAX_STATE_BYTES", raw)
		if _, _, e = loadNativeSnapshotLimits(); e == nil {
			t.Fatalf("accepted limit %q", raw)
		}
	}
}

type nativeSpaceFake struct {
	*nativeStateFake
	available int64
	checks    int
}

func (f *nativeSpaceFake) StateSpace(context.Context, *Instance, nativeRecord, []nativeRecord) (nativeStateSpace, error) {
	f.checks++
	return nativeStateSpace{Available: f.available}, nil
}

type nativeReadCounter struct {
	io.Reader
	n int64
}

func (r *nativeReadCounter) Read(p []byte) (int, error) {
	n, e := r.Reader.Read(p)
	r.n += int64(n)
	return n, e
}
func nativeSmallStateBackend(t *testing.T) (*nativeBackend, *nativeSpaceFake, nativeRequest) {
	t.Helper()
	b, f, r := newNativeTest(t)
	prepareNativeTest(t, b, r)
	state := &nativeSpaceFake{nativeStateFake: &nativeStateFake{nativeFake: f, data: map[string][]byte{"old": []byte("safe")}}, available: 64 << 30}
	b.transport = state
	b.stateLimits = nativeSnapshotLimits{ArchiveBytes: 1 << 20, StateBytes: 1 << 20}
	b.stateDiskFree = func(string) (int64, error) { return 64 << 30, nil }
	return b, state, r
}
func nativeStateErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var v struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil || v.Error.Code == "" {
		t.Fatalf("missing structured error: %d %s", w.Code, w.Body.String())
	}
	return v.Error.Code
}
func TestNativeSnapshotUploadLimitsAndErrorCodes(t *testing.T) {
	data := archiveFixture(t, []*tar.Header{{Name: "db", Typeflag: tar.TypeReg, Size: 8}}, [][]byte{[]byte("database")})
	for _, kind := range []string{"chunked_oversize", "expanded_oversize", "truncated_upload", "checksum", "gzip_crc", "local_space", "remote_space"} {
		t.Run(kind, func(t *testing.T) {
			b, f, r := nativeSmallStateBackend(t)
			input := data
			want, status := "", 400
			switch kind {
			case "chunked_oversize":
				b.stateLimits.ArchiveBytes = 32
				input = bytes.Repeat([]byte("x"), 1024)
				want = "archive_too_large"
				status = 413
			case "expanded_oversize":
				b.stateLimits.StateBytes = 4
				want = "expanded_state_too_large"
				status = 413
			case "truncated_upload":
				want = "incomplete_transfer"
			case "checksum":
				want = "checksum_mismatch"
			case "gzip_crc":
				input = append([]byte(nil), data...)
				input[len(input)-8] ^= 1
				want = "checksum_mismatch"
			case "local_space":
				b.stateDiskFree = func(string) (int64, error) { return nativeStateReserve, nil }
				want = "insufficient_disk_space"
				status = 507
			case "remote_space":
				f.available = nativeStateReserve
				want = "insufficient_disk_space"
				status = 507
			}
			body := &nativeReadCounter{Reader: bytes.NewReader(input)}
			req := httptest.NewRequest("POST", nativePrefix+"/restore?deployment_id="+r.DeploymentID, body)
			req.ContentLength = int64(len(input))
			req.Header.Set("Idempotency-Key", "safe-operation")
			req.Header.Set("X-Apteva-Snapshot-SHA256", nativeHash(string(input)))
			if kind == "chunked_oversize" {
				req.ContentLength = -1
				req.TransferEncoding = []string{"chunked"}
			}
			if kind == "truncated_upload" {
				req.ContentLength += 10
			}
			if kind == "checksum" {
				req.Header.Set("X-Apteva-Snapshot-SHA256", nativeHash("wrong"))
			}
			w := httptest.NewRecorder()
			b.serveHTTP(w, req)
			if w.Code != status || nativeStateErrorCode(t, w) != want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if kind == "chunked_oversize" && body.n != 33 {
				t.Fatalf("read beyond bounded probe: %d", body.n)
			}
			if strings.HasSuffix(kind, "space") && body.n != 0 {
				t.Fatal("preflight consumed upload")
			}
			if f.restores != 0 || string(f.data["old"]) != "safe" {
				t.Fatal("failure changed target data")
			}
			d, e := b.record(r.DeploymentID)
			if e != nil || d.Restore != nil {
				t.Fatal("failure persisted restore intent", e)
			}
			if _, e = os.Stat(filepath.Join(b.root, "state-transfer", "snapshot.tar.gz")); e == nil {
				t.Fatal("failed archive retained")
			}
		})
	}
}
func TestNativeSnapshotIndependentBoundsAndCache(t *testing.T) {
	b, f, r := nativeSmallStateBackend(t)
	f.data = map[string][]byte{"db": bytes.Repeat([]byte("x"), 4096)}
	b.stateLimits.ArchiveBytes = 1024
	w := stateRequest(b, "snapshot", r.DeploymentID, "snapshot-key", "", nil)
	if w.Code != 200 || w.Body.Len() >= 4096 || w.Header().Get("X-Apteva-Snapshot-Expanded-Bytes") != "4096" {
		t.Fatalf("independent bounds rejected compression: %d %s", w.Code, w.Body.String())
	}
	b.stateLimits.StateBytes = 2048
	w = stateRequest(b, "snapshot", r.DeploymentID, "snapshot-key", "", nil)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "policy") {
		t.Fatalf("old cache bypassed new limit: %d %s", w.Code, w.Body.String())
	}
	b.stateLimits.StateBytes = 1 << 20
	b.stateLimits.ArchiveBytes = 10
	w = stateRequest(b, "snapshot", r.DeploymentID, "new-snapshot-key", "", nil)
	if w.Code != 413 || nativeStateErrorCode(t, w) != "archive_too_large" {
		t.Fatalf("snapshot output bypassed compressed bound: %d %s", w.Code, w.Body.String())
	}
}
func TestNativeSnapshotHeadroomAndHints(t *testing.T) {
	b, f, r := nativeSmallStateBackend(t)
	data := archiveFixture(t, []*tar.Header{{Name: "db", Typeflag: tar.TypeReg, Size: 8}}, [][]byte{[]byte("database")})
	f.available = nativeRestoreSpace(8, nativeStateMaxEntries)
	req := httptest.NewRequest("POST", nativePrefix+"/restore?deployment_id="+r.DeploymentID, bytes.NewReader(data))
	req.Header.Set("Idempotency-Key", "restore-hint")
	req.Header.Set("X-Apteva-Snapshot-SHA256", nativeHash(string(data)))
	req.Header.Set("X-Apteva-Snapshot-Expanded-Bytes", "8")
	w := httptest.NewRecorder()
	b.serveHTTP(w, req)
	if w.Code != 200 || f.restores != 1 || f.checks != 2 {
		t.Fatalf("hint/headroom failed: %d %s checks=%d", w.Code, w.Body.String(), f.checks)
	}
	// Space already occupied by retained old state must not be counted twice.
	if nativeRequireSpace(nativeRestoreSpace(8, 1), nativeRestoreSpace(8, 1)) != nil {
		t.Fatal("exact free headroom rejected")
	}
	b, f, r = nativeSmallStateBackend(t)
	req = httptest.NewRequest("POST", nativePrefix+"/restore?deployment_id="+r.DeploymentID, bytes.NewReader(data))
	req.Header.Set("Idempotency-Key", "restore-bad-hint")
	req.Header.Set("X-Apteva-Snapshot-SHA256", nativeHash(string(data)))
	req.Header.Set("X-Apteva-Snapshot-Expanded-Bytes", "0")
	w = httptest.NewRecorder()
	b.serveHTTP(w, req)
	if w.Code != 400 || f.restores != 0 || nativeStateErrorCode(t, w) != "invalid_archive" {
		t.Fatal("unverified size hint accepted")
	}
}

// The payload is generated and consumed as a stream; no database-sized byte slice.
type nativeZeroReader struct{}

func (nativeZeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type nativeLargeState struct {
	*nativeStateFake
	size int64
}

func (f *nativeLargeState) StateSpace(context.Context, *Instance, nativeRecord, []nativeRecord) (nativeStateSpace, error) {
	return nativeStateSpace{Bytes: f.size, Entries: 1, Available: 64 << 30}, nil
}
func (f *nativeLargeState) Snapshot(_ context.Context, _ *Instance, _ nativeRecord, _ []nativeRecord, w io.Writer) error {
	tw := tar.NewWriter(w)
	if e := tw.WriteHeader(&tar.Header{Name: "app.db", Typeflag: tar.TypeReg, Mode: 0600, Size: f.size}); e != nil {
		return e
	}
	if _, e := io.CopyN(tw, nativeZeroReader{}, f.size); e != nil {
		return e
	}
	return tw.Close()
}
func (f *nativeLargeState) Restore(_ context.Context, _ *Instance, d nativeRecord, _ []nativeRecord, r io.Reader) error {
	tr := tar.NewReader(r)
	h, e := tr.Next()
	if e != nil {
		return e
	}
	if h.Size != f.size || h.Name != "app.db" {
		return errors.New("wrong large state")
	}
	if n, e := io.Copy(io.Discard, tr); e != nil || n != f.size {
		return errors.New("incomplete large state")
	}
	if _, e = tr.Next(); e != io.EOF {
		return errors.New("unexpected extra entry")
	}
	f.digest = d.Restore.SHA256
	f.restores++
	return nil
}
func TestNativeSnapshotLargeStreamingRoundTrip(t *testing.T) {
	size := int64(513 << 20)
	if !testing.Short() {
		size = (4 << 30) + (128 << 20)
	}
	b, f, r := nativeSmallStateBackend(t)
	b.stateLimits = defaultNativeSnapshotLimits()
	state := &nativeLargeState{nativeStateFake: f.nativeStateFake, size: size}
	b.transport = state
	w := stateRequest(b, "snapshot", r.DeploymentID, "large-snapshot", "", nil)
	if w.Code != 200 {
		t.Fatalf("large snapshot: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Apteva-Snapshot-Expanded-Bytes") != strconv.FormatInt(size, 10) {
		t.Fatal("wrong expanded metadata")
	}
	sha := w.Header().Get("X-Apteva-Snapshot-SHA256")
	restored := stateRequest(b, "restore", r.DeploymentID, "large-restore", sha, w.Body.Bytes())
	if restored.Code != 200 || state.restores != 1 || restored.Header().Get("X-Apteva-Snapshot-SHA256") != sha {
		t.Fatalf("large restore: %d %s", restored.Code, restored.Body.String())
	}
	// No growing per-chunk progress log or retained restore archive.
	info, e := os.Stat(filepath.Join(b.root, "state-progress.json"))
	if e != nil || info.Size() > 4096 {
		t.Fatal("unbounded progress", e)
	}
	t.Logf("streamed %d expanded bytes; compressed archive %d bytes", size, w.Body.Len())
}
func TestNativeStateRemoteSafetyAndSpaceScripts(t *testing.T) {
	for _, tc := range []struct {
		output string
		code   int
	}{{"f 4380866642 1", 0}, {"f 8589934593 1", 73}, {"l 10 1", 74}, {"f 10 2", 74}} {
		script := "set -eu\ndata=" + quoteShellArg(t.TempDir()) + "\nfind() { printf '%s\\n' " + quoteShellArg(tc.output) + "; };\n" + nativeDataSafe()
		e := exec.Command("sh", "-c", script).Run()
		code := 0
		if e != nil {
			var exit *exec.ExitError
			if !errors.As(e, &exit) {
				t.Fatal(e)
			}
			code = exit.ExitCode()
		}
		if code != tc.code {
			t.Fatalf("%s: code %d want %d", tc.output, code, tc.code)
		}
	}
	script := "set -eu\ndata=" + quoteShellArg(t.TempDir()) + "\nfind() { printf 'f 10 1\\n'; return 1; };\n" + nativeDataSafe()
	if e := exec.Command("sh", "-c", script).Run(); e == nil {
		t.Fatal("find failure accepted")
	}
	for _, content := range []int64{0, 1} {
		script := "set -eu\nparent=/\nstat(){ printf '65536'; };\ndf(){ printf 'Filesystem 1024-blocks Used Available Capacity Mounted\\nx 1 0 " + strconv.FormatInt((nativeStateReserve+65536)/1024, 10) + " 0%% /\\n'; };\n" + nativeStateRestoreDiskCheck(content, 1)
		e := exec.Command("sh", "-c", script).Run()
		if (e == nil) != (content == 0) {
			t.Fatalf("filesystem allocation threshold %d: %v", content, e)
		}
	}

}
func TestNativeStateHashAndMetadataBounds(t *testing.T) {
	name := filepath.Join(t.TempDir(), "oversized.gz")
	f, e := os.Create(name)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Truncate(513 << 20); e != nil {
		t.Fatal(e)
	}
	f.Close()
	if _, e = nativeStateFileHashLimited(name, nativeSnapshotLimits{ArchiveBytes: 512 << 20, StateBytes: 8 << 30}, nil); !errors.Is(e, errNativeArchiveTooLarge) {
		t.Fatal("hash bypassed archive limit", e)
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "db", Typeflag: tar.TypeReg, Size: (8 << 30) + 1})
	tw.Close()
	gz.Close()
	os.WriteFile(name, archive.Bytes(), 0600)
	if e = readNativeArchive(name, io.Discard); !errors.Is(e, errNativeStateTooLarge) {
		t.Fatal("oversized file not rejected from header", e)
	}
}

type nativePausedBody struct {
	io.Reader
	entered, release chan struct{}
	paused           bool
}

func (r *nativePausedBody) Read(p []byte) (int, error) {
	if !r.paused {
		r.paused = true
		close(r.entered)
		<-r.release
	}
	return r.Reader.Read(p)
}
func TestNativeStateProgressDuringUploadAndRestart(t *testing.T) {
	b, _, r := nativeSmallStateBackend(t)
	b.stateEpoch = "original-controller"
	data := archiveFixture(t, []*tar.Header{{Name: "db", Typeflag: tar.TypeReg, Size: 8}}, [][]byte{[]byte("database")})
	body := &nativePausedBody{Reader: bytes.NewReader(data), entered: make(chan struct{}), release: make(chan struct{})}
	req := httptest.NewRequest(http.MethodPost, nativePrefix+"/restore?deployment_id="+r.DeploymentID, body)
	req.ContentLength = int64(len(data))
	req.Header.Set("Idempotency-Key", "secret-progress-key")
	req.Header.Set("X-Apteva-Snapshot-SHA256", nativeHash(string(data)))
	done := make(chan struct{})
	w := httptest.NewRecorder()
	go func() { defer close(done); b.serveHTTP(w, req) }()
	released := false
	defer func() {
		if !released {
			close(body.release)
			<-done
		}
	}()
	select {
	case <-body.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not begin")
	}
	readProgress := func(key string) *httptest.ResponseRecorder {
		t.Helper()
		get := httptest.NewRequest("GET", nativePrefix+"/state-progress?deployment_id="+r.DeploymentID, nil)
		get.Header.Set("Idempotency-Key", key)
		out := httptest.NewRecorder()
		b.serveHTTP(out, get)
		return out
	}
	snapshot := readProgress("secret-progress-key")
	var value nativeStateProgress
	if snapshot.Code != 200 || json.Unmarshal(snapshot.Body.Bytes(), &value) != nil || value.Phase != "upload" || value.Status != "running" {
		t.Fatalf("progress blocked or wrong: %d %s", snapshot.Code, snapshot.Body.String())
	}
	if strings.Contains(snapshot.Body.String(), "secret-progress-key") || strings.Contains(snapshot.Body.String(), nativeHash("secret-progress-key")) {
		t.Fatal("progress exposed idempotency key")
	}
	if other := readProgress("wrong-key"); other.Code != 404 {
		t.Fatal("another operation's progress exposed")
	}
	close(body.release)
	released = true
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not finish")
	}
	if w.Code != 200 {
		t.Fatalf("restore failed %d %s", w.Code, w.Body.String())
	}
	snapshot = readProgress("secret-progress-key")
	json.Unmarshal(snapshot.Body.Bytes(), &value)
	if value.Status != "completed" || value.RequiresInspection {
		t.Fatal(snapshot.Body.String())
	}
	// A restarted controller must not label an in-flight receipt as completed.
	stale := b.newStateProgress(r.DeploymentID, "restore", "interrupted")
	stale.phase("restore", 10)
	b.stateEpoch = "new-controller"
	snapshot = readProgress("interrupted")
	json.Unmarshal(snapshot.Body.Bytes(), &value)
	if value.Status != "unknown" || value.ErrorCode != "controller_restarted" || !value.RequiresInspection {
		t.Fatal(snapshot.Body.String())
	}
	files, e := filepath.Glob(filepath.Join(b.root, "state-progress*"))
	if e != nil || len(files) != 1 {
		t.Fatal("progress history grows", files, e)
	}
}

func TestNativeSnapshotInstallSettingsAndManifest(t *testing.T) {
	for _, name := range []string{"APTEVA_NATIVE_SNAPSHOT_MAX_ARCHIVE_BYTES", "APTEVA_NATIVE_SNAPSHOT_MAX_STATE_BYTES", "APTEVA_NATIVE_SNAPSHOT_TIMEOUT_SECONDS"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	config := sdk.Config{"native_snapshot_max_archive_bytes": "1073741824", "native_snapshot_max_state_bytes": "8589934592", "native_snapshot_timeout_seconds": "9000"}
	l, timeout, e := loadNativeSnapshotLimits(config)
	if e != nil || l.ArchiveBytes != 1<<30 || l.StateBytes != 8<<30 || timeout != 9000*time.Second {
		t.Fatal(l, timeout, e)
	}
	for _, body := range [][]byte{[]byte(manifestYAML), func() []byte {
		body, e := os.ReadFile("apteva.yaml")
		if e != nil {
			t.Fatal(e)
		}
		return body
	}()} {
		m, e := sdk.ParseManifest(body)
		if e != nil {
			t.Fatal(e)
		}
		found := map[string]string{}
		for _, field := range m.ConfigSchema {
			found[field.Name] = field.Default
		}
		if found["native_snapshot_max_archive_bytes"] != "8589934592" || found["native_snapshot_max_state_bytes"] != "8589934592" || found["native_snapshot_timeout_seconds"] != "7200" {
			t.Fatal("manifests omit limits", found)
		}
	}
	t.Setenv("APTEVA_NATIVE_SNAPSHOT_MAX_ARCHIVE_BYTES", "536870912")
	l, _, e = loadNativeSnapshotLimits(config)
	if e != nil || l.ArchiveBytes != 512<<20 {
		t.Fatal("environment override ignored", l, e)
	}
	t.Setenv("APTEVA_NATIVE_SNAPSHOT_TIMEOUT_SECONDS", "21601")
	if _, _, e = loadNativeSnapshotLimits(config); e == nil {
		t.Fatal("unbounded deadline accepted")
	}
}
func TestNativeSnapshotFilesystemAllocationAndInterruptedUpload(t *testing.T) {
	if nativeRestoreSpace(100, 10, 65536) != 100+10*65536+nativeStateReserve {
		t.Fatal("filesystem allocation not accounted for")
	}
	r := nativeUploadReader{brokenNativeUpload{}}
	if _, e := r.Read(make([]byte, 1)); !errors.Is(e, errNativeTransferIncomplete) {
		t.Fatal("body transport failure not distinguished", e)
	}
}

type brokenNativeUpload struct{}

func (brokenNativeUpload) Read([]byte) (int, error) { return 0, errors.New("connection lost") }
