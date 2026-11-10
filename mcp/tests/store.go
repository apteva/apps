package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type store struct {
	db      *sql.DB
	project string
}
type scanner interface{ Scan(...any) error }

const suiteColumns = `id,name,description,environment,archived,created_at,updated_at,release_config`
const checkColumns = `id,suite_id,name,kind,enabled,definition`
const runColumns = `id,suite_id,suite_name,environment,status,trigger,passed,failed,error,created_at,started_at,finished_at,release_record,cancel_requested`

func scanSuite(row scanner) (s Suite, err error) {
	var raw string
	err = row.Scan(&s.ID, &s.Name, &s.Description, &s.Environment, &s.Archived, &s.CreatedAt, &s.UpdatedAt, &raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &s.Release)
	}
	return
}
func scanCheck(row scanner) (c Check, err error) {
	var raw string
	err = row.Scan(&c.ID, &c.SuiteID, &c.Name, &c.Kind, &c.Enabled, &raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &c.Definition)
	}
	return
}
func scanRun(row scanner) (r Run, err error) {
	var raw string
	err = row.Scan(&r.ID, &r.SuiteID, &r.SuiteName, &r.Environment, &r.Status, &r.Trigger, &r.Passed, &r.Failed, &r.Error, &r.CreatedAt, &r.StartedAt, &r.FinishedAt, &raw, &r.CancelRequested)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &r.Release)
	}
	return
}
func outbox(tx *sql.Tx, project, topic string, payload any) error {
	_, err := tx.Exec(`INSERT INTO test_outbox(project_id,topic,payload) VALUES(?,?,?)`, project, topic, encode(payload))
	return err
}
func changed(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s store) suites() ([]Suite, error) {
	// The full OpenAPI document is fetched only when editing one suite.
	columns := strings.Replace(suiteColumns, "release_config", "json_remove(release_config,'$.openapi')", 1)
	rows, err := s.db.Query(`SELECT `+columns+` FROM test_suites WHERE project_id=? AND archived=0 ORDER BY id DESC LIMIT 500`, s.project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Suite{}
	for rows.Next() {
		item, err := scanSuite(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s store) saveSuite(v Suite) (Suite, error) {
	if v.Release != nil {
		if err := v.Release.validate(); err != nil {
			return v, err
		}
	}
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" || len(v.Name) > 200 || len(v.Description) > 5000 || len(v.Environment) > 200 {
		return v, errors.New("name (1–200 characters), description (up to 5000), and environment (up to 200) required")
	}
	if v.Environment == "" {
		v.Environment = "test"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	if v.ID == 0 {
		res, e := tx.Exec(`INSERT INTO test_suites(project_id,name,description,environment,created_at,updated_at,release_config) VALUES(?,?,?,?,?,?,?)`, s.project, v.Name, v.Description, v.Environment, now(), now(), encode(v.Release))
		if e != nil {
			return v, e
		}
		v.ID, err = res.LastInsertId()
	} else {
		err = changed(tx.Exec(`UPDATE test_suites SET name=?,description=?,environment=?,updated_at=?,release_config=? WHERE project_id=? AND id=? AND archived=0`, v.Name, v.Description, v.Environment, now(), encode(v.Release), s.project, v.ID))
	}
	if err != nil {
		return v, err
	}
	v, err = scanSuite(tx.QueryRow(`SELECT `+suiteColumns+` FROM test_suites WHERE project_id=? AND id=?`, s.project, v.ID))
	if err != nil {
		return v, err
	}
	if err = outbox(tx, s.project, "tests.suite.saved", map[string]any{"suite_id": v.ID, "name": v.Name}); err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func (s store) archiveSuite(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = changed(tx.Exec(`UPDATE test_suites SET archived=1,updated_at=? WHERE project_id=? AND id=? AND archived=0`, now(), s.project, id)); err != nil {
		return err
	}
	if err = outbox(tx, s.project, "tests.suite.archived", map[string]any{"suite_id": id}); err != nil {
		return err
	}
	return tx.Commit()
}
func (s store) checks(suite int64) ([]Check, error) {
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM test_suites WHERE project_id=? AND id=?`, s.project, suite).Scan(&id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT `+checkColumns+` FROM test_checks WHERE project_id=? AND suite_id=? ORDER BY id`, s.project, suite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Check{}
	for rows.Next() {
		c, e := scanCheck(rows)
		if e != nil {
			return nil, e
		}
		items = append(items, c)
	}
	return items, rows.Err()
}
func (s store) saveCheck(c Check) (Check, error) {
	if err := validateCheck(&c); err != nil {
		return c, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	suite, err := scanSuite(tx.QueryRow(`SELECT `+suiteColumns+` FROM test_suites WHERE project_id=? AND id=? AND archived=0`, s.project, c.SuiteID))
	if err != nil {
		return c, err
	}
	if err = validateScenarioBinding(c, suite.Release); err != nil {
		return c, err
	}
	if c.ID == 0 {
		var count int
		if err = tx.QueryRow(`SELECT count(*) FROM test_checks WHERE project_id=? AND suite_id=?`, s.project, c.SuiteID).Scan(&count); err != nil {
			return c, err
		}
		if count >= maxChecks {
			return c, fmt.Errorf("maximum %d checks per suite", maxChecks)
		}
		res, e := tx.Exec(`INSERT INTO test_checks(project_id,suite_id,name,kind,enabled,definition,updated_at) VALUES(?,?,?,?,?,?,?)`, s.project, c.SuiteID, c.Name, c.Kind, c.Enabled, encode(c.Definition), now())
		if e != nil {
			return c, e
		}
		c.ID, err = res.LastInsertId()
	} else {
		err = changed(tx.Exec(`UPDATE test_checks SET name=?,kind=?,enabled=?,definition=?,updated_at=? WHERE project_id=? AND suite_id=? AND id=?`, c.Name, c.Kind, c.Enabled, encode(c.Definition), now(), s.project, c.SuiteID, c.ID))
	}
	if err != nil {
		return c, err
	}
	if err = outbox(tx, s.project, "tests.check.saved", map[string]any{"suite_id": c.SuiteID, "check_id": c.ID}); err != nil {
		return c, err
	}
	return c, tx.Commit()
}
func (s store) deleteCheck(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = changed(tx.Exec(`DELETE FROM test_checks WHERE project_id=? AND id=?`, s.project, id)); err != nil {
		return err
	}
	if err = outbox(tx, s.project, "tests.check.deleted", map[string]any{"check_id": id}); err != nil {
		return err
	}
	return tx.Commit()
}
func (s store) queue(suite int64, trigger, key string) (Run, error) {
	return s.queueRelease(suite, trigger, key, QueueOptions{}, environmentDefinition{})
}

func (s store) queueRelease(suite int64, trigger, key string, options QueueOptions, fixture environmentDefinition) (Run, error) {
	if len(key) > 200 {
		return Run{}, errors.New("request_key exceeds 200 characters")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	// A retried request must return its original run, even if the suite has changed.
	if key != "" {
		old, e := scanRun(tx.QueryRow(`SELECT `+runColumns+` FROM test_runs WHERE project_id=? AND request_key=?`, s.project, key))
		if e == nil {
			return old, validateRetry(old, suite, options)
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return Run{}, e
		}
	}
	v, err := scanSuite(tx.QueryRow(`SELECT `+suiteColumns+` FROM test_suites WHERE project_id=? AND id=? AND archived=0`, s.project, suite))
	if err != nil {
		return Run{}, err
	}
	var pending int
	if err = tx.QueryRow(`SELECT count(*) FROM test_runs WHERE project_id=? AND status IN ('queued','running')`, s.project).Scan(&pending); err != nil {
		return Run{}, err
	}
	if pending >= 100 {
		return Run{}, errors.New("project already has 100 pending runs")
	}
	rows, err := tx.Query(`SELECT `+checkColumns+` FROM test_checks WHERE project_id=? AND suite_id=? AND enabled=1 ORDER BY id`, s.project, suite)
	if err != nil {
		return Run{}, err
	}
	checks := []Check{}
	for rows.Next() {
		c, e := scanCheck(rows)
		if e != nil {
			rows.Close()
			return Run{}, e
		}
		checks = append(checks, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Run{}, err
	}
	if len(checks) == 0 {
		return Run{}, errors.New("suite has no enabled checks")
	}
	var requestKey any
	if key != "" {
		requestKey = key
	}
	r := Run{SuiteID: suite, SuiteName: v.Name, Environment: v.Environment, Status: "queued", Trigger: trigger, CreatedAt: now()}
	for _, check := range checks {
		if err = validateScenarioBinding(check, v.Release); err != nil {
			return r, err
		}
	}
	snapshot := releaseSnapshot{}
	if v.Release != nil {
		if !digestPattern.MatchString(options.CandidateDigest) {
			return r, errors.New("release run requires a lowercase SHA-256 candidate_digest")
		}
		if err = checkFixture(fixture, v.Release); err != nil {
			return r, err
		}
		r.Release = &ReleaseRecord{
			CandidateDigest: options.CandidateDigest, SuiteRevision: suiteRevision(v, checks),
			FixtureRevision: canonicalDigest(fixture.Spec),
		}
		if options.SuiteRevision != "" && options.SuiteRevision != r.Release.SuiteRevision ||
			options.FixtureRevision != "" && options.FixtureRevision != r.Release.FixtureRevision {
			return r, errors.New("suite or fixture revision does not match the release request")
		}
		if len(v.Release.OpenAPI) > 0 {
			r.Release.OpenAPIDigest = canonicalDigest(v.Release.OpenAPI)
		}
		snapshot = releaseSnapshot{Config: v.Release, Fixture: fixture.Spec}
	} else if options != (QueueOptions{}) {
		return r, errors.New("candidate and revision options require a release suite")
	}
	res, err := tx.Exec(`INSERT INTO test_runs(project_id,suite_id,suite_name,environment,status,trigger,request_key,snapshot,created_at,release_record,fixture_spec) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, s.project, suite, v.Name, v.Environment, r.Status, trigger, requestKey, encode(checks), r.CreatedAt, encode(r.Release), encode(snapshot))
	if err != nil {
		return r, err
	}
	r.ID, err = res.LastInsertId()
	if err != nil {
		return r, err
	}
	if err = outbox(tx, s.project, "tests.run.queued", r); err != nil {
		return r, err
	}
	return r, tx.Commit()
}
func (s store) runs(suite int64) ([]Run, error) {
	rows, err := s.db.Query(`SELECT `+runColumns+` FROM test_runs WHERE project_id=? AND (?=0 OR suite_id=?) ORDER BY id DESC LIMIT 100`, s.project, suite, suite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Run{}
	for rows.Next() {
		r, e := scanRun(rows)
		if e != nil {
			return nil, e
		}
		items = append(items, r)
	}
	return items, rows.Err()
}
func (s store) run(id int64) (Run, error) {
	r, err := scanRun(s.db.QueryRow(`SELECT `+runColumns+` FROM test_runs WHERE project_id=? AND id=?`, s.project, id))
	if err != nil {
		return r, err
	}
	var snapshot string
	if err = s.db.QueryRow(`SELECT snapshot FROM test_runs WHERE project_id=? AND id=?`, s.project, id).Scan(&snapshot); err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(snapshot), &r.Checks); err != nil {
		return r, err
	}
	rows, err := s.db.Query(`SELECT check_id,name,kind,status,duration_ms,output,assertions,error FROM test_results WHERE project_id=? AND run_id=? ORDER BY id`, s.project, id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	r.Results = []Result{}
	for rows.Next() {
		var v Result
		var output, assertions string
		if err = rows.Scan(&v.CheckID, &v.Name, &v.Kind, &v.Status, &v.DurationMS, &output, &assertions, &v.Error); err != nil {
			return r, err
		}
		if err = json.Unmarshal([]byte(output), &v.Output); err != nil {
			return r, err
		}
		if err = json.Unmarshal([]byte(assertions), &v.Assertions); err != nil {
			return r, err
		}
		r.Results = append(r.Results, v)
	}
	return r, rows.Err()
}
func (s store) claim() (Run, error) {
	// UPDATE RETURNING is one SQLite statement: overlapping ticks cannot claim the same run.
	var id int64
	err := s.db.QueryRow(`UPDATE test_runs SET status='running',started_at=? WHERE id=(SELECT id FROM test_runs WHERE project_id=? AND status='queued' ORDER BY id LIMIT 1) AND status='queued' RETURNING id`, now(), s.project).Scan(&id)
	if err != nil {
		return Run{}, err
	}
	return s.run(id)
}
func (s store) saveResult(id int64, r Result) error {
	_, err := s.db.Exec(`INSERT INTO test_results(project_id,run_id,check_id,name,kind,status,duration_ms,output,assertions,error) VALUES(?,?,?,?,?,?,?,?,?,?)`, s.project, id, r.CheckID, r.Name, r.Kind, r.Status, r.DurationMS, encode(r.Output), encode(r.Assertions), r.Error)
	return err
}
func (s store) finish(id int64, message string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var passed, failed int
	if err = tx.QueryRow(`SELECT COALESCE(sum(status='passed'),0),COALESCE(sum(status!='passed'),0) FROM test_results WHERE project_id=? AND run_id=?`, s.project, id).Scan(&passed, &failed); err != nil {
		return err
	}
	var cancelled bool
	if err = tx.QueryRow(`SELECT cancel_requested FROM test_runs WHERE project_id=? AND id=?`, s.project, id).Scan(&cancelled); err != nil {
		return err
	}
	if cancelled {
		message = "run cancelled"
	}
	status := "passed"
	if failed > 0 || message != "" || passed == 0 {
		status = "failed"
	}
	if err = changed(tx.Exec(`UPDATE test_runs SET status=?,passed=?,failed=?,error=?,finished_at=? WHERE project_id=? AND id=? AND status='running'`, status, passed, failed, message, now(), s.project, id)); err != nil {
		return err
	}
	r, err := scanRun(tx.QueryRow(`SELECT `+runColumns+` FROM test_runs WHERE project_id=? AND id=?`, s.project, id))
	if err != nil {
		return err
	}
	if err = outbox(tx, s.project, "tests.run.completed", r); err != nil {
		return err
	}
	return tx.Commit()
}
