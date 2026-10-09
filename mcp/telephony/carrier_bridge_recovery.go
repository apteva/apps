package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// Carrier-independent ownership and recovery. Only adapters implementing the
// optional restart contract may issue carrier commands; never place another call.
type carrierStreamRestarter interface {
	RestartMedia(context.Context, *sdk.AppCtx, *callRow, string) error
}

func (c *telnyxCarrier) RestartMedia(request context.Context, ctx *sdk.AppCtx, row *callRow, command string) error {
	input := map[string]any{"call_control_id": row.CarrierSID, "stream_url": c.app.publicWSStreamURL("telnyx", row.ID, row.CallbackSecret), "stream_track": "inbound_track", "command_id": command}
	applyTelnyxMediaProfile(input)
	_, err := executeCarrierTool(ctx, c.connID, "start_streaming", input, request)
	return err
}

const carrierRecoveryAttempts = 3
const carrierProtocolTimeout = 20 * time.Second

var errCarrierBridgeBusy = errors.New("media bridge already active")

type carrierBridgeRegistry struct {
	mu      sync.Mutex
	current map[string]*carrierBridgeLease
}
type carrierBridgeEvidence struct {
	Transport         *websocketTransportSnapshot `json:"transport,omitempty"`
	At                string                      `json:"at"`
	OccurredAt        string                      `json:"occurred_at,omitempty"`
	Kind              string                      `json:"kind"`
	Leg               string                      `json:"leg,omitempty"`
	Detail            string                      `json:"detail,omitempty"`
	EventID           string                      `json:"provider_event_id,omitempty"`
	StreamID          string                      `json:"stream_id,omitempty"`
	ActualCloseCode   int                         `json:"actual_close_code,omitempty"`
	ActualCloseReason string                      `json:"actual_close_reason,omitempty"`
	LocalCloseCode    int                         `json:"local_close_code,omitempty"`
}
type carrierBridgeLease struct {
	app           *App
	row           callRow
	generation    string
	started       time.Time
	ctx           context.Context
	cancel        context.CancelFunc
	closeState    *websocketCloseState
	mu            sync.Mutex
	sockets       []net.Conn
	writers       map[mediaCloseLeg]*websocketWriterPump
	stream        string
	connected     bool
	recovered     bool
	events        []carrierBridgeEvidence
	first         *carrierBridgeEvidence
	deadline      string
	attempts      int
	lastRead      atomic.Int64
	flowing       atomic.Bool
	finished      bool
	commandCancel context.CancelFunc
}

