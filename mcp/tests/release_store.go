package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

type releaseSnapshot struct {
	Config      *ReleaseConfig           `json:"config,omitempty"`
	Fixture     json.RawMessage          `json:"fixture,omitempty"`
	Target      *TargetConfig            `json:"target,omitempty"`
	Candidate   *CandidateRef            `json:"candidate,omitempty"`
	Credentials map[string]CredentialRef `json:"credentials,omitempty"`
	Session     *SessionConfig           `json:"session,omitempty"`
}

func (s store) getSuite(id int64) (Suite, error) {
	return scanSuite(s.db.QueryRow(`SELECT `+suiteColumns+` FROM test_suites WHERE project_id=? AND id=? AND archived=0`, s.project, id))
}

func validateRetry(r Run, suite int64, opt QueueOptions) error {
	if r.SuiteID != suite {
		return errors.New("request_key already used for another suite")
	}
	if r.Release == nil && opt.CandidateDigest == "" && opt.Candidate == nil && opt.SuiteRevision == "" && opt.FixtureRevision == "" {
		return nil
	}
	if opt.Candidate != nil && opt.CandidateDigest == "" && r.Release != nil {
		// The build was resolved when the run was first queued; retries reuse it.
		return nil
	}
	if r.Release == nil || r.Release.CandidateDigest != opt.CandidateDigest ||
		opt.SuiteRevision != "" && opt.SuiteRevision != r.Release.SuiteRevision ||
		opt.FixtureRevision != "" && opt.FixtureRevision != r.Release.FixtureRevision {
		return errors.New("request_key already used for another release candidate or revision")
	}
	return nil
}

func (s store) existingRequest(suite int64, key string, opt QueueOptions) (Run, error) {
	r, err := scanRun(s.db.QueryRow(`SELECT `+runColumns+` FROM test_runs WHERE project_id=? AND request_key=?`, s.project, key))
	if err != nil {
		return r, err
	}
	return r, validateRetry(r, suite, opt)
}

func (s store) getReleaseSnapshot(id int64) (releaseSnapshot, error) {
	var snapshot releaseSnapshot
	var raw string
	if err := s.db.QueryRow(`SELECT fixture_spec FROM test_runs WHERE project_id=? AND id=?`, s.project, id).Scan(&raw); err != nil {
		return snapshot, err
	}
	return snapshot, json.Unmarshal([]byte(raw), &snapshot)
}

func (s store) saveReleaseRecord(id int64, release *ReleaseRecord, cleanup bool) error {
	return changed(s.db.Exec(`UPDATE test_runs SET release_record=?,cleanup_pending=? WHERE project_id=? AND id=? AND status='running'`, encode(release), cleanup, s.project, id))
}

