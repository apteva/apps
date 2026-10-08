package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type runOptions struct {
	ProfileID         int64          `json:"profile_id"`
	Source            string         `json:"source"`
	Query             string         `json:"query"`
	Limit             int            `json:"limit"`
	Qualify           bool           `json:"qualify"`
	CRMMode           string         `json:"crm_mode"`
	MinFit            int            `json:"min_fit_score"`
	MinConfidence     int            `json:"min_confidence_score"`
	ListIDs           []any          `json:"list_ids,omitempty"`
	ConnectionID      int64          `json:"places_connection_id,omitempty"`
	MaxPages          int            `json:"max_pages"`
	MaxPlacesRequests int            `json:"max_places_requests"`
	IncludedType      string         `json:"included_type,omitempty"`
	Bounds            map[string]any `json:"location_restriction,omitempty"`
	Bias              map[string]any `json:"location_bias,omitempty"`
	Engine            string         `json:"engine,omitempty"`
	FallbackEngine    string         `json:"fallback_engine,omitempty"`
}

type runState struct {
	Phase          string         `json:"phase"`
	PageToken      string         `json:"page_token,omitempty"`
	NextPageToken  string         `json:"next_page_token,omitempty"`
	Page           []placeDetails `json:"page,omitempty"`
	Index          int            `json:"index"`
	SearchPages    int            `json:"search_pages"`
	PlacesRequests int            `json:"places_requests"`
}

type prospectingJob struct {
	ID         int64          `json:"id"`
	ProjectID  string         `json:"project_id,omitempty"`
	ProfileID  int64          `json:"profile_id"`
	Status     string         `json:"status"`
	Options    runOptions     `json:"options"`
	State      runState       `json:"progress"`
	Error      string         `json:"error,omitempty"`
	CreatedAt  string         `json:"created_at"`
	UpdatedAt  string         `json:"updated_at"`
	Counts     map[string]int `json:"counts"`
	Items      []runItem      `json:"items"`
	leaseToken string
	leaseUntil int64
}

