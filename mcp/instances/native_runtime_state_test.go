package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
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
	"sync"
	"testing"
	"time"
)

type nativeStateFake struct {
	*nativeFake
	data        map[string][]byte
	digest      string
	restores    int
	loseRestore bool
	failStatus  bool
}

func (f *nativeStateFake) StateSpace(context.Context, *Instance, nativeRecord, []nativeRecord) (nativeStateSpace, error) {
	return nativeStateSpace{Available: 64 << 30}, nil
}
func (f *nativeStateFake) Snapshot(_ context.Context, _ *Instance, _ nativeRecord, _ []nativeRecord, w io.Writer) error {
	tw := tar.NewWriter(w)
	for name, body := range f.data {
		if e := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body)), Typeflag: tar.TypeReg}); e != nil {
			return e
		}
		if _, e := tw.Write(body); e != nil {
			return e
		}
	}
	return tw.Close()
}
func (f *nativeStateFake) Restore(_ context.Context, _ *Instance, d nativeRecord, _ []nativeRecord, r io.Reader) error {
	tr := tar.NewReader(r)
	next := map[string][]byte{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if h.Typeflag == tar.TypeReg {
			next[h.Name], e = io.ReadAll(tr)
			if e != nil {
				return e
			}
		}
	}
	f.data = next
	f.digest = d.Restore.SHA256
	f.restores++
	if f.loseRestore {
		return errors.New("SSH response lost")
	}
	return nil
}
func (f *nativeStateFake) RestoreStatus(_ context.Context, _ *Instance, _ nativeRecord, _ []nativeRecord) (string, error) {
	if f.failStatus {
		return "", errors.New("SSH unavailable")
	}
	return f.digest, nil
}
func stateRequest(b *nativeBackend, action, id, key, sha string, body []byte) *httptest.ResponseRecorder {
	method := http.MethodGet
	if action == "restore" {
		method = http.MethodPost
	}
	req := httptest.NewRequest(method, nativePrefix+"/"+action+"?deployment_id="+id, bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("X-Apteva-State-Transfer-Policy", nativeJSON(b.statePolicy()))
	if sha != "" {
		req.Header.Set("X-Apteva-Snapshot-SHA256", sha)
	}
	w := httptest.NewRecorder()
	b.serveHTTP(w, req)
	return w
}
func archiveFixture(t *testing.T, headers []*tar.Header, bodies [][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for n, h := range headers {
		if e := tw.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if n < len(bodies) && bodies[n] != nil {
			if _, e := tw.Write(bodies[n]); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e := tw.Close(); e != nil {
		t.Fatal(e)
	}
	if e := gz.Close(); e != nil {
		t.Fatal(e)
	}
	return out.Bytes()
}
func TestNativeStateRoundTripLostAcknowledgementAndInspection(t *testing.T) {
	b, f, r := newNativeTest(t)
	prepareNativeTest(t, b, r)
	state := &nativeStateFake{nativeFake: f, data: map[string][]byte{"app.db": []byte("database"), "app.db-wal": []byte("uncheckpointed commit"), ".hidden": []byte("persistent")}}
	b.transport = state
	snap := stateRequest(b, "snapshot", r.DeploymentID, "snapshot-op", "", nil)
	if snap.Code != 200 {
		t.Fatalf("snapshot %d %s", snap.Code, snap.Body.String())
	}
	sha := snap.Header().Get("X-Apteva-Snapshot-SHA256")
	if sha != nativeHash(snap.Body.String()) {
		t.Fatal("wrong snapshot digest")
	}
	replay := stateRequest(b, "snapshot", r.DeploymentID, "snapshot-op", "", nil)
	if replay.Code != 200 || !bytes.Equal(replay.Body.Bytes(), snap.Body.Bytes()) {
		t.Fatal("snapshot retry changed bytes")
	}
	state.data = map[string][]byte{"old": []byte("replace me")}
	state.loseRestore = true
	restored := stateRequest(b, "restore", r.DeploymentID, "restore-op", sha, snap.Body.Bytes())
	if restored.Code != 503 {
		t.Fatalf("lost response should be unknown: %d %s", restored.Code, restored.Body.String())
	}
	// Reopen the backend state, as after an Instances restart. Inspect proves the
	// already committed receipt; it must not start or restore a second process.
	fresh := &nativeBackend{db: b.db, root: b.root, installID: b.installID, transport: state, tunnels: b.tunnels, ctx: b.ctx, locks: map[string]*sync.Mutex{}, jobs: map[string]context.CancelFunc{}}
	observed, w := nativeCall(t, fresh, "inspect", r)
	if w.Code != 200 || observed.SnapshotSHA256 != sha {
		t.Fatalf("inspection lost restore receipt: %d %s", w.Code, w.Body.String())
	}
	restored = stateRequest(fresh, "restore", r.DeploymentID, "restore-op", sha, snap.Body.Bytes())
	if restored.Code != 200 || restored.Header().Get("X-Apteva-Snapshot-SHA256") != sha || state.restores != 1 {
		t.Fatalf("restore retry was not idempotent: %d %s count %d", restored.Code, restored.Body.String(), state.restores)
	}
	if string(state.data["app.db-wal"]) != "uncheckpointed commit" || string(state.data[".hidden"]) != "persistent" || state.data["old"] != nil {
		t.Fatal("persistent data not restored exactly")
	}
	if w = stateRequest(fresh, "restore", r.DeploymentID, "restore-op", nativeHash("different"), snap.Body.Bytes()); w.Code != 409 {
		t.Fatal("conflicting retry accepted")
	}
	if f.starts != 0 {
		t.Fatal("state inspection started an app")
	}
}
func TestNativeStateRejectsWritersAndUnsafeArchives(t *testing.T) {
	b, f, r := newNativeTest(t)
	prepareNativeTest(t, b, r)
	state := &nativeStateFake{nativeFake: f, data: map[string][]byte{"original": []byte("safe")}}
	b.transport = state
	f.mu.Lock()
	f.states[r.DeploymentID] = nativeObservation{State: "running"}
	f.mu.Unlock()
	if w := stateRequest(b, "snapshot", r.DeploymentID, "snapshot-op", "", nil); w.Code != 409 {
		t.Fatal("running snapshot accepted")
	}
	f.mu.Lock()
	f.states[r.DeploymentID] = nativeObservation{State: "prepared", Prepared: true}
	f.mu.Unlock()
	for _, h := range []*tar.Header{{Name: "../escape", Typeflag: tar.TypeReg}, {Name: "/absolute", Typeflag: tar.TypeReg}, {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../outside"}, {Name: "hard", Typeflag: tar.TypeLink, Linkname: "outside"}, {Name: "fifo", Typeflag: tar.TypeFifo}, {Name: nativeStateMarker, Typeflag: tar.TypeReg}} {
		raw := archiveFixture(t, []*tar.Header{h}, nil)
		w := stateRequest(b, "restore", r.DeploymentID, "bad-restore", nativeHash(string(raw)), raw)
		if w.Code != 400 {
			t.Fatalf("unsafe %q accepted: %d %s", h.Name, w.Code, w.Body.String())
		}
	}
	good := archiveFixture(t, []*tar.Header{{Name: "data", Typeflag: tar.TypeReg, Size: 1}}, [][]byte{[]byte("x")})
	if w := stateRequest(b, "restore", r.DeploymentID, "checksum", nativeHash("bad"), good); w.Code != 400 {
		t.Fatal("checksum mismatch accepted")
	}
	if state.restores != 0 || string(state.data["original"]) != "safe" {
		t.Fatal("rejected archive changed persistent data")
	}
}
func TestNativeStatePreservesSQLiteWAL(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "db.sqlite")
	db, e := sql.Open("sqlite", name)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0", "CREATE TABLE records(value TEXT)", "INSERT INTO records VALUES('committed in WAL')"} {
		if _, e = db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	// Freeze the owner without checkpointing: committed WAL bytes remain separate.
	fdata := map[string][]byte{}
	for _, part := range []string{"db.sqlite", "db.sqlite-wal", "db.sqlite-shm"} {
		v, e := os.ReadFile(filepath.Join(root, part))
		if e != nil {
			t.Fatal(e)
		}
		fdata[part] = v
	}
	fake := &nativeStateFake{data: fdata}
	var raw bytes.Buffer
	if e = fake.Snapshot(context.Background(), nil, nativeRecord{}, nil, &raw); e != nil {
		t.Fatal(e)
	}
	var packed bytes.Buffer
	gz := gzip.NewWriter(&packed)
	if e = canonicalNativeTar(&raw, gz, false); e != nil {
		t.Fatal(e)
	}
	if e = gz.Close(); e != nil {
		t.Fatal(e)
	}
	snapshot := filepath.Join(t.TempDir(), "state.gz")
	os.WriteFile(snapshot, packed.Bytes(), 0600)
	dest := t.TempDir()
	var unpack bytes.Buffer
	if e = readNativeArchive(snapshot, &unpack); e != nil {
		t.Fatal(e)
	}
	tr := tar.NewReader(&unpack)
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		v, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dest, h.Name), v, 0600); e != nil {
			t.Fatal(e)
		}
	}
	restored, e := sql.Open("sqlite", filepath.Join(dest, "db.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	var value string
	if e = restored.QueryRow("SELECT value FROM records").Scan(&value); e != nil || value != "committed in WAL" {
		t.Fatalf("WAL commit lost: %q %v", value, e)
	}
}

func TestNativeRestoreJournalRecoversEveryRenameBoundary(t *testing.T) {
	for _, boundary := range []string{"partial-upload", "stage-ready", "old-renamed", "new-renamed"} {
		t.Run(boundary, func(t *testing.T) {
			parent := t.TempDir()
			data := filepath.Join(parent, "data")
			stage := filepath.Join(parent, ".restore-stage")
			backup := filepath.Join(parent, ".restore-previous")
			d := nativeRecord{Fingerprint: nativeHash("owner"), DataDir: data, Restore: &nativeRestore{SHA256: nativeHash("archive"), Key: nativeHash("request")}}
			expected := d.Fingerprint + ":" + d.Restore.Key + ":" + d.Restore.SHA256
			os.Mkdir(data, 0700)
			os.WriteFile(filepath.Join(data, "old"), []byte("retain"), 0600)
			os.Mkdir(stage, 0700)
			os.WriteFile(filepath.Join(stage, "new"), []byte("restored"), 0600)
			os.WriteFile(filepath.Join(parent, ".restore-intent"), []byte(expected), 0600)
			if boundary != "partial-upload" {
				os.WriteFile(filepath.Join(stage, nativeStateMarker), []byte(expected), 0600)
			}
			if boundary == "old-renamed" || boundary == "new-renamed" {
				if e := os.Rename(data, backup); e != nil {
					t.Fatal(e)
				}
			}
			if boundary == "new-renamed" {
				if e := os.Rename(stage, data); e != nil {
					t.Fatal(e)
				}
			}
			cmd := exec.Command("sh", "-c", "data="+quoteShellArg(data)+"\n"+nativeRestoreRecover(d, nil))
			out, e := cmd.CombinedOutput()
			if e != nil {
				t.Fatalf("recover %s: %s %v", boundary, out, e)
			}
			if boundary == "partial-upload" {
				if len(out) != 0 {
					t.Fatal("partial extraction falsely committed")
				}
				if _, e = os.Stat(filepath.Join(data, "old")); e != nil {
					t.Fatal("partial upload changed original")
				}
				return
			}
			if string(out) != d.Restore.SHA256 {
				t.Fatalf("receipt missing: %s", out)
			}
			if _, e = os.Stat(filepath.Join(data, "new")); e != nil {
				t.Fatal("new data absent")
			}
			if _, e = os.Stat(filepath.Join(data, "old")); !os.IsNotExist(e) {
				t.Fatal("old data not replaced")
			}
			if _, e = os.Stat(backup); !os.IsNotExist(e) {
				t.Fatal("backup cleanup incomplete")
			}
			// Repeated recovery reads the same receipt with no additional replacement.
			out, e = exec.Command("sh", "-c", "data="+quoteShellArg(data)+"\n"+nativeRestoreRecover(d, nil)).CombinedOutput()
			if e != nil || string(out) != d.Restore.SHA256 {
				t.Fatal("receipt retry failed")
			}
		})
	}
}

func TestNativeTargetListingAndPreflight(t *testing.T) {
	b, _, r := newNativeTest(t)
	for _, in := range []CreateInstanceInput{{Name: "linux-second", Provider: "external", Status: "ready", Platform: "linux", SSHUser: "root"}, {Name: "mac", Provider: "external", Status: "ready", Platform: "macos"}, {Name: "pending", Provider: "external", Status: "provisioning", Platform: "linux"}} {
		if _, e := dbCreateInstance(b.db, in); e != nil {
			t.Fatal(e)
		}
	}
	get := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		b.serveHTTP(w, httptest.NewRequest("GET", nativePrefix+"/targets"+query, nil))
		return w
	}
	w := get("?limit=1")
	var page nativeTargetPage
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Targets) != 1 || page.NextCursor == "" {
		t.Fatalf("bad first page: %d %s", w.Code, w.Body.String())
	}
	if page.Targets[0].Ref.InstallID != b.installID || page.Targets[0].Ref.ID == "0" {
		t.Fatal("wrong target namespace")
	}
	w = get("?limit=1&cursor=" + page.NextCursor)
	page = nativeTargetPage{}
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Targets) != 1 || page.Targets[0].Name != "linux-second" || page.NextCursor != "" {
		t.Fatalf("bad second page %s", w.Body.String())
	}
	w = get("?q=second")
	if !strings.Contains(w.Body.String(), "linux-second") || strings.Contains(w.Body.String(), "ssh_") {
		t.Fatal("unsafe catalog response")
	}
	w = get("?q=other&cursor=garbage")
	if w.Code != 400 {
		t.Fatal("invalid cursor accepted")
	}
	b.projectID = "owned"
	if w = get("?project_id=other"); w.Code != 403 {
		t.Fatal("cross-project catalog accepted")
	}
	b.projectID = ""
	r.InstallID = 0
	r.Generation = 0
	r.DeploymentID = ""
	r.Manifest = sdk.Manifest{}
	delete(r.Options, "artifact_id")
	before, _ := os.ReadDir(b.root)
	req := httptest.NewRequest("POST", nativePrefix+"/inspect-target", strings.NewReader(nativeJSON(r)))
	w = httptest.NewRecorder()
	b.serveHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("preflight without execution: %d %s", w.Code, w.Body.String())
	}
	var count int
	if e := b.db.QueryRow("SELECT count(*) FROM native_deployments").Scan(&count); e != nil || count != 0 {
		t.Fatal("inspection created deployment")
	}
	after, _ := os.ReadDir(b.root)
	if len(before) != len(after) {
		t.Fatal("inspection created directories")
	}
	r.Contract = "wrong"
	_, w = nativeCall(t, b, "inspect-target", r)
	if w.Code != 400 {
		t.Fatal("invalid contract accepted")
	}
}

