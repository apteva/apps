package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const nativeDefaultStateLimit int64 = 8 << 30
const nativeStateHardLimit int64 = 16 << 30

// Tar headers, padding and bounded path metadata are separate from file content.
const nativeStateTarOverhead int64 = 128 << 20
const nativeStateReserve int64 = 256 << 20
const nativeStateMaxEntries int64 = 100000

type nativeSnapshotLimits struct {
	ArchiveBytes int64 `json:"max_archive_bytes"`
	StateBytes   int64 `json:"max_expanded_bytes"`
	MaxEntries   int64 `json:"max_entries,omitempty"`
}

func defaultNativeSnapshotLimits() nativeSnapshotLimits {
	return nativeSnapshotLimits{ArchiveBytes: nativeDefaultStateLimit, StateBytes: nativeDefaultStateLimit, MaxEntries: nativeStateMaxEntries}
}
func loadNativeSnapshotLimits(configs ...sdk.Config) (nativeSnapshotLimits, time.Duration, error) {
	config := sdk.Config{}
	if len(configs) > 0 {
		config = configs[0]
	}
	lookup := func(env, key string) (string, bool) {
		if raw, ok := os.LookupEnv(env); ok {
			return raw, true
		}
		raw := config.Get(key)
		return raw, raw != ""
	}
	l := defaultNativeSnapshotLimits()
	for name, value := range map[string]*int64{
		"APTEVA_NATIVE_SNAPSHOT_MAX_ARCHIVE_BYTES": &l.ArchiveBytes,
		"APTEVA_NATIVE_SNAPSHOT_MAX_STATE_BYTES":   &l.StateBytes,
	} {
		key := "native_snapshot_max_archive_bytes"
		if name == "APTEVA_NATIVE_SNAPSHOT_MAX_STATE_BYTES" {
			key = "native_snapshot_max_state_bytes"
		}
		if raw, ok := lookup(name, key); ok {
			n, e := strconv.ParseInt(raw, 10, 64)
			if e != nil || n <= 0 || n > nativeStateHardLimit {
				return l, 0, fmt.Errorf("%s (%s) must be between 1 and %d bytes", key, name, nativeStateHardLimit)
			}
			*value = n
		}
	}
	seconds := int64(7200)
	if raw, ok := lookup("APTEVA_NATIVE_SNAPSHOT_TIMEOUT_SECONDS", "native_snapshot_timeout_seconds"); ok {
		n, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || n < 60 || n > 21600 {
			return l, 0, errors.New("APTEVA_NATIVE_SNAPSHOT_TIMEOUT_SECONDS must be between 60 and 21600")
		}
		seconds = n
	}
	return l, time.Duration(seconds) * time.Second, nil
}
func (b *nativeBackend) snapshotLimits() nativeSnapshotLimits {
	if b.stateLimits.ArchiveBytes == 0 {
		return defaultNativeSnapshotLimits()
	}
	return b.stateLimits
}
func (b *nativeBackend) snapshotTimeout() time.Duration {
	if b.stateTimeout == 0 {
		return 2 * time.Hour
	}
	return b.stateTimeout
}

type nativeLimitsKey struct{}

func nativeLimitsFromContext(c context.Context) nativeSnapshotLimits {
	if l, ok := c.Value(nativeLimitsKey{}).(nativeSnapshotLimits); ok {
		return l
	}
	return defaultNativeSnapshotLimits()
}

type nativeStateSpace struct {
	Bytes          int64 `json:"bytes"`
	Entries        int64 `json:"entries"`
	Available      int64 `json:"available_bytes"`
	AllocationUnit int64 `json:"allocation_unit"`
}
type nativeArchiveStats struct{ Bytes, Entries int64 }

// Free space already excludes the old state retained during atomic replacement.
// Account for allocation rounding, directories and receipts in addition to data.
func nativeRestoreSpace(bytes, entries int64, units ...int64) int64 {
	unit := int64(4096)
	if len(units) > 0 && units[0] > 0 {
		unit = units[0]
	}
	return bytes + entries*unit + nativeStateReserve
}
func nativeRequireSpace(available, needed int64) error {
	if available < needed {
		return errNativeDiskSpace
	}
	return nil
}
func (b *nativeBackend) localStateSpace(name string, needed int64) error {
	var available int64
	var e error
	if b.stateDiskFree != nil {
		available, e = b.stateDiskFree(name)
	} else {
		available, e = nativeDiskAvailable(name)
	}
	if e != nil {
		return e
	}
	return nativeRequireSpace(available, needed)
}

var errNativeArchiveTooLarge = errors.New("compressed archive exceeds configured limit")
var errNativeStateTooLarge = errors.New("expanded state exceeds configured limit")
var errNativeDiskSpace = errors.New("insufficient disk space including safety reserve")
var errNativeTransferIncomplete = errors.New("snapshot transfer incomplete")
var errNativeChecksum = errors.New("snapshot checksum mismatch")
var errNativeArchiveInvalid = errors.New("unsafe or corrupt snapshot archive")