func (s store) cleanupCandidates() ([]Run, error) {
	rows, err := s.db.Query(`SELECT `+runColumns+` FROM test_runs WHERE project_id=? AND cleanup_pending=1 AND status='failed' ORDER BY id DESC LIMIT 20`, s.project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s store) cancelled(id int64) bool {
	var cancelled bool
	err := s.db.QueryRow(`SELECT cancel_requested FROM test_runs WHERE project_id=? AND id=?`, s.project, id).Scan(&cancelled)
	return err != nil || cancelled
}

func (s store) cancel(id int64) (Run, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	r, err := scanRun(tx.QueryRow(`SELECT `+runColumns+` FROM test_runs WHERE project_id=? AND id=?`, s.project, id))
	if err != nil {
		return r, err
	}
	switch r.Status {
	case "queued":
		r.Status, r.CancelRequested, r.Error, r.FinishedAt = "failed", true, "run cancelled", now()
		_, err = tx.Exec(`UPDATE test_runs SET status=?,cancel_requested=1,error=?,finished_at=? WHERE project_id=? AND id=?`, r.Status, r.Error, r.FinishedAt, s.project, id)
		if err == nil {
			err = outbox(tx, s.project, "tests.run.completed", r)
		}
	case "running":
		r.CancelRequested = true
		_, err = tx.Exec(`UPDATE test_runs SET cancel_requested=1 WHERE project_id=? AND id=?`, s.project, id)
	}
	if err != nil {
		return r, err
	}
	return r, tx.Commit()
}

type Verdict struct {
	Allowed bool           `json:"allowed"`
	State   string         `json:"state"`
	RunID   int64          `json:"run_id,omitempty"`
	Release *ReleaseRecord `json:"release,omitempty"`
}

func (s store) verdict(suite int64, opt QueueOptions) (Verdict, error) {
	if suite < 1 || !digestPattern.MatchString(opt.CandidateDigest) || !digestPattern.MatchString(opt.SuiteRevision) ||
		!digestPattern.MatchString(opt.FixtureRevision) {
		return Verdict{}, errors.New("suite_id, candidate_digest, suite_revision and fixture_revision are required")
	}
	r, err := scanRun(s.db.QueryRow(`SELECT `+runColumns+` FROM test_runs WHERE project_id=? AND suite_id=? AND release_record!='null'
		AND json_extract(release_record,'$.candidate_digest')=? AND json_extract(release_record,'$.suite_revision')=?
		AND json_extract(release_record,'$.fixture_revision')=?
		AND coalesce(json_extract(release_record,'$.partial'),0)=0 ORDER BY id DESC LIMIT 1`,
		s.project, suite, opt.CandidateDigest, opt.SuiteRevision, opt.FixtureRevision))
	if errors.Is(err, sql.ErrNoRows) {
		return Verdict{State: "missing"}, nil
	}
	if err != nil {
		return Verdict{}, err
	}
	if r.Release == nil {
		return Verdict{}, fmt.Errorf("run %d has no release evidence", r.ID)
	}
	state := r.Status
	if r.Release.Inconclusive {
		state = "inconclusive"
	}
	measured := r.Release.EnvironmentRun != "" && r.Release.RuntimeID != ""
	switch r.Release.Mode {
	case "existing":
		measured = true
	case "source":
		measured = r.Release.SourceDigest != ""
	}
	if opt.SourceDigest != "" && r.Release.SourceDigest != "" && opt.SourceDigest != r.Release.SourceDigest {
		return Verdict{State: "source_mismatch", RunID: r.ID, Release: r.Release}, nil
	}
	return Verdict{
		Allowed: r.Status == "passed" && !r.CancelRequested && !r.Release.Inconclusive && !r.Release.Partial && measured &&
			r.Release.ObservedDigest == opt.CandidateDigest && len(r.Release.Missing) == 0,
		State: state, RunID: r.ID, Release: r.Release,
	}, nil
}

// suiteVerdict is one release-blocking suite's verdict for a candidate.
type suiteVerdict struct {
	SuiteID int64          `json:"suite_id"`
	Name    string         `json:"name"`
	Allowed bool           `json:"allowed"`
	State   string         `json:"state"`
	RunID   int64          `json:"run_id,omitempty"`
	Release *ReleaseRecord `json:"release,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// releaseVerdicts checks a candidate against every release-blocking suite at
// its current suite and fixture revisions.
func (a *App) releaseVerdicts(ctx context.Context, app *sdk.AppCtx, candidate, source string) (map[string]any, error) {
	if !digestPattern.MatchString(candidate) {
		return nil, errors.New("candidate_digest must be a lowercase SHA-256 digest")
	}
	s := store{app.AppDB(), app.CurrentProject()}
	suites, err := s.suites()
	if err != nil {
		return nil, err
	}
	out := []suiteVerdict{}
	allowed := true
	for _, suite := range suites {
		if !suite.ReleaseBlocking {
			continue
		}
		e := suiteVerdict{SuiteID: suite.ID, Name: suite.Name}
		spec, err := a.releaseSpec(ctx, app, suite.ID)
		if err != nil {
			e.State, e.Error = "error", err.Error()
		} else {
			opt := QueueOptions{CandidateDigest: candidate, SourceDigest: source}
			opt.SuiteRevision, _ = spec["suite_revision"].(string)
			opt.FixtureRevision, _ = spec["fixture_revision"].(string)
			v, err := s.verdict(suite.ID, opt)
			if err != nil {
				e.State, e.Error = "error", err.Error()
			} else {
				e.Allowed, e.State, e.RunID, e.Release = v.Allowed, v.State, v.RunID, v.Release
			}
		}
		allowed = allowed && e.Allowed
		out = append(out, e)
	}
	result := map[string]any{"candidate_digest": candidate, "suites": out}
	if len(out) == 0 {
		allowed = false
		result["reason"] = "no suite is marked release_blocking"
	}
	result["allowed"] = allowed
	return result, nil
}

// callerLabel names who changed a quarantine, for its audit field.
func callerLabel(ctx context.Context) string {
	if c := sdk.CallerFrom(ctx); c != nil && c.AgentID > 0 {
		return fmt.Sprintf("agent %d", c.AgentID)
	}
	return "user"
}
