package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const nativeStateLimit int64 = 512 << 20
const nativeStateMarker = ".apteva-native-restore"

type nativeRestore struct {
	Completed bool   `json:"completed,omitempty"`
	SHA256    string `json:"sha256"`
	Key       string `json:"key"`
	Bytes     int64  `json:"bytes,omitempty"`
}
type nativeSnapshotReceipt struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
}
type nativeStateTransport interface {
	Snapshot(context.Context, *Instance, nativeRecord, []nativeRecord, io.Writer) error
	Restore(context.Context, *Instance, nativeRecord, []nativeRecord, io.Reader) error
	RestoreStatus(context.Context, *Instance, nativeRecord, []nativeRecord) (string, error)
}

// This lock also covers normal lifecycle mutations and background preparation.
// Remote data-directory locks provide the same exclusion across SSH sessions.
func (b *nativeBackend) statePeers(d nativeRecord) ([]nativeRecord, error) {
	rows, e := b.db.Query(`SELECT record FROM native_deployments WHERE json_extract(record,'$.data_dir')=? AND json_extract(record,'$.execution.host_key')=?`, d.DataDir, d.Execution.HostKey)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []nativeRecord{}
	for rows.Next() {
		var raw string
		var peer nativeRecord
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &peer); e != nil {
			return nil, e
		}
		out = append(out, peer)
	}
	return out, rows.Err()
}

func (b *nativeBackend) reconcileRestore(c context.Context, i *Instance, d *nativeRecord) error {
	if !b.restoreNeedsVerification(i, *d) {
		return nil
	}
	t, ok := b.transport.(nativeStateTransport)
	if !ok {
		return errors.New("restore transport unavailable")
	}
	peers, e := b.statePeers(*d)
	if e != nil {
		return e
	}
	sha, e := t.RestoreStatus(c, i, *d, peers)
	if e != nil {
		b.invalidateRestore(*d)
		return e
	}
	return b.confirmRestore(i, d, sha)
}

func (b *nativeBackend) restoreVerificationKey(i *Instance, d nativeRecord) string {
	if d.Restore == nil {
		return ""
	}
	return nativeHash(nativeJSON([]any{d.Fingerprint, d.Execution.HostKey, d.Restore.Key, d.Restore.SHA256, nativeInspectionIdentity(i)}))
}
func (b *nativeBackend) restoreNeedsVerification(i *Instance, d nativeRecord) bool {
	if d.Restore == nil {
		return false
	}
	return d.Execution.State == "unknown" || d.Execution.State == "failed" || !d.Restore.Completed || d.Execution.SnapshotSHA256 != d.Restore.SHA256 || b.restoreVerified[d.ID] != b.restoreVerificationKey(i, d)
}
func (b *nativeBackend) invalidateRestore(d nativeRecord) { delete(b.restoreVerified, d.ID) }
func (b *nativeBackend) confirmRestore(i *Instance, d *nativeRecord, sha string) error {
	if d.Restore == nil {
		return nil
	}
	if sha == "" {
		b.invalidateRestore(*d)
		if d.Restore.Completed || d.Execution.SnapshotSHA256 == d.Restore.SHA256 {
			return errors.New("completed restore receipt missing")
		}
		return nil
	}
	if sha != d.Restore.SHA256 {
		return errors.New("restore digest differs from durable intent")
	}
	if !d.Restore.Completed || d.Execution.SnapshotSHA256 != sha {
		before := nativeJSON(*d)
		d.Restore.Completed = true
		d.Execution.SnapshotSHA256 = sha
		if e := b.saveCAS(before, *d); e != nil {
			return e
		}
	}
	if b.restoreVerified == nil || len(b.restoreVerified) >= 1024 {
		b.restoreVerified = map[string]string{}
	}
	b.restoreVerified[d.ID] = b.restoreVerificationKey(i, *d)
	return nil
}

// The SSH implementation folds receipt recovery and process inspection into one
// operation. Test/alternate transports can retain the separate primitive API.
type nativeCombinedInspector interface {
	InspectState(context.Context, *Instance, nativeRecord, []nativeRecord, bool) (nativeObservation, error)
}

func (b *nativeBackend) inspectRemote(c context.Context, i *Instance, d *nativeRecord) (nativeObservation, error) {
	if transport, ok := b.transport.(nativeCombinedInspector); ok {
		verify := b.restoreNeedsVerification(i, *d)
		var peers []nativeRecord
		var e error
		if verify {
			peers, e = b.statePeers(*d)
			if e != nil {
				return nativeObservation{}, e
			}
		}
		obs, e := transport.InspectState(c, i, *d, peers, verify)
		if obs.RestorePending {
			b.invalidateRestore(*d)
			return obs, errors.New("restore still pending")
		}
		if e != nil {
			return obs, e
		}
		if verify {
			e = b.confirmRestore(i, d, obs.SnapshotSHA256)
		}
		return obs, e
	}
	if e := b.reconcileRestore(c, i, d); e != nil {
		return nativeObservation{}, e
	}
	return b.transport.Inspect(c, i, *d)
}