func carrierEvidenceDetail(row *callRow, value string) string {
	if row.CallbackSecret != "" {
		value = strings.ReplaceAll(value, row.CallbackSecret, "[redacted]")
	}
	if row.PeerToken != "" {
		value = strings.ReplaceAll(value, row.PeerToken, "[redacted]")
	}
	if row.AudioBridgeURL != "" {
		value = strings.ReplaceAll(value, row.AudioBridgeURL, redactURL(row.AudioBridgeURL))
	}
	value = strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, value)
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}
func normalizeCarrierEvidence(row *callRow, e carrierBridgeEvidence) carrierBridgeEvidence {
	e.At = ringTime(time.Now())
	e.Detail = carrierEvidenceDetail(row, e.Detail)
	e.ActualCloseReason = carrierEvidenceDetail(row, e.ActualCloseReason)
	e.EventID = limitDiagnosticText(e.EventID, 256)
	e.StreamID = limitDiagnosticText(e.StreamID, 256)
	e.OccurredAt = limitDiagnosticText(e.OccurredAt, 40)
	e.Kind = limitDiagnosticText(e.Kind, 40)
	e.Leg = limitDiagnosticText(e.Leg, 40)
	return e
}
func transportEvidenceError(err error) string {
	var u *url.Error
	if errors.As(err, &u) {
		err = u.Err
	}
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%T: %v", err, err)
}
func (a *App) claimCarrierBridge(row *callRow, parent context.Context) (*carrierBridgeLease, error) {
	unlock := a.softphones.lockClaim(row.ID)
	defer unlock()
	r := &a.mediaBridges
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		r.current = map[string]*carrierBridgeLease{}
	}
	old := r.current[row.ID]
	if old != nil && old.ctx.Err() == nil {
		return nil, errCarrierBridgeBusy
	}
	closedGeneration := ""
	if old != nil {
		closedGeneration = old.generation
	}
	ctx, cancel := context.WithCancel(parent)
	b := &carrierBridgeLease{app: a, row: *row, generation: newCallID(), started: time.Now().UTC(), ctx: ctx, cancel: cancel, closeState: &websocketCloseState{}}
	b.lastRead.Store(time.Now().UnixNano())
	if old != nil {
		old.closeSockets()
		old.mu.Lock()
		if old.commandCancel != nil {
			old.commandCancel()
		}
		b.deadline, b.attempts = old.deadline, old.attempts
		old.mu.Unlock()
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		cancel()
		return nil, err
	}
	defer tx.Rollback()
	var previousGeneration string
	_ = tx.QueryRow(`SELECT media_generation FROM calls WHERE id=? AND project_id=?`, row.ID, row.ProjectID).Scan(&previousGeneration)
	res, err := tx.Exec(`UPDATE calls SET media_active=1,media_generation=?,media_status='connecting',media_close_code=0,media_close_reason='',media_close_leg='',media_error_message='',media_disconnected_at='',updated_at=? WHERE id=? AND project_id=? AND (media_active=0 OR (?<>'' AND media_generation=?)) AND status NOT IN ('completed','failed','no-answer','busy','canceled') AND NOT EXISTS(SELECT 1 FROM carrier_media_bridges WHERE generation=calls.media_generation AND state='failed')`, b.generation, ringTime(time.Now()), row.ID, row.ProjectID, closedGeneration, closedGeneration)
	if err != nil {
		cancel()
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		cancel()
		return nil, errCarrierBridgeBusy
	}
	// Rehydrate a bounded recovery budget after worker/process interruptions.
	if old == nil {
		_ = tx.QueryRow(`SELECT deadline_at,attempts FROM carrier_media_bridges WHERE call_id=? AND project_id=? AND state IN ('recovering','connecting') AND deadline_at<>'' ORDER BY started_at DESC LIMIT 1`, row.ID, row.ProjectID).Scan(&b.deadline, &b.attempts)
	}
	_, err = tx.Exec(`INSERT INTO carrier_media_bridges(generation,call_id,project_id,provider,started_at,deadline_at,attempts) VALUES(?,?,?,?,?,?,?)`, b.generation, row.ID, row.ProjectID, row.CarrierSlug, ringTime(b.started), b.deadline, b.attempts)
	if err != nil {
		cancel()
		return nil, err
	}
	_, err = tx.Exec(`DELETE FROM carrier_media_bridges WHERE generation IN (SELECT generation FROM carrier_media_bridges WHERE call_id=? AND project_id=? ORDER BY started_at DESC LIMIT -1 OFFSET 32)`, row.ID, row.ProjectID)
	if err != nil {
		cancel()
		return nil, err
	}
	if previousGeneration != "" {
		if _, err = tx.Exec(`UPDATE carrier_media_bridges SET state='replaced',next_attempt_at='' WHERE generation=? AND state IN ('connecting','connected','recovering')`, previousGeneration); err != nil {
			cancel()
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		cancel()
		return nil, err
	}
	r.current[row.ID] = b
	// Socket closure must interrupt blocked reads/writes, including during dial.
	go func() {
		<-ctx.Done()
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		b.closeSockets()
	}()
	return b, nil
}
func (b *carrierBridgeLease) bind(conn net.Conn) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx.Err() != nil {
		_ = conn.Close()
		return false
	}
	b.sockets = append(b.sockets, conn)
	return true
}
func (b *carrierBridgeLease) closeSockets() {
	b.mu.Lock()
	s := append([]net.Conn(nil), b.sockets...)
	b.mu.Unlock()
	for _, conn := range s {
		_ = conn.SetDeadline(time.Now())
		_ = conn.Close()
	}
}
func (b *carrierBridgeLease) appendLocked(e carrierBridgeEvidence) {
	e = normalizeCarrierEvidence(&b.row, e)
	if e.EventID != "" {
		for _, old := range b.events {
			if old.EventID == e.EventID {
				return
			}
		}
	}
	b.events = append(b.events, e)
	if len(b.events) > 48 {
		b.events = append([]carrierBridgeEvidence(nil), b.events[len(b.events)-48:]...)
	}
}
func (b *carrierBridgeLease) persistLocked(state string) error {
	events, _ := json.Marshal(b.events)
	first := []byte("{}")
	at := ""
	if b.first != nil {
		first, _ = json.Marshal(b.first)
		at = b.first.At
	}
	_, err := b.app.db().db.Exec(`UPDATE carrier_media_bridges SET state=CASE WHEN state IN ('ended','failed','replaced') THEN state ELSE ? END,stream_id=?,events_json=?,first_failed_at=CASE WHEN first_failed_at='' THEN ? ELSE first_failed_at END,first_failure_json=CASE WHEN first_failed_at='' THEN ? ELSE first_failure_json END,deadline_at=?,attempts=? WHERE generation=?`, state, b.stream, string(events), at, string(first), b.deadline, b.attempts, b.generation)
	return err
}
func (b *carrierBridgeLease) fail(e carrierBridgeEvidence) {
	b.mu.Lock()
	b.appendLocked(e)
	// Later cleanup/callbacks add evidence but cannot change the first cause,
	// extend the recovery budget, or revive a canceled generation.
	if b.ctx.Err() != nil {
		events, _ := json.Marshal(b.events)
		_, _ = b.app.db().db.Exec(`UPDATE carrier_media_bridges SET events_json=? WHERE generation=?`, string(events), b.generation)
		b.mu.Unlock()
		return
	}
	b.connected = false
	if b.first == nil {
		copy := b.events[len(b.events)-1]
		b.first = &copy
	}
	if b.deadline == "" {
		b.deadline = ringTime(time.Now().Add(time.Duration(mediaRecoveryOrDefault(b.row.MediaRecoveryTimeoutSec)) * time.Second))
	}
	first := *b.first
	b.flowing.Store(false)
	b.mu.Unlock()
	b.closeState.SetLeg(mediaCloseLegLocalError, ws.StatusGoingAway, "carrier media recovering")
	b.cancel()
	if e.ActualCloseCode != 1000 && e.ActualCloseCode != 1001 {
		b.closeSockets()
	}
	// Transport teardown precedes reporting; a busy database cannot leave the
	// failed sockets open while the failure journal is being written.
	b.mu.Lock()
	_ = b.persistLocked("recovering")
	b.mu.Unlock()
	// Release only this generation after its sockets have been closed, so a
	// replacement arriving before deferred cleanup can claim immediately.
	_ = b.app.db().updateMediaStatusWithLeg(b.row.ID, "error", first.Detail, 0, "", first.Leg, b.generation)
	_, _ = b.app.db().db.Exec(`UPDATE calls SET media_active=0 WHERE id=? AND media_generation=?`, b.row.ID, b.generation)
	_, _ = b.app.db().db.Exec(`UPDATE carrier_media_bridges SET next_attempt_at=? WHERE generation=? AND next_attempt_at=''`, ringTime(time.Now().Add(2*time.Second)), b.generation)
}
func (b *carrierBridgeLease) socketError(leg mediaCloseLeg, operation string, err error) {
	if b.ctx.Err() != nil {
		return
	}
	e := carrierBridgeEvidence{Kind: "socket_" + operation, Leg: string(leg), Detail: transportEvidenceError(err)}
	var closed wsutil.ClosedError
	if errors.As(err, &closed) {
		e.ActualCloseCode = int(closed.Code)
		e.ActualCloseReason = carrierEvidenceDetail(&b.row, closed.Reason)
		if closed.Code == 1000 || closed.Code == 1001 {
			b.closeState.SetLeg(leg, closed.Code, closed.Reason)
		}
	}
	b.fail(e)
}
func (b *carrierBridgeLease) setStream(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx.Err() != nil {
		return
	}
	b.stream = boundedContextValue(id)
	b.appendLocked(carrierBridgeEvidence{Kind: "stream_identified", StreamID: b.stream})
	state := "connecting"
	if b.connected {
		state = "connected"
	}
	_ = b.persistLocked(state)
}

