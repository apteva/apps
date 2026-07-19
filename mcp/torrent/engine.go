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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/types"
	"golang.org/x/sys/unix"
	"golang.org/x/time/rate"
)

const (
	enginePollInterval = 2 * time.Second
)

// Engine is the user-facing surface. All methods are safe to call
// from any goroutine; the bittorrent client serialises internally.
type Engine struct {
	cli          *torrent.Client
	cfg          EngineConfig
	mu           sync.Mutex
	torrents     map[string]*managedTorrent
	logFn        func(string, string)
	onTransition func(infohash string, prev, next string, snap TorrentSnapshot)
}

type EngineConfig struct {
	WorkingDir            string
	ListenPort            int
	BindInterface         string
	DHTEnabled            bool
	EncryptionForced      bool
	GlobalDownKiBps       int
	GlobalUpKiBps         int
	MaxConcurrent         int
	FreeDiskSafetyPercent int
	SeedRatioTarget       float64
	SeedTimeTarget        time.Duration
}

type managedTorrent struct {
	t             *torrent.Torrent
	infohash      string
	prevState     string
	paused        bool
	addedAt       time.Time
	completedAt   time.Time
	lastErr       string
	queued        bool
	seedStopped   bool
	priorityHint  map[int]types.PiecePriority // file_index → priority, used for restore-on-resume
	lastSampleAt  time.Time
	lastReadData  int64
	lastWriteData int64
	downRateBPS   int64
	upRateBPS     int64
}

type interfaceBinding struct {
	ipv4 net.IP
	ipv6 net.IP
}

func resolveInterfaceBinding(name string) (*interfaceBinding, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("bind_interface %q: %w", name, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("bind_interface %q addresses: %w", name, err)
	}
	b := &interfaceBinding{}
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err != nil {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil && b.ipv4 == nil {
			b.ipv4 = ip4
		} else if ip.To16() != nil && b.ipv6 == nil {
			b.ipv6 = ip
		}
	}
	if b.ipv4 == nil && b.ipv6 == nil {
		return nil, fmt.Errorf("bind_interface %q has no usable IP address", name)
	}
	return b, nil
}

func (b *interfaceBinding) ipForNetwork(network string) net.IP {
	if strings.HasSuffix(network, "6") {
		return b.ipv6
	}
	if strings.HasSuffix(network, "4") {
		return b.ipv4
	}
	if b.ipv4 != nil {
		return b.ipv4
	}
	return b.ipv6
}

func (b *interfaceBinding) listenHost(network string) string {
	if ip := b.ipForNetwork(network); ip != nil {
		return ip.String()
	}
	return ""
}

func (b *interfaceBinding) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	ip := b.ipForNetwork(network)
	if ip == nil {
		return nil, fmt.Errorf("bound interface has no address for %s", network)
	}
	d := net.Dialer{}
	if strings.HasPrefix(network, "udp") {
		d.LocalAddr = &net.UDPAddr{IP: ip}
	} else {
		d.LocalAddr = &net.TCPAddr{IP: ip}
	}
	return d.DialContext(ctx, network, addr)
}

// TorrentSnapshot is the read-only view we hand back to callers.
// Crucially it contains no engine handles; copies are safe to ship
// across the cross-app boundary.
type TorrentSnapshot struct {
	Infohash        string  `json:"infohash"`
	Name            string  `json:"name"`
	State           string  `json:"state"` // queued | downloading | seeding | paused | completed | error
	Length          int64   `json:"length"`
	BytesCompleted  int64   `json:"bytes_completed"`
	BytesMissing    int64   `json:"bytes_missing"`
	Progress        float64 `json:"progress"` // 0..1
	DownloadRateBPS int64   `json:"download_rate_bps"`
	UploadRateBPS   int64   `json:"upload_rate_bps"`
	UploadedBytes   int64   `json:"uploaded_bytes"`
	Peers           int     `json:"peers"`
	Seeds           int     `json:"seeds"`
	ETASeconds      int64   `json:"eta_seconds"` // -1 if unknown
	LastError       string  `json:"last_error"`
	HasInfo         bool    `json:"has_info"`
	IsPaused        bool    `json:"is_paused"`
}

