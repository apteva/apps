// Package episode owns the runtime side of a navigation eval —
// starting an episode, applying tool calls, deciding termination,
// rolling up metrics. Static world data lives in package world; this
// package mediates between it and SQLite.
package episode

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/apteva/apps/mcp/robot/world"
)

// ErrNoActiveEpisode is returned by tools that operate on the active
// episode (observe/move/pick/drop) when none has been started.
var ErrNoActiveEpisode = errors.New("no active episode — call start_episode first")

// ErrEpisodeFinished is returned when a tool tries to act on an
// episode the harness has already terminated.
var ErrEpisodeFinished = errors.New("episode already ended")

// Manager carries the things tool/HTTP handlers need: the open DB and
// the loaded scenario set. There is one Manager per running sidecar.
type Manager struct {
	db        *sql.DB
	scenarios map[string]*world.Scenario
}

func NewManager(db *sql.DB, scenarios map[string]*world.Scenario) *Manager {
	return &Manager{db: db, scenarios: scenarios}
}

func (m *Manager) Scenarios() []*world.Scenario {
	out := make([]*world.Scenario, 0, len(m.scenarios))
	for _, s := range m.scenarios {
		out = append(out, s)
	}
	return out
}

func (m *Manager) Scenario(id string) *world.Scenario { return m.scenarios[id] }

// StepResult is the shape every step-producing tool call returns. Each
// field is optional; tools fill what's relevant. View is folded in by
// observe/move/pick/drop so the agent always sees its new perception
// alongside the action's outcome — one fewer round-trip per step.
type StepResult struct {
	EpisodeID string     `json:"episode_id"`
	Step      int        `json:"step"`
	Position  [2]int     `json:"position"`
	View      world.View `json:"view,omitempty"`

	// Action-specific fields:
	Moved  *bool  `json:"moved,omitempty"`
	Reason string `json:"reason,omitempty"`
	Picked string `json:"picked,omitempty"` // v0.2+
	Dropped string `json:"dropped,omitempty"` // v0.2+

	// Set when this step terminated the episode.
	TerminalReason string `json:"terminal_reason,omitempty"`
}

// Start creates a new episode for the given scenario and returns the
// initial observation. There can be multiple active episodes per
// project; tools that don't carry an explicit id resolve to the most
// recent.
func (m *Manager) Start(scenarioID, model string) (*StepResult, error) {
	scen := m.scenarios[scenarioID]
	if scen == nil {
		return nil, fmt.Errorf("unknown scenario %q", scenarioID)
	}
	id := newEpisodeID()
	now := time.Now().UTC()
	if err := insertEpisode(m.db, &episodeRow{
		ID:           id,
		ScenarioID:   scenarioID,
		Model:        model,
		StartedAt:    now,
		MaxSteps:     scen.MaxSteps,
		OptimalSteps: scen.OptimalSteps,
		PosX:         scen.AgentStart.X,
		PosY:         scen.AgentStart.Y,
		Heading:      string(scen.AgentStart.Heading),
	}); err != nil {
		return nil, err
	}

	view := buildSceneView(scen, world.Pos{X: scen.AgentStart.X, Y: scen.AgentStart.Y})
	return &StepResult{
		EpisodeID: id,
		Step:      0,
		Position:  [2]int{scen.AgentStart.X, scen.AgentStart.Y},
		View:      view,
	}, nil
}

// Observe — non-step tool. Returns the agent's current view without
// advancing the step counter or writing a steps row. Cheap to call.
func (m *Manager) Observe(episodeID string) (*StepResult, error) {
	ep, scen, err := m.resolve(episodeID)
	if err != nil {
		return nil, err
	}
	view := buildSceneView(scen, world.Pos{X: ep.PosX, Y: ep.PosY})
	if ep.EndedAt != nil && ep.Success {
		view.Status = "done"
	}
	return &StepResult{
		EpisodeID: ep.ID,
		Step:      ep.Steps,
		Position:  [2]int{ep.PosX, ep.PosY},
		View:      view,
	}, nil
}