// Zero-valued PCM is valid media: recovery confirmation never depends on speech.
func (b *carrierBridgeLease) media() {
	// Established media never waits for lifecycle reporting locks/SQL.
	if b.flowing.Load() || b.ctx.Err() != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.connected || b.ctx.Err() != nil {
		return
	}
	b.connected = true
	b.flowing.Store(true)
	b.recovered = b.deadline != ""
	b.appendLocked(carrierBridgeEvidence{Kind: "media_flowing", StreamID: b.stream})
	_ = b.persistLocked("connected")
	_, _ = b.app.db().db.Exec(`UPDATE carrier_media_bridges SET connected_at=? WHERE generation=?`, ringTime(time.Now()), b.generation)
	_ = b.app.db().updateMediaStatusWithLeg(b.row.ID, "connected", "", 0, "", "", b.generation)
	_, _ = b.app.db().db.Exec(`UPDATE calls SET state_expires_at='' WHERE id=? AND media_generation=? AND status NOT IN ('completed','failed','no-answer','busy','canceled')`, b.row.ID, b.generation)
	b.deadline = ""
	b.attempts = 0
}
func (b *carrierBridgeLease) received(op ws.OpCode, data []byte) {
	b.lastRead.Store(time.Now().UnixNano())
	// Keep the received close frame even if writing the close reply fails.
	if op == ws.OpClose && len(data) != 1 {
		code, reason := ws.StatusNoStatusRcvd, ""
		if len(data) >= 2 {
			code, reason = ws.ParseCloseFrameData(data)
			if ws.CheckCloseFrameData(code, reason) != nil {
				return
			}
		}
		b.mu.Lock()
		b.appendLocked(carrierBridgeEvidence{Kind: "peer_close_frame", Leg: "carrier", ActualCloseCode: int(code), ActualCloseReason: carrierEvidenceDetail(&b.row, reason)})
		b.mu.Unlock()
	}
}
func (b *carrierBridgeLease) protocolHealthy(now time.Time) bool {
	return now.Sub(time.Unix(0, b.lastRead.Load())) <= carrierProtocolTimeout
}
func (b *carrierBridgeLease) trackWriter(leg mediaCloseLeg, writer *websocketWriterPump) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.writers == nil {
		b.writers = map[mediaCloseLeg]*websocketWriterPump{}
	}
	b.writers[leg] = writer
	writer.setDiagnosticID(b.generation + ":" + string(leg))
}
func (b *carrierBridgeLease) watch(writer *websocketWriterPump) {
	b.trackWriter(mediaCloseLegCarrier, writer)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-b.ctx.Done():
				return
			case <-ticker.C:
				if !b.protocolHealthy(time.Now()) {
					b.fail(carrierBridgeEvidence{Kind: "protocol_timeout", Leg: "carrier", Detail: "carrier WebSocket stopped responding to protocol liveness"})
					return
				}
				if !writer.queueControlFrame(ws.OpPing, []byte("telephony-media")) {
					b.fail(carrierBridgeEvidence{Kind: "socket_write", Leg: "carrier", Detail: "carrier liveness write unavailable"})
					return
				}
			}
		}
	}()
}
func (b *carrierBridgeLease) finish() {
	b.cancel()
	b.closeSockets()
	b.mu.Lock()
	if b.finished {
		b.mu.Unlock()
		return
	}
	b.finished = true
	leg, code, reason := b.closeState.Cause()
	state := "disconnected"
	newFailure := false
	if b.first == nil {
		current, err := b.app.db().findCall(b.row.ID)
		if err == nil && current != nil && !isTerminalStatus(current.Status) {
			e := carrierBridgeEvidence{Kind: "bridge_ended", Leg: string(leg), Detail: reason, LocalCloseCode: int(code)}
			b.appendLocked(e)
			copy := b.events[len(b.events)-1]
			b.first = &copy
			newFailure = true
			if b.deadline == "" {
				b.deadline = ringTime(time.Now().Add(time.Duration(mediaRecoveryOrDefault(b.row.MediaRecoveryTimeoutSec)) * time.Second))
			}
		}
	}
	if b.first != nil {
		state = "recovering"
	}
	for leg, writer := range b.writers {
		snapshot := writer.audioSnapshot().Transport
		b.appendLocked(carrierBridgeEvidence{Kind: "socket_summary", Leg: string(leg), Transport: &snapshot})
	}
	b.appendLocked(carrierBridgeEvidence{Kind: "local_cleanup", Leg: string(leg), Detail: reason, LocalCloseCode: int(code)})
	_ = b.persistLocked(state)
	b.mu.Unlock()
	_, _ = b.app.db().db.Exec(`UPDATE carrier_media_bridges SET cleanup_at=? WHERE generation=?`, ringTime(time.Now()), b.generation)
	if state == "recovering" {
		if newFailure {
			_ = b.app.db().updateMediaStatusWithLeg(b.row.ID, "error", b.first.Detail, 0, "", b.first.Leg, b.generation)
		}
		_, _ = b.app.db().db.Exec(`UPDATE carrier_media_bridges SET next_attempt_at=? WHERE generation=? AND next_attempt_at=''`, ringTime(time.Now().Add(2*time.Second)), b.generation)
	}
	if state == "disconnected" {
		_ = b.app.db().updateMediaStatusWithLeg(b.row.ID, "disconnected", "", int(code), reason, string(leg), b.generation)
	}
	_, _ = b.app.db().db.Exec(`UPDATE calls SET media_active=0 WHERE id=? AND media_generation=?`, b.row.ID, b.generation)
}
func (a *App) cancelCarrierBridge(id string) {
	a.mediaBridges.mu.Lock()
	b := a.mediaBridges.current[id]
	delete(a.mediaBridges.current, id)
	a.mediaBridges.mu.Unlock()
	if b != nil {
		b.mu.Lock()
		if b.commandCancel != nil {
			b.commandCancel()
		}
		b.mu.Unlock()
		b.closeState.SetLeg(mediaCloseLegLocalError, ws.StatusNormalClosure, "call ended")
		b.cancel()
		b.closeSockets()
		_ = a.db().updateMediaStatusWithLeg(id, "disconnected", "", 1000, "call ended", string(mediaCloseLegLocalError), b.generation)
	}
	_, _ = a.db().db.Exec(`UPDATE carrier_media_bridges SET state='ended',next_attempt_at='' WHERE call_id=? AND state IN ('connecting','connected','recovering','failed')`, id)
}
func (a *App) carrierStreamEvent(row *callRow, update callbackUpdate) bool {
	a.mediaBridges.mu.Lock()
	b := a.mediaBridges.current[row.ID]
	a.mediaBridges.mu.Unlock()
	if b == nil {
		managed := a.recordCarrierBridgeObservation(row, carrierBridgeEvidence{Kind: "provider_" + update.MediaStatus, Leg: "provider", Detail: update.MediaError, EventID: update.Facts.ProviderEventID, OccurredAt: update.Facts.OccurredAt, StreamID: update.StreamID})
		return managed || (row.CarrierSlug == "telnyx" && update.MediaStatus == "connected")
	}
	b.mu.Lock()
	e := carrierBridgeEvidence{Kind: "provider_" + update.MediaStatus, Leg: "provider", Detail: update.MediaError, EventID: update.Facts.ProviderEventID, OccurredAt: update.Facts.OccurredAt, StreamID: update.StreamID}
	stale := update.StreamID != "" && b.stream != "" && update.StreamID != b.stream
	if at, err := time.Parse(time.RFC3339Nano, update.Facts.OccurredAt); err == nil && at.Before(b.started) {
		stale = true
	}
	// A replacement's start frame may not have arrived yet. Recognize stream
	// IDs belonging to previous generations before assigning a failure.
	if update.StreamID != "" && b.stream == "" {
		var historical bool
		_ = a.db().db.QueryRow(`SELECT EXISTS(SELECT 1 FROM carrier_media_bridges WHERE call_id=? AND project_id=? AND generation<>? AND stream_id=?)`, row.ID, row.ProjectID, b.generation, update.StreamID).Scan(&historical)
		stale = stale || historical || b.deadline != ""
	}
	// Without stream identity a callback cannot safely fail a replacement.
	if update.StreamID == "" && (b.recovered || b.deadline != "") {
		stale = true
	}
	if stale {
		e.Kind = "stale_provider_event"
		b.appendLocked(e)
		events, _ := json.Marshal(b.events)
		_, _ = a.db().db.Exec(`UPDATE carrier_media_bridges SET events_json=? WHERE generation=?`, string(events), b.generation)
		b.mu.Unlock()
		return true
	}
	b.mu.Unlock()
	if update.MediaStatus == "error" || update.MediaStatus == "disconnected" {
		b.fail(e)
	} else {
		b.mu.Lock()
		b.appendLocked(e)
		state := "connecting"
		if b.connected {
			state = "connected"
		}
		if b.ctx.Err() != nil {
			state = "recovering"
		}
		_ = b.persistLocked(state)
		b.mu.Unlock()
	}
	return true
}

