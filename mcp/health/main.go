// Apteva Health app — personal weight / sleep / workout / mood log.
//
// One flexible time-series table (`metrics`) + a list-shaped table
// for workouts. The agent's leverage point is `health_log`: a single
// NL one-liner that routes to the right table with the right unit.
// Everything else hangs off that — charts read metrics, summaries
// aggregate metrics + workouts, goals threshold metrics.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: health
display_name: Health
version: 0.1.0
description: Personal health log — metrics, workouts, goals, NL ingest.
author: Apteva
scopes: [project, global]
requires:
  permissions: [db.write.app]
  integrations: []
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: health_log,      description: "Log a NL one-liner." }
    - { name: metrics_record,  description: "Structured metric insert." }
    - { name: metrics_list,    description: "Window query for one kind." }
    - { name: metrics_kinds,   description: "List logged kinds with last value." }
    - { name: metrics_summary, description: "Per-kind avg/min/max/last/streak in a window." }
    - { name: metrics_delete,  description: "Delete a metric row." }
    - { name: workouts_log,    description: "Structured workout insert." }
    - { name: workouts_list,   description: "Recent workouts." }
    - { name: workouts_delete, description: "Delete a workout." }
    - { name: goals_set,       description: "Upsert a goal." }
    - { name: goals_list,      description: "List configured goals." }
    - { name: goals_status,    description: "Per-goal pass/fail for current period." }
    - { name: goals_delete,    description: "Delete a goal." }
    - { name: pins_set,        description: "Pin metric kinds for the panel sidebar." }
    - { name: pins_get,        description: "Read pinned kinds." }
    - { name: health_summary,  description: "Free-text rollup over a window." }
  ui_panels:
    - slot: project.page
      label: Health
      icon: heart
      entry: /ui/HealthPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/health
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/health.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

// ─── Kind catalogue ──────────────────────────────────────────────
//
// Known metric kinds get a default unit and a chart hint. Unknown
// kinds still work — they just render with whatever unit the
// caller passed (or none) and the panel falls back to a raw axis.

type kindMeta struct {
	Unit       string  // canonical unit
	ChartLow   float64 // sensible y-axis floor (used by sparklines)
	ChartHigh  float64 // 0 means auto-scale
	Aliases    []string
	Pretty     string
}

var kindCatalog = map[string]kindMeta{
	"weight":        {Unit: "kg", Pretty: "Weight"},
	"sleep_hours":   {Unit: "h", ChartLow: 0, ChartHigh: 12, Aliases: []string{"sleep"}, Pretty: "Sleep"},
	"mood":          {Unit: "1-10", ChartLow: 1, ChartHigh: 10, Pretty: "Mood"},
	"energy":        {Unit: "1-10", ChartLow: 1, ChartHigh: 10, Pretty: "Energy"},
	"resting_hr":    {Unit: "bpm", Aliases: []string{"hr", "resting", "rhr"}, Pretty: "Resting HR"},
	"bp_systolic":   {Unit: "mmHg", Pretty: "BP Systolic"},
	"bp_diastolic":  {Unit: "mmHg", Pretty: "BP Diastolic"},
	"steps":         {Unit: "steps", Pretty: "Steps"},
	"water_ml":      {Unit: "ml", Aliases: []string{"water"}, Pretty: "Water"},
	"calories":      {Unit: "kcal", Pretty: "Calories"},
	"body_fat":      {Unit: "%", Pretty: "Body fat"},
}

// resolveKind picks a canonical kind name from a raw token. Returns
// (canonical, unit) — unit is the catalogue default for the kind,
// or empty if the kind is unknown.
func resolveKind(raw string) (string, string) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if m, ok := kindCatalog[raw]; ok {
		return raw, m.Unit
	}
	for canonical, meta := range kindCatalog {
		for _, a := range meta.Aliases {
			if a == raw {
				return canonical, meta.Unit
			}
		}
	}
	return raw, ""
}

