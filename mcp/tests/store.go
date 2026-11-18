package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type store struct {
	db      *sql.DB
	project string
}
type scanner interface{ Scan(...any) error }

const suiteColumns = `id,name,description,environment,archived,created_at,updated_at,release_config,suite_config`
const checkColumns = `id,suite_id,name,kind,enabled,definition`
const runColumns = `id,suite_id,suite_name,environment,status,trigger,passed,failed,blocked,error,created_at,started_at,finished_at,release_record,cancel_requested,warnings`

// suiteConfig holds the 0.5.0 suite fields in one JSON column.
type suiteConfig struct {
	Target          *TargetConfig            `json:"target,omitempty"`
	Credentials     map[string]CredentialRef `json:"credentials,omitempty"`
	Session         *SessionConfig           `json:"session,omitempty"`
	ReleaseBlocking bool                     `json:"release_blocking,omitempty"`
}

func scanSuite(row scanner) (s Suite, err error) {
	var raw, extra string
	err = row.Scan(&s.ID, &s.Name, &s.Description, &s.Environment, &s.Archived, &s.CreatedAt, &s.UpdatedAt, &raw, &extra)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &s.Release)
	}
	if err == nil {
		var cfg suiteConfig
		if err = json.Unmarshal([]byte(extra), &cfg); err == nil {
			s.Target, s.Credentials, s.Session, s.ReleaseBlocking = cfg.Target, cfg.Credentials, cfg.Session, cfg.ReleaseBlocking
		}
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
	var raw, warnings string
	err = row.Scan(&r.ID, &r.SuiteID, &r.SuiteName, &r.Environment, &r.Status, &r.Trigger, &r.Passed, &r.Failed, &r.Blocked, &r.Error, &r.CreatedAt, &r.StartedAt, &r.FinishedAt, &raw, &r.CancelRequested, &warnings)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &r.Release)
	}
	if err == nil && warnings != "" && warnings != "[]" {
		err = json.Unmarshal([]byte(warnings), &r.Warnings)
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
	if err := validateSuiteExtensions(&v); err != nil {
		return v, err
	}
	config := encode(suiteConfig{v.Target, v.Credentials, v.Session, v.ReleaseBlocking})
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
		res, e := tx.Exec(`INSERT INTO test_suites(project_id,name,description,environment,created_at,updated_at,release_config,suite_config) VALUES(?,?,?,?,?,?,?,?)`, s.project, v.Name, v.Description, v.Environment, now(), now(), encode(v.Release), config)
		if e != nil {
			return v, e
		}
		v.ID, err = res.LastInsertId()
	} else {
		err = changed(tx.Exec(`UPDATE test_suites SET name=?,description=?,environment=?,updated_at=?,release_config=?,suite_config=? WHERE project_id=? AND id=? AND archived=0`, v.Name, v.Description, v.Environment, now(), encode(v.Release), config, s.project, v.ID))
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
	if err = validateScenarioBinding(c, suite); err != nil {
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
	return s.queueRelease(suite, trigger, key, QueueOptions{}, queuePrep{})
}

func (s store) queueRelease(suite int64, trigger, key string, options QueueOptions, prep queuePrep) (Run, error) {
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
	partial := len(options.CheckNames) > 0
	if partial && (options.SuiteRevision != "" || options.FixtureRevision != "") {
		return Run{}, errors.New("check_names runs never produce a release verdict; omit suite_revision and fixture_revision")
	}
	rows, err := tx.Query(`SELECT `+checkColumns+` FROM test_checks WHERE project_id=? AND suite_id=? AND (enabled=1 OR ?) ORDER BY id`, s.project, suite, partial)
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
	if partial {
		if checks, err = selectChecks(checks, options.CheckNames, v.Session); err != nil {
			return Run{}, err
		}
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
		if err = validateScenarioBinding(check, v); err != nil {
			return r, err
		}
	}
	snapshot := releaseSnapshot{Target: v.Target, Credentials: v.Credentials, Session: v.Session}
	if v.Session != nil {
		setups := 0
		for _, check := range checks {
			if check.Name == v.Session.SetupCheck {
				setups++
				if check.Kind != "scenario" && check.Kind != "browser" {
					return r, errors.New("the session setup check must be a scenario or browser check")
				}
				if v.Session.Kind == "browser" && !hasBrowser(check) {
					return r, errors.New("a browser session setup check needs browser steps")
				}
			}
		}
		if setups != 1 {
			return r, fmt.Errorf("session.setup_check %q must name exactly one enabled check (found %d)", v.Session.SetupCheck, setups)
		}
	}
	if v.Target != nil {
		snapshot.Candidate = prep.Candidate
		if v.Target.Mode == "isolated" {
			config, err := generatedReleaseConfig(v.Target, prep)
			if err != nil {
				return r, err
			}
			snapshot.Config, snapshot.Fixture = config, prep.Fixture.Spec
			r.Release = &ReleaseRecord{Mode: "isolated", CandidateDigest: options.CandidateDigest, BuildID: prep.BuildID,
				SuiteRevision: suiteRevision(v, checks), FixtureRevision: canonicalDigest(prep.Fixture.Spec)}
		} else if options.CandidateDigest != "" {
			r.Release = &ReleaseRecord{Mode: v.Target.Mode, CandidateDigest: options.CandidateDigest, BuildID: prep.BuildID,
				SuiteRevision: suiteRevision(v, checks), FixtureRevision: targetRevision(v.Target)}
		} else if options.SuiteRevision != "" || options.FixtureRevision != "" {
			return r, errors.New("revision pins need a candidate; set target.candidate or pass candidate_digest")
		}
		if r.Release != nil && (options.SuiteRevision != "" && options.SuiteRevision != r.Release.SuiteRevision ||
			options.FixtureRevision != "" && options.FixtureRevision != r.Release.FixtureRevision) {
			return r, fmt.Errorf("revision mismatch: requested suite %s / fixture %s, current suite %s / fixture %s",
				short(options.SuiteRevision), short(options.FixtureRevision), short(r.Release.SuiteRevision), short(r.Release.FixtureRevision))
		}
	} else if v.Release != nil {
		fixture := prep.Fixture
		if !digestPattern.MatchString(options.CandidateDigest) {
			return r, errors.New("release run requires a lowercase SHA-256 candidate_digest")
		}
		if err := checkFixture(fixture, v.Release); err != nil {
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
		snapshot.Config, snapshot.Fixture = v.Release, fixture.Spec
	} else if options.CandidateDigest != "" || options.Candidate != nil || options.SuiteRevision != "" || options.FixtureRevision != "" {
		return r, errors.New("candidate and revision options require a release suite")
	}
	if partial && r.Release != nil {
		r.Release.Partial = true
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

// selectChecks keeps the named checks plus the session setup check. The
// setup check must be enabled; named checks run whether enabled or not.
func selectChecks(all []Check, names []string, session *SessionConfig) ([]Check, error) {
	if len(names) > 50 {
		return nil, errors.New("check_names accepts at most 50 names")
	}
	wanted := map[string]bool{}
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("check_names cannot contain an empty name")
		}
		wanted[name] = true
	}
	found := map[string]bool{}
	var out []Check
	for _, check := range all {
		setup := session != nil && check.Name == session.SetupCheck && check.Enabled
		if wanted[check.Name] || setup {
			out = append(out, check)
			found[check.Name] = true
		}
	}
	var missing []string
	for _, name := range names {
		if !found[name] {
			missing = append(missing, strconv.Quote(name))
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("no check named %s in this suite", strings.Join(missing, ", "))
	}
	return out, nil
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

// finish records a run's outcome. Warnings are kept on the run but never fail it.
func (s store) finish(id int64, message string, warnings ...string) error {
	if warnings == nil {
		warnings = []string{}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var passed, failed, blocked int
	if err = tx.QueryRow(`SELECT COALESCE(sum(status='passed'),0),COALESCE(sum(status!='passed' AND status!='blocked'),0),COALESCE(sum(status='blocked'),0) FROM test_results WHERE project_id=? AND run_id=?`, s.project, id).Scan(&passed, &failed, &blocked); err != nil {
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
	if failed > 0 || blocked > 0 || message != "" || passed == 0 {
		status = "failed"
	}
	if err = changed(tx.Exec(`UPDATE test_runs SET status=?,passed=?,failed=?,blocked=?,error=?,finished_at=?,warnings=? WHERE project_id=? AND id=? AND status='running'`, status, passed, failed, blocked, message, now(), encode(warnings), s.project, id)); err != nil {
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