type runItem struct {
	SourceKey    string `json:"source_key"`
	CandidateID  *int64 `json:"candidate_id,omitempty"`
	WasCreated   bool   `json:"was_created"`
	Stage        string `json:"stage"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	CRMContactID *int64 `json:"crm_contact_id,omitempty"`
}

func newRunKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func optionScore(args map[string]any, key string, fallback int) (int, error) {
	n := fallback
	if _, ok := args[key]; ok {
		n = int(int64Arg(args, key))
	}
	if n < 0 || n > 100 {
		return 0, fmt.Errorf("%s must be between 0 and 100", key)
	}
	return n, nil
}

func (a *App) toolRun(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if ctx.CurrentProject() == "" {
		return nil, errors.New("project_id is required")
	}
	key := stringArg(args, "idempotency_key")
	if len(key) > 128 {
		return nil, errors.New("idempotency_key must be at most 128 characters")
	}
	if key != "" {
		var id int64
		err := ctx.AppDB().QueryRow(`SELECT id FROM prospecting_jobs WHERE project_id=? AND idempotency_key=?`, ctx.CurrentProject(), key).Scan(&id)
		if err == nil {
			j, e := getProspectingJob(ctx, id)
			if e != nil {
				return nil, e
			}
			raw, _ := json.Marshal(j.Options)
			var saved map[string]any
			_ = json.Unmarshal(raw, &saved)
			for field, value := range args {
				if stored, ok := saved[field]; ok {
					if field == "query" && stringArg(args, field) == "" {
						continue
					}
					if mustJSON(value) != mustJSON(stored) {
						return nil, errors.New("idempotency_key was already used with different run options")
					}
				}
			}
			return map[string]any{"run": j, "idempotent": true}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	} else {
		key = newRunKey()
	}
	p, err := getProfile(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "profile_id"))
	if err != nil {
		return nil, err
	}
	if p == nil || p.Status != "active" {
		return nil, errors.New("active target profile required")
	}
	o := runOptions{ProfileID: p.ID, Source: defaultString(stringArg(args, "source"), "web"), Query: stringArg(args, "query"), Limit: intArg(args, "limit", 20), Qualify: boolArg(args, "qualify", true), CRMMode: defaultString(stringArg(args, "crm_mode"), "review"), MaxPages: intArg(args, "max_pages", 2), MaxPlacesRequests: intArg(args, "max_places_requests", 3), IncludedType: stringArg(args, "included_type"), Engine: defaultString(stringArg(args, "engine"), "google"), FallbackEngine: defaultString(stringArg(args, "fallback_engine"), "duckduckgo")}
	if o.Limit < 1 || o.Limit > 20 {
		return nil, errors.New("runs are limited to 1–20 prospects")
	}
	if o.MaxPages < 1 || o.MaxPages > 5 || o.MaxPlacesRequests < 1 || o.MaxPlacesRequests > 10 {
		return nil, errors.New("max_pages must be 1–5 and max_places_requests 1–10")
	}
	if o.CRMMode != "review" && o.CRMMode != "auto" {
		return nil, errors.New("crm_mode must be review or auto")
	}
	o.MinFit, err = optionScore(args, "min_fit_score", 70)
	if err != nil {
		return nil, err
	}
	o.MinConfidence, err = optionScore(args, "min_confidence_score", 60)
	if err != nil {
		return nil, err
	}
	if v, ok := args["list_ids"].([]any); ok {
		o.ListIDs = v
	} else if v, ok := args["list_ids"].([]string); ok {
		for _, s := range v {
			o.ListIDs = append(o.ListIDs, s)
		}
	}
	if o.CRMMode == "auto" {
		if !o.Qualify {
			return nil, errors.New("automatic CRM handoff requires website qualification")
		}
		if err = requireOptionalApp(ctx, "crm"); err != nil {
			return nil, err
		}
	}
	if o.Qualify || o.Source == "web" {
		if err = requireOptionalApp(ctx, "web"); err != nil {
			return nil, err
		}
	}
	switch o.Source {
	case "web":
		if o.Query == "" {
			o.Query = buildSearchQuery(p)
		}
	case "google_places":
		s, e := loadDiscoverySettings(ctx)
		if e != nil {
			return nil, e
		}
		o.ConnectionID = s.PlacesConnectionID
		if o.ConnectionID == 0 {
			return nil, errors.New("connect Google Places in Prospecting settings first")
		}
		if err = validatePlacesConnection(ctx, o.ConnectionID); err != nil {
			return nil, err
		}
		if o.Query == "" {
			o.Query = strings.Join(p.Industries, ", ") + " in " + strings.Join(p.Locations, ", ")
		}
		o.Query = strings.TrimSpace(o.Query)
		if o.Query == "" || o.Query == "in" {
			return nil, errors.New("Places search query or profile industry/location required")
		}
		o.Bounds, _ = args["location_restriction"].(map[string]any)
		o.Bias, _ = args["location_bias"].(map[string]any)
		if o.Bounds != nil && o.Bias != nil {
			return nil, errors.New("use either location_restriction or location_bias")
		}
	default:
		return nil, errors.New("source must be web or google_places")
	}
	now := nowUTC()
	_, err = ctx.AppDB().Exec(`INSERT INTO prospecting_jobs(project_id,profile_id,idempotency_key,options_json,state_json,created_at,updated_at) VALUES (?,?,?,?,?,?,?) ON CONFLICT(project_id,idempotency_key) DO NOTHING`, ctx.CurrentProject(), p.ID, key, mustJSON(o), mustJSON(runState{Phase: "discover"}), now, now)
	if err != nil {
		return nil, err
	}
	var id int64
	err = ctx.AppDB().QueryRow(`SELECT id FROM prospecting_jobs WHERE project_id=? AND idempotency_key=?`, ctx.CurrentProject(), key).Scan(&id)
	if err != nil {
		return nil, err
	}
	j, err := getProspectingJob(ctx, id)
	return map[string]any{"run": j}, err
}

func getProspectingJob(ctx *sdk.AppCtx, id int64) (*prospectingJob, error) {
	j := &prospectingJob{Counts: map[string]int{}, Items: []runItem{}}
	var o, s string
	err := ctx.AppDB().QueryRow(`SELECT id,project_id,profile_id,status,options_json,state_json,error,created_at,updated_at,lease_token,lease_until FROM prospecting_jobs WHERE project_id=? AND id=?`, ctx.CurrentProject(), id).Scan(&j.ID, &j.ProjectID, &j.ProfileID, &j.Status, &o, &s, &j.Error, &j.CreatedAt, &j.UpdatedAt, &j.leaseToken, &j.leaseUntil)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(o), &j.Options); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(s), &j.State); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT i.source_key,i.candidate_id,i.was_created,i.stage,i.status,i.reason,c.crm_contact_id FROM prospecting_job_items i LEFT JOIN candidates c ON c.id=i.candidate_id AND c.project_id=? WHERE i.run_id=? ORDER BY i.rowid`, ctx.CurrentProject(), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i runItem
		if err = rows.Scan(&i.SourceKey, &i.CandidateID, &i.WasCreated, &i.Stage, &i.Status, &i.Reason, &i.CRMContactID); err != nil {
			return nil, err
		}
		j.Items = append(j.Items, i)
		j.Counts["discovered"]++
		j.Counts[i.Status]++
		if i.CandidateID != nil {
			if i.WasCreated {
				j.Counts["created"]++
			} else {
				j.Counts["existing"]++
			}
		}
		if i.Status != "qualified" && (i.Stage == "crm" || i.Status == "transferred") {
			j.Counts["qualified"]++
		}
	}
	return j, rows.Err()
}

