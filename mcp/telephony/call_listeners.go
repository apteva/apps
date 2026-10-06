package main

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
)

const listenerFrameMagic uint32 = 0x314c5441 // ATL1, PCM16LE@24k, direction + sequence + server time
const listenerMaxAge = 200 * time.Millisecond
const listenerMaxFrameBytes = 960 // 20 ms
const listenerQueueFrames = 12    // combined directions; at most 240 ms aggregate, further bounded by age

type listenerAudioFrame struct {
	data []byte
	at   time.Time
}
type callListener struct {
	coaching   coachingState
	coachStops chan coachingStop
	hash       string
	audio      chan listenerAudioFrame
	done       chan struct{}
	once       sync.Once
	dropped    [2]atomic.Int64
	sent       [2]atomic.Int64
	stale      [2]atomic.Int64
	trimmed    [2]atomic.Int64
	maxWriteMS atomic.Int64
	mu         sync.Mutex
	reason     string
}

func (l *callListener) close(reason string) {
	l.once.Do(func() { l.mu.Lock(); l.reason = reason; l.mu.Unlock(); close(l.done) })
}
func (l *callListener) closeReason() string { l.mu.Lock(); defer l.mu.Unlock(); return l.reason }
func (l *callListener) diagnostics() map[string]any {
	return map[string]any{"sent_frames": []int64{l.sent[0].Load(), l.sent[1].Load()}, "overflow_frames": []int64{l.dropped[0].Load(), l.dropped[1].Load()}, "stale_frames": []int64{l.stale[0].Load(), l.stale[1].Load()}, "source_trimmed_frames": []int64{l.trimmed[0].Load(), l.trimmed[1].Load()}, "max_write_ms": l.maxWriteMS.Load(), "coaching": l.coaching.grant.Enabled, "coach_starts": l.coaching.starts.Load(), "coach_received_frames": l.coaching.received.Load(), "coach_dropped_frames": l.coaching.dropped.Load()}
}

type callAudioTap struct {
	mu        sync.Mutex
	listeners map[string]*callListener
	closed    bool
	sequence  [2]uint64
}

func (t *callAudioTap) hasListeners() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.closed && len(t.listeners) > 0
}
func (t *callAudioTap) add(hash string, maximum int) (*callListener, error) {
	return t.addWithCoaching(hash, maximum, coachingGrant{})
}
func (t *callAudioTap) addWithCoaching(hash string, maximum int, g coachingGrant) (*callListener, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, errors.New("media disconnected")
	}
	if _, ok := t.listeners[hash]; ok {
		return nil, errors.New("listener already connected")
	}
	if len(t.listeners) >= maximum {
		return nil, errors.New("listener limit reached")
	}
	l := &callListener{coachStops: make(chan coachingStop, 8), hash: hash, audio: make(chan listenerAudioFrame, listenerQueueFrames), done: make(chan struct{})}
	l.coaching.grant = g
	l.coaching.expires.Store(g.Expires)
	if t.listeners == nil {
		t.listeners = map[string]*callListener{}
	}
	t.listeners[hash] = l
	return l, nil
}
func (t *callAudioTap) remove(l *callListener) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.listeners[l.hash] == l {
		delete(t.listeners, l.hash)
	}
	l.close("listener_stopped")
}
func (t *callAudioTap) close(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	for _, l := range t.listeners {
		l.close(reason)
	}
}

