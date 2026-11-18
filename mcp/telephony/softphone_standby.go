package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// A ring-time connection ("standby") lets a browser that is being offered a
// call open its WebRTC audio connection while the call rings, so the answer
// only has to claim the call. It carries no call audio while it waits: the
// claim promotes it with the call's new media grant, the browser attaches its
// microphone and says so ("standby.armed"), and only then is the carrier
// answered, exactly as after a connection opened by the answer.
// standbyArmTimeout bounds the wait for the browser's microphone after the
// claim. A manual pick-up may still be on the browser's permission prompt.
// A variable so tests can shorten it.
var standbyArmTimeout = 60 * time.Second

const (
	standbyMaxLifetime  = 60 * time.Second
	standbyPerPrincipal = 4
	standbyPerCall      = 8
	standbyTotal        = 64
)

const (
	standbyIssued int32 = iota
	standbyConnecting
	standbyParked
	standbyClaiming
	standbyPromoted
	standbyEnded
)

var errStandbyLimit = errors.New("standby limit reached")

type softphoneStandby struct {
	id, callID, projectID, principal, destination string
	identity                                      phoneIdentity
	tokenHash                                     string
	expires                                       time.Time
	state                                         atomic.Int32
	mediaGrant                                    atomic.Pointer[string]
	promoted                                      chan string
	ended                                         chan struct{}
	endOnce                                       sync.Once
}

// waiting is true until the standby ends or is promoted.
func (s *softphoneStandby) waiting() bool {
	state := s.state.Load()
	return state != standbyEnded && state != standbyPromoted
}

// mediaToken is the claim's media grant once the standby was promoted.
func (s *softphoneStandby) mediaToken() string {
	if token := s.mediaGrant.Load(); token != nil {
		return *token
	}
	return ""
}

type softphoneStandbyRegistry struct {
	mu   sync.Mutex
	byID map[string]*softphoneStandby
}

// issue registers s, replacing the same principal's earlier standby for the
// call and keeping each principal, call and the install within bounds.
func (r *softphoneStandbyRegistry) issue(s *softphoneStandby) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID == nil {
		r.byID = map[string]*softphoneStandby{}
	}
	now := time.Now()
	perCall, perPrincipal := 0, 0
	var oldest *softphoneStandby
	for _, other := range r.byID {
		if !other.waiting() || now.After(other.expires) {
			r.endLocked(other)
			continue
		}
		// Trusted operators have no user identity to tell their browsers apart:
		// they are bounded per call and per install only.
		if s.principal != "" && other.callID == s.callID && other.principal == s.principal && other.state.Load() != standbyClaiming {
			r.endLocked(other)
			continue
		}
		if other.callID == s.callID {
			perCall++
		}
		if s.principal != "" && other.principal == s.principal && other.projectID == s.projectID {
			perPrincipal++
			if oldest == nil || other.expires.Before(oldest.expires) {
				oldest = other
			}
		}
	}
	if perPrincipal >= standbyPerPrincipal && oldest != nil && oldest.state.Load() != standbyClaiming {
		r.endLocked(oldest)
		perPrincipal--
	}
	if perCall >= standbyPerCall || perPrincipal >= standbyPerPrincipal || len(r.byID) >= standbyTotal {
		return errStandbyLimit
	}
	s.promoted = make(chan string, 1)
	s.ended = make(chan struct{})
	r.byID[s.id] = s
	return nil
}

// connect hands a standby token's connection to the media handler, once.
func (r *softphoneStandbyRegistry) connect(callID, token string) *softphoneStandby {
	hash := phoneHash(token)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.byID {
		if s.callID == callID && secureEqual(s.tokenHash, hash) {
			if time.Now().Before(s.expires) && s.state.CompareAndSwap(standbyIssued, standbyConnecting) {
				return s
			}
			return nil
		}
	}
	return nil
}

// reserve marks a parked standby as being claimed so it is not ended while
// the claim runs. It returns nil when the standby is not parked.
func (r *softphoneStandbyRegistry) reserve(callID, id, principal string) *softphoneStandby {
	if id == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.byID[id]
	if s == nil || s.callID != callID || s.principal != principal || !s.state.CompareAndSwap(standbyParked, standbyClaiming) {
		return nil
	}
	return s
}