// Advisory or orphaned callbacks retain evidence without claiming media,
// confirming recovery, or reviving a completed generation.
func (a *App) recordCarrierBridgeObservation(row *callRow, e carrierBridgeEvidence) bool {
	a.mediaBridges.mu.Lock()
	b := a.mediaBridges.current[row.ID]
	a.mediaBridges.mu.Unlock()
	if b != nil {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.appendLocked(e)
		events, _ := json.Marshal(b.events)
		_, err := a.db().db.Exec(`UPDATE carrier_media_bridges SET events_json=? WHERE generation=?`, string(events), b.generation)
		return err == nil
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		return false
	}
	defer tx.Rollback()
	var generation, raw string
	err = tx.QueryRow(`SELECT b.generation,b.events_json FROM carrier_media_bridges b JOIN calls c ON c.id=b.call_id AND c.media_generation=b.generation WHERE c.id=? AND c.project_id=?`, row.ID, row.ProjectID).Scan(&generation, &raw)
	if err != nil {
		return false
	}
	var events []carrierBridgeEvidence
	_ = json.Unmarshal([]byte(raw), &events)
	for _, old := range events {
		if e.EventID != "" && old.EventID == e.EventID {
			return true
		}
	}
	e = normalizeCarrierEvidence(row, e)
	events = append(events, e)
	if len(events) > 48 {
		events = events[len(events)-48:]
	}
	data, _ := json.Marshal(events)
	if _, err = tx.Exec(`UPDATE carrier_media_bridges SET events_json=? WHERE generation=?`, string(data), generation); err != nil {
		return false
	}
	return tx.Commit() == nil
}
func (a *App) runCarrierMediaRecovery(_ context.Context, ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().Query(`SELECT b.generation,b.call_id FROM carrier_media_bridges b JOIN calls c ON c.id=b.call_id AND c.media_generation=b.generation WHERE b.project_id=? AND b.state IN ('recovering','connecting') AND b.deadline_at<>'' AND (b.next_attempt_at<=? OR b.deadline_at<=?) ORDER BY b.next_attempt_at LIMIT 32`, ctx.CurrentProject(), ringTime(time.Now()), ringTime(time.Now()))
	if err != nil {
		return err
	}
	var items [][2]string
	for rows.Next() {
		var v [2]string
		if err = rows.Scan(&v[0], &v[1]); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	rows.Close()
	for _, v := range items {
		generation, id := v[0], v[1]
		a.launchAIRecovery("media-recovery/"+id, func() { _ = a.restartCarrierMedia(ctx, id, generation) })
	}
	// A single bounded reconciliation query covers DB-driven terminal states.
	ended, e := ctx.AppDB().Query(`SELECT b.call_id FROM carrier_media_bridges b JOIN calls c ON c.id=b.call_id AND c.media_generation=b.generation WHERE b.project_id=? AND b.state IN ('connecting','connected','recovering','failed') AND c.status IN ('completed','failed','no-answer','busy','canceled') LIMIT 64`, ctx.CurrentProject())
	if e == nil {
		var ids []string
		for ended.Next() {
			var id string
			if ended.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		ended.Close()
		for _, id := range ids {
			a.cancelCarrierBridge(id)
		}
	}
	// Bounded indexed retention also removes histories whose calls were deleted.
	_, _ = ctx.AppDB().Exec(`DELETE FROM carrier_media_bridges WHERE generation IN (SELECT generation FROM carrier_media_bridges WHERE project_id=? AND started_at<? AND state IN ('ended','failed','replaced','disconnected') ORDER BY started_at LIMIT 100)`, ctx.CurrentProject(), ringTime(time.Now().Add(-30*24*time.Hour)))
	return nil
}
func (a *App) restartCarrierMedia(ctx *sdk.AppCtx, id, generation string) error {
	unlock := a.softphones.lockClaim(id)
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	row, err := a.db().findCall(id)
	if err != nil {
		return err
	}
	if row == nil || isTerminalStatus(row.Status) {
		return nil
	}
	var state, deadline, next string
	var attempts int
	err = a.db().db.QueryRow(`SELECT b.state,b.deadline_at,b.next_attempt_at,b.attempts FROM carrier_media_bridges b JOIN calls c ON c.id=b.call_id AND c.media_generation=b.generation WHERE b.generation=?`, generation).Scan(&state, &deadline, &next, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state == "connected" || state == "ended" || state == "failed" {
		return nil
	}
	now := ringTime(time.Now())
	if next > now {
		return nil
	}
	a.mediaBridges.mu.Lock()
	b := a.mediaBridges.current[id]
	a.mediaBridges.mu.Unlock()
	if deadline == "" {
		return nil
	}
	if deadline <= now {
		return a.failCarrierRecovery(id, generation, b, "media recovery deadline reached")
	}
	if b != nil && b.generation == generation && b.ctx.Err() == nil {
		// Advance the due cursor so one slow replacement cannot starve other calls.
		nextCheck := ringTime(time.Now().Add(2 * time.Second))
		if deadline < nextCheck {
			nextCheck = deadline
		}
		_, err = a.db().db.Exec(`UPDATE carrier_media_bridges SET next_attempt_at=? WHERE generation=?`, nextCheck, generation)
		return err // Valid socket is allowed until the budget expires.
	}
	if attempts >= carrierRecoveryAttempts {
		return a.failCarrierRecovery(id, generation, b, "media restart attempts exhausted")
	}
	carrier, err := a.carrierForRow(ctx, nil, row)
	if err != nil {
		return err
	}
	restarter, ok := carrier.(carrierStreamRestarter)
	if !ok {
		_, err = a.db().db.Exec(`UPDATE carrier_media_bridges SET next_attempt_at=deadline_at WHERE generation=?`, generation)
		return err
	}
	attempts++
	// Reserve before dispatch, persist across ticks, and give provider reconnection
	// time to win. Deterministic bounded jitter staggers simultaneous failures.
	jitter := fnv.New32a()
	_, _ = jitter.Write([]byte(generation))
	delay := time.Duration(3*(1<<uint(attempts-1)))*time.Second + time.Duration(jitter.Sum32()%7)*100*time.Millisecond
	_, err = a.db().db.Exec(`UPDATE carrier_media_bridges SET attempts=?,next_attempt_at=? WHERE generation=?`, attempts, ringTime(time.Now().Add(delay)), generation)
	if err != nil {
		return err
	}
	if b != nil {
		b.mu.Lock()
		b.attempts = attempts
		b.appendLocked(carrierBridgeEvidence{Kind: "recovery_attempt", Detail: fmt.Sprint(attempts)})
		_ = b.persistLocked("recovering")
		b.mu.Unlock()
	}
	// Reserve under the ownership lock, but never hold it over a network call:
	// some providers open the replacement socket before their API returns.
	// A replacement cancels this in-flight request and inherits its budget.
	// No routing/answer/dial/stop command runs.
	limit := 5 * time.Second
	if end, e := time.Parse(time.RFC3339Nano, deadline); e == nil && time.Until(end) < limit {
		limit = time.Until(end)
	}
	request, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	if b != nil {
		b.mu.Lock()
		b.commandCancel = cancel
		b.mu.Unlock()
		defer func() { b.mu.Lock(); b.commandCancel = nil; b.mu.Unlock() }()
	}
	fresh, e := a.db().findCall(id)
	if e != nil {
		return e
	}
	if fresh == nil || isTerminalStatus(fresh.Status) {
		return nil
	}
	unlock()
	locked = false
	err = restarter.RestartMedia(request, ctx, row, telnyxCommandID(id, "media-recovery-"+generation+fmt.Sprint(attempts)))
	if b != nil {
		b.mu.Lock()
		detail := "accepted; awaiting replacement media"
		if err != nil {
			detail = transportEvidenceError(err)
		}
		b.appendLocked(carrierBridgeEvidence{Kind: "recovery_command_result", Detail: detail})
		_ = b.persistLocked("recovering")
		b.mu.Unlock()
	}
	return err
}

// A failed budget hands terminal handling to the existing lifecycle watchdog.
// It never reoffers advisers or dials another carrier leg.
func (a *App) failCarrierRecovery(id, generation string, b *carrierBridgeLease, reason string) error {
	if b != nil && b.generation == generation {
		b.mu.Lock()
		b.appendLocked(carrierBridgeEvidence{Kind: "recovery_failed", Detail: reason})
		_ = b.persistLocked("failed")
		b.mu.Unlock()
		b.cancel()
		b.closeSockets()
	}
	_, err := a.db().db.Exec(`UPDATE carrier_media_bridges SET state='failed',next_attempt_at='' WHERE generation=? AND state IN ('connecting','recovering')`, generation)
	if err != nil {
		return err
	}
	_, err = a.db().db.Exec(`UPDATE calls SET media_active=0,media_deadline_at=? WHERE id=? AND media_generation=? AND status NOT IN ('completed','failed','no-answer','busy','canceled')`, ringTime(time.Now()), id, generation)
	return err
}
func (c *callsDB) carrierBridgeHistory(project, call string) ([]map[string]any, error) {
	rows, err := c.db.Query(`SELECT generation,provider,stream_id,state,started_at,connected_at,first_failed_at,first_failure_json,cleanup_at,deadline_at,attempts,events_json FROM carrier_media_bridges WHERE project_id=? AND call_id=? ORDER BY started_at DESC LIMIT 32`, project, call)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var gen, provider, stream, state, start, connected, failed, first, cleanup, deadline, events string
		var attempts int
		if err := rows.Scan(&gen, &provider, &stream, &state, &start, &connected, &failed, &first, &cleanup, &deadline, &attempts, &events); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"generation": gen, "provider": provider, "stream_id": stream, "state": state, "started_at": start, "connected_at": connected, "first_failed_at": failed, "first_failure": json.RawMessage(first), "cleanup_at": cleanup, "deadline_at": deadline, "recovery_attempts": attempts, "events": json.RawMessage(events)})
	}
	return out, rows.Err()
}
func (a *App) stopCarrierBridges() {
	a.mediaBridges.mu.Lock()
	all := a.mediaBridges.current
	a.mediaBridges.current = nil
	a.mediaBridges.mu.Unlock()
	for _, b := range all {
		b.mu.Lock()
		if b.commandCancel != nil {
			b.commandCancel()
		}
		b.mu.Unlock()
		b.cancel()
		b.closeSockets()
	}
}
