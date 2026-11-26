package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/apteva/apps/mcp/arena/engine"
)

// Seat kinds.
const (
	SeatOpen  = "open"
	SeatAgent = "agent"
	SeatBot   = "bot"
	SeatHuman = "human"
)

type Seat struct {
	Seat      int    `json:"seat"`
	Kind      string `json:"kind"`
	AgentID   int64  `json:"agent_id,omitempty"`
	Name      string `json:"name"`
	BotLevel  string `json:"bot_level,omitempty"`
	TokenHash string `json:"-"`
	JoinedAt  string `json:"joined_at,omitempty"`
}

type Match struct {
	ID           int64           `json:"id"`
	ProjectID    string          `json:"project_id"`
	Game         string          `json:"game"`
	Title        string          `json:"title"`
	Status       string          `json:"status"`
	Options      map[string]any  `json:"options"`
	SeatCount    int             `json:"seat_count"`
	TurnSeconds  int             `json:"turn_seconds"`
	SeedHash     string          `json:"seed_hash"`
	Seed         string          `json:"seed,omitempty"` // revealed when finished
	Version      int64           `json:"version"`
	Result       *engine.Outcome `json:"result,omitempty"`
	TurnDeadline string          `json:"turn_deadline,omitempty"`
	CreatedBy    string          `json:"created_by,omitempty"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	StartedAt    string          `json:"started_at,omitempty"`
	FinishedAt   string          `json:"finished_at,omitempty"`
	Seats        []Seat          `json:"seats"`
	ToAct        []int           `json:"to_act,omitempty"`

	seedHex string
}

// StoredEvent is one row of the match log.
type StoredEvent struct {
	ID        int64          `json:"id"`
	Version   int64          `json:"version"`
	Seat      *int           `json:"seat,omitempty"`
	Type      string         `json:"type"`
	Data      map[string]any `json:"data,omitempty"`
	PrivateTo []int          `json:"-"`
	CreatedAt string         `json:"created_at"`
}

func (e StoredEvent) visibleTo(seat int) bool {
	return engine.Event{PrivateTo: e.PrivateTo}.VisibleTo(seat)
}

// Notifier receives lifecycle signals so main.go can emit app events and
// wake agents. Calls happen outside match locks.
type Notifier interface {
	MatchChanged(m *Match, kind string)
	AgentTurn(m *Match, seat Seat)
}

type nopNotifier struct{}

func (nopNotifier) MatchChanged(*Match, string) {}
func (nopNotifier) AgentTurn(*Match, Seat)      {}

type liveMatch struct {
	mu      sync.Mutex
	m       *Match
	state   engine.State
	changed chan struct{}
	timer   *time.Timer // bot move or turn clock
}

func (lm *liveMatch) broadcast() {
	close(lm.changed)
	lm.changed = make(chan struct{})
}

// Runtime owns live matches. All mutation of a match happens under its lock.
type Runtime struct {
	db       *sql.DB
	notify   Notifier
	BotDelay time.Duration
	now      func() time.Time

	mu   sync.Mutex
	live map[int64]*liveMatch
}

func NewRuntime(db *sql.DB, n Notifier) *Runtime {
	if n == nil {
		n = nopNotifier{}
	}
	return &Runtime{db: db, notify: n, BotDelay: 700 * time.Millisecond, now: time.Now, live: map[int64]*liveMatch{}}
}

// --- errors ---------------------------------------------------------------

var (
	ErrNotFound   = errors.New("match not found")
	ErrNotSeated  = errors.New("you are not seated in this match (join first, or pass your seat_token)")
	ErrNotStarted = errors.New("match has not started — waiting for open seats to fill")
)

// --- loading --------------------------------------------------------------

func (rt *Runtime) get(projectID string, id int64) (*liveMatch, error) {
	rt.mu.Lock()
	lm := rt.live[id]
	rt.mu.Unlock()
	if lm != nil {
		if lm.m.ProjectID != projectID {
			return nil, ErrNotFound
		}
		return lm, nil
	}
	m, stateRaw, err := rt.loadMatch(id)
	if err != nil {
		return nil, err
	}
	if m.ProjectID != projectID {
		return nil, ErrNotFound
	}
	lm = &liveMatch{m: m, changed: make(chan struct{})}
	if stateRaw != "" {
		g, ok := engine.Get(m.Game)
		if !ok {
			return nil, fmt.Errorf("unknown game %q", m.Game)
		}
		if lm.state, err = g.Restore(json.RawMessage(stateRaw)); err != nil {
			return nil, fmt.Errorf("restore state: %w", err)
		}
	}
	rt.mu.Lock()
	if existing := rt.live[id]; existing != nil {
		lm = existing
	} else {
		rt.live[id] = lm
	}
	rt.mu.Unlock()
	return lm, nil
}

// Resume loads every live match so turn clocks and bots keep running after
// a restart.
func (rt *Runtime) Resume() error {
	rows, err := rt.db.Query(`SELECT id, project_id FROM matches WHERE status = 'live'`)
	if err != nil {
		return err
	}
	type ref struct {
		id  int64
		pid string
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.pid); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	rows.Close()
	for _, r := range refs {
		lm, err := rt.get(r.pid, r.id)
		if err != nil {
			continue
		}
		lm.mu.Lock()
		rt.scheduleLocked(lm, false)
		lm.mu.Unlock()
	}
	return nil
}

const matchCols = `id, project_id, game, title, status, options_json, seat_count, turn_seconds,
	seed_hex, seed_hash, state_json, version, result_json, turn_deadline, created_by,
	created_at, updated_at, started_at, finished_at`

func (rt *Runtime) loadMatch(id int64) (*Match, string, error) {
	row := rt.db.QueryRow(`SELECT `+matchCols+` FROM matches WHERE id = ?`, id)
	m, state, err := scanMatch(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if m.Seats, err = rt.loadSeats(id); err != nil {
		return nil, "", err
	}
	return m, state, nil
}

type scanner interface{ Scan(...any) error }

func scanMatch(s scanner) (*Match, string, error) {
	var m Match
	var opts, state, result string
	if err := s.Scan(&m.ID, &m.ProjectID, &m.Game, &m.Title, &m.Status, &opts, &m.SeatCount, &m.TurnSeconds,
		&m.seedHex, &m.SeedHash, &state, &m.Version, &result, &m.TurnDeadline, &m.CreatedBy,
		&m.CreatedAt, &m.UpdatedAt, &m.StartedAt, &m.FinishedAt); err != nil {
		return nil, "", err
	}
	_ = json.Unmarshal([]byte(opts), &m.Options)
	if result != "" {
		m.Result = &engine.Outcome{}
		_ = json.Unmarshal([]byte(result), m.Result)
	}
	if m.Status == "finished" || m.Status == "cancelled" {
		m.Seed = m.seedHex
	}
	return &m, state, nil
}

func (rt *Runtime) loadSeats(id int64) ([]Seat, error) {
	rows, err := rt.db.Query(`SELECT seat, kind, agent_id, name, bot_level, token_hash, joined_at FROM seats WHERE match_id = ? ORDER BY seat`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Seat{}
	for rows.Next() {
		var s Seat
		if err := rows.Scan(&s.Seat, &s.Kind, &s.AgentID, &s.Name, &s.BotLevel, &s.TokenHash, &s.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// --- create / join --------------------------------------------------------

// SeatSpec is a requested occupant when creating a match.
//
//	"me"            the calling agent
//	"open"          anyone may join later
//	"bot[:level]"   built-in bot (random | basic | perfect)
//	"agent:<id>"    reserve the seat for another agent
//	"human[:name]"  a human seat; a seat token is returned
type SeatSpec string

type CreateRequest struct {
	ProjectID   string
	Game        string
	Title       string
	Seats       []string
	Options     map[string]any
	TurnSeconds *int
	CallerAgent int64  // 0 when not called by an agent
	CreatedBy   string // display string
	AgentName   func(id int64) string
}

type CreateResult struct {
	Match  *Match         `json:"match"`
	Tokens map[int]string `json:"seat_tokens,omitempty"`
	You    *int           `json:"your_seat,omitempty"`
}

func newToken() (string, string) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	tok := "seat_" + hex.EncodeToString(b)
	return tok, hashToken(tok)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (rt *Runtime) Create(req CreateRequest) (*CreateResult, error) {
	g, ok := engine.Get(req.Game)
	if !ok {
		return nil, fmt.Errorf("unknown game %q — call games_list", req.Game)
	}
	info := g.Info()
	if len(req.Seats) == 0 {
		first := SeatOpen
		if req.CallerAgent > 0 {
			first = "me"
		}
		req.Seats = []string{first}
		for len(req.Seats) < info.MinSeats {
			req.Seats = append(req.Seats, SeatOpen)
		}
	}
	if len(req.Seats) < info.MinSeats || len(req.Seats) > info.MaxSeats {
		return nil, fmt.Errorf("%s needs %d-%d seats, got %d", info.Name, info.MinSeats, info.MaxSeats, len(req.Seats))
	}
	turn := info.DefaultTurn
	if req.TurnSeconds != nil {
		turn = max(*req.TurnSeconds, 0)
	}
	if req.Options == nil {
		req.Options = map[string]any{}
	}
	if req.AgentName == nil {
		req.AgentName = func(id int64) string { return fmt.Sprintf("Agent #%d", id) }
	}
	now := rt.now().UTC().Format(time.RFC3339)
	res := &CreateResult{Tokens: map[int]string{}}
	seats := make([]Seat, len(req.Seats))
	seenAgent := map[int64]bool{}
	for i, raw := range req.Seats {
		kind, arg, _ := strings.Cut(strings.TrimSpace(strings.ToLower(raw)), ":")
		s := Seat{Seat: i, Kind: kind}
		switch kind {
		case "me":
			if req.CallerAgent == 0 {
				return nil, errors.New(`seat "me" needs an agent caller`)
			}
			s.Kind, s.AgentID = SeatAgent, req.CallerAgent
			you := i
			res.You = &you
		case SeatAgent:
			id, err := strconv.ParseInt(arg, 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("seat %d: use agent:<id>", i)
			}
			s.AgentID = id
		case SeatBot:
			if arg == "" {
				arg = "basic"
			}
			s.BotLevel = arg
			s.Name = fmt.Sprintf("Bot %d (%s)", i+1, arg)
			s.JoinedAt = now
		case SeatHuman:
			s.Name = strings.TrimSpace(raw[len("human"):])
			s.Name = strings.TrimPrefix(s.Name, ":")
			if s.Name == "" {
				s.Name = fmt.Sprintf("Player %d", i+1)
			}
			tok, h := newToken()
			s.TokenHash, s.JoinedAt = h, now
			res.Tokens[i] = tok
		case SeatOpen, "":
			s.Kind = SeatOpen
		default:
			return nil, fmt.Errorf("seat %d: unknown seat %q (me | open | bot[:level] | agent:<id> | human[:name])", i, raw)
		}
		if s.Kind == SeatAgent {
			if seenAgent[s.AgentID] {
				return nil, fmt.Errorf("agent %d listed twice", s.AgentID)
			}
			seenAgent[s.AgentID] = true
			s.Name = req.AgentName(s.AgentID)
			s.JoinedAt = now
		}
		seats[i] = s
	}
	seed := make([]byte, 32)
	_, _ = rand.Read(seed)
	seedHash := sha256.Sum256(seed)
	opts, _ := json.Marshal(req.Options)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = info.Name
	}

	tx, err := rt.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := tx.Exec(`INSERT INTO matches (project_id, game, title, status, options_json, seat_count, turn_seconds, seed_hex, seed_hash, created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'waiting', ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ProjectID, req.Game, title, string(opts), len(seats), turn, hex.EncodeToString(seed), hex.EncodeToString(seedHash[:]), req.CreatedBy, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := r.LastInsertId()
	for _, s := range seats {
		if _, err := tx.Exec(`INSERT INTO seats (match_id, seat, kind, agent_id, name, bot_level, token_hash, joined_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, s.Seat, s.Kind, s.AgentID, s.Name, s.BotLevel, s.TokenHash, s.JoinedAt); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	lm, err := rt.get(req.ProjectID, id)
	if err != nil {
		return nil, err
	}
	lm.mu.Lock()
	var notes []func()
	notes = append(notes, rt.changedNote(lm, "created"))
	if err := rt.maybeStartLocked(lm, &notes); err != nil {
		lm.mu.Unlock()
		return nil, err
	}
	res.Match = rt.snapshotLocked(lm)
	lm.mu.Unlock()
	runAll(notes)
	return res, nil
}

type JoinRequest struct {
	ProjectID   string
	MatchID     int64
	Seat        *int
	CallerAgent int64
	Name        string // humans
	AgentName   func(id int64) string
}

type JoinResult struct {
	Match *Match `json:"match"`
	Seat  int    `json:"your_seat"`
	Token string `json:"seat_token,omitempty"`
}

func (rt *Runtime) Join(req JoinRequest) (*JoinResult, error) {
	lm, err := rt.get(req.ProjectID, req.MatchID)
	if err != nil {
		return nil, err
	}
	lm.mu.Lock()
	var notes []func()
	defer func() { runAll(notes) }()
	defer lm.mu.Unlock()
	m := lm.m
	if req.CallerAgent > 0 {
		for _, s := range m.Seats {
			if s.Kind == SeatAgent && s.AgentID == req.CallerAgent {
				return &JoinResult{Match: rt.snapshotLocked(lm), Seat: s.Seat}, nil // idempotent / reserved seat
			}
		}
	}
	if m.Status != "waiting" {
		return nil, fmt.Errorf("match is %s; no seats to join", m.Status)
	}
	target := -1
	for _, s := range m.Seats {
		if s.Kind != SeatOpen {
			continue
		}
		if req.Seat == nil || *req.Seat == s.Seat {
			target = s.Seat
			break
		}
	}
	if target < 0 {
		return nil, errors.New("no open seat available (or the requested seat is taken)")
	}
	s := &m.Seats[target]
	now := rt.now().UTC().Format(time.RFC3339)
	res := &JoinResult{Seat: target}
	if req.CallerAgent > 0 {
		s.Kind, s.AgentID = SeatAgent, req.CallerAgent
		if req.AgentName != nil {
			s.Name = req.AgentName(req.CallerAgent)
		} else {
			s.Name = fmt.Sprintf("Agent #%d", req.CallerAgent)
		}
	} else {
		tok, h := newToken()
		s.Kind, s.TokenHash = SeatHuman, h
		s.Name = strings.TrimSpace(req.Name)
		if s.Name == "" {
			s.Name = fmt.Sprintf("Player %d", target+1)
		}
		res.Token = tok
	}
	s.JoinedAt = now
	if _, err := rt.db.Exec(`UPDATE seats SET kind = ?, agent_id = ?, name = ?, token_hash = ?, joined_at = ? WHERE match_id = ? AND seat = ?`,
		s.Kind, s.AgentID, s.Name, s.TokenHash, s.JoinedAt, m.ID, target); err != nil {
		return nil, err
	}
	notes = append(notes, rt.changedNote(lm, "joined"))
	if err := rt.maybeStartLocked(lm, &notes); err != nil {
		return nil, err
	}
	res.Match = rt.snapshotLocked(lm)
	return res, nil
}

func (rt *Runtime) maybeStartLocked(lm *liveMatch, notes *[]func()) error {
	m := lm.m
	if m.Status != "waiting" {
		return nil
	}
	for _, s := range m.Seats {
		if s.Kind == SeatOpen {
			return nil
		}
	}
	g, _ := engine.Get(m.Game)
	seed, _ := hex.DecodeString(m.seedHex)
	st, evs, err := g.New(seed, m.SeatCount, m.Options)
	if err != nil {
		return err
	}
	lm.state = st
	m.Status = "live"
	m.StartedAt = rt.now().UTC().Format(time.RFC3339)
	if err := rt.commitLocked(lm, evs, notes); err != nil {
		return err
	}
	*notes = append(*notes, rt.changedNote(lm, "started"))
	return nil
}

// --- acting ---------------------------------------------------------------

// ResolveSeat finds the seat for an agent caller or a seat token.
func (rt *Runtime) ResolveSeat(projectID string, matchID int64, callerAgent int64, token string) (int, error) {
	lm, err := rt.get(projectID, matchID)
	if err != nil {
		return 0, err
	}
	lm.mu.Lock()
	defer lm.mu.Unlock()
	if token != "" {
		h := hashToken(token)
		for _, s := range lm.m.Seats {
			if s.TokenHash != "" && s.TokenHash == h {
				return s.Seat, nil
			}
		}
		return 0, errors.New("invalid seat token")
	}
	if callerAgent > 0 {
		for _, s := range lm.m.Seats {
			if s.Kind == SeatAgent && s.AgentID == callerAgent {
				return s.Seat, nil
			}
		}
	}
	return 0, ErrNotSeated
}

func (rt *Runtime) Act(projectID string, matchID int64, seat int, a engine.Action) (*Match, error) {
	lm, err := rt.get(projectID, matchID)
	if err != nil {
		return nil, err
	}
	var notes []func()
	lm.mu.Lock()
	err = rt.applyLocked(lm, seat, a, &notes)
	snap := rt.snapshotLocked(lm)
	lm.mu.Unlock()
	runAll(notes)
	if err != nil {
		return nil, err
	}
	return snap, nil
}

func (rt *Runtime) applyLocked(lm *liveMatch, seat int, a engine.Action, notes *[]func()) error {
	if lm.m.Status == "waiting" {
		return ErrNotStarted
	}
	if lm.m.Status != "live" || lm.state == nil {
		return engine.ErrGameOver
	}
	evs, err := lm.state.Apply(seat, a)
	if err != nil {
		return err
	}
	return rt.commitLocked(lm, evs, notes)
}

// commitLocked persists state + events, bumps the version, wakes waiters and
// schedules the next bot move / clock.
func (rt *Runtime) commitLocked(lm *liveMatch, evs []engine.Event, notes *[]func()) error {
	m := lm.m
	now := rt.now().UTC()
	stamp := now.Format(time.RFC3339)
	m.Version++
	m.UpdatedAt = stamp
	finished := false
	if r := lm.state.Result(); r != nil && m.Status == "live" {
		m.Status, m.Result, m.FinishedAt, m.Seed = "finished", r, stamp, m.seedHex
		finished = true
	}
	m.ToAct = lm.state.ToAct()
	m.TurnDeadline = ""
	if m.Status == "live" && m.TurnSeconds > 0 {
		m.TurnDeadline = now.Add(time.Duration(m.TurnSeconds) * time.Second).Format(time.RFC3339)
	}
	stateJSON, err := json.Marshal(lm.state)
	if err != nil {
		return err
	}
	resultJSON := ""
	if m.Result != nil {
		b, _ := json.Marshal(m.Result)
		resultJSON = string(b)
	}
	tx, err := rt.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE matches SET status = ?, state_json = ?, version = ?, result_json = ?, turn_deadline = ?, updated_at = ?, started_at = ?, finished_at = ? WHERE id = ?`,
		m.Status, string(stateJSON), m.Version, resultJSON, m.TurnDeadline, stamp, m.StartedAt, m.FinishedAt, m.ID); err != nil {
		return err
	}
	for _, e := range evs {
		data, _ := json.Marshal(e.Data)
		priv := ""
		if len(e.PrivateTo) > 0 {
			b, _ := json.Marshal(e.PrivateTo)
			priv = string(b)
		}
		var seat any
		if e.Seat != nil {
			seat = *e.Seat
		}
		if _, err := tx.Exec(`INSERT INTO events (match_id, version, seat, type, data_json, private_to_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.Version, seat, e.Type, string(data), priv, stamp); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	lm.broadcast()
	if finished {
		*notes = append(*notes, rt.changedNote(lm, "finished"))
	}
	rt.scheduleLocked(lm, true)
	if m.Status == "live" {
		for _, s := range m.ToAct {
			if seat := m.Seats[s]; seat.Kind == SeatAgent {
				snap := rt.snapshotLocked(lm)
				*notes = append(*notes, func() { rt.notify.AgentTurn(snap, seat) })
			}
		}
	}
	return nil
}

// scheduleLocked arms one timer: a bot move if a bot is to act, otherwise the
// turn clock.
func (rt *Runtime) scheduleLocked(lm *liveMatch, _ bool) {
	if lm.timer != nil {
		lm.timer.Stop()
		lm.timer = nil
	}
	m := lm.m
	if m.Status != "live" || lm.state == nil {
		return
	}
	toAct := lm.state.ToAct()
	if len(toAct) == 0 {
		return
	}
	seat := toAct[0]
	version := m.Version
	if m.Seats[seat].Kind == SeatBot {
		lm.timer = time.AfterFunc(rt.BotDelay, func() { rt.fire(lm, version, seat, true) })
		return
	}
	if m.TurnDeadline == "" {
		return
	}
	dl, err := time.Parse(time.RFC3339, m.TurnDeadline)
	if err != nil {
		return
	}
	lm.timer = time.AfterFunc(max(dl.Sub(rt.now()), 0), func() { rt.fire(lm, version, seat, false) })
}

// fire plays a bot move or a timeout move if the match hasn't moved on.
func (rt *Runtime) fire(lm *liveMatch, version int64, seat int, bot bool) {
	var notes []func()
	lm.mu.Lock()
	if lm.m.Status != "live" || lm.m.Version != version || lm.state == nil {
		lm.mu.Unlock()
		return
	}
	var a engine.Action
	if bot {
		rng := mrand.New(mrand.NewPCG(uint64(time.Now().UnixNano()), uint64(seat)))
		a = lm.state.BotAction(seat, lm.m.Seats[seat].BotLevel, rng)
	} else {
		a = lm.state.TimeoutAction(seat)
		_ = rt.recordLocked(lm, engine.Event{Type: "timeout", Seat: engine.SeatPtr(seat), Data: map[string]any{"auto_action": a.Type}})
	}
	if err := rt.applyLocked(lm, seat, a, &notes); err != nil {
		// Should not happen: fall back to the timeout action once.
		if bot {
			_ = rt.applyLocked(lm, seat, lm.state.TimeoutAction(seat), &notes)
		}
	}
	lm.mu.Unlock()
	runAll(notes)
}

// recordLocked appends an event at the current version without changing
// game state (chat, timeouts).
func (rt *Runtime) recordLocked(lm *liveMatch, e engine.Event) error {
	data, _ := json.Marshal(e.Data)
	var seat any
	if e.Seat != nil {
		seat = *e.Seat
	}
	_, err := rt.db.Exec(`INSERT INTO events (match_id, version, seat, type, data_json, private_to_json, created_at) VALUES (?, ?, ?, ?, ?, '', ?)`,
		lm.m.ID, lm.m.Version, seat, e.Type, string(data), rt.now().UTC().Format(time.RFC3339))
	return err
}

func (rt *Runtime) Chat(projectID string, matchID int64, seat int, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("text required")
	}
	if len(text) > 500 {
		text = text[:500]
	}
	lm, err := rt.get(projectID, matchID)
	if err != nil {
		return err
	}
	lm.mu.Lock()
	defer lm.mu.Unlock()
	if err := rt.recordLocked(lm, engine.Event{Type: "chat", Seat: engine.SeatPtr(seat), Data: map[string]any{"text": text, "name": lm.m.Seats[seat].Name}}); err != nil {
		return err
	}
	lm.broadcast()
	return nil
}

func (rt *Runtime) Cancel(projectID string, matchID int64) (*Match, error) {
	lm, err := rt.get(projectID, matchID)
	if err != nil {
		return nil, err
	}
	var notes []func()
	lm.mu.Lock()
	m := lm.m
	if m.Status == "finished" || m.Status == "cancelled" {
		lm.mu.Unlock()
		return nil, fmt.Errorf("match already %s", m.Status)
	}
	now := rt.now().UTC().Format(time.RFC3339)
	m.Status, m.FinishedAt, m.UpdatedAt, m.Seed, m.TurnDeadline = "cancelled", now, now, m.seedHex, ""
	m.Version++
	if _, err := rt.db.Exec(`UPDATE matches SET status = 'cancelled', finished_at = ?, updated_at = ?, version = ?, turn_deadline = '' WHERE id = ?`, now, now, m.Version, m.ID); err != nil {
		lm.mu.Unlock()
		return nil, err
	}
	_ = rt.recordLocked(lm, engine.Event{Type: "cancelled"})
	rt.scheduleLocked(lm, false)
	lm.broadcast()
	notes = append(notes, rt.changedNote(lm, "cancelled"))
	snap := rt.snapshotLocked(lm)
	lm.mu.Unlock()
	runAll(notes)
	return snap, nil
}

// --- reading --------------------------------------------------------------

// SeatState is everything a seat (or spectator, Seat = -1) may know.
type SeatState struct {
	Match    *Match              `json:"match"`
	Seat     int                 `json:"your_seat"`
	YourTurn bool                `json:"your_turn"`
	View     any                 `json:"view,omitempty"`
	Legal    []engine.ActionSpec `json:"legal_actions,omitempty"`
	Recent   []StoredEvent       `json:"recent_events,omitempty"`
	Chat     []StoredEvent       `json:"chat,omitempty"`
	Help     string              `json:"action_help,omitempty"`
}

func (rt *Runtime) State(projectID string, matchID int64, seat int, recent int) (*SeatState, error) {
	lm, err := rt.get(projectID, matchID)
	if err != nil {
		return nil, err
	}
	lm.mu.Lock()
	out := rt.seatStateLocked(lm, seat)
	lm.mu.Unlock()
	if recent > 0 {
		evs, err := rt.Events(projectID, matchID, seat, 0, 200)
		if err == nil {
			for _, e := range evs {
				if e.Type == "chat" {
					out.Chat = append(out.Chat, e)
				} else {
					out.Recent = append(out.Recent, e)
				}
			}
			out.Recent = tail(out.Recent, recent)
			out.Chat = tail(out.Chat, 10)
		}
	}
	return out, nil
}

func tail[T any](xs []T, n int) []T {
	if len(xs) > n {
		return xs[len(xs)-n:]
	}
	return xs
}

func (rt *Runtime) seatStateLocked(lm *liveMatch, seat int) *SeatState {
	out := &SeatState{Match: rt.snapshotLocked(lm), Seat: seat}
	if g, ok := engine.Get(lm.m.Game); ok {
		out.Help = g.Info().ActionHelp
	}
	if lm.state != nil {
		out.View = lm.state.View(seat)
		if seat >= 0 && lm.m.Status == "live" {
			out.Legal = lm.state.Legal(seat)
			for _, s := range lm.state.ToAct() {
				if s == seat {
					out.YourTurn = true
				}
			}
		}
	}
	return out
}

// Wait blocks until the match version exceeds since (and, with untilTurn,
// until it's seat's turn or the match ends), or the timeout passes.
func (rt *Runtime) Wait(ctx context.Context, projectID string, matchID int64, seat int, since int64, untilTurn bool, timeout time.Duration) (*SeatState, bool, error) {
	lm, err := rt.get(projectID, matchID)
	if err != nil {
		return nil, false, err
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		lm.mu.Lock()
		st := rt.seatStateLocked(lm, seat)
		ch := lm.changed
		lm.mu.Unlock()
		done := st.Match.Status == "finished" || st.Match.Status == "cancelled"
		ready := done || (untilTurn && st.YourTurn) || (!untilTurn && st.Match.Version > since)
		if ready {
			full, err := rt.State(projectID, matchID, seat, 12)
			if err != nil {
				return st, true, nil
			}
			return full, true, nil
		}
		select {
		case <-ch:
			if !untilTurn {
				// chat wakes too; return on any change.
				full, _ := rt.State(projectID, matchID, seat, 12)
				return full, true, nil
			}
		case <-deadline.C:
			return st, false, nil
		case <-ctx.Done():
			return st, false, ctx.Err()
		}
	}
}

func (rt *Runtime) Events(projectID string, matchID int64, seat int, afterID int64, limit int) ([]StoredEvent, error) {
	lm, err := rt.get(projectID, matchID)
	if err != nil {
		return nil, err
	}
	lm.mu.Lock()
	revealAll := lm.m.Status == "finished" || lm.m.Status == "cancelled"
	lm.mu.Unlock()
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	// Newest `limit` events after afterID, returned oldest first.
	rows, err := rt.db.Query(`SELECT * FROM (SELECT id, version, seat, type, data_json, private_to_json, created_at FROM events
		WHERE match_id = ? AND id > ? ORDER BY id DESC LIMIT ?) ORDER BY id`, matchID, afterID, limit*3)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoredEvent{}
	for rows.Next() {
		var e StoredEvent
		var seatN sql.NullInt64
		var data, priv string
		if err := rows.Scan(&e.ID, &e.Version, &seatN, &e.Type, &data, &priv, &e.CreatedAt); err != nil {
			return nil, err
		}
		if seatN.Valid {
			s := int(seatN.Int64)
			e.Seat = &s
		}
		_ = json.Unmarshal([]byte(data), &e.Data)
		if priv != "" {
			_ = json.Unmarshal([]byte(priv), &e.PrivateTo)
		}
		if !revealAll && !e.visibleTo(seat) {
			continue
		}
		out = append(out, e)
	}
	return tail(out, limit), rows.Err()
}

type ListFilter struct {
	Status  string
	Game    string
	AgentID int64
	Limit   int
}

func (rt *Runtime) List(projectID string, f ListFilter) ([]*Match, error) {
	where := []string{"project_id = ?"}
	args := []any{projectID}
	if f.Status != "" && f.Status != "all" {
		if f.Status == "active" {
			where = append(where, "status IN ('waiting','live')")
		} else {
			where = append(where, "status = ?")
			args = append(args, f.Status)
		}
	}
	if f.Game != "" {
		where = append(where, "game = ?")
		args = append(args, f.Game)
	}
	if f.AgentID > 0 {
		where = append(where, "id IN (SELECT match_id FROM seats WHERE agent_id = ?)")
		args = append(args, f.AgentID)
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	args = append(args, f.Limit)
	rows, err := rt.db.Query(`SELECT `+matchCols+` FROM matches WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	var out []*Match
	for rows.Next() {
		m, _, err := scanMatch(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, m)
	}
	rows.Close()
	for _, m := range out {
		// Prefer the live in-memory copy for to_act/deadline.
		rt.mu.Lock()
		lm := rt.live[m.ID]
		rt.mu.Unlock()
		if lm != nil {
			lm.mu.Lock()
			*m = *rt.snapshotLocked(lm)
			lm.mu.Unlock()
			continue
		}
		if m.Seats, err = rt.loadSeats(m.ID); err != nil {
			return nil, err
		}
	}
	if out == nil {
		out = []*Match{}
	}
	return out, nil
}

func (rt *Runtime) snapshotLocked(lm *liveMatch) *Match {
	c := *lm.m
	c.Seats = append([]Seat(nil), lm.m.Seats...)
	if lm.state != nil && c.Status == "live" {
		c.ToAct = lm.state.ToAct()
	} else {
		c.ToAct = nil
	}
	return &c
}

func (rt *Runtime) changedNote(lm *liveMatch, kind string) func() {
	snap := rt.snapshotLocked(lm)
	return func() { rt.notify.MatchChanged(snap, kind) }
}

func runAll(fs []func()) {
	for _, f := range fs {
		f()
	}
}