// promote hands the claim's media grant to the parked connection.
func (r *softphoneStandbyRegistry) promote(s *softphoneStandby, token string) bool {
	if s == nil || s.state.Load() != standbyClaiming {
		return false
	}
	// The grant is visible before the state says promoted.
	s.mediaGrant.Store(&token)
	if !s.state.CompareAndSwap(standbyClaiming, standbyPromoted) {
		return false
	}
	s.promoted <- token
	r.mu.Lock()
	delete(r.byID, s.id)
	r.mu.Unlock()
	return true
}

// release returns a standby whose claim failed to waiting.
func (r *softphoneStandbyRegistry) release(s *softphoneStandby) {
	if s != nil {
		s.state.CompareAndSwap(standbyClaiming, standbyParked)
	}
}

// endIfWaiting ends a standby that is not being claimed or promoted.
func (r *softphoneStandbyRegistry) endIfWaiting(s *softphoneStandby) bool {
	for {
		state := s.state.Load()
		if state == standbyClaiming || state == standbyPromoted {
			return false
		}
		if state == standbyEnded {
			return true
		}
		if s.state.CompareAndSwap(state, standbyEnded) {
			r.mu.Lock()
			r.endLocked(s)
			r.mu.Unlock()
			return true
		}
	}
}

// endCall ends every other waiting standby for a call that was just claimed.
func (r *softphoneStandbyRegistry) endCall(callID string, except *softphoneStandby) {
	r.mu.Lock()
	var ending []*softphoneStandby
	for _, s := range r.byID {
		if s.callID == callID && s != except {
			ending = append(ending, s)
		}
	}
	r.mu.Unlock()
	for _, s := range ending {
		r.endIfWaiting(s)
	}
}

func (r *softphoneStandbyRegistry) endLocked(s *softphoneStandby) {
	for {
		state := s.state.Load()
		if state == standbyEnded || state == standbyClaiming || state == standbyPromoted {
			break
		}
		if s.state.CompareAndSwap(state, standbyEnded) {
			break
		}
	}
	if s.state.Load() == standbyEnded {
		delete(r.byID, s.id)
		s.endOnce.Do(func() { close(s.ended) })
	}
}

// softphoneStandbyRequest issues a ring-time connection for a call that is
// currently offered to this browser.
func (a *App) softphoneStandbyRequest(w http.ResponseWriter, r *http.Request, project, callID string) {
	var body struct {
		DestinationID string `json:"destination_id"`
	}
	if r.ContentLength != 0 && decodeJSONBody(r, &body) != nil {
		http.Error(w, "invalid standby request", http.StatusBadRequest)
		return
	}
	callID = strings.Trim(callID, "/")
	if callID == "" || strings.Contains(callID, "/") {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	row, err := a.db().findCall(callID)
	if err != nil {
		http.Error(w, "call lookup failed", http.StatusServiceUnavailable)
		return
	}
	if row == nil || row.ProjectID != project {
		http.Error(w, "unknown call_id", http.StatusNotFound)
		return
	}
	if row.Status != "pending" || row.Direction != "inbound" {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"code": "not_offered"})
		return
	}
	offers, err := a.db().activeRingOffers(row.ID, project)
	if err != nil {
		http.Error(w, "load ring offers", http.StatusServiceUnavailable)
		return
	}
	s := &softphoneStandby{id: newSecret(), callID: row.ID, projectID: project, destination: body.DestinationID}
	if p := phoneUserFrom(r); p != nil {
		dest := a.phoneOfferDestination(p, row, body.DestinationID)
		if dest == "" {
			writeJSONStatus(w, http.StatusConflict, map[string]any{"code": "not_offered"})
			return
		}
		s.destination, s.identity, s.principal = dest, p.Identity, p.Identity.key()
	} else if row.PeerKind != peerKindHuman && !ringHasBrowser(offers) {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"code": "not_offered"})
		return
	}
	config := map[string]string{}
	if globalCtx != nil {
		config = globalCtx.WithProject(project).Config()
	}
	if rtc, err := parseSoftphoneRTCConfig(config); err != nil || !rtc.Enabled {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"code": "webrtc_unavailable"})
		return
	}
	if config["softphone_webrtc_standby"] == "false" {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"code": "standby_disabled"})
		return
	}
	// The standby ends with the ring-group offer it was opened for; a direct
	// destination has no offer expiry.
	s.expires = time.Now().Add(standbyMaxLifetime)
	for _, offer := range offers {
		if offer.Kind != "browser" || s.destination != "" && offer.DestinationID != s.destination {
			continue
		}
		if at, e := time.Parse(time.RFC3339Nano, offer.ExpiresAt); e == nil && at.Before(s.expires) {
			s.expires = at
		}
	}
	token := newSecret()
	s.tokenHash = phoneHash(token)
	if err := a.softphoneStandbys.issue(s); err != nil {
		writeJSONStatus(w, http.StatusTooManyRequests, map[string]any{"code": "standby_limit"})
		return
	}
	writeJSON(w, map[string]any{"standby_id": s.id, "call_id": row.ID, "media_url": a.softphoneMediaURL(row.ID, token), "expires_in_ms": time.Until(s.expires).Milliseconds()})
}