type nativeStateFailure struct {
	Code, Message string
	Status        int
}

func nativeStateFailureFor(e error) nativeStateFailure {
	switch {
	case errors.Is(e, errNativeArchiveTooLarge):
		return nativeStateFailure{"archive_too_large", errNativeArchiveTooLarge.Error(), 413}
	case errors.Is(e, errNativeStateTooLarge):
		return nativeStateFailure{"expanded_state_too_large", errNativeStateTooLarge.Error(), 413}
	case errors.Is(e, errNativeDiskSpace), errors.Is(e, syscall.ENOSPC), errors.Is(e, syscall.EDQUOT):
		return nativeStateFailure{"insufficient_disk_space", errNativeDiskSpace.Error(), 507}
	case errors.Is(e, errNativeTransferIncomplete), errors.Is(e, io.ErrUnexpectedEOF), errors.Is(e, context.Canceled), errors.Is(e, context.DeadlineExceeded):
		return nativeStateFailure{"incomplete_transfer", "snapshot transfer incomplete", 400}
	case errors.Is(e, errNativeChecksum):
		return nativeStateFailure{"checksum_mismatch", errNativeChecksum.Error(), 400}
	case errors.Is(e, errNativeArchiveInvalid):
		return nativeStateFailure{"invalid_archive", errNativeArchiveInvalid.Error(), 400}
	default:
		return nativeStateFailure{"state_transfer_failed", "state transfer failed; inspect before retry", 503}
	}
}
func writeNativeStateFailure(w http.ResponseWriter, e error, uncertain ...bool) {
	f := nativeStateFailureFor(e)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.Status)
	detail := map[string]any{"code": f.Code, "message": f.Message}
	if len(uncertain) > 0 && uncertain[0] {
		detail["outcome"] = "unknown"
		detail["requires_inspection"] = true
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": detail})
}

// One latest progress receipt for the serialized state-transfer slot, never a
// per-chunk history. Persist at most once a second, and at phase boundaries.
type nativeStateProgress struct {
	Deployment         string    `json:"deployment_id"`
	Action             string    `json:"action"`
	Key                string    `json:"key_hash,omitempty"`
	Epoch              string    `json:"controller_epoch,omitempty"`
	Phase              string    `json:"phase"`
	Status             string    `json:"status"`
	Bytes              int64     `json:"bytes"`
	Total              int64     `json:"total_bytes,omitempty"`
	ErrorCode          string    `json:"error_code,omitempty"`
	RequiresInspection bool      `json:"requires_inspection,omitempty"`
	Started            time.Time `json:"started_at"`
	Updated            time.Time `json:"updated_at"`
}
type nativeProgress struct {
	mu      sync.Mutex
	path    string
	value   nativeStateProgress
	last    time.Time
	failure error
	ctx     context.Context
}

func (b *nativeBackend) newStateProgress(id, action, key string) *nativeProgress {
	now := time.Now().UTC()
	return &nativeProgress{path: filepath.Join(b.root, "state-progress.json"), value: nativeStateProgress{Deployment: id, Action: action, Key: nativeHash(key), Epoch: b.stateEpoch, Status: "running", Started: now}}
}
func (p *nativeProgress) persist(force bool) {
	now := time.Now().UTC()
	if !force && now.Sub(p.last) < time.Second {
		return
	}
	p.value.Updated = now
	_ = writeNativeStateJSON(p.path, p.value)
	p.last = now
}
func (p *nativeProgress) phase(phase string, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value.Phase, p.value.Bytes, p.value.Total = phase, 0, total
	p.persist(true)
}
func (p *nativeProgress) add(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value.Bytes += int64(n)
	p.persist(false)
}
func (p *nativeProgress) fail(e error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failure = e
	p.value.ErrorCode = nativeStateFailureFor(e).Code
}
func (p *nativeProgress) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure == nil {
		p.value.Status = "completed"
		p.value.RequiresInspection = false
	} else {
		p.value.Status = "failed"
	}
	p.persist(true)
}

type nativeProgressReader struct {
	io.Reader
	progress *nativeProgress
}

func (r nativeProgressReader) Read(buf []byte) (int, error) {
	if r.progress.ctx != nil && r.progress.ctx.Err() != nil {
		return 0, r.progress.ctx.Err()
	}
	n, e := r.Reader.Read(buf)
	r.progress.add(n)
	return n, e
}

type nativeProgressWriter struct {
	io.Writer
	progress *nativeProgress
}

