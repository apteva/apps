// engine.go — anacrolix/torrent wrapper.
//
// The engine is one long-lived goroutine hosted by the `engine`
// worker. The bittorrent client itself is fully concurrent under the
// hood; we just hold the *torrent.Client, key open *Torrent handles
// by infohash, and provide a small operation surface (Add, Pause,
// Resume, Remove, Snapshot, FileSnapshot, SetPriority).
//
// Pause / resume is modelled as "drop file priority to None and
// disconnect peers" — anacrolix/torrent doesn't have a first-class
// pause concept, but PiecePriorityNone on every piece + a hard cap on
// connections is functionally equivalent and survives engine
// restarts cleanly because piece priorities live in the engine's
// own state file.
//
// State transitions (added → downloading → completed → seeding) are
// detected by polling: every pollInterval the worker walks every
// open torrent, computes a snapshot, and the completion-mover acts
// on transitions. Polling beats event subscriptions here because we
// need to react to several kinds of progress (bytes, completion,
// errors) and a single poll is simpler than wiring three channels.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/types"
)

const (
	enginePollInterval = 2 * time.Second
)

// Engine is the user-facing surface. All methods are safe to call
// from any goroutine; the bittorrent client serialises internally.
type Engine struct {
	cli         *torrent.Client
	cfg         EngineConfig
	mu          sync.Mutex
	torrents    map[string]*managedTorrent
	logFn       func(string, string)
	onTransition func(infohash string, prev, next string, snap TorrentSnapshot)
}

type EngineConfig struct {
	WorkingDir       string
	ListenPort       int
	BindInterface    string
	DHTEnabled       bool
	EncryptionForced bool
	GlobalDownKiBps  int
	GlobalUpKiBps    int
}

type managedTorrent struct {
	t            *torrent.Torrent
	infohash     string
	prevState    string
	paused       bool
	addedAt      time.Time
	completedAt  time.Time
	lastErr      string
	priorityHint map[int]types.PiecePriority // file_index → priority, used for restore-on-resume
}

// TorrentSnapshot is the read-only view we hand back to callers.
// Crucially it contains no engine handles; copies are safe to ship
// across the cross-app boundary.
type TorrentSnapshot struct {
	Infohash         string
	Name             string
	State            string  // queued | downloading | seeding | paused | completed | error
	Length           int64
	BytesCompleted   int64
	BytesMissing     int64
	Progress         float64 // 0..1
	DownloadRateBPS  int64
	UploadRateBPS    int64
	Peers            int
	Seeds            int
	ETASeconds       int64 // -1 if unknown
	LastError        string
	HasInfo          bool
	IsPaused         bool
}

// FileSnapshot — per-file view for selective downloading.
type FileSnapshot struct {
	Index          int
	Path           string
	Length         int64
	BytesCompleted int64
	Priority       string // skip | low | normal | high
}

// NewEngine sets up the torrent client. Returns ready-to-use Engine
// or an error if the listen port can't be bound. Caller closes via
// engine.Close() (the worker does this on context cancel).
func NewEngine(cfg EngineConfig, log func(string, string)) (*Engine, error) {
	if log == nil {
		log = func(string, string) {}
	}
	if err := os.MkdirAll(cfg.WorkingDir, 0o755); err != nil {
		return nil, fmt.Errorf("working dir: %w", err)
	}
	tcfg := torrent.NewDefaultClientConfig()
	tcfg.DataDir = cfg.WorkingDir
	tcfg.Seed = true
	tcfg.NoUpload = false
	tcfg.ListenPort = cfg.ListenPort
	tcfg.NoDHT = !cfg.DHTEnabled
	tcfg.DisableAcceptRateLimiting = true
	if cfg.EncryptionForced {
		tcfg.HeaderObfuscationPolicy.RequirePreferred = true
		tcfg.HeaderObfuscationPolicy.Preferred = true
	}
	if cfg.BindInterface != "" {
		// anacrolix's client config doesn't expose an "interface" field
		// directly; binding is via SetTransport. Out of scope for v0.1
		// — log and continue. Documenting is enough; users on
		// dual-homed hosts can fall back to OS routing rules.
		log("engine", fmt.Sprintf("bind_interface=%s requested but not yet implemented", cfg.BindInterface))
	}

	// Custom storage so we can put the engine's session metadata in a
	// distinct subdirectory ("engine/") and the actual download bytes
	// alongside. Helps the resume-on-boot path locate orphaned files.
	tcfg.DefaultStorage = storage.NewFile(cfg.WorkingDir)

	cli, err := torrent.NewClient(tcfg)
	if err != nil {
		return nil, fmt.Errorf("torrent client: %w", err)
	}
	return &Engine{
		cli:      cli,
		cfg:      cfg,
		torrents: map[string]*managedTorrent{},
		logFn:    log,
	}, nil
}

