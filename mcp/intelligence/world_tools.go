package main

// MCP tools and panel routes for the world model, forecasts and scenarios.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func prop(t string) map[string]any { return map[string]any{"type": t} }

func (a *App) worldTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "overview", Description: "What changed: new evidence by source, forecasts that moved and why, questions closing soon, resolutions, and source health. Args: since? (RFC3339/YYYY-MM-DD, default 24h ago).",
			InputSchema: schemaObject(map[string]any{"since": prop("string")}, nil), Handler: a.toolOverview},
		{Name: "sources_list", Description: "Ingestion connectors with permitted use, licence, freshness, cost meter, last error, and recent runs.",
			InputSchema: schemaObject(nil, nil), Handler: a.toolSourcesList},
		{Name: "source_configure", Description: "Enable/disable a connector, change its interval, replace its config (e.g. news watch_topics, BLS/FRED series with optional region, market horizon), set its access class (open|internal|restricted, applied to evidence it records from now on) or its retention_days (content of older evidence is removed; 0 keeps it). Args: slug, enabled?, interval_sec?, config?, access?, retention_days?.",
			InputSchema: schemaObject(map[string]any{"slug": prop("string"), "enabled": prop("boolean"), "interval_sec": prop("integer"), "config": prop("object"), "access": prop("string"), "retention_days": prop("integer")}, []string{"slug"}), Handler: a.toolSourceConfigure},
		{Name: "ingest_now", Description: "Run connectors now (all, or one source), then claim extraction and forecast updates. Args: source?, skip_forecasts?.",
			InputSchema: schemaObject(map[string]any{"source": prop("string"), "skip_forecasts": prop("boolean")}, nil), Handler: a.toolIngestNow},
		{Name: "entities_search", Description: "Search world-model entities by name/alias and kind. Args: query?, kind?, limit?.",
			InputSchema: schemaObject(map[string]any{"query": prop("string"), "kind": prop("string"), "limit": prop("integer")}, nil), Handler: a.toolEntitiesSearch},
		{Name: "entity_get", Description: "One entity with its relationships (and their evidence status), claims, measured state series and linked open questions. Args: id, as_of?.",
			InputSchema: schemaObject(map[string]any{"id": prop("integer"), "as_of": prop("string")}, []string{"id"}), Handler: a.toolEntityGet},
		{Name: "entity_upsert", Description: "Create or merge an entity. Kinds: organization, facility, place, region, commodity, product, technology, market, institution, infrastructure, event, indicator, public_role, concept. Private individuals are out of scope. Args: name, kind, description?, aliases?, external_ids?.",
			InputSchema: schemaObject(map[string]any{"name": prop("string"), "kind": prop("string"), "description": prop("string"), "aliases": map[string]any{"type": "array", "items": prop("string")}, "external_ids": prop("object")}, []string{"name", "kind"}), Handler: a.toolEntityUpsert},
		{Name: "relationship_assert", Description: "Record a typed, time-bounded relationship. status observed|reported need evidence_ids; use assumed for operator assumptions. causal_basis says what the sources support: mechanism (they explain how the effect passes), asserted (stated without how), co_occurrence (only mentioned together), unsupported. Args: from, type, to (entity ids or names), status, confidence?, valid_from?, valid_to?, evidence_ids?, note?, causal_basis?, causal_note?.",
			InputSchema: schemaObject(map[string]any{"from": prop("string"), "type": prop("string"), "to": prop("string"), "status": prop("string"), "confidence": prop("number"), "valid_from": prop("string"), "valid_to": prop("string"), "evidence_ids": map[string]any{"type": "array", "items": prop("integer")}, "note": prop("string"), "causal_basis": prop("string"), "causal_note": prop("string")}, []string{"from", "type", "to", "status"}), Handler: a.toolRelationshipAssert},
		{Name: "relationship_retract", Description: "Retract a relationship from now on (history is kept). Args: id.",
			InputSchema: schemaObject(map[string]any{"id": prop("integer")}, []string{"id"}), Handler: a.toolRelationshipRetract},
		{Name: "graph_neighbors", Description: "Entities and relationships within depth hops of an entity, valid at as_of. Args: entity_id, depth? (1-3), as_of?.",
			InputSchema: schemaObject(map[string]any{"entity_id": prop("integer"), "depth": prop("integer"), "as_of": prop("string")}, []string{"entity_id"}), Handler: a.toolGraphNeighbors},
		{Name: "claims_list", Description: "Claims extracted from evidence. Args: evidence_id?, entity_id?, status? (proposed|accepted|rejected), limit?.",
			InputSchema: schemaObject(map[string]any{"evidence_id": prop("integer"), "entity_id": prop("integer"), "status": prop("string"), "limit": prop("integer")}, nil), Handler: a.toolClaimsList},
		{Name: "claim_review", Description: "Accept or reject an extracted claim. Args: id, status.",
			InputSchema: schemaObject(map[string]any{"id": prop("integer"), "status": prop("string")}, []string{"id", "status"}), Handler: a.toolClaimReview},
		{Name: "question_create", Description: "Create a resolvable question. The resolution rule is frozen once created. Args: text, resolution_rule, resolution_source, deadline, topics?, entity_ids?, category?.",
			InputSchema: schemaObject(map[string]any{"text": prop("string"), "resolution_rule": prop("string"), "resolution_source": prop("string"), "deadline": prop("string"), "topics": map[string]any{"type": "array", "items": prop("string")}, "entity_ids": map[string]any{"type": "array", "items": prop("integer")}, "category": prop("string")}, []string{"text", "resolution_rule", "resolution_source", "deadline"}), Handler: a.toolQuestionCreate},
		{Name: "questions_list", Description: "Questions with latest forecast and market price. Args: status? (open|resolved|void|all, default open), limit?.",
			InputSchema: schemaObject(map[string]any{"status": prop("string"), "limit": prop("integer")}, nil), Handler: a.toolQuestionsList},
		{Name: "question_get", Description: "A question with every forecast revision (explanations, evidence, cutoffs), its market price series and linked evidence. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": prop("integer")}, []string{"id"}), Handler: a.toolQuestionGet},
		{Name: "forecast_now", Description: "Record a new forecast revision for a question using evidence ingested up to now. Args: question_id.",
			InputSchema: schemaObject(map[string]any{"question_id": prop("integer")}, []string{"question_id"}), Handler: a.toolForecastNow},
		{Name: "question_resolve", Description: "Resolve a manual question (outcome yes/no) or void any question. Market questions resolve automatically from their venue. Args: id, outcome? (yes|no), void?, note.",
			InputSchema: schemaObject(map[string]any{"id": prop("integer"), "outcome": prop("string"), "void": prop("boolean"), "note": prop("string")}, []string{"id", "note"}), Handler: a.toolQuestionResolve},
		{Name: "ledger", Description: "Outcome ledger: one scored forecast per resolved question vs market and uniform baselines (Brier, log loss, skill), calibration, coverage, by horizon and venue. Args: lock_hours? (default 24).",
			InputSchema: schemaObject(map[string]any{"lock_hours": prop("number")}, nil), Handler: a.toolLedger},
		{Name: "scenario_run", Description: "Conditional what-if, not a forecast: propagate stated shocks through typed relationships known at known_at and valid at as_of; returns exposed entities with each link's causal basis and sources, open questions (forecasts unchanged), the evidence profile, limitations and a snapshot hash for replay. By default only mechanism, asserted and assumed links carry a shock; include_weak adds co_occurrence and ungraded links. Args: name?, shocks [{entity_id|entity, description, severity 0-1}], rules? {type:{factor,direction}}, as_of?, known_at? (default: as_of if past, else now), max_depth?, min_impact?, skip_assumed?, include_weak?, ignore_confidence?.",
			InputSchema: schemaObject(map[string]any{"name": prop("string"), "shocks": map[string]any{"type": "array", "items": prop("object")}, "rules": prop("object"), "as_of": prop("string"), "known_at": prop("string"), "max_depth": prop("integer"), "min_impact": prop("number"), "skip_assumed": prop("boolean"), "include_weak": prop("boolean"), "ignore_confidence": prop("boolean")}, []string{"shocks"}), Handler: a.toolScenarioRun},
		{Name: "scenario_replay", Description: "Re-run a stored scenario from its spec and check it reproduces: same graph hash and same exposures. Args: scenario_id.",
			InputSchema: schemaObject(map[string]any{"scenario_id": prop("integer")}, []string{"scenario_id"}), Handler: a.toolScenarioReplay},
		{Name: "scenario_sensitivity", Description: "Assumption and sensitivity report for a stored scenario: assumption table, one-at-a-time changes, seeded Monte Carlo ranges, single-link fragility and robust/sensitive/fragile labels, plus the latest historical test. Stored on the scenario. Args: scenario_id, draws? (default 200), seed?.",
			InputSchema: schemaObject(map[string]any{"scenario_id": prop("integer"), "draws": prop("integer"), "seed": prop("integer")}, []string{"scenario_id"}), Handler: a.toolScenarioSensitivity},
		{Name: "scenario_report", Description: "A stored scenario with its result and latest assumption and sensitivity report. Args: scenario_id.",
			InputSchema: schemaObject(map[string]any{"scenario_id": prop("integer")}, []string{"scenario_id"}), Handler: a.toolScenarioReport},
		{Name: "scenario_history_test", Description: "Historical shock test: replay past news events through the graph known at the time and compare later price moves of exposed vs other market questions, with a before-event placebo and event-clustered intervals. Measures association, not causation. Args: window_hours? (24), since_days? (14), max_events? (40), include_weak?, move_threshold? (0.05), seed?.",
			InputSchema: schemaObject(map[string]any{"window_hours": prop("integer"), "since_days": prop("integer"), "max_events": prop("integer"), "include_weak": prop("boolean"), "move_threshold": prop("number"), "seed": prop("integer")}, nil), Handler: a.toolScenarioHistoryTest},
		{Name: "scenario_history_tests", Description: "Past historical shock tests. Args: limit?.",
			InputSchema: schemaObject(map[string]any{"limit": prop("integer")}, nil), Handler: a.toolScenarioHistoryTests},
		{Name: "relationships_grade", Description: "Grade ungraded links by what their cited headlines say (mechanism, asserted, co_occurrence, unsupported) using the configured LLM. Grades text, not causality. Args: limit? (links, default 60).",
			InputSchema: schemaObject(map[string]any{"limit": prop("integer")}, nil), Handler: a.toolRelationshipsGrade},
		{Name: "scenarios_list", Description: "Saved scenario runs. Args: limit?.",
			InputSchema: schemaObject(map[string]any{"limit": prop("integer")}, nil), Handler: a.toolScenariosList},
	}
}

