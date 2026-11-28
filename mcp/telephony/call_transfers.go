package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws"
)

// Call transfers ring advisers' browsers for a call that is already connected.
// The call's AI starts one with telephony_transfer_call; the adviser on the call
// starts one from the softphone. The carrier leg never changes.
//
//   - Adviser to adviser: the caller is put on hold and the first adviser keeps
//     the call until a colleague answers. Answering moves ownership and the
//     browser media grant, as a supervisor takeover does; the softphone hub and
//     the carrier bridge stay as they are.
//   - AI to adviser: the AI hears silence instead of the caller (it is
//     "parked") and the caller hears hold music. Answering switches the call to
//     a human peer and restarts the carrier bridge with the softphone hub as its
//     peer: a Telnyx stream restart, or a new local bridge on the same direct SIP
//     RTP socket. The AI thread is then ended.
//
// When nobody answers, the call returns to whoever transferred it; the AI may
// ask for a hang-up instead. A normal call pays one atomic load per caller audio
// frame (the parked check) and nothing else.

const (
	transferSourceAI       = "ai"
	transferSourceAdviser  = "adviser"
	transferSourceOperator = "operator"

	transferRinging   = "ringing"
	transferAnswered  = "answered"
	transferNoAnswer  = "no_answer"
	transferCanceled  = "canceled"
	transferFailed    = "failed"
	transferAbandoned = "abandoned"

	transferDefaultTimeout = 30 * time.Second
	transferMinTimeout     = 10 * time.Second
	transferMaxTimeout     = 90 * time.Second
	transferNoteMax        = 500
	// The AI's last sentence ("please hold") may still be playing when it asks
	// for the transfer. Hold music waits for it, up to this long.
	transferSpeechDrain = 5 * time.Second
)

// transferError is a refused transfer request: an HTTP status for the
// softphone routes, and a code and message for the agent tool.
type transferError struct {
	Status  int
	Code    string
	Message string
}

func (e *transferError) Error() string { return e.Message }

func transferRefused(status int, code, message string) error {
	return &transferError{Status: status, Code: code, Message: message}
}

// callTransfer is one transfer of one call. The fields after mu are in-memory
// state of this process.
type callTransfer struct {
	ID, CallID, ProjectID, Source, Status              string
	TargetKind, TargetID, TargetName                   string
	Destinations                                       []string
	FromPrincipal, FromDestinationID                   string
	FromAgentID                                        int64
	FromThreadID                                       string
	Note, NoAnswer, PriorHoldState                     string
	StartedAt, ExpiresAt                               time.Time
	EndedAt, AnsweredBy, AnsweredDestinationID, Reason string

	mu       sync.Mutex
	finished bool
	music    bool // hold music was started for this transfer
	restored bool // left ringing by a previous process
	done     chan struct{}
}

func (t *callTransfer) rings(destination string) bool {
	for _, d := range t.Destinations {
		if d == destination {
			return true
		}
	}
	return false
}

func (t *callTransfer) isFinished() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.finished
}

// outcome is the transfer's final state once done is closed.
func (t *callTransfer) outcome() (status, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Status, t.Reason
}

// public is what a caller of the transfer routes, tools and call reads sees.
func (t *callTransfer) public() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	from := map[string]any{"kind": t.Source}
	if t.FromAgentID != 0 {
		from["agent_id"] = t.FromAgentID
	}
	var identity phoneIdentity
	if t.FromPrincipal != "" && json.Unmarshal([]byte(t.FromPrincipal), &identity) == nil {
		from["subject_id"] = identity.SubjectID
	}
	out := map[string]any{
		"id": t.ID, "call_id": t.CallID, "status": t.Status, "source": t.Source,
		"target": map[string]any{"kind": t.TargetKind, "id": t.TargetID, "name": t.TargetName},
		"from":   from, "note": t.Note, "no_answer": t.NoAnswer,
		"started_at": ringTime(t.StartedAt), "expires_at": ringTime(t.ExpiresAt),
	}
	if t.EndedAt != "" {
		out["ended_at"] = t.EndedAt
	}
	if t.Reason != "" {
		out["reason"] = t.Reason
	}
	if t.AnsweredDestinationID != "" {
		out["answered_destination_id"] = t.AnsweredDestinationID
	}
	return out
}

// transferRegistry indexes the transfers that are ringing in this process.
// Call reads use it, so listing calls costs no query when nothing rings.
type transferRegistry struct {
	mu       sync.Mutex
	byCall   map[string]*callTransfer
	restored bool
}

func (r *transferRegistry) get(call string) *callTransfer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byCall[call]
}

func (r *transferRegistry) put(t *callTransfer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byCall == nil {
		r.byCall = map[string]*callTransfer{}
	}
	r.byCall[t.CallID] = t
}

func (r *transferRegistry) remove(t *callTransfer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byCall[t.CallID] == t {
		delete(r.byCall, t.CallID)
	}
}

func (r *transferRegistry) all() []*callTransfer {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*callTransfer, 0, len(r.byCall))
	for _, t := range r.byCall {
		out = append(out, t)
	}
	return out
}

// callsRinging lists the calls in project whose ringing transfer rings one of
// destinations, for the adviser call list.
func (r *transferRegistry) callsRinging(project string, destinations map[string]bool) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for call, t := range r.byCall {
		if t.ProjectID != project {
			continue
		}
		for _, d := range t.Destinations {
			if destinations[d] {
				out = append(out, call)
				break
			}
		}
	}
	return out
}

// ─── Targets ───────────────────────────────────────────────────────────────

// transferTarget is a browser destination or a ring group, with the enabled
// browser destinations a transfer to it rings. A ring group rings all of them
// at once, whatever its strategy.
type transferTarget struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	Destinations []string `json:"-"`
}

