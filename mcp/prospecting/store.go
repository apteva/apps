package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type scanner interface {
	Scan(dest ...any) error
}

func createProfile(db *sql.DB, pid string, args map[string]any) (*TargetProfile, error) {
	p := &TargetProfile{
		ProjectID:    pid,
		Name:         stringArg(args, "name"),
		Description:  stringArg(args, "description"),
		Industries:   stringSliceArg(args, "industries"),
		Locations:    stringSliceArg(args, "locations"),
		EmployeeMin:  optionalIntArg(args, "employee_min"),
		EmployeeMax:  optionalIntArg(args, "employee_max"),
		TargetTitles: stringSliceArg(args, "target_titles"),
		Keywords:     stringSliceArg(args, "keywords"),
		Status:       "active",
	}
	if err := validateProfile(p); err != nil {
		return nil, err
	}
	now := nowUTC()
	result, err := db.Exec(`INSERT INTO target_profiles
        (project_id,name,description,industries_json,locations_json,employee_min,employee_max,target_titles_json,keywords_json,status,created_at,updated_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		pid, p.Name, p.Description, mustJSON(p.Industries), mustJSON(p.Locations), nullableInt(p.EmployeeMin), nullableInt(p.EmployeeMax),
		mustJSON(p.TargetTitles), mustJSON(p.Keywords), p.Status, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("a target profile named %q already exists", p.Name)
		}
		return nil, err
	}
	id, _ := result.LastInsertId()
	return getProfile(db, pid, id)
}

func listProfiles(db *sql.DB, pid, status string) ([]TargetProfile, error) {
	query := `SELECT id,project_id,name,description,industries_json,locations_json,employee_min,employee_max,target_titles_json,keywords_json,status,created_at,updated_at,COALESCE(archived_at,'')
        FROM target_profiles WHERE project_id=?`
	args := []any{pid}
	if status == "" {
		status = "active"
	}
	if status != "all" {
		if status != "active" && status != "archived" {
			return nil, errors.New("status must be active, archived, or all")
		}
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY updated_at DESC, id DESC`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TargetProfile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func getProfile(db *sql.DB, pid string, id int64) (*TargetProfile, error) {
	if id <= 0 {
		return nil, errors.New("profile id required")
	}
	row := db.QueryRow(`SELECT id,project_id,name,description,industries_json,locations_json,employee_min,employee_max,target_titles_json,keywords_json,status,created_at,updated_at,COALESCE(archived_at,'')
        FROM target_profiles WHERE project_id=? AND id=?`, pid, id)
	p, err := scanProfile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func scanProfile(row scanner) (*TargetProfile, error) {
	var p TargetProfile
	var industries, locations, titles, keywords string
	var min, max sql.NullInt64
	err := row.Scan(&p.ID, &p.ProjectID, &p.Name, &p.Description, &industries, &locations, &min, &max, &titles, &keywords,
		&p.Status, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	if err != nil {
		return nil, err
	}
	p.Industries = decodeStrings(industries)
	p.Locations = decodeStrings(locations)
	p.TargetTitles = decodeStrings(titles)
	p.Keywords = decodeStrings(keywords)
	if min.Valid {
		v := int(min.Int64)
		p.EmployeeMin = &v
	}
	if max.Valid {
		v := int(max.Int64)
		p.EmployeeMax = &v
	}
	return &p, nil
}

func updateProfile(db *sql.DB, pid string, id int64, args map[string]any) (*TargetProfile, error) {
	p, err := getProfile(db, pid, id)
	if err != nil || p == nil {
		if err == nil {
			err = sql.ErrNoRows
		}
		return nil, err
	}
	if _, ok := args["name"]; ok {
		p.Name = stringArg(args, "name")
	}
	if _, ok := args["description"]; ok {
		p.Description = stringArg(args, "description")
	}
	if _, ok := args["industries"]; ok {
		p.Industries = stringSliceArg(args, "industries")
	}
	if _, ok := args["locations"]; ok {
		p.Locations = stringSliceArg(args, "locations")
	}
	if _, ok := args["employee_min"]; ok {
		p.EmployeeMin = optionalIntArg(args, "employee_min")
	}
	if _, ok := args["employee_max"]; ok {
		p.EmployeeMax = optionalIntArg(args, "employee_max")
	}
	if _, ok := args["target_titles"]; ok {
		p.TargetTitles = stringSliceArg(args, "target_titles")
	}
	if _, ok := args["keywords"]; ok {
		p.Keywords = stringSliceArg(args, "keywords")
	}
	if err := validateProfile(p); err != nil {
		return nil, err
	}
	_, err = db.Exec(`UPDATE target_profiles SET name=?,description=?,industries_json=?,locations_json=?,employee_min=?,employee_max=?,target_titles_json=?,keywords_json=?,updated_at=?
        WHERE project_id=? AND id=?`, p.Name, p.Description, mustJSON(p.Industries), mustJSON(p.Locations), nullableInt(p.EmployeeMin), nullableInt(p.EmployeeMax),
		mustJSON(p.TargetTitles), mustJSON(p.Keywords), nowUTC(), pid, id)
	if err != nil {
		return nil, err
	}
	return getProfile(db, pid, id)
}

func archiveProfile(db *sql.DB, pid string, id int64) (*TargetProfile, error) {
	now := nowUTC()
	result, err := db.Exec(`UPDATE target_profiles SET status='archived',archived_at=?,updated_at=? WHERE project_id=? AND id=?`, now, now, pid, id)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	return getProfile(db, pid, id)
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func startSearchRun(db *sql.DB, pid string, profileID int64, query string, limit int) (*SearchRun, error) {
	now := nowUTC()
	result, err := db.Exec(`INSERT INTO search_runs
        (project_id,profile_id,query,source,status,requested_limit,started_at,created_at)
        VALUES (?,?,?,'web','running',?,?,?)`, pid, profileID, query, limit, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return getSearchRun(db, pid, id)
}

func finishSearchRun(db *sql.DB, pid string, id int64, status string, count int, errText string) error {
	_, err := db.Exec(`UPDATE search_runs SET status=?,result_count=?,error=?,completed_at=? WHERE project_id=? AND id=?`,
		status, count, truncate(errText, 2000), nowUTC(), pid, id)
	return err
}

func getSearchRun(db *sql.DB, pid string, id int64) (*SearchRun, error) {
	var r SearchRun
	err := db.QueryRow(`SELECT id,project_id,profile_id,query,source,status,requested_limit,result_count,error,started_at,COALESCE(completed_at,''),created_at
        FROM search_runs WHERE project_id=? AND id=?`, pid, id).Scan(
		&r.ID, &r.ProjectID, &r.ProfileID, &r.Query, &r.Source, &r.Status, &r.RequestedLimit, &r.ResultCount, &r.Error, &r.StartedAt, &r.CompletedAt, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

func listSearchRuns(db *sql.DB, pid string, profileID int64, limit int) ([]SearchRun, error) {
	limit = clamp(limit, 1, 200)
	query := `SELECT id,project_id,profile_id,query,source,status,requested_limit,result_count,error,started_at,COALESCE(completed_at,''),created_at
        FROM search_runs WHERE project_id=?`
	args := []any{pid}
	if profileID > 0 {
		query += ` AND profile_id=?`
		args = append(args, profileID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchRun
	for rows.Next() {
		var r SearchRun
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.ProfileID, &r.Query, &r.Source, &r.Status, &r.RequestedLimit, &r.ResultCount, &r.Error, &r.StartedAt, &r.CompletedAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func insertCandidate(db *sql.DB, pid string, input candidateInput, profile *TargetProfile) (*Candidate, bool, error) {
	input.CompanyName = strings.TrimSpace(input.CompanyName)
	if input.CompanyName == "" {
		return nil, false, errors.New("company_name required")
	}
	website, websiteDomain := normalizeWebsite(input.Website)
	if website != "" {
		input.Website = website
	}
	if input.CompanyDomain == "" {
		input.CompanyDomain = websiteDomain
	}
	input.CompanyDomain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(input.CompanyDomain), "www."))
	input.Email = normalizeEmail(input.Email)
	input.Phone = normalizePhone(input.Phone)
	if input.PersonDisplayName == "" {
		input.PersonDisplayName = strings.TrimSpace(input.PersonFirstName + " " + input.PersonLastName)
	}
	if input.Source == "" {
		input.Source = "manual"
	}
	if input.CanonicalKey == "" {
		input.CanonicalKey = canonicalCandidateKey(input.CompanyDomain, input.Website, input.CompanyName, input.SourceURL)
	}
	candidateForScore := &Candidate{
		CompanyName: input.CompanyName, CompanyDomain: input.CompanyDomain, Website: input.Website,
		PersonFirstName: input.PersonFirstName, PersonLastName: input.PersonLastName, PersonDisplayName: input.PersonDisplayName,
		JobTitle: input.JobTitle, Email: input.Email, Phone: input.Phone, Summary: input.Summary, SourceURL: input.SourceURL,
	}
	fit, confidence, reasons := scoreCandidate(profile, candidateForScore, 0)
	now := nowUTC()
	result, err := db.Exec(`INSERT OR IGNORE INTO candidates
        (project_id,profile_id,run_id,canonical_key,company_name,company_domain,website,person_first_name,person_last_name,person_display_name,job_title,email,phone,summary,fit_score,confidence_score,score_reasons_json,status,source,source_url,created_at,updated_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'ready',?,?,?,?)`,
		pid, input.ProfileID, nullableInt64(input.RunID), input.CanonicalKey, input.CompanyName, input.CompanyDomain, input.Website,
		input.PersonFirstName, input.PersonLastName, input.PersonDisplayName, input.JobTitle, input.Email, input.Phone, truncate(input.Summary, 5000),
		fit, confidence, mustJSON(reasons), input.Source, input.SourceURL, now, now)
	if err != nil {
		return nil, false, err
	}
	created := false
	if n, _ := result.RowsAffected(); n > 0 {
		created = true
	}
	c, err := getCandidateByKey(db, pid, input.ProfileID, input.CanonicalKey)
	return c, created, err
}

func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func getCandidateByKey(db *sql.DB, pid string, profileID int64, key string) (*Candidate, error) {
	return scanCandidateRow(db.QueryRow(candidateSelect+` WHERE project_id=? AND profile_id=? AND canonical_key=?`, pid, profileID, key))
}

const candidateSelect = `SELECT id,project_id,profile_id,run_id,canonical_key,company_name,company_domain,website,
    person_first_name,person_last_name,person_display_name,job_title,email,phone,summary,
    location,employee_estimate,location_count,eligibility,eligibility_reasons_json,automation_signals_json,
    fit_score,confidence_score,score_reasons_json,status,source,source_url,decision_reason,crm_contact_id,
    COALESCE(researched_at,''),COALESCE(accepted_at,''),COALESCE(rejected_at,''),COALESCE(deferred_at,''),COALESCE(enriched_at,''),created_at,updated_at FROM candidates`

func getCandidate(db *sql.DB, pid string, id int64) (*Candidate, error) {
	c, err := scanCandidateRow(db.QueryRow(candidateSelect+` WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func scanCandidateRow(row scanner) (*Candidate, error) {
	var c Candidate
	var runID, crmID, employeeEstimate sql.NullInt64
	var reasons, eligibilityReasons, automationSignals string
	err := row.Scan(&c.ID, &c.ProjectID, &c.ProfileID, &runID, &c.CanonicalKey, &c.CompanyName, &c.CompanyDomain, &c.Website,
		&c.PersonFirstName, &c.PersonLastName, &c.PersonDisplayName, &c.JobTitle, &c.Email, &c.Phone, &c.Summary,
		&c.Location, &employeeEstimate, &c.LocationCount, &c.Eligibility, &eligibilityReasons, &automationSignals,
		&c.FitScore, &c.ConfidenceScore, &reasons, &c.Status, &c.Source, &c.SourceURL, &c.DecisionReason, &crmID,
		&c.ResearchedAt, &c.AcceptedAt, &c.RejectedAt, &c.DeferredAt, &c.EnrichedAt, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if runID.Valid {
		v := runID.Int64
		c.RunID = &v
	}
	if crmID.Valid {
		v := crmID.Int64
		c.CRMContactID = &v
	}
	if employeeEstimate.Valid {
		v := int(employeeEstimate.Int64)
		c.EmployeeEstimate = &v
	}
	if !json.Valid([]byte(reasons)) {
		reasons = "[]"
	}
	c.ScoreReasons = json.RawMessage(reasons)
	if err := json.Unmarshal([]byte(eligibilityReasons), &c.EligibilityReasons); err != nil || c.EligibilityReasons == nil {
		c.EligibilityReasons = []string{}
	}
	if err := json.Unmarshal([]byte(automationSignals), &c.AutomationSignals); err != nil || c.AutomationSignals == nil {
		c.AutomationSignals = []AutomationSignal{}
	}
	return &c, nil
}

func listCandidates(db *sql.DB, pid string, filter candidateFilter) ([]Candidate, int, error) {
	filter.Limit = clamp(filter.Limit, 1, 200)
	filter.Offset = clamp(filter.Offset, 0, 1000000)
	where := []string{"project_id=?"}
	args := []any{pid}
	if filter.ProfileID > 0 {
		where = append(where, "profile_id=?")
		args = append(args, filter.ProfileID)
	}
	if filter.Status != "" && filter.Status != "all" {
		where = append(where, "status=?")
		args = append(args, filter.Status)
	}
	if filter.Q != "" {
		like := "%" + escapeLike(strings.ToLower(filter.Q)) + "%"
		where = append(where, `(LOWER(company_name) LIKE ? ESCAPE '\' OR LOWER(company_domain) LIKE ? ESCAPE '\' OR LOWER(person_display_name) LIKE ? ESCAPE '\' OR LOWER(email) LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like, like)
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM candidates WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	queryArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)
	rows, err := db.Query(candidateSelect+` WHERE `+clause+` ORDER BY updated_at DESC,id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		c, err := scanCandidateRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *c)
	}
	return out, total, rows.Err()
}

func updateCandidate(db *sql.DB, pid string, id int64, args map[string]any) (*Candidate, error) {
	c, err := getCandidate(db, pid, id)
	if err != nil || c == nil {
		if err == nil {
			err = sql.ErrNoRows
		}
		return nil, err
	}
	setString := func(key string, dest *string) {
		if _, ok := args[key]; ok {
			*dest = stringArg(args, key)
		}
	}
	setString("company_name", &c.CompanyName)
	setString("company_domain", &c.CompanyDomain)
	setString("website", &c.Website)
	setString("person_first_name", &c.PersonFirstName)
	setString("person_last_name", &c.PersonLastName)
	setString("person_display_name", &c.PersonDisplayName)
	setString("job_title", &c.JobTitle)
	setString("email", &c.Email)
	setString("phone", &c.Phone)
	setString("summary", &c.Summary)
	setString("source_url", &c.SourceURL)
	if c.CompanyName == "" {
		return nil, errors.New("company_name required")
	}
	if website, domain := normalizeWebsite(c.Website); website != "" {
		c.Website = website
		if c.CompanyDomain == "" {
			c.CompanyDomain = domain
		}
	}
	c.CompanyDomain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(c.CompanyDomain), "www."))
	if _, ok := args["email"]; ok && stringArg(args, "email") != "" && normalizeEmail(stringArg(args, "email")) == "" {
		return nil, errors.New("email is invalid")
	}
	c.Email = normalizeEmail(c.Email)
	if _, ok := args["phone"]; ok && stringArg(args, "phone") != "" && normalizePhone(stringArg(args, "phone")) == "" {
		return nil, errors.New("phone is invalid")
	}
	c.Phone = normalizePhone(c.Phone)
	if c.PersonDisplayName == "" {
		c.PersonDisplayName = strings.TrimSpace(c.PersonFirstName + " " + c.PersonLastName)
	}
	p, err := getProfile(db, pid, c.ProfileID)
	if err != nil || p == nil {
		return nil, fmt.Errorf("load target profile: %w", err)
	}
	evidenceCount, _ := countEvidence(db, pid, c.ID)
	fit, confidence, reasons := scoreCandidate(p, c, evidenceCount)
	_, err = db.Exec(`UPDATE candidates SET company_name=?,company_domain=?,website=?,person_first_name=?,person_last_name=?,person_display_name=?,job_title=?,email=?,phone=?,summary=?,source_url=?,fit_score=?,confidence_score=?,score_reasons_json=?,updated_at=? WHERE project_id=? AND id=?`,
		c.CompanyName, c.CompanyDomain, c.Website, c.PersonFirstName, c.PersonLastName, c.PersonDisplayName, c.JobTitle, c.Email, c.Phone,
		truncate(c.Summary, 5000), c.SourceURL, fit, confidence, mustJSON(reasons), nowUTC(), pid, id)
	if err != nil {
		return nil, err
	}
	return getCandidate(db, pid, id)
}

func saveCandidateQualification(db *sql.DB, pid string, candidate *Candidate) (*Candidate, error) {
	if candidate == nil {
		return nil, errors.New("candidate required")
	}
	profile, err := getProfile(db, pid, candidate.ProfileID)
	if err != nil || profile == nil {
		return nil, fmt.Errorf("load target profile: %w", err)
	}
	evidenceCount, _ := countEvidence(db, pid, candidate.ID)
	fit, confidence, reasons := scoreCandidate(profile, candidate, evidenceCount)
	now := nowUTC()
	status := candidate.Status
	decisionReason := candidate.DecisionReason
	rejectedAt := candidate.RejectedAt
	if candidate.Eligibility == "ineligible" && status != "accepted" {
		status = "rejected"
		decisionReason = "Deterministic qualification: " + strings.Join(candidate.EligibilityReasons, "; ")
		rejectedAt = now
	} else if status == "discovered" || status == "researching" {
		status = "ready"
	}
	_, err = db.Exec(`UPDATE candidates SET
        company_name=?,company_domain=?,website=?,person_first_name=?,person_last_name=?,person_display_name=?,job_title=?,email=?,phone=?,summary=?,
        location=?,employee_estimate=?,location_count=?,eligibility=?,eligibility_reasons_json=?,automation_signals_json=?,
        fit_score=?,confidence_score=?,score_reasons_json=?,status=?,decision_reason=?,rejected_at=?,enriched_at=?,updated_at=?
        WHERE project_id=? AND id=?`,
		candidate.CompanyName, candidate.CompanyDomain, candidate.Website, candidate.PersonFirstName, candidate.PersonLastName,
		candidate.PersonDisplayName, candidate.JobTitle, normalizeEmail(candidate.Email), normalizePhone(candidate.Phone), truncate(candidate.Summary, 5000),
		candidate.Location, nullableInt(candidate.EmployeeEstimate), candidate.LocationCount, defaultString(candidate.Eligibility, "review"),
		mustJSON(candidate.EligibilityReasons), mustJSON(candidate.AutomationSignals), fit, confidence, mustJSON(reasons), status,
		truncate(decisionReason, 1000), nullableText(rejectedAt), now, now, pid, candidate.ID)
	if err != nil {
		return nil, err
	}
	return getCandidate(db, pid, candidate.ID)
}

func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func setCandidateResearch(db *sql.DB, pid string, candidate *Candidate, summary string, evidenceCount int) (*Candidate, error) {
	p, err := getProfile(db, pid, candidate.ProfileID)
	if err != nil || p == nil {
		return nil, fmt.Errorf("load target profile: %w", err)
	}
	candidate.Summary = truncate(summary, 5000)
	fit, confidence, reasons := scoreCandidate(p, candidate, evidenceCount)
	now := nowUTC()
	_, err = db.Exec(`UPDATE candidates SET summary=?,fit_score=?,confidence_score=?,score_reasons_json=?,status='ready',researched_at=?,updated_at=? WHERE project_id=? AND id=?`,
		candidate.Summary, fit, confidence, mustJSON(reasons), now, now, pid, candidate.ID)
	if err != nil {
		return nil, err
	}
	return getCandidate(db, pid, candidate.ID)
}

func setCandidateDecision(db *sql.DB, pid string, id int64, status, reason string) (*Candidate, error) {
	if status != "deferred" && status != "rejected" {
		return nil, errors.New("unsupported decision status")
	}
	now := nowUTC()
	column := "deferred_at"
	if status == "rejected" {
		column = "rejected_at"
	}
	result, err := db.Exec(`UPDATE candidates SET status=?,decision_reason=?,`+column+`=?,updated_at=? WHERE project_id=? AND id=? AND status!='accepted'`,
		status, truncate(reason, 1000), now, now, pid, id)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, errors.New("candidate not found or already accepted")
	}
	return getCandidate(db, pid, id)
}

func addEvidence(db *sql.DB, pid string, evidence Evidence) error {
	if strings.TrimSpace(evidence.URL) == "" {
		return nil
	}
	_, err := db.Exec(`INSERT INTO candidate_evidence
        (project_id,candidate_id,source_kind,title,url,excerpt,artifact_id,retrieved_at)
        VALUES (?,?,?,?,?,?,?,?)
        ON CONFLICT(project_id,candidate_id,url) DO UPDATE SET title=excluded.title,excerpt=excluded.excerpt,artifact_id=COALESCE(excluded.artifact_id,candidate_evidence.artifact_id),retrieved_at=excluded.retrieved_at`,
		pid, evidence.CandidateID, defaultString(evidence.SourceKind, "web"), evidence.Title, evidence.URL, truncate(evidence.Excerpt, 3000), nullableInt64(evidence.ArtifactID), evidence.RetrievedAt)
	return err
}

func listEvidence(db *sql.DB, pid string, candidateID int64) ([]Evidence, error) {
	rows, err := db.Query(`SELECT id,project_id,candidate_id,source_kind,title,url,excerpt,artifact_id,retrieved_at
        FROM candidate_evidence WHERE project_id=? AND candidate_id=? ORDER BY retrieved_at DESC,id DESC`, pid, candidateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Evidence
	for rows.Next() {
		var e Evidence
		var artifact sql.NullInt64
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.CandidateID, &e.SourceKind, &e.Title, &e.URL, &e.Excerpt, &artifact, &e.RetrievedAt); err != nil {
			return nil, err
		}
		if artifact.Valid {
			v := artifact.Int64
			e.ArtifactID = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func countEvidence(db *sql.DB, pid string, candidateID int64) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM candidate_evidence WHERE project_id=? AND candidate_id=?`, pid, candidateID).Scan(&count)
	return count, err
}

func addExclusion(db *sql.DB, pid, kind, value, reason string) (*Exclusion, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	value = normalizeExclusionValue(kind, value)
	if value == "" {
		return nil, errors.New("exclusion value required")
	}
	if kind != "domain" && kind != "company" && kind != "email" && kind != "phone" {
		return nil, errors.New("exclusion kind must be domain, company, email, or phone")
	}
	now := nowUTC()
	_, err := db.Exec(`INSERT INTO exclusions(project_id,kind,value,reason,created_at) VALUES (?,?,?,?,?)
        ON CONFLICT(project_id,kind,value) DO UPDATE SET reason=excluded.reason`, pid, kind, value, truncate(reason, 1000), now)
	if err != nil {
		return nil, err
	}
	var e Exclusion
	err = db.QueryRow(`SELECT id,project_id,kind,value,reason,created_at FROM exclusions WHERE project_id=? AND kind=? AND value=?`, pid, kind, value).
		Scan(&e.ID, &e.ProjectID, &e.Kind, &e.Value, &e.Reason, &e.CreatedAt)
	return &e, err
}

func normalizeExclusionValue(kind, value string) string {
	value = strings.TrimSpace(value)
	switch kind {
	case "domain":
		if _, domain := normalizeWebsite(value); domain != "" {
			return domain
		}
		return strings.ToLower(strings.TrimPrefix(value, "www."))
	case "company":
		return strings.ToLower(value)
	case "email":
		return normalizeEmail(value)
	case "phone":
		return normalizePhone(value)
	default:
		return value
	}
}

func isExcluded(db *sql.DB, pid string, input candidateInput) (bool, error) {
	checks := [][2]string{
		{"domain", normalizeExclusionValue("domain", input.CompanyDomain)},
		{"company", normalizeExclusionValue("company", input.CompanyName)},
		{"email", normalizeExclusionValue("email", input.Email)},
		{"phone", normalizeExclusionValue("phone", input.Phone)},
	}
	for _, check := range checks {
		if check[1] == "" {
			continue
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM exclusions WHERE project_id=? AND kind=? AND value=?`, pid, check[0], check[1]).Scan(&n); err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

func listExclusions(db *sql.DB, pid, kind string, limit int) ([]Exclusion, error) {
	limit = clamp(limit, 1, 500)
	query := `SELECT id,project_id,kind,value,reason,created_at FROM exclusions WHERE project_id=?`
	args := []any{pid}
	if kind != "" {
		query += ` AND kind=?`
		args = append(args, kind)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Exclusion
	for rows.Next() {
		var e Exclusion
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Kind, &e.Value, &e.Reason, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func removeExclusion(db *sql.DB, pid string, id int64) error {
	result, err := db.Exec(`DELETE FROM exclusions WHERE project_id=? AND id=?`, pid, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func getHandoff(db *sql.DB, pid string, candidateID int64) (*Handoff, error) {
	var h Handoff
	var created int
	err := db.QueryRow(`SELECT id,project_id,candidate_id,crm_contact_id,channel_kind,channel_value,was_created,activity_warning,created_at
        FROM crm_handoffs WHERE project_id=? AND candidate_id=?`, pid, candidateID).
		Scan(&h.ID, &h.ProjectID, &h.CandidateID, &h.CRMContactID, &h.ChannelKind, &h.ChannelValue, &created, &h.ActivityWarning, &h.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	h.WasCreated = created != 0
	return &h, err
}

func saveHandoff(db *sql.DB, pid string, candidateID, crmContactID int64, kind, value string, wasCreated bool, warning string) (*Handoff, error) {
	now := nowUTC()
	created := 0
	if wasCreated {
		created = 1
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO crm_handoffs(project_id,candidate_id,crm_contact_id,channel_kind,channel_value,was_created,activity_warning,created_at)
        VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(project_id,candidate_id) DO NOTHING`, pid, candidateID, crmContactID, kind, value, created, warning, now)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`UPDATE candidates SET status='accepted',crm_contact_id=?,accepted_at=COALESCE(accepted_at,?),updated_at=? WHERE project_id=? AND id=?`,
		crmContactID, now, now, pid, candidateID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getHandoff(db, pid, candidateID)
}

func overview(db *sql.DB, pid string) (map[string]any, error) {
	var profiles, runs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM target_profiles WHERE project_id=? AND status='active'`, pid).Scan(&profiles); err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM search_runs WHERE project_id=?`, pid).Scan(&runs); err != nil {
		return nil, err
	}
	statuses := map[string]int{}
	rows, err := db.Query(`SELECT status,COUNT(*) FROM candidates WHERE project_id=? GROUP BY status`, pid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return nil, err
		}
		statuses[status] = count
	}
	rows.Close()
	qualifications := map[string]int{}
	rows, err = db.Query(`SELECT eligibility,COUNT(*) FROM candidates WHERE project_id=? GROUP BY eligibility`, pid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var eligibility string
		var count int
		if err := rows.Scan(&eligibility, &count); err != nil {
			rows.Close()
			return nil, err
		}
		qualifications[eligibility] = count
	}
	rows.Close()
	var evidence, exclusions, enriched int
	_ = db.QueryRow(`SELECT COUNT(*) FROM candidate_evidence WHERE project_id=?`, pid).Scan(&evidence)
	_ = db.QueryRow(`SELECT COUNT(*) FROM exclusions WHERE project_id=?`, pid).Scan(&exclusions)
	_ = db.QueryRow(`SELECT COUNT(*) FROM candidates WHERE project_id=? AND enriched_at IS NOT NULL AND enriched_at<>''`, pid).Scan(&enriched)
	return map[string]any{
		"active_profiles": profiles,
		"search_runs":     runs,
		"candidates":      statuses,
		"qualifications":  qualifications,
		"enriched":        enriched,
		"evidence":        evidence,
		"exclusions":      exclusions,
	}, nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