func TestNativeStateLimitsAndCorruption(t *testing.T) {
	var output bytes.Buffer
	limit := nativeLimitWriter{Writer: &output, Remaining: 3}
	if _, e := limit.Write([]byte("four")); e == nil || output.Len() != 0 {
		t.Fatal("oversized output accepted")
	}
	// Oversized declared entries are rejected before allocating or reading bodies.
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if e := tw.WriteHeader(&tar.Header{Name: "huge", Typeflag: tar.TypeReg, Size: nativeDefaultStateLimit + 1}); e != nil {
		t.Fatal(e)
	}
	if e := canonicalNativeTar(bytes.NewReader(raw.Bytes()), io.Discard, false); e == nil {
		t.Fatal("oversized expanded file accepted")
	}
	malformed := archiveFixture(t, []*tar.Header{{Name: "file", Typeflag: tar.TypeReg, Size: 1}}, [][]byte{[]byte("x")})
	malformed[len(malformed)-5] ^= 0xff
	name := filepath.Join(t.TempDir(), "bad.gz")
	os.WriteFile(name, malformed, 0600)
	if e := readNativeArchive(name, io.Discard); e == nil {
		t.Fatal("invalid gzip CRC accepted")
	}
	collision := archiveFixture(t, []*tar.Header{{Name: "dir/file", Typeflag: tar.TypeReg}, {Name: "dir", Typeflag: tar.TypeReg}}, nil)
	os.WriteFile(name, collision, 0600)
	if e := readNativeArchive(name, io.Discard); e == nil {
		t.Fatal("directory/file collision accepted")
	}
	b, _, r := newNativeTest(t)
	req := httptest.NewRequest("POST", nativePrefix+"/restore?deployment_id="+r.DeploymentID, strings.NewReader("tiny"))
	req.ContentLength = nativeDefaultStateLimit + 1
	w := httptest.NewRecorder()
	b.serveHTTP(w, req)
	if w.Code != 413 {
		t.Fatalf("oversized transfer not rejected immediately: %d", w.Code)
	}
}