func (a *App) resolveTransferTarget(project, id string) (*transferTarget, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, transferRefused(400, "target_required", "target_id is required")
	}
	d, err := a.findRoutingDestination(project, id)
	if err != nil {
		return nil, err
	}
	if d != nil {
		if d.Kind != "browser" || !d.Enabled {
			return nil, transferRefused(409, "target_unavailable", "the transfer target must be an enabled browser destination or ring group")
		}
		return &transferTarget{ID: d.ID, Kind: "destination", Name: d.Name, Destinations: []string{d.ID}}, nil
	}
	var name string
	var enabled int
	err = a.db().db.QueryRow(`SELECT name,enabled FROM ring_groups WHERE id=? AND project_id=?`, id, project).Scan(&name, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, transferRefused(404, "target_not_found", "transfer target not found")
	}
	if err != nil {
		return nil, err
	}
	if enabled == 0 {
		return nil, transferRefused(409, "target_unavailable", "the ring group is disabled")
	}
	members, err := a.listRingGroupMembers(id)
	if err != nil {
		return nil, err
	}
	target := &transferTarget{ID: id, Kind: "ring_group", Name: name}
	seen := map[string]bool{}
	for _, m := range members {
		if !m.Enabled || seen[m.DestinationID] {
			continue
		}
		member, err := a.findRoutingDestination(project, m.DestinationID)
		if err != nil {
			return nil, err
		}
		if member != nil && member.Kind == "browser" && member.Enabled {
			seen[member.ID] = true
			target.Destinations = append(target.Destinations, member.ID)
		}
	}
	if len(target.Destinations) == 0 {
		return nil, transferRefused(409, "target_unavailable", "the ring group has no enabled browser destination")
	}
	return target, nil
}

// transferTargets lists the targets allowed reports true for.
func (a *App) transferTargets(project string, allowed func(string) bool) ([]transferTarget, error) {
	destinations, err := a.listRoutingDestinations(project)
	if err != nil {
		return nil, err
	}
	out := []transferTarget{}
	for _, d := range destinations {
		if d.Kind == "browser" && d.Enabled && allowed(d.ID) {
			out = append(out, transferTarget{ID: d.ID, Kind: "destination", Name: d.Name})
		}
	}
	groups, err := a.listRingGroups(project)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if !g.Enabled || !allowed(g.ID) {
			continue
		}
		if target, err := a.resolveTransferTarget(project, g.ID); err == nil {
			out = append(out, *target)
		}
	}
	return out, nil
}

// aiTransferAllowed reads ai_transfer_targets: empty allows every enabled
// browser destination and ring group, otherwise only the listed IDs.
func aiTransferAllowed(ctx *sdk.AppCtx, project string) func(string) bool {
	raw := ""
	if ctx == nil {
		ctx = globalCtx
	}
	if ctx != nil {
		raw = strings.TrimSpace(ctx.WithProject(project).Config()["ai_transfer_targets"])
	}
	if raw == "" {
		return func(string) bool { return true }
	}
	allowed := map[string]bool{}
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = true
		}
	}
	return func(id string) bool { return allowed[id] }
}

// aiTransferSupported reports whether an AI call's bridge can be handed to a
// browser: a Telnyx media stream or an inbound direct SIP call.
func (a *App) aiTransferSupported(row *callRow) bool {
	if a.callUsesDirectSIP(row) {
		return row.Direction == "inbound"
	}
	return row.CarrierSlug == "telnyx"
}

// ─── Start ─────────────────────────────────────────────────────────────────

type transferRequest struct {
	CallID, ProjectID, Source, TargetID, Note, NoAnswer string
	Timeout                                             time.Duration
	From                                                *phonePrincipal // the adviser on the call
	AgentID                                             int64           // the AI on the call
}