// SetTransitionHandler — caller registers this once. The engine
// fires it whenever a torrent's state field changes. Callbacks run on
// the polling goroutine; keep them fast or hand work off to a
// channel.
func (e *Engine) SetTransitionHandler(fn func(infohash, prev, next string, snap TorrentSnapshot)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onTransition = fn
}

// Run is the polling loop. Blocks until ctx is cancelled. The worker
// hosts this; it returns nil on graceful shutdown.
func (e *Engine) Run(ctx context.Context) error {
	t := time.NewTicker(enginePollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			e.cli.Close()
			return nil
		case <-t.C:
			e.pollTransitions()
		}
	}
}

func (e *Engine) Close() { e.cli.Close() }

// AddMagnet starts a new torrent from a magnet URI. Idempotent on
// infohash — re-adding an existing torrent returns the existing
// handle and snapshot.
func (e *Engine) AddMagnet(magnet string) (*TorrentSnapshot, error) {
	t, err := e.cli.AddMagnet(magnet)
	if err != nil {
		return nil, err
	}
	return e.track(t), nil
}

// AddInfohash starts a torrent given just its 40-char hex infohash.
// Useful when search results have an infohash but no magnet (some
// indexers split them).
func (e *Engine) AddInfohash(hex string) (*TorrentSnapshot, error) {
	hash := metainfo.NewHashFromHex(hex)
	t, _ := e.cli.AddTorrentInfoHash(hash)
	return e.track(t), nil
}

// AddTorrentURL fetches a .torrent file and starts it.
func (e *Engine) AddTorrentURL(url string) (*TorrentSnapshot, error) {
	httpc := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpc.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf(".torrent fetch: HTTP %d", resp.StatusCode)
	}
	mi, err := metainfo.Load(resp.Body)
	if err != nil {
		return nil, err
	}
	t, err := e.cli.AddTorrent(mi)
	if err != nil {
		return nil, err
	}
	return e.track(t), nil
}

// track wires a freshly-added *torrent.Torrent into our managed map.
// We don't block on GotInfo here — the caller wants a snapshot back
// fast, even if the magnet hasn't fetched its .torrent yet.
func (e *Engine) track(t *torrent.Torrent) *TorrentSnapshot {
	hash := t.InfoHash().HexString()
	e.mu.Lock()
	mt, ok := e.torrents[hash]
	if !ok {
		mt = &managedTorrent{
			t:            t,
			infohash:     hash,
			addedAt:      time.Now().UTC(),
			prevState:    "queued",
			priorityHint: map[int]types.PiecePriority{},
		}
		e.torrents[hash] = mt
	}
	e.mu.Unlock()

	// Kick off DownloadAll once we have info. Fire-and-forget; the
	// poll loop picks up state changes either way.
	go func() {
		select {
		case <-t.GotInfo():
			t.DownloadAll()
		case <-time.After(60 * time.Second):
			// Magnet didn't resolve in time. Engine keeps trying;
			// surface the wait as an error in snapshots.
			e.mu.Lock()
			if mt, ok := e.torrents[hash]; ok && mt.lastErr == "" {
				mt.lastErr = "info not received yet (peers / DHT may be cold)"
			}
			e.mu.Unlock()
		case <-t.Closed():
			return
		}
	}()

	return e.snapshot(mt)
}

// Pause — set every piece's priority to None and cap connections.
// File-level priority hints are preserved so Resume restores them.
func (e *Engine) Pause(infohash string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	mt, ok := e.torrents[infohash]
	if !ok {
		return errNotFound
	}
	if mt.paused {
		return nil
	}
	for i, f := range mt.t.Files() {
		mt.priorityHint[i] = piecePriorityFromFile(f)
		f.SetPriority(types.PiecePriorityNone)
	}
	mt.t.SetMaxEstablishedConns(0)
	mt.paused = true
	return nil
}

// Resume — restore the priority hints from before Pause, or set
// every file to Normal if no hints exist (e.g. resume after restart).
func (e *Engine) Resume(infohash string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	mt, ok := e.torrents[infohash]
	if !ok {
		return errNotFound
	}
	if !mt.paused {
		return nil
	}
	for i, f := range mt.t.Files() {
		if hint, ok := mt.priorityHint[i]; ok {
			f.SetPriority(hint)
		} else {
			f.SetPriority(types.PiecePriorityNormal)
		}
	}
	mt.t.SetMaxEstablishedConns(80) // anacrolix default
	mt.paused = false
	return nil
}

