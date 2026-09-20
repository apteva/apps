package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type store struct{ db *sql.DB }

const timeLayout = time.RFC3339Nano

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(value string) time.Time {
	parsed, err := time.Parse(timeLayout, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func nullableTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return formatTime(*t)
}

func scanTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed := parseTime(value.String)
	if parsed.IsZero() {
		return nil
	}
	return &parsed
}

func encodeJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(raw)
}

func decodeJSON(raw string, out any) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	_ = json.Unmarshal([]byte(raw), out)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// ---- packs ----

const packColumns = `id,name,description,category,state,version,digest,scoring_version,source_pack_id,scenarios_json,revision,created_at,updated_at,profile_digest`

func scanPack(row interface{ Scan(...any) error }) (*Pack, error) {
	var pack Pack
	var scenarios, created, updated string
	err := row.Scan(&pack.ID, &pack.Name, &pack.Description, &pack.Category, &pack.State, &pack.Version, &pack.Digest,
		&pack.ScoringVersion, &pack.SourcePackID, &scenarios, &pack.Revision, &created, &updated, &pack.ProfileDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decodeJSON(scenarios, &pack.Scenarios)
	if pack.Scenarios == nil {
		pack.Scenarios = []Scenario{}
	}
	pack.CreatedAt, pack.UpdatedAt = parseTime(created), parseTime(updated)
	return &pack, nil
}

func (s store) listPacks() ([]Pack, error) {
	rows, err := s.db.Query(`SELECT ` + packColumns + ` FROM bench_packs ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	packs := []Pack{}
	for rows.Next() {
		pack, err := scanPack(rows)
		if err != nil {
			return nil, err
		}
		if pack != nil {
			packs = append(packs, *pack)
		}
	}
	return packs, rows.Err()
}

func (s store) getPack(id string) (*Pack, error) {
	return scanPack(s.db.QueryRow(`SELECT `+packColumns+` FROM bench_packs WHERE id=?`, id))
}

func (s store) getPackByDigest(digest string) (*Pack, error) {
	if digest == "" {
		return nil, nil
	}
	return scanPack(s.db.QueryRow(`SELECT `+packColumns+` FROM bench_packs WHERE digest=?`, digest))
}

func (s store) savePack(pack *Pack) error {
	_, err := s.db.Exec(`INSERT INTO bench_packs(`+packColumns+`)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, description=excluded.description, category=excluded.category, state=excluded.state,
			version=excluded.version, digest=excluded.digest, scoring_version=excluded.scoring_version,
			source_pack_id=excluded.source_pack_id, scenarios_json=excluded.scenarios_json,
			profile_digest=excluded.profile_digest,
			revision=bench_packs.revision+1, updated_at=excluded.updated_at`,
		pack.ID, pack.Name, pack.Description, pack.Category, pack.State, pack.Version, pack.Digest,
		pack.ScoringVersion, pack.SourcePackID, encodeJSON(pack.Scenarios), pack.Revision,
		formatTime(pack.CreatedAt), formatTime(pack.UpdatedAt), pack.ProfileDigest)
	return err
}

func (s store) deletePack(id string) error {
	_, err := s.db.Exec(`DELETE FROM bench_packs WHERE id=?`, id)
	return err
}

func (s store) countRunsForPack(id string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM bench_runs WHERE pack_id=?`, id).Scan(&count)
	return count, err
}

// sealedVersions counts sealed descendants of one draft lineage, so seal can
// pick the next version number without the caller tracking it.
func (s store) sealedVersions(sourcePackID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM bench_packs WHERE state=? AND source_pack_id=?`,
		PackStateSealed, sourcePackID).Scan(&count)
	return count, err
}

// ---- materialized suites ----

type packSuite struct {
	SuiteID string
	CaseMap map[string]string // eval case id -> scenario id
}

func (s store) getPackSuite(digest string) (*packSuite, error) {
	var suiteID, caseMap string
	err := s.db.QueryRow(`SELECT suite_id,case_map_json FROM bench_pack_suites WHERE pack_digest=?`, digest).
		Scan(&suiteID, &caseMap)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := packSuite{SuiteID: suiteID, CaseMap: map[string]string{}}
	decodeJSON(caseMap, &out.CaseMap)
	return &out, nil
}

func (s store) savePackSuite(digest string, suite packSuite) error {
	_, err := s.db.Exec(`INSERT INTO bench_pack_suites(pack_digest,suite_id,case_map_json,created_at)
		VALUES(?,?,?,?)
		ON CONFLICT(pack_digest) DO UPDATE SET suite_id=excluded.suite_id, case_map_json=excluded.case_map_json`,
		digest, suite.SuiteID, encodeJSON(suite.CaseMap), formatTime(time.Now()))
	return err
}

// ---- scoring profiles ----

const profileColumns = `id,name,description,state,version,digest,source_id,builtin,on_failure,components_json,revision,created_at,updated_at`

func scanProfile(row interface{ Scan(...any) error }) (*Profile, error) {
	var p Profile
	var components, created, updated string
	var builtin int
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.State, &p.Version, &p.Digest,
		&p.SourceID, &builtin, &p.OnFailure, &components, &p.Revision, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decodeJSON(components, &p.Components)
	if p.Components == nil {
		p.Components = []ProfileComponent{}
	}
	p.Builtin = builtin == 1
	p.CreatedAt, p.UpdatedAt = parseTime(created), parseTime(updated)
	return &p, nil
}

func (s store) listProfiles() ([]Profile, error) {
	rows, err := s.db.Query(`SELECT ` + profileColumns + ` FROM bench_profiles ORDER BY builtin DESC, updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Profile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		if p != nil {
			out = append(out, *p)
		}
	}
	return out, rows.Err()
}