func (a *App) startTransfer(ctx *sdk.AppCtx, req transferRequest) (*callTransfer, error) {
	if req.Timeout == 0 {
		req.Timeout = transferDefaultTimeout
	}
	req.Timeout = min(max(req.Timeout, transferMinTimeout), transferMaxTimeout)
	req.Note = strings.TrimSpace(req.Note)
	if len(req.Note) > transferNoteMax {
		return nil, transferRefused(400, "note_too_long", fmt.Sprintf("note must be at most %d characters", transferNoteMax))
	}
	switch req.NoAnswer {
	case "", "return":
		req.NoAnswer = "return"
	case "hangup":
		if req.Source != transferSourceAI {
			return nil, transferRefused(400, "invalid_no_answer", "only the AI may hang up when nobody answers")
		}
	default:
		return nil, transferRefused(400, "invalid_no_answer", "if_no_answer must be return or hangup")
	}
	unlock := a.softphones.lockClaim(req.CallID)
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	row, err := a.db().findCall(req.CallID)
	if err != nil {
		return nil, err
	}
	if row == nil || row.ProjectID != req.ProjectID {
		return nil, transferRefused(404, "call_not_found", "call not found")
	}
	if isTerminalStatus(row.Status) {
		return nil, transferRefused(410, "call_ended", "the call has ended")
	}
	if row.Status != "answered" && row.Status != "in-progress" {
		return nil, transferRefused(409, "call_not_connected", "only a connected call can be transferred")
	}
	var fromPrincipal, fromDestination string
	var allowed func(string) bool
	switch req.Source {
	case transferSourceAI:
		if row.PeerKind != peerKindRealtime || row.AgentID != req.AgentID {
			return nil, transferRefused(403, "not_call_agent", "only the AI on this call can transfer it")
		}
		if row.MediaStatus != "connected" {
			return nil, transferRefused(409, "call_not_connected", "the call's audio is not connected")
		}
		if !a.aiTransferSupported(row) {
			return nil, transferRefused(501, "transfer_unsupported", "this call's carrier cannot hand an AI call to a browser")
		}
		allowed = aiTransferAllowed(ctx, row.ProjectID)
	case transferSourceAdviser, transferSourceOperator:
		if row.PeerKind != peerKindHuman {
			return nil, transferRefused(409, "not_softphone_call", "only a softphone call can be transferred from the softphone")
		}
		if req.From != nil {
			if !a.phoneCallAllowed(req.From, row, false) {
				return nil, transferRefused(403, "not_call_owner", "only the adviser on the call can transfer it")
			}
			fromPrincipal = req.From.Identity.key()
			_, fromDestination, err = a.phoneOwner(row.ID)
			if err != nil {
				return nil, err
			}
			grants := req.From.TransferTargets
			allowed = func(id string) bool { return grants[id] }
		} else {
			allowed = func(string) bool { return true }
		}
	default:
		return nil, errors.New("unknown transfer source")
	}
	if !allowed(strings.TrimSpace(req.TargetID)) {
		return nil, transferRefused(403, "target_not_allowed", "this transfer target is not allowed")
	}
	target, err := a.resolveTransferTarget(row.ProjectID, req.TargetID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	t := &callTransfer{
		ID: newCallID(), CallID: row.ID, ProjectID: row.ProjectID, Source: req.Source, Status: transferRinging,
		TargetKind: target.Kind, TargetID: target.ID, TargetName: target.Name, Destinations: target.Destinations,
		FromPrincipal: fromPrincipal, FromDestinationID: fromDestination, Note: req.Note, NoAnswer: req.NoAnswer,
		PriorHoldState: firstNonEmpty(row.HoldState, "active"), StartedAt: now, ExpiresAt: now.Add(req.Timeout),
		done: make(chan struct{}),
	}
	if req.Source == transferSourceAI {
		t.FromAgentID, t.FromThreadID = row.AgentID, row.ThreadID
		// The AI's own hold state never applies; its transfer always ends on hold.
		t.PriorHoldState = "active"
	}
	destinations, _ := json.Marshal(t.Destinations)
	tx, err := a.db().db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO call_transfers(id,call_id,project_id,source,status,target_kind,target_id,target_name,destinations_json,from_principal,from_destination_id,from_agent_id,from_thread_id,note,no_answer,prior_hold_state,started_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.CallID, t.ProjectID, t.Source, t.Status, t.TargetKind, t.TargetID, t.TargetName, string(destinations), t.FromPrincipal, t.FromDestinationID, t.FromAgentID, t.FromThreadID, t.Note, t.NoAnswer, t.PriorHoldState, ringTime(t.StartedAt), ringTime(t.ExpiresAt))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, transferRefused(409, "transfer_in_progress", "a transfer is already ringing for this call")
		}
		return nil, err
	}
	if t.PriorHoldState == "active" {
		if _, err = tx.Exec(`UPDATE calls SET hold_state='held',control_error='',updated_at=? WHERE id=?`, ringTime(now), row.ID); err != nil {
			return nil, err
		}
		row.HoldState = "held"
	}
	if err = enqueueTransferEventTx(tx, row, t, "call.transfer_started"); err != nil {
		return nil, err
	}
	// Registered before commit: a call list rebuilt for the new read version
	// must find the transfer (see attachTransfers).
	a.transfers.put(t)
	if err = a.db().commitCall(tx, row.ID); err != nil {
		a.transfers.remove(t)
		return nil, err
	}
	if req.Source == transferSourceAI {
		a.parkCall(row.ID, true)
	} else if t.PriorHoldState == "active" {
		if hub := a.softphones.lookup(row.ID); hub != nil {
			hub.setHeld(true)
			interrupt, _ := json.Marshal(realtimeBridgeControl{Type: "interrupt", Source: "hold"})
			hub.toPeer(ws.OpText, interrupt)
		}
	}
	unlock()
	locked = false
	a.transferChanged(ctx, row.ID)
	if ctx != nil {
		music := ctx.WithProject(row.ProjectID)
		go a.startTransferMusic(music, t)
	}
	return t, nil
}

// parkCall makes the AI on the call hear silence instead of the caller, or
// stops doing so. A bridge that starts while the call is parked is parked too.
func (a *App) parkCall(callID string, parked bool) {
	if parked {
		a.parkedCalls.Store(callID, struct{}{})
	} else {
		a.parkedCalls.Delete(callID)
	}
	if v, ok := a.agentAudioSessions.Load(callID); ok {
		v.(*agentAudioSession).parked.Store(parked)
	}
}

// aiSpeechPending reports whether the AI's speech is still queued for the caller.
func (a *App) aiSpeechPending(callID string) bool {
	v, ok := a.agentAudioSessions.Load(callID)
	if !ok {
		return false
	}
	pending := v.(*agentAudioSession).speechPending.Load()
	return pending != nil && (*pending)()
}

// transferController is the call's hold-music controller, or nil.
func (a *App) transferController(ctx *sdk.AppCtx, row *callRow) carrierCallController {
	if a.callUsesDirectSIP(row) || row.Direction == "outbound" && row.CarrierSlug == "didww" {
		return &localSIPController{app: a}
	}
	if ctx == nil {
		return nil
	}
	adapter, err := a.carrierForRow(ctx.WithProject(row.ProjectID), nil, row)
	if err != nil {
		return nil
	}
	controller, _ := adapter.(carrierCallController)
	return controller
}

