package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Quality configuration is part of the immutable procedure version, so every
// rating is validated against, and aggregated by, the policy its run used.
type QualityCriterion struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`
	Options []string `json:"options,omitempty"`
}
type QualityReview struct {
	LowScoreThreshold *int `json:"low_score_threshold,omitempty"`
	FailedRuns        bool `json:"failed_runs,omitempty"`
	SamplePercent     int  `json:"sample_percent,omitempty"`
}
type QualityConfig struct {
	Scale         string             `json:"scale,omitempty"`
	Tags          []string           `json:"tags,omitempty"`
	Criteria      []QualityCriterion `json:"criteria,omitempty"`
	RequestRating bool               `json:"request_rating,omitempty"`
	Review        *QualityReview     `json:"review,omitempty"`
}

var defaultRatingTags = []string{"Wrong output", "Incomplete", "Too slow", "Needed manual fix", "Great"}

const ratingLowScore = 2

func (q *QualityConfig) scale() string {
	if q == nil || q.Scale == "" {
		return "stars"
	}
	return q.Scale
}
func (q *QualityConfig) tags() []string {
	if q == nil || len(q.Tags) == 0 {
		return defaultRatingTags
	}
	return q.Tags
}
func (q *QualityConfig) lowScoreThreshold() int {
	if q == nil || q.Review == nil || q.Review.LowScoreThreshold == nil {
		return ratingLowScore
	}
	return *q.Review.LowScoreThreshold
}
func validateQuality(q *QualityConfig) error {
	if q == nil {
		return nil
	}
	if q.Scale != "" && q.Scale != "stars" && q.Scale != "thumbs" {
		return errors.New("quality scale must be stars or thumbs")
	}
	if len(q.Tags) > 12 {
		return errors.New("quality allows at most 12 reason tags")
	}
	seen := map[string]bool{}
	for i, t := range q.Tags {
		t = strings.TrimSpace(t)
		if t == "" || len(t) > 40 || seen[strings.ToLower(t)] {
			return errors.New("quality tags need unique labels of 1–40 bytes")
		}
		seen[strings.ToLower(t)] = true
		q.Tags[i] = t
	}
	if len(q.Criteria) > 8 {
		return errors.New("quality allows at most 8 criteria")
	}
	keys := map[string]bool{}
	for _, c := range q.Criteria {
		if c.Key == "" || len(c.Key) > 64 || c.Label == "" || len(c.Label) > 80 || keys[c.Key] {
			return errors.New("quality criteria need unique keys (64 bytes) and labels (80 bytes)")
		}
		keys[c.Key] = true
		switch c.Type {
		case "score", "pass_fail":
			if len(c.Options) > 0 {
				return errors.New("only choice criteria take options")
			}
		case "choice":
			if len(c.Options) < 2 || len(c.Options) > 10 {
				return errors.New("choice criteria need 2–10 options")
			}
			for _, o := range c.Options {
				if strings.TrimSpace(o) == "" || len(o) > 40 {
					return errors.New("choice options must contain 1–40 bytes")
				}
			}
		default:
			return errors.New("criterion type must be score, pass_fail or choice")
		}
	}
	if r := q.Review; r != nil {
		if r.LowScoreThreshold != nil && (*r.LowScoreThreshold < 0 || *r.LowScoreThreshold > 4) {
			return errors.New("low_score_threshold must be 0 (off) to 4")
		}
		if r.SamplePercent < 0 || r.SamplePercent > 100 {
			return errors.New("sample_percent must be 0–100")
		}
	}
	return nil
}
func qualitySchema() map[string]any {
	return object([]string{}, map[string]any{
		"scale":          map[string]any{"type": "string", "enum": []string{"stars", "thumbs"}, "description": "stars = 1–5 (default); thumbs stores up as 5 and down as 1"},
		"tags":           map[string]any{"type": "array", "maxItems": 12, "items": textField("Reason tag label"), "description": "Reason tags raters pick from; defaults apply when empty"},
		"criteria":       map[string]any{"type": "array", "maxItems": 8, "items": object([]string{"key", "label", "type"}, map[string]any{"key": textField("Stable criterion key"), "label": textField("Rubric label"), "type": map[string]any{"type": "string", "enum": []string{"score", "pass_fail", "choice"}}, "options": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}})},
		"request_rating": map[string]any{"type": "boolean", "description": "Ask the run coordinator to rate each finished run"},
		"review": object([]string{}, map[string]any{
			"low_score_threshold": map[string]any{"type": "integer", "minimum": 0, "maximum": 4, "description": "Queue a review for ratings at or below this score; default 2, 0 disables"},
			"failed_runs":         map[string]any{"type": "boolean", "description": "Queue every failed run for review"},
			"sample_percent":      map[string]any{"type": "integer", "minimum": 0, "maximum": 100, "description": "Queue this share of completed runs for review"},
		}),
	})
}

type Rating struct {
	ID              string         `json:"id"`
	RunID           string         `json:"run_id"`
	StepID          string         `json:"step_id,omitempty"`
	StepKey         string         `json:"step_key,omitempty"`
	ProcessID       string         `json:"process_id"`
	Version         int            `json:"procedure_version"`
	ExecutorAgentID int64          `json:"executor_agent_id,omitempty"`
	Rater           string         `json:"rater"`
	Source          string         `json:"source"`
	Revision        int            `json:"revision"`
	Score           int            `json:"score"`
	Criteria        map[string]any `json:"criteria"`
	Tags            []string       `json:"tags"`
	Comment         string         `json:"comment,omitempty"`
	CorrectedOutput string         `json:"corrected_output,omitempty"`
	Superseded      bool           `json:"superseded"`
	CreatedAt       string         `json:"created_at"`
}
type RunRatingSummary struct {
	Average     float64 `json:"average"`
	Count       int     `json:"count"`
	Latest      int     `json:"latest"`
	StepRatings int     `json:"step_ratings"`
}

// Agents act through their trusted caller identity; one active rating exists
// per rater, run and step, so the thread a call arrived on is not identity.
func ratingRater(actor string) string {
	if actor == "operator" {
		return actor
	}
	parts := strings.SplitN(actor, ":", 3)
	if len(parts) >= 2 && parts[0] == "agent" {
		return "agent:" + parts[1]
	}
	return actor
}

const ratingColumns = `id,run_id,step_id,process_id,version,executor_agent_id,rater,source,revision,score,criteria_json,tags_json,comment,corrected_output,superseded,created_at`

func scanRating(row interface{ Scan(...any) error }) (Rating, error) {
	var r Rating
	var criteria, tags string
	e := row.Scan(&r.ID, &r.RunID, &r.StepID, &r.ProcessID, &r.Version, &r.ExecutorAgentID, &r.Rater, &r.Source, &r.Revision, &r.Score, &criteria, &tags, &r.Comment, &r.CorrectedOutput, &r.Superseded, &r.CreatedAt)
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal([]byte(criteria), &r.Criteria); e != nil {
		return r, e
	}
	e = json.Unmarshal([]byte(tags), &r.Tags)
	return r, e
}
func (a *App) runRatingSummary(run string) (*RunRatingSummary, error) {
	var s RunRatingSummary
	var avg sql.NullFloat64
	var latest sql.NullInt64
	e := a.db.QueryRow(`SELECT avg(CASE WHEN step_id='' THEN score END),count(CASE WHEN step_id='' THEN 1 END),count(CASE WHEN step_id<>'' THEN 1 END),(SELECT score FROM process_run_ratings WHERE run_id=? AND step_id='' AND superseded=0 ORDER BY created_at DESC,id DESC LIMIT 1) FROM process_run_ratings WHERE run_id=? AND superseded=0`, run, run).Scan(&avg, &s.Count, &s.StepRatings, &latest)
	if e != nil {
		return nil, e
	}
	if s.Count == 0 && s.StepRatings == 0 {
		return nil, nil
	}
	s.Average = float64(int(avg.Float64*100+0.5)) / 100
	s.Latest = int(latest.Int64)
	return &s, nil
}

func (a *App) runRate(project, actor, process string, args map[string]any) (any, error) {
	r, e := a.getRun(project, process, str(args, "run_id"))
	if e != nil {
		return nil, e
	}
	if r.State != "completed" && r.State != "failed" && r.State != "cancelled" {
		return nil, errors.New("only finished runs can be rated; rate after the run completes, fails or is cancelled")
	}
	d, e := a.runDefinition(r)
	if e != nil {
		return nil, e
	}
	q := d.Quality
	rater := ratingRater(actor)
	source := "agent"
	if actor == "operator" {
		source = "operator"
		if s := str(args, "source"); s == "reviewer" {
			source = s
		} else if s != "" && s != "operator" {
			return nil, errors.New("operator ratings use source operator or reviewer")
		}
	}
	key := str(args, "idempotency_key")
	if key == "" || len(key) > 128 {
		return nil, errors.New("stable idempotency_key required (max 128 bytes)")
	}
	stepID, stepKey := str(args, "step_id"), ""
	var executor int64
	if stepID != "" {
		steps, e := a.steps(r.ID)
		if e != nil {
			return nil, e
		}
		found := false
		for _, s := range steps {
			if s.ID == stepID {
				found, stepKey = true, s.Key
				if s.Executor.Kind == "agent" {
					executor = s.Executor.AgentID
				}
			}
		}
		if !found {
			return nil, errNotFound
		}
		// Self-assessment belongs in summaries; a rating is another party's judgement.
		if source == "agent" && executor > 0 && rater == fmt.Sprintf("agent:%d", executor) {
			return nil, errors.New("an agent cannot rate a step it executed; save a run summary instead")
		}
	}
	f, ok := args["score"].(float64)
	if !ok || f != float64(int(f)) || f < 1 || f > 5 {
		return nil, errors.New("score must be an integer from 1 to 5")
	}
	score := int(f)
	if q.scale() == "thumbs" && score != 1 && score != 5 {
		return nil, errors.New("this procedure rates with thumbs: use score 5 for up or 1 for down")
	}
	tags := []string{}
	if raw, present := args["tags"]; present && raw != nil {
		list, valid := raw.([]any)
		if !valid || len(list) > 12 {
			return nil, errors.New("tags must be an array of at most 12 configured reason tags")
		}
		allowed := map[string]string{}
		for _, t := range q.tags() {
			allowed[strings.ToLower(t)] = t
		}
		seen := map[string]bool{}
		for _, v := range list {
			t, _ := v.(string)
			canonical, known := allowed[strings.ToLower(strings.TrimSpace(t))]
			if !known {
				return nil, fmt.Errorf("unknown reason tag %q; use one of: %s", t, strings.Join(q.tags(), ", "))
			}
			if !seen[canonical] {
				seen[canonical] = true
				tags = append(tags, canonical)
			}
		}
	}
	if score <= ratingLowScore && len(tags) == 0 {
		return nil, fmt.Errorf("low scores need at least one reason tag: %s", strings.Join(q.tags(), ", "))
	}
	criteria := map[string]any{}
	if raw, present := args["criteria"]; present && raw != nil {
		values, valid := raw.(map[string]any)
		if !valid {
			return nil, errors.New("criteria must be an object keyed by criterion")
		}
		defs := map[string]QualityCriterion{}
		if q != nil {
			for _, c := range q.Criteria {
				defs[c.Key] = c
			}
		}
		for k, v := range values {
			c, known := defs[k]
			if !known {
				return nil, fmt.Errorf("criterion %s is not declared in the frozen procedure quality rubric", k)
			}
			switch c.Type {
			case "score":
				n, isNum := v.(float64)
				if !isNum || n != float64(int(n)) || n < 1 || n > 5 {
					return nil, fmt.Errorf("criterion %s needs an integer score from 1 to 5", k)
				}
				criteria[k] = int(n)
			case "pass_fail":
				b, isBool := v.(bool)
				if !isBool {
					return nil, fmt.Errorf("criterion %s needs true (pass) or false (fail)", k)
				}
				criteria[k] = b
			case "choice":
				s, _ := v.(string)
				match := ""
				for _, o := range c.Options {
					if o == s {
						match = o
					}
				}
				if match == "" {
					return nil, fmt.Errorf("criterion %s needs one of: %s", k, strings.Join(c.Options, ", "))
				}
				criteria[k] = match
			}
		}
	}
	comment, corrected := str(args, "comment"), str(args, "corrected_output")
	if utf8.RuneCountInString(comment) > 2000 || len(corrected) > 8192 {
		return nil, errors.New("comment is limited to 2000 characters and corrected_output to 8 KiB")
	}
	criteriaJSON, tagsJSON := jsonText(criteria), jsonText(tags)

	var existing Rating
	existing, e = scanRating(a.db.QueryRow(`SELECT `+ratingColumns+` FROM process_run_ratings WHERE run_id=? AND rater=? AND request_key=?`, r.ID, rater, key))
	if e == nil {
		if existing.StepID != stepID || existing.Score != score || jsonText(existing.Criteria) != criteriaJSON || jsonText(existing.Tags) != tagsJSON || existing.Comment != comment || existing.CorrectedOutput != corrected {
			return nil, errors.New("idempotency key reused with a different rating")
		}
		return a.ratingReceipt(existing, nil), nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	tx, e := a.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var revision int
	if e = tx.QueryRow(`SELECT coalesce(max(revision),0) FROM process_run_ratings WHERE run_id=? AND step_id=? AND rater=?`, r.ID, stepID, rater).Scan(&revision); e != nil {
		return nil, e
	}
	saved := Rating{ID: newID("rtg_"), RunID: r.ID, StepID: stepID, StepKey: stepKey, ProcessID: r.ProcessID, Version: r.Version, ExecutorAgentID: executor, Rater: rater, Source: source, Revision: revision + 1, Score: score, Criteria: criteria, Tags: tags, Comment: comment, CorrectedOutput: corrected, CreatedAt: timestamp()}
	if _, e = tx.Exec(`UPDATE process_run_ratings SET superseded=1 WHERE run_id=? AND step_id=? AND rater=? AND superseded=0`, r.ID, stepID, rater); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(`INSERT INTO process_run_ratings(`+ratingColumns+`,request_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?)`, saved.ID, saved.RunID, saved.StepID, saved.ProcessID, saved.Version, saved.ExecutorAgentID, saved.Rater, saved.Source, saved.Revision, saved.Score, criteriaJSON, tagsJSON, saved.Comment, saved.CorrectedOutput, saved.CreatedAt, key); e != nil {
		return nil, e
	}
	var review *ReviewItem
	if threshold := q.lowScoreThreshold(); threshold > 0 && score <= threshold {
		now := timestamp()
		item := ReviewItem{ID: newID("rvw_"), RunID: r.ID, ProcessID: r.ProcessID, Version: r.Version, Reason: "low_score", RatingID: saved.ID, State: "open", Revision: 1, CreatedAt: now, UpdatedAt: now}
		res, e := tx.Exec(`INSERT INTO process_review_items(id,run_id,process_id,version,reason,rating_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, item.ID, item.RunID, item.ProcessID, item.Version, item.Reason, item.RatingID, now, now)
		if e != nil {
			return nil, e
		}
		if n, _ := res.RowsAffected(); n == 1 {
			review = &item
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return a.ratingReceipt(saved, review), nil
}
func (a *App) ratingReceipt(r Rating, review *ReviewItem) map[string]any {
	out := map[string]any{"saved": true, "rating_id": r.ID, "run_id": r.RunID, "revision": r.Revision, "score": r.Score, "source": r.Source, "reread": RereadReference{Tool: "processes_run_ratings", Args: map[string]any{"process_id": r.ProcessID, "run_id": r.RunID}}}
	if r.StepID != "" {
		out["step_id"] = r.StepID
	}
	if review != nil {
		out["review_item"] = map[string]any{"id": review.ID, "reason": review.Reason, "state": review.State}
	}
	return out
}
func (a *App) runRatings(project, process string, args map[string]any) (any, error) {
	if _, e := a.get(project, process); e != nil {
		return nil, e
	}
	history := args["include_history"] == true
	if run := str(args, "run_id"); run != "" {
		r, e := a.getRun(project, process, run)
		if e != nil {
			return nil, e
		}
		q := `SELECT ` + ratingColumns + ` FROM process_run_ratings WHERE run_id=?`
		if !history {
			q += ` AND superseded=0`
		}
		rows, e := a.db.Query(q+` ORDER BY step_id,created_at DESC,id DESC LIMIT 200`, r.ID)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		out := []Rating{}
		for rows.Next() {
			x, e := scanRating(rows)
			if e != nil {
				return nil, e
			}
			out = append(out, x)
		}
		if e = rows.Err(); e != nil {
			return nil, e
		}
		if e = a.labelRatingSteps(out); e != nil {
			return nil, e
		}
		summary, e := a.runRatingSummary(r.ID)
		if e != nil {
			return nil, e
		}
		d, e := a.runDefinition(r)
		if e != nil {
			return nil, e
		}
		criteria := []QualityCriterion{}
		if d.Quality != nil && d.Quality.Criteria != nil {
			criteria = d.Quality.Criteria
		}
		quality := map[string]any{"scale": d.Quality.scale(), "tags": d.Quality.tags(), "criteria": criteria, "low_score": ratingLowScore}
		return map[string]any{"run_id": r.ID, "run_state": r.State, "ratings": out, "summary": summary, "quality": quality}, nil
	}
	limit, e := pageLimit(args)
	if e != nil {
		return nil, e
	}
	version, source, tag := number(args, "version"), str(args, "source"), str(args, "tag")
	maxScore := number(args, "max_score")
	qid := jsonText([]string{project, process, strconv.Itoa(version), source, tag, strconv.Itoa(maxScore), strconv.FormatBool(history)})
	c, e := parseCursor(str(args, "cursor"), qid)
	if e != nil {
		return nil, e
	}
	q := `SELECT ` + ratingColumns + ` FROM process_run_ratings WHERE process_id=?`
	values := []any{process}
	if !history {
		q += ` AND superseded=0`
	}
	if version > 0 {
		q += ` AND version=?`
		values = append(values, version)
	}
	if source != "" {
		q += ` AND source=?`
		values = append(values, source)
	}
	if maxScore > 0 {
		q += ` AND score<=?`
		values = append(values, maxScore)
	}
	if tag != "" {
		q += ` AND EXISTS(SELECT 1 FROM json_each(tags_json) WHERE lower(value)=lower(?))`
		values = append(values, tag)
	}
	if c.ID != "" {
		q += ` AND (created_at<? OR (created_at=? AND id<?))`
		values = append(values, c.Date, c.Date, c.ID)
	}
	rows, e := a.db.Query(q+` ORDER BY created_at DESC,id DESC LIMIT ?`, append(values, limit+1)...)
	if e != nil {
		return nil, e
	}
	out := []Rating{}
	next := ""
	for rows.Next() {
		x, e := scanRating(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		if len(out) >= limit {
			last := out[len(out)-1]
			next = cursorToken(qid, last.CreatedAt, last.ID)
			break
		}
		out = append(out, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if e = a.labelRatingSteps(out); e != nil {
		return nil, e
	}
	return map[string]any{"ratings": out, "has_more": next != "", "next_cursor": next}, nil
}
func (a *App) labelRatingSteps(xs []Rating) error {
	for i := range xs {
		if xs[i].StepID == "" {
			continue
		}
		if e := a.db.QueryRow(`SELECT step_key FROM process_step_runs WHERE id=?`, xs[i].StepID).Scan(&xs[i].StepKey); e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
	}
	return nil
}

type QualityVersion struct {
	Version      int            `json:"version"`
	FinishedRuns int            `json:"finished_runs"`
	RatedRuns    int            `json:"rated_runs"`
	Coverage     float64        `json:"coverage"`
	Average      *float64       `json:"average"`
	Distribution map[string]int `json:"distribution"`
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

func (a *App) qualityGet(project, process string, args map[string]any) (any, error) {
	p, e := a.get(project, process)
	if e != nil {
		return nil, e
	}
	version := number(args, "version")
	scope, values := ` AND superseded=0`, []any{process}
	if version > 0 {
		scope += ` AND version=?`
		values = append(values, version)
	}
	versions := []QualityVersion{}
	rows, e := a.db.Query(`SELECT r.version,count(*),count(DISTINCT x.run_id) FROM process_runs r LEFT JOIN (SELECT DISTINCT run_id FROM process_run_ratings WHERE process_id=? AND superseded=0) x ON x.run_id=r.id WHERE r.process_id=? AND r.state IN ('completed','failed','cancelled') GROUP BY r.version ORDER BY r.version DESC LIMIT 20`, process, process)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		v := QualityVersion{Distribution: map[string]int{"1": 0, "2": 0, "3": 0, "4": 0, "5": 0}}
		if e = rows.Scan(&v.Version, &v.FinishedRuns, &v.RatedRuns); e != nil {
			rows.Close()
			return nil, e
		}
		if v.FinishedRuns > 0 {
			v.Coverage = round2(float64(v.RatedRuns) / float64(v.FinishedRuns))
		}
		versions = append(versions, v)
	}
	rows.Close()
	for i := range versions {
		v := &versions[i]
		dist, e := a.db.Query(`SELECT score,count(*) FROM process_run_ratings WHERE process_id=? AND version=? AND step_id='' AND superseded=0 GROUP BY score`, process, v.Version)
		if e != nil {
			return nil, e
		}
		total, sum := 0, 0
		for dist.Next() {
			var score, n int
			if e = dist.Scan(&score, &n); e != nil {
				dist.Close()
				return nil, e
			}
			v.Distribution[strconv.Itoa(score)] = n
			total += n
			sum += score * n
		}
		dist.Close()
		if total > 0 {
			avg := round2(float64(sum) / float64(total))
			v.Average = &avg
		}
	}
	tags := []map[string]any{}
	rows, e = a.db.Query(`SELECT j.value,count(*) FROM process_run_ratings, json_each(tags_json) j WHERE process_id=?`+scope+` GROUP BY j.value ORDER BY count(*) DESC,j.value LIMIT 12`, values...)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var t string
		var n int
		if e = rows.Scan(&t, &n); e != nil {
			rows.Close()
			return nil, e
		}
		tags = append(tags, map[string]any{"tag": t, "count": n})
	}
	rows.Close()
	executors := []map[string]any{}
	rows, e = a.db.Query(`SELECT executor_agent_id,count(*),avg(score) FROM process_run_ratings WHERE process_id=? AND step_id<>'' AND executor_agent_id>0`+scope+` GROUP BY executor_agent_id ORDER BY avg(score),executor_agent_id`, values...)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id int64
		var n int
		var avg float64
		if e = rows.Scan(&id, &n, &avg); e != nil {
			rows.Close()
			return nil, e
		}
		executors = append(executors, map[string]any{"agent_id": id, "step_ratings": n, "average": round2(avg)})
	}
	rows.Close()
	criteria, e := a.criteriaAggregates(p.Definition.Quality, process, scope, values)
	if e != nil {
		return nil, e
	}
	worst := []map[string]any{}
	rows, e = a.db.Query(`SELECT run_id,version,score,tags_json,created_at FROM process_run_ratings WHERE process_id=? AND step_id='' AND score<=?`+scope+` ORDER BY score,created_at DESC LIMIT 5`, append([]any{process, ratingLowScore}, values[1:]...)...)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var run, tagsJSON, created string
		var v, score int
		if e = rows.Scan(&run, &v, &score, &tagsJSON, &created); e != nil {
			rows.Close()
			return nil, e
		}
		var ts []string
		_ = json.Unmarshal([]byte(tagsJSON), &ts)
		worst = append(worst, map[string]any{"run_id": run, "procedure_version": v, "score": score, "tags": ts, "rated_at": created})
	}
	rows.Close()
	review := map[string]int{"open": 0, "claimed": 0}
	rows, e = a.db.Query(`SELECT state,count(*) FROM process_review_items WHERE process_id=? AND state IN ('open','claimed') GROUP BY state`, process)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var s string
		var n int
		if e = rows.Scan(&s, &n); e != nil {
			rows.Close()
			return nil, e
		}
		review[s] = n
	}
	rows.Close()
	return map[string]any{"process_id": process, "current_version": p.Version, "scale": p.Definition.Quality.scale(), "versions": versions, "tags": tags, "executors": executors, "criteria": criteria, "worst_runs": worst, "review": review}, nil
}

