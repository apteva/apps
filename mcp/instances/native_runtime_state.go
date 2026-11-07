package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const nativeStateMarker = ".apteva-native-restore"

type nativeRestore struct {
	Completed    bool               `json:"completed,omitempty"`
	SHA256       string             `json:"sha256"`
	Key          string             `json:"key"`
	Bytes        int64              `json:"bytes,omitempty"`
	Entries      int64              `json:"entries,omitempty"`
	Policy       *nativeStatePolicy `json:"policy,omitempty"`
	StateBytes   int64              `json:"state_bytes,omitempty"`
	ArchiveBytes int64              `json:"archive_bytes,omitempty"`
}
type nativeSnapshotReceipt struct {
	Policy *nativeStatePolicy `json:"policy,omitempty"`
	Key    string             `json:"key"`
	SHA256 string             `json:"sha256"`
}
type nativeStateTransport interface {
	StateSpace(context.Context, *Instance, nativeRecord, []nativeRecord) (nativeStateSpace, error)
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
	policy, e := b.stateHTTPPolicy(r)
	if e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	limits := policy.limits()
	if r.ContentLength > limits.ArchiveBytes {
		writeNativeStateFailure(w, errNativeArchiveTooLarge)
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
	c, cancel := context.WithTimeout(context.WithValue(r.Context(), nativeLimitsKey{}, limits), time.Duration(policy.TimeoutSeconds)*time.Second)
	defer cancel()
	controller := http.NewResponseController(w)
	deadline, _ := c.Deadline()
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
	readStopped := make(chan struct{})
	stopRead := context.AfterFunc(c, func() {
		defer close(readStopped)
		_ = controller.SetReadDeadline(time.Now())
		if c.Err() != context.DeadlineExceeded {
			_ = controller.SetWriteDeadline(time.Now())
		}
	})
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
	if d.Snapshot != nil && action == "snapshot" && d.Snapshot.Key == nativeHash(key) && d.Snapshot.Policy != nil && *d.Snapshot.Policy != policy {
		http.Error(w, "idempotency key conflicts with transfer policy", 409)
		return
	}
	if action == "restore" {
		if d.Restore != nil && d.Restore.Key == nativeHash(key) && d.Restore.Policy != nil && *d.Restore.Policy != policy {
			http.Error(w, "idempotency key conflicts with transfer policy", 409)
			return
		}
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
			if d.Restore != nil && (d.Restore.StateBytes > limits.StateBytes || d.Restore.ArchiveBytes > limits.ArchiveBytes || d.Restore.Entries > nativeEntryLimit(limits)) {
				writeNativeStateFailure(w, errNativeStateTooLarge)
				return
			}
			progress := b.newStateProgress(id, action, key)
			progress.phase("verify_restore", 0)
			progress.finish()
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
	progress := b.newStateProgress(id, action, key)
	progress.ctx = c
	progress.phase("preflight", 0)
	success := false
	defer func() {
		if !success && progress.failure == nil {
			progress.fail(errors.New("state transfer failed"))
		}
		progress.finish()
	}()
	uncertain := d.Restore != nil && d.Execution.SnapshotSHA256 != d.Restore.SHA256
	fail := func(e error) {
		progress.fail(e)
		// Leave a bounded window to report an upload timeout after its read deadline.
		if c.Err() == context.DeadlineExceeded || nativeStateFailureFor(e).Code == "incomplete_transfer" {
			if !stopRead() {
				<-readStopped
			}
			_ = controller.SetWriteDeadline(time.Now().Add(2 * time.Second))
		}
		writeNativeStateFailure(w, e, uncertain)
	}
	space, e := transport.StateSpace(c, i, d, peers)
	if e != nil {
		fail(e)
		return
	}
	// A supplied expanded-size hint is checked against actual content later. When
	// absent, reserve the configured worst case before receiving an unknown body.
	expectedState := limits.StateBytes
	hasExpectedState := false
	if action == "restore" {
		if raw := r.Header.Get("X-Apteva-Snapshot-Expanded-Bytes"); raw != "" {
			expectedState, e = strconv.ParseInt(raw, 10, 64)
			if e != nil || expectedState < 0 {
				fail(errNativeArchiveInvalid)
				return
			}
			if expectedState > limits.StateBytes {
				fail(errNativeStateTooLarge)
				return
			}
			hasExpectedState = true
		}
		if e = nativeRequireSpace(space.Available, nativeRestoreSpace(expectedState, nativeEntryLimit(limits), space.AllocationUnit)); e != nil {
			fail(e)
			return
		}
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
			progress.phase("verify_cache", 0)
			if sha, err := nativeStateFileHashLimited(snapshot, limits, progress); err == nil && sha == meta.SHA256 {
				var stats nativeArchiveStats
				if err = readNativeArchiveContext(c, snapshot, nativeProgressWriter{io.Discard, progress}, limits, &stats); err != nil {
					fail(err)
					return
				}
				w.Header().Set("X-Apteva-Snapshot-Expanded-Bytes", strconv.FormatInt(stats.Bytes, 10))
				progress.phase("download", meta.ArchiveBytes)
				if e = serveNativeSnapshot(w, r, snapshot, sha, progress); e != nil {
					progress.fail(errNativeTransferIncomplete)
					return
				}
				success = true
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
	archiveBudget := limits.ArchiveBytes
	if action == "restore" && r.ContentLength >= 0 {
		archiveBudget = r.ContentLength
	}
	if action == "snapshot" && space.Bytes+nativeStateTarOverhead+space.Bytes/100+(8<<20) < archiveBudget {
		archiveBudget = space.Bytes + nativeStateTarOverhead + space.Bytes/100 + (8 << 20)
	}
	if e = b.localStateSpace(cache, archiveBudget+nativeStateReserve); e != nil {
		fail(e)
		return
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
	var stats nativeArchiveStats
	if action == "snapshot" {
		progress.phase("create_snapshot", space.Bytes)
		pipeR, pipeW := io.Pipe()
		done := make(chan error, 1)
		go func() { err := transport.Snapshot(c, i, d, peers, pipeW); pipeW.CloseWithError(err); done <- err }()
		gz, _ := gzip.NewWriterLevel(&nativeLimitWriter{Writer: f, Remaining: limits.ArchiveBytes, LimitError: errNativeArchiveTooLarge}, gzip.BestSpeed)
		e = canonicalNativeTarLimited(pipeR, nativeProgressWriter{gz, progress}, true, limits, &stats)
		closeErr := gz.Close()
		if e != nil || closeErr != nil {
			cancel()
			pipeR.CloseWithError(errors.New("snapshot rejected"))
		} else {
			pipeR.Close()
		}
		remoteErr := <-done
		if e != nil || closeErr != nil || remoteErr != nil {
			for _, err := range []error{e, closeErr, remoteErr} {
				if err != nil {
					e = err
					if nativeStateFailureFor(err).Code != "state_transfer_failed" {
						break
					}
				}
			}
			fail(e)
			return
		}
	} else {
		progress.phase("upload", r.ContentLength)
		n, err := io.Copy(f, io.LimitReader(nativeProgressReader{nativeUploadReader{r.Body}, progress}, limits.ArchiveBytes+1))
		if n > limits.ArchiveBytes {
			fail(errNativeArchiveTooLarge)
			return
		}
		if err != nil {
			if errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrUnexpectedEOF) || c.Err() != nil {
				err = errNativeTransferIncomplete
			}
			fail(err)
			return
		}
		if r.ContentLength >= 0 && n != r.ContentLength {
			fail(errNativeTransferIncomplete)
			return
		}
	}
	if e = f.Sync(); e != nil {
		fail(e)
		return
	}
	progress.phase("verify_checksum", 0)
	sha, e := nativeStateFileHashLimited(snapshot, limits, progress)
	if e != nil {
		fail(e)
		return
	}
	if action == "snapshot" {
		if d.Snapshot != nil && d.Snapshot.Key == nativeHash(key) && d.Snapshot.SHA256 != sha {
			http.Error(w, "snapshot changed since original request", 409)
			return
		}
		before := nativeJSON(d)
		d.Snapshot = &nativeSnapshotReceipt{Key: nativeHash(key), SHA256: sha, Policy: &policy}
		if e = b.saveCAS(before, d); e != nil {
			http.Error(w, "cannot persist snapshot receipt", 503)
			return
		}
		if e = writeNativeStateJSON(receipt, nativeSnapshotCache{Deployment: id, Key: nativeHash(key), SHA256: sha, ArchiveBytes: progress.value.Bytes}); e != nil {
			http.Error(w, "snapshot receipt sync failed", 503)
			return
		}
		valid = true
		w.Header().Set("X-Apteva-Snapshot-Expanded-Bytes", strconv.FormatInt(stats.Bytes, 10))
		progress.phase("download", progress.value.Bytes)
		if e = serveNativeSnapshot(w, r, snapshot, sha, progress); e != nil {
			progress.fail(errNativeTransferIncomplete)
			return
		}
		success = true
		return
	}
	if sha != r.Header.Get("X-Apteva-Snapshot-SHA256") {
		fail(errNativeChecksum)
		return
	}
	// Validate every entry and the gzip trailer before any remote mutation.
	progress.phase("verify_archive", 0)
	var expanded nativeByteCounter
	if e = readNativeArchiveContext(c, snapshot, nativeProgressWriter{&expanded, progress}, limits, &stats); e != nil {
		fail(e)
		return
	}
	if hasExpectedState && stats.Bytes != expectedState {
		fail(errNativeArchiveInvalid)
		return
	}
	// Recheck just before transfer; available disk space can change during upload.
	space, e = transport.StateSpace(c, i, d, peers)
	if e != nil {
		fail(e)
		return
	}
	if e = nativeRequireSpace(space.Available, nativeRestoreSpace(stats.Bytes, stats.Entries, space.AllocationUnit)); e != nil {
		fail(e)
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
	archiveInfo, e := f.Stat()
	if e != nil {
		fail(e)
		return
	}
	d.Restore = &nativeRestore{SHA256: sha, Key: restoreKey, Bytes: int64(expanded), Entries: stats.Entries, Policy: &policy, StateBytes: stats.Bytes, ArchiveBytes: archiveInfo.Size()}
	if e = b.saveCAS(before, d); e != nil {
		http.Error(w, "cannot persist restore intent", 503)
		return
	}
	uncertain = true
	progress.uncertain()
	pipeR, pipeW := io.Pipe()
	done := make(chan error, 1)
	progress.phase("restore", int64(expanded))
	go func() {
		err := readNativeArchiveContext(c, snapshot, nativeProgressWriter{pipeW, progress}, limits, nil)
		pipeW.CloseWithError(err)
		done <- err
	}()
	e = transport.Restore(c, i, d, peers, pipeR)
	pipeR.CloseWithError(errors.New("restore transport closed"))
	encodeErr := <-done
	if e != nil || encodeErr != nil {
		if e == nil {
			e = encodeErr
		}
		fail(e)
		return
	}
	progress.phase("verify_restore", 0)
	if e = b.reconcileRestore(c, i, &d); e != nil || d.Execution.SnapshotSHA256 != sha {
		if e == nil {
			e = errors.New("restore digest unconfirmed")
		}
		fail(e)
		return
	}
	success = true
	w.Header().Set("X-Apteva-Snapshot-SHA256", sha)
	writeNative(w, map[string]any{"snapshot_sha256": sha})
}

type nativeSnapshotCache struct {
	Deployment, Key, SHA256 string
	ArchiveBytes            int64
}
type nativeLimitWriter struct {
	io.Writer
	Remaining  int64
	LimitError error
}

func (w *nativeLimitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.Remaining {
		if w.LimitError != nil {
			return 0, w.LimitError
		}
		return 0, errNativeStateTooLarge
	}
	n, e := w.Writer.Write(p)
	w.Remaining -= int64(n)
	return n, e
}
func nativeStateFileHash(name string) (string, error) {
	return nativeStateFileHashLimited(name, defaultNativeSnapshotLimits(), nil)
}
func nativeStateFileHashLimited(name string, limits nativeSnapshotLimits, progress *nativeProgress) (string, error) {
	f, e := os.Open(name)
	if e != nil {
		return "", e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return "", e
	}
	if !info.Mode().IsRegular() {
		return "", errNativeArchiveInvalid
	}
	if info.Size() > limits.ArchiveBytes {
		return "", errNativeArchiveTooLarge
	}
	h := sha256.New()
	var reader io.Reader = io.LimitReader(f, limits.ArchiveBytes+1)
	if progress != nil {
		reader = nativeProgressReader{reader, progress}
	}
	n, err := io.Copy(h, reader)
	e = err
	if n > limits.ArchiveBytes {
		return "", errNativeArchiveTooLarge
	}
	if n != info.Size() {
		return "", errNativeTransferIncomplete
	}
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
func serveNativeSnapshot(w http.ResponseWriter, r *http.Request, name, sha string, progress *nativeProgress) error {
	f, e := os.Open(name)
	if e != nil {
		http.Error(w, "snapshot unavailable", 503)
		return e
	}
	defer f.Close()
	w.Header().Set("X-Apteva-Snapshot-SHA256", sha)
	w.Header().Set("Content-Type", "application/gzip")
	out := &nativeSnapshotResponse{ResponseWriter: w, progress: progress}
	http.ServeContent(out, r, "state.tar.gz", time.Time{}, f)
	return out.err
}

type nativeSnapshotResponse struct {
	http.ResponseWriter
	progress *nativeProgress
	err      error
}

func (w *nativeSnapshotResponse) Write(p []byte) (int, error) {
	if w.progress.ctx != nil && w.progress.ctx.Err() != nil {
		w.err = w.progress.ctx.Err()
		return 0, w.err
	}
	n, e := w.ResponseWriter.Write(p)
	w.progress.add(n)
	if e != nil {
		w.err = e
	}
	return n, e
}
func readNativeArchive(name string, out io.Writer) error {
	return readNativeArchiveLimited(name, out, defaultNativeSnapshotLimits(), nil)
}
func readNativeArchiveLimited(name string, out io.Writer, limits nativeSnapshotLimits, stats *nativeArchiveStats) error {
	return readNativeArchiveContext(context.Background(), name, out, limits, stats)
}
func readNativeArchiveContext(c context.Context, name string, out io.Writer, limits nativeSnapshotLimits, stats *nativeArchiveStats) error {
	f, e := os.Open(name)
	if e != nil {
		return e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() {
		return errNativeArchiveInvalid
	}
	if info.Size() > limits.ArchiveBytes {
		return errNativeArchiveTooLarge
	}
	gz, e := gzip.NewReader(nativeContextReader{io.LimitReader(f, limits.ArchiveBytes+1), c})
	if e != nil {
		return fmt.Errorf("%w: %w", errNativeArchiveInvalid, e)
	}
	defer gz.Close()
	e = canonicalNativeTarLimited(gz, out, false, limits, stats)
	if errors.Is(e, gzip.ErrChecksum) {
		return errNativeChecksum
	}
	if e != nil && !errors.Is(e, errNativeStateTooLarge) && !errors.Is(e, errNativeArchiveTooLarge) && !errors.Is(e, io.ErrClosedPipe) && !errors.Is(e, syscall.ENOSPC) {
		return fmt.Errorf("%w: %w", errNativeArchiveInvalid, e)
	}
	return e
}

// Re-encode trusted types only, removing ownership, PAX metadata and unsafe
// permission bits. WAL, SHM, journals and ordinary hidden app files are retained.
func canonicalNativeTar(input io.Reader, out io.Writer, fromHost bool) error {
	return canonicalNativeTarLimited(input, out, fromHost, defaultNativeSnapshotLimits(), nil)
}
func canonicalNativeTarLimited(input io.Reader, out io.Writer, fromHost bool, limits nativeSnapshotLimits, stats *nativeArchiveStats) error {
	limited := &io.LimitedReader{R: input, N: limits.StateBytes + nativeStateTarOverhead + 1}
	tr := tar.NewReader(limited)
	tw := tar.NewWriter(&nativeLimitWriter{Writer: out, Remaining: limits.StateBytes + nativeStateTarOverhead, LimitError: errNativeStateTooLarge})
	seen := map[string]byte{}
	var total int64
	count := 0
	var names int64
	rootSeen, markerSeen := false, false
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if h.Size < 0 {
			return errNativeArchiveInvalid
		}
		name := strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/")
		if (h.Name == "." || h.Name == "./") && h.Typeflag == tar.TypeDir {
			if rootSeen || h.Size != 0 {
				return errNativeArchiveInvalid
			}
			rootSeen = true
			continue
		}
		if name == nativeStateMarker && fromHost && h.Typeflag == tar.TypeReg {
			if markerSeen || h.Size > 256 {
				return errNativeArchiveInvalid
			}
			markerSeen = true
			continue
		}
		count++
		if count > int(nativeEntryLimit(limits)) || h.Size > limits.StateBytes-total {
			return errNativeStateTooLarge
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
		if len(seen) > int(nativeEntryLimit(limits)) {
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
			return errNativeStateTooLarge
		}
	}
	if limited.N <= 0 {
		return errNativeStateTooLarge
	}
	if stats != nil {
		stats.Bytes = total
		stats.Entries = int64(len(seen))
	}
	return tw.Close()
}

type nativeByteCounter int64

func (n *nativeByteCounter) Write(p []byte) (int, error) {
	*n += nativeByteCounter(len(p))
	return len(p), nil
}