// startTransferMusic plays the project's hold music while the transfer rings.
// Without configured hold music the caller hears silence.
func (a *App) startTransferMusic(ctx *sdk.AppCtx, t *callTransfer) {
	if t.Source == transferSourceAI {
		deadline := time.Now().Add(transferSpeechDrain)
		for time.Now().Before(deadline) && !t.isFinished() && a.aiSpeechPending(t.CallID) {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if t.PriorHoldState != "active" || t.isFinished() {
		return // the adviser's own hold music is already playing
	}
	row, err := a.db().findCall(t.CallID)
	if err != nil || row == nil || isTerminalStatus(row.Status) {
		return
	}
	musicURL := row.HoldMusicURL
	if row.HoldMusicStorageFileID > 0 {
		if musicURL, err = holdMusicFromStorage(ctx, row.ProjectID, row.HoldMusicStorageFileID); err != nil {
			ctx.Logger().Warn("transfer hold music unavailable", "call", row.ID, "err", err)
			return
		}
	}
	controller := a.transferController(ctx, row)
	if musicURL == "" || controller == nil {
		return
	}
	commandID := telnyxCommandID(row.ID, "transfer:"+t.ID+":hold")
	row.HoldClientState = base64.StdEncoding.EncodeToString([]byte(commandID))
	if _, err = a.db().db.Exec(`UPDATE calls SET hold_client_state=? WHERE id=? AND hold_state='held'`, row.HoldClientState, row.ID); err != nil {
		return
	}
	err = controller.StartHoldMusic(ctx, row, musicURL, commandID)
	t.mu.Lock()
	t.music = err == nil
	finished := t.finished
	t.mu.Unlock()
	if err != nil {
		ctx.Logger().Warn("transfer hold music failed", "call", row.ID, "err", err)
		return
	}
	if finished {
		a.stopTransferMusic(ctx, t)
	}
}

func (a *App) stopTransferMusic(ctx *sdk.AppCtx, t *callTransfer) {
	t.mu.Lock()
	music := t.music
	t.music = false
	t.mu.Unlock()
	if !music || ctx == nil {
		return
	}
	row, err := a.db().findCall(t.CallID)
	if err != nil || row == nil || isTerminalStatus(row.Status) {
		return
	}
	if controller := a.transferController(ctx, row); controller != nil {
		if err := controller.StopHoldMusic(ctx.WithProject(row.ProjectID), row, telnyxCommandID(row.ID, "transfer:"+t.ID+":resume")); err != nil {
			ctx.Logger().Warn("transfer hold music did not stop", "call", row.ID, "err", err)
		}
	}
}

// ─── End ───────────────────────────────────────────────────────────────────

// endTransfer records the outcome in memory and wakes whoever waits on it.
func (a *App) endTransfer(t *callTransfer, status, reason, endedAt string) {
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		return
	}
	t.finished = true
	t.Status, t.Reason, t.EndedAt = status, reason, endedAt
	t.mu.Unlock()
	a.transfers.remove(t)
	close(t.done)
}

// finishTransfer ends a ringing transfer that nobody answered: no answer,
// canceled, failed or abandoned. The call goes back to whoever transferred
// it, or is hung up when the AI asked for that. It returns false when the
// transfer had already ended.
func (a *App) finishTransfer(ctx *sdk.AppCtx, t *callTransfer, status, reason string) bool {
	unlock := a.softphones.lockClaim(t.CallID)
	row, err := a.db().findCall(t.CallID)
	if err != nil {
		unlock()
		return false
	}
	now := ringTime(time.Now())
	tx, err := a.db().db.Begin()
	if err != nil {
		unlock()
		return false
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE call_transfers SET status=?,reason=?,ended_at=? WHERE id=? AND status='ringing'`, status, reason, now, t.ID)
	if err != nil {
		unlock()
		return false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		unlock()
		a.endTransfer(t, status, reason, now) // ended by another path or process
		return false
	}
	live := row != nil && !isTerminalStatus(row.Status)
	if live && t.PriorHoldState == "active" {
		if _, err = tx.Exec(`UPDATE calls SET hold_state='active',updated_at=? WHERE id=? AND hold_state='held'`, now, row.ID); err != nil {
			unlock()
			return false
		}
		row.HoldState = "active"
	}
	if _, err = tx.Exec(`DELETE FROM telephony_auto_answer_reservations WHERE call_id=?`, t.CallID); err != nil {
		unlock()
		return false
	}
	if row != nil {
		t.mu.Lock()
		t.Status, t.Reason, t.EndedAt = status, reason, now
		t.mu.Unlock()
		if err = enqueueTransferEventTx(tx, row, t, "call.transfer_"+status); err != nil {
			unlock()
			return false
		}
	}
	if err = a.db().commitCall(tx, t.CallID); err != nil {
		unlock()
		return false
	}
	if live && t.Source != transferSourceAI && t.PriorHoldState == "active" {
		if hub := a.softphones.lookup(t.CallID); hub != nil {
			hub.setHeld(false)
		}
	}
	unlock()
	a.endTransfer(t, status, reason, now)
	if t.Source == transferSourceAI {
		a.parkCall(t.CallID, false)
	}
	if live {
		a.stopTransferMusic(ctx, t)
		if t.Source == transferSourceAI && status == transferNoAnswer && t.NoAnswer == "hangup" && ctx != nil {
			if _, message := a.hangupCall(ctx, t.CallID, 0, t.ProjectID, hangupActor{Initiator: terminationByTelephony, Principal: fmt.Sprintf("agent:%d", t.FromAgentID), Action: "transfer_no_answer", Path: "transfer"}); message != "" {
				ctx.Logger().Warn("transfer no-answer hangup failed", "call", t.CallID, "error", message)
			}
		}
	}
	a.transferChanged(ctx, t.CallID)
	return true
}

// transferChanged tells call watchers and the call's browser about a change.
func (a *App) transferChanged(ctx *sdk.AppCtx, callID string) {
	row, err := a.db().findCall(callID)
	if err != nil || row == nil {
		return
	}
	if hub := a.softphones.lookup(row.ID); hub != nil {
		if w := hub.readyBrowserWriter(); w != nil {
			_ = w.Write(ws.OpText, a.transferStatusEvent(row))
		}
	}
	a.callChanges.notify(row.ProjectID)
	if ctx != nil {
		project := ctx.WithProject(row.ProjectID)
		go func() { _ = a.publishLifecycleEvents(project, callID) }()
	}
}

// ─── Answer ────────────────────────────────────────────────────────────────

// transferDestinationFor is the transfer destination p answers through, or "".
// The adviser who transferred the call cannot answer it.
func (a *App) transferDestinationFor(p *phonePrincipal, t *callTransfer, requested string) string {
	if t == nil || t.isFinished() {
		return ""
	}
	if p == nil {
		if requested != "" {
			if t.rings(requested) {
				return requested
			}
			return ""
		}
		if len(t.Destinations) == 0 {
			return ""
		}
		return t.Destinations[0]
	}
	if p.Project != t.ProjectID || p.Identity.key() == t.FromPrincipal {
		return ""
	}
	for _, d := range t.Destinations {
		if (requested == "" || requested == d) && p.Destinations[d] && a.phoneReadDestination(p, t.ProjectID, d) {
			return d
		}
	}
	return ""
}

// answerTransfer is softphoneAnswer for a call whose transfer is ringing. The
// caller holds the call's claim lock.
func (a *App) answerTransfer(row *callRow, p *phonePrincipal, t *callTransfer, dest string) (*softphoneSession, error) {
	var capacity destinationCapacity
	principal, revision := "", int64(0)
	if p != nil {
		fresh, err := a.phonePrincipal(row.ProjectID, p.Identity)
		if err != nil {
			return nil, err
		}
		if !fresh.forAction("call.attach").Destinations[dest] {
			return nil, fmt.Errorf("call not owned by user: %w", errPhoneAccessDenied)
		}
		if capacity, err = a.capacityForIdentity(row.ProjectID, p.Identity); err != nil {
			return nil, fmt.Errorf("%w: %v", errClaimOwnership, err)
		}
		principal, revision = p.Identity.key(), fresh.Revision
	}
	now := ringTime(time.Now())
	db := a.db()
	tx, err := db.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE call_transfers SET status='answered',answered_by=?,answered_destination_id=?,ended_at=? WHERE id=? AND status='ringing'`, principal, dest, now, t.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, transferRefused(409, "transfer_ended", "the transfer is no longer ringing")
	}
	if t.Source == transferSourceAI {
		// The call becomes a softphone call: the carrier bridge's next peer is
		// the softphone hub, reached through the loopback URL.
		res, err = tx.Exec(`UPDATE calls SET peer_kind=?,peer_token=?,audio_bridge_url=?,thread_id='human-'||id,directive='',voice='',hold_state='active',routing_destination_id=?,updated_at=? WHERE id=? AND peer_kind=? AND status IN ('answered','in-progress')`,
			peerKindHuman, newSecret(), a.peerLoopbackURL(row), dest, now, row.ID, peerKindRealtime)
	} else {
		res, err = tx.Exec(`UPDATE calls SET hold_state='active',routing_destination_id=?,updated_at=? WHERE id=? AND peer_kind=? AND status IN ('answered','in-progress')`, dest, now, row.ID, peerKindHuman)
	}
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, transferRefused(409, "call_changed", "the call changed while the transfer rang")
	}
	current, err := scanCall(tx.QueryRow(`SELECT `+callSelectColumns+` FROM calls WHERE id = ?`, row.ID))
	if err != nil {
		return nil, err
	}
	if p != nil {
		if err = setPhoneOwnerTx(tx, current, p, dest, capacity); err != nil {
			return nil, fmt.Errorf("%w: %v", errClaimOwnership, err)
		}
	} else {
		// An operator answer leaves the call unowned, like an operator pick-up.
		if _, err = tx.Exec(`DELETE FROM telephony_call_owners WHERE call_id=?`, row.ID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(`DELETE FROM phone_capacity WHERE call_id=?`, row.ID); err != nil {
			return nil, err
		}
	}
	// The new browser grant replaces the first adviser's, so their socket is
	// refused at its next lease check and cannot rejoin.
	token, query, args, issued := phoneMediaSessionInsert(current, principal, revision, audioSessionCorrelation{InitiatingAction: "answer"})
	var saved string
	if err = tx.QueryRow(query+` RETURNING diagnostic_json`, args...).Scan(&saved); err != nil {
		return nil, fmt.Errorf("%w: %v", errClaimSession, err)
	}
	_ = json.Unmarshal([]byte(saved), &issued)
	if _, err = tx.Exec(`DELETE FROM telephony_auto_answer_reservations WHERE call_id=?`, row.ID); err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.Status, t.EndedAt, t.AnsweredBy, t.AnsweredDestinationID = transferAnswered, now, principal, dest
	t.mu.Unlock()
	if err = enqueueTransferEventTx(tx, current, t, "call.transfer_answered"); err != nil {
		return nil, err
	}
	if err = db.commitCall(tx, row.ID); err != nil {
		return nil, err
	}
	session := a.phoneSessionIssued(current, p, token, issued)
	if hub := a.softphones.lookup(row.ID); hub != nil && t.Source != transferSourceAI {
		// The first adviser's browser stays held until the new one replaces it.
		if browser := hub.browserWriter(); browser != nil {
			_ = browser.Write(ws.OpText, softphoneEventDetail("call.transferred", row.ID, t.ID))
		}
	}
	a.endTransfer(t, transferAnswered, "", now)
	return session, nil
}

// completeAnsweredTransfer runs after the answer response: it moves the
// carrier audio to the softphone for an AI call, stops the hold music and
// ends the AI thread.
func (a *App) completeAnsweredTransfer(ctx *sdk.AppCtx, t *callTransfer) {
	row, err := a.db().findCall(t.CallID)
	if err != nil || row == nil {
		return
	}
	if t.Source == transferSourceAI && !isTerminalStatus(row.Status) {
		switch {
		case a.callUsesDirectSIP(row):
			if !a.requestSIPBridgeHandover(row.ID) {
				ctx.Logger().Warn("transfer found no direct SIP bridge to hand over", "call", row.ID)
			}
		case row.CarrierSlug == "telnyx":
			if err := a.restartTelnyxStreamForTransfer(ctx, t, row); err != nil {
				ctx.Logger().Warn("transfer stream restart failed; media recovery takes over", "call", row.ID, "err", err)
			}
		}
	}
	a.stopTransferMusic(ctx, t)
	if t.Source == transferSourceAI {
		a.parkCall(t.CallID, false)
		if t.FromAgentID != 0 && t.FromThreadID != "" && ctx.PlatformAPI() != nil {
			if err := ctx.PlatformAPI().KillThread(t.FromAgentID, t.FromThreadID); err != nil {
				ctx.Logger().Warn("transferred AI thread did not stop", "call", row.ID, "err", err)
			}
		}
	}
	a.transferChanged(ctx, t.CallID)
}

// restartTelnyxStreamForTransfer replaces the AI's Telnyx media stream with
// one whose peer is the softphone hub. The stop is announced as a planned
// handover, and a carrier command lease holds media recovery off until the
// new stream arrives; if it never does, recovery restarts it.
func (a *App) restartTelnyxStreamForTransfer(ctx *sdk.AppCtx, t *callTransfer, row *callRow) error {
	if ctx.PlatformAPI() == nil {
		return errors.New("platform unavailable")
	}
	carrier, err := a.carrierForRow(ctx.WithProject(row.ProjectID), nil, row)
	if err != nil {
		return err
	}
	restarter, ok := carrier.(carrierStreamRestarter)
	if !ok {
		return errors.New("carrier cannot restart media")
	}
	unlock := a.softphones.lockClaim(row.ID)
	token, err := a.reserveCarrierCommand(row, "transfer-stream", t.ID)
	if err != nil {
		unlock()
		return err
	}
	a.markCarrierHandover(row.ID, time.Now().Add(carrierActivationCommandTimeout+2*time.Second))
	a.mediaBridges.mu.Lock()
	b := a.mediaBridges.current[row.ID]
	a.mediaBridges.mu.Unlock()
	if b != nil {
		b.closeState.SetLeg(mediaCloseLegLocalError, ws.StatusNormalClosure, "call transferred to an adviser")
		b.cancelWithCause("transfer_handover", mediaCloseLegLocalError, 1000, "call transferred to an adviser", "")
		b.closeSockets()
	}
	unlock()
	request, cancel := context.WithTimeout(context.Background(), carrierActivationCommandTimeout)
	defer cancel()
	err = restarter.RestartMedia(request, ctx, row, telnyxCommandID(row.ID, "transfer-stream-"+t.ID))
	if err != nil && !ambiguousCarrierCommandError(err, request) {
		// Recovery may retry at once instead of after the lease.
		a.releaseCarrierCommand(row.ID, token)
	}
	return err
}

// ─── Expiry ────────────────────────────────────────────────────────────────

// runCallTransfers ends transfers nobody answered in time and transfers whose
// call ended. It reads nothing while no transfer rings.
func (a *App) runCallTransfers(_ context.Context, ctx *sdk.AppCtx) error {
	if err := a.restoreCallTransfers(); err != nil {
		return err
	}
	now := time.Now()
	for _, t := range a.transfers.all() {
		row, err := a.db().findCall(t.CallID)
		if err != nil {
			continue
		}
		project := ctx.WithProject(t.ProjectID)
		switch {
		case row == nil || isTerminalStatus(row.Status):
			a.finishTransfer(project, t, transferAbandoned, "the call ended while the transfer rang")
		case t.restored:
			a.finishTransfer(project, t, transferFailed, "telephony restarted while the transfer rang")
		case !now.Before(t.ExpiresAt):
			a.finishTransfer(project, t, transferNoAnswer, "nobody answered")
		}
	}
	return nil
}

// restoreCallTransfers loads, once per process, the transfers a previous
// process left ringing. Its media state is gone, so they end at the next run
// with the normal fallback: hold music stopped and the caller taken off hold.
func (a *App) restoreCallTransfers() error {
	a.transfers.mu.Lock()
	restored := a.transfers.restored
	a.transfers.mu.Unlock()
	if restored {
		return nil
	}
	rows, err := a.db().db.Query(`SELECT id,call_id,project_id,source,target_kind,target_id,target_name,destinations_json,from_principal,from_destination_id,from_agent_id,from_thread_id,note,no_answer,prior_hold_state,started_at FROM call_transfers WHERE status='ringing'`)
	if err != nil {
		return err
	}
	var found []*callTransfer
	for rows.Next() {
		t := &callTransfer{Status: transferRinging, done: make(chan struct{})}
		var destinations, started string
		if err = rows.Scan(&t.ID, &t.CallID, &t.ProjectID, &t.Source, &t.TargetKind, &t.TargetID, &t.TargetName, &destinations, &t.FromPrincipal, &t.FromDestinationID, &t.FromAgentID, &t.FromThreadID, &t.Note, &t.NoAnswer, &t.PriorHoldState, &started); err != nil {
			rows.Close()
			return err
		}
		_ = json.Unmarshal([]byte(destinations), &t.Destinations)
		t.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		t.ExpiresAt = time.Now()
		t.restored = true
		// Music may be playing on a carrier leg that survived the restart.
		t.music = t.PriorHoldState == "active"
		found = append(found, t)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	a.transfers.mu.Lock()
	defer a.transfers.mu.Unlock()
	if a.transfers.restored {
		return nil
	}
	if a.transfers.byCall == nil {
		a.transfers.byCall = map[string]*callTransfer{}
	}
	for _, t := range found {
		if a.transfers.byCall[t.CallID] == nil {
			a.transfers.byCall[t.CallID] = t
		}
	}
	a.transfers.restored = true
	return nil
}

// transferredFromAI reports whether the call's AI handed it to an adviser.
// That AI may no longer hang up the call.
func (a *App) transferredFromAI(callID string) bool {
	var found int
	err := a.db().db.QueryRow(`SELECT 1 FROM call_transfers WHERE call_id=? AND source='ai' AND status='answered' LIMIT 1`, callID).Scan(&found)
	return err == nil
}

// ─── Events ────────────────────────────────────────────────────────────────

// enqueueTransferEventTx adds a transfer event to the call's event outbox. A
// call may be transferred several times, so each event is keyed by transfer.
func enqueueTransferEventTx(tx *sql.Tx, call *callRow, t *callTransfer, topic string) error {
	call.LifecycleRevision++
	if _, err := tx.Exec(`UPDATE calls SET lifecycle_revision = ? WHERE id = ?`, call.LifecycleRevision, call.ID); err != nil {
		return err
	}
	occurredAt := time.Now().UTC().Format(time.RFC3339Nano)
	eventID := fmt.Sprintf("telephony:%s:transfer:%s:%s", call.ID, t.ID, topic)
	payload := lifecycleEventPublic(*call, eventID, topic, occurredAt, lifecycleFacts{Source: "telephony"})
	payload["status"] = call.Status
	payload["transfer"] = t.public()
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO call_events (event_id, call_id, project_id, topic, revision, occurred_at, payload_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, call.ID, call.ProjectID, topic, call.LifecycleRevision, occurredAt, string(encoded), occurredAt)
	return err
}

// validTransferTargetID checks a transfer_targets grant entry: a browser
// destination or a ring group of this project.
func (a *App) validTransferTargetID(project, id string) error {
	d, err := a.findRoutingDestination(project, id)
	if err != nil {
		return err
	}
	if d != nil {
		if d.Kind != "browser" {
			return errors.New("transfer targets must be browser destinations or ring groups")
		}
		return nil
	}
	var found int
	err = a.db().db.QueryRow(`SELECT 1 FROM ring_groups WHERE id=? AND project_id=?`, id, project).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("transfer target must be a browser destination or ring group in this project")
	}
	return err
}

// ─── Call reads ────────────────────────────────────────────────────────────

// offers are the browser offers a ringing transfer makes, one per destination.
func (t *callTransfer) offers() []ringOffer {
	out := make([]ringOffer, 0, len(t.Destinations))
	for _, d := range t.Destinations {
		out = append(out, ringOffer{ID: t.ID, DestinationID: d, Name: t.TargetName, Kind: "browser", ExpiresAt: ringTime(t.ExpiresAt)})
	}
	return out
}

// mayRing is false only when this process knows no transfer is ringing.
func (r *transferRegistry) mayRing() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.restored || len(r.byCall) > 0
}

