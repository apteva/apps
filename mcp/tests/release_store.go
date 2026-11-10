package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type releaseSnapshot struct {
	Config  *ReleaseConfig  `json:"config,omitempty"`
	Fixture json.RawMessage `json:"fixture,omitempty"`
}

func (s store) getSuite(id int64) (Suite, error) {
	return scanSuite(s.db.QueryRow(`SELECT `+suiteColumns+` FROM test_suites WHERE project_id=? AND id=? AND archived=0`, s.project, id))
}

func validateRetry(r Run, suite int64, opt QueueOptions) error {
	if r.SuiteID != suite {
		return errors.New("request_key already used for another suite")
	}
	if r.Release == nil && opt == (QueueOptions{}) {
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
		AND json_extract(release_record,'$.fixture_revision')=? ORDER BY id DESC LIMIT 1`,
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
	return Verdict{
		Allowed: r.Status == "passed" && !r.CancelRequested && r.Release.EnvironmentRun != "" && r.Release.RuntimeID != "" &&
			r.Release.ObservedDigest == opt.CandidateDigest && len(r.Release.Missing) == 0,
		State: r.Status, RunID: r.ID, Release: r.Release,
	}, nil
}