func (s store) getProfile(id string) (*Profile, error) {
	return scanProfile(s.db.QueryRow(`SELECT `+profileColumns+` FROM bench_profiles WHERE id=?`, id))
}

func (s store) getProfileByDigest(digest string) (*Profile, error) {
	if digest == "" {
		return nil, nil
	}
	return scanProfile(s.db.QueryRow(`SELECT `+profileColumns+` FROM bench_profiles WHERE digest=?`, digest))
}

func (s store) saveProfile(p *Profile) error {
	_, err := s.db.Exec(`INSERT INTO bench_profiles(`+profileColumns+`)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, description=excluded.description, state=excluded.state,
			version=excluded.version, digest=excluded.digest, source_id=excluded.source_id,
			builtin=excluded.builtin, on_failure=excluded.on_failure,
			components_json=excluded.components_json,
			revision=bench_profiles.revision+1, updated_at=excluded.updated_at`,
		p.ID, p.Name, p.Description, p.State, p.Version, p.Digest, p.SourceID,
		boolInt(p.Builtin), p.OnFailure, encodeJSON(p.Components), p.Revision,
		formatTime(p.CreatedAt), formatTime(p.UpdatedAt))
	return err
}

func (s store) deleteProfile(id string) error {
	_, err := s.db.Exec(`DELETE FROM bench_profiles WHERE id=? AND builtin=0`, id)
	return err
}