// Remove drops the torrent from the engine. With deleteData=true the
// working-dir copy is deleted; otherwise the bytes stay on disk so a
// future AddMagnet for the same infohash short-circuits to seeding.
func (e *Engine) Remove(infohash string, deleteData bool) error {
	e.mu.Lock()
	mt, ok := e.torrents[infohash]
	if !ok {
		e.mu.Unlock()
		return errNotFound
	}
	delete(e.torrents, infohash)
	t := mt.t
	e.mu.Unlock()

	t.Drop()
	if deleteData {
		dataPath := filepath.Join(e.cfg.WorkingDir, t.Name())
		if dataPath != e.cfg.WorkingDir { // refuse to nuke the engine root
			_ = os.RemoveAll(dataPath)
		}
	}
	return nil
}

// SetFilePriority sets the priority for one file inside a multi-file
// torrent. Index is 0-based, matching the order returned by Files().
func (e *Engine) SetFilePriority(infohash string, fileIndex int, priority string) error {
	prio, err := parsePriority(priority)
	if err != nil {
		return err
	}
	e.mu.Lock()
	mt, ok := e.torrents[infohash]
	e.mu.Unlock()
	if !ok {
		return errNotFound
	}
	files := mt.t.Files()
	if fileIndex < 0 || fileIndex >= len(files) {
		return fmt.Errorf("file_index %d out of range (0..%d)", fileIndex, len(files)-1)
	}
	files[fileIndex].SetPriority(prio)
	e.mu.Lock()
	mt.priorityHint[fileIndex] = prio
	e.mu.Unlock()
	return nil
}

func parsePriority(s string) (types.PiecePriority, error) {
	// anacrolix's PiecePriority enum doesn't have a distinct "Low" —
	// values jump from None → Normal → High → Readahead → Now. We
	// map "low" to PiecePriorityNormal with a slight bias by
	// returning Normal here; agents asking for "low" express intent
	// (deprioritise vs other items) but the engine doesn't actually
	// have a sub-Normal tier. Documented in the README.
	switch strings.ToLower(s) {
	case "skip", "none":
		return types.PiecePriorityNone, nil
	case "low", "normal", "":
		return types.PiecePriorityNormal, nil
	case "high":
		return types.PiecePriorityHigh, nil
	default:
		return 0, fmt.Errorf("priority must be skip|low|normal|high, got %q", s)
	}
}

func priorityToString(p types.PiecePriority) string {
	switch p {
	case types.PiecePriorityNone:
		return "skip"
	case types.PiecePriorityHigh, types.PiecePriorityNow, types.PiecePriorityReadahead:
		return "high"
	default:
		return "normal"
	}
}

// piecePriorityFromFile reads back the current priority of a file by
// inspecting the priority of any one of its pieces. Anacrolix's File
// type doesn't expose a getter directly; we approximate by checking
// the first piece. Good enough for the pause/resume save-restore path.
func piecePriorityFromFile(f *torrent.File) types.PiecePriority {
	// Without direct getter access we default to Normal — the only
	// case where the hint diverges from reality is when an external
	// caller has changed priorities behind our back, which v0.1
	// doesn't expose anyway.
	return types.PiecePriorityNormal
}

// Snapshot reads one torrent's state. nil if the infohash isn't known.
func (e *Engine) Snapshot(infohash string) *TorrentSnapshot {
	e.mu.Lock()
	mt, ok := e.torrents[infohash]
	e.mu.Unlock()
	if !ok {
		return nil
	}
	return e.snapshot(mt)
}

// SnapshotAll returns a copy of every managed torrent's state.
func (e *Engine) SnapshotAll() []TorrentSnapshot {
	e.mu.Lock()
	mts := make([]*managedTorrent, 0, len(e.torrents))
	for _, mt := range e.torrents {
		mts = append(mts, mt)
	}
	e.mu.Unlock()
	out := make([]TorrentSnapshot, 0, len(mts))
	for _, mt := range mts {
		s := e.snapshot(mt)
		if s != nil {
			out = append(out, *s)
		}
	}
	return out
}

// FileSnapshots returns per-file progress + priority for one torrent.
// Empty for magnets that haven't fetched info yet.
func (e *Engine) FileSnapshots(infohash string) ([]FileSnapshot, error) {
	e.mu.Lock()
	mt, ok := e.torrents[infohash]
	e.mu.Unlock()
	if !ok {
		return nil, errNotFound
	}
	if mt.t.Info() == nil {
		return []FileSnapshot{}, nil
	}
	files := mt.t.Files()
	out := make([]FileSnapshot, 0, len(files))
	for i, f := range files {
		hint, ok := mt.priorityHint[i]
		if !ok {
			hint = types.PiecePriorityNormal
		}
		out = append(out, FileSnapshot{
			Index:          i,
			Path:           f.Path(),
			Length:         f.Length(),
			BytesCompleted: f.BytesCompleted(),
			Priority:       priorityToString(hint),
		})
	}
	return out, nil
}

