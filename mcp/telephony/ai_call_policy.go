package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	terminationAIInactivity    = "ai_inactivity"
	terminationAIMaxDuration   = "ai_max_duration"
	terminationAIPolicyFailure = "ai_policy_failure"
	aiReminderDeliveryLimit    = 30 * time.Second
)

type aiPolicyEvent struct {
	At     string `json:"at"`
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}
type aiPolicyState struct {
	Revision         int64           `json:"revision"`
	Policy           aiCallPolicy    `json:"policy"`
	CreatedAt        time.Time       `json:"created_at"`
	StartedAt        time.Time       `json:"started_at,omitempty"`
	Deadline         time.Time       `json:"deadline,omitempty"`
	Stage            string          `json:"stage"`
	IdleSince        time.Time       `json:"idle_since,omitempty"`
	ReminderAt       time.Time       `json:"reminder_requested_at,omitempty"`
	ReminderToken    string          `json:"reminder_token,omitempty"`
	ReminderSent     bool            `json:"reminder_accepted"`
	ReminderIssued   bool            `json:"reminder_issued"`
	ResponseDeadline time.Time       `json:"response_deadline,omitempty"`
	Reason           string          `json:"termination_reason,omitempty"`
	Attempts         int             `json:"termination_attempts"`
	NextAttempt      time.Time       `json:"next_attempt_at,omitempty"`
	LastError        string          `json:"last_error,omitempty"`
	Events           []aiPolicyEvent `json:"events"`
}

func (s *aiPolicyState) event(now time.Time, kind, detail string) {
	s.Events = append(s.Events, aiPolicyEvent{now.UTC().Format(time.RFC3339Nano), kind, detail})
	if len(s.Events) > 32 {
		s.Events = s.Events[len(s.Events)-32:]
	}
}

type aiPolicyRuntime struct {
	dirty                    bool
	mu                       sync.Mutex
	id, project, thread      string
	agent                    int64
	state                    aiPolicyState
	phase                    string
	phaseAt                  time.Time
	lastActivity, lastOutput time.Time
	reminderAudio            bool
	playback                 func() bool
	generation               string
	// Never restored as listening from disk: a new live phase is required.
	feedLive bool
}
type aiPolicyRegistry struct {
	mu       sync.RWMutex
	calls    map[string]*aiPolicyRuntime
	feeds    map[int64]*aiPhaseFeed
	commands chan struct{}
	stopped  bool
}

func (a *App) aiRuntime(row *callRow) *aiPolicyRuntime {
	if row == nil || row.PeerKind == peerKindHuman || row.PeerKind == peerKindExternal {
		return nil
	}
	a.aiPolicies.mu.RLock()
	defer a.aiPolicies.mu.RUnlock()
	r := a.aiPolicies.calls[row.ID]
	if r == nil || r.thread != row.ThreadID || r.project != row.ProjectID {
		return nil
	}
	return r
}
func (a *App) registerAIPolicy(ctx *sdk.AppCtx, row *callRow, thread string, policy aiCallPolicy, now time.Time) error {
	s := aiPolicyState{Policy: policy, CreatedAt: now, Stage: "prepared", Events: []aiPolicyEvent{}}
	var raw string
	err := a.db().db.QueryRow(`SELECT state_json FROM ai_call_policies WHERE call_id=? AND project_id=?`, row.ID, row.ProjectID).Scan(&raw)
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &s); err != nil {
			return err
		}
		if s.Stage == "ended" || s.Stage == "disarmed" {
			return errors.New("AI policy already ended")
		}
		if !s.StartedAt.IsZero() {
			return errors.New("AI policy cannot be rearmed after media connection")
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	s.Revision++
	s.event(now, "prepared", "")
	encoded, _ := json.Marshal(s)
	if _, err = a.db().db.Exec(`INSERT INTO ai_call_policies(call_id,project_id,thread_id,revision,stage,state_json,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(call_id) DO UPDATE SET thread_id=excluded.thread_id,revision=excluded.revision,state_json=excluded.state_json,updated_at=excluded.updated_at WHERE ai_call_policies.stage='prepared'`, row.ID, row.ProjectID, thread, s.Revision, s.Stage, string(encoded), ringTime(now)); err != nil {
		return err
	}
	a.aiPolicies.mu.Lock()
	if a.aiPolicies.stopped {
		a.aiPolicies.mu.Unlock()
		return errors.New("AI policy is stopping")
	}
	if a.aiPolicies.calls == nil {
		a.aiPolicies.calls = map[string]*aiPolicyRuntime{}
	}
	if a.aiPolicies.commands == nil {
		a.aiPolicies.commands = make(chan struct{}, 4)
	}
	a.aiPolicies.calls[row.ID] = &aiPolicyRuntime{id: row.ID, project: row.ProjectID, thread: thread, agent: row.AgentID, state: s}
	a.aiPolicies.mu.Unlock()
	a.ensureAIPhaseFeed(ctx, row.AgentID)
	return nil
}

