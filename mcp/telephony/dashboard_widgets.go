package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Reads behind the "live-calls" and "calls-overview" dashboard widgets. Both
// are served by /audio-health?view=… so they inherit its operator-only,
// project-scoped authorization and diagnostics workload admission.

// softphonePresence counts open /calls/events streams per application user.
// A user whose browser holds the call stream open can be offered calls now.
type softphonePresence struct {
	mu   sync.Mutex
	open map[string]map[string]int // project -> identity key -> open streams
}

func (p *softphonePresence) enter(project string, identity phoneIdentity) func() {
	if p == nil || project == "" || !identity.valid() {
		return func() {}
	}
	key := identity.key()
	p.mu.Lock()
	if p.open == nil {
		p.open = map[string]map[string]int{}
	}
	if p.open[project] == nil {
		p.open[project] = map[string]int{}
	}
	p.open[project][key]++
	p.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.open[project][key] <= 1 {
				delete(p.open[project], key)
				if len(p.open[project]) == 0 {
					delete(p.open, project)
				}
				return
			}
			p.open[project][key]--
		})
	}
}

func (p *softphonePresence) connected(project string) map[string]bool {
	out := map[string]bool{}
	if p == nil {
		return out
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for key := range p.open[project] {
		out[key] = true
	}
	return out
}

type liveWidgetCall struct {
	CallID      string `json:"call_id"`
	Direction   string `json:"direction"`
	Status      string `json:"status"`
	Phase       string `json:"phase"` // ringing | in_progress | on_hold
	Number      string `json:"number"`
	Remote      string `json:"remote"`
	AnsweredBy  string `json:"answered_by"` // person | ai | ""
	Handler     string `json:"handler"`
	StartedAt   string `json:"started_at"`
	AnsweredAt  string `json:"answered_at,omitempty"`
	destination string
	owner       string
}

type liveWidgetAnswerer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`  // browser | ai
	State  string `json:"state"` // busy | connected | offline | available
	Calls  int    `json:"calls"`
	People int    `json:"people"`
}

func liveCallPhase(status, holdState string, answered bool) string {
	if !answered && status != "in-progress" {
		return "ringing"
	}
	if holdState == "held" {
		return "on_hold"
	}
	return "in_progress"
}

func (a *App) serveLiveCallsWidget(w http.ResponseWriter, r *http.Request, project string) {
	q := r.URL.Query()
	direction, number := q.Get("direction"), q.Get("number")
	if direction != "" && direction != "inbound" && direction != "outbound" {
		http.Error(w, "unknown direction", 400)
		return
	}
	if len(number) > 64 {
		http.Error(w, "filter too long", 400)
		return
	}
	result, err := a.liveCallsWidget(r.Context(), project, direction, number)
	if err != nil {
		http.Error(w, "live calls unavailable", 503)
		return
	}
	writeJSON(w, result)
}

func (a *App) liveCallsWidget(ctx context.Context, project, direction, number string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Read the policy before the transaction: the pool may hold one connection.
	policy, err := a.phonePolicy(project)
	if err != nil {
		return nil, err
	}
	tx, err := a.db().db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// The predicate matches idx_calls_live_lifecycle exactly. Ring-group child
	// legs are internal fan-out and never shown as separate calls.
	where := `c.project_id=? AND c.status NOT IN ('completed','failed','no-answer','busy','canceled') AND c.ingress_path<>'ring_group'`
	args := []any{project}
	if direction != "" {
		where += ` AND c.direction=?`
		args = append(args, direction)
	}
	if number != "" {
		where += ` AND (CASE WHEN c.direction='inbound' THEN c.to_number ELSE c.from_number END)=?`
		args = append(args, number)
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.direction,c.status,c.from_number,c.to_number,c.placed_at,COALESCE(c.answered_at,''),c.peer_kind,c.hold_state,c.agent_id,c.routing_destination_id,COALESCE(o.principal,''),COALESCE(d.name,'')
 FROM calls c INDEXED BY idx_calls_live_lifecycle
 LEFT JOIN telephony_call_owners o ON o.call_id=c.id AND o.project_id=c.project_id
 LEFT JOIN routing_destinations d ON d.id=c.routing_destination_id AND d.project_id=c.project_id
 WHERE `+where+` ORDER BY c.placed_at,c.id LIMIT 201`, args...)
	if err != nil {
		return nil, err
	}
	calls := []liveWidgetCall{}
	for rows.Next() {
		var c liveWidgetCall
		var from, to, peer, hold, destName string
		var agent int64
		if err := rows.Scan(&c.CallID, &c.Direction, &c.Status, &from, &to, &c.StartedAt, &c.AnsweredAt, &peer, &hold, &agent, &c.destination, &c.owner, &destName); err != nil {
			rows.Close()
			return nil, err
		}
		c.Number, c.Remote = to, from
		if c.Direction != "inbound" {
			c.Number, c.Remote = from, to
		}
		answered := c.AnsweredAt != ""
		c.Phase = liveCallPhase(c.Status, hold, answered)
		switch {
		case answered && peer == "human":
			c.AnsweredBy = "person"
			c.Handler = audioDashboardAdviser(c.owner, destName)
			if c.owner == "" && destName == "" {
				c.Handler = "Person"
			}
		case answered:
			c.AnsweredBy = "ai"
			c.Handler = "AI agent"
			if agent > 0 {
				c.Handler += " #" + strconv.FormatInt(agent, 10)
			}
		case destName != "":
			c.Handler = destName
		}
		calls = append(calls, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	truncated := len(calls) > 200
	if truncated {
		calls = calls[:200]
	}
	answerers, err := a.liveAnswerers(ctx, tx, project, policy)
	if err != nil {
		return nil, err
	}
	summary := map[string]int{"ringing": 0, "in_progress": 0, "on_hold": 0}
	for _, c := range calls {
		summary[c.Phase]++
	}
	return map[string]any{"generated_at": time.Now().UTC().Format(time.RFC3339Nano), "calls": calls, "truncated": truncated, "summary": summary, "answerers": answerers}, nil
}

// liveAnswerers lists every enabled destination that can take a call, with the
// state of the people behind each browser destination: busy (on a live call),
// connected (a softphone holds the call stream open) or offline.
func (a *App) liveAnswerers(ctx context.Context, tx *sql.Tx, project string, policy phonePolicy) ([]liveWidgetAnswerer, error) {
	groups := map[string][]string{}
	for _, g := range policy.Groups {
		groups[g.ID] = g.Destinations
	}
	people := map[string]map[string]bool{} // destination -> identity keys ("*" = every destination)
	addPerson := func(dest, key string) {
		if people[dest] == nil {
			people[dest] = map[string]bool{}
		}
		people[dest][key] = true
	}
	for _, u := range policy.Users {
		if !u.Enabled || !u.Identity.valid() {
			continue
		}
		dests := append([]string{}, u.Destinations...)
		for _, g := range u.Groups {
			dests = append(dests, groups[g]...)
		}
		for _, d := range dests {
			addPerson(d, u.Identity.key())
		}
	}
	busyPeople := map[string]bool{}
	busyDest := map[string]int{}
	rows, err := tx.QueryContext(ctx, `SELECT c.routing_destination_id,COALESCE(o.principal,'') FROM calls c INDEXED BY idx_calls_live_occupancy
 LEFT JOIN telephony_call_owners o ON o.call_id=c.id AND o.project_id=c.project_id
 WHERE c.project_id=? AND c.peer_kind='human' AND c.status NOT IN ('completed','failed','busy','no-answer','canceled')`, project)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var dest, owner string
		if err := rows.Scan(&dest, &owner); err != nil {
			rows.Close()
			return nil, err
		}
		if owner != "" {
			busyPeople[owner] = true
		}
		if dest != "" {
			busyDest[dest]++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	online := a.softphoneStreams.connected(project)
	aaRows, err := tx.QueryContext(ctx, `SELECT principal FROM telephony_auto_answer_sessions WHERE project_id=? AND expires_at>?`, project, time.Now().Unix())
	if err == nil {
		for aaRows.Next() {
			var principal string
			if aaRows.Scan(&principal) == nil {
				online[principal] = true
			}
		}
		aaRows.Close()
	}
	drows, err := tx.QueryContext(ctx, `SELECT id,name,kind,config_json FROM routing_destinations WHERE project_id=? AND enabled=1 AND kind IN ('browser','ai') ORDER BY kind,name`, project)
	if err != nil {
		return nil, err
	}
	defer drows.Close()
	out := []liveWidgetAnswerer{}
	for drows.Next() {
		var item liveWidgetAnswerer
		var raw string
		if err := drows.Scan(&item.ID, &item.Name, &item.Kind, &raw); err != nil {
			return nil, err
		}
		item.Calls = busyDest[item.ID]
		if item.Kind == "ai" {
			item.State = "available"
			out = append(out, item)
			continue
		}
		members := map[string]bool{}
		for k := range people[item.ID] {
			members[k] = true
		}
		for k := range people["*"] {
			members[k] = true
		}
		if c, e := readDestinationCapacity(raw); e == nil && c.Identity.valid() {
			members[c.Identity.key()] = true
		}
		item.People = len(members)
		busy, connected := item.Calls > 0, false
		for k := range members {
			busy = busy || busyPeople[k]
			connected = connected || online[k]
		}
		switch {
		case busy:
			item.State = "busy"
		case connected:
			item.State = "connected"
		default:
			item.State = "offline"
		}
		out = append(out, item)
	}
	if err := drows.Err(); err != nil {
		return nil, err
	}
	rank := map[string]int{"connected": 0, "busy": 1, "available": 2, "offline": 3}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].State] < rank[out[j].State] })
	return out, nil
}

