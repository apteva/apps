package main

// Browser softphone.
//
// Every carrier bridge in this app (bridge_twilio.go, bridge_carriers.go,
// sip_rtp.go) resolves its far side through a.mediaBridgeURL(row), dials that
// WebSocket, and speaks one small symmetric protocol:
//
//	telephony -> peer   OpBinary  PCM16LE @ 24 kHz mono   (caller audio)
//	                    OpText    input.speech_started | playback.progress |
//	                              playback.overflow
//	peer -> telephony   OpBinary  PCM16LE @ 24 kHz mono   (audio to the caller)
//	                    OpText    audio.frame | interrupt
//
// Nothing in those loops knows the peer is a realtime model. So a human call is
// simply a call whose mediaBridgeURL points back at this app: the bridge dials
// /peer/<call_id>/<secret> here, an operator's browser attaches to
// /softphone/media/<call_id>/<token>, and this file joins the two. No bridge
// loop is modified.
//
// The hub is keyed by call id and created by whichever side arrives first —
// normally the browser, which attaches at dial time so the operator is already
// listening when the callee picks up. The hub deliberately OUTLIVES the browser
// session: a tab reload leaves the carrier leg up (the caller hears silence)
// and re-attaching resumes audio.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws"
)

// softphoneHub joins one carrier-facing peer socket to one operator browser
// socket. Both sides are optional at any instant; audio arriving for an
// unattached side is dropped rather than buffered, because late audio on a
// phone call is worse than no audio.
type softphoneHub struct {
	callID string

	mu                  sync.Mutex
	peer                *websocketWriterPump
	browser             *websocketWriterPump
	direction           string
	status              string
	preAnswerMicSamples int64
	captureSequenceSet  bool
	captureExpected     uint32
	captureSequenceGaps int
	captureDropEvents   []audioDropEvent
	closed              bool
}

const softphoneAudioFrameMagic uint32 = 0x31545041

func decodeSoftphoneAudioFrame(data []byte) (payload []byte, sequence uint32, framed bool) {
	if len(data) < 16 || binary.LittleEndian.Uint32(data[:4]) != softphoneAudioFrameMagic {
		return data, 0, false
	}
	return data[16:], binary.LittleEndian.Uint32(data[4:8]), true
}

func (h *softphoneHub) observeCaptureFrame(sequence uint32) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.captureSequenceSet && sequence > h.captureExpected {
		gap := int(sequence - h.captureExpected)
		h.captureSequenceGaps += gap
		h.captureDropEvents = append(h.captureDropEvents, audioDropEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Direction: "operator_to_carrier",
			Reason: "capture_sequence_gap", DurationMS: gap * 20, Sequence: uint64(h.captureExpected),
		})
		if len(h.captureDropEvents) > 100 {
			h.captureDropEvents = h.captureDropEvents[len(h.captureDropEvents)-100:]
		}
	}
	h.captureExpected = sequence + 1
	h.captureSequenceSet = true
}

func (h *softphoneHub) captureDiagnostics() (int, []audioDropEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.captureSequenceGaps, append([]audioDropEvent(nil), h.captureDropEvents...)
}

func (h *softphoneHub) setCallState(direction, status string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.direction = firstNonEmpty(direction, h.direction)
	if status != "" {
		h.status = status
	}
}

func (h *softphoneHub) microphoneReady() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	// The outbound carrier stream can exist while the remote phone is still
	// ringing. Do not let those early microphone frames enter a carrier playback
	// queue. Inbound audio is also held until the carrier answer succeeds.
	return h.status == "answered" || h.status == "in-progress"
}

func (h *softphoneHub) dropPreAnswerMicrophone(data []byte) {
	h.mu.Lock()
	h.preAnswerMicSamples += int64(len(data) / 2)
	h.mu.Unlock()
}

func (h *softphoneHub) preAnswerDroppedMS() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.preAnswerMicSamples * 1000 / 24000
}

func (h *softphoneHub) setPeer(w *websocketWriterPump) (replaced *websocketWriterPump) {
	h.mu.Lock()
	defer h.mu.Unlock()
	replaced, h.peer = h.peer, w
	return replaced
}

