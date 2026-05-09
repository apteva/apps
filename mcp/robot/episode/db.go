package episode

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// episodeRow mirrors the robot_episodes table 1:1 — internal to this
// package; callers see EpisodeSummary instead.
type episodeRow struct {
	ID             string
	ScenarioID     string
	Model          string
	StartedAt      time.Time
	EndedAt        *time.Time
	Success        bool
	Steps          int
	OptimalSteps   int
	MaxSteps       int
	TerminalReason string
	WalltimeMs     int64
	PosX           int
	PosY           int
	Heading        string
}

func insertEpisode(db *sql.DB, ep *episodeRow) error {
	_, err := db.Exec(`
		INSERT INTO robot_episodes
		    (id, scenario_id, model, started_at, max_steps, optimal_steps,
		     pos_x, pos_y, heading)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ep.ID, ep.ScenarioID, ep.Model, ep.StartedAt.UTC(),
		ep.MaxSteps, ep.OptimalSteps,
		ep.PosX, ep.PosY, ep.Heading)
	return err
}

// readEpisode loads either the named episode (id != "") or the most
// recent one (id == ""). Returns (nil, nil) when no episode exists.
func readEpisode(db *sql.DB, id string) (*episodeRow, error) {
	const cols = `id, scenario_id, model, started_at, ended_at,
	              success, steps, optimal_steps, max_steps,
	              terminal_reason, walltime_ms, pos_x, pos_y, heading`
	var (
		row *sql.Row
	)
	if id != "" {
		row = db.QueryRow(`SELECT `+cols+` FROM robot_episodes WHERE id = ?`, id)
	} else {
		row = db.QueryRow(`SELECT ` + cols + ` FROM robot_episodes
		                   ORDER BY started_at DESC LIMIT 1`)
	}
	ep, err := scanEpisode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ep, err
}

func listEpisodes(db *sql.DB, limit int) ([]episodeRow, error) {
	rows, err := db.Query(`
		SELECT id, scenario_id, model, started_at, ended_at,
		       success, steps, optimal_steps, max_steps,
		       terminal_reason, walltime_ms, pos_x, pos_y, heading
		FROM robot_episodes
		ORDER BY started_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []episodeRow
	for rows.Next() {
		ep, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ep)
	}
	return out, rows.Err()
}

// scanEpisode handles both *sql.Row and *sql.Rows via the small
// rowScanner interface — same column order as cols above.
type rowScanner interface{ Scan(dest ...any) error }

func scanEpisode(s rowScanner) (*episodeRow, error) {
	var ep episodeRow
	var ended sql.NullTime
	var success int
	if err := s.Scan(
		&ep.ID, &ep.ScenarioID, &ep.Model, &ep.StartedAt, &ended,
		&success, &ep.Steps, &ep.OptimalSteps, &ep.MaxSteps,
		&ep.TerminalReason, &ep.WalltimeMs, &ep.PosX, &ep.PosY, &ep.Heading,
	); err != nil {
		return nil, err
	}
	ep.Success = success != 0
	if ended.Valid {
		t := ended.Time
		ep.EndedAt = &t
	}
	return &ep, nil
}

func updateEpisodeAfterStep(db *sql.DB, ep *episodeRow, terminalReason string, stepWalltime int64) error {
	ep.WalltimeMs += stepWalltime
	if terminalReason == "" {
		_, err := db.Exec(`
			UPDATE robot_episodes
			SET steps = ?, pos_x = ?, pos_y = ?, heading = ?, walltime_ms = ?
			WHERE id = ?`,
			ep.Steps, ep.PosX, ep.PosY, ep.Heading, ep.WalltimeMs, ep.ID)
		return err
	}
	now := time.Now().UTC()
	ep.EndedAt = &now
	ep.TerminalReason = terminalReason
	successInt := 0
	if ep.Success {
		successInt = 1
	}
	_, err := db.Exec(`
		UPDATE robot_episodes
		SET steps = ?, pos_x = ?, pos_y = ?, heading = ?,
		    walltime_ms = ?, ended_at = ?, success = ?, terminal_reason = ?
		WHERE id = ?`,
		ep.Steps, ep.PosX, ep.PosY, ep.Heading, ep.WalltimeMs,
		now, successInt, terminalReason, ep.ID)
	return err
}

func writeStep(db *sql.DB, episodeID string, step int, tool string,
	args, result map[string]any, posX, posY int, walltimeMs int64,
) error {
	argsBytes, _ := json.Marshal(args)
	resBytes, _ := json.Marshal(result)
	_, err := db.Exec(`
		INSERT INTO robot_episode_steps
		    (episode_id, step, tool, args_json, result_json, pos_x, pos_y, walltime_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		episodeID, step, tool, string(argsBytes), string(resBytes),
		posX, posY, walltimeMs)
	return err
}

// StepRow is the read-side shape for the activity feed. Result is
// returned as raw JSON so the panel can render whatever the tool
// emitted without the Go side having to type-define every variant.
type StepRow struct {
	Step       int             `json:"step"`
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args"`
	Result     json.RawMessage `json:"result"`
	PosX       int             `json:"pos_x"`
	PosY       int             `json:"pos_y"`
	WalltimeMs int64           `json:"walltime_ms"`
	CreatedAt  string          `json:"created_at"`
}

func listSteps(db *sql.DB, episodeID string, limit int) ([]StepRow, error) {
	rows, err := db.Query(`
		SELECT step, tool, args_json, result_json, pos_x, pos_y, walltime_ms, created_at
		FROM robot_episode_steps
		WHERE episode_id = ?
		ORDER BY step ASC
		LIMIT ?`, episodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StepRow
	for rows.Next() {
		var r StepRow
		var args, res string
		if err := rows.Scan(&r.Step, &r.Tool, &args, &res, &r.PosX, &r.PosY, &r.WalltimeMs, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Args = json.RawMessage(args)
		r.Result = json.RawMessage(res)
		out = append(out, r)
	}
	return out, rows.Err()
}