// No socket I/O, database work or waiting on listeners occurs in the call path.
// The frame is copied once and shared immutably across independent bounded queues.
func (t *callAudioTap) publish(direction uint32, pcm []byte) {
	if direction > 1 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || len(t.listeners) == 0 {
		return
	}
	now := time.Now()
	clockMS := int64(mediaClockMS())
	// Never replay seconds of a catch-up burst to a supervisor.
	if len(pcm) > listenerMaxFrameBytes*6 {
		skipped := uint64((len(pcm) - listenerMaxFrameBytes*6 + listenerMaxFrameBytes - 1) / listenerMaxFrameBytes)
		t.sequence[direction] += skipped
		for _, l := range t.listeners {
			l.trimmed[direction].Add(int64(skipped))
		}
		pcm = pcm[len(pcm)-listenerMaxFrameBytes*6:]
	}
	offset := int64(0)
	for len(pcm) >= 2 {
		n := min(len(pcm)&^1, listenerMaxFrameBytes)
		data := make([]byte, 24+n)
		binary.LittleEndian.PutUint32(data, listenerFrameMagic)
		binary.LittleEndian.PutUint32(data[4:], direction)
		binary.LittleEndian.PutUint64(data[8:], t.sequence[direction])
		t.sequence[direction]++
		binary.LittleEndian.PutUint64(data[16:], uint64(clockMS+offset))
		copy(data[24:], pcm[:n])
		pcm = pcm[n:]
		offset += int64(n) * 1000 / 48000
		frame := listenerAudioFrame{data, now}
		for _, l := range t.listeners {
			select {
			case <-l.done:
				continue
			default:
			}
			select {
			case l.audio <- frame:
			default:
				select {
				case old := <-l.audio:
					d := binary.LittleEndian.Uint32(old.data[4:])
					l.dropped[d].Add(1)
				default:
				}
				select {
				case l.audio <- frame:
				default:
					l.dropped[direction].Add(1)
				}
			}
		}
	}
}

type callListenerRegistry struct {
	mu   sync.Mutex
	taps map[string]*callAudioTap
}