// These observations use the AI frontend's existing VAD and playback tracker.
// No additional audio decoding, buffering, database writes or model calls on the hot path.
func (a *App) observeAICaller(row *callRow, speech bool, now time.Time, generations ...string) {
	if !speech {
		return
	}
	if r := a.aiRuntime(row); r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if len(generations) > 0 && generations[0] != r.generation {
			return
		}
		r.activity(now)
	}
}
func (r *aiPolicyRuntime) activity(now time.Time) {
	if r.state.Stage == "terminating" || r.state.Stage == "ended" || r.state.Stage == "disarmed" {
		return
	}
	r.lastActivity = now
	if !r.state.IdleSince.IsZero() || r.state.ReminderToken != "" {
		r.dirty = true
	}
	r.state.IdleSince = time.Time{}
	if r.state.ReminderToken != "" {
		r.state.event(now, "caller_response", "")
		r.state.Stage = "active"
		r.state.ReminderToken = ""
		r.state.ReminderSent = false
		r.state.ReminderIssued = false
		r.state.ReminderAt = time.Time{}
		r.state.ResponseDeadline = time.Time{}
		r.reminderAudio = false
	}
}
func (a *App) observeAIOutput(row *callRow, now time.Time, generations ...string) {
	if r := a.aiRuntime(row); r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if len(generations) > 0 && generations[0] != r.generation {
			return
		}
		r.lastOutput = now
		if r.state.Stage == "reminder" {
			r.reminderAudio = true
		}
	}
}
func (a *App) observeAIJSONOutput(row *callRow, payload []byte, now time.Time, generation string) {
	if a.aiRuntime(row) == nil {
		return
	}
	var frame struct {
		Event     string `json:"event"`
		EventType string `json:"eventType"`
	}
	if json.Unmarshal(payload, &frame) == nil && (frame.Event == "media" || frame.Event == "playAudio" || frame.EventType == "playAudio") {
		a.observeAIOutput(row, now, generation)
	}
}
func (a *App) bindAIPlayback(row *callRow, generation string, pending func() bool) {
	if r := a.aiRuntime(row); r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.generation != generation {
			if !r.state.IdleSince.IsZero() || !r.state.ResponseDeadline.IsZero() {
				r.dirty = true
			}
			r.state.IdleSince = time.Time{}
			r.state.ResponseDeadline = time.Time{}
		}
		r.generation = generation
		r.playback = pending
	}
}
func (a *App) observeAIPhase(event sdk.TelemetryStreamEvent) {
	var data struct {
		State string `json:"state"`
		Phase string `json:"phase"`
	}
	if json.Unmarshal(event.Data, &data) != nil {
		return
	}
	a.aiPolicies.mu.RLock()
	defer a.aiPolicies.mu.RUnlock()
	for _, r := range a.aiPolicies.calls {
		if r.agent != event.AgentID || r.thread != event.ThreadID {
			continue
		}
		r.mu.Lock()
		at := event.Time
		if at.IsZero() || at.After(time.Now().Add(time.Second)) {
			at = time.Now().UTC()
		}
		if at.Before(r.phaseAt) {
			r.mu.Unlock()
			continue
		}
		r.phaseAt = at
		if event.Type == "realtime.user" {
			r.activity(at)
		} else if event.Type == "realtime.state" {
			// Only an explicit listening state means waiting for the caller. Working,
			// thinking, speaking, waiting/hold, disconnected and unknown all pause it.
			r.feedLive = true
			r.phase = data.State
			if data.Phase == "hold" || data.Phase == "paused" {
				r.phase = "hold"
			}
			if r.phase != "listening" {
				if !r.state.IdleSince.IsZero() || !r.state.ResponseDeadline.IsZero() {
					r.dirty = true
				}
				r.state.IdleSince = time.Time{}
				r.state.ResponseDeadline = time.Time{}
			}
		}
		r.mu.Unlock()
	}
}