// ─── Calls overview ────────────────────────────────────────────────

type overviewTotals struct {
	Calls          int      `json:"calls"`
	AnsweredPerson int      `json:"answered_person"`
	AnsweredAI     int      `json:"answered_ai"`
	Missed         int      `json:"missed"`
	Abandoned      int      `json:"abandoned"`
	Failed         int      `json:"failed"`
	Active         int      `json:"active"`
	AnswerRate     *float64 `json:"answer_rate"`
	AverageWait    *float64 `json:"average_wait_seconds"`
	AverageLength  *float64 `json:"average_length_seconds"`
	waitSum        float64
	waitN          int
	lengthSum      float64
	lengthN        int
}

type overviewBucket struct {
	From           string `json:"from"`
	Until          string `json:"until"`
	AnsweredPerson int    `json:"answered_person"`
	AnsweredAI     int    `json:"answered_ai"`
	Missed         int    `json:"missed"`
	Abandoned      int    `json:"abandoned"`
	start, end     int64
}

type overviewCall struct {
	status, direction, peer, initiator string
	start, answer, end, talk           int64
}

// callOutcome classifies one call. Abandoned means the caller hung up before
// anyone answered; missed covers every other unanswered end (no answer, busy,
// rejected, nobody available) and failed calls are counted separately.
func callOutcome(c overviewCall) string {
	switch {
	case !isTerminalStatus(c.status):
		return "active"
	case c.answer > 0 && c.peer == "human":
		return "person"
	case c.answer > 0:
		return "ai"
	case c.status == "failed":
		return "failed"
	case c.direction == "inbound" && (c.status == "canceled" || c.initiator == "caller"):
		return "abandoned"
	default:
		return "missed"
	}
}

