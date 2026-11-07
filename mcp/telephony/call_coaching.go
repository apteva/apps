package main

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"sync/atomic"
	"time"
)

const coachCaptureMagic uint32 = 0x31435741  // AWC1: generation, sequence, capture clock, PCM16@24k
const coachPlaybackMagic uint32 = 0x31575041 // APW1: epoch, server source clock, PCMU@8k
const coachAudioMaxAge = 200 * time.Millisecond

// A target check proves the adviser binding for slightly longer than one 1 s
// supervisor tick. A stalled or failed check therefore silences coaching soon after.
const coachTargetCheckTTL = 1500 * time.Millisecond

type coachingGrant struct {
	Enabled                           bool
	PeerHash, OwnerHash, BrowserEpoch string
	Expires                           int64
}

type coachingStop struct {
	generation uint32
	reason     string
}

type coachingState struct {
	grant             coachingGrant
	generation        atomic.Uint32
	talking           atomic.Bool
	deadline          atomic.Int64
	received, dropped atomic.Int64
	starts            atomic.Int64
	expires           atomic.Int64
	// Unix ms until which the last full target check (taken under the call's
	// claim lock) authorizes coach audio. Zero means unverified.
	verifiedUntil atomic.Int64
}

func (c *coachingState) markTargetVerified() {
	c.verifiedUntil.Store(time.Now().Add(coachTargetCheckTTL).UnixMilli())
}
func (c *coachingState) clearTargetVerified() { c.verifiedUntil.Store(0) }
func (c *coachingState) targetVerified() bool {
	return time.Now().UnixMilli() < c.verifiedUntil.Load()
}

func (a *App) phoneCanCoach(p *phonePrincipal, row *callRow) bool {
	return a.phoneCanListen(p, row) && (p == nil || (p.Coach && p.ListenScope && p.CoachScope))
}

func (a *App) coachingGrant(row *callRow, token string) (coachingGrant, error) {
	var g coachingGrant
	err := a.db().db.QueryRow(`SELECT coaching,target_peer_hash,target_owner_hash,target_browser_epoch,expires_at FROM telephony_listener_sessions WHERE token_hash=? AND call_id=? AND project_id=?`, phoneHash(token), row.ID, row.ProjectID).Scan(&g.Enabled, &g.PeerHash, &g.OwnerHash, &g.BrowserEpoch, &g.Expires)
	return g, err
}