// Move — step tool. Increments the step counter, attempts the
// requested movement, writes a steps row, may terminate the episode.
func (m *Manager) Move(episodeID string, dir world.Direction) (*StepResult, error) {
	start := time.Now()
	ep, scen, err := m.resolveActive(episodeID)
	if err != nil {
		return nil, err
	}
	dx, dy := dir.Step()
	if dx == 0 && dy == 0 {
		return nil, fmt.Errorf("invalid direction %q", dir)
	}
	nx, ny := ep.PosX+dx, ep.PosY+dy
	moved := false
	reason := ""
	switch {
	case !scen.Grid.InBounds(nx, ny):
		reason = "oob"
	case scen.Grid.IsWall(nx, ny):
		reason = "wall"
	default:
		ep.PosX, ep.PosY = nx, ny
		ep.Heading = string(dir)
		moved = true
	}
	ep.Steps++

	view := buildSceneView(scen, world.Pos{X: ep.PosX, Y: ep.PosY})
	res := &StepResult{
		EpisodeID: ep.ID,
		Step:      ep.Steps,
		Position:  [2]int{ep.PosX, ep.PosY},
		View:      view,
		Moved:     boolPtr(moved),
		Reason:    reason,
	}

	terminalReason := ""
	if ep.PosX == scen.Goal[0] && ep.PosY == scen.Goal[1] {
		terminalReason = "success"
		ep.Success = true
		view.Status = "done"
		res.View = view
	} else if ep.Steps >= scen.MaxSteps {
		terminalReason = "timeout"
	}
	res.TerminalReason = terminalReason

	walltime := time.Since(start).Milliseconds()
	if err := writeStep(m.db, ep.ID, ep.Steps, "move",
		map[string]any{"direction": string(dir)},
		map[string]any{"moved": moved, "reason": reason, "position": [2]int{ep.PosX, ep.PosY}},
		ep.PosX, ep.PosY, walltime,
	); err != nil {
		return nil, err
	}
	if err := updateEpisodeAfterStep(m.db, ep, terminalReason, walltime); err != nil {
		return nil, err
	}
	return res, nil
}

// Pick / Drop — inert in v0.1. Still increments the step counter and
// writes a step row so the contract is honest about cost. Becomes
// useful when v0.2 ships scenarios with items.
func (m *Manager) Pick(episodeID string) (*StepResult, error) {
	return m.inertItemStep(episodeID, "pick")
}

func (m *Manager) Drop(episodeID string) (*StepResult, error) {
	return m.inertItemStep(episodeID, "drop")
}

func (m *Manager) inertItemStep(episodeID, tool string) (*StepResult, error) {
	start := time.Now()
	ep, scen, err := m.resolveActive(episodeID)
	if err != nil {
		return nil, err
	}
	ep.Steps++
	reason := "no_items_in_scenario" // v0.1 ships no items
	view := buildSceneView(scen, world.Pos{X: ep.PosX, Y: ep.PosY})
	res := &StepResult{
		EpisodeID: ep.ID,
		Step:      ep.Steps,
		Position:  [2]int{ep.PosX, ep.PosY},
		View:      view,
		Reason:    reason,
	}

	terminalReason := ""
	if ep.Steps >= scen.MaxSteps {
		terminalReason = "timeout"
	}
	res.TerminalReason = terminalReason

	walltime := time.Since(start).Milliseconds()
	if err := writeStep(m.db, ep.ID, ep.Steps, tool,
		map[string]any{},
		map[string]any{"reason": reason},
		ep.PosX, ep.PosY, walltime,
	); err != nil {
		return nil, err
	}
	if err := updateEpisodeAfterStep(m.db, ep, terminalReason, walltime); err != nil {
		return nil, err
	}
	return res, nil
}