// attachTransfers adds each connected call's ringing transfer to its row,
// with the transfer's browser offers. It reads nothing while no transfer rings.
func (a *App) attachTransfers(project string, calls []callRow) error {
	if !a.transfers.mayRing() {
		return nil
	}
	indices := map[string]int{}
	args := []any{project}
	for i := range calls {
		if calls[i].Status == "answered" || calls[i].Status == "in-progress" {
			if _, seen := indices[calls[i].ID]; !seen {
				args = append(args, calls[i].ID)
			}
			indices[calls[i].ID] = i
		}
	}
	if len(indices) == 0 {
		return nil
	}
	rows, err := a.db().db.Query(`SELECT id,call_id,source,target_kind,target_id,target_name,destinations_json,from_principal,from_agent_id,note,no_answer,started_at,expires_at FROM call_transfers WHERE status='ringing' AND project_id=? AND call_id IN (`+phonePlaceholders(len(args)-1)+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		t := &callTransfer{ProjectID: project, Status: transferRinging}
		var destinations, started, expires string
		if err := rows.Scan(&t.ID, &t.CallID, &t.Source, &t.TargetKind, &t.TargetID, &t.TargetName, &destinations, &t.FromPrincipal, &t.FromAgentID, &t.Note, &t.NoAnswer, &started, &expires); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(destinations), &t.Destinations)
		t.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		t.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
		if i, ok := indices[t.CallID]; ok {
			calls[i].Transfer = t
			calls[i].RingOffers = t.offers()
		}
	}
	return rows.Err()
}