// ─── App boilerplate ─────────────────────────────────────────────

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("health requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("health mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/metrics", Handler: a.handleMetrics},
		{Pattern: "/metrics/", Handler: a.handleMetricsItem},
		{Pattern: "/kinds", Handler: a.handleKinds},
		{Pattern: "/workouts", Handler: a.handleWorkouts},
		{Pattern: "/workouts/", Handler: a.handleWorkoutsItem},
		{Pattern: "/goals", Handler: a.handleGoals},
		{Pattern: "/goals/", Handler: a.handleGoalsItem},
		{Pattern: "/goals_status", Handler: a.handleGoalsStatus},
		{Pattern: "/pins", Handler: a.handlePins},
		{Pattern: "/log", Handler: a.handleLog},
		{Pattern: "/summary", Handler: a.handleSummary},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "health_log",
			Description: "Log a single natural-language line. Recognised shapes: 'weight 78.4', 'weight 172 lb yesterday', 'slept 7h tossing', 'mood 6/10', 'bp 122/78', 'hr 58', 'ran 5k 26min', 'lifted 45min legs', 'walked 30min'. Routes to metrics or workouts and returns the row(s) created. Args: text (required), source? (human|agent|device).",
			InputSchema: schemaObject(map[string]any{
				"text":   map[string]any{"type": "string"},
				"source": map[string]any{"type": "string"},
			}, []string{"text"}),
			Handler: a.toolHealthLog},

		{Name: "metrics_record",
			Description: "Insert a metric reading. Args: kind (required, e.g. 'weight'|'sleep_hours'|'mood'|'resting_hr'|'bp_systolic'|'bp_diastolic'|'steps'|'water_ml' or any custom string), value (required), unit? (defaults from catalogue), recorded_at? (RFC3339 or YYYY-MM-DD; default now), notes?, source? (human|agent|device).",
			InputSchema: schemaObject(map[string]any{
				"kind":        map[string]any{"type": "string"},
				"value":       map[string]any{"type": "number"},
				"unit":        map[string]any{"type": "string"},
				"recorded_at": map[string]any{"type": "string"},
				"notes":       map[string]any{"type": "string"},
				"source":      map[string]any{"type": "string"},
			}, []string{"kind", "value"}),
			Handler: a.toolMetricsRecord},

		{Name: "metrics_list",
			Description: "List points for one kind in a window. Args: kind (required), from? (RFC3339 or 'today'|'7d'|'30d'|'90d'|'all'; default '30d'), to?, agg? (none|daily|weekly; default 'none'), limit?.",
			InputSchema: schemaObject(map[string]any{
				"kind":  map[string]any{"type": "string"},
				"from":  map[string]any{"type": "string"},
				"to":    map[string]any{"type": "string"},
				"agg":   map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			}, []string{"kind"}),
			Handler: a.toolMetricsList},

		{Name: "metrics_kinds",
			Description: "List the metric kinds you've logged with most-recent value, unit and count.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolMetricsKinds},

		{Name: "metrics_summary",
			Description: "Per-kind avg/min/max/last/count over a window. Args: window? (today|week|month|90d|all; default 'week'), kinds? (array; default = pinned kinds, falling back to all).",
			InputSchema: schemaObject(map[string]any{
				"window": map[string]any{"type": "string"},
				"kinds":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}, nil),
			Handler: a.toolMetricsSummary},

		{Name: "metrics_delete",
			Description: "Delete one metric row. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolMetricsDelete},

		{Name: "workouts_log",
			Description: "Log a workout. Args: kind (required, e.g. 'run'|'ride'|'lift'|'yoga'|'walk'|'swim'|'hike'|'other'), started_at? (default now), duration_min (required), distance_km?, avg_hr?, perceived? (1-10), notes?, source?.",
			InputSchema: schemaObject(map[string]any{
				"kind":         map[string]any{"type": "string"},
				"started_at":   map[string]any{"type": "string"},
				"duration_min": map[string]any{"type": "integer"},
				"distance_km":  map[string]any{"type": "number"},
				"avg_hr":       map[string]any{"type": "integer"},
				"perceived":    map[string]any{"type": "integer"},
				"notes":        map[string]any{"type": "string"},
				"source":       map[string]any{"type": "string"},
			}, []string{"kind", "duration_min"}),
			Handler: a.toolWorkoutsLog},

		{Name: "workouts_list",
			Description: "Recent workouts. Args: from? (default '30d'), to?, limit? (default 100).",
			InputSchema: schemaObject(map[string]any{
				"from":  map[string]any{"type": "string"},
				"to":    map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolWorkoutsList},

		{Name: "workouts_delete",
			Description: "Delete a workout. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolWorkoutsDelete},

		{Name: "goals_set",
			Description: "Upsert a goal threshold. Args: kind (required; can be 'workouts' for the special workout-count goal), op (gte|lte|eq), target (number), cadence (daily|weekly).",
			InputSchema: schemaObject(map[string]any{
				"kind":    map[string]any{"type": "string"},
				"op":      map[string]any{"type": "string", "enum": []string{"gte", "lte", "eq"}},
				"target":  map[string]any{"type": "number"},
				"cadence": map[string]any{"type": "string", "enum": []string{"daily", "weekly"}},
			}, []string{"kind", "op", "target"}),
			Handler: a.toolGoalsSet},

		{Name: "goals_list",
			Description: "List configured goals.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolGoalsList},

		{Name: "goals_status",
			Description: "Pass/fail per goal for the current period (today for daily, this week for weekly).",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolGoalsStatus},

		{Name: "goals_delete",
			Description: "Delete a goal. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolGoalsDelete},

		{Name: "pins_set",
			Description: "Pin metric kinds for the panel sidebar. Replaces the full list. Args: kinds (array of kind names).",
			InputSchema: schemaObject(map[string]any{
				"kinds": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}, []string{"kinds"}),
			Handler: a.toolPinsSet},

		{Name: "pins_get",
			Description: "Read the current pinned-kinds list.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolPinsGet},

		{Name: "health_summary",
			Description: "Free-text markdown rollup of the last window. Args: window (today|week|month; default 'week').",
			InputSchema: schemaObject(map[string]any{"window": map[string]any{"type": "string"}}, nil),
			Handler:     a.toolHealthSummary},
	}
}

// ─── Models ──────────────────────────────────────────────────────

type Metric struct {
	ID         int64   `json:"id"`
	Kind       string  `json:"kind"`
	Value      float64 `json:"value"`
	Unit       string  `json:"unit"`
	Notes      string  `json:"notes"`
	Source     string  `json:"source"`
	RecordedAt string  `json:"recorded_at"`
	CreatedAt  string  `json:"created_at"`
}

type Workout struct {
	ID          int64    `json:"id"`
	Kind        string   `json:"kind"`
	StartedAt   string   `json:"started_at"`
	DurationMin int      `json:"duration_min"`
	DistanceKm  *float64 `json:"distance_km,omitempty"`
	AvgHR       *int     `json:"avg_hr,omitempty"`
	Perceived   *int     `json:"perceived,omitempty"`
	Notes       string   `json:"notes"`
	Source      string   `json:"source"`
	CreatedAt   string   `json:"created_at"`
}

type Goal struct {
	ID      int64   `json:"id"`
	Kind    string  `json:"kind"`
	Op      string  `json:"op"`
	Target  float64 `json:"target"`
	Cadence string  `json:"cadence"`
	Enabled bool    `json:"enabled"`
}

type GoalStatus struct {
	Goal     Goal    `json:"goal"`
	Period   string  `json:"period"`
	Observed float64 `json:"observed"`
	Pass     bool    `json:"pass"`
}

// ─── DB helpers ──────────────────────────────────────────────────

func projectScope() string {
	if pid := os.Getenv("APTEVA_PROJECT_ID"); pid != "" {
		return pid
	}
	return "default"
}

func insertMetric(db *sql.DB, pid, kind string, value float64, unit, notes, source, recordedAt string) (*Metric, error) {
	if recordedAt == "" {
		recordedAt = time.Now().UTC().Format(time.RFC3339)
	} else {
		recordedAt = normaliseTime(recordedAt)
	}
	if source == "" {
		source = "human"
	}
	canonical, defUnit := resolveKind(kind)
	if unit == "" {
		unit = defUnit
	}
	res, err := db.Exec(
		`INSERT INTO metrics (project_id, kind, value, unit, notes, source, recorded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		pid, canonical, value, unit, notes, source, recordedAt,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getMetric(db, pid, id)
}

func getMetric(db *sql.DB, pid string, id int64) (*Metric, error) {
	var m Metric
	err := db.QueryRow(
		`SELECT id, kind, value, unit, notes, source, recorded_at, created_at
		   FROM metrics WHERE id = ? AND project_id = ?`, id, pid,
	).Scan(&m.ID, &m.Kind, &m.Value, &m.Unit, &m.Notes, &m.Source, &m.RecordedAt, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// listMetrics returns rows, optionally aggregated. agg='daily' or
// 'weekly' returns one synthetic row per bucket with avg(value) — id
// is 0, recorded_at is the bucket start.
func listMetrics(db *sql.DB, pid, kind, from, to, agg string, limit int) ([]Metric, error) {
	if limit <= 0 {
		limit = 1000
	}
	canonical, _ := resolveKind(kind)
	fromT, toT := resolveWindow(from, to, "30d")

	if agg == "" || agg == "none" {
		rows, err := db.Query(
			`SELECT id, kind, value, unit, notes, source, recorded_at, created_at
			   FROM metrics
			  WHERE project_id = ? AND kind = ?
			    AND recorded_at >= ? AND recorded_at <= ?
			  ORDER BY recorded_at ASC
			  LIMIT ?`,
			pid, canonical, fromT, toT, limit,
		)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []Metric{}
		for rows.Next() {
			var m Metric
			if err := rows.Scan(&m.ID, &m.Kind, &m.Value, &m.Unit, &m.Notes, &m.Source, &m.RecordedAt, &m.CreatedAt); err != nil {
				return nil, err
			}
			out = append(out, m)
		}
		return out, nil
	}

	bucket := "date(recorded_at)"
	if agg == "weekly" {
		bucket = "date(recorded_at, 'weekday 0', '-6 days')"
	}
	rows, err := db.Query(
		`SELECT `+bucket+` AS bucket, AVG(value), COUNT(*),
		        (SELECT unit FROM metrics m2
		           WHERE m2.project_id = m.project_id AND m2.kind = m.kind
		           ORDER BY recorded_at DESC LIMIT 1)
		   FROM metrics m
		  WHERE project_id = ? AND kind = ?
		    AND recorded_at >= ? AND recorded_at <= ?
		  GROUP BY bucket ORDER BY bucket ASC`,
		pid, canonical, fromT, toT,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Metric{}
	for rows.Next() {
		var b string
		var avg float64
		var count int
		var unit sql.NullString
		if err := rows.Scan(&b, &avg, &count, &unit); err != nil {
			return nil, err
		}
		out = append(out, Metric{
			Kind:       canonical,
			Value:      round2(avg),
			Unit:       unit.String,
			RecordedAt: b,
			Notes:      fmt.Sprintf("avg of %d", count),
			Source:     "agg",
		})
	}
	return out, nil
}

func listKinds(db *sql.DB, pid string) ([]map[string]any, error) {
	rows, err := db.Query(
		`SELECT kind, COUNT(*),
		        (SELECT value FROM metrics m2
		           WHERE m2.project_id = m.project_id AND m2.kind = m.kind
		           ORDER BY recorded_at DESC LIMIT 1) AS last_value,
		        (SELECT unit FROM metrics m3
		           WHERE m3.project_id = m.project_id AND m3.kind = m.kind
		           ORDER BY recorded_at DESC LIMIT 1) AS last_unit,
		        MAX(recorded_at) AS last_at
		   FROM metrics m
		  WHERE project_id = ?
		  GROUP BY kind ORDER BY kind`,
		pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var kind string
		var count int
		var last sql.NullFloat64
		var unit sql.NullString
		var at sql.NullString
		if err := rows.Scan(&kind, &count, &last, &unit, &at); err != nil {
			return nil, err
		}
		row := map[string]any{
			"kind":       kind,
			"count":      count,
			"last_value": last.Float64,
			"unit":       unit.String,
			"last_at":    at.String,
		}
		if meta, ok := kindCatalog[kind]; ok {
			row["pretty"] = meta.Pretty
		}
		out = append(out, row)
	}
	return out, nil
}

func summariseMetrics(db *sql.DB, pid string, kinds []string, fromT, toT string) ([]map[string]any, error) {
	if len(kinds) == 0 {
		// All distinct kinds in the window.
		rows, err := db.Query(
			`SELECT DISTINCT kind FROM metrics
			  WHERE project_id = ? AND recorded_at >= ? AND recorded_at <= ?
			  ORDER BY kind`,
			pid, fromT, toT,
		)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return nil, err
			}
			kinds = append(kinds, k)
		}
	}
	out := []map[string]any{}
	for _, raw := range kinds {
		kind, _ := resolveKind(raw)
		var n int
		var avg, mn, mx, last sql.NullFloat64
		var unit sql.NullString
		err := db.QueryRow(
			`SELECT COUNT(*), AVG(value), MIN(value), MAX(value),
			        (SELECT value FROM metrics m2
			           WHERE m2.project_id = m.project_id AND m2.kind = m.kind
			             AND recorded_at >= ? AND recorded_at <= ?
			           ORDER BY recorded_at DESC LIMIT 1),
			        (SELECT unit FROM metrics m3
			           WHERE m3.project_id = m.project_id AND m3.kind = m.kind
			           ORDER BY recorded_at DESC LIMIT 1)
			   FROM metrics m
			  WHERE project_id = ? AND kind = ?
			    AND recorded_at >= ? AND recorded_at <= ?`,
			fromT, toT, pid, kind, fromT, toT,
		).Scan(&n, &avg, &mn, &mx, &last, &unit)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"kind":  kind,
			"count": n,
			"avg":   round2(avg.Float64),
			"min":   round2(mn.Float64),
			"max":   round2(mx.Float64),
			"last":  round2(last.Float64),
			"unit":  unit.String,
		})
	}
	return out, nil
}

func insertWorkout(db *sql.DB, pid string, w *Workout) (*Workout, error) {
	if w.StartedAt == "" {
		w.StartedAt = time.Now().UTC().Format(time.RFC3339)
	} else {
		w.StartedAt = normaliseTime(w.StartedAt)
	}
	if w.Source == "" {
		w.Source = "human"
	}
	res, err := db.Exec(
		`INSERT INTO workouts
		  (project_id, kind, started_at, duration_min, distance_km, avg_hr, perceived, notes, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, w.Kind, w.StartedAt, w.DurationMin,
		nullF64(w.DistanceKm), nullInt64(w.AvgHR), nullInt64(w.Perceived),
		w.Notes, w.Source,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getWorkout(db, pid, id)
}

func getWorkout(db *sql.DB, pid string, id int64) (*Workout, error) {
	var w Workout
	var dist sql.NullFloat64
	var hr, perc sql.NullInt64
	err := db.QueryRow(
		`SELECT id, kind, started_at, duration_min, distance_km, avg_hr, perceived, notes, source, created_at
		   FROM workouts WHERE id = ? AND project_id = ?`, id, pid,
	).Scan(&w.ID, &w.Kind, &w.StartedAt, &w.DurationMin, &dist, &hr, &perc, &w.Notes, &w.Source, &w.CreatedAt)
	if err != nil {
		return nil, err
	}
	if dist.Valid {
		v := dist.Float64
		w.DistanceKm = &v
	}
	if hr.Valid {
		v := int(hr.Int64)
		w.AvgHR = &v
	}
	if perc.Valid {
		v := int(perc.Int64)
		w.Perceived = &v
	}
	return &w, nil
}

func listWorkouts(db *sql.DB, pid, from, to string, limit int) ([]Workout, error) {
	if limit <= 0 {
		limit = 100
	}
	fromT, toT := resolveWindow(from, to, "30d")
	rows, err := db.Query(
		`SELECT id, kind, started_at, duration_min, distance_km, avg_hr, perceived, notes, source, created_at
		   FROM workouts
		  WHERE project_id = ? AND started_at >= ? AND started_at <= ?
		  ORDER BY started_at DESC LIMIT ?`,
		pid, fromT, toT, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Workout{}
	for rows.Next() {
		var w Workout
		var dist sql.NullFloat64
		var hr, perc sql.NullInt64
		if err := rows.Scan(&w.ID, &w.Kind, &w.StartedAt, &w.DurationMin, &dist, &hr, &perc, &w.Notes, &w.Source, &w.CreatedAt); err != nil {
			return nil, err
		}
		if dist.Valid {
			v := dist.Float64
			w.DistanceKm = &v
		}
		if hr.Valid {
			v := int(hr.Int64)
			w.AvgHR = &v
		}
		if perc.Valid {
			v := int(perc.Int64)
			w.Perceived = &v
		}
		out = append(out, w)
	}
	return out, nil
}

func listGoals(db *sql.DB, pid string) ([]Goal, error) {
	rows, err := db.Query(
		`SELECT id, kind, op, target, cadence, enabled FROM goals
		  WHERE project_id = ? ORDER BY kind, cadence`, pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Goal{}
	for rows.Next() {
		var g Goal
		var en int
		if err := rows.Scan(&g.ID, &g.Kind, &g.Op, &g.Target, &g.Cadence, &en); err != nil {
			return nil, err
		}
		g.Enabled = en == 1
		out = append(out, g)
	}
	return out, nil
}

// goalStatus computes pass/fail for a goal in its current period.
// 'workouts' is a special kind that counts workout sessions.
func goalStatus(db *sql.DB, pid string, g Goal) (GoalStatus, error) {
	var fromT, toT string
	switch g.Cadence {
	case "weekly":
		fromT, toT = weekBounds(time.Now().UTC())
	default:
		fromT, toT = dayBounds(time.Now().UTC())
	}
	var observed float64
	if g.Kind == "workouts" {
		var n int
		err := db.QueryRow(
			`SELECT COUNT(*) FROM workouts
			  WHERE project_id = ? AND started_at >= ? AND started_at <= ?`,
			pid, fromT, toT,
		).Scan(&n)
		if err != nil {
			return GoalStatus{}, err
		}
		observed = float64(n)
	} else {
		var avg sql.NullFloat64
		err := db.QueryRow(
			`SELECT AVG(value) FROM metrics
			  WHERE project_id = ? AND kind = ?
			    AND recorded_at >= ? AND recorded_at <= ?`,
			pid, g.Kind, fromT, toT,
		).Scan(&avg)
		if err != nil {
			return GoalStatus{}, err
		}
		observed = avg.Float64
	}
	pass := false
	switch g.Op {
	case "gte":
		pass = observed >= g.Target
	case "lte":
		pass = observed <= g.Target
	case "eq":
		pass = observed == g.Target
	}
	return GoalStatus{
		Goal:     g,
		Period:   fromT + "/" + toT,
		Observed: round2(observed),
		Pass:     pass,
	}, nil
}

func getPref(db *sql.DB, pid, key string) string {
	var v string
	_ = db.QueryRow(`SELECT value FROM prefs WHERE project_id = ? AND key = ?`, pid, key).Scan(&v)
	return v
}

func setPref(db *sql.DB, pid, key, value string) error {
	_, err := db.Exec(
		`INSERT INTO prefs (project_id, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(project_id, key) DO UPDATE SET value = excluded.value`,
		pid, key, value,
	)
	return err
}

func getPins(db *sql.DB, pid string) []string {
	raw := getPref(db, pid, "pins")
	if raw == "" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// ─── NL parser ───────────────────────────────────────────────────
//
// healthLog dispatches on the first recognisable token. The grammar
// is deliberately small — anything it can't parse, it stores as a
// `notes` reading on a 'unparsed' kind so nothing is silently dropped.

var (
	reBP        = regexp.MustCompile(`^(\d{2,3})/(\d{2,3})$`)
	reMoodScale = regexp.MustCompile(`^(\d{1,2})/10$`)
	reHM        = regexp.MustCompile(`^(\d+)h(\d+)?m?$`)        // "1h30", "1h30m", "2h"
	reMin       = regexp.MustCompile(`^(\d+)(?:min|m)$`)        // "26min", "45m"
	reKm        = regexp.MustCompile(`^(\d+(?:\.\d+)?)k(?:m)?$`) // "5k", "5km", "10.5km"
	reFloat     = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)$`)
)

type logResult struct {
	Metrics  []Metric  `json:"metrics,omitempty"`
	Workouts []Workout `json:"workouts,omitempty"`
	Parsed   bool      `json:"parsed"`
	Reason   string    `json:"reason,omitempty"`
}

func healthLog(db *sql.DB, pid, text, source string) (*logResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("text required")
	}
	if source == "" {
		source = "agent"
	}

	// Time hints: "today" / "yesterday" / "now".
	recordedAt := ""
	lowText := strings.ToLower(text)
	if strings.Contains(lowText, "yesterday") {
		recordedAt = atNineAM(time.Now().UTC().AddDate(0, 0, -1))
		text = strings.ReplaceAll(text, "yesterday", "")
		text = strings.ReplaceAll(text, "Yesterday", "")
	} else if strings.Contains(lowText, "today") {
		text = strings.ReplaceAll(text, "today", "")
		text = strings.ReplaceAll(text, "Today", "")
	}
	text = strings.TrimSpace(text)

	tokens := strings.Fields(text)
	if len(tokens) == 0 {
		return nil, errors.New("nothing to parse")
	}
	head := strings.ToLower(tokens[0])

	// Workout shapes — verb first.
	if isWorkoutVerb(head) {
		return parseWorkout(db, pid, head, tokens[1:], recordedAt, source)
	}

	// Metric shapes — kind first.
	if head == "bp" || head == "blood" {
		return parseBP(db, pid, tokens, recordedAt, source)
	}
	if head == "slept" {
		return parseSleep(db, pid, tokens[1:], recordedAt, source)
	}
	// kind value [unit] [notes…]
	if len(tokens) >= 2 {
		kindRaw := head
		valRaw := tokens[1]
		// "mood 6/10"
		if m := reMoodScale.FindStringSubmatch(valRaw); m != nil {
			v, _ := strconv.ParseFloat(m[1], 64)
			row, err := insertMetric(db, pid, kindRaw, v, "1-10",
				strings.Join(tokens[2:], " "), source, recordedAt)
			if err != nil {
				return nil, err
			}
			return &logResult{Metrics: []Metric{*row}, Parsed: true}, nil
		}
		if m := reFloat.FindStringSubmatch(valRaw); m != nil {
			v, _ := strconv.ParseFloat(m[1], 64)
			unit := ""
			rest := tokens[2:]
			if len(rest) > 0 && isUnit(rest[0]) {
				unit = strings.ToLower(rest[0])
				rest = rest[1:]
			}
			row, err := insertMetric(db, pid, kindRaw, v, unit,
				strings.Join(rest, " "), source, recordedAt)
			if err != nil {
				return nil, err
			}
			return &logResult{Metrics: []Metric{*row}, Parsed: true}, nil
		}
	}

	// Fallback — store on 'unparsed' so nothing is dropped, but flag it.
	row, err := insertMetric(db, pid, "unparsed", 0, "", text, source, recordedAt)
	if err != nil {
		return nil, err
	}
	return &logResult{Metrics: []Metric{*row}, Parsed: false, Reason: "couldn't parse; stored as note"}, nil
}

func parseBP(db *sql.DB, pid string, tokens []string, recordedAt, source string) (*logResult, error) {
	for _, tok := range tokens[1:] {
		if m := reBP.FindStringSubmatch(tok); m != nil {
			sys, _ := strconv.ParseFloat(m[1], 64)
			dia, _ := strconv.ParseFloat(m[2], 64)
			a, err := insertMetric(db, pid, "bp_systolic", sys, "mmHg", "", source, recordedAt)
			if err != nil {
				return nil, err
			}
			b, err := insertMetric(db, pid, "bp_diastolic", dia, "mmHg", "", source, recordedAt)
			if err != nil {
				return nil, err
			}
			return &logResult{Metrics: []Metric{*a, *b}, Parsed: true}, nil
		}
	}
	return nil, errors.New("bp: expected '<systolic>/<diastolic>'")
}

func parseSleep(db *sql.DB, pid string, tokens []string, recordedAt, source string) (*logResult, error) {
	for i, tok := range tokens {
		// "7h", "7h30", "7h30m"
		if m := reHM.FindStringSubmatch(strings.ToLower(tok)); m != nil {
			h, _ := strconv.ParseFloat(m[1], 64)
			if m[2] != "" {
				min, _ := strconv.ParseFloat(m[2], 64)
				h += min / 60
			}
			notes := strings.Join(append([]string{}, tokens[i+1:]...), " ")
			row, err := insertMetric(db, pid, "sleep_hours", round2(h), "h", notes, source, recordedAt)
			if err != nil {
				return nil, err
			}
			return &logResult{Metrics: []Metric{*row}, Parsed: true}, nil
		}
		// "7" (bare number → hours)
		if v, err := strconv.ParseFloat(tok, 64); err == nil {
			notes := strings.Join(append([]string{}, tokens[i+1:]...), " ")
			row, err := insertMetric(db, pid, "sleep_hours", v, "h", notes, source, recordedAt)
			if err != nil {
				return nil, err
			}
			return &logResult{Metrics: []Metric{*row}, Parsed: true}, nil
		}
	}
	return nil, errors.New("slept: expected hours like '7h', '7h30m', or '7'")
}

func parseWorkout(db *sql.DB, pid, verb string, rest []string, recordedAt, source string) (*logResult, error) {
	kind := verbToKind(verb)
	w := &Workout{Kind: kind, Source: source, StartedAt: recordedAt}
	notes := []string{}
	for _, tok := range rest {
		low := strings.ToLower(tok)
		switch {
		case reKm.MatchString(low):
			m := reKm.FindStringSubmatch(low)
			d, _ := strconv.ParseFloat(m[1], 64)
			w.DistanceKm = &d
		case reHM.MatchString(low):
			m := reHM.FindStringSubmatch(low)
			h, _ := strconv.Atoi(m[1])
			minutes := h * 60
			if m[2] != "" {
				if mm, err := strconv.Atoi(m[2]); err == nil {
					minutes += mm
				}
			}
			w.DurationMin = minutes
		case reMin.MatchString(low):
			m := reMin.FindStringSubmatch(low)
			n, _ := strconv.Atoi(m[1])
			w.DurationMin = n
		default:
			notes = append(notes, tok)
		}
	}
	w.Notes = strings.Join(notes, " ")
	if w.DurationMin == 0 && w.DistanceKm == nil {
		return nil, fmt.Errorf("workout: need at least a duration ('30min', '1h') or distance ('5k')")
	}
	out, err := insertWorkout(db, pid, w)
	if err != nil {
		return nil, err
	}
	return &logResult{Workouts: []Workout{*out}, Parsed: true}, nil
}

func isWorkoutVerb(s string) bool {
	switch s {
	case "ran", "running", "run",
		"rode", "biking", "ride", "cycled", "cycling",
		"lifted", "lifting", "lift",
		"yoga",
		"walked", "walking", "walk",
		"swam", "swimming", "swim",
		"hiked", "hiking", "hike",
		"workout", "trained", "training":
		return true
	}
	return false
}

func verbToKind(verb string) string {
	switch verb {
	case "ran", "running", "run":
		return "run"
	case "rode", "biking", "ride", "cycled", "cycling":
		return "ride"
	case "lifted", "lifting", "lift":
		return "lift"
	case "yoga":
		return "yoga"
	case "walked", "walking", "walk":
		return "walk"
	case "swam", "swimming", "swim":
		return "swim"
	case "hiked", "hiking", "hike":
		return "hike"
	}
	return "other"
}

func isUnit(s string) bool {
	switch strings.ToLower(s) {
	case "kg", "lb", "lbs", "g", "h", "hr", "bpm", "mmhg",
		"%", "ml", "l", "kcal", "cal", "steps":
		return true
	}
	return false
}

// ─── Time helpers ────────────────────────────────────────────────

func resolveWindow(from, to, defWindow string) (string, string) {
	now := time.Now().UTC()
	parseShortcut := func(s string) (time.Time, bool) {
		switch strings.ToLower(s) {
		case "today":
			return now.Truncate(24 * time.Hour), true
		case "week", "7d":
			return now.AddDate(0, 0, -7), true
		case "month", "30d":
			return now.AddDate(0, 0, -30), true
		case "90d":
			return now.AddDate(0, 0, -90), true
		case "all":
			return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), true
		}
		return time.Time{}, false
	}
	fromT := now.AddDate(0, 0, -30)
	if t, ok := parseShortcut(defWindow); ok {
		fromT = t
	}
	if from != "" {
		if t, ok := parseShortcut(from); ok {
			fromT = t
		} else if parsed, err := parseFlexibleTime(from); err == nil {
			fromT = parsed
		}
	}
	toT := now.Add(time.Hour) // include "now" comfortably
	if to != "" {
		if parsed, err := parseFlexibleTime(to); err == nil {
			toT = parsed
		}
	}
	return fromT.Format(time.RFC3339), toT.Format(time.RFC3339)
}

func dayBounds(t time.Time) (string, string) {
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour).Add(-time.Second)
	return start.Format(time.RFC3339), end.Format(time.RFC3339)
}

func weekBounds(t time.Time) (string, string) {
	// Monday-start week.
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7
	}
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(wd - 1))
	end := start.AddDate(0, 0, 7).Add(-time.Second)
	return start.Format(time.RFC3339), end.Format(time.RFC3339)
}

func atNineAM(t time.Time) string {
	return time.Date(t.Year(), t.Month(), t.Day(), 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

func normaliseTime(s string) string {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Format(time.RFC3339)
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return atNineAM(t)
	}
	return time.Now().UTC().Format(time.RFC3339)
}

func parseFlexibleTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

// ─── HTTP handlers ───────────────────────────────────────────────

func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	pid := projectScope()
	switch r.Method {
	case http.MethodGet:
		kind := r.URL.Query().Get("kind")
		if kind == "" {
			http.Error(w, "kind required", 400)
			return
		}
		out, err := listMetrics(
			ctx.AppDB(), pid, kind,
			r.URL.Query().Get("from"),
			r.URL.Query().Get("to"),
			r.URL.Query().Get("agg"),
			atoiOr(r.URL.Query().Get("limit"), 0),
		)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body struct {
			Kind       string  `json:"kind"`
			Value      float64 `json:"value"`
			Unit       string  `json:"unit"`
			Notes      string  `json:"notes"`
			RecordedAt string  `json:"recorded_at"`
			Source     string  `json:"source"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Kind == "" {
			http.Error(w, "kind required", 400)
			return
		}
		out, err := insertMetric(ctx.AppDB(), pid, body.Kind, body.Value, body.Unit, body.Notes, body.Source, body.RecordedAt)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

func (a *App) handleMetricsItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE", 405)
		return
	}
	ctx := mustCtx(r)
	id := pathSuffixInt(r.URL.Path, "/metrics/")
	if _, err := ctx.AppDB().Exec(`DELETE FROM metrics WHERE id = ? AND project_id = ?`, id, projectScope()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleKinds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", 405)
		return
	}
	out, err := listKinds(mustCtx(r).AppDB(), projectScope())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleWorkouts(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	pid := projectScope()
	switch r.Method {
	case http.MethodGet:
		out, err := listWorkouts(
			ctx.AppDB(), pid,
			r.URL.Query().Get("from"),
			r.URL.Query().Get("to"),
			atoiOr(r.URL.Query().Get("limit"), 0),
		)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body struct {
			Kind        string   `json:"kind"`
			StartedAt   string   `json:"started_at"`
			DurationMin int      `json:"duration_min"`
			DistanceKm  *float64 `json:"distance_km"`
			AvgHR       *int     `json:"avg_hr"`
			Perceived   *int     `json:"perceived"`
			Notes       string   `json:"notes"`
			Source      string   `json:"source"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Kind == "" || body.DurationMin <= 0 {
			http.Error(w, "kind + duration_min required", 400)
			return
		}
		out, err := insertWorkout(ctx.AppDB(), pid, &Workout{
			Kind: body.Kind, StartedAt: body.StartedAt, DurationMin: body.DurationMin,
			DistanceKm: body.DistanceKm, AvgHR: body.AvgHR, Perceived: body.Perceived,
			Notes: body.Notes, Source: body.Source,
		})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

func (a *App) handleWorkoutsItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE", 405)
		return
	}
	id := pathSuffixInt(r.URL.Path, "/workouts/")
	if _, err := mustCtx(r).AppDB().Exec(
		`DELETE FROM workouts WHERE id = ? AND project_id = ?`, id, projectScope(),
	); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleGoals(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	pid := projectScope()
	switch r.Method {
	case http.MethodGet:
		out, err := listGoals(ctx.AppDB(), pid)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var g Goal
		json.NewDecoder(r.Body).Decode(&g)
		if g.Kind == "" || g.Op == "" {
			http.Error(w, "kind + op required", 400)
			return
		}
		if g.Cadence == "" {
			g.Cadence = "daily"
		}
		_, err := ctx.AppDB().Exec(
			`INSERT INTO goals (project_id, kind, op, target, cadence, enabled)
			 VALUES (?, ?, ?, ?, ?, 1)
			 ON CONFLICT(project_id, kind, cadence)
			 DO UPDATE SET op = excluded.op, target = excluded.target, enabled = 1`,
			pid, g.Kind, g.Op, g.Target, g.Cadence,
		)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		out, _ := listGoals(ctx.AppDB(), pid)
		writeJSON(w, out)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

func (a *App) handleGoalsItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE", 405)
		return
	}
	id := pathSuffixInt(r.URL.Path, "/goals/")
	if _, err := mustCtx(r).AppDB().Exec(
		`DELETE FROM goals WHERE id = ? AND project_id = ?`, id, projectScope(),
	); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleGoalsStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", 405)
		return
	}
	ctx := mustCtx(r)
	pid := projectScope()
	goals, err := listGoals(ctx.AppDB(), pid)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := []GoalStatus{}
	for _, g := range goals {
		s, err := goalStatus(ctx.AppDB(), pid, g)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		out = append(out, s)
	}
	writeJSON(w, out)
}

func (a *App) handlePins(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	pid := projectScope()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, getPins(ctx.AppDB(), pid))
	case http.MethodPost:
		var body struct{ Kinds []string }
		json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body.Kinds)
		if err := setPref(ctx.AppDB(), pid, "pins", string(raw)); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, body.Kinds)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

func (a *App) handleLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", 405)
		return
	}
	var body struct {
		Text   string `json:"text"`
		Source string `json:"source"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	out, err := healthLog(mustCtx(r).AppDB(), projectScope(), body.Text, body.Source)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", 405)
		return
	}
	window := r.URL.Query().Get("window")
	out, err := healthSummary(mustCtx(r).AppDB(), projectScope(), window)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

// healthSummary builds a small markdown brief plus structured numbers
// for the panel header. Keeping the markdown server-side means the
// agent can blast it into a thread without re-formatting.
func healthSummary(db *sql.DB, pid, window string) (map[string]any, error) {
	if window == "" {
		window = "week"
	}
	fromT, toT := resolveWindow(window, "", window)
	pins := getPins(db, pid)
	summary, err := summariseMetrics(db, pid, pins, fromT, toT)
	if err != nil {
		return nil, err
	}
	wks, err := listWorkouts(db, pid, window, "", 100)
	if err != nil {
		return nil, err
	}
	totalMin := 0
	for _, w := range wks {
		totalMin += w.DurationMin
	}
	md := strings.Builder{}
	fmt.Fprintf(&md, "**Health · %s**\n\n", window)
	for _, s := range summary {
		fmt.Fprintf(&md, "- %s: avg %v %s (last %v, n=%v)\n",
			s["kind"], s["avg"], s["unit"], s["last"], s["count"])
	}
	if len(wks) > 0 {
		fmt.Fprintf(&md, "\n**Workouts**: %d sessions · %d min total\n", len(wks), totalMin)
		for i, w := range wks {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&md, "- %s · %s · %dm", w.StartedAt[:10], w.Kind, w.DurationMin)
			if w.DistanceKm != nil {
				fmt.Fprintf(&md, " · %.1fkm", *w.DistanceKm)
			}
			if w.Notes != "" {
				fmt.Fprintf(&md, " · %s", w.Notes)
			}
			md.WriteString("\n")
		}
	}
	return map[string]any{
		"window":      window,
		"metrics":     summary,
		"workouts":    map[string]any{"count": len(wks), "total_min": totalMin, "rows": wks},
		"markdown":    md.String(),
	}, nil
}

// ─── MCP tool handlers ───────────────────────────────────────────

func (a *App) toolHealthLog(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	text, _ := args["text"].(string)
	source, _ := args["source"].(string)
	return healthLog(ctx.AppDB(), projectScope(), text, source)
}

func (a *App) toolMetricsRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	kind, _ := args["kind"].(string)
	if kind == "" {
		return nil, errors.New("kind required")
	}
	val, ok := args["value"].(float64)
	if !ok {
		return nil, errors.New("value required (number)")
	}
	return insertMetric(
		ctx.AppDB(), projectScope(),
		kind, val,
		strArg(args, "unit", ""),
		strArg(args, "notes", ""),
		strArg(args, "source", "agent"),
		strArg(args, "recorded_at", ""),
	)
}

func (a *App) toolMetricsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	kind, _ := args["kind"].(string)
	if kind == "" {
		return nil, errors.New("kind required")
	}
	return listMetrics(
		ctx.AppDB(), projectScope(),
		kind,
		strArg(args, "from", ""),
		strArg(args, "to", ""),
		strArg(args, "agg", "none"),
		int(toInt64(args["limit"])),
	)
}

func (a *App) toolMetricsKinds(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return listKinds(ctx.AppDB(), projectScope())
}

func (a *App) toolMetricsSummary(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	window := strArg(args, "window", "week")
	fromT, toT := resolveWindow(window, "", window)
	var kinds []string
	if v, ok := args["kinds"]; ok {
		ks, err := toStringSlice(v)
		if err != nil {
			return nil, err
		}
		kinds = ks
	}
	if len(kinds) == 0 {
		kinds = getPins(ctx.AppDB(), projectScope())
	}
	return summariseMetrics(ctx.AppDB(), projectScope(), kinds, fromT, toT)
}

func (a *App) toolMetricsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	if _, err := ctx.AppDB().Exec(
		`DELETE FROM metrics WHERE id = ? AND project_id = ?`, id, projectScope(),
	); err != nil {
		return nil, err
	}
	return map[string]any{"status": "deleted", "id": id}, nil
}

func (a *App) toolWorkoutsLog(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	kind, _ := args["kind"].(string)
	if kind == "" {
		return nil, errors.New("kind required")
	}
	dur := int(toInt64(args["duration_min"]))
	if dur <= 0 {
		return nil, errors.New("duration_min required")
	}
	w := &Workout{
		Kind:        kind,
		StartedAt:   strArg(args, "started_at", ""),
		DurationMin: dur,
		Notes:       strArg(args, "notes", ""),
		Source:      strArg(args, "source", "agent"),
	}
	if v, ok := args["distance_km"].(float64); ok {
		w.DistanceKm = &v
	}
	if v := int(toInt64(args["avg_hr"])); v != 0 {
		w.AvgHR = &v
	}
	if v := int(toInt64(args["perceived"])); v != 0 {
		w.Perceived = &v
	}
	return insertWorkout(ctx.AppDB(), projectScope(), w)
}

func (a *App) toolWorkoutsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listWorkouts(
		ctx.AppDB(), projectScope(),
		strArg(args, "from", ""),
		strArg(args, "to", ""),
		int(toInt64(args["limit"])),
	)
}

func (a *App) toolWorkoutsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	if _, err := ctx.AppDB().Exec(
		`DELETE FROM workouts WHERE id = ? AND project_id = ?`, id, projectScope(),
	); err != nil {
		return nil, err
	}
	return map[string]any{"status": "deleted", "id": id}, nil
}

func (a *App) toolGoalsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	g := Goal{
		Kind:    strArg(args, "kind", ""),
		Op:      strArg(args, "op", ""),
		Target:  toFloat64(args["target"]),
		Cadence: strArg(args, "cadence", "daily"),
	}
	if g.Kind == "" || g.Op == "" {
		return nil, errors.New("kind + op required")
	}
	pid := projectScope()
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO goals (project_id, kind, op, target, cadence, enabled)
		 VALUES (?, ?, ?, ?, ?, 1)
		 ON CONFLICT(project_id, kind, cadence)
		 DO UPDATE SET op = excluded.op, target = excluded.target, enabled = 1`,
		pid, g.Kind, g.Op, g.Target, g.Cadence,
	); err != nil {
		return nil, err
	}
	return listGoals(ctx.AppDB(), pid)
}

func (a *App) toolGoalsList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return listGoals(ctx.AppDB(), projectScope())
}

func (a *App) toolGoalsStatus(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	pid := projectScope()
	goals, err := listGoals(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	out := []GoalStatus{}
	for _, g := range goals {
		s, err := goalStatus(ctx.AppDB(), pid, g)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (a *App) toolGoalsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	if _, err := ctx.AppDB().Exec(
		`DELETE FROM goals WHERE id = ? AND project_id = ?`, id, projectScope(),
	); err != nil {
		return nil, err
	}
	return map[string]any{"status": "deleted", "id": id}, nil
}

func (a *App) toolPinsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	v, ok := args["kinds"]
	if !ok {
		return nil, errors.New("kinds required")
	}
	names, err := toStringSlice(v)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(names)
	if err := setPref(ctx.AppDB(), projectScope(), "pins", string(raw)); err != nil {
		return nil, err
	}
	return names, nil
}

func (a *App) toolPinsGet(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	pins := getPins(ctx.AppDB(), projectScope())
	if pins == nil {
		pins = []string{}
	}
	sort.Strings(pins)
	return pins, nil
}

func (a *App) toolHealthSummary(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return healthSummary(ctx.AppDB(), projectScope(), strArg(args, "window", "week"))
}

// ─── helpers ─────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func toFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case string:
		n, _ := strconv.ParseFloat(x, 64)
		return n
	}
	return 0
}

func toStringSlice(v any) ([]string, error) {
	switch x := v.(type) {
	case []string:
		return x, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out, nil
	case nil:
		return nil, nil
	}
	return nil, fmt.Errorf("expected array, got %T", v)
}

func strArg(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return def
}

func nullF64(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullInt64(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

func pathSuffixInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func mustCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

// ─── main ────────────────────────────────────────────────────────

func main() {
	app := &App{}
	wrapped := wrapApp{app: app}
	sdk.Run(&wrapped)
}

type wrapApp struct{ app *App }

func (w *wrapApp) Manifest() sdk.Manifest             { return w.app.Manifest() }
func (w *wrapApp) OnMount(ctx *sdk.AppCtx) error      { globalCtx = ctx; return w.app.OnMount(ctx) }
func (w *wrapApp) OnUnmount(c *sdk.AppCtx) error      { return w.app.OnUnmount(c) }
func (w *wrapApp) HTTPRoutes() []sdk.Route            { return w.app.HTTPRoutes() }
func (w *wrapApp) MCPTools() []sdk.Tool               { return w.app.MCPTools() }
func (w *wrapApp) Channels() []sdk.ChannelFactory     { return w.app.Channels() }
func (w *wrapApp) Workers() []sdk.Worker              { return w.app.Workers() }
func (w *wrapApp) EventHandlers() []sdk.EventHandler  { return w.app.EventHandlers() }