func (b *nativeBackend) stateHTTP(w http.ResponseWriter, r *http.Request, action string) {
	if !nativeSnapshotsSupported {
		http.Error(w, "state transfer requires a Linux or macOS controller", 501)
		return
	}
	if r.ContentLength > nativeStateLimit {
		http.Error(w, "snapshot exceeds transfer limit", 413)
		return
	}
	expected := http.MethodGet
	if action == "restore" {
		expected = http.MethodPost
	}
	if r.Method != expected {
		w.Header().Set("Allow", expected)
		http.Error(w, expected+" required", 405)
		return
	}
	defer r.Body.Close()
	id := r.URL.Query().Get("deployment_id")
	key := r.Header.Get("Idempotency-Key")
	if !nativeID(id) || key == "" || len(key) > 512 || strings.ContainsAny(key, "\x00\r\n") {
		http.Error(w, "deployment_id and bounded Idempotency-Key required", 400)
		return
	}
	lock := b.lock(id)
	if !lock.TryLock() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "deployment busy", 409)
		return
	}
	defer lock.Unlock()
	if !b.stateMu.TryLock() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "backend state operation busy", 409)
		return
	}
	defer b.stateMu.Unlock()
	unlock, e := nativeStateFileLock(b.root)
	if e != nil {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "state operation busy in another backend process", 409)
		return
	}
	defer unlock()
	d, e := b.record(id)
	if e != nil || d.Execution.State == "deleted" {
		http.Error(w, "deployment unavailable", 404)
		return
	}
	i, e := dbGetInstance(b.db, d.InstanceID)
	if e != nil || i.IsLocal() {
		http.Error(w, "owned target unavailable", 409)
		return
	}
	transport, ok := b.transport.(nativeStateTransport)
	if !ok {
		http.Error(w, "state transport unavailable", 503)
		return
	}
	c, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	controller := http.NewResponseController(w)
	deadline, _ := c.Deadline()
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
	stopRead := context.AfterFunc(c, func() { _ = controller.SetReadDeadline(time.Now()) })
	defer stopRead()
	stop := context.AfterFunc(b.ctx, cancel)
	defer stop()
	if e = b.reconcileRestore(c, i, &d); e != nil {
		http.Error(w, "restore recovery requires inspection/retry", 503)
		return
	}
	if action == "snapshot" && d.Restore != nil && d.Execution.SnapshotSHA256 != d.Restore.SHA256 {
		http.Error(w, "pending restore must finish first", 409)
		return
	}
	if action == "restore" {
		sha := r.Header.Get("X-Apteva-Snapshot-SHA256")
		if !nativeDigest(sha) {
			http.Error(w, "valid snapshot SHA-256 required", 400)
			return
		}
		if d.Restore != nil && d.Restore.Key == nativeHash(key) && d.Restore.SHA256 != sha {
			http.Error(w, "idempotency key conflicts with snapshot", 409)
			return
		}
		if d.Execution.SnapshotSHA256 == sha {
			w.Header().Set("X-Apteva-Snapshot-SHA256", sha)
			writeNative(w, map[string]any{"snapshot_sha256": sha})
			return
		}
		if d.Restore != nil && d.Execution.SnapshotSHA256 != d.Restore.SHA256 && d.Restore.SHA256 != sha {
			http.Error(w, "previous restore must be recovered first", 409)
			return
		}
	}
	obs, e := b.transport.Inspect(c, i, d)
	if e != nil || !(obs.Stopped || obs.Prepared && !d.Started) {
		http.Error(w, "writers are not confirmed stopped", 409)
		return
	}
	peers, e := b.statePeers(d)
	if e != nil {
		http.Error(w, "state ownership unavailable", 503)
		return
	}
	cache := filepath.Join(b.root, "state-transfer")
	if e = os.MkdirAll(cache, 0700); e != nil {
		http.Error(w, "state staging unavailable", 503)
		return
	}
	// At most one bounded local archive is retained. All state operations are
	// serialized, and the previous response has closed before reclaiming it.
	snapshot := filepath.Join(cache, "snapshot.tar.gz")
	receipt := filepath.Join(cache, "snapshot.json")
	if action == "snapshot" {
		meta := nativeSnapshotCache{}
		if data, err := os.ReadFile(receipt); err == nil && json.Unmarshal(data, &meta) == nil && meta.Deployment == id && meta.Key == nativeHash(key) {
			if sha, err := nativeStateFileHash(snapshot); err == nil && sha == meta.SHA256 {
				serveNativeSnapshot(w, r, snapshot, sha)
				return
			}
		}
	}
	entries, e := os.ReadDir(cache)
	if e != nil {
		http.Error(w, "state staging unavailable", 503)
		return
	}
	for _, entry := range entries {
		if e = os.Remove(filepath.Join(cache, entry.Name())); e != nil {
			http.Error(w, "state staging recovery failed", 503)
			return
		}
	}
	f, e := os.OpenFile(snapshot, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		http.Error(w, "state staging unavailable", 503)
		return
	}
	defer f.Close()
	valid := false
	defer func() {
		if !valid {
			os.Remove(snapshot)
		}
	}()
	if action == "snapshot" {
		pipeR, pipeW := io.Pipe()
		done := make(chan error, 1)
		go func() { err := transport.Snapshot(c, i, d, peers, pipeW); pipeW.CloseWithError(err); done <- err }()
		gz, _ := gzip.NewWriterLevel(&nativeLimitWriter{Writer: f, Remaining: nativeStateLimit}, gzip.BestSpeed)
		e = canonicalNativeTar(pipeR, gz, true)
		closeErr := gz.Close()
		if e != nil || closeErr != nil {
			cancel()
			pipeR.CloseWithError(errors.New("snapshot rejected"))
		} else {
			pipeR.Close()
		}
		remoteErr := <-done
		if e != nil || closeErr != nil || remoteErr != nil {
			http.Error(w, "snapshot failed ownership, quiescence or archive validation", 409)
			return
		}
	} else {
		n, err := io.Copy(f, io.LimitReader(r.Body, nativeStateLimit+1))
		if err != nil || n > nativeStateLimit {
			http.Error(w, "snapshot upload incomplete or oversized", 400)
			return
		}
	}
	if e = f.Sync(); e != nil {
		http.Error(w, "state staging sync failed", 503)
		return
	}
	sha, e := nativeStateFileHash(snapshot)
	if e != nil {
		http.Error(w, "state digest unavailable", 503)
		return
	}
	if action == "snapshot" {
		if d.Snapshot != nil && d.Snapshot.Key == nativeHash(key) && d.Snapshot.SHA256 != sha {
			http.Error(w, "snapshot changed since original request", 409)
			return
		}
		before := nativeJSON(d)
		d.Snapshot = &nativeSnapshotReceipt{nativeHash(key), sha}
		if e = b.saveCAS(before, d); e != nil {
			http.Error(w, "cannot persist snapshot receipt", 503)
			return
		}
		if e = writeNativeStateJSON(receipt, nativeSnapshotCache{id, nativeHash(key), sha}); e != nil {
			http.Error(w, "snapshot receipt sync failed", 503)
			return
		}
		valid = true
		serveNativeSnapshot(w, r, snapshot, sha)
		return
	}
	if sha != r.Header.Get("X-Apteva-Snapshot-SHA256") {
		http.Error(w, "snapshot checksum mismatch", 400)
		return
	}
	// Validate every entry and the gzip trailer before any remote mutation.
	var expanded nativeByteCounter
	if e = readNativeArchive(snapshot, &expanded); e != nil {
		http.Error(w, "unsafe or oversized snapshot archive", 400)
		return
	}
	before := nativeJSON(d)
	restoreKey := nativeHash(key)
	if d.Restore != nil && d.Restore.SHA256 == sha && d.Execution.SnapshotSHA256 != sha {
		restoreKey = d.Restore.Key
	}
	for _, peer := range peers {
		b.invalidateRestore(peer)
	}
	d.Restore = &nativeRestore{SHA256: sha, Key: restoreKey, Bytes: int64(expanded)}
	if e = b.saveCAS(before, d); e != nil {
		http.Error(w, "cannot persist restore intent", 503)
		return
	}
	pipeR, pipeW := io.Pipe()
	done := make(chan error, 1)
	go func() { err := readNativeArchive(snapshot, pipeW); pipeW.CloseWithError(err); done <- err }()
	e = transport.Restore(c, i, d, peers, pipeR)
	pipeR.CloseWithError(errors.New("restore transport closed"))
	encodeErr := <-done
	if e != nil || encodeErr != nil {
		http.Error(w, "restore outcome unknown; inspect/retry", 503)
		return
	}
	if e = b.reconcileRestore(c, i, &d); e != nil || d.Execution.SnapshotSHA256 != sha {
		http.Error(w, "restore digest unconfirmed; inspect/retry", 503)
		return
	}
	w.Header().Set("X-Apteva-Snapshot-SHA256", sha)
	writeNative(w, map[string]any{"snapshot_sha256": sha})
}

