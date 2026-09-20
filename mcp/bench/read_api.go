package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	defaultReadLimit = 100
	maxReadLimit     = 500
)

type pageInfo struct {
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	Total   int  `json:"total"`
	HasMore bool `json:"has_more"`
}

func normalizePage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultReadLimit
	}
	if limit > maxReadLimit {
		limit = maxReadLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func newPage(limit, offset, total int) pageInfo {
	return pageInfo{Limit: limit, Offset: offset, Total: total, HasMore: offset+limit < total}
}

type runQuery struct {
	PackID         string `json:"pack_id"`
	PackDigest     string `json:"pack_digest"`
	ProfileDigest  string `json:"profile_digest"`
	Status         string `json:"status"`
	Query          string `json:"query"`
	Limit          int    `json:"limit"`
	Offset         int    `json:"offset"`
	IncludeResults bool   `json:"include_results"`
}

type runPage struct {
	Runs []Run    `json:"runs"`
	Page pageInfo `json:"page"`
}

func (s store) searchRuns(input runQuery) (*runPage, error) {
	limit, offset := normalizePage(input.Limit, input.Offset)
	where, args := []string{"1=1"}, []any{}
	add := func(clause string, value any) {
		where, args = append(where, clause), append(args, value)
	}
	if input.PackID != "" {
		add("pack_id=?", input.PackID)
	}
	if input.PackDigest != "" {
		add("pack_digest=?", input.PackDigest)
	}
	if input.ProfileDigest != "" {
		add("scoring_profile_digest=?", input.ProfileDigest)
	}
	if input.Status != "" {
		add("status=?", input.Status)
	}
	if q := strings.TrimSpace(input.Query); q != "" {
		like := "%" + q + "%"
		where = append(where, "(name LIKE ? OR pack_name LIKE ? OR error LIKE ?)")
		args = append(args, like, like, like)
	}
	whereSQL := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM bench_runs WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, err
	}
	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.Query(`SELECT `+runColumns+` FROM bench_runs WHERE `+whereSQL+
		` ORDER BY created_at DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		if run == nil {
			continue
		}
		runs = append(runs, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Finish the page query before loading children. App DBs may deliberately
	// use one SQLite connection, in which case a nested query while rows remain
	// open would deadlock waiting for that same connection.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if input.IncludeResults {
		for i := range runs {
			runs[i].Results, err = s.listResults(runs[i].ID)
			if err != nil {
				return nil, err
			}
		}
	}
	return &runPage{Runs: runs, Page: newPage(limit, offset, total)}, nil
}

type resultQuery struct {
	RunID      string `json:"run_id"`
	PackID     string `json:"pack_id"`
	PackDigest string `json:"pack_digest"`
	ScenarioID string `json:"scenario_id"`
	Admission  string `json:"admission"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Passed     *bool  `json:"passed"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

type resultPage struct {
	Results []Result `json:"results"`
	Page    pageInfo `json:"page"`
}

func scanResult(row interface{ Scan(...any) error }) (*Result, error) {
	var result Result
	var target, score, metrics, created string
	var passed int
	err := row.Scan(&result.ID, &result.BenchRunID, &result.ScenarioID, &result.ScenarioName,
		&result.TargetIndex, &target, &result.Trial, &result.EvalRunID, &result.Admission,
		&result.InvalidReason, &passed, &score, &metrics, &result.Error, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decodeJSON(target, &result.Target)
	decodeJSON(score, &result.Score)
	decodeJSON(metrics, &result.Metrics)
	result.Passed = passed == 1
	result.CreatedAt = parseTime(created)
	return &result, nil
}

func (s store) getResult(id string) (*Result, error) {
	return scanResult(s.db.QueryRow(`SELECT `+resultColumns+` FROM bench_results WHERE id=?`, id))
}

func (s store) searchResults(input resultQuery) (*resultPage, error) {
	limit, offset := normalizePage(input.Limit, input.Offset)
	where, args := []string{"1=1"}, []any{}
	add := func(clause string, value any) {
		where, args = append(where, clause), append(args, value)
	}
	if input.RunID != "" {
		add("r.bench_run_id=?", input.RunID)
	}
	if input.PackID != "" {
		add("b.pack_id=?", input.PackID)
	}
	if input.PackDigest != "" {
		add("b.pack_digest=?", input.PackDigest)
	}
	if input.ScenarioID != "" {
		add("r.scenario_id=?", input.ScenarioID)
	}
	if input.Admission != "" {
		add("r.admission=?", input.Admission)
	}
	if input.Provider != "" {
		add("json_extract(r.target_json, '$.provider')=?", input.Provider)
	}
	if input.Model != "" {
		add("json_extract(r.target_json, '$.model')=?", input.Model)
	}
	if input.Passed != nil {
		add("r.passed=?", boolInt(*input.Passed))
	}
	whereSQL := strings.Join(where, " AND ")
	fromSQL := ` FROM bench_results r JOIN bench_runs b ON b.id=r.bench_run_id WHERE ` + whereSQL
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*)`+fromSQL, args...).Scan(&total); err != nil {
		return nil, err
	}
	queryArgs := append(append([]any{}, args...), limit, offset)
	qualifiedColumns := "r." + strings.ReplaceAll(resultColumns, ",", ",r.")
	rows, err := s.db.Query(`SELECT `+qualifiedColumns+fromSQL+
		` ORDER BY r.created_at DESC, r.id LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, err
	}
	results, err := scanResults(rows)
	if err != nil {
		return nil, err
	}
	return &resultPage{Results: results, Page: newPage(limit, offset, total)}, nil
}

func (s store) allResults() ([]Result, error) {
	rows, err := s.db.Query(`SELECT ` + resultColumns + ` FROM bench_results ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	return scanResults(rows)
}

type baselineQuery struct {
	PackDigest string `json:"pack_digest"`
	ScenarioID string `json:"scenario_id"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

type baselinePage struct {
	Baselines []Baseline `json:"baselines"`
	Page      pageInfo   `json:"page"`
}

func scanBaseline(row interface{ Scan(...any) error }) (*Baseline, error) {
	var baseline Baseline
	var target, metrics, created string
	err := row.Scan(&baseline.ID, &baseline.PackDigest, &baseline.ScenarioID, &baseline.Label,
		&target, &baseline.Score, &baseline.PassRate, &metrics, &baseline.SourceRunID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decodeJSON(target, &baseline.Target)
	decodeJSON(metrics, &baseline.Metrics)
	baseline.CreatedAt = parseTime(created)
	return &baseline, nil
}

func (s store) getBaseline(id string) (*Baseline, error) {
	return scanBaseline(s.db.QueryRow(`SELECT id,pack_digest,scenario_id,label,target_json,score,pass_rate,metrics_json,source_run_id,created_at
		FROM bench_baselines WHERE id=?`, id))
}

func (s store) searchBaselines(input baselineQuery) (*baselinePage, error) {
	limit, offset := normalizePage(input.Limit, input.Offset)
	where, args := []string{"1=1"}, []any{}
	if input.PackDigest != "" {
		where, args = append(where, "pack_digest=?"), append(args, input.PackDigest)
	}
	if input.ScenarioID != "" {
		where, args = append(where, "scenario_id=?"), append(args, input.ScenarioID)
	}
	whereSQL := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM bench_baselines WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, err
	}
	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.Query(`SELECT id,pack_digest,scenario_id,label,target_json,score,pass_rate,metrics_json,source_run_id,created_at
		FROM bench_baselines WHERE `+whereSQL+` ORDER BY pack_digest,scenario_id LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	baselines := []Baseline{}
	for rows.Next() {
		baseline, err := scanBaseline(rows)
		if err != nil {
			return nil, err
		}
		if baseline != nil {
			baselines = append(baselines, *baseline)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &baselinePage{Baselines: baselines, Page: newPage(limit, offset, total)}, nil
}

func (s store) allBaselines() ([]Baseline, error) {
	rows, err := s.db.Query(`SELECT id,pack_digest,scenario_id,label,target_json,score,pass_rate,metrics_json,source_run_id,created_at
		FROM bench_baselines ORDER BY pack_digest,scenario_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Baseline{}
	for rows.Next() {
		baseline, err := scanBaseline(rows)
		if err != nil {
			return nil, err
		}
		if baseline != nil {
			out = append(out, *baseline)
		}
	}
	return out, rows.Err()
}

type packSuiteRecord struct {
	PackDigest string            `json:"pack_digest"`
	SuiteID    string            `json:"suite_id"`
	CaseMap    map[string]string `json:"case_map"`
	CreatedAt  time.Time         `json:"created_at"`
}

func (s store) listPackSuiteRecords(packDigest string) ([]packSuiteRecord, error) {
	query := `SELECT pack_digest,suite_id,case_map_json,created_at FROM bench_pack_suites`
	args := []any{}
	if packDigest != "" {
		query += ` WHERE pack_digest=?`
		args = append(args, packDigest)
	}
	query += ` ORDER BY created_at,pack_digest`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []packSuiteRecord{}
	for rows.Next() {
		var item packSuiteRecord
		var caseMap, created string
		if err := rows.Scan(&item.PackDigest, &item.SuiteID, &caseMap, &created); err != nil {
			return nil, err
		}
		item.CaseMap = map[string]string{}
		decodeJSON(caseMap, &item.CaseMap)
		item.CreatedAt = parseTime(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *service) scoringData(profileID, profileDigest string) (map[string]any, error) {
	var profile *Profile
	var err error
	switch {
	case profileID != "":
		profile, err = s.db.getProfile(profileID)
	case profileDigest != "":
		profile, err = s.db.getProfileByDigest(profileDigest)
	default:
		profileDigest, err = s.defaultProfileDigest()
		if err == nil {
			profile, err = s.db.getProfileByDigest(profileDigest)
		}
	}
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, errors.New("scoring profile not found")
	}
	return map[string]any{
		"profile":           profile,
		"supported_metrics": []string{"duration_ms", "turns_used", "tokens_total", "tokens_in", "tokens_out", "cost_usd", "llm_calls", "tool_calls", "errors"},
		"component_kinds":   []string{KindGate, KindBudget, KindThreshold},
		"curves":            []string{CurveCliff, CurveLinear, CurveRatio, CurveLog},
		"admission_states":  []string{AdmissionVerified, AdmissionDiagnostic, AdmissionInvalid},
		"run_states":        []string{RunStatusQueued, RunStatusRunning, RunStatusCompleted, RunStatusFailed, RunStatusCancelled},
	}, nil
}

func (s *service) dataExport() (map[string]any, error) {
	packs, err := s.db.listPacks()
	if err != nil {
		return nil, err
	}
	profiles, err := s.db.listProfiles()
	if err != nil {
		return nil, err
	}
	runs, err := s.db.listRuns(1 << 30)
	if err != nil {
		return nil, err
	}
	results, err := s.db.allResults()
	if err != nil {
		return nil, err
	}
	baselines, err := s.db.allBaselines()
	if err != nil {
		return nil, err
	}
	suites, err := s.db.listPackSuiteRecords("")
	if err != nil {
		return nil, err
	}
	scoring, err := s.scoringData("", "")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"exported_at": time.Now().UTC(),
		"packs":       packs,
		"profiles":    profiles,
		"runs":        runs,
		"results":     results,
		"baselines":   baselines,
		"pack_suites": suites,
		"scoring":     scoring,
		"counts": map[string]int{
			"packs": len(packs), "profiles": len(profiles), "runs": len(runs),
			"results": len(results), "baselines": len(baselines), "pack_suites": len(suites),
		},
	}, nil
}