func (s store) countPacksUsingProfile(digest string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM bench_packs WHERE profile_digest=?`, digest).Scan(&n)
	return n, err
}

// backfillRunProfiles stamps rows written before profiles existed with the
// contract they were actually scored under, so the leaderboard's join does not
// silently drop the entire history.
func (s store) backfillRunProfiles(digest string) (int64, error) {
	res, err := s.db.Exec(`UPDATE bench_runs SET scoring_profile_digest=? WHERE scoring_profile_digest=''`, digest)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := s.db.Exec(`UPDATE bench_packs SET profile_digest=? WHERE profile_digest='' AND state=?`, digest, PackStateSealed); err != nil {
		return n, err
	}
	return n, nil
}

// ---- runs ----

const runColumns = `id,pack_id,pack_name,pack_category,pack_version,pack_digest,scoring_version,name,targets_json,trials,` +
	`suite_id,experiment_id,status,provenance_json,summary_json,error,created_at,started_at,finished_at,scoring_profile_digest`

func scanRun(row interface{ Scan(...any) error }) (*Run, error) {
	var run Run
	var targets, provenance, summary, created string
	var started, finished sql.NullString
	err := row.Scan(&run.ID, &run.PackID, &run.PackName, &run.PackCategory, &run.PackVersion, &run.PackDigest,
		&run.ScoringVersion, &run.Name, &targets, &run.Trials, &run.SuiteID, &run.ExperimentID,
		&run.Status, &provenance, &summary, &run.Error, &created, &started, &finished, &run.ScoringProfileDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decodeJSON(targets, &run.Targets)
	decodeJSON(provenance, &run.Provenance)
	decodeJSON(summary, &run.Summary)
	if run.Targets == nil {
		run.Targets = []Target{}
	}
	run.CreatedAt = parseTime(created)
	run.StartedAt, run.FinishedAt = scanTime(started), scanTime(finished)
	return &run, nil
}

func (s store) saveRun(run *Run) error {
	_, err := s.db.Exec(`INSERT INTO bench_runs(`+runColumns+`)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			suite_id=excluded.suite_id, experiment_id=excluded.experiment_id, status=excluded.status,
			provenance_json=excluded.provenance_json, summary_json=excluded.summary_json,
			error=excluded.error, started_at=excluded.started_at, finished_at=excluded.finished_at`,
		run.ID, run.PackID, run.PackName, run.PackCategory, run.PackVersion, run.PackDigest, run.ScoringVersion,
		run.Name, encodeJSON(run.Targets), run.Trials, run.SuiteID, run.ExperimentID, run.Status,
		encodeJSON(run.Provenance), encodeJSON(run.Summary), run.Error,
		formatTime(run.CreatedAt), nullableTime(run.StartedAt), nullableTime(run.FinishedAt), run.ScoringProfileDigest)
	return err
}

func (s store) getRun(id string) (*Run, error) {
	run, err := scanRun(s.db.QueryRow(`SELECT `+runColumns+` FROM bench_runs WHERE id=?`, id))
	if err != nil || run == nil {
		return run, err
	}
	run.Results, err = s.listResults(id)
	return run, err
}

func (s store) listRuns(limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+runColumns+` FROM bench_runs ORDER BY created_at DESC LIMIT ?`, limit)
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
		if run != nil {
			runs = append(runs, *run)
		}
	}
	return runs, rows.Err()
}

// nextActiveRun returns the oldest run still needing work, so the worker
// advances one run per tick in creation order.
func (s store) nextActiveRun() (*Run, error) {
	return scanRun(s.db.QueryRow(`SELECT `+runColumns+` FROM bench_runs
		WHERE status IN (?,?) ORDER BY created_at LIMIT 1`, RunStatusQueued, RunStatusRunning))
}

// ---- results ----

const resultColumns = `id,bench_run_id,scenario_id,scenario_name,scenario_tags_json,target_index,target_json,trial,` +
	`eval_run_id,admission,invalid_reason,passed,score_json,metrics_json,error,created_at`

func (s store) saveResult(result *Result) error {
	_, err := s.db.Exec(`INSERT INTO bench_results(`+resultColumns+`)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			admission=excluded.admission, invalid_reason=excluded.invalid_reason, passed=excluded.passed,
			score_json=excluded.score_json, metrics_json=excluded.metrics_json, error=excluded.error`,
		result.ID, result.BenchRunID, result.ScenarioID, result.ScenarioName, encodeJSON(result.ScenarioTags), result.TargetIndex,
		encodeJSON(result.Target), result.Trial, result.EvalRunID, result.Admission,
		result.InvalidReason, boolInt(result.Passed), encodeJSON(result.Score),
		encodeJSON(result.Metrics), result.Error, formatTime(result.CreatedAt))
	return err
}

func scanResults(rows *sql.Rows) ([]Result, error) {
	defer rows.Close()
	results := []Result{}
	for rows.Next() {
		var result Result
		var tags, target, score, metrics, created string
		var passed int
		if err := rows.Scan(&result.ID, &result.BenchRunID, &result.ScenarioID, &result.ScenarioName,
			&tags, &result.TargetIndex, &target, &result.Trial, &result.EvalRunID, &result.Admission,
			&result.InvalidReason, &passed, &score, &metrics, &result.Error, &created); err != nil {
			return nil, err
		}
		decodeJSON(target, &result.Target)
		decodeJSON(tags, &result.ScenarioTags)
		decodeJSON(score, &result.Score)
		decodeJSON(metrics, &result.Metrics)
		result.Passed = passed == 1
		result.CreatedAt = parseTime(created)
		results = append(results, result)
	}
	return results, rows.Err()
}

func (s store) listResults(benchRunID string) ([]Result, error) {
	rows, err := s.db.Query(`SELECT `+resultColumns+` FROM bench_results
		WHERE bench_run_id=? ORDER BY scenario_id, target_index, trial`, benchRunID)
	if err != nil {
		return nil, err
	}
	return scanResults(rows)
}

// resultWithPack carries the sealed pack a result belongs to, so a global
// leaderboard can report coverage rather than silently averaging targets that
// ran different benchmarks.
type resultWithPack struct {
	Result
	PackDigest   string
	PackName     string
	PackCategory string
	PackVersion  string
}

// listAdmittedResults gathers every admitted result under one scoring version
// across all sealed packs. Comparability is enforced by the scoring-version
// filter; coverage differences are reported rather than hidden.
func (s store) listAdmittedResults(profileDigest, category string) ([]resultWithPack, error) {
	query := `SELECT r.` + strings.ReplaceAll(resultColumns, ",", ",r.") + `,
		b.pack_digest, b.pack_name, b.pack_category, b.pack_version
		FROM bench_results r JOIN bench_runs b ON b.id = r.bench_run_id
		WHERE b.scoring_profile_digest=? AND r.admission<>? AND b.pack_digest<>''`
	args := []any{profileDigest, AdmissionInvalid}
	if category != "" {
		query += ` AND b.pack_category=?`
		args = append(args, category)
	}
	query += ` ORDER BY b.pack_digest, r.scenario_id, r.target_index, r.trial`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []resultWithPack{}
	for rows.Next() {
		var item resultWithPack
		var tags, target, score, metrics, created string
		var passed int
		if err := rows.Scan(&item.ID, &item.BenchRunID, &item.ScenarioID, &item.ScenarioName,
			&tags, &item.TargetIndex, &target, &item.Trial, &item.EvalRunID, &item.Admission,
			&item.InvalidReason, &passed, &score, &metrics, &item.Error, &created,
			&item.PackDigest, &item.PackName, &item.PackCategory, &item.PackVersion); err != nil {
			return nil, err
		}
		decodeJSON(target, &item.Target)
		decodeJSON(tags, &item.ScenarioTags)
		decodeJSON(score, &item.Score)
		decodeJSON(metrics, &item.Metrics)
		item.Passed = passed == 1
		item.CreatedAt = parseTime(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

// scoringVersions lists every scoring version with recorded results, so a
// caller can tell when the record spans more than one incomparable contract.
func (s store) scoringVersions(category string) ([]string, error) {
	query := `SELECT DISTINCT scoring_profile_digest FROM bench_runs WHERE scoring_profile_digest<>''`
	args := []any{}
	if category != "" {
		query += ` AND pack_category=?`
		args = append(args, category)
	}
	rows, err := s.db.Query(query+` ORDER BY scoring_profile_digest`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

// ---- baselines ----

func (s store) saveBaseline(baseline *Baseline) error {
	_, err := s.db.Exec(`INSERT INTO bench_baselines(id,pack_digest,scenario_id,label,target_json,score,pass_rate,metrics_json,source_run_id,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(pack_digest,scenario_id) DO UPDATE SET
			label=excluded.label, target_json=excluded.target_json, score=excluded.score,
			pass_rate=excluded.pass_rate, metrics_json=excluded.metrics_json,
			source_run_id=excluded.source_run_id, created_at=excluded.created_at`,
		baseline.ID, baseline.PackDigest, baseline.ScenarioID, baseline.Label,
		encodeJSON(baseline.Target), baseline.Score, baseline.PassRate,
		encodeJSON(baseline.Metrics), baseline.SourceRunID, formatTime(baseline.CreatedAt))
	return err
}

func (s store) listBaselines(digest string) ([]Baseline, error) {
	rows, err := s.db.Query(`SELECT id,pack_digest,scenario_id,label,target_json,score,pass_rate,metrics_json,source_run_id,created_at
		FROM bench_baselines WHERE pack_digest=? ORDER BY scenario_id`, digest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	baselines := []Baseline{}
	for rows.Next() {
		var baseline Baseline
		var target, metrics, created string
		if err := rows.Scan(&baseline.ID, &baseline.PackDigest, &baseline.ScenarioID, &baseline.Label,
			&target, &baseline.Score, &baseline.PassRate, &metrics, &baseline.SourceRunID, &created); err != nil {
			return nil, err
		}
		decodeJSON(target, &baseline.Target)
		decodeJSON(metrics, &baseline.Metrics)
		baseline.CreatedAt = parseTime(created)
		baselines = append(baselines, baseline)
	}
	return baselines, rows.Err()
}

// summarize folds results into per-target aggregates. Invalid results count
// toward Invalid and nothing else — they never move a pass rate or an average.
func summarize(results []Result, targets []Target) Summary {
	summary := Summary{Targets: []TargetSummary{}}
	buckets := map[int]*TargetSummary{}
	order := []int{}

	for _, result := range results {
		summary.Total++
		bucket, ok := buckets[result.TargetIndex]
		if !ok {
			target := result.Target
			if result.TargetIndex < len(targets) {
				target = targets[result.TargetIndex]
			}
			bucket = &TargetSummary{TargetIndex: result.TargetIndex, Target: target, Label: target.label()}
			buckets[result.TargetIndex] = bucket
			order = append(order, result.TargetIndex)
		}
		bucket.Runs++
		if result.Admission == AdmissionInvalid {
			summary.Invalid++
			bucket.Invalid++
			continue
		}
		summary.Verified++
		bucket.Verified++
		if result.Passed {
			summary.Passed++
			bucket.Passed++
		}
		bucket.AverageScore += result.Score.Score
		bucket.AverageDurationMS += float64(result.Metrics.DurationMS)
		bucket.AverageTokens += float64(result.Metrics.TokensTotal)
		bucket.AverageCostUSD += result.Metrics.CostUSD
	}

	if summary.Verified > 0 {
		summary.PassRate = round3(float64(summary.Passed) / float64(summary.Verified))
	}
	totalScore := 0.0
	sort.Ints(order)
	for _, index := range order {
		bucket := buckets[index]
		if bucket.Verified > 0 {
			divisor := float64(bucket.Verified)
			totalScore += bucket.AverageScore
			bucket.PassRate = round3(float64(bucket.Passed) / divisor)
			bucket.AverageScore = round1(bucket.AverageScore / divisor)
			bucket.AverageDurationMS = math.Round(bucket.AverageDurationMS / divisor)
			bucket.AverageTokens = math.Round(bucket.AverageTokens / divisor)
			bucket.AverageCostUSD = round3(bucket.AverageCostUSD / divisor)
		}
		summary.Targets = append(summary.Targets, *bucket)
	}
	if summary.Verified > 0 {
		summary.AverageScore = round1(totalScore / float64(summary.Verified))
	}
	return summary
}

func newID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}
