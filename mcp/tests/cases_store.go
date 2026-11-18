package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	recentHistory      = 20
	caseRetentionDays  = 30
	maxQuarantineDays  = 90
	defaultQuarantineD = 14
	maxTextArtifacts   = 30
)

var statusLetter = map[string]string{"passed": "p", "failed": "f", "error": "e", "skipped": "s", "flaky": "y"}

// saveCases stores a code check's cases and folds them into per-case history.
func (s store) saveCases(suiteID, runID, checkID int64, cases []TestCase, secrets []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	insert, err := tx.Prepare(`INSERT INTO test_cases(project_id,suite_id,run_id,check_id,case_key,file,suite,name,status,quarantined,duration_ms,message,stack,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer insert.Close()
	history, err := tx.Prepare(`INSERT INTO test_case_history(project_id,suite_id,case_key,file,name,runs,failures,flaky,recent,last_status,last_run_id,last_failure_at,last_failure_message,updated_at)
		VALUES(?,?,?,?,?,1,?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id,suite_id,case_key) DO UPDATE SET
		 file=excluded.file, name=excluded.name, runs=runs+1, failures=failures+excluded.failures, flaky=flaky+excluded.flaky,
		 recent=substr(recent||excluded.recent, -` + strconv.Itoa(recentHistory) + `), last_status=excluded.last_status, last_run_id=excluded.last_run_id,
		 last_failure_at=CASE WHEN excluded.failures>0 THEN excluded.last_failure_at ELSE last_failure_at END,
		 last_failure_message=CASE WHEN excluded.failures>0 THEN excluded.last_failure_message ELSE last_failure_message END,
		 updated_at=excluded.updated_at`)
	if err != nil {
		return err
	}
	defer history.Close()
	at := now()
	for _, c := range cases {
		message, stack := scrubText(c.Message, secrets), scrubText(c.Stack, secrets)
		if _, err = insert.Exec(s.project, suiteID, runID, checkID, c.Key, c.File, c.Suite, c.Name, c.Status, c.Quarantined, c.DurationMS, message, stack, at); err != nil {
			return err
		}
		failures, flaky, failedAt, failure := 0, 0, "", ""
		if failing(c) {
			failures, failedAt, failure = 1, at, truncateText(message, 2000)
		}
		if c.Status == "flaky" {
			flaky = 1
		}
		if _, err = history.Exec(s.project, suiteID, c.Key, c.File, c.Name, failures, flaky, statusLetter[c.Status], c.Status, runID, failedAt, failure, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// activeQuarantine returns the suite's unexpired quarantined case keys.
func (s store) activeQuarantine(suiteID int64) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT case_key,reason FROM test_quarantine WHERE project_id=? AND suite_id=? AND expires_at>?`, s.project, suiteID, now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, reason string
		if err = rows.Scan(&key, &reason); err != nil {
			return nil, err
		}
		out[key] = reason
	}
	return out, rows.Err()
}

// previousCoverage is the line coverage of the same check in an earlier run.
func (s store) previousCoverage(suiteID int64, check string, before int64) (float64, bool) {
	var pct sql.NullFloat64
	err := s.db.QueryRow(`SELECT json_extract(r.output,'$.coverage.lines_pct') FROM test_results r JOIN test_runs t ON t.project_id=r.project_id AND t.id=r.run_id
		WHERE r.project_id=? AND t.suite_id=? AND r.name=? AND r.kind='code' AND r.run_id<? AND json_extract(r.output,'$.coverage.lines_pct') IS NOT NULL
		ORDER BY r.run_id DESC LIMIT 1`, s.project, suiteID, check, before).Scan(&pct)
	return pct.Float64, err == nil && pct.Valid
}