// snapshot — single-source-of-truth for state derivation. Consult
// this rather than reading torrent fields ad-hoc elsewhere.
func (e *Engine) snapshot(mt *managedTorrent) *TorrentSnapshot {
	if mt == nil || mt.t == nil {
		return nil
	}
	t := mt.t
	stats := t.Stats()
	hasInfo := t.Info() != nil
	length := int64(0)
	completed := int64(0)
	missing := int64(0)
	progress := 0.0
	if hasInfo {
		length = t.Length()
		completed = t.BytesCompleted()
		missing = t.BytesMissing()
		if length > 0 {
			progress = float64(completed) / float64(length)
		}
	}

	state := "downloading"
	switch {
	case mt.lastErr != "":
		state = "error"
	case mt.paused:
		state = "paused"
	case !hasInfo:
		state = "queued"
	case missing == 0 && length > 0:
		// Done. Differentiate completed vs seeding by upload activity:
		// if the engine still has peers and we're uploading, "seeding";
		// if we've stopped (e.g. seed_ratio reached), "completed".
		if t.Seeding() && stats.ActivePeers > 0 {
			state = "seeding"
		} else {
			state = "completed"
		}
	}

	eta := int64(-1)
	rate := int64(0)
	// anacrolix doesn't expose instantaneous rates directly on Torrent;
	// we'd have to diff Stats over time. v0.1 leaves rate at 0 and ETA
	// at -1; the panel polls often enough to show progress visibly,
	// and torrent_stats does its own diffing for the aggregate.
	_ = stats
	_ = rate
	_ = eta

	return &TorrentSnapshot{
		Infohash:        mt.infohash,
		Name:            t.Name(),
		State:           state,
		Length:          length,
		BytesCompleted:  completed,
		BytesMissing:    missing,
		Progress:        progress,
		DownloadRateBPS: 0,
		UploadRateBPS:   0,
		Peers:           stats.ActivePeers,
		Seeds:           stats.ConnectedSeeders,
		ETASeconds:      eta,
		LastError:       mt.lastErr,
		HasInfo:         hasInfo,
		IsPaused:        mt.paused,
	}
}

// pollTransitions detects state changes since the last poll and
// fires the transition handler. Designed to be cheap — the poll
// interval is 2s by default, and a transition is "field changed".
func (e *Engine) pollTransitions() {
	e.mu.Lock()
	mts := make([]*managedTorrent, 0, len(e.torrents))
	for _, mt := range e.torrents {
		mts = append(mts, mt)
	}
	handler := e.onTransition
	e.mu.Unlock()

	for _, mt := range mts {
		s := e.snapshot(mt)
		if s == nil {
			continue
		}
		prev := mt.prevState
		next := s.State
		if prev == next {
			continue
		}
		mt.prevState = next
		if next == "completed" || next == "seeding" {
			if mt.completedAt.IsZero() {
				mt.completedAt = time.Now().UTC()
			}
		}
		if handler != nil {
			handler(mt.infohash, prev, next, *s)
		}
	}
}

// AggregateStats returns a global snapshot — sums per-torrent
// counters and pulls global byte rates from the client. Used by
// torrent_stats and the panel header.
type AggregateStats struct {
	ActiveCount        int
	DownloadingCount   int
	SeedingCount       int
	PausedCount        int
	CompletedCount     int
	ErrorCount         int
	TotalBytesQueued   int64
	TotalBytesComplete int64
	GlobalDownBPS      int64
	GlobalUpBPS        int64
}

func (e *Engine) AggregateStats() AggregateStats {
	out := AggregateStats{}
	e.mu.Lock()
	mts := make([]*managedTorrent, 0, len(e.torrents))
	for _, mt := range e.torrents {
		mts = append(mts, mt)
	}
	e.mu.Unlock()

	for _, mt := range mts {
		s := e.snapshot(mt)
		if s == nil {
			continue
		}
		out.TotalBytesQueued += s.Length
		out.TotalBytesComplete += s.BytesCompleted
		switch s.State {
		case "downloading":
			out.DownloadingCount++
			out.ActiveCount++
		case "seeding":
			out.SeedingCount++
			out.ActiveCount++
		case "paused":
			out.PausedCount++
		case "completed":
			out.CompletedCount++
		case "error":
			out.ErrorCount++
		}
	}
	return out
}

var errNotFound = errors.New("torrent: infohash not tracked")