func (t *overviewTotals) add(c overviewCall, outcome string) {
	t.Calls++
	switch outcome {
	case "active":
		t.Active++
	case "person":
		t.AnsweredPerson++
	case "ai":
		t.AnsweredAI++
	case "failed":
		t.Failed++
	case "abandoned":
		t.Abandoned++
	default:
		t.Missed++
	}
	if c.answer > 0 && c.answer >= c.start {
		t.waitSum += float64(c.answer - c.start)
		t.waitN++
		length := c.talk
		if length <= 0 && c.end > c.answer {
			length = c.end - c.answer
		}
		if length > 0 && isTerminalStatus(c.status) {
			t.lengthSum += float64(length)
			t.lengthN++
		}
	}
}

func (t *overviewTotals) finish() {
	answered := t.AnsweredPerson + t.AnsweredAI
	t.AnswerRate = statisticsRatio(float64(answered), float64(answered+t.Missed+t.Abandoned))
	if t.waitN > 0 {
		v := t.waitSum / float64(t.waitN)
		t.AverageWait = &v
	}
	if t.lengthN > 0 {
		v := t.lengthSum / float64(t.lengthN)
		t.AverageLength = &v
	}
}

func (a *App) serveCallsOverviewWidget(w http.ResponseWriter, r *http.Request, project string) {
	f, err := parseCallStatisticsFilter(r, time.Now().UTC())
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	result, err := a.db().callsOverview(r.Context(), project, f)
	if err != nil {
		http.Error(w, "call overview unavailable", 503)
		return
	}
	writeJSON(w, result)
}

