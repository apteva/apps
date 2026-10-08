package main

// Diagnostic health is independent of call/media lifecycle and never terminates
// a carrier leg. All state is bounded and sampled outside the frame path.
import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws/wsutil"
)

const audioCorrelationWindow = 30 * time.Second
const audioHealthRecovery = 10 * time.Second

type audioPeerHasher struct {
	once  sync.Once
	key   [32]byte
	epoch string
}

func (p *audioPeerHasher) hash(r *http.Request, proxies string) (string, string, string) {
	p.once.Do(func() {
		if _, err := rand.Read(p.key[:]); err != nil {
			return
		}
		sum := sha256.Sum256(p.key[:])
		p.epoch = hex.EncodeToString(sum[:8])
	})
	if p.epoch == "" {
		return "", "", "unavailable"
	}
	addr, source := audioPeerAddress(r, proxies)
	if !addr.IsValid() {
		return "", p.epoch, "unavailable"
	}
	mac := hmac.New(sha256.New, p.key[:])
	mac.Write([]byte(addr.String()))
	return hex.EncodeToString(mac.Sum(nil)[:16]), p.epoch, source
}
func audioPeerAddress(r *http.Request, proxies string) (netip.Addr, string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, "unavailable"
	}
	peer = peer.Unmap()
	trusted := func(ip netip.Addr) bool {
		for _, raw := range strings.Split(proxies, ",") {
			if p, e := netip.ParsePrefix(strings.TrimSpace(raw)); e == nil && p.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !trusted(peer) {
		return peer, "socket_peer"
	}
	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if len(chain) > 16 {
		return peer, "socket_peer"
	}
	addresses := make([]netip.Addr, len(chain))
	for i, v := range chain {
		a, e := netip.ParseAddr(strings.TrimSpace(v))
		if e != nil {
			return peer, "socket_peer"
		}
		addresses[i] = a.Unmap()
	}
	for i := len(addresses) - 1; i >= 0 && trusted(peer); i-- {
		peer = addresses[i]
	}
	return peer, "trusted_forwarded_peer"
}

type audioSocketEvent struct {
	At              string        `json:"at"`
	ConnectionID    string        `json:"connection_id"`
	Action          string        `json:"action"`
	Reason          string        `json:"reason,omitempty"`
	Code            int           `json:"close_code,omitempty"`
	PeerHash        string        `json:"peer_address_hash,omitempty"`
	HashEpoch       string        `json:"peer_hash_epoch,omitempty"`
	AddressSource   string        `json:"address_source,omitempty"`
	AdviserIdentity phoneIdentity `json:"adviser_identity"`
	IdentitySource  string        `json:"identity_source,omitempty"`
	Classification  string        `json:"network_classification,omitempty"`
}
type audioSocketSnapshot struct {
	ConnectionID     string             `json:"connection_id,omitempty"`
	Connections      int64              `json:"connections"`
	Reconnects       int64              `json:"reconnects"`
	Disconnects      int64              `json:"disconnects"`
	PeerHash         string             `json:"peer_address_hash,omitempty"`
	HashEpoch        string             `json:"peer_hash_epoch,omitempty"`
	AddressSource    string             `json:"address_source,omitempty"`
	Events           []audioSocketEvent `json:"events,omitempty"`
	BrowserTotals    map[string]float64 `json:"browser_totals,omitempty"`
	MaxRTTMS         float64            `json:"max_rtt_ms"`
	MaxBufferedBytes int                `json:"max_buffered_bytes"`
}
type audioHealthStage struct {
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	Signal    string `json:"signal,omitempty"`
	LastBadAt string `json:"last_bad_at,omitempty"`
}
type audioHealthEvent struct {
	At     string `json:"at"`
	Stage  string `json:"stage"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	Signal string `json:"signal,omitempty"`
}
type audioHealthSnapshot struct {
	State  string                      `json:"state"`
	Reason string                      `json:"reason,omitempty"`
	Stages map[string]audioHealthStage `json:"stages"`
	Events []audioHealthEvent          `json:"events,omitempty"`
}
type audioHealthObservation struct {
	Stage, Signal string
	Bad, Active   bool
	Counter       float64
}
type audioCallTelemetry struct {
	persistMu      sync.Mutex
	dirty          bool
	browserSeen    bool
	healthWriter   *websocketWriterPump
	mu             sync.Mutex
	restored       bool
	socket         audioSocketSnapshot
	sockets        map[*websocketWriterPump]audioNetworkEvent
	collectNetwork func(audioNetworkEvent)
	clientEpoch    string
	clientCounters map[string]float64
	browser        browserAudioDiagnostics
	health         audioHealthSnapshot
	counters       map[string]float64
	lastBad        map[string]time.Time
}

func (t *audioCallTelemetry) restore(raw string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.restored {
		return
	}
	t.restored = true
	var prior browserAudioDiagnostics
	if json.Unmarshal([]byte(raw), &prior) == nil && prior.Server != nil {
		t.socket = prior.Server.Socket
		t.health = prior.Server.Health
		t.clientEpoch = prior.ClientEpoch
		t.clientCounters = audioBrowserCounters(prior)
	}
	if t.socket.BrowserTotals == nil {
		t.socket.BrowserTotals = map[string]float64{}
	}
}
func (t *audioCallTelemetry) event(e audioSocketEvent) {
	t.socket.Events = append(t.socket.Events, e)
	if len(t.socket.Events) > 64 {
		t.socket.Events = t.socket.Events[len(t.socket.Events)-64:]
	}
}
func (t *audioCallTelemetry) opened(w *websocketWriterPump, hash, epoch, source string) {
	t.openedWithNetwork(w, hash, epoch, source, audioNetworkEvent{}, nil)
}
func (t *audioCallTelemetry) openedWithNetwork(w *websocketWriterPump, hash, epoch, source string, network audioNetworkEvent, collect func(audioNetworkEvent)) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sockets == nil {
		t.sockets = map[*websocketWriterPump]audioNetworkEvent{}
	}
	t.socket.Connections++
	if t.socket.Connections > 1 {
		t.socket.Reconnects++
	}
	t.socket.PeerHash, t.socket.HashEpoch, t.socket.AddressSource = hash, epoch, source
	id := newAudioConnectionID()
	network.ConnectionID = id
	network.ID = id + ":connected"
	network.OccurredAt = time.Now().UTC().Format(time.RFC3339Nano)
	network.ExpiresAt = time.Now().UTC().Add(network.Retention).Format(time.RFC3339Nano)
	network.Event, network.Action = "softphone.browser.connected", "attached"
	if t.socket.Connections > 1 {
		network.Action = "reconnected"
	}
	t.sockets[w] = network
	t.socket.ConnectionID = id
	t.collectNetwork = collect
	if collect != nil {
		collect(network)
	}
	t.dirty = true
	t.event(audioSocketEvent{At: time.Now().UTC().Format(time.RFC3339Nano), ConnectionID: id, Action: "attached", PeerHash: hash, HashEpoch: epoch, AddressSource: source, AdviserIdentity: network.AdviserIdentity, IdentitySource: network.IdentitySource, Classification: network.Classification})
	// New browser diagnostics cannot be inferred from the previous tab/worker.
	t.browser = browserAudioDiagnostics{}
	t.browserSeen = false
	return id
}
func (t *audioCallTelemetry) closed(w *websocketWriterPump, reason string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	network, ok := t.sockets[w]
	if !ok {
		return
	}
	delete(t.sockets, w)
	code := 0
	var closed wsutil.ClosedError
	if errors.As(err, &closed) {
		code = int(closed.Code)
		if reason == "" || reason == "transport_read_error" {
			reason = "peer_close"
		}
	} else if err != nil {
		reason = "transport_read_error"
	}
	t.socket.Disconnects++
	reason = audioNetworkCloseReason(reason)
	at := time.Now().UTC()
	t.event(audioSocketEvent{At: at.Format(time.RFC3339Nano), ConnectionID: network.ConnectionID, Action: "detached", Reason: reason, Code: code, AdviserIdentity: network.AdviserIdentity, IdentitySource: network.IdentitySource, Classification: network.Classification})
	if t.socket.ConnectionID == network.ConnectionID {
		t.socket.ConnectionID = ""
	}
	network.ID = network.ConnectionID + ":disconnected"
	network.OccurredAt = at.Format(time.RFC3339Nano)
	network.ExpiresAt = at.Add(network.Retention).Format(time.RFC3339Nano)
	network.Event, network.Action, network.Reason, network.CloseCode = "softphone.browser.disconnected", "detached", reason, code
	if reason == "session_replaced" {
		network.Action = "replaced"
	}
	if t.collectNetwork != nil {
		t.collectNetwork(network)
	}
	t.dirty = true
}
func positiveDelta(current, previous float64) float64 {
	if current < previous {
		return current
	}
	return current - previous
}
func (t *audioCallTelemetry) observeBrowser(v browserAudioDiagnostics) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.observeBrowserLocked(v)
}
func (t *audioCallTelemetry) observeBrowserConnection(w *websocketWriterPump, v browserAudioDiagnostics) {
	t.mu.Lock()
	defer t.mu.Unlock()
	network, ok := t.sockets[w]
	if !ok || network.ConnectionID != t.socket.ConnectionID {
		return
	}
	v.ConnectionID = network.ConnectionID
	for i := range v.DropEvents {
		v.DropEvents[i].ConnectionID = t.connectionAtLocked(v.DropEvents[i].Timestamp)
	}
	for i := range v.SessionEvents {
		v.SessionEvents[i].ConnectionID = t.connectionAtLocked(v.SessionEvents[i].Timestamp)
	}
	if v.Timing != nil {
		for i := range v.Timing.Transport.RTTSamples {
			v.Timing.Transport.RTTSamples[i].ConnectionID = t.connectionAtLocked(v.Timing.Transport.RTTSamples[i].At)
		}
	}
	t.observeBrowserLocked(v)
}

// Delayed browser reports may contain samples from a previous socket. Attribute
// timestamped samples only to a recorded interval; never guess from latest owner.
func (t *audioCallTelemetry) connectionAtLocked(timestamp string) string {
	at, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return ""
	}
	if at.After(time.Now().Add(time.Second)) {
		return ""
	}
	for i := len(t.socket.Events) - 1; i >= 0; i-- {
		e := t.socket.Events[i]
		when, err := time.Parse(time.RFC3339Nano, e.At)
		if err != nil || when.After(at) {
			continue
		}
		if e.Action == "attached" {
			return e.ConnectionID
		}
		// A replaced socket's late detach must not end the newer socket interval.
		for j := i - 1; j >= 0; j-- {
			prior := t.socket.Events[j]
			if prior.Action == "attached" {
				if prior.ConnectionID != e.ConnectionID {
					return prior.ConnectionID
				}
				break
			}
		}
		return ""
	}
	return ""
}
func (t *audioCallTelemetry) observeBrowserLocked(v browserAudioDiagnostics) {
	t.browser = v
	t.browserSeen = true
	t.dirty = true
	if v.RTTMS != nil {
		t.socket.MaxRTTMS = max(t.socket.MaxRTTMS, float64(*v.RTTMS))
	}
	t.socket.MaxBufferedBytes = max(t.socket.MaxBufferedBytes, v.WebSocketBufferedBytes)
	if v.Timing == nil {
		return
	}
	tr := v.Timing.Transport
	t.socket.MaxRTTMS = max(t.socket.MaxRTTMS, tr.RTTMaxMS)
	t.socket.MaxBufferedBytes = max(t.socket.MaxBufferedBytes, int(tr.WebSocketMaxBufferedBytes))
	values := audioBrowserCounters(v)
	if t.clientEpoch != v.ClientEpoch {
		t.clientEpoch = v.ClientEpoch
		t.clientCounters = nil
	}
	if t.socket.BrowserTotals == nil {
		t.socket.BrowserTotals = map[string]float64{}
	}
	for k, n := range values {
		t.socket.BrowserTotals[k] += positiveDelta(n, t.clientCounters[k])
	}
	t.clientCounters = values
}
func (t *audioCallTelemetry) snapshots() (audioSocketSnapshot, audioHealthSnapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, h := t.socket, t.health
	s.Events = append([]audioSocketEvent(nil), s.Events...)
	s.BrowserTotals = map[string]float64{}
	for k, v := range t.socket.BrowserTotals {
		s.BrowserTotals[k] = v
	}
	h.Events = append([]audioHealthEvent(nil), h.Events...)
	h.Stages = map[string]audioHealthStage{}
	for k, v := range t.health.Stages {
		h.Stages[k] = v
	}
	return s, h
}
func (t *audioCallTelemetry) observeHealth(now time.Time, observations []audioHealthObservation) []audioHealthEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.health.Stages == nil {
		t.health.Stages = map[string]audioHealthStage{}
	}
	if t.counters == nil {
		t.counters = map[string]float64{}
		t.lastBad = map[string]time.Time{}
	}
	changes := []audioHealthEvent{}
	t.health.State = "healthy"
	t.health.Reason = ""
	for _, o := range observations {
		prev, known := t.counters[o.Stage]
		t.counters[o.Stage] = o.Counter
		bad := o.Active && (o.Bad || (known && o.Counter > prev) || (known && o.Counter < prev && o.Counter > 0))
		if bad {
			t.lastBad[o.Stage] = now
		}
		if !o.Active {
			delete(t.lastBad, o.Stage)
		}
		s := t.health.Stages[o.Stage]
		previous := s.State
		s.State = "inactive"
		s.Reason = ""
		s.Signal = ""
		if o.Active {
			s.State = "healthy"
			if at := t.lastBad[o.Stage]; !at.IsZero() && now.Sub(at) < audioHealthRecovery {
				s.State = "audio_degraded"
				s.Reason = "audio_degraded"
				s.Signal = o.Signal
				s.LastBadAt = at.UTC().Format(time.RFC3339Nano)
			}
		}
		if s.State != previous && (previous != "" || s.State == "audio_degraded") {
			e := audioHealthEvent{At: now.UTC().Format(time.RFC3339Nano), Stage: o.Stage, State: s.State, Reason: s.Reason, Signal: s.Signal}
			changes = append(changes, e)
			t.health.Events = append(t.health.Events, e)
		}
		t.health.Stages[o.Stage] = s
		if s.State == "audio_degraded" {
			t.health.State = "audio_degraded"
			t.health.Reason = "audio_degraded"
		}
	}
	if len(t.health.Events) > 64 {
		t.health.Events = t.health.Events[len(t.health.Events)-64:]
	}
	return changes
}

// Rolling, bounded correlation counts distinct calls, never audio frames or
// historical maxima. Alerts are scoped by project, provider and pipeline stage.
type audioAlertKey struct{ Project, Provider, Stage string }
type audioAlertGroup struct {
	calls     map[string]time.Time
	active    bool
	lastAlert time.Time
}
type audioAlertCorrelator struct {
	mu     sync.Mutex
	groups map[audioAlertKey]*audioAlertGroup
}
type audioAlert struct {
	ProjectID     string   `json:"project_id"`
	Provider      string   `json:"provider"`
	Stage         string   `json:"stage"`
	State         string   `json:"state"`
	WindowSeconds int      `json:"window_seconds"`
	CallCount     int      `json:"call_count"`
	CallIDs       []string `json:"call_ids"`
	At            string   `json:"occurred_at"`
}

func (a *audioAlertCorrelator) observe(project, provider, stage, call string, bad bool, now time.Time, minimum int, cooldown time.Duration) *audioAlert {
	a.mu.Lock()
	defer a.mu.Unlock()
	if minimum < 2 {
		return nil
	}
	if a.groups == nil {
		a.groups = map[audioAlertKey]*audioAlertGroup{}
	}
	// Only inspect this group here; the scheduled worker prunes other groups.
	key := audioAlertKey{project, provider, stage}
	g := a.groups[key]
	if g == nil {
		if !bad || len(a.groups) >= 1024 {
			return nil
		}
		g = &audioAlertGroup{calls: map[string]time.Time{}}
		a.groups[key] = g
	}
	for id, at := range g.calls {
		if now.Sub(at) >= audioCorrelationWindow {
			delete(g.calls, id)
		}
	}
	if bad {
		if len(g.calls) < 256 || !g.calls[call].IsZero() {
			g.calls[call] = now
		}
	}
	count := len(g.calls)
	state := ""
	if count >= minimum && !g.active && (g.lastAlert.IsZero() || now.Sub(g.lastAlert) >= cooldown) {
		g.active = true
		g.lastAlert = now
		state = "audio_degraded"
	}
	if count < minimum && g.active {
		g.active = false
		state = "recovered"
	}
	if state == "" {
		return nil
	}
	ids := make([]string, 0, count)
	for id := range g.calls {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 10 {
		ids = ids[:10]
	}
	return &audioAlert{project, provider, stage, state, 30, count, ids, now.UTC().Format(time.RFC3339Nano)}
}

func (a *App) sampleAudioHealth(row *callRow, h *softphoneHub, w *websocketWriterPump, now time.Time) {
	if h.readyBrowserWriter() != w {
		return
	}
	s := h.serverAudioSnapshot()
	h.mu.Lock()
	active := !h.held && !isTerminalStatus(h.status) && (h.status == "answered" || h.status == "in-progress")
	h.mu.Unlock()
	h.telemetry.mu.Lock()
	b := h.telemetry.browser
	h.telemetry.mu.Unlock()
	browserActive := active && b.ConnectionState == "connected" && b.AudioContextState == "running"
	h.telemetry.mu.Lock()
	playback, capture := h.telemetry.socket.BrowserTotals["playback_latency_discard_ms"], h.telemetry.socket.BrowserTotals["capture_latency_discard_ms"]
	h.telemetry.mu.Unlock()

	observations := []audioHealthObservation{
		{"carrier_to_telephony", "carrier_delivery_gap_or_age", s.Reception.Stalled, active, float64(s.Reception.Stalls+s.Reception.GapsOverBudget) + s.Reception.StaleDroppedMS},
		{"telephony_to_browser", "playback_delivery_over_budget", false, active, playback + float64(s.ToBrowser.StaleBytes+s.ToBrowser.SourceStaleBytes+s.ToBrowser.OverflowBytes)/48},
		{"browser_to_telephony", "capture_delivery_over_budget", false, browserActive && !b.MicrophoneMuted && !b.MicrophoneDeviceMuted, capture + float64(s.CaptureStaleBytes)/48},
	}
	if b.MediaTransport == "webrtc" {
		observations[1].Counter += float64(s.WebRTC.OutboundDroppedMS + s.WebRTC.PacingSkippedMS)
		observations[2].Counter += float64(s.WebRTC.IngressRejectedPackets + s.WebRTC.IngressQueueDrops + s.WebRTC.DecodeErrors)
		if b.WebRTC != nil {
			observations[1].Bad = b.WebRTC.JitterBufferMS > 320
			observations[1].Counter += b.WebRTC.PacketsLost + b.WebRTC.PacketsDiscarded
		}
	}
	changes := h.telemetry.observeHealth(now, observations)
	h.telemetry.mu.Lock()
	first := h.telemetry.healthWriter != w
	h.telemetry.healthWriter = w
	h.telemetry.mu.Unlock()
	if first || len(changes) > 0 {
		_, health := h.telemetry.snapshots()
		data, _ := json.Marshal(map[string]any{"type": "audio.health", "state": health.State, "reason": health.Reason, "stages": health.Stages})
		w.queueControl(data)
	}
	ctx := globalCtx
	if ctx == nil {
		return
	}
	ctx = ctx.WithProject(row.ProjectID)
	for _, e := range changes {
		topic := "telephony.audio.recovered"
		if e.State == "audio_degraded" {
			topic = "telephony.audio.degraded"
		}
		ctx.Emit(topic, map[string]any{"call_id": row.ID, "provider": row.CarrierSlug, "stage": e.Stage, "state": e.State, "reason": e.Reason, "signal": e.Signal, "occurred_at": e.At})
	}
	config := ctx.Config()
	minimum := durationSetting(config, "audio_alert_min_calls", 3, 0, 256)
	if minimum == 1 {
		minimum = 3
	}
	cooldown := time.Duration(durationSetting(config, "audio_alert_cooldown_seconds", 120, 30, 3600)) * time.Second
	// Only fresh observations count in the rolling window, not the 10s recovery hold.
	h.telemetry.mu.Lock()
	recent := map[string]time.Time{}
	for k, v := range h.telemetry.lastBad {
		recent[k] = v
	}
	h.telemetry.mu.Unlock()
	for _, o := range observations {
		bad := o.Active && recent[o.Stage].Equal(now)
		if alert := a.audioAlerts.observe(row.ProjectID, row.CarrierSlug, o.Stage, row.ID, bad, now, minimum, cooldown); alert != nil {
			topic := "telephony.audio.alert"
			if alert.State == "recovered" {
				topic = "telephony.audio.alert_recovered"
			}
			ctx.Logger().Warn("correlated audio health", "stage", alert.Stage, "state", alert.State, "calls", alert.CallCount)
			if err := a.db().saveAudioDashboardAlert(*alert); err != nil {
				ctx.Logger().Warn("audio alert history write failed", "error", err)
			}
			ctx.Emit(topic, alert)
		}
	}
}

// Scheduled recovery also expires alerts after all affected calls have ended.
func (a *audioAlertCorrelator) expire(now time.Time) []audioAlert {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []audioAlert
	for key, g := range a.groups {
		for id, at := range g.calls {
			if now.Sub(at) >= audioCorrelationWindow {
				delete(g.calls, id)
			}
		}
		if g.active && len(g.calls) == 0 {
			g.active = false
			out = append(out, audioAlert{ProjectID: key.Project, Provider: key.Provider, Stage: key.Stage, State: "recovered", WindowSeconds: 30, At: now.UTC().Format(time.RFC3339Nano)})
		}
		if !g.active && len(g.calls) == 0 && now.Sub(g.lastAlert) >= time.Hour {
			delete(a.groups, key)
		}
	}
	return out
}
func (a *App) runAudioTelemetryTick(c context.Context, ctx *sdk.AppCtx) error {
	if changed, err := a.audioNetworks.flush(c, a.db(), time.Now()); err != nil {
		ctx.Logger().Warn("browser network telemetry write failed", "error", err)
	} else {
		for project, ids := range changed {
			ctx.WithProject(project).Emit("telephony.audio.reports.changed", map[string]any{"call_ids": ids, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano)})
		}
	}
	if dropped := a.audioNetworks.dropped.Swap(0); dropped > 0 {
		ctx.Logger().Warn("browser network telemetry collection overflow", "events_dropped", dropped)
	}
	for _, alert := range a.audioAlerts.expire(time.Now()) {
		if err := a.db().saveAudioDashboardAlert(alert); err != nil {
			ctx.Logger().Warn("audio alert history write failed", "error", err)
		}
		ctx.WithProject(alert.ProjectID).Emit("telephony.audio.alert_recovered", alert)
	}
	return a.db().refreshAudioDashboardAndNotify(c, func(changed map[string][]string) {
		for project, ids := range changed {
			ctx.WithProject(project).Emit("telephony.audio.reports.changed", map[string]any{"call_ids": ids, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano)})
		}
	})
}

func audioBrowserCounters(v browserAudioDiagnostics) map[string]float64 {
	if v.Timing == nil {
		return nil
	}
	tr, rt := v.Timing.Transport, v.Timing.Runtime
	return map[string]float64{"reconnect_attempts": tr.ReconnectAttempts, "reconnect_successes": tr.ReconnectSuccesses, "worker_pause_count": tr.WorkerPauseCount, "main_thread_pause_count": rt.MainThreadPauseCount, "audio_context_suspend_count": rt.AudioContextSuspendCount, "audio_context_suspended_ms": rt.AudioContextSuspendedMS,
		"playback_transport_dropped_ms": tr.PlaybackTransportDroppedMS, "playback_source_dropped_ms": tr.PlaybackSourceDroppedMS, "capture_worker_dropped_ms": tr.CaptureDroppedMS, "capture_muted_ms": tr.CaptureMutedMS, "capture_muted_frames": tr.CaptureMutedFrames, "playback_worklet_dropped_ms": float64(v.PlaybackDroppedMS), "playback_transport_sequence_gaps": tr.PlaybackSequenceGaps, "playback_worklet_sequence_gaps": float64(v.PlaybackSequenceGaps),
		"playback_latency_discard_ms": tr.DropTotalsMS["playback_transport_age"] + tr.DropTotalsMS["playback_delivery_excess"] + tr.DropTotalsMS["playback_source_age"] + v.Timing.Playback.DropTotalsMS["playback_hard_limit"] + v.Timing.Playback.DropTotalsMS["playback_age_limit"],
		"capture_latency_discard_ms":  tr.DropTotalsMS["capture_age_limit"] + tr.DropTotalsMS["websocket_backpressure"]}
}

// Persist off the socket read path. The latest browser report is coalesced;
// cumulative counters and server disconnect events survive browser recovery.
func (a *App) persistAudioTelemetry(callID string, h *softphoneHub) (err error) {
	h.telemetry.persistMu.Lock()
	defer h.telemetry.persistMu.Unlock()
	h.telemetry.mu.Lock()
	browser, seen := h.telemetry.browser, h.telemetry.browserSeen
	h.telemetry.dirty = false
	h.telemetry.mu.Unlock()
	defer func() {
		if err != nil {
			h.telemetry.mu.Lock()
			h.telemetry.dirty = true
			h.telemetry.mu.Unlock()
		}
	}()
	server := h.serverAudioSnapshot()
	if !seen {
		return a.db().updateServerAudioDiagnostics(callID, server)
	}
	browser.Server = &server
	return a.db().updateBrowserAudioDiagnostics(callID, browser)
}
func (t *audioCallTelemetry) pending() bool { t.mu.Lock(); defer t.mu.Unlock(); return t.dirty }