func (h *softphoneHub) setBrowser(w *websocketWriterPump) (replaced *websocketWriterPump) {
	h.mu.Lock()
	defer h.mu.Unlock()
	replaced, h.browser = h.browser, w
	return replaced
}

// clearPeer / clearBrowser only detach the side they own. A stale goroutine
// finishing after a reconnect must not unhook the newer socket, so both compare
// identity before clearing.
func (h *softphoneHub) clearPeer(w *websocketWriterPump) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.peer == w {
		h.peer = nil
		return true
	}
	return false
}

func (h *softphoneHub) clearBrowser(w *websocketWriterPump) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.browser == w {
		h.browser = nil
	}
}

func (h *softphoneHub) peerWriter() *websocketWriterPump {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.peer
}

func (h *softphoneHub) browserWriter() *websocketWriterPump {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.browser
}

// toBrowser forwards caller audio. A missing or wedged browser is not an error
// for the call — the carrier leg stays up regardless.
func (h *softphoneHub) toBrowser(op ws.OpCode, data []byte) {
	if w := h.browserWriter(); w != nil {
		_ = w.Write(op, data)
	}
}

// toPeer forwards operator audio toward the carrier bridge.
func (h *softphoneHub) toPeer(op ws.OpCode, data []byte) {
	if w := h.peerWriter(); w != nil {
		_ = w.Write(op, data)
	}
}

type softphoneRegistry struct {
	mu   sync.Mutex
	hubs map[string]*softphoneHub
}

// hubFor is get-or-create: either the browser or the carrier bridge may arrive
// first, and both need the same hub instance.
func (r *softphoneRegistry) hubFor(callID string) *softphoneHub {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hubs == nil {
		r.hubs = map[string]*softphoneHub{}
	}
	if hub, ok := r.hubs[callID]; ok {
		return hub
	}
	hub := &softphoneHub{callID: callID}
	r.hubs[callID] = hub
	return hub
}

func (r *softphoneRegistry) lookup(callID string) *softphoneHub {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hubs[callID]
}

func (r *softphoneRegistry) updateCallState(callID, direction, status string) {
	r.mu.Lock()
	hub := r.hubs[callID]
	r.mu.Unlock()
	if hub != nil {
		hub.setCallState(direction, status)
	}
}