func (c *callsDB) callsOverview(ctx context.Context, project string, f callStatisticsFilter) (map[string]any, error) {
	if c == nil || c.db == nil {
		return nil, errors.New("database unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	where := `c.project_id=? AND unixepoch(c.placed_at)>=? AND unixepoch(c.placed_at)<? AND c.ingress_path<>'ring_group'`
	args := []any{project, f.From.Unix(), f.Until.Unix()}
	if f.Direction != "" {
		where += ` AND c.direction=?`
		args = append(args, f.Direction)
	}
	if f.Number != "" {
		where += ` AND (CASE WHEN c.direction='inbound' THEN c.to_number ELSE c.from_number END)=?`
		args = append(args, f.Number)
	}
	rows, err := c.db.QueryContext(ctx, `SELECT c.status,c.direction,c.peer_kind,c.termination_initiator,unixepoch(c.placed_at),COALESCE(unixepoch(c.answered_at),0),COALESCE(unixepoch(c.ended_at),0),c.talk_duration_seconds
 FROM calls c INDEXED BY idx_calls_statistics_start WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	source := statisticsBuckets(f)
	buckets := make([]overviewBucket, len(source))
	for i, b := range source {
		buckets[i] = overviewBucket{From: b.From, Until: b.Until, start: b.start, end: b.end}
	}
	total := overviewTotals{}
	for rows.Next() {
		var call overviewCall
		if err := rows.Scan(&call.status, &call.direction, &call.peer, &call.initiator, &call.start, &call.answer, &call.end, &call.talk); err != nil {
			return nil, err
		}
		outcome := callOutcome(call)
		total.add(call, outcome)
		i := sort.Search(len(buckets), func(i int) bool { return buckets[i].end > call.start })
		if i < len(buckets) && buckets[i].start <= call.start {
			switch outcome {
			case "person":
				buckets[i].AnsweredPerson++
			case "ai":
				buckets[i].AnsweredAI++
			case "abandoned":
				buckets[i].Abandoned++
			case "missed", "failed":
				buckets[i].Missed++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	total.finish()
	granularity := "hour"
	if f.Until.Sub(f.From) > 72*time.Hour {
		granularity = "day"
	}
	numbers, err := c.overviewNumbers(ctx, project)
	if err != nil {
		return nil, err
	}
	return map[string]any{"generated_at": time.Now().UTC().Format(time.RFC3339Nano), "from": f.From.UTC().Format(time.RFC3339), "until": f.Until.UTC().Format(time.RFC3339), "granularity": granularity, "totals": total, "buckets": buckets, "numbers": numbers}, nil
}

// overviewNumbers lists the project's routed numbers for the widget filter.
func (c *callsDB) overviewNumbers(ctx context.Context, project string) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT DISTINCT phone_number FROM inbound_routes WHERE project_id=? AND phone_number<>'' ORDER BY phone_number LIMIT 200`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