// Criteria are aggregated by the current rubric's types; keys a later
// revision removed are still reported from the stored values.
func (a *App) criteriaAggregates(q *QualityConfig, process, scope string, values []any) ([]map[string]any, error) {
	labels := map[string]QualityCriterion{}
	order := []string{}
	if q != nil {
		for _, c := range q.Criteria {
			labels[c.Key] = c
			order = append(order, c.Key)
		}
	}
	type agg struct {
		n, sum, pass int
		choices      map[string]int
		kind         string
	}
	acc := map[string]*agg{}
	rows, e := a.db.Query(`SELECT j.key,j.type,j.value FROM process_run_ratings, json_each(criteria_json) j WHERE process_id=?`+scope+` ORDER BY created_at DESC LIMIT 5000`, values...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var k, t string
		var v any
		if e = rows.Scan(&k, &t, &v); e != nil {
			return nil, e
		}
		x := acc[k]
		if x == nil {
			x = &agg{choices: map[string]int{}}
			acc[k] = x
			if _, known := labels[k]; !known {
				order = append(order, k)
			}
		}
		x.n++
		switch t {
		case "integer":
			x.kind = "score"
			n, _ := strconv.Atoi(fmt.Sprint(v))
			x.sum += n
		case "true", "false":
			x.kind = "pass_fail"
			if t == "true" {
				x.pass++
			}
		default:
			x.kind = "choice"
			x.choices[fmt.Sprint(v)]++
		}
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	out := []map[string]any{}
	for _, k := range order {
		c := labels[k]
		x := acc[k]
		item := map[string]any{"key": k, "label": c.Label, "type": c.Type, "count": 0}
		if c.Label == "" {
			item["label"] = k
		}
		if x != nil {
			item["count"] = x.n
			if c.Type == "" {
				item["type"] = x.kind
			}
			switch item["type"] {
			case "score":
				item["average"] = round2(float64(x.sum) / float64(x.n))
			case "pass_fail":
				item["pass_rate"] = round2(float64(x.pass) / float64(x.n))
			default:
				item["choices"] = x.choices
			}
		}
		out = append(out, item)
	}
	return out, nil
}

type ReviewItem struct {
	ID         string            `json:"id"`
	RunID      string            `json:"run_id"`
	ProcessID  string            `json:"process_id"`
	Version    int               `json:"procedure_version"`
	Reason     string            `json:"reason"`
	RatingID   string            `json:"rating_id,omitempty"`
	State      string            `json:"state"`
	Assignee   string            `json:"assignee,omitempty"`
	Resolution string            `json:"resolution,omitempty"`
	Revision   int               `json:"revision"`
	CreatedAt  string            `json:"created_at"`
	UpdatedAt  string            `json:"updated_at"`
	RunState   string            `json:"run_state,omitempty"`
	Rating     *RunRatingSummary `json:"rating,omitempty"`
	Reread     RereadReference   `json:"reread"`
}

const reviewColumns = `i.id,i.run_id,i.process_id,i.version,i.reason,i.rating_id,i.state,i.assignee,i.resolution,i.revision,i.created_at,i.updated_at,r.state`

func scanReview(row interface{ Scan(...any) error }) (ReviewItem, error) {
	var x ReviewItem
	e := row.Scan(&x.ID, &x.RunID, &x.ProcessID, &x.Version, &x.Reason, &x.RatingID, &x.State, &x.Assignee, &x.Resolution, &x.Revision, &x.CreatedAt, &x.UpdatedAt, &x.RunState)
	x.Reread = runReread(x.ProcessID, x.RunID)
	return x, e
}

// An empty process reads the whole project's queue for the panel and widget.
func (a *App) reviewQueue(project, process string, args map[string]any) (any, error) {
	if process != "" {
		if _, e := a.get(project, process); e != nil {
			return nil, e
		}
	}
	limit, e := pageLimit(args)
	if e != nil {
		return nil, e
	}
	state := str(args, "state")
	if state == "" {
		state = "active"
	}
	if !strings.Contains("|active|open|claimed|resolved|dismissed|all|", "|"+state+"|") {
		return nil, errors.New("state must be active, open, claimed, resolved, dismissed or all")
	}
	qid := jsonText([]string{project, process, state})
	c, e := parseCursor(str(args, "cursor"), qid)
	if e != nil {
		return nil, e
	}
	q := `SELECT ` + reviewColumns + ` FROM process_review_items i JOIN process_runs r ON r.id=i.run_id JOIN processes p ON p.id=i.process_id WHERE p.project_id=?`
	values := []any{project}
	if process != "" {
		q += ` AND i.process_id=?`
		values = append(values, process)
	}
	switch state {
	case "active":
		q += ` AND i.state IN ('open','claimed')`
	case "all":
	default:
		q += ` AND i.state=?`
		values = append(values, state)
	}
	if c.ID != "" {
		q += ` AND (i.created_at<? OR (i.created_at=? AND i.id<?))`
		values = append(values, c.Date, c.Date, c.ID)
	}
	rows, e := a.db.Query(q+` ORDER BY i.created_at DESC,i.id DESC LIMIT ?`, append(values, limit+1)...)
	if e != nil {
		return nil, e
	}
	out := []ReviewItem{}
	next := ""
	for rows.Next() {
		x, e := scanReview(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		if len(out) >= limit {
			last := out[len(out)-1]
			next = cursorToken(qid, last.CreatedAt, last.ID)
			break
		}
		out = append(out, x)
	}
	rows.Close()
	for i := range out {
		if out[i].Rating, e = a.runRatingSummary(out[i].RunID); e != nil {
			return nil, e
		}
	}
	var open int
	countQ, countArgs := `SELECT count(*) FROM process_review_items i JOIN processes p ON p.id=i.process_id WHERE p.project_id=? AND i.state IN ('open','claimed')`, []any{project}
	if process != "" {
		countQ += ` AND i.process_id=?`
		countArgs = append(countArgs, process)
	}
	if e = a.db.QueryRow(countQ, countArgs...).Scan(&open); e != nil {
		return nil, e
	}
	return map[string]any{"items": out, "active_count": open, "has_more": next != "", "next_cursor": next}, nil
}
func (a *App) reviewUpdate(project, actor, process string, args map[string]any) (any, error) {
	if _, e := a.get(project, process); e != nil {
		return nil, e
	}
	id, state := str(args, "review_id"), str(args, "state")
	expected, e := memoryRevision(args, "expected_revision", 1)
	if e != nil {
		return nil, e
	}
	x, e := scanReview(a.db.QueryRow(`SELECT `+reviewColumns+` FROM process_review_items i JOIN process_runs r ON r.id=i.run_id WHERE i.id=? AND i.process_id=?`, id, process))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if e != nil {
		return nil, e
	}
	resolution := str(args, "resolution")
	if utf8.RuneCountInString(resolution) > 500 {
		return nil, errors.New("resolution is limited to 500 characters")
	}
	assignee := x.Assignee
	switch state {
	case "claimed":
		assignee = ratingRater(actor)
	case "open":
		assignee = ""
	case "resolved", "dismissed":
		if resolution == "" {
			return nil, errors.New("resolving or dismissing a review needs a short resolution")
		}
		if assignee == "" {
			assignee = ratingRater(actor)
		}
	default:
		return nil, errors.New("state must be open, claimed, resolved or dismissed")
	}
	if x.Revision != expected {
		return nil, errConflict
	}
	res, e := a.db.Exec(`UPDATE process_review_items SET state=?,assignee=?,resolution=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, state, assignee, resolution, timestamp(), id, expected)
	if e != nil {
		return nil, e
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errConflict
	}
	return map[string]any{"saved": true, "review_id": id, "state": state, "revision": expected + 1, "run_id": x.RunID, "reread": RereadReference{Tool: "processes_review_queue", Args: map[string]any{"process_id": process, "state": "all"}}}, nil
}

// Coordinators are asked to judge outcomes only when the frozen procedure
// requests it; the notice names the exact scale and tags the tool accepts.
func ratingRequest(q *QualityConfig, process, run string) string {
	if q == nil || !q.RequestRating {
		return ""
	}
	scale := "score 1–5"
	if q.scale() == "thumbs" {
		scale = "score 5 (thumbs up) or 1 (thumbs down)"
	}
	return fmt.Sprintf(" This procedure requests a quality rating: after verifying the outcome, call processes_run_rate once with process_id=%s run_id=%s, %s, a stable idempotency_key and reason tags from [%s] (required for scores of %d or lower). You may also rate steps other agents executed with step_id. Do not rate steps you executed yourself.", process, run, scale, strings.Join(q.tags(), ", "), ratingLowScore)
}
