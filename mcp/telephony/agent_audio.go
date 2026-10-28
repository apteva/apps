package main

import (
	"encoding/json"
	"errors"
	"github.com/gobwas/ws"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// This contract describes media behavior, independently of tool grants and
// model/provider names. Older Core/Server pairs omit it and retain legacy audio.
const agentAudioHeader = "X-Apteva-Audio-Session"

// Continuous sessions bound each receipt write to Core by this budget.
const agentCoreWriteBudget = 250 * time.Millisecond

type agentAudioCapabilities struct {
	Format               string `json:"audio_format,omitempty"`
	SampleRate           int    `json:"sample_rate,omitempty"`
	Channels             int    `json:"channels,omitempty"`
	Version              int    `json:"version"`
	Continuous           bool   `json:"continuous_conversation"`
	InputSpeechEvents    bool   `json:"input_speech_events"`
	ProviderInterruption bool   `json:"provider_interruption"`
	LocalInterruption    bool   `json:"local_interruption_required"`
	ScopedCancellation   bool   `json:"scoped_cancellation"`
	ReceiptEvidence      bool   `json:"receipt_evidence"`
	PlaybackFlowControl  bool   `json:"playback_flow_control"`
}

type agentPlaybackKey struct {
	Generation int64
	Item       string
}
type agentCollateralPlayback struct {
	ItemID     string `json:"item_id"`
	Generation int64  `json:"session_generation"`
}
type agentClearReceipt struct {
	CollateralPlaybacks []agentCollateralPlayback `json:"collateral_playbacks,omitempty"`
	ItemID              string                    `json:"item_id,omitempty"`
	Generation          int64                     `json:"session_generation,omitempty"`
	Scope               string                    `json:"scope"`
	LocalMS             int                       `json:"local_discarded_ms"`
	RemoteCommandSent   bool                      `json:"remote_command_sent"`
	RemoteConfirmed     bool                      `json:"remote_confirmed"`
	Collateral          []string                  `json:"collateral_item_ids,omitempty"`
	RequestedAtMS       int64                     `json:"requested_at_ms,omitempty"`
	Reason              string                    `json:"reason,omitempty"`
	// Diagnostics only: audio of this playback discarded after the receipt was sent.
	LateDiscardedMS int `json:"late_discarded_ms,omitempty"`
}

type agentAudioSnapshot struct {
	TimingTrace        []agentAudioTraceEvent        `json:"timing_trace,omitempty"`
	TraceOmitted       uint64                        `json:"trace_omitted"`
	TraceHeadFrames    int                           `json:"trace_head_frames,omitempty"`
	Playout            agentPlayoutStats             `json:"playout"`
	CoreLink           *agentCoreLinkSnapshot        `json:"core_link,omitempty"`
	PlaybackPressure   agentPlaybackPressureSnapshot `json:"playback_pressure"`
	Capabilities       agentAudioCapabilities        `json:"capabilities"`
	PlaybackCapacityMS int                           `json:"playback_capacity_ms,omitempty"`
	Profile            agentAudioProfile             `json:"profile"`
	Evidence           string                        `json:"progress_evidence"`
	LateFrames         int64                         `json:"late_cancelled_frames"`
	SilentFrames       int64                         `json:"silent_frames"`
	Clears             []agentClearReceipt           `json:"clears,omitempty"`
	ReplyTiming        agentReplyTimingSnapshot      `json:"reply_timing"`
}

type agentAudioSession struct {
	trace                    []agentAudioTraceEvent
	traceOmitted             uint64
	traceNext                int
	playout                  agentPlayoutStats
	link                     *agentCoreLink
	pressure                 agentPlaybackPressureSnapshot
	mu                       sync.Mutex
	capabilities             agentAudioCapabilities
	profile                  agentAudioProfile
	cancelled                map[agentPlaybackKey]bool
	order                    []agentPlaybackKey
	evidence                 string
	lateFrames, silentFrames int64
	clears                   []agentClearReceipt
	timing                   agentReplyTimingState
	waitingDispatch          atomic.Bool
	// Discard accounting for cleared receipts; see noteDiscardLocked.
	discardPending map[agentPlaybackKey]int
	discardReceipt map[agentPlaybackKey]bool
	discardOrder   []agentPlaybackKey
	clearDue       []realtimeBridgeControl
	frameKey       agentPlaybackKey
	frameEndMS     int
	frameMS        int
}

func agentBridgeURL(raw string, row *callRow) string {
	if row == nil || row.PeerKind == peerKindHuman || row.PeerKind == peerKindExternal {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("audio_protocol", "1")
	u.RawQuery = q.Encode()
	return u.String()
}

func (a *App) newAgentAudioSession(row *callRow, response *http.Response) (*agentAudioSession, error) {
	if row == nil || row.PeerKind == peerKindHuman || row.PeerKind == peerKindExternal {
		return nil, nil
	}
	p, err := a.pinAgentAudioProfile(row)
	if err != nil {
		return nil, err
	}
	s := &agentAudioSession{profile: p, cancelled: make(map[agentPlaybackKey]bool)}
	if response != nil {
		raw := response.Header.Get(agentAudioHeader)
		if len(raw) > 4096 {
			return nil, errors.New("invalid agent audio capability header")
		}
		if raw != "" && (json.Unmarshal([]byte(raw), &s.capabilities) != nil || s.capabilities.Version != 1) {
			return nil, errors.New("unsupported agent audio capability contract")
		}
	}
	if err := s.validateMedia(); err != nil {
		return nil, err
	}
	if (p.Mode == "continuous" || s.capabilities.Continuous) && (!s.capabilities.ScopedCancellation || !s.capabilities.ReceiptEvidence) {
		return nil, errors.New("continuous agent audio requires a compatible Core and Server")
	}
	return s, nil
}

func (s *agentAudioSession) continuous() bool {
	return s != nil && s.profile.Mode != "traditional" && (s.capabilities.Continuous || s.profile.Mode == "continuous")
}
func (s *agentAudioSession) policy() carrierPacerPolicy {
	if !s.continuous() {
		p := bufferedCarrierPacerPolicy()
		p.agentSession = s
		return p
	}
	return carrierPacerPolicy{bufferMS: s.profile.SendAheadMS, maxQueueMS: s.profile.BacklogMS, agentSession: s}
}
func (s *agentAudioSession) configureFrontend(f *carrierAudioFrontend) {
	if !s.continuous() {
		return
	}
	f.analysisOnly = s.profile.Preprocessing == "analysis_only"
	f.speechStartMS = s.profile.SpeechStartMS
	mode := s.profile.Interruption
	if mode == "local" || mode == "auto" && s.capabilities.LocalInterruption && !s.capabilities.ProviderInterruption {
		f.mode = localBargeInFallback
	} else {
		f.mode = localBargeInOff
	}
}
func (s *agentAudioSession) cancel(item string, generation int64) {
	if s == nil || item == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := agentPlaybackKey{generation, item}
	if s.cancelled[k] {
		return
	}
	s.cancelled[k] = true
	s.cancelReplyTimingLocked(item, generation)
	s.order = append(s.order, k)
	if len(s.order) > 256 {
		delete(s.cancelled, s.order[0])
		s.order = s.order[1:]
	}
}
func (s *agentAudioSession) rejects(item string, generation int64) bool {
	if s == nil || item == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelled[agentPlaybackKey{generation, item}] || s.cancelled[agentPlaybackKey{0, item}]
}
func (s *agentAudioSession) accept(f realtimeBridgeControl) bool {
	return s.acceptAt(f, time.Time{})
}

func (s *agentAudioSession) acceptAt(f realtimeBridgeControl, at time.Time) bool {
	if s == nil {
		return true
	}
	var link *agentCoreLink
	var receipt []byte
	defer func() { link.sendReceipt(receipt) }() // after unlocking
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observeFrameLocked(f)
	if s.cancelled[agentPlaybackKey{f.Generation, f.ItemID}] || s.cancelled[agentPlaybackKey{0, f.ItemID}] {
		s.lateFrames++
		link, receipt = s.link, s.noteDiscardLocked(f.ItemID, f.Generation, s.frameMS)
		return false
	}
	if f.Silence {
		s.silentFrames++
	}
	s.traceReceiveLocked(f, at)
	s.observeCoreReplyLocked(f, at)
	return true
}
func (s *agentAudioSession) ready(w *websocketWriterPump, evidence string) error {
	if s == nil || !s.capabilities.ReceiptEvidence {
		return nil
	}
	s.mu.Lock()
	s.evidence = evidence
	s.mu.Unlock()
	ready := map[string]any{"type": "playback.ready", "version": 1, "evidence": evidence}
	if capacity := s.playbackCapacityMS(); capacity > 0 {
		ready["capacity_ms"] = capacity
	}
	if s.profile.Playout.TraceFrames > 0 {
		ready["timing_trace"] = true
	}
	raw, _ := json.Marshal(ready)
	return w.Write(ws.OpText, raw)
}

// Credits bound Core's outstanding audio before it reaches our relay and pacer.
// They use the call's pinned backlog, not a new buffer or a playback delay.
// Older Core versions and traditional/human transports keep their prior path.
func (s *agentAudioSession) playbackCapacityMS() int {
	if !s.continuous() || !s.capabilities.PlaybackFlowControl || !s.capabilities.ReceiptEvidence {
		return 0
	}
	return s.profile.BacklogMS
}
func (s *agentAudioSession) progress(w *websocketWriterPump, p twilioPlaybackProgress, fallback string) error {
	evidence := fallback
	if s != nil {
		s.mu.Lock()
		if s.evidence != "" {
			evidence = s.evidence
		}
		s.mu.Unlock()
	}
	raw, _ := json.Marshal(map[string]any{"type": "playback.progress", "item_id": p.ItemID, "audio_end_ms": p.AudioEndMS, "session_generation": p.Generation, "evidence": evidence})
	return w.Write(ws.OpText, raw)
}
func (s *agentAudioSession) cleared(w *websocketWriterPump, control realtimeBridgeControl, result agentClearReceipt) error {
	if s == nil {
		return nil
	}
	result.ItemID, result.Generation, result.RequestedAtMS = control.ItemID, control.Generation, control.RequestedAtMS
	s.mu.Lock()
	result.LocalMS += s.takeDiscardsLocked(control)
	s.appendClearLocked(result)
	s.mu.Unlock()
	for _, ref := range result.collateralRefs(control.Generation) {
		s.cancel(ref.ItemID, ref.Generation)
	}
	if !s.capabilities.ReceiptEvidence {
		return nil
	}
	raw, _ := json.Marshal(struct {
		Type string `json:"type"`
		agentClearReceipt
	}{"playback.cleared", result})
	if err := w.Write(ws.OpText, raw); err != nil {
		return err
	}
	for _, ref := range result.collateralRefs(control.Generation) {
		if err := s.overflow(w, ref.ItemID, ref.Generation, "carrier_global_clear"); err != nil {
			return err
		}
	}
	return nil
}
func (s *agentAudioSession) appendClearLocked(r agentClearReceipt) {
	s.clears = append(s.clears, r)
	if len(s.clears) > 32 {
		s.clears = s.clears[len(s.clears)-32:]
	}
}

// A Core interrupt fences its playback before the renderer sees it; audio the
// link or pressure loop discards meanwhile belongs to the coming cleared receipt.
func (s *agentAudioSession) expectClear(c realtimeBridgeControl) {
	if s == nil || c.ItemID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearDue = append(s.clearDue, realtimeBridgeControl{ItemID: c.ItemID, Generation: c.Generation})
	if len(s.clearDue) > 32 {
		s.clearDue = s.clearDue[len(s.clearDue)-32:]
	}
}

// Frame duration for discard accounting: Core's per-frame duration when traced,
// otherwise the audio_end_ms advance within the playback (bounded by the backlog).
func (s *agentAudioSession) observeFrameLocked(f realtimeBridgeControl) {
	k := agentPlaybackKey{f.Generation, f.ItemID}
	ms := 0
	if f.Timing != nil && f.Timing.DurationMS > 0 {
		ms = f.Timing.DurationMS
	} else if k != s.frameKey {
		ms = f.AudioEndMS
	} else if f.AudioEndMS > s.frameEndMS {
		ms = f.AudioEndMS - s.frameEndMS
	}
	if s.profile.BacklogMS > 0 {
		ms = min(ms, s.profile.BacklogMS)
	}
	s.frameKey, s.frameMS = k, max(ms, 0)
	if f.AudioEndMS > 0 {
		s.frameEndMS = f.AudioEndMS
	}
}

// discardFrame accounts for the last accepted frame when the renderer drops it.
func (s *agentAudioSession) discardFrame(f realtimeBridgeControl) {
	if s == nil {
		return
	}
	s.mu.Lock()
	ms := 0
	if s.frameKey == (agentPlaybackKey{f.Generation, f.ItemID}) {
		ms, s.frameMS = s.frameMS, 0
	}
	raw := s.noteDiscardLocked(f.ItemID, f.Generation, ms)
	link := s.link
	s.mu.Unlock()
	link.sendReceipt(raw)
}

// linkDiscarded is the Core link's staging-drop hook; the link sends any receipt.
func (s *agentAudioSession) linkDiscarded(c realtimeBridgeControl, ms int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.noteDiscardLocked(c.ItemID, c.Generation, ms)
}

// Discarded audio is reported in the playback's cleared receipt: held until the
// receipt for a fenced interrupt is sent, recorded as late diagnostics after it,
// or sent as its own local receipt when none is due (an overflow cancelled it),
// so Core can release that playback's credit.
func (s *agentAudioSession) noteDiscardLocked(item string, generation int64, ms int) []byte {
	if item == "" || ms <= 0 {
		return nil
	}
	k := agentPlaybackKey{generation, item}
	if s.discardReceipt[k] || s.discardReceipt[agentPlaybackKey{0, item}] {
		for i := len(s.clears) - 1; i >= 0; i-- {
			if c := &s.clears[i]; c.ItemID == item && (c.Generation == generation || c.Generation == 0) {
				c.LateDiscardedMS += ms
				break
			}
		}
		return nil
	}
	for _, c := range s.clearDue {
		if matchesAgentPlayback(item, generation, c) {
			if s.discardPending == nil {
				s.discardPending = make(map[agentPlaybackKey]int)
			}
			if _, ok := s.discardPending[k]; !ok {
				s.trackDiscardLocked(k)
			}
			s.discardPending[k] += ms
			return nil
		}
	}
	s.markReceiptLocked(k)
	r := agentClearReceipt{ItemID: item, Generation: generation, Scope: "local", LocalMS: ms, Reason: "discarded_after_cancel"}
	s.appendClearLocked(r)
	if !s.capabilities.ReceiptEvidence {
		return nil
	}
	raw, _ := json.Marshal(struct {
		Type string `json:"type"`
		agentClearReceipt
	}{"playback.cleared", r})
	return raw
}

func (s *agentAudioSession) takeDiscardsLocked(control realtimeBridgeControl) int {
	total := 0
	for k, ms := range s.discardPending {
		if matchesAgentPlayback(k.Item, k.Generation, control) {
			total += ms
			delete(s.discardPending, k)
			s.markReceiptLocked(k)
		}
	}
	due := s.clearDue[:0]
	for _, c := range s.clearDue {
		if !matchesAgentPlayback(c.ItemID, c.Generation, control) {
			due = append(due, c)
		}
	}
	s.clearDue = due
	if control.ItemID != "" {
		s.markReceiptLocked(agentPlaybackKey{control.Generation, control.ItemID})
	}
	return total
}

func (s *agentAudioSession) markReceiptLocked(k agentPlaybackKey) {
	if s.discardReceipt == nil {
		s.discardReceipt = make(map[agentPlaybackKey]bool)
	}
	if !s.discardReceipt[k] {
		s.discardReceipt[k] = true
		s.trackDiscardLocked(k)
	}
}

func (s *agentAudioSession) trackDiscardLocked(k agentPlaybackKey) {
	s.discardOrder = append(s.discardOrder, k)
	if len(s.discardOrder) > 256 {
		old := s.discardOrder[0]
		s.discardOrder = s.discardOrder[1:]
		if !slices.Contains(s.discardOrder, old) {
			delete(s.discardPending, old)
			delete(s.discardReceipt, old)
		}
	}
}

func (s *agentAudioSession) overflow(w *websocketWriterPump, item string, generation int64, reason string) error {
	s.cancel(item, generation)
	raw, _ := json.Marshal(map[string]any{"type": "playback.overflow", "item_id": item, "session_generation": generation, "reason": reason})
	return w.Write(ws.OpText, raw)
}
func (s *agentAudioSession) snapshot() *agentAudioSnapshot {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var link *agentCoreLinkSnapshot
	if s.link != nil {
		v := s.link.snapshot()
		link = &v
	}
	return &agentAudioSnapshot{TimingTrace: s.traceSnapshotLocked(), TraceOmitted: s.traceOmitted, TraceHeadFrames: s.traceHeadLocked(), Playout: s.playout, CoreLink: link, PlaybackPressure: s.pressure, Capabilities: s.capabilities, PlaybackCapacityMS: s.playbackCapacityMS(), Profile: s.profile, Evidence: s.evidence, LateFrames: s.lateFrames, SilentFrames: s.silentFrames, Clears: append([]agentClearReceipt(nil), s.clears...), ReplyTiming: s.replyTimingSnapshotLocked()}
}

type agentBridgeConn struct {
	net.Conn
	response *http.Response
}

func agentBridgeResponse(c net.Conn) *http.Response {
	if c, ok := c.(agentBridgeConn); ok {
		return c.response
	}
	return nil
}

func matchesAgentPlayback(item string, generation int64, c realtimeBridgeControl) bool {
	return c.ItemID == "" || c.ItemID == item && (c.Generation == 0 || c.Generation == generation)
}

func (a *App) agentBridgeDialer(row *callRow) (ws.Dialer, *http.Response) {
	d := a.correlatedBridgeDialer(row)
	response := &http.Response{Header: make(http.Header)}
	previous := d.OnHeader
	d.OnHeader = func(key, value []byte) error {
		if strings.EqualFold(string(key), agentAudioHeader) {
			if len(value) > 4096 {
				return errors.New("agent audio capability header too large")
			}
			response.Header.Set(agentAudioHeader, string(value))
		}
		if previous != nil {
			return previous(key, value)
		}
		return nil
	}
	return d, response
}

func (a *App) trackAgentAudio(call string, s *agentAudioSession) func() {
	if s == nil {
		return func() {}
	}
	a.agentAudioSessions.Store(call, s)
	return func() { a.agentAudioSessions.CompareAndDelete(call, s) }
}
func (a *App) liveAgentAudio(call string) *agentAudioSnapshot {
	if s, ok := a.agentAudioSessions.Load(call); ok {
		return s.(*agentAudioSession).snapshot()
	}
	return nil
}

func (s *agentAudioSession) configureWriter(w *websocketWriterPump) {
	if s.continuous() {
		w.maxWriteBudget.Store(int64(agentCoreWriteBudget))
	}
}

func (r agentClearReceipt) collateralRefs(fallback int64) []agentCollateralPlayback {
	if len(r.CollateralPlaybacks) > 0 {
		return r.CollateralPlaybacks
	}
	refs := make([]agentCollateralPlayback, 0, len(r.Collateral))
	for _, id := range r.Collateral {
		refs = append(refs, agentCollateralPlayback{ItemID: id, Generation: fallback})
	}
	return refs
}