// saveTextArtifact keeps a report or log next to a run. It is served as
// plain text.
func saveTextArtifact(app *sdk.AppCtx, run, check int64, step string, data []byte) (int64, error) {
	if len(data) == 0 || len(data) > maxTextArtifact {
		return 0, errors.New("text artifact is empty or exceeds 1 MB")
	}
	var count int
	if err := app.AppDB().QueryRow(`SELECT count(*) FROM test_artifacts WHERE project_id=? AND run_id=? AND mime!='image/png'`, app.CurrentProject(), run).Scan(&count); err != nil {
		return 0, err
	}
	if count >= maxTextArtifacts {
		return 0, errors.New("run report limit reached")
	}
	digest := sha256.Sum256(data)
	sum := hex.EncodeToString(digest[:])
	result, err := app.AppDB().Exec(`INSERT INTO test_artifacts(project_id,run_id,check_id,step_id,mime,digest,bytes,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		app.CurrentProject(), run, check, step, "text/plain", sum, data, now())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// caseFilter selects rows for tests_cases_list.
type caseFilter struct {
	RunID   int64  `json:"run_id"`
	CheckID int64  `json:"check_id"`
	SuiteID int64  `json:"suite_id"`
	Status  string `json:"status"`
	File    string `json:"file"`
	CaseKey string `json:"case_key"`
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
}

type caseRow struct {
	TestCase
	SuiteID   int64  `json:"suite_id"`
	RunID     int64  `json:"run_id"`
	CheckID   int64  `json:"check_id"`
	CreatedAt string `json:"created_at"`
}

func (s store) cases(f caseFilter) ([]caseRow, error) {
	if f.RunID == 0 && f.SuiteID == 0 {
		return nil, errors.New("run_id or suite_id required")
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	switch f.Status {
	case "", "passed", "failed", "error", "skipped", "flaky":
	case "failing":
	default:
		return nil, errors.New("status must be passed, failed, error, skipped, flaky or failing")
	}
	query := `SELECT suite_id,run_id,check_id,case_key,file,suite,name,status,quarantined,duration_ms,message,stack,created_at FROM test_cases WHERE project_id=?`
	args := []any{s.project}
	if f.RunID > 0 {
		query += ` AND run_id=?`
		args = append(args, f.RunID)
	}
	if f.SuiteID > 0 {
		query += ` AND suite_id=?`
		args = append(args, f.SuiteID)
	}
	if f.CheckID > 0 {
		query += ` AND check_id=?`
		args = append(args, f.CheckID)
	}
	if f.Status == "failing" {
		query += ` AND status IN ('failed','error')`
	} else if f.Status != "" {
		query += ` AND status=?`
		args = append(args, f.Status)
	}
	if f.File != "" {
		query += ` AND file=?`
		args = append(args, f.File)
	}
	if f.CaseKey != "" {
		query += ` AND case_key=?`
		args = append(args, f.CaseKey)
	}
	query += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []caseRow{}
	for rows.Next() {
		var r caseRow
		if err = rows.Scan(&r.SuiteID, &r.RunID, &r.CheckID, &r.Key, &r.File, &r.Suite, &r.Name, &r.Status, &r.Quarantined, &r.DurationMS, &r.Message, &r.Stack, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type caseHistory struct {
	CaseKey            string  `json:"case_key"`
	File               string  `json:"file,omitempty"`
	Name               string  `json:"name"`
	Runs               int     `json:"runs"`
	Failures           int     `json:"failures"`
	Flaky              int     `json:"flaky"`
	FailureRate        float64 `json:"failure_rate"`
	Recent             string  `json:"recent"`
	LastStatus         string  `json:"last_status"`
	LastRunID          int64   `json:"last_run_id"`
	LastFailureAt      string  `json:"last_failure_at,omitempty"`
	LastFailureMessage string  `json:"last_failure_message,omitempty"`
	Quarantined        bool    `json:"quarantined"`
	QuarantineReason   string  `json:"quarantine_reason,omitempty"`
}

// caseHistories lists per-case history for a suite; "unstable" keeps cases
// that failed or flaked at least once.
func (s store) caseHistories(suiteID int64, caseKey string, unstable bool, limit int) ([]caseHistory, error) {
	if suiteID < 1 {
		return nil, errors.New("suite_id required")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT h.case_key,h.file,h.name,h.runs,h.failures,h.flaky,h.recent,h.last_status,h.last_run_id,h.last_failure_at,h.last_failure_message,
		COALESCE(q.reason,'') FROM test_case_history h LEFT JOIN test_quarantine q ON q.project_id=h.project_id AND q.suite_id=h.suite_id AND q.case_key=h.case_key AND q.expires_at>?
		WHERE h.project_id=? AND h.suite_id=?`
	args := []any{now(), s.project, suiteID}
	if caseKey != "" {
		query += ` AND h.case_key=?`
		args = append(args, caseKey)
	}
	if unstable {
		query += ` AND (h.failures>0 OR h.flaky>0)`
	}
	query += ` ORDER BY (h.failures+h.flaky) DESC, h.updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []caseHistory{}
	for rows.Next() {
		var h caseHistory
		if err = rows.Scan(&h.CaseKey, &h.File, &h.Name, &h.Runs, &h.Failures, &h.Flaky, &h.Recent, &h.LastStatus, &h.LastRunID, &h.LastFailureAt, &h.LastFailureMessage, &h.QuarantineReason); err != nil {
			return nil, err
		}
		h.Quarantined = h.QuarantineReason != ""
		if h.Runs > 0 {
			h.FailureRate = percent(h.Failures, h.Runs)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

type quarantineEntry struct {
	SuiteID   int64  `json:"suite_id"`
	CaseKey   string `json:"case_key"`
	Reason    string `json:"reason"`
	AddedBy   string `json:"added_by,omitempty"`
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
	Expired   bool   `json:"expired,omitempty"`
}

type quarantineRequest struct {
	SuiteID   int64  `json:"suite_id"`
	CaseKey   string `json:"case_key"`
	Reason    string `json:"reason"`
	Days      int    `json:"days"`
	Remove    bool   `json:"remove"`
	ExpiresAt string `json:"expires_at"`
}

// setQuarantine adds, renews or removes a quarantined case. A quarantined
// failure is recorded but does not fail its code check; it always expires.
func (s store) setQuarantine(req quarantineRequest, addedBy string) (*quarantineEntry, error) {
	req.CaseKey = strings.TrimSpace(req.CaseKey)
	if req.SuiteID < 1 || req.CaseKey == "" || len(req.CaseKey) > maxCaseKey {
		return nil, errors.New("suite_id and case_key required")
	}
	if _, err := s.getSuite(req.SuiteID); err != nil {
		return nil, err
	}
	if req.Remove {
		return nil, changed(s.db.Exec(`DELETE FROM test_quarantine WHERE project_id=? AND suite_id=? AND case_key=?`, s.project, req.SuiteID, req.CaseKey))
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if len(req.Reason) < 5 || len(req.Reason) > 1000 {
		return nil, errors.New("reason (5–1000 characters) required: say why the case is quarantined")
	}
	expires := time.Now().UTC()
	switch {
	case req.ExpiresAt != "":
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil || !t.After(expires) || t.After(expires.AddDate(0, 0, maxQuarantineDays)) {
			return nil, errors.New("expires_at must be an RFC 3339 time within the next 90 days")
		}
		expires = t.UTC()
	default:
		if req.Days == 0 {
			req.Days = defaultQuarantineD
		}
		if req.Days < 1 || req.Days > maxQuarantineDays {
			return nil, errors.New("days must be 1–90")
		}
		expires = expires.AddDate(0, 0, req.Days)
	}
	var known int
	if err := s.db.QueryRow(`SELECT count(*) FROM test_case_history WHERE project_id=? AND suite_id=? AND case_key=?`, s.project, req.SuiteID, req.CaseKey).Scan(&known); err != nil {
		return nil, err
	}
	if known == 0 {
		return nil, errors.New("no case with this case_key has run in the suite; copy case_key from tests_cases_list or tests_case_history")
	}
	entry := &quarantineEntry{SuiteID: req.SuiteID, CaseKey: req.CaseKey, Reason: req.Reason, AddedBy: addedBy, ExpiresAt: expires.Format(time.RFC3339Nano), CreatedAt: now()}
	_, err := s.db.Exec(`INSERT INTO test_quarantine(project_id,suite_id,case_key,reason,added_by,expires_at,created_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(project_id,suite_id,case_key) DO UPDATE SET reason=excluded.reason, added_by=excluded.added_by, expires_at=excluded.expires_at`,
		s.project, entry.SuiteID, entry.CaseKey, entry.Reason, entry.AddedBy, entry.ExpiresAt, entry.CreatedAt)
	return entry, err
}

func (s store) quarantineList(suiteID int64, includeExpired bool) ([]quarantineEntry, error) {
	query := `SELECT suite_id,case_key,reason,added_by,expires_at,created_at FROM test_quarantine WHERE project_id=? AND (?=0 OR suite_id=?)`
	args := []any{s.project, suiteID, suiteID}
	if !includeExpired {
		query += ` AND expires_at>?`
		args = append(args, now())
	}
	rows, err := s.db.Query(query+` ORDER BY expires_at LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []quarantineEntry{}
	at := now()
	for rows.Next() {
		var e quarantineEntry
		if err = rows.Scan(&e.SuiteID, &e.CaseKey, &e.Reason, &e.AddedBy, &e.ExpiresAt, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Expired = e.ExpiresAt <= at
		out = append(out, e)
	}
	return out, rows.Err()
}

// pruneCases drops raw case rows after 30 days; history keeps the aggregates.
func pruneCases(app *sdk.AppCtx) error {
	cutoff := time.Now().UTC().AddDate(0, 0, -caseRetentionDays).Format(time.RFC3339Nano)
	_, err := app.AppDB().Exec(`DELETE FROM test_cases WHERE project_id=? AND created_at<?`, app.CurrentProject(), cutoff)
	return err
}