func TestNativeRestoreUnknownBlocksStart(t *testing.T) {
	b, f, r := newNativeTest(t)
	prepareNativeTest(t, b, r)
	state := &nativeStateFake{nativeFake: f, failStatus: true}
	b.transport = state
	d, e := b.record(r.DeploymentID)
	if e != nil {
		t.Fatal(e)
	}
	d.Restore = &nativeRestore{SHA256: nativeHash("archive"), Key: nativeHash("key")}
	if e = b.save(d); e != nil {
		t.Fatal(e)
	}
	observed, w := nativeCall(t, b, "inspect", r)
	if w.Code != 200 || observed.State != "unknown" || observed.SnapshotSHA256 != "" {
		t.Fatalf("uncertain restore reported ready: %d %s", w.Code, w.Body.String())
	}
	_, w = nativeCall(t, b, "start", r)
	if w.Code != 409 || f.starts != 0 {
		t.Fatal("start allowed without verified restore outcome")
	}
}

// Optional integration uses an existing pinned test host and only an owned
// /tmp tree. No services are installed, stopped, started or reconfigured.
func TestNativeStateSSHIntegration(t *testing.T) {
	file := os.Getenv("INSTANCES_NATIVE_STATE_TEST_DB")
	if file == "" {
		t.Skip("opt-in temporary-tree SSH state protocol test")
	}
	db, e := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	id, e := strconv.ParseInt(os.Getenv("INSTANCES_NATIVE_STATE_TEST_ID"), 10, 64)
	if e != nil {
		t.Fatal(e)
	}
	inst, e := dbGetInstance(db, id)
	if e != nil || inst.IsLocal() || inst.SSHHostKey == "" {
		t.Fatal("pinned remote test host required", e)
	}
	c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, e := nativeRun(c, inst, `cat /etc/machine-id; if test -f /etc/ssh/ssh_host_ed25519_key.pub; then cat /etc/ssh/ssh_host_ed25519_key.pub; else cat /etc/ssh/ssh_host_rsa_key.pub; fi`)
	if e != nil {
		t.Fatal(e)
	}
	facts := strings.SplitN(strings.TrimSpace(out), "\n", 2)
	if len(facts) != 2 {
		t.Fatal("invalid host identity")
	}
	token := nativeHash(strconv.FormatInt(time.Now().UnixNano(), 10))[:16]
	base := "/tmp/apteva-native-state-test-" + token
	d := nativeRecord{ID: "state-test-" + token, Fingerprint: nativeHash("state-owner-" + token), ReleaseDir: base + "/release", DataDir: base + "/data", Unit: "apteva-state-test-" + token + ".service", Execution: nativeExecution{HostKey: "linux-systemd:" + nativeHash(facts[0]+"\n"+facts[1])}}
	defer func() { _, _ = nativeRun(context.Background(), inst, "rm -rf -- "+quoteShellArg(base)) }()
	_, e = nativeRun(c, inst, "umask 077; mkdir -p "+quoteShellArg(d.ReleaseDir)+" "+quoteShellArg(d.DataDir)+"; printf '%s' "+quoteShellArg(d.Fingerprint)+" > "+quoteShellArg(d.ReleaseDir+"/.owner")+"; printf old > "+quoteShellArg(d.DataDir+"/old"))
	if e != nil {
		t.Fatal(e)
	}
	archive := archiveFixture(t, []*tar.Header{{Name: "app.db", Typeflag: tar.TypeReg, Size: 3}, {Name: "app.db-wal", Typeflag: tar.TypeReg, Size: 3}}, [][]byte{[]byte("db!"), []byte("wal")})
	name := filepath.Join(t.TempDir(), "state.gz")
	os.WriteFile(name, archive, 0600)
	var input bytes.Buffer
	if e = readNativeArchive(name, &input); e != nil {
		t.Fatal(e)
	}
	d.Restore = &nativeRestore{SHA256: nativeHash(string(archive)), Key: nativeHash("state-test-request"), Bytes: int64(input.Len())}
	transport := nativeSSH{}
	peers := []nativeRecord{d}
	if e = transport.Restore(c, inst, d, peers, &input); e != nil {
		t.Fatal(e)
	}
	if sha, e := transport.RestoreStatus(c, inst, d, peers); e != nil || sha != d.Restore.SHA256 {
		t.Fatal("remote receipt mismatch", sha, e)
	}
	var snapshot bytes.Buffer
	if e = transport.Snapshot(c, inst, d, peers, &snapshot); e != nil {
		t.Fatal(e)
	}
	var canonical bytes.Buffer
	if e = canonicalNativeTar(&snapshot, &canonical, true); e != nil {
		t.Fatal(e)
	}
	tr := tar.NewReader(&canonical)
	seen := map[string]string{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		v, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		seen[h.Name] = string(v)
	}
	if seen["app.db"] != "db!" || seen["app.db-wal"] != "wal" || len(seen) != 2 {
		t.Fatalf("remote roundtrip changed data: %v", seen)
	}
	if _, e = nativeRun(c, inst, "ln -s /etc/hosts "+quoteShellArg(d.DataDir+"/bad-link")); e != nil {
		t.Fatal(e)
	}
	if e = transport.Snapshot(c, inst, d, peers, io.Discard); e == nil {
		t.Fatal("remote symlink accepted")
	}
}