// EpisodeSummary is the read-only metric snapshot returned by
// episode_status and the panel.
type EpisodeSummary struct {
	ID              string  `json:"episode_id"`
	ScenarioID      string  `json:"scenario_id"`
	Model           string  `json:"model,omitempty"`
	StartedAt       string  `json:"started_at"`
	EndedAt         string  `json:"ended_at,omitempty"`
	Success         bool    `json:"success"`
	Steps           int     `json:"steps"`
	OptimalSteps    int     `json:"optimal_steps"`
	OptimalityRatio float64 `json:"optimality_ratio"`
	MaxSteps        int     `json:"max_steps"`
	TerminalReason  string  `json:"terminal_reason,omitempty"`
	WalltimeMs      int64   `json:"walltime_ms"`
	Position        [2]int  `json:"position"`
}

func (m *Manager) Status(episodeID string) (*EpisodeSummary, error) {
	ep, _, err := m.resolve(episodeID)
	if err != nil {
		return nil, err
	}
	return summarize(ep), nil
}

// RecentEpisodes returns the N most recently started episodes — used
// by the panel's runs list.
func (m *Manager) RecentEpisodes(limit int) ([]EpisodeSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := listEpisodes(m.db, limit)
	if err != nil {
		return nil, err
	}
	out := make([]EpisodeSummary, 0, len(rows))
	for i := range rows {
		out = append(out, *summarize(&rows[i]))
	}
	return out, nil
}

// Steps returns up to N most-recent step rows for an episode, oldest
// first — drives the activity feed in the panel.
func (m *Manager) Steps(episodeID string, limit int) ([]StepRow, error) {
	if episodeID == "" {
		return nil, ErrNoActiveEpisode
	}
	if limit <= 0 {
		limit = 200
	}
	return listSteps(m.db, episodeID, limit)
}

// resolve looks up an episode by id, or the most recent any-status
// episode when id is empty. Used by read-only paths (observe, status).
func (m *Manager) resolve(episodeID string) (*episodeRow, *world.Scenario, error) {
	ep, err := readEpisode(m.db, episodeID)
	if err != nil {
		return nil, nil, err
	}
	if ep == nil {
		return nil, nil, ErrNoActiveEpisode
	}
	scen := m.scenarios[ep.ScenarioID]
	if scen == nil {
		return nil, nil, fmt.Errorf("scenario %q for episode %s no longer installed", ep.ScenarioID, ep.ID)
	}
	return ep, scen, nil
}

// resolveActive is resolve + a check that the episode hasn't ended.
// Used by mutating tools (move, pick, drop).
func (m *Manager) resolveActive(episodeID string) (*episodeRow, *world.Scenario, error) {
	ep, scen, err := m.resolve(episodeID)
	if err != nil {
		return nil, nil, err
	}
	if ep.EndedAt != nil {
		return nil, nil, ErrEpisodeFinished
	}
	return ep, scen, nil
}

// buildSceneView is the small adapter that picks goal vs no-goal and
// hands off to world.BuildView.
func buildSceneView(scen *world.Scenario, at world.Pos) world.View {
	goal := world.Pos{X: scen.Goal[0], Y: scen.Goal[1]}
	v := world.BuildView(&scen.Grid, at, &goal, scen.Observability)
	v.Step = 0
	v.Status = "idle"
	return v
}

func summarize(ep *episodeRow) *EpisodeSummary {
	s := &EpisodeSummary{
		ID:             ep.ID,
		ScenarioID:     ep.ScenarioID,
		Model:          ep.Model,
		StartedAt:      ep.StartedAt.UTC().Format(time.RFC3339),
		Success:        ep.Success,
		Steps:          ep.Steps,
		OptimalSteps:   ep.OptimalSteps,
		MaxSteps:       ep.MaxSteps,
		TerminalReason: ep.TerminalReason,
		WalltimeMs:     ep.WalltimeMs,
		Position:       [2]int{ep.PosX, ep.PosY},
	}
	if ep.EndedAt != nil {
		s.EndedAt = ep.EndedAt.UTC().Format(time.RFC3339)
	}
	if ep.Steps > 0 && ep.OptimalSteps > 0 && ep.Success {
		s.OptimalityRatio = float64(ep.OptimalSteps) / float64(ep.Steps)
	}
	return s
}

func newEpisodeID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("ep_%d_%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}

func boolPtr(v bool) *bool { return &v }