// ─── arg helpers ───────────────────────────────────────────────────

func intArgAny(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

func floatArgAny(args map[string]any, key string) (float64, bool) {
	switch v := args[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	}
	return 0, false
}

func boolArgAny(args map[string]any, key string) (bool, bool) {
	switch v := args[key].(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(v) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
	}
	return false, false
}

func idsArg(args map[string]any, key string) []int64 {
	out := []int64{}
	switch v := args[key].(type) {
	case []any:
		for _, x := range v {
			if n := intArgAny(map[string]any{"x": x}, "x"); n > 0 {
				out = append(out, n)
			}
		}
	case string:
		for _, p := range strings.Split(v, ",") {
			if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil && n > 0 {
				out = append(out, n)
			}
		}
	}
	return out
}

func stringsArg(args map[string]any, key string) []string {
	return cfgStrings(args, key)
}

func timeArg(args map[string]any, key string) (*time.Time, error) {
	v := strArg(args, key)
	if v == "" {
		return nil, nil
	}
	t, err := parseOptionalTime(v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return t, nil
}

func (a *App) env(ctx *sdk.AppCtx, pid string) *ingestEnv {
	return &ingestEnv{db: ctx.AppDB(), pid: pid, sc: a.client(), llm: appLLM(ctx), now: nowUTC()}
}

func ensureDefaultSources(ctx *sdk.AppCtx, pid string) error {
	for _, s := range append(defaultSources(), IntelSource{Slug: "extraction", Connector: "extraction", Enabled: true, IntervalSec: 300,
		PermittedUse: "LLM extraction of entities, claims and relationships from stored headlines.", Licence: "derived data"}) {
		if err := ensureSource(ctx.AppDB(), pid, s); err != nil {
			return err
		}
	}
	return nil
}

// forecastAfterIngest revises forecasts with the clock read after this
// cycle's ingestion. Evidence is stamped with the time it was stored, so
// a cutoff taken when the cycle started would exclude the very evidence
// that triggered the revision.
func forecastAfterIngest(env *ingestEnv, llmBudget int) (map[string]int, error) {
	env.now = nowUTC()
	return updateForecasts(env, llmBudget)
}

// runCycle is one full pass: due connectors → extraction → forecasts.
func (a *App) runCycle(ctx *sdk.AppCtx, pid string, force bool, only string, skipForecasts bool) (map[string]any, error) {
	if err := ensureDefaultSources(ctx, pid); err != nil {
		return nil, err
	}
	env := a.env(ctx, pid)
	out := map[string]any{"at": ts(env.now)}
	runs, err := runDueSources(env, force, only)
	out["ingest"] = runs
	if err != nil {
		return out, err
	}
	if only == "" || only == "extraction" {
		if enabled, _ := sourceEnabled(env, "extraction"); enabled {
			counts, err := extractClaims(env, configInt(ctx, "extraction_batches_per_cycle", 3, 0, 50))
			out["extraction"] = counts
			if err != nil {
				out["extraction_error"] = err.Error()
				_, _ = env.db.Exec(`UPDATE intel_sources SET last_run_at = ?, last_error = ? WHERE project_id = ? AND slug = 'extraction'`, ts(env.now), err.Error(), pid)
			}
		}
	}
	if n, err := applyRetention(env.db, pid, env.now); err != nil {
		out["retention_error"] = err.Error()
	} else if n > 0 {
		out["retention_expired"] = n
	}
	if !skipForecasts {
		counts, err := forecastAfterIngest(env, configInt(ctx, "forecast_llm_per_cycle", 20, 0, 1000))
		out["forecasts"] = counts
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func sourceEnabled(env *ingestEnv, slug string) (bool, error) {
	var enabled int
	err := env.db.QueryRow(`SELECT enabled FROM intel_sources WHERE project_id = ? AND slug = ?`, env.pid, slug).Scan(&enabled)
	return enabled != 0, err
}

// ─── handlers ──────────────────────────────────────────────────────

func (a *App) toolOverview(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	since := nowUTC().Add(-24 * time.Hour)
	if t, err := timeArg(args, "since"); err != nil {
		return nil, err
	} else if t != nil {
		since = *t
	}
	if err := ensureDefaultSources(ctx, pid); err != nil {
		return nil, err
	}
	return buildOverview(ctx, pid, since)
}

func buildOverview(ctx *sdk.AppCtx, pid string, since time.Time) (map[string]any, error) {
	db := ctx.AppDB()
	out := map[string]any{"since": ts(since), "generated_at": ts(nowUTC())}
	rows, err := db.Query(`SELECT source, COUNT(*) FROM intelligence_evidence WHERE project_id = ? AND ingested_at >= ? GROUP BY source ORDER BY COUNT(*) DESC LIMIT 12`, pid, ts(since))
	if err != nil {
		return nil, err
	}
	bySource := []map[string]any{}
	total := 0
	for rows.Next() {
		var s string
		var n int
		if rows.Scan(&s, &n) == nil {
			bySource = append(bySource, map[string]any{"source": s, "count": n})
			total += n
		}
	}
	rows.Close()
	out["new_evidence"] = map[string]any{"total": total, "by_source": bySource}

	fs, err := recentForecasts(db, pid, since, 500)
	if err != nil {
		return nil, err
	}
	moves := []map[string]any{}
	newForecasts := 0
	for _, f := range fs {
		if f.Revision == 1 {
			newForecasts++
			continue
		}
		if f.Probability == nil {
			continue
		}
		var prev *float64
		if all, err := forecastsFor(db, f.QuestionID); err == nil {
			for _, g := range all {
				if g.Revision == f.Revision-1 {
					prev = g.Probability
				}
			}
		}
		if prev == nil || math.Abs(*f.Probability-*prev) < 0.03 {
			continue
		}
		q, err := getQuestion(db, pid, f.QuestionID)
		if err != nil {
			continue
		}
		moves = append(moves, map[string]any{"question_id": q.ID, "question": q.Text, "from": *prev, "to": *f.Probability, "delta": round3(*f.Probability - *prev),
			"trigger": f.Trigger, "explanation": f.Explanation, "evidence_ids": f.EvidenceIDs, "at": ts(f.CreatedAt)})
	}
	sort.Slice(moves, func(i, j int) bool {
		return math.Abs(moves[i]["delta"].(float64)) > math.Abs(moves[j]["delta"].(float64))
	})
	if len(moves) > 15 {
		moves = moves[:15]
	}
	out["forecast_moves"] = moves
	out["new_forecasts"] = newForecasts

	open, err := listQuestions(db, pid, "open", 5000)
	if err != nil {
		return nil, err
	}
	closing := []map[string]any{}
	newQuestions := 0
	for _, q := range open {
		if q.CreatedAt != nil && q.CreatedAt.After(since) {
			newQuestions++
		}
		if q.Deadline.After(nowUTC()) && q.Deadline.Before(nowUTC().Add(48*time.Hour)) && len(closing) < 15 {
			closing = append(closing, questionRow(db, q))
		}
	}
	out["closing_soon"] = closing
	resolved, _ := listQuestions(db, pid, "resolved", 50)
	recent := []map[string]any{}
	for _, q := range resolved {
		if q.ResolvedAt != nil && q.ResolvedAt.After(since) {
			recent = append(recent, questionRow(db, q))
		}
	}
	out["resolved"] = recent
	out["questions"] = map[string]any{"open": len(open), "new": newQuestions}

	sources, err := listSources(db, pid)
	if err != nil {
		return nil, err
	}
	health := []map[string]any{}
	for _, s := range sources {
		health = append(health, map[string]any{"slug": s.Slug, "enabled": s.Enabled, "freshness_sec": s.FreshnessSec(nowUTC()), "last_error": s.LastError, "items_total": s.ItemsTotal})
	}
	out["sources"] = health
	var entities, rels, claims, evidence int
	_ = db.QueryRow(`SELECT COUNT(*) FROM entities WHERE project_id = ?`, pid).Scan(&entities)
	_ = db.QueryRow(`SELECT COUNT(*) FROM intel_relationships WHERE project_id = ? AND retracted_at IS NULL`, pid).Scan(&rels)
	_ = db.QueryRow(`SELECT COUNT(*) FROM intel_claims WHERE project_id = ?`, pid).Scan(&claims)
	_ = db.QueryRow(`SELECT COUNT(*) FROM intelligence_evidence WHERE project_id = ?`, pid).Scan(&evidence)
	out["world"] = map[string]any{"entities": entities, "relationships": rels, "claims": claims, "evidence": evidence}
	if q, err := dataQuality(db, pid, nowUTC()); err == nil {
		top := q.Issues
		if len(top) > 12 {
			top = top[:12]
		}
		out["quality"] = map[string]any{"summary": q.Summary, "issues": top, "total": len(q.Issues), "basis": q.Basis, "access": q.Access}
	}
	llm := appLLM(ctx)
	out["llm"] = map[string]any{"enabled": llm != nil, "model": configString(ctx, "llm_model", "anthropic/claude-haiku-5-5"), "calls_today": llmCallsToday(), "daily_limit": configInt(ctx, "llm_daily_calls", 300, 0, 100000)}
	return out, nil
}

// questionRow is the list shape: question plus latest forecast and market.
func questionRow(db *sql.DB, q Question) map[string]any {
	row := map[string]any{"id": q.ID, "text": q.Text, "origin": q.Origin, "venue": q.Venue, "url": q.URL, "category": q.Category,
		"deadline": ts(q.Deadline), "status": q.Status, "outcome": q.Outcome, "resolved_at": timeString(q.ResolvedAt), "topics": q.Topics,
		"entity_ids": q.EntityIDs, "last_evidence_at": timeString(q.LastEvidenceAt)}
	if f, err := latestForecast(db, q.ID); err == nil && f != nil {
		row["forecast"] = map[string]any{"id": f.ID, "revision": f.Revision, "probability": f.Probability, "abstained": f.Abstained,
			"adjustment": f.Adjustment, "trigger": f.Trigger, "created_at": ts(f.CreatedAt)}
	}
	if p, err := priceAt(db, q.ID, nowUTC()); err == nil && p != nil {
		row["market_price"] = *p
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM intel_question_evidence WHERE question_id = ?`, q.ID).Scan(&n)
	row["evidence_count"] = n
	return row
}

func (a *App) toolSourcesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if err := ensureDefaultSources(ctx, pid); err != nil {
		return nil, err
	}
	sources, err := listSources(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	rows := []map[string]any{}
	for _, s := range sources {
		rows = append(rows, map[string]any{"slug": s.Slug, "connector": s.Connector, "enabled": s.Enabled, "interval_sec": s.IntervalSec,
			"config": s.Config, "permitted_use": s.PermittedUse, "licence": s.Licence, "retention_days": s.RetentionDays, "access": s.Access,
			"last_run_at": timeString(s.LastRunAt), "last_success_at": timeString(s.LastSuccessAt), "freshness_sec": s.FreshnessSec(now),
			"last_error": s.LastError, "items_total": s.ItemsTotal, "calls_total": s.CallsTotal})
	}
	runs, err := recentIngestRuns(ctx.AppDB(), pid, 30)
	if err != nil {
		return nil, err
	}
	return map[string]any{"sources": rows, "recent_runs": runs, "llm_calls_today": llmCallsToday()}, nil
}

func (a *App) toolSourceConfigure(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if err := ensureDefaultSources(ctx, pid); err != nil {
		return nil, err
	}
	var enabled *bool
	if v, ok := boolArgAny(args, "enabled"); ok {
		enabled = &v
	}
	var interval *int
	if v := intArgAny(args, "interval_sec"); v > 0 {
		n := int(v)
		interval = &n
	}
	var cfg map[string]any
	if raw, ok := args["config"]; ok && raw != nil {
		switch c := raw.(type) {
		case map[string]any:
			cfg = c
		case string:
			if err := json.Unmarshal([]byte(c), &cfg); err != nil {
				return nil, errors.New("config must be a JSON object")
			}
		default:
			return nil, errors.New("config must be an object")
		}
	}
	var retention *int
	if _, ok := args["retention_days"]; ok {
		n := int(intArgAny(args, "retention_days"))
		retention = &n
	}
	if err := updateSourceSettings(ctx.AppDB(), pid, strArg(args, "slug"), enabled, interval, cfg, strArg(args, "access"), retention); err != nil {
		return nil, err
	}
	return a.toolSourcesList(ctx, args)
}

func (a *App) toolIngestNow(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	skip, _ := boolArgAny(args, "skip_forecasts")
	return a.runCycle(ctx, pid, true, strArg(args, "source"), skip)
}

func (a *App) toolEntitiesSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := searchWorldEntities(ctx.AppDB(), pid, strArg(args, "query"), strArg(args, "kind"), int(intArgAny(args, "limit")))
	if err != nil {
		return nil, err
	}
	return map[string]any{"entities": rows, "count": len(rows)}, nil
}

func (a *App) toolEntityGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	id := intArgAny(args, "id")
	e, err := getWorldEntity(db, pid, id)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, fmt.Errorf("entity %d not found", id)
	}
	at, err := timeArg(args, "as_of")
	if err != nil {
		return nil, err
	}
	rels, err := relationshipsAt(db, pid, []int64{id}, at)
	if err != nil {
		return nil, err
	}
	claims, err := listClaims(db, pid, 0, id, "", 50)
	if err != nil {
		return nil, err
	}
	state, err := stateSeries(db, pid, id, "", 60)
	if err != nil {
		return nil, err
	}
	open, err := listQuestions(db, pid, "open", 5000)
	if err != nil {
		return nil, err
	}
	qs := []map[string]any{}
	for _, q := range open {
		if containsID(q.EntityIDs, id) {
			qs = append(qs, questionRow(db, q))
		}
	}
	evidence, err := searchEvidence(db, EvidenceQuery{ProjectID: pid, Entity: e.Name, Limit: 20})
	if err != nil {
		return nil, err
	}
	rd := readerFrom(args)
	redacted := rd.redactRows(evidence)
	locs, err := entityLocations(db, pid)
	if err != nil {
		return nil, err
	}
	mine := locs[id]
	if mine == nil {
		mine = []EntityLocation{}
	}
	quality, err := dataQuality(db, pid, nowUTC())
	if err != nil {
		return nil, err
	}
	issues := []QualityIssue{}
	for _, is := range quality.Issues {
		if containsID(is.EntityIDs, id) {
			issues = append(issues, is)
		}
	}
	series := []SeriesFreshness{}
	for _, s := range quality.Series {
		if s.EntityID == id {
			series = append(series, s)
		}
	}
	return map[string]any{"entity": e, "relationships": rels, "claims": claims, "state": state, "open_questions": qs, "recent_evidence": evidence,
		"redacted": redacted, "locations": mine, "location": bestLocation(mine), "issues": issues, "series": series}, nil
}

func (a *App) toolEntityUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	ext := map[string]string{}
	if m, ok := args["external_ids"].(map[string]any); ok {
		for k, v := range m {
			ext[k] = fmt.Sprint(v)
		}
	}
	e, created, err := upsertWorldEntity(ctx.AppDB(), pid, WorldEntity{Name: strArg(args, "name"), Kind: strArg(args, "kind"), Description: strArg(args, "description"),
		Aliases: stringsArg(args, "aliases"), ExternalIDs: ext})
	if err != nil {
		return nil, err
	}
	return map[string]any{"entity": e, "created": created}, nil
}

// entityRef accepts an id or a name.
func entityRef(ctx *sdk.AppCtx, pid string, args map[string]any, key string) (int64, error) {
	if id := intArgAny(args, key); id > 0 {
		return id, nil
	}
	name := strArg(args, key)
	if name == "" {
		return 0, fmt.Errorf("%s is required", key)
	}
	e, err := findWorldEntity(ctx.AppDB(), pid, name, nil)
	if err != nil {
		return 0, err
	}
	if e == nil {
		return 0, fmt.Errorf("%s: entity %q not found (create it with entity_upsert)", key, name)
	}
	return e.ID, nil
}

func (a *App) toolRelationshipAssert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	from, err := entityRef(ctx, pid, args, "from")
	if err != nil {
		return nil, err
	}
	to, err := entityRef(ctx, pid, args, "to")
	if err != nil {
		return nil, err
	}
	r := Relationship{From: from, To: to, Type: strArg(args, "type"), Status: strArg(args, "status"), EvidenceIDs: idsArg(args, "evidence_ids"), Note: strArg(args, "note"),
		CausalBasis: strArg(args, "causal_basis"), CausalNote: strArg(args, "causal_note")}
	if r.CausalBasis != "" {
		r.GradedBy = "operator"
	}
	if c, ok := floatArgAny(args, "confidence"); ok {
		if c < 0 || c > 1 {
			return nil, errors.New("confidence must be between 0 and 1")
		}
		r.Confidence = &c
	}
	if r.ValidFrom, err = timeArg(args, "valid_from"); err != nil {
		return nil, err
	}
	if r.ValidTo, err = timeArg(args, "valid_to"); err != nil {
		return nil, err
	}
	if len(r.EvidenceIDs) > 0 {
		rows, err := searchEvidence(ctx.AppDB(), EvidenceQuery{ProjectID: pid, IDs: r.EvidenceIDs, Limit: len(r.EvidenceIDs)})
		if err != nil {
			return nil, err
		}
		if len(rows) != len(r.EvidenceIDs) {
			return nil, errors.New("one or more evidence_ids do not exist in this project")
		}
	}
	out, err := assertRelationship(ctx.AppDB(), pid, r)
	if err != nil {
		return nil, err
	}
	return map[string]any{"relationship": out}, nil
}

func (a *App) toolRelationshipRetract(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if err := retractRelationship(ctx.AppDB(), pid, intArgAny(args, "id")); err != nil {
		return nil, err
	}
	return map[string]any{"retracted": intArgAny(args, "id")}, nil
}

func (a *App) toolGraphNeighbors(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	at, err := timeArg(args, "as_of")
	if err != nil {
		return nil, err
	}
	depth := int(intArgAny(args, "depth"))
	if depth <= 0 {
		depth = 1
	}
	if depth > 3 {
		depth = 3
	}
	return graphAround(ctx.AppDB(), pid, intArgAny(args, "entity_id"), depth, at)
}

func graphAround(db *sql.DB, pid string, root int64, depth int, at *time.Time) (map[string]any, error) {
	e, err := getWorldEntity(db, pid, root)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, fmt.Errorf("entity %d not found", root)
	}
	nodes := map[int64]bool{root: true}
	edges := map[int64]Relationship{}
	frontier := []int64{root}
	for d := 0; d < depth && len(frontier) > 0; d++ {
		rels, err := relationshipsAt(db, pid, frontier, at)
		if err != nil {
			return nil, err
		}
		next := []int64{}
		for _, r := range rels {
			edges[r.ID] = r
			for _, id := range []int64{r.From, r.To} {
				if !nodes[id] && len(nodes) < 200 {
					nodes[id] = true
					next = append(next, id)
				}
			}
		}
		frontier = next
	}
	nodeList := []WorldEntity{}
	for id := range nodes {
		if n, err := getWorldEntity(db, pid, id); err == nil && n != nil {
			nodeList = append(nodeList, *n)
		}
	}
	sort.Slice(nodeList, func(i, j int) bool { return nodeList[i].ID < nodeList[j].ID })
	edgeList := []Relationship{}
	for _, r := range edges {
		if nodes[r.From] && nodes[r.To] {
			edgeList = append(edgeList, r)
		}
	}
	sort.Slice(edgeList, func(i, j int) bool { return edgeList[i].ID < edgeList[j].ID })
	return map[string]any{"root": e, "nodes": nodeList, "edges": edgeList, "as_of": timeString(at)}, nil
}

func (a *App) toolClaimsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := listClaims(ctx.AppDB(), pid, intArgAny(args, "evidence_id"), intArgAny(args, "entity_id"), strArg(args, "status"), int(intArgAny(args, "limit")))
	if err != nil {
		return nil, err
	}
	return map[string]any{"claims": rows, "count": len(rows)}, nil
}

func (a *App) toolClaimReview(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if err := setClaimStatus(ctx.AppDB(), pid, intArgAny(args, "id"), strArg(args, "status")); err != nil {
		return nil, err
	}
	return map[string]any{"id": intArgAny(args, "id"), "status": strArg(args, "status")}, nil
}

func (a *App) toolQuestionCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	deadline, err := timeArg(args, "deadline")
	if err != nil {
		return nil, err
	}
	if deadline == nil || !deadline.After(nowUTC()) {
		return nil, errors.New("deadline must be in the future")
	}
	if strArg(args, "resolution_source") == "" {
		return nil, errors.New("resolution_source is required: name where the outcome will be observed")
	}
	q := Question{Origin: "manual", Text: strArg(args, "text"), ResolutionRule: strArg(args, "resolution_rule"), ResolutionSource: strArg(args, "resolution_source"),
		Category: strArg(args, "category"), Deadline: *deadline, EntityIDs: idsArg(args, "entity_ids")}
	if topics := stringsArg(args, "topics"); len(topics) > 0 {
		q.Topics = topics
	}
	db := ctx.AppDB()
	for _, id := range q.EntityIDs {
		if e, err := getWorldEntity(db, pid, id); err != nil || e == nil {
			return nil, fmt.Errorf("entity %d not found", id)
		}
	}
	nq, _, err := insertQuestion(db, pid, q)
	if err != nil {
		return nil, err
	}
	return map[string]any{"question": nq}, nil
}

func (a *App) toolQuestionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	status := fallback(strArg(args, "status"), "open")
	qs, err := listQuestions(ctx.AppDB(), pid, status, int(intArgAny(args, "limit")))
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	for _, q := range qs {
		rows = append(rows, questionRow(ctx.AppDB(), q))
	}
	return map[string]any{"questions": rows, "count": len(rows), "status": status}, nil
}

func (a *App) toolQuestionGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	q, err := getQuestion(db, pid, intArgAny(args, "id"))
	if err != nil {
		return nil, err
	}
	fs, err := forecastsFor(db, q.ID)
	if err != nil {
		return nil, err
	}
	prices, err := priceSeries(db, q.ID)
	if err != nil {
		return nil, err
	}
	ids, err := questionEvidenceIDs(db, pid, q.ID, nowUTC(), 60)
	if err != nil {
		return nil, err
	}
	evidence, err := searchEvidence(db, EvidenceQuery{ProjectID: pid, IDs: ids, Limit: 60})
	if err != nil {
		return nil, err
	}
	readerFrom(args).redactRows(evidence)
	ents := []WorldEntity{}
	for _, id := range q.EntityIDs {
		if e, err := getWorldEntity(db, pid, id); err == nil && e != nil {
			ents = append(ents, *e)
		}
	}
	return map[string]any{"question": q, "forecasts": fs, "prices": prices, "evidence": evidence, "entities": ents}, nil
}

func (a *App) toolForecastNow(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	q, err := getQuestion(ctx.AppDB(), pid, intArgAny(args, "question_id"))
	if err != nil {
		return nil, err
	}
	if q.Status != "open" || !q.Deadline.After(nowUTC()) {
		return nil, errors.New("forecasts can only be recorded for open questions before their deadline")
	}
	f, err := forecastQuestion(a.env(ctx, pid), q, "manual")
	if err != nil {
		return nil, err
	}
	return map[string]any{"forecast": f}, nil
}

func (a *App) toolQuestionResolve(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	q, err := getQuestion(db, pid, intArgAny(args, "id"))
	if err != nil {
		return nil, err
	}
	note := strArg(args, "note")
	if note == "" {
		return nil, errors.New("note is required: state the observed outcome and its source")
	}
	if v, _ := boolArgAny(args, "void"); v {
		if err := resolveQuestionRow(db, pid, q.ID, "void", nil, note, nowUTC()); err != nil {
			return nil, err
		}
		return getQuestion(db, pid, q.ID)
	}
	if q.Origin == "market" {
		return nil, errors.New("market questions resolve from their venue; only void is allowed manually")
	}
	var outcome int
	switch strings.ToLower(strArg(args, "outcome")) {
	case "yes", "1", "true":
		outcome = 1
	case "no", "0", "false":
		outcome = 0
	default:
		return nil, errors.New("outcome must be yes or no (or pass void: true)")
	}
	if err := resolveQuestionRow(db, pid, q.ID, "resolved", &outcome, note, nowUTC()); err != nil {
		return nil, err
	}
	return getQuestion(db, pid, q.ID)
}

func (a *App) toolLedger(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	lock := 24.0
	if v, ok := floatArgAny(args, "lock_hours"); ok && v >= 0 && v <= 720 {
		lock = v
	}
	return buildLedger(ctx.AppDB(), pid, lock)
}

func (a *App) toolScenarioRun(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	in := scenarioInput{Name: strArg(args, "name"), MaxDepth: int(intArgAny(args, "max_depth"))}
	raw, _ := json.Marshal(args["shocks"])
	if s, ok := args["shocks"].(string); ok {
		raw = []byte(s)
	}
	if err := json.Unmarshal(raw, &in.Shocks); err != nil {
		return nil, errors.New("shocks must be an array of {entity_id|entity, description, severity}")
	}
	if args["rules"] != nil {
		rraw, _ := json.Marshal(args["rules"])
		if s, ok := args["rules"].(string); ok {
			rraw = []byte(s)
		}
		if err := json.Unmarshal(rraw, &in.Rules); err != nil {
			return nil, errors.New("rules must map relationship type to {factor, direction}")
		}
	}
	if v, ok := floatArgAny(args, "min_impact"); ok {
		in.MinImpact = v
	}
	in.SkipAssumed, _ = boolArgAny(args, "skip_assumed")
	in.IncludeWeak, _ = boolArgAny(args, "include_weak")
	in.IgnoreConfidence, _ = boolArgAny(args, "ignore_confidence")
	if t, err := timeArg(args, "as_of"); err != nil {
		return nil, err
	} else if t != nil {
		in.AsOf = *t
	}
	if t, err := timeArg(args, "known_at"); err != nil {
		return nil, err
	} else if t != nil {
		in.KnownAt = *t
	}
	return runScenario(ctx.AppDB(), pid, in)
}

func (a *App) toolScenarioReplay(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	return replayScenario(ctx.AppDB(), pid, intArgAny(args, "scenario_id"))
}

func (a *App) toolScenarioSensitivity(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	return runSensitivity(ctx.AppDB(), pid, intArgAny(args, "scenario_id"), int(intArgAny(args, "draws")), intArgAny(args, "seed"))
}

func (a *App) toolScenarioReport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := intArgAny(args, "scenario_id")
	spec, hash, result, err := loadScenarioSpec(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	rep, err := loadScenarioReport(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"scenario_id": id, "spec": spec, "snapshot_hash": hash, "result": result, "report": rep}, nil
}

func (a *App) toolScenarioHistoryTest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	hp := historyParams{WindowHours: int(intArgAny(args, "window_hours")), SinceDays: int(intArgAny(args, "since_days")), MaxEvents: int(intArgAny(args, "max_events")), Seed: intArgAny(args, "seed")}
	hp.IncludeWeak, _ = boolArgAny(args, "include_weak")
	if v, ok := floatArgAny(args, "move_threshold"); ok {
		hp.MoveThreshold = v
	}
	return runHistoryTest(ctx.AppDB(), pid, hp, nowUTC())
}

func (a *App) toolScenarioHistoryTests(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := listHistoryTests(ctx.AppDB(), pid, int(intArgAny(args, "limit")))
	if err != nil {
		return nil, err
	}
	return map[string]any{"tests": rows}, nil
}

func (a *App) toolRelationshipsGrade(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := gradeRelationships(ctx.AppDB(), pid, appLLM(ctx), int(intArgAny(args, "limit")))
	if err != nil {
		return nil, err
	}
	if prof, err := causalProfile(ctx.AppDB(), pid); err == nil {
		out["links_by_causal_basis"] = prof
	}
	return out, nil
}

func (a *App) toolScenariosList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := listScenarios(ctx.AppDB(), pid, int(intArgAny(args, "limit")))
	if err != nil {
		return nil, err
	}
	return map[string]any{"scenarios": rows}, nil
}

// ─── forecast replay ───────────────────────────────────────────────

// replayForecast re-assembles a forecast's inputs from storage and checks
// them against the recorded snapshot hash.
func replayForecast(db *sql.DB, pid string, forecastID int64) (map[string]any, error) {
	f, err := scanForecast(db.QueryRow(`SELECT `+forecastCols+` FROM intel_forecasts WHERE project_id = ? AND id = ?`, pid, forecastID))
	if err != nil {
		return nil, fmt.Errorf("forecast %d not found", forecastID)
	}
	briefs, err := loadEvidenceBriefs(db, pid, f.EvidenceIDs)
	if err != nil {
		return nil, err
	}
	recomputedIDs, err := questionEvidenceIDs(db, pid, f.QuestionID, f.EvidenceCutoff, 12)
	if err != nil {
		return nil, err
	}
	hash := snapshotHash(f.QuestionID, f.EvidenceCutoff, f.Prior, briefs)
	market, _ := priceAt(db, f.QuestionID, f.EvidenceCutoff)
	sameSet := len(recomputedIDs) == len(f.EvidenceIDs)
	for i := range recomputedIDs {
		if sameSet && recomputedIDs[i] != f.EvidenceIDs[i] {
			sameSet = false
		}
	}
	marketMatches := (market == nil && f.MarketPrice == nil) || (market != nil && f.MarketPrice != nil && *market == *f.MarketPrice)
	evidence, err := searchEvidence(db, EvidenceQuery{ProjectID: pid, IDs: f.EvidenceIDs, Limit: len(f.EvidenceIDs) + 1})
	if err != nil {
		return nil, err
	}
	return map[string]any{"forecast": f, "evidence": evidence, "recomputed_snapshot_hash": hash,
		"snapshot_verified": hash == f.SnapshotHash, "evidence_set_reproduced": sameSet, "market_price_reproduced": marketMatches,
		"lookahead_safe": true, "knowledge": "ingested"}, nil
}

// ─── panel routes ──────────────────────────────────────────────────

type toolFn func(*sdk.AppCtx, map[string]any) (any, error)

// toolRoute exposes a tool to the panel: query params (and a JSON body for
// POST) become tool args.
func toolRoute(method string, fn toolFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			httpErr(w, 405, method+" only")
			return
		}
		args := map[string]any{"_project_id": r.URL.Query().Get("project_id")}
		if r.Method == http.MethodPost && r.Body != nil {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
				httpErr(w, 400, "invalid JSON body")
				return
			}
			for k, v := range body {
				args[k] = v
			}
		}
		for k := range r.URL.Query() {
			if k != "project_id" && k != "install_id" {
				args[k] = r.URL.Query().Get(k)
			}
		}
		// Set last so a body or query value cannot replace it. The panel is
		// the project's operator surface.
		args[readerArg] = operatorReader
		out, err := fn(globalCtx, args)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, 200, out)
	}
}

func (a *App) worldRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/overview", Handler: toolRoute(http.MethodGet, a.toolOverview)},
		{Pattern: "/connectors", Handler: toolRoute(http.MethodGet, a.toolSourcesList)},
		{Pattern: "/connector-configure", Handler: toolRoute(http.MethodPost, a.toolSourceConfigure)},
		{Pattern: "/ingest", Handler: toolRoute(http.MethodPost, a.toolIngestNow)},
		{Pattern: "/questions", Handler: toolRoute(http.MethodGet, a.toolQuestionsList)},
		{Pattern: "/question", Handler: toolRoute(http.MethodGet, a.toolQuestionGet)},
		{Pattern: "/forecast-now", Handler: toolRoute(http.MethodPost, a.toolForecastNow)},
		{Pattern: "/ledger", Handler: toolRoute(http.MethodGet, a.toolLedger)},
		{Pattern: "/entities", Handler: toolRoute(http.MethodGet, a.toolEntitiesSearch)},
		{Pattern: "/entity", Handler: toolRoute(http.MethodGet, a.toolEntityGet)},
		{Pattern: "/graph", Handler: toolRoute(http.MethodGet, a.toolGraphNeighbors)},
		{Pattern: "/scenarios", Handler: toolRoute(http.MethodGet, a.toolScenariosList)},
		{Pattern: "/scenario-run", Handler: toolRoute(http.MethodPost, a.toolScenarioRun)},
		{Pattern: "/scenario-replay", Handler: toolRoute(http.MethodPost, a.toolScenarioReplay)},
		{Pattern: "/scenario-sensitivity", Handler: toolRoute(http.MethodPost, a.toolScenarioSensitivity)},
		{Pattern: "/scenario-report", Handler: toolRoute(http.MethodGet, a.toolScenarioReport)},
		{Pattern: "/scenario-history-test", Handler: toolRoute(http.MethodPost, a.toolScenarioHistoryTest)},
		{Pattern: "/scenario-history-tests", Handler: toolRoute(http.MethodGet, a.toolScenarioHistoryTests)},
		{Pattern: "/relationships-grade", Handler: toolRoute(http.MethodPost, a.toolRelationshipsGrade)},
		{Pattern: "/access-classes", Handler: handleAccessClasses},
		{Pattern: "/world-map", Handler: toolRoute(http.MethodGet, a.toolWorldMap)},
		{Pattern: "/quality", Handler: toolRoute(http.MethodGet, a.toolDataQuality)},
		{Pattern: "/entity-locate", Handler: toolRoute(http.MethodPost, a.toolEntityLocate)},
		{Pattern: "/synthetic-seed", Handler: toolRoute(http.MethodPost, a.toolSyntheticSeed)},
		{Pattern: "/synthetic-clear", Handler: toolRoute(http.MethodPost, a.toolSyntheticClear)},
	}
}

// cycleWorker is the scheduled ingestion → extraction → forecast loop.
func (a *App) cycleWorker(c context.Context, app *sdk.AppCtx) error {
	pid := scanProjectID()
	if pid == "" {
		app.Logger().Info("intelligence cycle skipped for global install without project context")
		return nil
	}
	out, err := a.runCycle(app, pid, false, "", false)
	if err != nil {
		app.Logger().Error("intelligence cycle failed", "error", err.Error())
		return err
	}
	app.Logger().Info("intelligence cycle", "result", mustJSON(out))
	return nil
}