func (w nativeProgressWriter) Write(buf []byte) (int, error) {
	if w.progress.ctx != nil && w.progress.ctx.Err() != nil {
		return 0, w.progress.ctx.Err()
	}
	n, e := w.Writer.Write(buf)
	w.progress.add(n)
	return n, e
}
func (b *nativeBackend) stateProgressHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", 405)
		return
	}
	id, key := r.URL.Query().Get("deployment_id"), r.Header.Get("Idempotency-Key")
	if !nativeID(id) || len(key) == 0 || len(key) > 512 {
		http.Error(w, "deployment_id and Idempotency-Key required", 400)
		return
	}
	if d, e := b.record(id); e != nil || d.Execution.State == "deleted" {
		http.Error(w, "deployment unavailable", 404)
		return
	}
	f, e := os.Open(filepath.Join(b.root, "state-progress.json"))
	if e != nil {
		http.Error(w, "progress unavailable", 404)
		return
	}
	defer f.Close()
	var v nativeStateProgress
	if json.NewDecoder(io.LimitReader(f, 16384)).Decode(&v) != nil || v.Deployment != id || v.Key != nativeHash(key) {
		http.Error(w, "progress unavailable", 404)
		return
	}
	if v.Status == "running" && v.Epoch != b.stateEpoch {
		v.Status = "unknown"
		v.ErrorCode = "controller_restarted"
		v.RequiresInspection = true
	}
	v.Key, v.Epoch = "", ""
	w.Header().Set("Cache-Control", "no-store")
	writeNative(w, v)
}

type nativeContextReader struct {
	io.Reader
	ctx context.Context
}

func (r nativeContextReader) Read(buf []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.Reader.Read(buf)
}

func (p *nativeProgress) uncertain() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value.RequiresInspection = true
	p.persist(true)
}

type nativeUploadReader struct{ io.Reader }

func (r nativeUploadReader) Read(p []byte) (int, error) {
	n, e := r.Reader.Read(p)
	if e != nil && e != io.EOF {
		e = fmt.Errorf("%w: %w", errNativeTransferIncomplete, e)
	}
	return n, e
}

// Wire mirror of Server's additive backend contract; does not require an
// unreleased local SDK type when this app is fetched/built independently.
type nativeStatePolicy struct {
	Format           string `json:"format"`
	MaxArchiveBytes  int64  `json:"max_archive_bytes"`
	MaxExpandedBytes int64  `json:"max_expanded_bytes"`
	MaxEntries       int64  `json:"max_entries"`
	TimeoutSeconds   int64  `json:"timeout_seconds"`
}

func nativeEntryLimit(l nativeSnapshotLimits) int64 {
	if l.MaxEntries > 0 {
		return l.MaxEntries
	}
	return nativeStateMaxEntries
}
func (b *nativeBackend) statePolicy() nativeStatePolicy {
	l := b.snapshotLimits()
	return nativeStatePolicy{Format: "tar.gz", MaxArchiveBytes: l.ArchiveBytes, MaxExpandedBytes: l.StateBytes, MaxEntries: nativeEntryLimit(l), TimeoutSeconds: int64(b.snapshotTimeout() / time.Second)}
}
func (b *nativeBackend) validateStatePolicy(p nativeStatePolicy) error {
	own := b.statePolicy()
	if p.Format != "tar.gz" || p.MaxArchiveBytes <= 0 || p.MaxExpandedBytes <= 0 || p.MaxEntries <= 0 || p.TimeoutSeconds <= 0 {
		return errors.New("invalid state transfer policy")
	}
	if p.MaxArchiveBytes > own.MaxArchiveBytes || p.MaxExpandedBytes > own.MaxExpandedBytes || p.MaxEntries > own.MaxEntries || p.TimeoutSeconds > own.TimeoutSeconds {
		return errors.New("state transfer policy exceeds backend capabilities")
	}
	return nil
}
func (b *nativeBackend) stateHTTPPolicy(r *http.Request) (nativeStatePolicy, error) {
	p := b.statePolicy()
	raw := r.Header.Get("X-Apteva-State-Transfer-Policy")
	if raw == "" {
		p.MaxArchiveBytes = min(p.MaxArchiveBytes, 512<<20)
		p.MaxExpandedBytes = min(p.MaxExpandedBytes, 512<<20)
		p.MaxEntries = min(p.MaxEntries, 100000)
		p.TimeoutSeconds = min(p.TimeoutSeconds, 1200)
		return p, nil
	}
	if len(raw) > 4096 || json.Unmarshal([]byte(raw), &p) != nil {
		return p, errors.New("invalid state transfer policy")
	}
	// Do not inherit omitted fields from defaults.
	var explicit nativeStatePolicy
	if json.Unmarshal([]byte(raw), &explicit) != nil {
		return p, errors.New("invalid state transfer policy")
	}
	return explicit, b.validateStatePolicy(explicit)
}
func (p nativeStatePolicy) limits() nativeSnapshotLimits {
	return nativeSnapshotLimits{ArchiveBytes: p.MaxArchiveBytes, StateBytes: p.MaxExpandedBytes, MaxEntries: p.MaxEntries}
}
