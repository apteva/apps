package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ─── lifecycle engine ───────────────────────────────────────────────
//
// Six fixed phases; each work type uses an ordered subset and may add
// its own statuses mapped to a phase. Every status change goes through
// transition(), which checks the guards, writes the timeline and
// returns the event to emit. Side states (on_hold, needs_owner) park
// the work and remember where it was; cancelled ends it.

var defaultStatuses = map[string][]string{
	"intake":   {"new"},
	"quote":    {"quoting", "quote_sent"},
	"schedule": {"to_schedule"},
	"execute":  {"scheduled", "in_progress"},
	"bill":     {"to_bill", "invoiced"},
	"closed":   {"closed"},
}

const (
	statusOnHold     = "on_hold"
	statusNeedsOwner = "needs_owner"
	statusCancelled  = "cancelled"
)

func phaseIndex(p string) int {
	for i, x := range phaseOrder {
		if x == p {
			return i
		}
	}
	return -1
}

// statusPhase maps a status key to its phase for this work type.
func statusPhase(wt *WorkType, status string) (string, bool) {
	for _, s := range wt.Statuses {
		if s.Key == status {
			return s.Phase, true
		}
	}
	for phase, list := range defaultStatuses {
		if contains(list, status) && wt.usesPhase(phase) {
			return phase, true
		}
	}
	if wt.usesPhase(status) {
		return status, true // a phase name moves to that phase's first status
	}
	return "", false
}

func firstStatus(wt *WorkType, phase string) string {
	if l := defaultStatuses[phase]; len(l) > 0 {
		return l[0]
	}
	return phase
}

func nextPhase(wt *WorkType, phase string) string {
	for i, p := range wt.Phases {
		if p == phase && i+1 < len(wt.Phases) {
			return wt.Phases[i+1]
		}
	}
	return ""
}

func allowedStatuses(wt *WorkType) map[string][]string {
	out := map[string][]string{}
	for _, p := range wt.Phases {
		out[p] = append(out[p], defaultStatuses[p]...)
	}
	for _, s := range wt.Statuses {
		if !contains(out[s.Phase], s.Key) {
			out[s.Phase] = append(out[s.Phase], s.Key)
		}
	}
	return out
}

type GuardError struct {
	To      string   `json:"to"`
	Reasons []string `json:"reasons"`
}

func (g *GuardError) Error() string {
	return fmt.Sprintf("cannot move to %s: %s", g.To, strings.Join(g.Reasons, "; "))
}

type TransitionResult struct {
	Work  *Work  `json:"work"`
	From  string `json:"from"`
	To    string `json:"to"`
	Phase string `json:"phase"`
	Event string `json:"event,omitempty"`
}

