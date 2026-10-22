package main

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/pion/webrtc/v4"
)

// This control-plane owner never runs on an RTP/audio goroutine. Only managed
// attachments start its expiry timer; providers are bounded and cancellation-aware.
type rtcRestartAttempt struct {
	revision uint64
	busy     bool
}
type rtcICESession struct {
	mu          sync.Mutex
	attempts    map[*webrtc.PeerConnection]*rtcRestartAttempt
	events      []mediaSessionEvent
	config      softphoneRTCConfig
	credentials rtcTURNCredentials
	revision    atomic.Uint64
	refresh     chan struct{}
	done        <-chan struct{}
}

// Control-plane evidence is bounded and contains no SDP, credentials or URLs.
func (s *rtcICESession) record(outcome string, revision uint64, attempt string, start time.Time, remaining int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) >= 50 {
		copy(s.events, s.events[1:])
		s.events = s.events[:49]
	}
	s.events = append(s.events, mediaSessionEvent{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Action: "ice_restart", Outcome: outcome, Code: "ice_rev_" + strconv.FormatUint(revision, 10), AttemptID: attempt, DurationMS: int(time.Since(start) / time.Millisecond), RemainingMS: max(0, min(3600000, remaining))})
}
func (s *rtcICESession) snapshot() []mediaSessionEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mediaSessionEvent(nil), s.events...)
}

func newRTCICESession(ctx context.Context, c softphoneRTCConfig, writer *websocketWriterPump, done <-chan struct{}) (*rtcICESession, error) {
	s := &rtcICESession{config: c, done: done, refresh: make(chan struct{}, 1)}
	s.revision.Store(1)
	s.credentials.ICEServers = c.ClientICEServers
	if s.credentials.ICEServers == nil {
		s.credentials.ICEServers = c.ICEServers
	}
	if c.TURN != nil {
		v, err := c.TURN.issue(ctx)
		if err != nil {
			if c.ICEPolicy == "relay" {
				raw, _ := json.Marshal(map[string]any{"type": "webrtc.error", "code": "turn_relay_unavailable", "relay_required": true})
				_ = writer.Write(ws.OpText, raw)
				return nil, errors.New("required TURN relay unavailable")
			}
			s.credentials = rtcTURNCredentials{}
		} else {
			s.credentials = v
		}
	}
	return s, nil
}
func (s *rtcICESession) initial(listener bool) []byte {
	policy := s.config.ICEPolicy
	if policy == "" {
		policy = "all"
	}
	state := "existing"
	if s.config.TURN != nil {
		state = "managed"
		if s.credentials.ExpiresAt == 0 {
			state = "unavailable"
		}
	}
	raw, _ := json.Marshal(map[string]any{"type": "webrtc.config", "ice_servers": s.credentials.ICEServers, "ice_transport_policy": policy, "ice_revision": s.revision.Load(), "ice_restart_supported": true, "turn_source": state, "expires_at": s.credentials.ExpiresAt, "relay_configured": hasTURN(s.credentials.ICEServers), "bitrate": s.config.Bitrate, "fec_requested": s.config.fecEnabled(), "listener": listener})
	return raw
}
func (s *rtcICESession) requestRefresh() {
	if s.config.TURN != nil {
		select {
		case s.refresh <- struct{}{}:
		default:
		}
	}
}
func rtcTURNRenewAfter(expires int64, now time.Time) time.Duration {
	remaining := time.Unix(expires, 0).Sub(now)
	if remaining <= 0 {
		return 15 * time.Second
	}
	margin := min(5*time.Minute, max(15*time.Second, remaining/5))
	// Refresh early with bounded staggering so a batch of answered calls does
	// not cause a credential-service burst an hour later.
	jitter := time.Duration(rand.Int64N(int64(max(time.Millisecond, margin/5))))
	return max(time.Second, remaining-margin-jitter)
}
func (s *rtcICESession) run(writer *websocketWriterPump) {
	s.runAfter(writer, rtcTURNRenewAfter(s.credentials.ExpiresAt, time.Now()))
}

func (s *rtcICESession) runAfter(writer *websocketWriterPump, delay time.Duration) {
	if s.config.TURN == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	var last time.Time
	failures := 0
	for {
		select {
		case <-s.done:
			return
		case <-timer.C:
		case <-s.refresh:
			// Repeated browser requests cannot hammer the infrastructure app.
			if time.Since(last) < 10*time.Second {
				continue
			}
		}
		last = time.Now()
		attemptID := newAudioSessionID()
		s.record("credential_refresh_started", s.revision.Load(), attemptID, last, int(time.Until(time.Unix(s.credentials.ExpiresAt, 0))/time.Millisecond))
		v, err := s.config.TURN.issue(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			failures++
			s.record("credential_refresh_unavailable", s.revision.Load(), attemptID, last, 0)
			raw, _ := json.Marshal(map[string]any{"type": "webrtc.ice_status", "state": "credential_refresh_unavailable", "attempt": min(failures, 3)})
			writer.queueControl(raw)
			timer.Reset(time.Duration(min(30, 5*failures)) * time.Second)
			continue
		}
		failures = 0
		s.credentials = v
		revision := s.revision.Add(1)
		s.record("credentials_refreshed", revision, attemptID, last, int(time.Until(time.Unix(v.ExpiresAt, 0))/time.Millisecond))
		raw, _ := json.Marshal(map[string]any{"type": "webrtc.ice_config", "ice_servers": v.ICEServers, "expires_at": v.ExpiresAt, "ice_transport_policy": s.config.ICEPolicy, "revision": revision})
		if !writer.queueControl(raw) {
			return
		}
		timer.Reset(rtcTURNRenewAfter(v.ExpiresAt, time.Now()))
	}
}