func TestNativeStateCrossProcessExclusion(t *testing.T) {
	b, _, r := newNativeTest(t)
	release, e := nativeStateFileLock(b.root)
	if e != nil {
		t.Fatal(e)
	}
	_, w := nativeCall(t, b, "prepare", r)
	if w.Code != 409 {
		t.Fatal("second backend process bypassed state lock")
	}
	release()
	prepareNativeTest(t, b, r)
}

func TestNativeSnapshotCapabilitiesAndUnsupportedMethods(t *testing.T) {
	caps := nativeCapabilities().(map[string]any)
	if caps["target_listing"] != true || caps["snapshots"] != nativeSnapshotsSupported {
		t.Fatal("capabilities do not match tested implementation")
	}
	b, _, r := newNativeTest(t)
	for _, action := range []string{"snapshot", "restore", "targets"} {
		w := httptest.NewRecorder()
		b.serveHTTP(w, httptest.NewRequest("DELETE", nativePrefix+"/"+action+"?deployment_id="+r.DeploymentID, nil))
		if w.Code != 405 {
			t.Fatal(action, "accepted an unsupported method")
		}
	}
	if w := stateRequest(b, "snapshot", "unknown", "key", "", nil); w.Code != 404 {
		t.Fatal("unknown deployment accepted")
	}
}