// Pure state transition; caller activity is serialized with termination commit.
func (r *aiPolicyRuntime) advance(row *callRow, now time.Time) string {
	s := &r.state
	if s.Stage == "ended" || s.Stage == "disarmed" {
		return ""
	}
	if isTerminalStatus(row.Status) || row.PeerKind != peerKindRealtime || row.ThreadID != r.thread && !strings.HasPrefix(row.ThreadID, "pending-") {
		s.Stage = "disarmed"
		s.event(now, "disarmed", "call ended or AI ownership changed")
		return ""
	}
	if s.Stage == "terminating" {
		if s.Attempts < 3 && !now.Before(s.NextAttempt) {
			return "terminate"
		}
		return ""
	}
	if row.ThreadID != r.thread {
		return ""
	}
	if s.StartedAt.IsZero() {
		if row.MediaStatus != "connected" {
			return ""
		}
		s.StartedAt = s.CreatedAt
		if connected, err := time.Parse(time.RFC3339Nano, row.MediaConnectedAt); err == nil && connected.After(s.StartedAt) {
			s.StartedAt = connected
		}
		s.Deadline = s.StartedAt.Add(time.Duration(s.Policy.MaxDurationSeconds) * time.Second)
		s.Stage = "active"
		s.event(now, "started", "")
	}
	if !now.Before(s.Deadline) {
		s.Reason = terminationAIMaxDuration
		return "terminate"
	}
	playable := row.MediaStatus == "connected" && row.HoldState != "held" && row.HoldState != "holding" && r.feedLive && r.phase == "listening" && r.playback != nil && !r.playback() && (r.lastOutput.IsZero() || now.Sub(r.lastOutput) >= 500*time.Millisecond)
	if !playable {
		s.IdleSince = time.Time{}
		s.ResponseDeadline = time.Time{}
	}
	if s.Stage == "reminder" {
		if !s.ReminderIssued {
			if playable {
				return "remind"
			}
			return ""
		}
		if s.ReminderSent && r.reminderAudio && playable {
			if s.ResponseDeadline.IsZero() {
				s.ResponseDeadline = now.Add(time.Duration(s.Policy.ResponseWindowSeconds) * time.Second)
				s.event(now, "reminder_playback_completed", "provider acknowledgement or paced playback drain")
			}
			if !now.Before(s.ResponseDeadline) {
				s.Reason = terminationAIInactivity
				return "terminate"
			}
		} else if r.feedLive && (r.phase == "listening" || r.phase == "speaking") && row.HoldState != "held" && row.HoldState != "holding" && row.MediaStatus == "connected" && s.ResponseDeadline.IsZero() && !s.ReminderAt.IsZero() && now.Sub(s.ReminderAt) >= aiReminderDeliveryLimit && (!s.ReminderSent || !r.reminderAudio) {
			s.Reason = terminationAIPolicyFailure
			s.LastError = "inactivity reminder could not be delivered"
			return "terminate"
		}
		return ""
	}
	if !playable || s.Policy.InactivityTimeoutSeconds == 0 {
		return ""
	}
	if s.IdleSince.IsZero() {
		s.IdleSince = now
		return ""
	}
	if !r.lastActivity.IsZero() && r.lastActivity.After(s.IdleSince) {
		s.IdleSince = r.lastActivity
	}
	if now.Sub(s.IdleSince) < time.Duration(s.Policy.InactivityTimeoutSeconds)*time.Second {
		return ""
	}
	s.Stage = "reminder"
	s.ReminderAt = now
	s.ReminderToken = newSecret()
	s.ReminderSent = false
	s.ReminderIssued = false
	s.ResponseDeadline = time.Time{}
	r.reminderAudio = false
	s.event(now, "reminder_requested", "")
	return "remind"
}
func (a *App) persistAIPolicy(r *aiPolicyRuntime, s aiPolicyState) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = a.db().db.Exec(`UPDATE ai_call_policies SET revision=?,stage=?,state_json=?,updated_at=? WHERE call_id=? AND project_id=? AND thread_id=? AND revision<=?`, s.Revision, s.Stage, string(raw), ringTime(time.Now()), r.id, r.project, r.thread, s.Revision)
	return err
}
func (a *App) runAICallPolicies(_ context.Context, ctx *sdk.AppCtx) error {
	return a.runAICallPoliciesAt(ctx, time.Now().UTC())
}
func (a *App) runAICallPoliciesAt(ctx *sdk.AppCtx, now time.Time) error {
	rows, err := a.db().db.Query(`SELECT p.call_id,p.thread_id,p.state_json,COALESCE(c.thread_id,''),COALESCE(c.agent_id,0),COALESCE(c.peer_kind,''),COALESCE(c.status,''),COALESCE(c.media_status,''),COALESCE(c.media_connected_at,''),COALESCE(c.hold_state,'') FROM ai_call_policies p LEFT JOIN calls c ON c.id=p.call_id AND c.project_id=p.project_id WHERE p.project_id=? AND p.stage NOT IN ('ended','disarmed')`, ctx.CurrentProject())
	if err != nil {
		return err
	}
	type saved struct {
		id, thread, raw string
		row             callRow
	}
	var savedRows []saved
	for rows.Next() {
		var s saved
		if err = rows.Scan(&s.id, &s.thread, &s.raw, &s.row.ThreadID, &s.row.AgentID, &s.row.PeerKind, &s.row.Status, &s.row.MediaStatus, &s.row.MediaConnectedAt, &s.row.HoldState); err != nil {
			rows.Close()
			return err
		}
		savedRows = append(savedRows, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, saved := range savedRows {
		row := &saved.row
		row.ID = saved.id
		row.ProjectID = ctx.CurrentProject()
		if row.Status == "" {
			if _, err = a.db().db.Exec(`DELETE FROM ai_call_policies WHERE call_id=? AND project_id=?`, row.ID, row.ProjectID); err != nil {
				return err
			}
			a.aiPolicies.mu.Lock()
			delete(a.aiPolicies.calls, row.ID)
			a.aiPolicies.mu.Unlock()
			continue
		}
		a.aiPolicies.mu.Lock()
		if a.aiPolicies.calls == nil {
			a.aiPolicies.calls = map[string]*aiPolicyRuntime{}
		}
		if a.aiPolicies.commands == nil {
			a.aiPolicies.commands = make(chan struct{}, 4)
		}
		r := a.aiPolicies.calls[saved.id]
		if r == nil {
			var s aiPolicyState
			if err = json.Unmarshal([]byte(saved.raw), &s); err != nil {
				a.aiPolicies.mu.Unlock()
				return err
			}
			s.IdleSince = time.Time{}
			s.ResponseDeadline = time.Time{}
			// A dispatched reminder is never blindly replayed after process loss.
			r = &aiPolicyRuntime{dirty: true, id: row.ID, project: row.ProjectID, thread: saved.thread, agent: row.AgentID, state: s}
			a.aiPolicies.calls[row.ID] = r
		}
		a.aiPolicies.mu.Unlock()
		a.ensureAIPhaseFeed(ctx, row.AgentID)
		r.mu.Lock()
		before, _ := json.Marshal(r.state)
		action := r.advance(row, now)
		after, _ := json.Marshal(r.state)
		changed := r.dirty || string(before) != string(after)
		if changed {
			r.state.Revision++
			r.dirty = false
		}
		snapshot := r.state
		r.mu.Unlock()
		if changed {
			if err = a.persistAIPolicy(r, snapshot); err != nil {
				return err
			}
		}
		if action == "remind" {
			a.dispatchAIReminder(ctx, r, snapshot.ReminderToken)
		}
		if action == "terminate" {
			if err = a.terminateAIPolicy(ctx, r, now); err != nil {
				ctx.Logger().Warn("AI policy carrier termination", "call", r.id, "error", err)
			}
		}
		if snapshot.Stage == "disarmed" || snapshot.Stage == "ended" {
			if snapshot.Stage == "disarmed" {
				_, err = a.db().db.Exec(`UPDATE calls SET termination_reason='',termination_cause='',termination_initiator='' WHERE id=? AND project_id=? AND status NOT IN ('completed','failed','no-answer','busy','canceled') AND (thread_id<>? OR peer_kind<>'realtime') AND termination_initiator='telephony' AND termination_reason IN ('ai_max_duration','ai_inactivity','ai_policy_failure')`, r.id, r.project, r.thread)
				if err != nil {
					return err
				}
			}
			a.aiPolicies.mu.Lock()
			delete(a.aiPolicies.calls, r.id)
			a.aiPolicies.mu.Unlock()
		}
	}
	a.pruneAIPhaseFeeds()
	return nil
}
func (a *App) dispatchAIReminder(ctx *sdk.AppCtx, r *aiPolicyRuntime, token string) {
	a.aiPolicies.mu.RLock()
	commands := a.aiPolicies.commands
	stopped := a.aiPolicies.stopped
	a.aiPolicies.mu.RUnlock()
	if stopped {
		return
	}
	select {
	case commands <- struct{}{}:
	default:
		return
	} // No unbounded goroutine queue.
	go func() {
		defer func() { <-commands }()
		unlock := a.softphones.lockClaim(r.id)
		row, err := a.db().findCall(r.id)
		r.mu.Lock()
		valid := err == nil && row != nil && !isTerminalStatus(row.Status) && row.PeerKind == peerKindRealtime && row.ThreadID == r.thread && r.state.Stage == "reminder" && r.state.ReminderToken == token && r.feedLive && r.phase == "listening" && r.playback != nil && !r.playback()
		r.mu.Unlock()
		if !valid {
			unlock()
			return
		}
		// Mark one issuance while holding the same owner lock as transfers/answers.
		// Issuance is durable before the bounded SDK request; release the owner
		// lock before network I/O so a transfer/cancel remains responsive.
		r.mu.Lock()
		if r.state.ReminderIssued {
			r.mu.Unlock()
			unlock()
			return
		}
		r.state.ReminderIssued = true
		r.state.ReminderAt = time.Now().UTC()
		r.state.Revision++
		snapshot := r.state
		r.mu.Unlock()
		if err = a.persistAIPolicy(r, snapshot); err != nil {
			r.mu.Lock()
			if r.state.ReminderToken == token {
				r.state.ReminderIssued = false
			}
			r.mu.Unlock()
			unlock()
			return
		}
		unlock()
		client, ok := ctx.PlatformAPI().(sdk.ThreadClient)
		if !ok {
			err = errors.New("thread event API unavailable")
		} else {
			err = client.SendThreadEvent(sdk.ThreadRef{AgentID: r.agent, ThreadID: r.thread}, "Telephony inactivity reminder: if the caller is still silent and you are not doing a tool operation, say one brief 'Are you still there?' in their language, then listen. Do not repeat this prompt.")
		}
		r.mu.Lock()
		if r.state.Stage == "reminder" && r.state.ReminderToken == token {
			r.dirty = true
			r.state.ReminderSent = err == nil
			if err != nil {
				r.state.LastError = "inactivity reminder event failed"
				r.state.event(time.Now(), "reminder_delivery_failed", "")
			}
		}
		r.mu.Unlock()
	}()
}
func (a *App) terminateAIPolicy(ctx *sdk.AppCtx, r *aiPolicyRuntime, now time.Time) error {
	unlock := a.softphones.lockClaim(r.id)
	defer unlock()
	row, err := a.db().findCall(r.id)
	if err != nil {
		return err
	}
	if row == nil {
		return nil
	}
	r.mu.Lock()
	if r.advance(row, now) != "terminate" {
		r.mu.Unlock()
		return nil
	}
	if row.ThreadID != r.thread || row.PeerKind != peerKindRealtime || isTerminalStatus(row.Status) {
		r.mu.Unlock()
		return nil
	}
	r.state.Stage = "terminating"
	r.state.Attempts++
	r.state.NextAttempt = now.Add(time.Duration(5*r.state.Attempts) * time.Second)
	r.state.event(now, "termination_requested", r.state.Reason)
	r.state.Revision++
	snapshot := r.state
	r.mu.Unlock()
	if err = a.persistAIPolicy(r, snapshot); err != nil {
		return err
	}
	if _, err = a.db().db.Exec(`UPDATE calls SET termination_reason=?,termination_cause=?,termination_initiator='telephony' WHERE id=? AND thread_id=? AND peer_kind='realtime' AND status NOT IN ('completed','failed','busy','no-answer','canceled')`, snapshot.Reason, snapshot.Reason, row.ID, r.thread); err != nil {
		return err
	}
	if row.CarrierSID == "" && !a.callUsesDirectSIP(row) {
		err = errors.New("carrier identity missing")
	} else {
		err = a.terminateCarrierCall(ctx, row)
	}
	if err != nil {
		r.mu.Lock()
		r.state.LastError = "carrier hangup failed"
		r.state.event(now, "carrier_termination_failed", "")
		r.state.Revision++
		snapshot = r.state
		r.mu.Unlock()
		_ = a.persistAIPolicy(r, snapshot)
		return fmt.Errorf("carrier termination: %w", err)
	}
	_ = a.killCallThread(ctx, row)
	status, message := "completed", ""
	if snapshot.Reason == terminationAIPolicyFailure {
		status, message = "failed", snapshot.LastError
	}
	if _, err = a.db().updateStatusWithFacts(row.ID, status, message, lifecycleFacts{Source: "telephony", OccurredAt: ringTime(now), ExpectedThreadID: r.thread, TerminationCause: snapshot.Reason, TerminationInitiator: "telephony"}); err != nil {
		return err
	}
	r.mu.Lock()
	r.state.Stage = "ended"
	r.state.event(now, "ended", snapshot.Reason)
	r.state.Revision++
	snapshot = r.state
	r.mu.Unlock()
	return a.persistAIPolicy(r, snapshot)
}
func (a *App) aiPolicyDiagnostics(project, id string) (any, error) {
	var raw string
	err := a.db().db.QueryRow(`SELECT state_json FROM ai_call_policies WHERE project_id=? AND call_id=?`, project, id).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var s aiPolicyState
	err = json.Unmarshal([]byte(raw), &s)
	// Ownership tokens are internal and never exposed in API diagnostics.
	s.ReminderToken = ""
	return s, err
}

// Hydrate before accepting media so the first replacement bridge can register
// its playback tracker. Restored phases remain unknown until fresh live evidence.
func (a *App) restoreAIPolicyRuntimes() error {
	rows, err := a.db().db.Query(`SELECT p.call_id,p.project_id,p.thread_id,p.state_json,c.agent_id FROM ai_call_policies p JOIN calls c ON c.id=p.call_id AND c.project_id=p.project_id WHERE p.stage NOT IN ('ended','disarmed') AND c.status NOT IN ('completed','failed','no-answer','busy','canceled')`)
	if err != nil {
		return err
	}
	var restored []*aiPolicyRuntime
	for rows.Next() {
		r := &aiPolicyRuntime{dirty: true}
		var raw string
		if err = rows.Scan(&r.id, &r.project, &r.thread, &raw, &r.agent); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal([]byte(raw), &r.state); err != nil {
			rows.Close()
			return err
		}
		r.state.IdleSince = time.Time{}
		r.state.ResponseDeadline = time.Time{}
		restored = append(restored, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	a.aiPolicies.mu.Lock()
	defer a.aiPolicies.mu.Unlock()
	if a.aiPolicies.calls == nil {
		a.aiPolicies.calls = map[string]*aiPolicyRuntime{}
	}
	if a.aiPolicies.commands == nil {
		a.aiPolicies.commands = make(chan struct{}, 4)
	}
	for _, r := range restored {
		a.aiPolicies.calls[r.id] = r
	}
	return nil
}