func (a *App) toolRunGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	j, e := getProspectingJob(ctx, int64Arg(args, "id"))
	return map[string]any{"run": j}, e
}
func (a *App) toolRunList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := ctx.AppDB().Query(`SELECT id FROM prospecting_jobs WHERE project_id=? ORDER BY id DESC LIMIT ?`, ctx.CurrentProject(), clamp(intArg(args, "limit", 20), 1, 100))
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []*prospectingJob{}
	for _, id := range ids {
		j, e := getProspectingJob(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return map[string]any{"runs": out}, nil
}
func (a *App) toolRunResume(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	j, err := getProspectingJob(ctx, int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	if j.leaseUntil > time.Now().Unix() {
		return nil, errors.New("run is currently processing; wait for its current step")
	}
	if j.Status == "completed" {
		return map[string]any{"run": j, "idempotent": true}, nil
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := tx.Exec(`UPDATE prospecting_jobs SET status='queued',error='',lease_token='',lease_until=0,updated_at=? WHERE id=? AND project_id=? AND lease_until<=?`, nowUTC(), j.ID, ctx.CurrentProject(), time.Now().Unix())
	if err != nil {
		return nil, err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return nil, errors.New("run is currently processing")
	}
	_, err = tx.Exec(`UPDATE prospecting_job_items SET status='pending',reason='' WHERE run_id=? AND status='failed'`, j.ID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return a.toolRunGet(ctx, args)
}

func pipelineWorker(c context.Context, ctx *sdk.AppCtx) error {
	if ctx.CurrentProject() == "" {
		return nil
	}
	var id int64
	err := ctx.AppDB().QueryRow(`SELECT id FROM prospecting_jobs WHERE project_id=? AND status IN ('queued','running') AND lease_until<=? ORDER BY id LIMIT 1`, ctx.CurrentProject(), time.Now().Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	token := newRunKey()
	r, err := ctx.AppDB().Exec(`UPDATE prospecting_jobs SET status='running',lease_token=?,lease_until=?,updated_at=? WHERE id=? AND project_id=? AND status IN ('queued','running') AND lease_until<=?`, token, time.Now().Unix()+300, nowUTC(), id, ctx.CurrentProject(), time.Now().Unix())
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return nil
	}
	j, err := getProspectingJob(ctx, id)
	if err != nil {
		return err
	}
	j.leaseToken = token
	step, cancel := context.WithTimeout(c, 180*time.Second)
	err = advanceProspectingJob(step, ctx, j)
	cancel()
	if c.Err() != nil {
		// Shutdown releases the lease without declaring a transient step failed.
		_, e := ctx.AppDB().Exec(`UPDATE prospecting_jobs SET lease_until=0,lease_token='' WHERE id=? AND lease_token=?`, j.ID, token)
		return e
	}
	status := j.Status
	if err != nil {
		status = "failed"
		j.Error = err.Error()
	}
	_, saveErr := ctx.AppDB().Exec(`UPDATE prospecting_jobs SET status=?,state_json=?,error=?,lease_token='',lease_until=0,updated_at=? WHERE id=? AND lease_token=?`, status, mustJSON(j.State), j.Error, nowUTC(), j.ID, token)
	if saveErr != nil {
		return saveErr
	}
	if status == "completed" || status == "completed_with_errors" {
		ctx.EmitWithProject("prospecting.run.completed", ctx.CurrentProject(), map[string]any{"run_id": j.ID, "status": status})
	}
	return err
}

func checkpointJob(ctx *sdk.AppCtx, j *prospectingJob) error {
	r, e := ctx.AppDB().Exec(`UPDATE prospecting_jobs SET state_json=?,updated_at=? WHERE id=? AND project_id=? AND lease_token=?`, mustJSON(j.State), nowUTC(), j.ID, ctx.CurrentProject(), j.leaseToken)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return errors.New("run lease lost")
	}
	return nil
}

func recordRunItem(ctx *sdk.AppCtx, j *prospectingJob, key string, c *Candidate, created bool, reason string) error {
	var id any
	status := "pending"
	if c != nil {
		id = c.ID
	}
	if reason != "" {
		status = "excluded"
	}
	_, err := ctx.AppDB().Exec(`INSERT INTO prospecting_job_items(run_id,source_key,candidate_id,was_created,status,reason) VALUES (?,?,?,?,?,?) ON CONFLICT(run_id,source_key) DO NOTHING`, j.ID, key, id, created, status, reason)
	return err
}

func advanceProspectingJob(c context.Context, ctx *sdk.AppCtx, j *prospectingJob) error {
	p, err := getProfile(ctx.AppDB(), ctx.CurrentProject(), j.ProfileID)
	if err != nil {
		return err
	}
	if p == nil || p.Status != "active" {
		return errors.New("target profile is no longer active")
	}
	if j.State.Phase == "discover" {
		if j.Options.Source == "web" {
			return discoverWebJob(c, ctx, j, p)
		}
		return discoverPlacesJob(c, ctx, j, p)
	}
	for _, item := range j.Items {
		if item.Status != "pending" {
			continue
		}
		return qualifyRunItem(c, ctx, j, item)
	}
	j.Status = "completed"
	for _, i := range j.Items {
		if i.Status == "failed" {
			j.Status = "completed_with_errors"
			break
		}
	}
	return nil
}

func discoverPlacesJob(c context.Context, ctx *sdk.AppCtx, j *prospectingJob, p *TargetProfile) error {
	if len(j.State.Page) == 0 {
		if j.State.PlacesRequests >= j.Options.MaxPlacesRequests {
			return errors.New("run Google Places request limit reached")
		}
		j.State.PlacesRequests++
		if err := checkpointJob(ctx, j); err != nil {
			return err
		}
		input := map[string]any{"textQuery": j.Options.Query, "pageSize": 20, "regionCode": "US", "fields": placesSearchFields, "includePureServiceAreaBusinesses": true}
		if j.Options.IncludedType != "" {
			input["includedType"] = j.Options.IncludedType
			input["strictTypeFiltering"] = true
		}
		if j.Options.Bounds != nil {
			input["locationRestriction"] = j.Options.Bounds
		}
		if j.Options.Bias != nil {
			input["locationBias"] = j.Options.Bias
		}
		if j.State.PageToken != "" {
			input["pageToken"] = j.State.PageToken
		}
		var out struct {
			Places        []placeDetails `json:"places"`
			NextPageToken string         `json:"nextPageToken"`
		}
		if err := callPlaces(c, ctx, j.Options.ConnectionID, "search_text", input, &out); err != nil {
			return err
		}
		j.State.Page = out.Places
		j.State.Index = 0
		j.State.NextPageToken = out.NextPageToken
		j.State.SearchPages++
		if err := checkpointJob(ctx, j); err != nil {
			return err
		}
	}
	for j.State.Index < len(j.State.Page) && j.Counts["discovered"] < j.Options.Limit {
		if c.Err() != nil {
			return c.Err()
		}
		place := j.State.Page[j.State.Index]
		candidate, created, reason, err := ingestPlace(ctx, p, place)
		if err != nil {
			return err
		}
		key := defaultString(place.ID, fmt.Sprintf("invalid:%d:%d", j.State.SearchPages, j.State.Index))
		if err = recordRunItem(ctx, j, key, candidate, created, reason); err != nil {
			return err
		}
		j.State.Index++
		fresh, err := getProspectingJob(ctx, j.ID)
		if err != nil {
			return err
		}
		j.Counts = fresh.Counts
		if err = checkpointJob(ctx, j); err != nil {
			return err
		}
	}
	if j.Counts["discovered"] >= j.Options.Limit || j.State.NextPageToken == "" || j.State.SearchPages >= 3 {
		j.State.Phase = "qualify"
		j.State.Page = nil
		j.State.Index = 0
		j.State.PageToken = ""
		j.State.NextPageToken = ""
	} else {
		j.State.PageToken = j.State.NextPageToken
		j.State.Page = nil
		j.State.Index = 0
	}
	return nil
}

func discoverWebJob(c context.Context, ctx *sdk.AppCtx, j *prospectingJob, p *TargetProfile) error {
	out, _, _, err := executeWebSearchContext(c, ctx, j.Options.Query, j.Options.Limit, j.Options.Engine, j.Options.FallbackEngine)
	if err != nil {
		return err
	}
	for index, r := range out.Results {
		if index >= j.Options.Limit {
			break
		}
		if c.Err() != nil {
			return c.Err()
		}
		website, domain := normalizeWebsite(r.URL)
		if domain == "" {
			continue
		}
		eligible, reason := classifySearchResult(r, domain)
		input := candidateInput{ProfileID: p.ID, CompanyName: cleanCompanyTitle(r.Title, domain), CompanyDomain: domain, Website: website, Summary: r.Snippet, Source: "web_search", SourceURL: r.URL}
		blocked, e := isExcluded(ctx.AppDB(), ctx.CurrentProject(), input)
		if e != nil {
			return e
		}
		var candidate *Candidate
		created := false
		if !eligible || blocked {
			reason = defaultString(reason, "matches an exclusion")
		} else {
			var ambiguous bool
			candidate, ambiguous, err = existingPlaceCompany(ctx.AppDB(), ctx.CurrentProject(), p.ID, domain)
			if err != nil {
				return err
			}
			if ambiguous {
				reason = "company domain already represented by multiple Places branches"
			} else if candidate == nil {
				candidate, created, err = insertCandidate(ctx.AppDB(), ctx.CurrentProject(), input, p)
			}
			if err != nil {
				return err
			}
			if created {
				if err = addEvidence(ctx.AppDB(), ctx.CurrentProject(), Evidence{CandidateID: candidate.ID, SourceKind: "web_search", Title: r.Title, URL: r.URL, Excerpt: r.Snippet, RetrievedAt: nowUTC()}); err != nil {
					return err
				}
				ctx.EmitWithProject("prospecting.candidate.created", ctx.CurrentProject(), candidateEvent(candidate))
			}
		}
		if err = recordRunItem(ctx, j, "web:"+domain, candidate, created, reason); err != nil {
			return err
		}
	}
	j.State.Phase = "qualify"
	return nil
}

func finishRunItem(ctx *sdk.AppCtx, j *prospectingJob, i runItem, status, reason string) error {
	_, err := ctx.AppDB().Exec(`UPDATE prospecting_job_items SET status=?,reason=?,stage=? WHERE run_id=? AND source_key=?`, status, reason, i.Stage, j.ID, i.SourceKey)
	return err
}

func qualifyRunItem(c context.Context, ctx *sdk.AppCtx, j *prospectingJob, i runItem) error {
	if i.CandidateID == nil {
		return finishRunItem(ctx, j, i, "excluded", "prospect was removed")
	}
	candidate, err := getCandidate(ctx.AppDB(), ctx.CurrentProject(), *i.CandidateID)
	if err != nil {
		return err
	}
	if candidate == nil {
		return finishRunItem(ctx, j, i, "excluded", "prospect was removed")
	}
	if candidate.Status == "accepted" && candidate.CRMContactID != nil {
		return finishRunItem(ctx, j, i, "transferred", "already in CRM")
	}
	if candidate.Status == "rejected" || candidate.Status == "deferred" {
		return finishRunItem(ctx, j, i, "excluded", "prospect is "+candidate.Status)
	}
	excluded, err := isExcluded(ctx.AppDB(), ctx.CurrentProject(), candidateInput{CompanyName: candidate.CompanyName, CompanyDomain: candidate.CompanyDomain, Email: candidate.Email, Phone: candidate.Phone})
	if err != nil {
		return err
	}
	if excluded {
		return finishRunItem(ctx, j, i, "excluded", "matches an exclusion")
	}
	if i.Stage == "qualify" && j.Options.Qualify && candidate.EnrichedAt == "" {
		if candidate.Website == "" {
			return finishRunItem(ctx, j, i, "retained", "no website available for qualification")
		}
		_, err = qualifyCandidateContext(c, ctx, candidate.ID, j.Options.MaxPages)
		if err != nil {
			if c.Err() != nil {
				return c.Err()
			}
			return finishRunItem(ctx, j, i, "failed", err.Error())
		}
		candidate, err = getCandidate(ctx.AppDB(), ctx.CurrentProject(), candidate.ID)
		if err != nil {
			return err
		}
	}
	if candidate.Status == "rejected" || candidate.Status == "deferred" {
		return finishRunItem(ctx, j, i, "excluded", defaultString(candidate.DecisionReason, "prospect is "+candidate.Status))
	}
	if !j.Options.Qualify {
		return finishRunItem(ctx, j, i, "retained", "discovery only; website qualification disabled")
	}
	// Preserve completed qualification separately before starting the CRM
	// side effect. A retry of a failed handoff never rescrapes operator edits.
	i.Stage = "crm"
	if err = finishRunItem(ctx, j, i, "pending", ""); err != nil {
		return err
	}
	if j.Options.CRMMode == "review" {
		return finishRunItem(ctx, j, i, "qualified", "saved for review")
	}
	if candidate.Status != "ready" || candidate.Eligibility != "eligible" {
		return finishRunItem(ctx, j, i, "retained", "qualification requires review or is ineligible")
	}
	if candidate.FitScore < j.Options.MinFit || candidate.ConfidenceScore < j.Options.MinConfidence {
		return finishRunItem(ctx, j, i, "retained", "fit or confidence is below the run threshold")
	}
	if candidate.Email == "" && candidate.Phone == "" {
		return finishRunItem(ctx, j, i, "retained", "no usable email or business phone")
	}
	if c.Err() != nil {
		return c.Err()
	}
	if _, err = linkCandidateToCRMContext(c, ctx, candidate.ID, j.Options.ListIDs, true); err != nil {
		return finishRunItem(ctx, j, i, "failed", err.Error())
	}
	return finishRunItem(ctx, j, i, "transferred", "added to CRM")
}