// Duplicate/stale answers cannot mutate a newer epoch. Peer owners serialize
// negotiation and check their role/generation before using this helper.
func (s *rtcICESession) answer(pc *webrtc.PeerConnection, sdp string, revision uint64) (string, error) {
	if revision == 0 || revision != s.revision.Load() {
		return "", errors.New("stale ICE revision")
	}
	return answerListenerRTC(pc, sdp, s.config.fecEnabled(), s.done)
}

// At most one negotiation per PC and three retained peer states. Control reads
// remain responsive to lease renewal, mute and coaching deadman messages.
func (s *rtcICESession) restart(pc *webrtc.PeerConnection, offer string, revision uint64, writer *websocketWriterPump, kind string, generation *uint32, valid func() bool, media ...*rtcMediaStats) {
	if revision == 0 || revision != s.revision.Load() || !valid() {
		return
	}
	s.mu.Lock()
	if s.attempts == nil {
		s.attempts = make(map[*webrtc.PeerConnection]*rtcRestartAttempt)
	}
	for peer := range s.attempts {
		if peer.ConnectionState() == webrtc.PeerConnectionStateClosed {
			delete(s.attempts, peer)
		}
	}
	attempt := s.attempts[pc]
	if attempt == nil {
		if len(s.attempts) >= 3 {
			s.mu.Unlock()
			return
		}
		attempt = &rtcRestartAttempt{}
		s.attempts[pc] = attempt
	}
	if attempt.busy || attempt.revision >= revision {
		s.mu.Unlock()
		return
	}
	attempt.busy = true
	attempt.revision = revision
	s.mu.Unlock()
	go func() {
		start := time.Now()
		attemptID := newAudioSessionID()
		s.record("restart_started", revision, attemptID, start, 0)
		release := func() {
			s.mu.Lock()
			if attempt.revision == revision {
				attempt.busy = false
			}
			s.mu.Unlock()
		}
		defer release()
		before := rtcRestartPeerProgress(pc, media...)
		answer, err := s.answer(pc, offer, revision)
		if !valid() {
			return
		}
		select {
		case <-s.done:
			return
		default:
		}
		outcome := "restart_signaled"
		if err != nil {
			outcome = "restart_failed"
		}
		s.record(outcome, revision, attemptID, start, 0)
		value := map[string]any{"type": kind, "sdp": answer, "revision": revision}
		if err != nil {
			value = map[string]any{"type": "webrtc.ice_status", "state": "restart_failed", "revision": revision}
		}
		if generation != nil {
			value["generation"] = *generation
		}
		raw, _ := json.Marshal(value)
		if !writer.queueControl(raw) || err != nil {
			return
		}
		release() // Media observation does not own the signaling negotiation slot.
		// Signaling success is not media success. Sample only while a renewal is
		// pending, outside the RTP path; do not create a permanent per-call poller.
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-timer.C:
				s.record("restart_media_timeout", revision, attemptID, start, 0)
				return
			case <-tick.C:
				if !valid() || s.revision.Load() != revision {
					return
				}
				if pc.ConnectionState() == webrtc.PeerConnectionStateConnected && rtcRestartMediaResumed(before, rtcRestartPeerProgress(pc, media...)) {
					s.record("restart_completed", revision, attemptID, start, 0)
					return
				}
			}
		}
	}()
}

type rtcRestartMediaProgress struct {
	pair           string
	sent, received uint64
	send, receive  bool
}

// Pion's generic GetStats omits selected-pair and outbound RTP counters in
// this pinned version. Reuse its selected-pair API and our existing atomics;
// installing another per-packet stats interceptor would add unnecessary work.
func rtcRestartPeerProgress(pc *webrtc.PeerConnection, media ...*rtcMediaStats) rtcRestartMediaProgress {
	pairID := ""
	if sctp := pc.SCTP(); sctp != nil && sctp.Transport() != nil {
		if pair, ok := sctp.Transport().ICETransport().GetSelectedCandidatePairStats(); ok {
			pairID = pair.ID
		}
	}
	return rtcRestartProgress(pairID, media...)
}
func rtcRestartProgress(pair string, media ...*rtcMediaStats) rtcRestartMediaProgress {
	p := rtcRestartMediaProgress{pair: pair}
	for _, stats := range media {
		if stats == nil {
			continue
		}
		attempts, failed := stats.rtp.attempts.Load(), stats.rtp.errors.Load()
		if attempts > failed {
			p.sent += attempts - failed
		}
		p.received += stats.ingress.packets.Load()
	}
	p.send, p.receive = p.sent > 0, p.received > 0
	return p
}

func rtcRestartMediaResumed(before, after rtcRestartMediaProgress) bool {
	return after.pair != "" && after.pair != before.pair && (before.send || before.receive) && (!before.send || after.sent > before.sent) && (!before.receive || after.received > before.received)
}