// ─── Softphone routes ──────────────────────────────────────────────────────

func writeTransferError(w http.ResponseWriter, err error) {
	var refused *transferError
	if errors.As(err, &refused) {
		writeJSONStatus(w, refused.Status, map[string]any{"code": refused.Code, "error": refused.Message})
		return
	}
	writeJSONStatus(w, 500, map[string]any{"code": "transfer_failed", "error": "transfer failed"})
}

// handleSoftphoneTransfer serves GET /softphone/transfer-targets,
// POST /softphone/transfer/{call_id} and POST /softphone/transfer-cancel/{call_id}.
func (a *App) handleSoftphoneTransfer(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		http.Error(w, "app context unavailable", http.StatusServiceUnavailable)
		return
	}
	project, err := a.panelProject(r)
	if err != nil {
		http.Error(w, "project not allowed", http.StatusForbidden)
		return
	}
	p := phoneUserFrom(r)
	path := r.URL.Path
	if path == "/softphone/transfer-targets" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		allowed := func(string) bool { return true }
		if p != nil {
			grants := p.TransferTargets
			allowed = func(id string) bool { return grants[id] }
		}
		targets, err := a.transferTargets(project, allowed)
		if err != nil {
			http.Error(w, "transfer targets unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]any{"targets": targets})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if rest, ok := strings.CutPrefix(path, "/softphone/transfer-cancel/"); ok {
		callID := strings.Trim(rest, "/")
		t := a.transfers.get(callID)
		if t == nil || t.ProjectID != project || t.isFinished() {
			writeTransferError(w, transferRefused(404, "transfer_not_found", "no transfer is ringing for this call"))
			return
		}
		// The adviser who started a transfer may cancel it; an operator may
		// cancel any. The AI's own transfer ends when it is answered or expires.
		if p != nil && (t.FromPrincipal == "" || t.FromPrincipal != p.Identity.key()) {
			writeTransferError(w, transferRefused(403, "not_transfer_owner", "only the adviser who started the transfer can cancel it"))
			return
		}
		if !a.finishTransfer(globalCtx.WithProject(project), t, transferCanceled, "canceled") {
			writeTransferError(w, transferRefused(409, "transfer_ended", "the transfer is no longer ringing"))
			return
		}
		writeJSON(w, map[string]any{"transfer": t.public()})
		return
	}
	callID := strings.Trim(strings.TrimPrefix(path, "/softphone/transfer/"), "/")
	if callID == "" || strings.Contains(callID, "/") {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	var body struct {
		TargetID       string `json:"target_id"`
		Note           string `json:"note"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		http.Error(w, "invalid transfer request", http.StatusBadRequest)
		return
	}
	source := transferSourceOperator
	if p != nil {
		source = transferSourceAdviser
	}
	t, err := a.startTransfer(globalCtx, transferRequest{
		CallID: callID, ProjectID: project, Source: source, TargetID: body.TargetID, Note: body.Note,
		Timeout: time.Duration(body.TimeoutSeconds) * time.Second, From: p,
	})
	if err != nil {
		writeTransferError(w, err)
		return
	}
	writeJSON(w, map[string]any{"transfer": t.public()})
}

// softphoneAnswerTransfer answers a call whose transfer is ringing; the
// caller holds the call's claim lock.
func (a *App) softphoneAnswerTransfer(w http.ResponseWriter, r *http.Request, row *callRow, p *phonePrincipal, t *callTransfer, dest, autoAnswerSession string, warningCompleted bool, record func(string)) {
	dest = a.transferDestinationFor(p, t, dest)
	if dest == "" {
		record("offer_expired")
		writePhoneOfferExpired(w)
		return
	}
	if autoAnswerSession != "" {
		if err := a.validateAutoAnswer(r, row, autoAnswerSession, dest, warningCompleted); err != nil {
			if err.Error() == "auto_answer_warning_required" {
				writeJSONStatus(w, 409, map[string]any{"code": "auto_answer_warning_required"})
				return
			}
			http.Error(w, err.Error(), 409)
			return
		}
	}
	session, err := a.answerTransfer(row, p, t, dest)
	if err != nil {
		record("claim_error")
		var refused *transferError
		switch {
		case errors.As(err, &refused):
			if refused.Code == "transfer_ended" {
				writePhoneOfferExpired(w)
				return
			}
			writeTransferError(w, err)
		case errors.Is(err, errPhoneAccessDenied), errors.Is(err, errClaimSession):
			writePhoneSessionFailure(w, err)
		case errors.Is(err, errClaimOwnership):
			http.Error(w, "ownership unavailable", 500)
		default:
			http.Error(w, "answer transfer: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}
	record("claimed")
	writeJSON(w, session)
	go a.completeAnsweredTransfer(globalCtx.WithProject(row.ProjectID), t)
}

// ─── Agent tools ───────────────────────────────────────────────────────────

func (a *App) toolTransferCall(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	agentID := callerAgentID(callerCtx)
	if agentID == 0 {
		return nil, errors.New("could not determine calling agent id")
	}
	callID := strings.TrimSpace(strArg(args, "call_id", ""))
	if callID == "" {
		return nil, errors.New("call_id required")
	}
	t, err := a.startTransfer(ctx, transferRequest{
		CallID: callID, ProjectID: currentProject(ctx), Source: transferSourceAI, TargetID: strArg(args, "target_id", ""),
		Note: strArg(args, "note", ""), NoAnswer: strings.TrimSpace(strArg(args, "if_no_answer", "return")),
		Timeout: time.Duration(intArg(args, "timeout_seconds", 0)) * time.Second, AgentID: agentID,
	})
	if err != nil {
		var refused *transferError
		if errors.As(err, &refused) {
			return nil, fmt.Errorf("%s: %s", refused.Code, refused.Message)
		}
		return nil, err
	}
	select {
	case <-t.done:
	case <-callerCtx.Done():
		// The transfer keeps ringing; its outcome is in the call's events.
		return map[string]any{"transfer": t.public(), "message": "The transfer is still ringing."}, nil
	}
	status, _ := t.outcome()
	result := map[string]any{"transfer": t.public()}
	switch status {
	case transferAnswered:
		result["message"] = "An adviser answered and now has the call. Your part of the call is over: do not speak, and do not hang up."
	case transferNoAnswer:
		if t.NoAnswer == "hangup" {
			result["message"] = "Nobody answered, so the call was ended."
		} else {
			result["message"] = "Nobody answered. You are back with the caller: tell them nobody was available and carry on."
		}
	case transferAbandoned:
		result["message"] = "The caller hung up while the transfer rang."
	default:
		result["message"] = "The transfer did not complete. You are back with the caller."
	}
	return result, nil
}

func (a *App) toolTransferTargets(callerCtx context.Context, ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	if callerAgentID(callerCtx) == 0 {
		return nil, errors.New("could not determine calling agent id")
	}
	project := currentProject(ctx)
	targets, err := a.transferTargets(project, aiTransferAllowed(ctx, project))
	if err != nil {
		return nil, err
	}
	return map[string]any{"targets": targets}, nil
}

// transferStatusEvent is the browser's call.status event with the call's
// ringing transfer, or a null transfer once none rings.
func (a *App) transferStatusEvent(row *callRow) []byte {
	var payload map[string]any
	_ = json.Unmarshal(softphoneStatusEvent(*row), &payload)
	payload["transfer"] = nil
	if t := a.transfers.get(row.ID); t != nil && !t.isFinished() {
		payload["transfer"] = t.public()
	}
	encoded, _ := json.Marshal(payload)
	return encoded
}