// standbyReader reads what the browser sends over a parked connection and
// discards it, until the browser says its microphone is attached. Nothing is
// forwarded: the call is not bridged before the claim.
type standbyReader struct {
	armed  chan struct{}
	closed chan struct{}
}

func readStandby(conn net.Conn) *standbyReader {
	reader := &standbyReader{armed: make(chan struct{}), closed: make(chan struct{})}
	go func() {
		for {
			data, op, err := wsutil.ReadClientData(conn)
			if err != nil {
				close(reader.closed)
				return
			}
			if op != ws.OpText || !strings.Contains(string(data), `"standby.armed"`) {
				continue
			}
			var message struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &message) == nil && message.Type == "standby.armed" {
				close(reader.armed)
				return
			}
		}
	}()
	return reader
}

// parkSoftphoneStandby holds a ring-time connection until the claim promotes
// it and the browser is armed. It returns the claim's media grant, or false
// when the offer ended, the browser left or the browser never armed.
func (a *App) parkSoftphoneStandby(s *softphoneStandby, conn net.Conn) (string, bool) {
	reader := readStandby(conn)
	if !s.state.CompareAndSwap(standbyConnecting, standbyParked) {
		return "", false
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var token string
	closed, gone := reader.closed, false
wait:
	for {
		select {
		case token = <-s.promoted:
			break wait
		case <-s.ended:
			return "", false
		case <-closed:
			if a.softphoneStandbys.endIfWaiting(s) {
				return "", false
			}
			// A claim is using this standby. If that claim fails, the standby is
			// parked again and ends on the next tick.
			closed, gone = nil, true
		case now := <-ticker.C:
			if gone || now.After(s.expires) || !a.standbyCallPending(s.callID) {
				if a.softphoneStandbys.endIfWaiting(s) {
					return "", false
				}
			}
		}
	}
	timer := time.NewTimer(standbyArmTimeout)
	defer timer.Stop()
	select {
	case <-reader.armed:
		return token, true
	case <-reader.closed:
		// The browser connects again with the claim's grant, or releases it.
		return "", false
	case <-timer.C:
	}
	// The browser is connected but never attached its microphone: give the call
	// back, as its own release would. Only an unanswered claim is reset.
	unlock := a.softphones.lockClaim(s.callID)
	defer unlock()
	if row, err := a.db().findCall(s.callID); err == nil && row != nil && row.PeerToken != "" && a.validPhoneMedia(row, token) {
		_ = a.db().resetAnswerClaim(s.callID, row.PeerToken)
	}
	return "", false
}

// standbyCallPending checks the call on the read pool: a waiting standby must
// not take turns on the connection that writes calls.
func (a *App) standbyCallPending(callID string) bool {
	db := (*sql.DB)(nil)
	if globalCtx != nil {
		db = globalCtx.AppReadDB()
	}
	if db == nil {
		return true
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM calls WHERE id=?`, callID).Scan(&status); err != nil {
		return !errors.Is(err, sql.ErrNoRows)
	}
	return status == "pending"
}