// dropIfEmpty removes only the hub the caller observed and only after both
// legs detached. Keeping a one-sided hub is what lets either the carrier or the
// browser reconnect after a transient network failure without losing the
// other live socket.
func (r *softphoneRegistry) dropIfEmpty(callID string, target *softphoneHub) {
	r.mu.Lock()
	defer r.mu.Unlock()
	hub, ok := r.hubs[callID]
	if !ok || hub != target {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.peer != nil || hub.browser != nil {
		return
	}
	hub.closed = true
	delete(r.hubs, callID)
}

// ─── loopback addressing ───────────────────────────────────────────

// softphoneListenPort mirrors app-sdk's own port resolution (run.go): the
// platform-injected APTEVA_APP_PORT wins, otherwise the manifest's runtime.port,
// otherwise the 8080 dev default. Getting this wrong would make the carrier
// bridge dial a closed port, so it reads the same env the SDK binds on rather
// than assuming 8080.
func softphoneListenPort() int {
	if v := strings.TrimSpace(os.Getenv("APTEVA_APP_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 8080
}

// peerLoopbackURL is what mediaBridgeURL hands the carrier bridge for a human
// call. Loopback only: the sidecar binds 127.0.0.1 unless explicitly opted out,
// and the per-call secret guards the route either way.
func (a *App) peerLoopbackURL(row *callRow) string {
	if row == nil {
		return ""
	}
	return fmt.Sprintf("ws://127.0.0.1:%d/peer/%s/%s",
		softphoneListenPort(), row.ID, firstNonEmpty(row.PeerToken, row.CallbackSecret))
}

// ─── /peer/<call_id>/<secret> — the carrier bridge dials this ──────

func (a *App) handlePeerSocket(w http.ResponseWriter, r *http.Request) {
	callID, token := softphonePathParts(r.URL.Path, "/peer/")
	if callID == "" || token == "" {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	row, err := a.db().findCall(callID)
	if err != nil || row == nil {
		http.Error(w, "unknown call_id", http.StatusNotFound)
		return
	}
	if row.PeerKind != peerKindHuman {
		http.Error(w, "call is not a softphone call", http.StatusConflict)
		return
	}
	if !secureEqual(token, firstNonEmpty(row.PeerToken, row.CallbackSecret)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if isTerminalStatus(row.Status) {
		http.Error(w, "call has ended", http.StatusGone)
		return
	}

	conn, readConn, err := upgradeBuffered(w, r)
	if err != nil {
		logSoftphone("peer ws upgrade failed", "call", callID, "err", err)
		return
	}
	writer := newWebSocketWriterPump(conn, ws.StateServerSide)
	closer := newGracefulWebSocket(conn, writer)
	hub := a.softphones.hubFor(callID)
	hub.setCallState(row.Direction, row.Status)
	// A fresh carrier bridge owns the peer side immediately. Stop a superseded
	// socket so it cannot keep delivering stale audio after a fast reconnect.
	if previous := hub.setPeer(writer); previous != nil {
		previous.Stop()
	}
	logSoftphone("softphone peer attached", "call", callID)
	if browser := hub.browserWriter(); browser != nil {
		_ = browser.Write(ws.OpText, softphoneEvent("peer.connected", callID))
	}

	defer func() {
		wasCurrent := hub.clearPeer(writer)
		closer.Close(ws.StatusNormalClosure, "softphone peer closed")
		// A peer socket can disappear because the carrier network blipped or a
		// blue-green app handoff moved the bridge. Keep the browser attached and
		// let the replacement peer rejoin this hub. The durable call status poll
		// remains the authority for whether the call actually ended.
		if wasCurrent {
			if browser := hub.browserWriter(); browser != nil {
				_ = browser.Write(ws.OpText, softphoneEvent("peer.disconnected", callID))
			}
		}
		a.softphones.dropIfEmpty(callID, hub)
		logSoftphone("softphone peer detached", "call", callID)
	}()

	for {
		data, op, err := readWebSocketData(readConn, ws.StateServerSide, writer)
		if err != nil {
			return
		}
		switch op {
		case ws.OpBinary:
			// Caller audio, PCM16LE @ 24 kHz — straight through.
			hub.toBrowser(ws.OpBinary, data)
		case ws.OpText:
			// input.speech_started / playback.progress / playback.overflow all
			// exist to pace synthesized speech. A human peer has none, so they
			// are consumed here rather than forwarded to the browser.
			continue
		}
	}
}

// ─── /softphone/media/<call_id>/<token> — the browser connects here ─

func (a *App) handleSoftphoneMedia(w http.ResponseWriter, r *http.Request) {
	callID, token := softphonePathParts(r.URL.Path, "/softphone/media/")
	if callID == "" || token == "" {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	row, err := a.db().findCall(callID)
	if err != nil || row == nil {
		http.Error(w, "unknown call_id", http.StatusNotFound)
		return
	}
	if row.PeerKind != peerKindHuman {
		http.Error(w, "call is not a softphone call", http.StatusConflict)
		return
	}
	if row.PeerToken == "" || !secureEqual(token, row.PeerToken) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if isTerminalStatus(row.Status) {
		http.Error(w, "call has ended", http.StatusGone)
		return
	}

	conn, readConn, err := upgradeBuffered(w, r)
	if err != nil {
		logSoftphone("browser ws upgrade failed", "call", callID, "err", err)
		return
	}
	writer := newWebSocketWriterPump(conn, ws.StateServerSide)
	closer := newGracefulWebSocket(conn, writer)
	hub := a.softphones.hubFor(callID)
	hub.setCallState(row.Direction, row.Status)
	// A reconnecting tab replaces the previous socket. Close the old one so a
	// stale session cannot keep injecting audio into a live call.
	if previous := hub.setBrowser(writer); previous != nil {
		_ = previous.Write(ws.OpText, softphoneEvent("session.replaced", ""))
		previous.Stop()
	}
	// A browser can reconnect while the carrier peer remains attached. Mirror
	// handlePeerSocket's notification so the client can safely reopen its
	// microphone gate without waiting for another carrier reconnect.
	if hub.peerWriter() != nil {
		_ = writer.Write(ws.OpText, softphoneEvent("peer.connected", callID))
	}
	logSoftphone("softphone browser attached", "call", callID)
	// The browser opens this socket only after getUserMedia succeeds. For an
	// inbound human call, this is therefore the first safe point to answer the
	// carrier leg: answering in /softphone/answer would connect the caller while
	// the operator was still deciding whether to grant microphone access.
	if row.Direction == "inbound" && row.Status == "answering" {
		if globalCtx == nil {
			_ = a.db().resetAnswerClaim(callID)
			_ = writer.Write(ws.OpText, softphoneEventDetail("call.error", callID, "Telephony is not ready to answer calls."))
			return
		}
		ctx := globalCtx.WithProject(row.ProjectID)
		if err := a.answerInboundCarrierCall(ctx, row); err != nil {
			_ = a.db().resetAnswerClaim(callID)
			logSoftphone("softphone carrier answer failed", "call", callID, "provider", row.CarrierSlug, "err", err)
			_ = writer.Write(ws.OpText, softphoneEventDetail("call.error", callID, "The carrier could not answer the call."))
			return
		}
		if err := a.db().updateStatus(callID, "answered", ""); err != nil {
			logSoftphone("softphone answered status failed", "call", callID, "err", err)
			_ = writer.Write(ws.OpText, softphoneEventDetail("call.error", callID, "The call connected, but Telephony could not save its status."))
			return
		}
		row.Status = "answered"
		hub.setCallState(row.Direction, row.Status)
	}
	_ = writer.Write(ws.OpText, softphoneEvent("ready", callID))

	defer func() {
		hub.clearBrowser(writer)
		closer.Close(ws.StatusNormalClosure, "softphone browser closed")
		a.softphones.dropIfEmpty(callID, hub)
		logSoftphone("softphone browser detached", "call", callID)
	}()

	for {
		data, op, err := readWebSocketData(readConn, ws.StateServerSide, writer)
		if err != nil {
			return
		}
		switch op {
		case ws.OpBinary:
			// Operator microphone audio, PCM16LE @ 24 kHz.
			payload, sequence, framed := decodeSoftphoneAudioFrame(data)
			if framed {
				hub.observeCaptureFrame(sequence)
			}
			if len(payload) == 0 {
				continue
			}
			if hub.microphoneReady() {
				hub.toPeer(ws.OpBinary, payload)
			} else {
				hub.dropPreAnswerMicrophone(payload)
			}
		case ws.OpText:
			var control struct {
				Type        string                   `json:"type"`
				Nonce       float64                  `json:"nonce,omitempty"`
				Diagnostics *browserAudioDiagnostics `json:"diagnostics,omitempty"`
			}
			if json.Unmarshal(data, &control) != nil {
				continue
			}
			switch control.Type {
			case "ping":
				pong, _ := json.Marshal(map[string]any{"type": "pong", "nonce": control.Nonce})
				_ = writer.Write(ws.OpText, pong)
			case "interrupt":
				payload, _ := json.Marshal(realtimeBridgeControl{Type: "interrupt", Source: "operator"})
				hub.toPeer(ws.OpText, payload)
			case "diagnostics":
				if control.Diagnostics != nil {
					if err := a.db().updateBrowserAudioDiagnostics(callID, *control.Diagnostics); err != nil {
						logSoftphone("persist browser audio diagnostics failed", "call", callID, "err", err)
					}
				}
			}
		}
	}
}

// hijackedConn pairs a hijacked connection with the buffered reader the HTTP
// server returns alongside it at upgrade time.
//
// A client that sends WebSocket frames immediately after its handshake can have
// those first bytes already sitting in that buffer by the time UpgradeHTTP
// returns. Reading straight from the raw net.Conn skips them and resumes
// mid-frame, which surfaces as "use of reserved op code" on the next read. The
// browser softphone hits this routinely because the operator's audio starts
// flowing the instant the socket opens.
type hijackedConn struct {
	net.Conn
	reader io.Reader
}

func (c hijackedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// upgradeBuffered performs the WebSocket upgrade and returns a connection whose
// reads start from the buffered data rather than from the socket.
func upgradeBuffered(w http.ResponseWriter, r *http.Request) (net.Conn, net.Conn, error) {
	conn, brw, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		return nil, nil, err
	}
	// Writes and Close stay on the raw conn; only reads go through the buffer.
	readConn := net.Conn(conn)
	if brw != nil && brw.Reader != nil {
		readConn = hijackedConn{Conn: conn, reader: brw.Reader}
	}
	return conn, readConn, nil
}

func softphoneEvent(kind, callID string) []byte {
	return softphoneEventDetail(kind, callID, "")
}

func softphoneEventDetail(kind, callID, detail string) []byte {
	payload := map[string]string{"type": kind, "call_id": callID}
	if detail != "" {
		payload["detail"] = detail
	}
	encoded, _ := json.Marshal(payload)
	return encoded
}

// softphonePathParts splits "<prefix><call_id>/<token>" into its two segments.
func softphonePathParts(path, prefix string) (callID, token string) {
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if rest == "" {
		return "", ""
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func logSoftphone(msg string, args ...any) {
	if globalCtx != nil && globalCtx.Logger() != nil {
		globalCtx.Logger().Info(msg, args...)
	}
}

// ─── /softphone/... — panel actions ────────────────────────────────

func (a *App) handleSoftphoneAction(w http.ResponseWriter, r *http.Request) {
	// /softphone/media/ has its own NoAuth route; a request reaching this
	// authenticated handler with that prefix means the mux matched the shorter
	// pattern, so reject rather than treating it as an action.
	if strings.HasPrefix(r.URL.Path, "/softphone/media/") {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app context unavailable", http.StatusServiceUnavailable)
		return
	}
	project, err := a.panelProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	action := strings.Trim(strings.TrimPrefix(r.URL.Path, "/softphone/"), "/")
	switch {
	case action == "place":
		a.softphonePlace(w, r, project)
	case strings.HasPrefix(action, "answer/"):
		a.softphoneAnswer(w, r, project, strings.TrimPrefix(action, "answer/"))
	case strings.HasPrefix(action, "release/"):
		a.softphoneReleaseAnswer(w, r, project, strings.TrimPrefix(action, "release/"))
	default:
		http.NotFound(w, r)
	}
}

type softphoneSession struct {
	CallID       string `json:"call_id"`
	MediaURL     string `json:"media_url"`
	SessionToken string `json:"session_token,omitempty"`
	To           string `json:"to,omitempty"`
	From         string `json:"from,omitempty"`
}

// softphoneMediaURL is the install-scoped path the operator's browser dials.
// Keep it relative: the panel may be served through a local or private Apteva
// host while PublicURL names the internet-facing carrier webhook host. The
// browser must attach through the same gateway that served the panel, not jump
// to PublicURL (which could be a different installation entirely).
func (a *App) softphoneMediaURL(callID, token string) string {
	path := "/softphone/media/" + callID + "/" + token
	if a.installID > 0 {
		return fmt.Sprintf("/api/apps/telephony/_install/%d%s", a.installID, path)
	}
	return path
}

func (a *App) softphonePlace(w http.ResponseWriter, r *http.Request, project string) {
	var body struct {
		To         string `json:"to"`
		From       string `json:"from"`
		Recording  *bool  `json:"recording"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	to := strings.TrimSpace(body.To)
	if !validE164(to) {
		http.Error(w, "to must be a valid E.164 number (+ followed by 8-15 digits)", http.StatusBadRequest)
		return
	}
	ctx := globalCtx.WithProject(project)
	session, err := a.placeHumanCall(ctx, project, to, strings.TrimSpace(body.From), body.TimeoutSec, body.Recording)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, session)
}

func (a *App) softphoneAnswer(w http.ResponseWriter, r *http.Request, project, callID string) {
	callID = strings.Trim(callID, "/")
	if callID == "" || strings.Contains(callID, "/") {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	row, err := a.db().findCall(callID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if row == nil || row.ProjectID != project {
		http.Error(w, "unknown call_id", http.StatusNotFound)
		return
	}
	if row.PeerKind != peerKindHuman {
		http.Error(w, "call is routed to an agent, not the softphone", http.StatusConflict)
		return
	}
	// Already answered by this or another operator — hand back the live session
	// so a reloading tab can rejoin instead of erroring.
	if row.Status == "answering" || row.Status == "answered" || row.Status == "in-progress" {
		if row.PeerToken == "" {
			http.Error(w, "call is answered but has no operator session", http.StatusConflict)
			return
		}
		writeJSON(w, softphoneSession{
			CallID:       row.ID,
			MediaURL:     a.softphoneMediaURL(row.ID, row.PeerToken),
			SessionToken: row.PeerToken,
			To:           row.ToNumber,
			From:         row.FromNumber,
		})
		return
	}
	if row.Status != "pending" {
		http.Error(w, "call is not available to answer (status="+row.Status+")", http.StatusConflict)
		return
	}

	claimed, err := a.db().claimPendingCallForHuman(callID, project)
	if err != nil {
		http.Error(w, "claim pending call: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !claimed {
		http.Error(w, "call was already answered", http.StatusConflict)
		return
	}
	peerToken := newSecret()
	row.PeerToken = peerToken
	if err := a.db().attachHumanCall(callID, a.peerLoopbackURL(row), peerToken); err != nil {
		_ = a.db().releaseAnswerClaim(callID)
		http.Error(w, "persist call answer: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, softphoneSession{
		CallID:       callID,
		MediaURL:     a.softphoneMediaURL(callID, peerToken),
		SessionToken: peerToken,
		To:           row.ToNumber,
		From:         row.FromNumber,
	})
}

// softphoneReleaseAnswer makes a browser-side setup failure retryable. The
// per-session token prevents another tab from releasing an unrelated claim.
// resetAnswerClaim only touches an unanswered, media-inactive call, so a late
// release can never roll an already connected carrier leg back to pending.
func (a *App) softphoneReleaseAnswer(w http.ResponseWriter, r *http.Request, project, callID string) {
	callID = strings.Trim(callID, "/")
	var body struct {
		SessionToken string `json:"session_token"`
	}
	if callID == "" || strings.Contains(callID, "/") || decodeJSONBody(r, &body) != nil {
		http.Error(w, "invalid release request", http.StatusBadRequest)
		return
	}
	row, err := a.db().findCall(callID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if row == nil || row.ProjectID != project {
		http.Error(w, "unknown call_id", http.StatusNotFound)
		return
	}
	if row.Status != "answering" || row.PeerKind != peerKindHuman ||
		row.PeerToken == "" || !secureEqual(body.SessionToken, row.PeerToken) {
		http.Error(w, "answer session is not releasable", http.StatusConflict)
		return
	}
	if err := a.db().resetAnswerClaim(callID); err != nil {
		http.Error(w, "release answer: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "pending"})
}

// setRouteAnswerMode lets the panel flip an inbound route between agent
// answering and the browser softphone. Mirrors setRouteTransport's ownership
// checks (sip_routes.go) so the panel cannot retarget another project's route.
func (a *App) setRouteAnswerMode(ctx *sdk.AppCtx, routeID, mode, directive, voice, greeting string) (*routeRow, error) {
	if routeID == "" {
		return nil, errors.New("route_id required")
	}
	route, err := a.db().findRoute(routeID)
	if err != nil {
		return nil, fmt.Errorf("load route: %w", err)
	}
	if route == nil {
		return nil, errors.New("unknown route_id")
	}
	if route.ProjectID != currentProject(ctx) {
		return nil, errors.New("route belongs to another project")
	}
	normalizedMode, normalizedDirective, normalizedVoice, normalizedGreeting, err :=
		normalizeRouteAnswerConfig(mode, directive, voice, greeting)
	if err != nil {
		return nil, err
	}
	if err := a.db().updateRouteAnswerMode(routeID, normalizedMode, normalizedDirective, normalizedVoice, normalizedGreeting); err != nil {
		return nil, fmt.Errorf("persist answer mode: %w", err)
	}
	route.AnswerMode = normalizedMode
	route.AutoDirective = normalizedDirective
	route.AutoVoice = normalizedVoice
	route.AutoGreeting = normalizedGreeting
	return route, nil
}

func decodeJSONBody(r *http.Request, out any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(out); err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}

// ─── outbound placement ────────────────────────────────────────────

// placeHumanCall is the softphone twin of toolPlaceCall. It shares the carrier
// placement path (placeOutboundLeg) and differs only in what sits on the far
// side of the bridge: a loopback softphone hub instead of a spawned realtime
// thread. No agent id is involved, because no thread is spawned.
func (a *App) placeHumanCall(ctx *sdk.AppCtx, projectID, to, requestedFrom string, timeoutSec int, recordingOverride *bool) (*softphoneSession, error) {
	// Carriers dial this app's public wss:// media endpoint (publicWSStreamURL),
	// so an unreachable public URL must fail here rather than after the callee's
	// phone has already rung. Mirrors the check toolPlaceCall makes.
	if err := a.validatePublicEndpoint(); err != nil {
		return nil, err
	}
	bound, creds, from, err := a.resolveCarrierBinding(ctx, projectID, requestedFrom)
	if err != nil {
		return nil, err
	}
	carrier, err := a.carrierFor(bound, creds.Slug, creds.Fields)
	if err != nil {
		return nil, err
	}
	recordingPolicy, err := a.db().recordingSettings(projectID)
	if err != nil {
		return nil, errors.New("load recording settings: " + err.Error())
	}
	recordingMode := recordingPolicy.DefaultMode
	if recordingOverride != nil {
		recordingMode = recordingModeOff
		if *recordingOverride {
			recordingMode = recordingModeAlways
		}
	}
	if timeoutSec == 0 {
		timeoutSec = 60
	}
	if timeoutSec < 5 || timeoutSec > 120 {
		return nil, errors.New("timeout_sec must be between 5 and 120 seconds")
	}

	now := time.Now().UTC()
	callID := newCallID()
	peerToken := newSecret()
	// calls.thread_id is unique even for human calls that never spawn a
	// realtime thread. A stable synthetic id prevents a legacy empty-id row
	// (or any earlier browser call) from blocking every later outbound call.
	row := callRow{
		ID:                     callID,
		ThreadID:               "human-" + callID,
		Direction:              "outbound",
		AgentID:                0,
		CarrierSlug:            carrier.Slug(),
		CarrierConnectionID:    bound.ConnectionID,
		CallbackSecret:         newSecret(),
		ToNumber:               to,
		FromNumber:             from,
		IngressPath:            "outbound",
		Directive:              "",
		Voice:                  "",
		Status:                 "initiated",
		PlacedAt:               now.Format(time.RFC3339),
		ProjectID:              projectID,
		StateExpiresAt:         now.Add(60 * time.Second).Format(time.RFC3339),
		DeadlineAt:             now.Add(time.Hour).Format(time.RFC3339),
		RecordingMode:          recordingMode,
		RecordingChannels:      recordingPolicy.Channels,
		RecordingStorageMode:   recordingPolicy.StorageMode,
		RecordingRetentionDays: recordingPolicy.RetentionDays,
		PeerKind:               peerKindHuman,
		PeerToken:              peerToken,
	}
	row.AudioBridgeURL = a.peerLoopbackURL(&row)

	if err := a.placeOutboundLeg(ctx, carrier, &row, timeoutSec, 3600, nil); err != nil {
		return nil, err
	}
	return &softphoneSession{
		CallID:   callID,
		MediaURL: a.softphoneMediaURL(callID, peerToken),
		To:       to,
		From:     from,
	}, nil
}