// FileSnapshot — per-file view for selective downloading.
type FileSnapshot struct {
	Index          int    `json:"index"`
	Path           string `json:"path"`
	Length         int64  `json:"length"`
	BytesCompleted int64  `json:"bytes_completed"`
	Priority       string `json:"priority"` // skip | low | normal | high
}

// NewEngine sets up the torrent client. Returns ready-to-use Engine
// or an error if the listen port can't be bound. Caller closes via
// engine.Close() (the worker does this on context cancel).
func NewEngine(cfg EngineConfig, log func(string, string)) (*Engine, error) {
	if log == nil {
		log = func(string, string) {}
	}
	if cfg.ListenPort < 0 || cfg.ListenPort > 65535 {
		return nil, fmt.Errorf("listen_port must be between 0 and 65535")
	}
	if cfg.GlobalDownKiBps < 0 || cfg.GlobalUpKiBps < 0 {
		return nil, errors.New("global rate limits must be zero or positive")
	}
	if cfg.MaxConcurrent < 0 {
		return nil, errors.New("max_concurrent_downloads must be zero or positive")
	}
	if cfg.FreeDiskSafetyPercent < 0 || cfg.FreeDiskSafetyPercent >= 100 {
		return nil, errors.New("free_disk_safety_pct must be between 0 and 99")
	}
	if cfg.SeedRatioTarget < 0 || cfg.SeedTimeTarget < 0 {
		return nil, errors.New("seed targets must be zero or positive")
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
	if cfg.GlobalDownKiBps > 0 {
		bps := cfg.GlobalDownKiBps * 1024
		tcfg.DownloadRateLimiter = rate.NewLimiter(rate.Limit(bps), max(64<<10, min(bps, 1<<20)))
	}
	if cfg.GlobalUpKiBps > 0 {
		bps := cfg.GlobalUpKiBps * 1024
		tcfg.UploadRateLimiter = rate.NewLimiter(rate.Limit(bps), max(64<<10, min(bps, 1<<20)))
	}
	if cfg.EncryptionForced {
		tcfg.HeaderObfuscationPolicy.RequirePreferred = true
		tcfg.HeaderObfuscationPolicy.Preferred = true
	}
	if cfg.BindInterface != "" {
		binding, err := resolveInterfaceBinding(cfg.BindInterface)
		if err != nil {
			return nil, err
		}
		tcfg.ListenHost = binding.listenHost
		tcfg.DisableIPv4 = binding.ipv4 == nil
		tcfg.DisableIPv6 = binding.ipv6 == nil
		tcfg.TrackerDialContext = binding.dialContext
		tcfg.HTTPDialContext = binding.dialContext
		log("engine", fmt.Sprintf("network pinned to interface %s", cfg.BindInterface))
	}

	// File storage resumes completed pieces from the bytes already in
	// working_dir after the DB-backed torrent definitions are re-added.
	tcfg.DefaultStorage = storage.NewFile(cfg.WorkingDir)

	cli, err := torrent.NewClient(tcfg)
	if err != nil && cfg.ListenPort != 0 {
		// Configured port is busy (orphaned sidecar after crash, another
		// BT client on the host, …). Fall back to a kernel-assigned port
		// so the engine still comes up — users wanting deterministic LAN
		// port-forwarding can pick a free one explicitly.
		log("engine", fmt.Sprintf("listen :%d busy (%v); falling back to random port", cfg.ListenPort, err))
		tcfg.ListenPort = 0
		cli, err = torrent.NewClient(tcfg)
	}
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
	var hash metainfo.Hash
	if err := hash.FromHexString(strings.TrimSpace(hex)); err != nil {
		return nil, fmt.Errorf("invalid infohash: %w", err)
	}
	t, _ := e.cli.AddTorrentInfoHash(hash)
	return e.track(t), nil
}

// AddTorrentURL fetches a .torrent file and starts it.
func (e *Engine) AddTorrentURL(rawURL string) (*TorrentSnapshot, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("torrent_url must be an absolute http(s) URL")
	}
	httpc := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpc.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf(".torrent fetch: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (10<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 10<<20 {
		return nil, errors.New(".torrent response exceeds 10 MiB")
	}
	mi, err := metainfo.Load(bytes.NewReader(body))
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
	if ok {
		return e.Snapshot(hash)
	}

	// Kick off DownloadAll once we have info. Fire-and-forget; the
	// poll loop picks up state changes either way.
	go func() {
		select {
		case <-t.GotInfo():
			e.onMetadataReady(hash)
		case <-time.After(60 * time.Second):
			// Magnet didn't resolve in time. Engine keeps trying;
			// surface the wait as an error in snapshots.
			e.mu.Lock()
			if mt, ok := e.torrents[hash]; ok && mt.lastErr == "" {
				mt.lastErr = "info not received yet (peers / DHT may be cold)"
			}
			e.mu.Unlock()
			// Keep listening after surfacing the warning. A cold magnet
			// can receive metadata later and should recover without an
			// app restart.
			select {
			case <-t.GotInfo():
				e.onMetadataReady(hash)
			case <-t.Closed():
			}
		case <-t.Closed():
			return
		}
	}()

	return e.Snapshot(hash)
}