func (a *App) coachCapability(row *callRow) (bool, string) {
	if supported, reason := a.listenerCapability(row); !supported {
		return false, reason
	}
	if row.PeerKind != peerKindHuman {
		return false, "human_softphone_required"
	}
	if row.HoldState == "held" {
		return false, "call_held"
	}
	h := a.softphones.lookup(row.ID)
	if h == nil {
		return false, "adviser_not_connected"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.browser == nil || h.readyBrowser != h.browser || h.peer == nil || h.closed || h.held {
		return false, "adviser_not_connected"
	}
	if h.whisperBrowser != h.browser {
		return false, "adviser_client_unsupported"
	}
	return true, ""
}

// The adviser hub may already be gone; callers must not assume it still exists.
func (a *App) coachingBrowserEpoch(id string) (string, bool) {
	h := a.softphones.lookup(id)
	if h == nil {
		return "", false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.browserEpoch, true
}

func (a *App) coachingTargetValid(row *callRow, g coachingGrant) bool {
	return a.coachingTargetReason(row, g) == ""
}
func (a *App) coachingTargetReason(row *callRow, g coachingGrant) string {
	if !g.Enabled || isTerminalStatus(row.Status) || row.PeerKind != peerKindHuman || phoneHash(row.PeerToken) != g.PeerHash {
		return "coach_target_changed"
	}
	owner, _, err := a.phoneOwner(row.ID)
	if err != nil {
		return "owner_lookup_failed"
	}
	if phoneHash(owner) != g.OwnerHash {
		return "coach_target_changed"
	}
	h := a.softphones.lookup(row.ID)
	if h == nil {
		return "coach_target_changed"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed && h.browser != nil && h.readyBrowser == h.browser && h.whisperBrowser == h.browser && h.browserEpoch == g.BrowserEpoch && h.peer != nil {
		return ""
	}
	return "coach_target_changed"
}

func (h *softphoneHub) stopCoachLocked(l *callListener, reasons ...string) {
	if h.coach == nil || (l != nil && h.coach != l) {
		return
	}
	reason := "operator_stop"
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	coach := h.coach
	coach.coaching.talking.Store(false)
	select {
	case coach.coachStops <- coachingStop{coach.coaching.generation.Load(), reason}:
	default:
	}
	if h.browser != nil {
		h.browser.clearWhisper()
		h.browser.queueControl([]byte(`{"type":"coach.state","talking":false}`))
	}
	h.coach = nil
}

func (h *softphoneHub) stopCoach(l *callListener, reasons ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopCoachLocked(l, reasons...)
}

// Invalidate all credentials pinned to the previous adviser session before an
// ownership/credential change can make queued coaching reach a replacement.
func (h *softphoneHub) invalidateCoaching() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.browserEpoch = newSecret()
	h.stopCoachLocked(nil, "coach_target_changed")
}

func (h *softphoneHub) startCoach(l *callListener, generation uint32) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.held || h.browser == nil || h.whisperBrowser != h.browser || h.readyBrowser != h.browser || h.browserEpoch != l.coaching.grant.BrowserEpoch || h.peer == nil || (h.status != "answered" && h.status != "in-progress") {
		return false
	}
	if h.coach != nil && h.coach != l && h.coach.coaching.talking.Load() && time.Now().UnixMilli() < h.coach.coaching.deadline.Load() {
		return false
	}
	h.stopCoachLocked(nil, "next_talk")
	h.coachEpoch++
	state, _ := json.Marshal(map[string]any{"type": "coach.state", "talking": true, "epoch": h.coachEpoch})
	if !h.browser.queueControl(state) {
		return false
	}
	h.coach = l
	l.coaching.generation.Store(generation)
	l.coaching.deadline.Store(time.Now().Add(2 * time.Second).UnixMilli())
	l.coaching.talking.Store(true)
	l.coaching.starts.Add(1)
	return true
}

// Only the pinned adviser's browser is a possible sink. No carrier/peer writer,
// recording tap or other listener is reachable from this method.
func (h *softphoneHub) forwardCoach(l *callListener, generation uint32, pcmu []byte, sourceClock float64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.held || h.coach != l || !l.coaching.talking.Load() || l.coaching.generation.Load() != generation || time.Now().UnixMilli() >= l.coaching.deadline.Load() || h.browserEpoch != l.coaching.grant.BrowserEpoch || h.whisperBrowser != h.browser || h.browser == nil || (h.status != "answered" && h.status != "in-progress") {
		return false
	}
	select {
	case <-l.done:
		return false
	default:
	}
	epoch, browser := h.coachEpoch, h.browser
	data := make([]byte, 16+len(pcmu))
	binary.LittleEndian.PutUint32(data, coachPlaybackMagic)
	binary.LittleEndian.PutUint32(data[4:], epoch)
	binary.LittleEndian.PutUint64(data[8:], math.Float64bits(sourceClock))
	copy(data[16:], pcmu)
	return browser.queueWhisper(data, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		select {
		case <-l.done:
			return false
		default:
		}
		return h.coach == l && l.coaching.talking.Load() && h.coachEpoch == epoch && h.browser == browser && !h.held && !h.closed && time.Now().UnixMilli() < l.coaching.deadline.Load() && time.Now().Unix() < l.coaching.expires.Load()
	})
}

// Indexed target checks run on the 1 s supervisor tick and on coach.start /
// keepalive, serialized with answer/attach/takeover. The 20 ms coach media path
// only reads their cached result, so it neither queries the DB nor holds the
// claim lock. In-memory adviser binding (epoch, browser, hold) is still checked
// per frame by forwardCoach.
func (a *App) forwardCoachFrame(id string, l *callListener, generation uint32, pcmu []byte, sourceClock float64) bool {
	if !l.coaching.targetVerified() {
		return false
	}
	h := a.softphones.lookup(id)
	return h != nil && h.forwardCoach(l, generation, pcmu, sourceClock)
}