func (r *callListenerRegistry) openBridge(id string) *callAudioTap {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.taps == nil {
		r.taps = map[string]*callAudioTap{}
	}
	if old := r.taps[id]; old != nil {
		old.close("media_replaced")
	}
	t := &callAudioTap{}
	r.taps[id] = t
	return t
}
func (r *callListenerRegistry) lookup(id string) *callAudioTap {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.taps[id]
}
func (r *callListenerRegistry) closeBridge(id string, t *callAudioTap) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t.close("media_disconnected")
	if r.taps[id] == t {
		delete(r.taps, id)
	}
}
func (r *callListenerRegistry) disconnect(id, hash string) {
	if t := r.lookup(id); t != nil {
		t.mu.Lock()
		if l := t.listeners[hash]; l != nil {
			l.close("listener_stopped")
		}
		t.mu.Unlock()
	}
}
func (a *App) maxCallListeners() int {
	if globalCtx == nil {
		return 4
	}
	return int(max(1, boundedBurstSetting(globalCtx.Config(), "max_call_listeners", 4, 16)))
}
func (a *App) phoneCanListen(p *phonePrincipal, row *callRow) bool {
	return p == nil || (p.Supervisor && p.Listen && a.phoneCallAllowed(p, row, true))
}
func (a *App) listenerCapability(row *callRow) (bool, string) {
	if isTerminalStatus(row.Status) {
		return false, "call_ended"
	}
	if row.MediaStatus != "connected" || a.listeners.lookup(row.ID) == nil {
		return false, "media_not_bridged"
	}
	return true, ""
}
func (a *App) listenerMediaURL(id, token string) string {
	path := "/softphone/listen-media/" + id + "/" + token
	if a.installID > 0 {
		return fmt.Sprintf("/api/apps/telephony/_install/%d%s", a.installID, path)
	}
	return path
}
func (a *App) listenerSessionValid(row *callRow, token string) bool {
	reason, _ := a.listenerSessionCheck(row, token)
	return reason == ""
}
func (a *App) listenerSessionCheck(row *callRow, token string) (string, int64) {
	if isTerminalStatus(row.Status) {
		return "call_ended", 0
	}
	var principal, providerJSON string
	var expires int64
	var coaching bool
	err := a.db().db.QueryRow(`SELECT principal,provider_json,expires_at,coaching FROM telephony_listener_sessions WHERE token_hash=? AND call_id=? AND project_id=?`, phoneHash(token), row.ID, row.ProjectID).Scan(&principal, &providerJSON, &expires, &coaching)
	if err == sql.ErrNoRows {
		return "access_revoked", 0
	}
	if err != nil {
		return "media_session_lookup_failed", 0
	}
	if expires <= time.Now().Unix() {
		return "media_lease_expired", expires
	}
	if coaching {
		g, e := a.coachingGrant(row, token)
		if e != nil {
			if e == sql.ErrNoRows {
				return "access_revoked", expires
			}
			return "media_session_lookup_failed", expires
		}
		if reason := a.coachingTargetReason(row, g); reason != "" {
			return reason, expires
		}
	}
	if principal == "" {
		return "", expires
	}
	var identity phoneIdentity
	if json.Unmarshal([]byte(principal), &identity) != nil || !identity.valid() {
		return "access_revoked", expires
	}
	policy, err := a.phonePolicy(row.ProjectID)
	if err != nil {
		return "policy_lookup_failed", expires
	}
	p, err := phonePrincipalFromPolicy(row.ProjectID, identity, policy)
	if err != nil || !a.phoneCanListen(p, row) || (coaching && !p.Coach) {
		return "access_revoked", expires
	}
	if providerJSON != "" {
		var provider phoneAuthProvider
		if json.Unmarshal([]byte(providerJSON), &provider) != nil || (!phoneProviderStillAllows(policy, provider, "call.listen") || (coaching && !phoneProviderStillAllows(policy, provider, "call.coach"))) {
			return "access_revoked", expires
		}
	}
	return "", expires
}
func (a *App) handleListenAction(w http.ResponseWriter, r *http.Request, project, action, id string) {
	coaching := strings.HasPrefix(action, "coach")
	if coaching {
		unlock := a.softphones.lockClaim(id)
		defer unlock()
	}
	row, err := a.db().findCall(id)
	p := phoneUserFrom(r)
	if err == nil && row != nil && row.ProjectID == project && p != nil && p.Supervisor && p.Listen {
		if _, _, e := a.phoneOwner(id); e != nil {
			writeJSONStatus(w, 503, map[string]any{"code": "owner_lookup_failed"})
			return
		}
	}
	if err != nil {
		writeJSONStatus(w, 503, map[string]any{"code": "call_lookup_failed"})
		return
	}
	if row == nil || row.ProjectID != project || !a.phoneCanListen(p, row) || (coaching && !a.phoneCanCoach(p, row)) {
		http.Error(w, "call not found", 404)
		return
	}
	if action == "listen-audit" {
		rows, err := a.db().db.Query(`SELECT id,principal,joined_at,left_at,reason,diagnostics_json,mode FROM telephony_listener_audit WHERE call_id=? AND project_id=? ORDER BY joined_at DESC LIMIT 100`, id, project)
		if err != nil {
			http.Error(w, "audit unavailable", 503)
			return
		}
		defer rows.Close()
		out := make([]map[string]any, 0)
		for rows.Next() {
			var auditID, principal, joined, left, reason, diagnostics, mode string
			if rows.Scan(&auditID, &principal, &joined, &left, &reason, &diagnostics, &mode) != nil {
				http.Error(w, "audit unavailable", 503)
				return
			}
			out = append(out, map[string]any{"id": auditID, "principal": json.RawMessage(firstNonEmpty(principal, "null")), "joined_at": joined, "left_at": left, "reason": reason, "diagnostics": json.RawMessage(diagnostics), "mode": mode})
		}
		if rows.Err() != nil {
			http.Error(w, "audit unavailable", 503)
			return
		}
		rows.Close()
		talks, err := a.db().db.Query(`SELECT id,listener_audit_id,principal,started_at,ended_at,reason FROM telephony_coaching_audit WHERE call_id=? AND project_id=? ORDER BY started_at DESC LIMIT 100`, id, project)
		if err != nil {
			http.Error(w, "audit unavailable", 503)
			return
		}
		defer talks.Close()
		spurts := make([]map[string]any, 0)
		for talks.Next() {
			var aid, lid, who, start, end, reason string
			if talks.Scan(&aid, &lid, &who, &start, &end, &reason) != nil {
				http.Error(w, "audit unavailable", 503)
				return
			}
			spurts = append(spurts, map[string]any{"id": aid, "listener_audit_id": lid, "principal": json.RawMessage(firstNonEmpty(who, "null")), "started_at": start, "ended_at": end, "reason": reason})
		}
		if talks.Err() != nil {
			http.Error(w, "audit unavailable", 503)
			return
		}
		writeJSON(w, map[string]any{"listeners": out, "coaching": spurts})
		return
	}
	if action != "listen" && action != "coach" {
		var body struct {
			SessionToken string `json:"session_token"`
		}
		if decodeJSONBody(r, &body) != nil || body.SessionToken == "" {
			http.Error(w, "listener credential required", 400)
			return
		}
		grant, grantErr := a.coachingGrant(row, body.SessionToken)
		if grantErr != nil && grantErr != sql.ErrNoRows {
			writeJSONStatus(w, 503, map[string]any{"code": "media_session_lookup_failed"})
			return
		}
		if grantErr != nil || grant.Enabled != coaching {
			http.Error(w, "session mode mismatch", 403)
			return
		}
		principal := ""
		if p != nil {
			principal = p.Identity.key()
		}
		if action == "listen-stop" || action == "coach-stop" {
			res, err := a.db().db.Exec(`DELETE FROM telephony_listener_sessions WHERE token_hash=? AND call_id=? AND project_id=? AND principal=?`, phoneHash(body.SessionToken), id, project, principal)
			if err != nil {
				http.Error(w, "session unavailable", 503)
				return
			}
			n, _ := res.RowsAffected()
			if n == 1 {
				a.listeners.disconnect(id, phoneHash(body.SessionToken))
			}
			writeJSON(w, map[string]any{"ok": true})
			return
		}
		if reason, _ := a.listenerSessionCheck(row, body.SessionToken); reason != "" {
			status := http.StatusForbidden
			if temporaryMediaFailure(reason) {
				status = http.StatusServiceUnavailable
			}
			writeJSONStatus(w, status, map[string]any{"code": reason})
			return
		}
		res, err := a.db().db.Exec(`UPDATE telephony_listener_sessions SET expires_at=? WHERE token_hash=? AND call_id=? AND project_id=? AND principal=?`, time.Now().Unix()+phoneLeaseSeconds, phoneHash(body.SessionToken), id, project, principal)
		if err != nil {
			http.Error(w, "session unavailable", 503)
			return
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			http.Error(w, "session not owned", 403)
			return
		}
		if tap := a.listeners.lookup(id); tap != nil {
			tap.mu.Lock()
			if l := tap.listeners[phoneHash(body.SessionToken)]; l != nil {
				l.coaching.expires.Store(time.Now().Unix() + phoneLeaseSeconds)
			}
			tap.mu.Unlock()
		}
		writeJSON(w, map[string]any{"lease_seconds": phoneLeaseSeconds})
		return
	}
	if supported, reason := a.listenerCapability(row); !supported {
		writeJSONStatus(w, 409, map[string]any{"code": reason})
		return
	}
	grant := coachingGrant{Enabled: coaching}
	if coaching {
		if ok, reason := a.coachCapability(row); !ok {
			writeJSONStatus(w, 409, map[string]any{"code": reason})
			return
		}
		owner, _, e := a.phoneOwner(id)
		if e != nil {
			http.Error(w, "ownership unavailable", 503)
			return
		}
		grant.PeerHash, grant.OwnerHash = phoneHash(row.PeerToken), phoneHash(owner)
		h := a.softphones.lookup(id)
		h.mu.Lock()
		grant.BrowserEpoch = h.browserEpoch
		h.mu.Unlock()
	}
	principal, providerJSON := "", ""
	if p != nil {
		principal = p.Identity.key()
		if p.AuthProvider != nil {
			b, _ := json.Marshal(p.AuthProvider)
			providerJSON = string(b)
		}
	}
	token := newSecret()
	tx, err := a.db().db.Begin()
	if err != nil {
		http.Error(w, "session unavailable", 503)
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec(`DELETE FROM telephony_listener_sessions WHERE expires_at<=?`, time.Now().Unix())
	if err != nil {
		http.Error(w, "session unavailable", 503)
		return
	}
	var count int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM telephony_listener_sessions WHERE call_id=? AND project_id=?`, id, project).Scan(&count); err != nil {
		http.Error(w, "session unavailable", 503)
		return
	}
	if count >= a.maxCallListeners() {
		writeJSONStatus(w, 409, map[string]any{"code": "listener_limit"})
		return
	}
	_, err = tx.Exec(`INSERT INTO telephony_listener_sessions(token_hash,call_id,project_id,principal,provider_json,expires_at,coaching,target_peer_hash,target_owner_hash,target_browser_epoch) VALUES(?,?,?,?,?,?,?,?,?,?)`, phoneHash(token), id, project, principal, providerJSON, time.Now().Unix()+phoneLeaseSeconds, coaching, grant.PeerHash, grant.OwnerHash, grant.BrowserEpoch)
	if err != nil || tx.Commit() != nil {
		http.Error(w, "session unavailable", 503)
		return
	}
	writeJSON(w, map[string]any{"call_id": id, "media_url": a.listenerMediaURL(id, token), "session_token": token, "lease_seconds": phoneLeaseSeconds, "coaching": coaching})
}

func (a *App) handleListenMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	id, token := softphonePathParts(r.URL.Path, "/softphone/listen-media/")
	row, err := a.db().findCall(id)
	if err != nil {
		writeJSONStatus(w, 503, map[string]any{"code": "call_lookup_failed"})
		return
	}
	if id == "" || token == "" || row == nil {
		http.Error(w, "listener access denied", 403)
		return
	}
	reason, verifiedExpiry := a.listenerSessionCheck(row, token)
	if reason != "" {
		status := http.StatusForbidden
		if temporaryMediaFailure(reason) {
			status = http.StatusServiceUnavailable
		}
		writeJSONStatus(w, status, map[string]any{"code": reason})
		return
	}
	tap := a.listeners.lookup(id)
	if tap == nil {
		http.Error(w, "media disconnected", 409)
		return
	}
	grant, err := a.coachingGrant(row, token)
	if err != nil {
		writeJSONStatus(w, 503, map[string]any{"code": "media_session_lookup_failed"})
		return
	}
	l, err := tap.addWithCoaching(phoneHash(token), a.maxCallListeners(), grant)
	if err != nil {
		http.Error(w, "listener unavailable", 409)
		return
	}
	defer tap.remove(l)
	if l.coaching.grant.Enabled {
		defer func() {
			if h := a.softphones.lookup(id); h != nil {
				h.stopCoach(l)
			}
		}()
	}
	conn, readConn, err := upgradeBuffered(w, r)
	if err != nil {
		return
	}
	writer := newWebSocketWriterPump(conn, ws.StateServerSide)
	closer := newGracefulWebSocket(conn, writer)
	auditID := newSecret()
	var principal string
	_ = a.db().db.QueryRow(`SELECT principal FROM telephony_listener_sessions WHERE token_hash=?`, l.hash).Scan(&principal)
	_, err = a.db().db.Exec(`INSERT INTO telephony_listener_audit(id,call_id,project_id,principal,joined_at,mode) VALUES(?,?,?,?,?,?)`, auditID, id, row.ProjectID, principal, ringTime(time.Now()), map[bool]string{true: "coach", false: "listen"}[l.coaching.grant.Enabled])
	if err != nil {
		closer.Close(ws.StatusInternalServerError, "audit_unavailable")
		return
	}
	defer func() {
		closer.Close(ws.StatusNormalClosure, l.closeReason())
		b, _ := json.Marshal(l.diagnostics())
		_, _ = a.db().db.Exec(`UPDATE telephony_listener_audit SET left_at=?,reason=?,diagnostics_json=? WHERE id=?`, ringTime(time.Now()), l.closeReason(), string(b), auditID)
		_, _ = a.db().db.Exec(`DELETE FROM telephony_listener_sessions WHERE token_hash=?`, l.hash)
	}()
	ready, _ := json.Marshal(map[string]any{"type": "listener.ready", "sample_rate": 24000, "channels": 2, "coaching": l.coaching.grant.Enabled})
	_ = writer.Write(ws.OpText, ready)
	var auditMu sync.Mutex
	spurt := ""
	var spurtGeneration uint32
	finishSpurtFor := func(generation uint32, reason string) {
		auditMu.Lock()
		defer auditMu.Unlock()
		if spurt != "" && (generation == 0 || generation == spurtGeneration) {
			_, _ = a.db().db.Exec(`UPDATE telephony_coaching_audit SET ended_at=?,reason=? WHERE id=? AND ended_at=''`, ringTime(time.Now()), reason, spurt)
			spurt = ""
		}
	}
	finishSpurt := func(reason string) { finishSpurtFor(0, reason) }
	defer func() { finishSpurt(l.closeReason()) }()
	done := make(chan struct{})
	watcherDone := make(chan struct{})
	defer func() { close(done); <-watcherDone }()
	go func() {
		defer close(watcherDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-l.done:
				if h := a.softphones.lookup(id); h != nil {
					h.stopCoach(l)
				}
				finishSpurt(l.closeReason())
				code := ws.StatusGoingAway
				if l.closeReason() == "call_ended" {
					code = ws.StatusNormalClosure
				}
				if l.closeReason() == "access_revoked" {
					code = ws.StatusPolicyViolation
				}
				closer.Close(code, l.closeReason())
				return
			case stopped := <-l.coachStops:
				finishSpurtFor(stopped.generation, stopped.reason)
				event, _ := json.Marshal(map[string]any{"type": "coach.stopped", "generation": stopped.generation, "reason": stopped.reason})
				writer.queueControl(event)
			case <-ticker.C:
				current, e := a.db().findCall(id)
				if e != nil && verifiedExpiry > time.Now().Unix() {
					if h := a.softphones.lookup(id); h != nil {
						h.stopCoach(l, "policy_unavailable")
					}
					continue
				}
				if e != nil || current == nil {
					l.close("access_revoked")
					continue
				}
				if isTerminalStatus(current.Status) {
					l.close("call_ended")
					continue
				}
				if l.coaching.talking.Load() && time.Now().UnixMilli() >= l.coaching.deadline.Load() {
					if h := a.softphones.lookup(id); h != nil {
						h.stopCoach(l, "talk_timeout")
					}
					finishSpurt("talk_timeout")

				}
				reason, expiry := a.listenerSessionCheck(current, token)
				if reason == "" {
					verifiedExpiry = expiry
					continue
				}
				if temporaryMediaFailure(reason) && verifiedExpiry > time.Now().Unix() {
					// A coach must explicitly push to talk again after an uncertain target check.
					if h := a.softphones.lookup(id); h != nil {
						h.stopCoach(l, "policy_unavailable")
					}
					continue
				}
				if reason == "coach_target_changed" {
					l.close(reason)
				} else if reason == "media_lease_expired" {
					l.close("media_disconnected")
				} else {
					l.close("access_revoked")
				}
				continue
			case frame := <-l.audio:
				select {
				case <-l.done:
					continue
				default:
				}
				direction := binary.LittleEndian.Uint32(frame.data[4:])
				if time.Since(frame.at) > listenerMaxAge {
					l.stale[direction].Add(1)
					continue
				}
				// Recheck termination/revocation on the periodic path, never on the primary media path.
				started := time.Now()
				if err := writer.write(ws.OpBinary, frame.data, listenerMaxAge); err != nil {
					l.close("listener_network_error")
					closer.Close(ws.StatusGoingAway, l.closeReason())
					return
				}
				elapsed := time.Since(started).Milliseconds()
				for previous := l.maxWriteMS.Load(); elapsed > previous && !l.maxWriteMS.CompareAndSwap(previous, elapsed); previous = l.maxWriteMS.Load() {
				}
				l.sent[direction].Add(1)
			}
		}
	}()
	var sourceBase float64
	var sourceSet, sequenceSet bool
	var sequence uint32
	resampler := newPCMResampler(24000, 8000)
	for {
		data, op, err := readWebSocketData(readConn, ws.StateServerSide, writer)
		if err != nil {
			l.close("listener_disconnected")
			return
		}
		if l.coaching.grant.Enabled {
			if op == ws.OpText {
				var c struct {
					Type       string `json:"type"`
					Generation uint32 `json:"generation"`
				}
				if json.Unmarshal(data, &c) == nil && c.Generation > 0 {
					if c.Type == "coach.stop" {
						if c.Generation >= l.coaching.generation.Load() {
							if h := a.softphones.lookup(id); h != nil {
								h.stopCoach(l)
							}
							finishSpurt("operator_stop")
							l.coaching.generation.Store(c.Generation)
						}
						continue
					}
					if c.Type == "coach.start" || c.Type == "coach.keepalive" {
						unlock := a.softphones.lockClaim(id)
						current, e := a.db().findCall(id)
						reason := "access_revoked"
						if e != nil {
							reason = "media_session_lookup_failed"
						} else if current != nil {
							reason, _ = a.listenerSessionCheck(current, token)
						}
						if temporaryMediaFailure(reason) {
							if h := a.softphones.lookup(id); h != nil {
								h.stopCoach(l, "policy_unavailable")
							}
							unlock()
							continue
						}
						if reason != "" {
							unlock()
							l.close("access_revoked")
							return
						}
						h := a.softphones.lookup(id)
						if c.Type == "coach.keepalive" {
							if l.coaching.talking.Load() && time.Now().UnixMilli() < l.coaching.deadline.Load() && c.Generation == l.coaching.generation.Load() {
								l.coaching.deadline.Store(time.Now().Add(2 * time.Second).UnixMilli())
							}
							unlock()
							continue
						}
						if c.Generation <= l.coaching.generation.Load() {
							unlock()
							continue
						}
						finishSpurt("next_talk")
						auditMu.Lock()
						if h == nil || !h.startCoach(l, c.Generation) {
							auditMu.Unlock()
							unlock()
							writer.queueControl([]byte(`{"type":"coach.rejected","detail":"Adviser unavailable or already being coached"}`))
							continue
						}
						unlock()
						spurt = newSecret()
						spurtGeneration = c.Generation
						_, e = a.db().db.Exec(`INSERT INTO telephony_coaching_audit(id,call_id,listener_audit_id,project_id,principal,started_at) VALUES(?,?,?,?,?,?)`, spurt, id, auditID, row.ProjectID, principal, ringTime(time.Now()))
						auditMu.Unlock()
						if e != nil {
							h.stopCoach(l)
							l.close("audit_unavailable")
							return
						}
						sourceSet = false
						sequenceSet = false
						resampler = newPCMResampler(24000, 8000)
						ack, _ := json.Marshal(map[string]any{"type": "coach.started", "generation": c.Generation})
						writer.queueControl(ack)
						continue
					}
				}
			}
			if op == ws.OpBinary && len(data) >= 26 && len(data) <= 984 && len(data)%2 == 0 && binary.LittleEndian.Uint32(data) == coachCaptureMagic {
				generation := binary.LittleEndian.Uint32(data[4:])
				seq := binary.LittleEndian.Uint32(data[8:])
				clock := math.Float64frombits(binary.LittleEndian.Uint64(data[16:]))
				now := mediaClockMS()
				l.coaching.received.Add(1)
				if !l.coaching.talking.Load() || generation != l.coaching.generation.Load() || l.coaching.expires.Load() <= time.Now().Unix() || math.IsNaN(clock) || math.IsInf(clock, 0) || clock < 0 || (sequenceSet && seq <= sequence) {
					l.coaching.dropped.Add(1)
					continue
				}
				sequence = seq
				sequenceSet = true
				offset := now - clock
				if !sourceSet {
					sourceBase = offset
					sourceSet = true
				} else {
					sourceBase = math.Min(sourceBase, offset)
				}
				if now-(clock+sourceBase) > float64(coachAudioMaxAge/time.Millisecond) {
					l.coaching.dropped.Add(1)
					resampler = newPCMResampler(24000, 8000)
					continue
				}
				if !a.forwardCoachFrame(id, l, generation, pcm16ToUlaw(resampler.Process(bytesToPCM16(data[24:]))), clock+sourceBase) {
					l.coaching.dropped.Add(1)
				}
				continue
			}
		}
		// Passive credentials remain receive-only; coaching accepts no call controls.
		if op == ws.OpBinary || (op == ws.OpText && strings.TrimSpace(string(data)) != "") {
			l.close("listener_protocol_violation")
			closer.Close(ws.StatusPolicyViolation, l.closeReason())
			return
		}
	}
}

// Observe only successfully transmitted carrier audio, after pacing, stale-frame
// dropping and codec conversion. Generated but canceled AI speech is never copied.
func (t *callAudioTap) jsonOutputObserver(codec string) func([]byte) {
	resampler := carrierInputResampler(codec)
	return func(payload []byte) {
		if !t.hasListeners() {
			return
		}
		var frame struct {
			Media *struct {
				Payload string `json:"payload"`
			} `json:"media"`
		}
		if json.Unmarshal(payload, &frame) != nil || frame.Media == nil || frame.Media.Payload == "" {
			return
		}
		pcm, err := decodeCarrierPCM(frame.Media.Payload, codec)
		if err != nil {
			return
		}
		if resampler != nil {
			pcm = resampler.Process(pcm)
		}
		t.publish(1, pcm16ToBytes(pcm))
	}
}
func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// Policy writes revoke only affected listeners. No main-call sockets are touched.
func (a *App) revokeCallListeners(project string) {
	rows, err := a.db().db.Query(`SELECT s.call_id,s.token_hash,s.principal,s.provider_json,s.coaching FROM telephony_listener_sessions s WHERE s.project_id=?`, project)
	if err != nil {
		return
	}
	type grant struct {
		id, hash, principal, provider string
		coaching                      bool
	}
	var grants []grant
	for rows.Next() {
		var g grant
		if rows.Scan(&g.id, &g.hash, &g.principal, &g.provider, &g.coaching) == nil {
			grants = append(grants, g)
		}
	}
	_ = rows.Close()
	policy, err := a.phonePolicy(project)
	if err != nil {
		return
	}
	for _, g := range grants {
		if g.principal == "" {
			continue
		}
		row, e := a.db().findCall(g.id)
		if e != nil || row == nil {
			continue
		}
		var identity phoneIdentity
		_ = json.Unmarshal([]byte(g.principal), &identity)
		p, e := phonePrincipalFromPolicy(project, identity, policy)
		allowed := e == nil && a.phoneCanListen(p, row) && (!g.coaching || p.Coach)
		if allowed && g.provider != "" {
			var provider phoneAuthProvider
			allowed = json.Unmarshal([]byte(g.provider), &provider) == nil && phoneProviderStillAllows(policy, provider, "call.listen") && (!g.coaching || phoneProviderStillAllows(policy, provider, "call.coach"))
		}
		if !allowed {
			_, _ = a.db().db.Exec(`DELETE FROM telephony_listener_sessions WHERE token_hash=?`, g.hash)
			if tap := a.listeners.lookup(g.id); tap != nil {
				tap.mu.Lock()
				if l := tap.listeners[g.hash]; l != nil {
					l.close("access_revoked")
				}
				tap.mu.Unlock()
			}
		}
	}
}

func (t *callAudioTap) publishPCM(direction uint32, pcm []int16) {
	if t.hasListeners() {
		t.publish(direction, pcm16ToBytes(pcm))
	}
}