func (e *Engine) onMetadataReady(infohash string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	mt, ok := e.torrents[infohash]
	if !ok {
		return
	}
	mt.lastErr = ""
	if mt.paused {
		return
	}
	if !e.hasDiskCapacityLocked(mt) {
		mt.lastErr = "insufficient free disk for torrent and configured safety margin"
		mt.t.SetMaxEstablishedConns(0)
		return
	}
	if e.downloadSlotAvailableLocked(mt) {
		e.startDownloadLocked(mt)
	} else {
		mt.queued = true
		mt.t.SetMaxEstablishedConns(0)
	}
}

func (e *Engine) downloadSlotAvailableLocked(candidate *managedTorrent) bool {
	if e.cfg.MaxConcurrent <= 0 {
		return true
	}
	active := 0
	for _, mt := range e.torrents {
		if mt == candidate || mt.paused || mt.queued || mt.seedStopped || mt.t.Info() == nil {
			continue
		}
		if mt.t.BytesMissing() > 0 {
			active++
		}
	}
	return active < e.cfg.MaxConcurrent
}

func (e *Engine) startDownloadLocked(mt *managedTorrent) {
	mt.queued = false
	mt.t.SetMaxEstablishedConns(80)
	files := mt.t.Files()
	for i, f := range files {
		if p, ok := mt.priorityHint[i]; ok {
			f.SetPriority(p)
		}
	}
	mt.t.DownloadAll()
	for i, f := range files {
		if p, ok := mt.priorityHint[i]; ok {
			f.SetPriority(p)
		}
	}
}

func (e *Engine) startQueuedLocked() {
	for _, mt := range e.torrents {
		if !mt.queued || mt.paused || mt.lastErr != "" || mt.t.Info() == nil {
			continue
		}
		if !e.downloadSlotAvailableLocked(mt) {
			return
		}
		e.startDownloadLocked(mt)
	}
}

func (e *Engine) hasDiskCapacityLocked(mt *managedTorrent) bool {
	pct := e.cfg.FreeDiskSafetyPercent
	if pct <= 0 || mt.t.Info() == nil {
		return true
	}
	if pct >= 100 {
		return false
	}
	free, err := availableDiskBytes(e.cfg.WorkingDir)
	if err != nil {
		return false
	}
	missing := mt.t.BytesMissing()
	return missing <= free*int64(100-pct)/100
}