// transition moves work to a status (or a phase name, or one of
// on_hold / needs_owner / resume / cancelled).
func transition(db *sql.DB, pid string, w *Work, to, actor, reason string, payload map[string]any) (*TransitionResult, error) {
	wt, err := getWorkType(db, pid, w.WorkTypeID, "")
	if err != nil {
		return nil, notFound(err, "work type")
	}
	to = strings.TrimSpace(to)
	from := w.Status
	if w.Phase == "closed" || w.Phase == statusCancelled {
		return nil, &GuardError{To: to, Reasons: []string{"work is " + w.Status}}
	}
	targetPhase, targetStatus, holdFrom := w.Phase, to, w.HoldFromStatus
	switch to {
	case statusOnHold, statusNeedsOwner:
		if w.Status != statusOnHold && w.Status != statusNeedsOwner {
			holdFrom = w.Status
		}
	case "resume":
		if w.Status != statusOnHold && w.Status != statusNeedsOwner {
			return nil, &GuardError{To: to, Reasons: []string{"work is not on hold"}}
		}
		targetStatus, holdFrom = w.HoldFromStatus, ""
		if targetStatus == "" {
			targetStatus = firstStatus(wt, w.Phase)
		}
	case statusCancelled, "cancel":
		targetPhase, targetStatus, holdFrom = statusCancelled, statusCancelled, ""
	default:
		if w.Status == statusOnHold || w.Status == statusNeedsOwner {
			return nil, &GuardError{To: to, Reasons: []string{"work is " + w.Status + "; resume it first"}}
		}
		p, ok := statusPhase(wt, to)
		if !ok {
			return nil, &GuardError{To: to, Reasons: []string{fmt.Sprintf("unknown status for work type %s; allowed: %v", wt.Key, allowedStatuses(wt))}}
		}
		if p == to {
			targetStatus = firstStatus(wt, p)
		}
		targetPhase = p
		ci, ti := phaseIndex(w.Phase), phaseIndex(p)
		switch {
		case ti == ci:
		case p == nextPhase(wt, w.Phase):
		case p == "schedule" && w.Phase == "execute":
			// re-plan: allowed when nothing is in progress
			if n := countSlots(db, w.ID, "in_progress"); n > 0 {
				return nil, &GuardError{To: to, Reasons: []string{"a slot is in progress"}}
			}
		default:
			return nil, &GuardError{To: to, Reasons: []string{fmt.Sprintf("from phase %s the next phase is %s", w.Phase, nextPhase(wt, w.Phase))}}
		}
		if ti != ci {
			if reasons := phaseGuards(db, pid, w, wt, p); len(reasons) > 0 {
				return nil, &GuardError{To: to, Reasons: reasons}
			}
		}
	}
	now := nowUTC()
	var closedAt any
	if targetPhase == "closed" || targetPhase == statusCancelled {
		closedAt = now
	}
	if _, err := db.Exec(`UPDATE work SET phase=?, status=?, hold_from_status=?, updated_at=?, closed_at=COALESCE(?, closed_at) WHERE project_id=? AND id=?`,
		targetPhase, targetStatus, holdFrom, now, closedAt, pid, w.ID); err != nil {
		return nil, err
	}
	if targetPhase == statusCancelled {
		if _, err := db.Exec(`UPDATE slots SET status='cancelled', updated_at=? WHERE work_id=? AND status IN ('booked','in_progress')`, now, w.ID); err != nil {
			return nil, err
		}
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if reason != "" {
		payload["reason"] = reason
	}
	if err := logEvent(db, pid, w.ID, actor, "status", from, targetStatus, payload); err != nil {
		return nil, err
	}
	nw, err := getWork(db, pid, w.ID)
	if err != nil {
		return nil, err
	}
	*w = *nw
	ev := "work.status_changed"
	switch {
	case targetPhase == "closed":
		ev = "work.closed"
	case targetStatus == statusNeedsOwner:
		ev = "work.needs_owner"
	case targetPhase != phaseOfStatus(wt, from, w):
		ev = "work.phase_changed"
	}
	return &TransitionResult{Work: nw, From: from, To: targetStatus, Phase: targetPhase, Event: ev}, nil
}

func phaseOfStatus(wt *WorkType, status string, w *Work) string {
	if p, ok := statusPhase(wt, status); ok {
		return p
	}
	return w.Phase
}

// phaseGuards are the checks for ENTERING a phase.
func phaseGuards(db *sql.DB, pid string, w *Work, wt *WorkType, phase string) []string {
	var reasons []string
	switch phase {
	case "schedule":
		if wt.usesPhase("quote") && w.QuoteStatus != "accepted" {
			reasons = append(reasons, "the quote must be accepted (quote_status is "+orNone(w.QuoteStatus)+")")
		}
	case "execute":
		if countSlots(db, w.ID, "booked", "in_progress") == 0 {
			reasons = append(reasons, "book a slot first (schedule_book)")
		}
		if wt.usesPhase("quote") && w.QuoteStatus != "accepted" {
			reasons = append(reasons, "the quote must be accepted")
		}
	case "bill":
		if n := countSlots(db, w.ID, "booked", "in_progress"); n > 0 {
			reasons = append(reasons, fmt.Sprintf("%d slot(s) still open; complete or cancel them", n))
		}
		if wt.usesPhase("execute") && countSlots(db, w.ID, "done") == 0 {
			reasons = append(reasons, "no completed slot")
		}
	case "closed":
		if wt.usesPhase("bill") && w.InvoiceRef == "" && w.PaidAt == "" {
			reasons = append(reasons, "invoice or record payment first")
		}
		if !wt.usesPhase("bill") && wt.usesPhase("execute") {
			if n := countSlots(db, w.ID, "booked", "in_progress"); n > 0 {
				reasons = append(reasons, fmt.Sprintf("%d slot(s) still open", n))
			}
		}
	}
	for _, key := range wt.RequiredForms[phase] {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM form_responses WHERE work_id=? AND form_key=?`, w.ID, key).Scan(&n)
		if n == 0 {
			reasons = append(reasons, "form "+key+" must be submitted")
		}
	}
	return reasons
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func countSlots(db *sql.DB, workID int64, statuses ...string) int {
	q := `SELECT COUNT(*) FROM slots WHERE work_id=? AND status IN (` + strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",") + `)`
	args := []any{workID}
	for _, s := range statuses {
		args = append(args, s)
	}
	var n int
	_ = db.QueryRow(q, args...).Scan(&n)
	return n
}

func logEvent(db execer, pid string, workID int64, actor, kind, from, to string, payload map[string]any) error {
	if actor == "" {
		actor = "agent"
	}
	_, err := db.Exec(`INSERT INTO work_events(project_id,work_id,at,actor,kind,from_status,to_status,payload_json) VALUES(?,?,?,?,?,?,?,?)`,
		pid, workID, nowUTC(), actor, kind, from, to, jsonOr(payload, "{}"))
	return err
}

type WorkEvent struct {
	ID      int64          `json:"id"`
	At      string         `json:"at"`
	Actor   string         `json:"actor"`
	Kind    string         `json:"kind"`
	From    string         `json:"from,omitempty"`
	To      string         `json:"to,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

func listEvents(db *sql.DB, pid string, workID int64) ([]WorkEvent, error) {
	rows, err := db.Query(`SELECT id,at,actor,kind,from_status,to_status,payload_json FROM work_events WHERE project_id=? AND work_id=? ORDER BY id`, pid, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkEvent{}
	for rows.Next() {
		var e WorkEvent
		var p string
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Kind, &e.From, &e.To, &p); err != nil {
			return nil, err
		}
		decodeInto(p, &e.Payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

var errNotBillable = errors.New("work is not in the bill phase")