type nativeSnapshotCache struct{ Deployment, Key, SHA256 string }
type nativeLimitWriter struct {
	io.Writer
	Remaining int64
}

func (w *nativeLimitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.Remaining {
		return 0, errors.New("snapshot exceeds size limit")
	}
	n, e := w.Writer.Write(p)
	w.Remaining -= int64(n)
	return n, e
}
func nativeStateFileHash(name string) (string, error) {
	f, e := os.Open(name)
	if e != nil {
		return "", e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() > nativeStateLimit {
		return "", errors.New("invalid state archive")
	}
	h := sha256.New()
	_, e = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}
func writeNativeStateJSON(name string, v any) error {
	f, e := os.OpenFile(name+".new", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write([]byte(nativeJSON(v)))
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(name+".new", name); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(name))
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
func serveNativeSnapshot(w http.ResponseWriter, r *http.Request, name, sha string) {
	f, e := os.Open(name)
	if e != nil {
		http.Error(w, "snapshot unavailable", 503)
		return
	}
	defer f.Close()
	w.Header().Set("X-Apteva-Snapshot-SHA256", sha)
	w.Header().Set("Content-Type", "application/gzip")
	http.ServeContent(w, r, "state.tar.gz", time.Time{}, f)
}
func readNativeArchive(name string, out io.Writer) error {
	f, e := os.Open(name)
	if e != nil {
		return e
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		return e
	}
	defer gz.Close()
	return canonicalNativeTar(gz, out, false)
}

// Re-encode trusted types only, removing ownership, PAX metadata and unsafe
// permission bits. WAL, SHM, journals and ordinary hidden app files are retained.
func canonicalNativeTar(input io.Reader, out io.Writer, fromHost bool) error {
	limited := &io.LimitedReader{R: input, N: nativeStateLimit + 1}
	tr := tar.NewReader(limited)
	tw := tar.NewWriter(&nativeLimitWriter{Writer: out, Remaining: nativeStateLimit})
	seen := map[string]byte{}
	var total int64
	count := 0
	var names int64
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		count++
		if count > 100000 || h.Size < 0 || h.Size > nativeStateLimit-total {
			return errors.New("snapshot expansion exceeds limit")
		}
		name := strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/")
		if (h.Name == "." || h.Name == "./") && h.Typeflag == tar.TypeDir {
			continue
		}
		if name == nativeStateMarker && fromHost && h.Typeflag == tar.TypeReg {
			continue
		}
		if name == "" || name == "." || len(name) > 4096 || name == nativeStateMarker || strings.ContainsAny(name, "\\\x00") || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return errors.New("unsafe snapshot path")
		}
		names += int64(len(name))
		if names > 16<<20 {
			return errors.New("snapshot path metadata exceeds limit")
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if seen[parent] == tar.TypeReg {
				return errors.New("snapshot parent is a file")
			}
			seen[parent] = tar.TypeDir
		}
		if old, exists := seen[name]; exists {
			if old == tar.TypeDir && h.Typeflag == tar.TypeDir {
				continue
			}
			return errors.New("duplicate snapshot path")
		}
		kind := h.Typeflag
		if kind == tar.TypeRegA {
			kind = tar.TypeReg
		}
		if kind != tar.TypeReg && kind != tar.TypeDir {
			return errors.New("snapshot links and special files forbidden")
		}
		if kind == tar.TypeDir && h.Size != 0 {
			return errors.New("invalid snapshot directory")
		}
		seen[name] = kind
		if len(seen) > 100000 {
			return errors.New("snapshot entry metadata exceeds limit")
		}
		total += h.Size
		mode := int64(0600)
		if kind == tar.TypeDir {
			mode = 0700
		} else {
			mode |= h.Mode & 0100
		}
		if e = tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Typeflag: kind, Size: h.Size}); e != nil {
			return e
		}
		if kind == tar.TypeReg {
			if _, e = io.CopyN(tw, tr, h.Size); e != nil {
				return e
			}
		}
	}
	// Consume padding and verify the gzip CRC, rejecting hidden trailing archives.
	buf := make([]byte, 32768)
	for {
		n, e := limited.Read(buf)
		for _, b := range buf[:n] {
			if b != 0 {
				return errors.New("snapshot has trailing payload")
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if limited.N <= 0 {
			return errors.New("snapshot exceeds expanded size limit")
		}
	}
	if limited.N <= 0 {
		return errors.New("snapshot exceeds expanded size limit")
	}
	return tw.Close()
}

type nativeByteCounter int64

func (n *nativeByteCounter) Write(p []byte) (int, error) {
	*n += nativeByteCounter(len(p))
	return len(p), nil
}