// RestoreState reapplies persisted user intent after the torrent is
// re-added on boot. It is safe before metadata arrives.
func (e *Engine) RestoreState(infohash string, paused bool, priorities map[int]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	mt, ok := e.torrents[infohash]
	if !ok {
		return
	}
	for i, value := range priorities {
		if p, err := parsePriority(value); err == nil {
			mt.priorityHint[i] = p
		}
	}
	mt.paused = paused
	if paused {
		mt.queued = false
		mt.t.SetMaxEstablishedConns(0)
		if mt.t.Info() != nil {
			for _, f := range mt.t.Files() {
				f.SetPriority(types.PiecePriorityNone)
			}
		}
	} else if mt.t.Info() != nil {
		if e.downloadSlotAvailableLocked(mt) {
			e.startDownloadLocked(mt)
		} else {
			mt.queued = true
		}
	}
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
	if mt.t.Info() != nil {
		for _, f := range mt.t.Files() {
			f.SetPriority(types.PiecePriorityNone)
		}
	}
	mt.t.SetMaxEstablishedConns(0)
	mt.paused = true
	mt.queued = false
	e.startQueuedLocked()
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
	mt.paused = false
	if mt.t.Info() == nil {
		mt.t.SetMaxEstablishedConns(80)
		return nil
	}
	if e.downloadSlotAvailableLocked(mt) {
		e.startDownloadLocked(mt)
	} else {
		mt.queued = true
	}
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
	e.startQueuedLocked()
	e.mu.Unlock()

	t.Drop()
	if deleteData {
		dataPath, err := safeChildPath(e.cfg.WorkingDir, t.Name())
		if err != nil {
			return err
		}
		if err := os.RemoveAll(dataPath); err != nil {
			return fmt.Errorf("delete torrent data: %w", err)
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
	defer e.mu.Unlock()
	mt, ok := e.torrents[infohash]
	if !ok {
		return errNotFound
	}
	if mt.t.Info() == nil {
		return errors.New("torrent metadata not available yet")
	}
	files := mt.t.Files()
	if fileIndex < 0 || fileIndex >= len(files) {
		return fmt.Errorf("file_index %d out of range (0..%d)", fileIndex, len(files)-1)
	}
	files[fileIndex].SetPriority(prio)
	mt.priorityHint[fileIndex] = prio
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

// Snapshot reads one torrent's state. nil if the infohash isn't known.
func (e *Engine) Snapshot(infohash string) *TorrentSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	mt, ok := e.torrents[infohash]
	if !ok {
		return nil
	}
	return e.snapshotLocked(mt, time.Now())
}

// SnapshotAll returns a copy of every managed torrent's state.
func (e *Engine) SnapshotAll() []TorrentSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]TorrentSnapshot, 0, len(e.torrents))
	for _, mt := range e.torrents {
		s := e.snapshotLocked(mt, time.Now())
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
	defer e.mu.Unlock()
	mt, ok := e.torrents[infohash]
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
func (e *Engine) snapshotLocked(mt *managedTorrent, now time.Time) *TorrentSnapshot {
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
	readData := stats.BytesReadUsefulData.Int64()
	writtenData := stats.BytesWrittenData.Int64()
	if !mt.lastSampleAt.IsZero() {
		seconds := now.Sub(mt.lastSampleAt).Seconds()
		if seconds >= 0.5 {
			mt.downRateBPS = int64(float64(max(int64(0), readData-mt.lastReadData)) / seconds)
			mt.upRateBPS = int64(float64(max(int64(0), writtenData-mt.lastWriteData)) / seconds)
			mt.lastSampleAt = now
			mt.lastReadData = readData
			mt.lastWriteData = writtenData
		}
	} else {
		mt.lastSampleAt = now
		mt.lastReadData = readData
		mt.lastWriteData = writtenData
	}

	state := "downloading"
	switch {
	case mt.paused:
		state = "paused"
	case mt.lastErr != "":
		state = "error"
	case !hasInfo:
		state = "queued"
	case mt.queued:
		state = "queued"
	case missing == 0 && length > 0:
		if mt.seedStopped {
			state = "completed"
		} else {
			state = "seeding"
		}
	}

	eta := int64(-1)
	if missing > 0 && mt.downRateBPS > 0 {
		eta = missing / mt.downRateBPS
	}

	return &TorrentSnapshot{
		Infohash:        mt.infohash,
		Name:            t.Name(),
		State:           state,
		Length:          length,
		BytesCompleted:  completed,
		BytesMissing:    missing,
		Progress:        progress,
		DownloadRateBPS: mt.downRateBPS,
		UploadRateBPS:   mt.upRateBPS,
		UploadedBytes:   writtenData,
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
	handler := e.onTransition
	type transition struct {
		infohash, prev, next string
		snap                 TorrentSnapshot
	}
	var transitions []transition
	now := time.Now()
	for _, mt := range e.torrents {
		if mt.lastErr == "insufficient free disk for torrent and configured safety margin" && e.hasDiskCapacityLocked(mt) {
			mt.lastErr = ""
			mt.queued = true
		}
		if mt.t.Info() != nil && mt.t.BytesMissing() == 0 {
			if mt.completedAt.IsZero() {
				mt.completedAt = now.UTC()
			}
			stats := mt.t.Stats()
			ratioReached := e.cfg.SeedRatioTarget <= 0 ||
				(mt.t.Length() > 0 && float64(stats.BytesWrittenData.Int64())/float64(mt.t.Length()) >= e.cfg.SeedRatioTarget)
			timeReached := e.cfg.SeedTimeTarget > 0 && now.Sub(mt.completedAt) >= e.cfg.SeedTimeTarget
			if !mt.seedStopped && (ratioReached || timeReached) {
				mt.t.DisallowDataUpload()
				mt.t.SetMaxEstablishedConns(0)
				mt.seedStopped = true
			}
		}
		s := e.snapshotLocked(mt, now)
		if s == nil {
			continue
		}
		prev := mt.prevState
		next := s.State
		if prev == next {
			continue
		}
		mt.prevState = next
		transitions = append(transitions, transition{mt.infohash, prev, next, *s})
	}
	e.startQueuedLocked()
	e.mu.Unlock()
	if handler != nil {
		for _, tr := range transitions {
			handler(tr.infohash, tr.prev, tr.next, tr.snap)
		}
	}
}

// AggregateStats returns a global snapshot — sums per-torrent
// counters and pulls global byte rates from the client. Used by
// torrent_stats and the panel header.
type AggregateStats struct {
	ActiveCount        int   `json:"active_count"`
	DownloadingCount   int   `json:"downloading_count"`
	SeedingCount       int   `json:"seeding_count"`
	PausedCount        int   `json:"paused_count"`
	CompletedCount     int   `json:"completed_count"`
	ErrorCount         int   `json:"error_count"`
	QueuedCount        int   `json:"queued_count"`
	TotalBytesQueued   int64 `json:"total_bytes_queued"`
	TotalBytesComplete int64 `json:"total_bytes_complete"`
	GlobalDownBPS      int64 `json:"global_down_bps"`
	GlobalUpBPS        int64 `json:"global_up_bps"`
}

func (e *Engine) AggregateStats() AggregateStats {
	return e.AggregateStatsFor(nil)
}

// AggregateStatsFor limits counters to a project's registered
// infohashes. A nil filter returns the physical engine-wide view.
func (e *Engine) AggregateStatsFor(infohashes map[string]struct{}) AggregateStats {
	out := AggregateStats{}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	for _, mt := range e.torrents {
		if infohashes != nil {
			if _, ok := infohashes[mt.infohash]; !ok {
				continue
			}
		}
		s := e.snapshotLocked(mt, now)
		if s == nil {
			continue
		}
		out.TotalBytesQueued += s.Length
		out.TotalBytesComplete += s.BytesCompleted
		out.GlobalDownBPS += s.DownloadRateBPS
		out.GlobalUpBPS += s.UploadRateBPS
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
		case "queued":
			out.QueuedCount++
		}
	}
	return out
}

func availableDiskBytes(path string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

func safeChildPath(root, child string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	candidate, err := filepath.Abs(filepath.Join(rootAbs, child))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe torrent data path %q", child)
	}
	return candidate, nil
}

var errNotFound = errors.New("torrent: infohash not tracked")
