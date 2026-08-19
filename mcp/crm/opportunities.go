package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	opportunityStatusOpen = "open"
	opportunityStatusWon  = "won"
	opportunityStatusLost = "lost"
)

var opportunityStatuses = map[string]bool{
	opportunityStatusOpen: true,
	opportunityStatusWon:  true,
	opportunityStatusLost: true,
}

type Pipeline struct {
	ID          int64            `json:"id"`
	ProjectID   string           `json:"project_id,omitempty"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	IsDefault   bool             `json:"is_default,omitempty"`
	ArchivedAt  string           `json:"archived_at,omitempty"`
	CreatedAt   string           `json:"created_at,omitempty"`
	UpdatedAt   string           `json:"updated_at,omitempty"`
	Stages      []*PipelineStage `json:"stages,omitempty"`
}

type PipelineStage struct {
	ID          int64    `json:"id"`
	ProjectID   string   `json:"project_id,omitempty"`
	PipelineID  int64    `json:"pipeline_id"`
	Name        string   `json:"name"`
	Position    int      `json:"position"`
	Category    string   `json:"category"`
	Probability *float64 `json:"probability,omitempty"`
	ArchivedAt  string   `json:"archived_at,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

type Opportunity struct {
	ID                int64    `json:"id"`
	ProjectID         string   `json:"project_id,omitempty"`
	ContactID         int64    `json:"contact_id"`
	PipelineID        int64    `json:"pipeline_id"`
	StageID           int64    `json:"stage_id"`
	Title             string   `json:"title"`
	Status            string   `json:"status"`
	Value             *float64 `json:"value,omitempty"`
	Currency          string   `json:"currency,omitempty"`
	OfferKey          string   `json:"offer_key,omitempty"`
	OfferName         string   `json:"offer_name,omitempty"`
	Source            string   `json:"source,omitempty"`
	SourceSite        string   `json:"source_site,omitempty"`
	SenderIdentity    string   `json:"sender_identity,omitempty"`
	Owner             string   `json:"owner,omitempty"`
	ExpectedCloseDate string   `json:"expected_close_date,omitempty"`
	ClosedAt          string   `json:"closed_at,omitempty"`
	LostReason        string   `json:"lost_reason,omitempty"`
	CreatedAt         string   `json:"created_at,omitempty"`
	UpdatedAt         string   `json:"updated_at,omitempty"`
	ArchivedAt        string   `json:"archived_at,omitempty"`

	PipelineName  string `json:"pipeline_name,omitempty"`
	StageName     string `json:"stage_name,omitempty"`
	StageCategory string `json:"stage_category,omitempty"`
	// StageProbability is the probability configured on the stage this
	// opportunity currently sits in. There is no per-opportunity
	// probability column, so this is a pipeline-level forecast weight,
	// not a per-deal override — named accordingly so consumers don't
	// read it as one.
	StageProbability *float64 `json:"stage_probability,omitempty"`
	ContactName      string   `json:"contact_name,omitempty"`
	ContactEmail     string   `json:"contact_email,omitempty"`
	ContactPhone     string   `json:"contact_phone,omitempty"`
}

type OpportunityStageHistory struct {
	ID            int64  `json:"id"`
	OpportunityID int64  `json:"opportunity_id"`
	FromStageID   int64  `json:"from_stage_id,omitempty"`
	ToStageID     int64  `json:"to_stage_id"`
	FromStatus    string `json:"from_status,omitempty"`
	ToStatus      string `json:"to_status"`
	Note          string `json:"note,omitempty"`
	Source        string `json:"source,omitempty"`
	ChangedAt     string `json:"changed_at"`
}

type opportunityCreateInput struct {
	ContactID         int64
	PipelineID        int64
	StageID           int64
	Title             string
	Status            string
	Value             *float64
	Currency          string
	OfferKey          string
	OfferName         string
	Source            string
	SourceSite        string
	SenderIdentity    string
	Owner             string
	ExpectedCloseDate string
	ClosedAt          string
	LostReason        string
	StageChangeNote   string
	StageChangeSource string
}

func defaultPipelineStages() []*PipelineStage {
	return []*PipelineStage{
		{Name: "New", Position: 1, Category: opportunityStatusOpen, Probability: floatPtr(0.05)},
		{Name: "Contacted", Position: 2, Category: opportunityStatusOpen, Probability: floatPtr(0.10)},
		{Name: "Replied", Position: 3, Category: opportunityStatusOpen, Probability: floatPtr(0.25)},
		{Name: "Qualified", Position: 4, Category: opportunityStatusOpen, Probability: floatPtr(0.45)},
		{Name: "Proposal", Position: 5, Category: opportunityStatusOpen, Probability: floatPtr(0.70)},
		{Name: "Won", Position: 6, Category: opportunityStatusWon, Probability: floatPtr(1.0)},
		{Name: "Lost", Position: 7, Category: opportunityStatusLost, Probability: floatPtr(0.0)},
	}
}

func floatPtr(v float64) *float64 { return &v }

func statusForStageCategory(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case opportunityStatusWon:
		return opportunityStatusWon
	case opportunityStatusLost:
		return opportunityStatusLost
	default:
		return opportunityStatusOpen
	}
}

func parseStageCategory(category string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "", opportunityStatusOpen:
		return opportunityStatusOpen, nil
	case opportunityStatusWon:
		return opportunityStatusWon, nil
	case opportunityStatusLost:
		return opportunityStatusLost, nil
	default:
		return "", fmt.Errorf("invalid stage category %q", category)
	}
}

func validateStageProbability(probability *float64) error {
	if probability != nil && (*probability < 0 || *probability > 1) {
		return errors.New("stage probability must be between 0 and 1")
	}
	return nil
}

func dbEnsureDefaultPipeline(db *sql.DB, pid string) (*Pipeline, error) {
	if p, err := dbDefaultPipeline(db, pid); err != nil || p != nil {
		return p, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO crm_pipelines
			(project_id, name, description, is_default, created_at, updated_at)
		 VALUES (?, 'Sales', 'Default sales pipeline', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		pid,
	)
	if err != nil {
		if isUniqueViolation(err) {
			_ = tx.Rollback()
			return dbDefaultPipeline(db, pid)
		}
		return nil, err
	}
	pipelineID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	for _, st := range defaultPipelineStages() {
		if _, err := tx.Exec(
			`INSERT INTO crm_pipeline_stages
				(project_id, pipeline_id, name, position, category, probability, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			pid, pipelineID, st.Name, st.Position, st.Category, nullableFloat(st.Probability),
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbPipelineGet(db, pid, pipelineID)
}

func dbDefaultPipeline(db *sql.DB, pid string) (*Pipeline, error) {
	var id int64
	err := db.QueryRow(
		`SELECT id FROM crm_pipelines
		 WHERE project_id = ? AND is_default = 1 AND archived_at IS NULL
		 ORDER BY id LIMIT 1`,
		pid,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbPipelineGet(db, pid, id)
}

func dbPipelineCreate(db *sql.DB, pid string, p *Pipeline, stages []*PipelineStage) (*Pipeline, error) {
	if strings.TrimSpace(p.Name) == "" {
		return nil, errors.New("name required")
	}
	if len(stages) == 0 {
		stages = defaultPipelineStages()
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if p.IsDefault {
		if _, err := tx.Exec(
			`UPDATE crm_pipelines SET is_default = 0, updated_at = CURRENT_TIMESTAMP
			 WHERE project_id = ? AND archived_at IS NULL`,
			pid,
		); err != nil {
			return nil, err
		}
	}
	res, err := tx.Exec(
		`INSERT INTO crm_pipelines
			(project_id, name, description, is_default, created_at, updated_at)
		 VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		pid, strings.TrimSpace(p.Name), nullStr(p.Description), boolToInt(p.IsDefault),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("pipeline %q already exists in this project", p.Name)
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	for i, st := range stages {
		pos := st.Position
		if pos == 0 {
			pos = i + 1
		}
		if pos < 1 {
			return nil, errors.New("stage position must be positive")
		}
		if strings.TrimSpace(st.Name) == "" {
			return nil, errors.New("stage name required")
		}
		category, err := parseStageCategory(st.Category)
		if err != nil {
			return nil, err
		}
		if err := validateStageProbability(st.Probability); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(
			`INSERT INTO crm_pipeline_stages
				(project_id, pipeline_id, name, position, category, probability, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			pid, id, strings.TrimSpace(st.Name), pos, category, nullableFloat(st.Probability),
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbPipelineGet(db, pid, id)
}

func dbPipelineGet(db *sql.DB, pid string, id int64) (*Pipeline, error) {
	p := &Pipeline{}
	var desc, arch sql.NullString
	var isDefault int
	err := db.QueryRow(
		`SELECT id, project_id, name, description, is_default,
				archived_at, created_at, updated_at
		 FROM crm_pipelines WHERE project_id = ? AND id = ?`,
		pid, id,
	).Scan(&p.ID, &p.ProjectID, &p.Name, &desc, &isDefault, &arch, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Description = desc.String
	p.IsDefault = isDefault == 1
	p.ArchivedAt = arch.String
	stages, err := dbPipelineStages(db, pid, p.ID, false)
	if err != nil {
		return nil, err
	}
	p.Stages = stages
	return p, nil
}

func dbPipelinesList(db *sql.DB, pid string, includeArchived bool) ([]*Pipeline, error) {
	if _, err := dbEnsureDefaultPipeline(db, pid); err != nil {
		return nil, err
	}
	where := "project_id = ?"
	if !includeArchived {
		where += " AND archived_at IS NULL"
	}
	rows, err := db.Query(
		`SELECT id FROM crm_pipelines WHERE `+where+` ORDER BY is_default DESC, name COLLATE NOCASE`,
		pid,
	)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := []*Pipeline{}
	for _, id := range ids {
		p, err := dbPipelineGet(db, pid, id)
		if err != nil {
			return nil, err
		}
		if p != nil {
			out = append(out, p)
		}
	}
	return out, nil
}

func dbPipelineStages(db *sql.DB, pid string, pipelineID int64, includeArchived bool) ([]*PipelineStage, error) {
	where := "project_id = ? AND pipeline_id = ?"
	if !includeArchived {
		where += " AND archived_at IS NULL"
	}
	rows, err := db.Query(
		`SELECT id, project_id, pipeline_id, name, position, category,
				probability, archived_at, created_at, updated_at
		 FROM crm_pipeline_stages
		 WHERE `+where+`
		 ORDER BY position, id`,
		pid, pipelineID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PipelineStage{}
	for rows.Next() {
		st, err := scanPipelineStage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanPipelineStage(row interface{ Scan(...any) error }) (*PipelineStage, error) {
	st := &PipelineStage{}
	var prob sql.NullFloat64
	var arch sql.NullString
	err := row.Scan(&st.ID, &st.ProjectID, &st.PipelineID, &st.Name, &st.Position,
		&st.Category, &prob, &arch, &st.CreatedAt, &st.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if prob.Valid {
		st.Probability = &prob.Float64
	}
	st.ArchivedAt = arch.String
	return st, nil
}

func dbPipelineStageGet(db *sql.DB, pid string, id int64) (*PipelineStage, error) {
	row := db.QueryRow(
		`SELECT id, project_id, pipeline_id, name, position, category,
				probability, archived_at, created_at, updated_at
		 FROM crm_pipeline_stages WHERE project_id = ? AND id = ?`,
		pid, id,
	)
	st, err := scanPipelineStage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return st, err
}

func dbPipelineStageCreate(db *sql.DB, pid string, st *PipelineStage) (*PipelineStage, error) {
	if st.PipelineID == 0 || strings.TrimSpace(st.Name) == "" {
		return nil, errors.New("pipeline_id and name required")
	}
	if p, err := dbPipelineGet(db, pid, st.PipelineID); err != nil || p == nil || p.ArchivedAt != "" {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("pipeline not found")
	}
	if st.Position == 0 {
		if err := db.QueryRow(
			`SELECT COALESCE(MAX(position),0)+1 FROM crm_pipeline_stages
			 WHERE project_id = ? AND pipeline_id = ? AND archived_at IS NULL`,
			pid, st.PipelineID,
		).Scan(&st.Position); err != nil {
			return nil, err
		}
	}
	if st.Position < 1 {
		return nil, errors.New("stage position must be positive")
	}
	category, err := parseStageCategory(st.Category)
	if err != nil {
		return nil, err
	}
	if err := validateStageProbability(st.Probability); err != nil {
		return nil, err
	}
	res, err := db.Exec(
		`INSERT INTO crm_pipeline_stages
			(project_id, pipeline_id, name, position, category, probability, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		pid, st.PipelineID, strings.TrimSpace(st.Name), st.Position,
		category, nullableFloat(st.Probability),
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return dbPipelineStageGet(db, pid, id)
}

func dbPipelineStageUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*PipelineStage, error) {
	existing, err := dbPipelineStageGet(db, pid, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, errors.New("stage not found")
	}
	if existing.ArchivedAt != "" {
		return nil, errors.New("stage is archived")
	}
	sets := []string{}
	args := []any{}
	for k, v := range patch {
		switch k {
		case "name":
			if s := strings.TrimSpace(strFromAny(v)); s != "" {
				sets = append(sets, "name = ?")
				args = append(args, s)
			}
		case "position":
			if int64FromAny(v) < 1 {
				return nil, errors.New("stage position must be positive")
			}
			sets = append(sets, "position = ?")
			args = append(args, int64FromAny(v))
		case "category":
			category, err := parseStageCategory(strFromAny(v))
			if err != nil {
				return nil, err
			}
			if category != existing.Category {
				var active int
				if err := db.QueryRow(
					`SELECT COUNT(*) FROM crm_opportunities
					 WHERE project_id = ? AND stage_id = ? AND archived_at IS NULL`,
					pid, id,
				).Scan(&active); err != nil {
					return nil, err
				}
				if active > 0 {
					return nil, errors.New("cannot change category while the stage has active opportunities")
				}
			}
			sets = append(sets, "category = ?")
			args = append(args, category)
		case "probability":
			probability := floatFromAnyPtr(v)
			if err := validateStageProbability(probability); err != nil {
				return nil, err
			}
			sets = append(sets, "probability = ?")
			args = append(args, nullableFloat(probability))
		}
	}
	if len(sets) == 0 {
		return existing, nil
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, pid, id)
	if _, err := db.Exec(
		`UPDATE crm_pipeline_stages SET `+strings.Join(sets, ", ")+`
		 WHERE project_id = ? AND id = ?`,
		args...,
	); err != nil {
		return nil, err
	}
	return dbPipelineStageGet(db, pid, id)
}

func dbPipelineStageArchive(db *sql.DB, pid string, id int64) error {
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM crm_opportunities
		 WHERE project_id = ? AND stage_id = ? AND archived_at IS NULL`,
		pid, id,
	).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("stage has %d active opportunities; move them before archiving", n)
	}
	res, err := db.Exec(
		`UPDATE crm_pipeline_stages
		 SET archived_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND id = ? AND archived_at IS NULL`,
		pid, id,
	)
	if err != nil {
		return err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return errors.New("stage not found")
	}
	return nil
}

func dbOpportunityCreate(db *sql.DB, pid string, in opportunityCreateInput) (*Opportunity, error) {
	if in.ContactID == 0 {
		return nil, errors.New("contact_id required")
	}
	if c, err := dbGetByID(db, pid, in.ContactID); err != nil || c == nil || c.Status == "merged" {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("contact not found")
	}
	if in.PipelineID == 0 && in.StageID == 0 {
		p, err := dbEnsureDefaultPipeline(db, pid)
		if err != nil {
			return nil, err
		}
		in.PipelineID = p.ID
		if len(p.Stages) > 0 {
			in.StageID = p.Stages[0].ID
		}
	}
	if in.PipelineID == 0 && in.StageID != 0 {
		st, err := dbPipelineStageGet(db, pid, in.StageID)
		if err != nil {
			return nil, err
		}
		if st == nil || st.ArchivedAt != "" {
			return nil, errors.New("stage not found")
		}
		in.PipelineID = st.PipelineID
	}
	if in.PipelineID != 0 && in.StageID == 0 {
		stages, err := dbPipelineStages(db, pid, in.PipelineID, false)
		if err != nil {
			return nil, err
		}
		if len(stages) == 0 {
			return nil, errors.New("pipeline has no active stages")
		}
		in.StageID = stages[0].ID
	}
	st, err := dbPipelineStageGet(db, pid, in.StageID)
	if err != nil {
		return nil, err
	}
	if st == nil || st.PipelineID != in.PipelineID || st.ArchivedAt != "" {
		return nil, errors.New("stage not found in pipeline")
	}
	if strings.TrimSpace(in.Title) == "" {
		in.Title = "Opportunity"
		if in.OfferName != "" {
			in.Title = in.OfferName
		}
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	expectedStatus := statusForStageCategory(st.Category)
	if status == "" {
		status = expectedStatus
	}
	if !opportunityStatuses[status] {
		return nil, fmt.Errorf("invalid status %q", status)
	}
	if status != expectedStatus {
		return nil, fmt.Errorf("status %q does not match stage category %q", status, st.Category)
	}
	if status == opportunityStatusOpen {
		in.ClosedAt = ""
		in.LostReason = ""
	} else if strings.TrimSpace(in.ClosedAt) == "" {
		in.ClosedAt = time.Now().UTC().Format(time.RFC3339)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO crm_opportunities
			(project_id, contact_id, pipeline_id, stage_id, title, status,
			 value, currency, offer_key, offer_name, source, source_site,
			 sender_identity, owner, expected_close_date, closed_at, lost_reason,
			 created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			 CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		pid, in.ContactID, in.PipelineID, in.StageID, strings.TrimSpace(in.Title), status,
		nullableFloat(in.Value), nullStr(strings.ToUpper(strings.TrimSpace(in.Currency))),
		nullStr(in.OfferKey), nullStr(in.OfferName), nullStr(in.Source), nullStr(in.SourceSite),
		nullStr(in.SenderIdentity), nullStr(in.Owner), nullStr(in.ExpectedCloseDate),
		nullStr(in.ClosedAt), nullStr(in.LostReason),
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO crm_opportunity_stage_history
			(project_id, opportunity_id, from_stage_id, to_stage_id,
			 from_status, to_status, note, source, changed_at)
		 VALUES (?, ?, NULL, ?, NULL, ?, ?, ?, CURRENT_TIMESTAMP)`,
		pid, id, in.StageID, status, nullStr(in.StageChangeNote), nullStr(in.StageChangeSource),
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbOpportunityGet(db, pid, id)
}

func dbOpportunityGet(db *sql.DB, pid string, id int64) (*Opportunity, error) {
	row := db.QueryRow(opportunitySelectSQL()+` WHERE o.project_id = ? AND o.id = ?`, pid, id)
	o, err := scanOpportunity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return o, err
}

func opportunitySelectSQL() string {
	return `SELECT o.id, o.project_id, o.contact_id, o.pipeline_id, o.stage_id,
			o.title, o.status, o.value, COALESCE(o.currency,''),
			COALESCE(o.offer_key,''), COALESCE(o.offer_name,''), COALESCE(o.source,''),
			COALESCE(o.source_site,''), COALESCE(o.sender_identity,''), COALESCE(o.owner,''),
			COALESCE(o.expected_close_date,''), COALESCE(o.closed_at,''), COALESCE(o.lost_reason,''),
			o.created_at, o.updated_at, COALESCE(o.archived_at,''),
			p.name, s.name, s.category, s.probability,
			COALESCE(c.display_name,''), COALESCE(c.primary_email,''), COALESCE(c.primary_phone,'')
		FROM crm_opportunities o
		JOIN crm_pipelines p ON p.project_id = o.project_id AND p.id = o.pipeline_id
		JOIN crm_pipeline_stages s ON s.project_id = o.project_id AND s.id = o.stage_id
		JOIN contacts c ON c.project_id = o.project_id AND c.id = o.contact_id`
}

func scanOpportunity(row interface{ Scan(...any) error }) (*Opportunity, error) {
	o := &Opportunity{}
	var value, stageProbability sql.NullFloat64
	err := row.Scan(
		&o.ID, &o.ProjectID, &o.ContactID, &o.PipelineID, &o.StageID,
		&o.Title, &o.Status, &value, &o.Currency,
		&o.OfferKey, &o.OfferName, &o.Source, &o.SourceSite, &o.SenderIdentity, &o.Owner,
		&o.ExpectedCloseDate, &o.ClosedAt, &o.LostReason,
		&o.CreatedAt, &o.UpdatedAt, &o.ArchivedAt,
		&o.PipelineName, &o.StageName, &o.StageCategory, &stageProbability,
		&o.ContactName, &o.ContactEmail, &o.ContactPhone,
	)
	if err != nil {
		return nil, err
	}
	if value.Valid {
		o.Value = &value.Float64
	}
	if stageProbability.Valid {
		o.StageProbability = &stageProbability.Float64
	}
	return o, nil
}

func dbOpportunityUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Opportunity, *Opportunity, error) {
	before, err := dbOpportunityGet(db, pid, id)
	if err != nil || before == nil {
		return nil, nil, err
	}
	targetStageID := before.StageID
	targetStage, err := dbPipelineStageGet(db, pid, targetStageID)
	if err != nil {
		return nil, nil, err
	}
	if targetStage == nil {
		return nil, nil, errors.New("opportunity stage not found")
	}
	if raw, ok := patch["stage_id"]; ok {
		targetStageID = int64FromAny(raw)
		targetStage, err = dbPipelineStageGet(db, pid, targetStageID)
		if err != nil {
			return nil, nil, err
		}
		if targetStage == nil || targetStage.PipelineID != before.PipelineID || targetStage.ArchivedAt != "" {
			return nil, nil, errors.New("stage not found in opportunity pipeline")
		}
	}
	targetStatus := before.Status
	if raw, ok := patch["status"]; ok {
		targetStatus = strings.ToLower(strings.TrimSpace(strFromAny(raw)))
		if !opportunityStatuses[targetStatus] {
			return nil, nil, fmt.Errorf("invalid status %q", targetStatus)
		}
	} else if targetStageID != before.StageID {
		targetStatus = statusForStageCategory(targetStage.Category)
	}
	if expected := statusForStageCategory(targetStage.Category); targetStatus != expected {
		return nil, nil, fmt.Errorf("status %q does not match stage category %q", targetStatus, targetStage.Category)
	}

	sets := []string{}
	args := []any{}
	note := strFromAny(patch["note"])
	source := strFromAny(patch["source"])
	for k, v := range patch {
		switch k {
		case "title", "currency", "offer_key", "offer_name", "source", "source_site",
			"sender_identity", "owner", "expected_close_date":
			if k == "source" {
				continue
			}
			sets = append(sets, k+" = ?")
			if k == "currency" {
				args = append(args, strings.ToUpper(strings.TrimSpace(strFromAny(v))))
			} else {
				args = append(args, nullStr(strFromAny(v)))
			}
		case "value":
			sets = append(sets, "value = ?")
			args = append(args, nullableFloat(floatFromAnyPtr(v)))
		}
	}
	if targetStageID != before.StageID {
		sets = append(sets, "stage_id = ?")
		args = append(args, targetStageID)
	}
	if targetStatus != before.Status {
		sets = append(sets, "status = ?")
		args = append(args, targetStatus)
	}
	if targetStatus == opportunityStatusOpen {
		sets = append(sets, "closed_at = NULL", "lost_reason = NULL")
	} else {
		closedAt := strings.TrimSpace(strFromAny(patch["closed_at"]))
		if closedAt == "" && before.Status == opportunityStatusOpen {
			closedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if closedAt != "" {
			sets = append(sets, "closed_at = ?")
			args = append(args, closedAt)
		}
		if targetStatus == opportunityStatusLost {
			if _, ok := patch["lost_reason"]; ok {
				sets = append(sets, "lost_reason = ?")
				args = append(args, nullStr(strFromAny(patch["lost_reason"])))
			}
		} else {
			sets = append(sets, "lost_reason = NULL")
		}
	}
	if len(sets) == 0 {
		return before, before, nil
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	updateArgs := append(append([]any{}, args...), pid, id)
	if _, err := tx.Exec(
		`UPDATE crm_opportunities SET `+strings.Join(sets, ", ")+`
		 WHERE project_id = ? AND id = ? AND archived_at IS NULL`,
		updateArgs...,
	); err != nil {
		return nil, nil, err
	}
	if targetStageID != before.StageID || targetStatus != before.Status {
		if _, err := tx.Exec(
			`INSERT INTO crm_opportunity_stage_history
				(project_id, opportunity_id, from_stage_id, to_stage_id,
				 from_status, to_status, note, source, changed_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
			pid, id, nullableInt64(before.StageID), targetStageID,
			before.Status, targetStatus, nullStr(note), nullStr(source),
		); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	after, err := dbOpportunityGet(db, pid, id)
	if err != nil {
		return nil, nil, err
	}
	return before, after, nil
}

func dbOpportunityLogStage(db *sql.DB, pid string, id, fromStage, toStage int64, fromStatus, toStatus, note, source string) error {
	_, err := db.Exec(
		`INSERT INTO crm_opportunity_stage_history
			(project_id, opportunity_id, from_stage_id, to_stage_id,
			 from_status, to_status, note, source, changed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		pid, id, nullableInt64(fromStage), toStage, nullStr(fromStatus), toStatus, nullStr(note), nullStr(source),
	)
	return err
}

func dbOpportunityHistory(db *sql.DB, pid string, id int64) ([]*OpportunityStageHistory, error) {
	rows, err := db.Query(
		`SELECT id, opportunity_id, COALESCE(from_stage_id,0), to_stage_id,
				COALESCE(from_status,''), to_status, COALESCE(note,''), COALESCE(source,''), changed_at
		 FROM crm_opportunity_stage_history
		 WHERE project_id = ? AND opportunity_id = ?
		 ORDER BY changed_at DESC, id DESC`,
		pid, id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OpportunityStageHistory{}
	for rows.Next() {
		h := &OpportunityStageHistory{}
		if err := rows.Scan(&h.ID, &h.OpportunityID, &h.FromStageID, &h.ToStageID,
			&h.FromStatus, &h.ToStatus, &h.Note, &h.Source, &h.ChangedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func dbOpportunitiesSearch(db *sql.DB, pid string, args map[string]any) ([]*Opportunity, int, error) {
	where := []string{"o.project_id = ?", "o.archived_at IS NULL", "c.deleted_at IS NULL", "c.status != 'merged'"}
	qargs := []any{pid}
	if status := strings.ToLower(strings.TrimSpace(strArg(args, "status"))); status != "" && status != "all" {
		if !opportunityStatuses[status] {
			return nil, 0, fmt.Errorf("invalid status %q", status)
		}
		where = append(where, "o.status = ?")
		qargs = append(qargs, status)
	}
	for _, f := range []struct {
		key string
		col string
	}{
		{"contact_id", "o.contact_id"},
		{"pipeline_id", "o.pipeline_id"},
		{"stage_id", "o.stage_id"},
	} {
		if id := int64Arg(args, f.key); id != 0 {
			where = append(where, f.col+" = ?")
			qargs = append(qargs, id)
		}
	}
	for _, f := range []struct {
		key string
		col string
	}{
		{"offer_key", "o.offer_key"},
		{"sender_identity", "o.sender_identity"},
		{"source_site", "o.source_site"},
		{"owner", "o.owner"},
	} {
		if v := strings.TrimSpace(strArg(args, f.key)); v != "" {
			where = append(where, f.col+" = ?")
			qargs = append(qargs, v)
		}
	}
	if listID := int64Arg(args, "list_id"); listID != 0 {
		where = append(where, `EXISTS (
			SELECT 1 FROM contact_list_members lm
			WHERE lm.project_id = o.project_id AND lm.contact_id = o.contact_id AND lm.list_id = ?
		)`)
		qargs = append(qargs, listID)
	}
	if tag := strings.TrimSpace(strArg(args, "tag")); tag != "" {
		where = append(where, `EXISTS (
			SELECT 1 FROM contact_tags t
			WHERE t.project_id = o.project_id AND t.contact_id = o.contact_id AND t.tag_name = ?
		)`)
		qargs = append(qargs, tag)
	}
	if q := strings.TrimSpace(strArg(args, "q")); q != "" {
		where = append(where, `(o.title LIKE ? OR COALESCE(o.offer_name,'') LIKE ? OR COALESCE(c.display_name,'') LIKE ? OR COALESCE(c.primary_email,'') LIKE ?)`)
		like := "%" + q + "%"
		qargs = append(qargs, like, like, like, like)
	}
	whereSQL := strings.Join(where, " AND ")
	var total int
	if err := db.QueryRow(
		`SELECT COUNT(*)
		 FROM crm_opportunities o
		 JOIN contacts c ON c.project_id = o.project_id AND c.id = o.contact_id
		 WHERE `+whereSQL,
		qargs...,
	).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := intArg(args, "offset", 0)
	qargs = append(qargs, limit, offset)
	rows, err := db.Query(opportunitySelectSQL()+` WHERE `+whereSQL+`
		ORDER BY o.updated_at DESC, o.id DESC LIMIT ? OFFSET ?`, qargs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Opportunity{}
	for rows.Next() {
		o, err := scanOpportunity(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func emitOpportunity(ctx *sdk.AppCtx, topic string, o *Opportunity, previousStageID int64) {
	if ctx == nil || o == nil {
		return
	}
	payload := map[string]any{
		"opportunity_id": o.ID,
		"contact_id":     o.ContactID,
		"pipeline_id":    o.PipelineID,
		"stage_id":       o.StageID,
		"status":         o.Status,
		"title":          o.Title,
		// stage_category (open|won|lost) rides along because the record
		// already carries it from the stage join — it saves every
		// consumer a stage lookup just to tell whether the deal is live.
		"stage_category": o.StageCategory,
	}
	// value, currency and stage_probability are genuinely optional:
	// an opportunity may carry no amount and a stage may have no
	// probability configured. Omit them rather than zero-filling — a
	// consumer summing pipeline value has to be able to tell "no amount
	// recorded" from "worth nothing".
	if o.Value != nil {
		payload["value"] = *o.Value
	}
	if o.Currency != "" {
		payload["currency"] = o.Currency
	}
	if o.StageProbability != nil {
		payload["stage_probability"] = *o.StageProbability
	}
	if previousStageID != 0 {
		payload["previous_stage_id"] = previousStageID
	}
	ctx.Emit(topic, payload)
}

func nullableFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func floatFromAnyPtr(v any) *float64 {
	switch x := v.(type) {
	case nil:
		return nil
	case float64:
		return &x
	case float32:
		f := float64(x)
		return &f
	case int:
		f := float64(x)
		return &f
	case int64:
		f := float64(x)
		return &f
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return nil
		}
		return &f
	default:
		return nil
	}
}

func pipelineStagesFromAny(raw any) []*PipelineStage {
	items, _ := raw.([]any)
	out := []*PipelineStage{}
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		st := &PipelineStage{
			Name:     strings.TrimSpace(strFromAny(m["name"])),
			Position: int(int64FromAny(m["position"])),
			Category: strFromAny(m["category"]),
		}
		if st.Position == 0 {
			st.Position = i + 1
		}
		st.Probability = floatFromAnyPtr(m["probability"])
		out = append(out, st)
	}
	return out
}

func opportunityCreateInputFromArgs(args map[string]any) opportunityCreateInput {
	return opportunityCreateInput{
		ContactID:         int64Arg(args, "contact_id"),
		PipelineID:        int64Arg(args, "pipeline_id"),
		StageID:           int64Arg(args, "stage_id"),
		Title:             strArg(args, "title"),
		Status:            strArg(args, "status"),
		Value:             floatFromAnyPtr(args["value"]),
		Currency:          strArg(args, "currency"),
		OfferKey:          strArg(args, "offer_key"),
		OfferName:         strArg(args, "offer_name"),
		Source:            strArg(args, "source"),
		SourceSite:        strArg(args, "source_site"),
		SenderIdentity:    strArg(args, "sender_identity"),
		Owner:             strArg(args, "owner"),
		ExpectedCloseDate: strArg(args, "expected_close_date"),
		ClosedAt:          strArg(args, "closed_at"),
		LostReason:        strArg(args, "lost_reason"),
		StageChangeNote:   strArg(args, "note"),
		StageChangeSource: strArg(args, "source"),
	}
}

func (a *App) toolPipelinesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	includeArchived, _ := args["include_archived"].(bool)
	out, err := dbPipelinesList(ctx.AppDB(), pid, includeArchived)
	if err != nil {
		return nil, err
	}
	return map[string]any{"pipelines": out, "count": len(out)}, nil
}

func (a *App) toolPipelineCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p := &Pipeline{Name: strArg(args, "name"), Description: strArg(args, "description")}
	if v, ok := args["is_default"].(bool); ok {
		p.IsDefault = v
	}
	out, err := dbPipelineCreate(ctx.AppDB(), pid, p, pipelineStagesFromAny(args["stages"]))
	if err != nil {
		return nil, err
	}
	ctx.Emit("pipeline.created", map[string]any{"pipeline_id": out.ID})
	return map[string]any{"pipeline": out}, nil
}

func (a *App) toolPipelineStageCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	st := &PipelineStage{
		PipelineID:  int64Arg(args, "pipeline_id"),
		Name:        strArg(args, "name"),
		Position:    intArg(args, "position", 0),
		Category:    strArg(args, "category"),
		Probability: floatFromAnyPtr(args["probability"]),
	}
	out, err := dbPipelineStageCreate(ctx.AppDB(), pid, st)
	if err != nil {
		return nil, err
	}
	ctx.Emit("pipeline.stage.created", map[string]any{"pipeline_id": out.PipelineID, "stage_id": out.ID})
	return map[string]any{"stage": out}, nil
}

func (a *App) toolPipelineStageUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	patch, _ := args["patch"].(map[string]any)
	if id == 0 || patch == nil {
		return nil, errors.New("id and patch required")
	}
	out, err := dbPipelineStageUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		return nil, err
	}
	ctx.Emit("pipeline.stage.updated", map[string]any{"pipeline_id": out.PipelineID, "stage_id": out.ID})
	return map[string]any{"stage": out}, nil
}

func (a *App) toolPipelineStageArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	if err := dbPipelineStageArchive(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	ctx.Emit("pipeline.stage.updated", map[string]any{"stage_id": id, "archived": true})
	return map[string]any{"archived": true, "id": id}, nil
}

func (a *App) toolOpportunitiesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbOpportunityCreate(ctx.AppDB(), pid, opportunityCreateInputFromArgs(args))
	if err != nil {
		return nil, err
	}
	emitOpportunity(ctx, "opportunity.created", out, 0)
	return map[string]any{"opportunity": out}, nil
}

func (a *App) toolOpportunitiesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "opportunity_id")
	if id == 0 {
		id = int64Arg(args, "id")
	}
	patch, _ := args["patch"].(map[string]any)
	if patch == nil {
		patch = map[string]any{}
		for k, v := range args {
			if k == "id" || k == "opportunity_id" || k == "_project_id" {
				continue
			}
			patch[k] = v
		}
	}
	before, after, err := dbOpportunityUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		return nil, err
	}
	if after == nil {
		return map[string]any{"opportunity": nil, "found": false}, nil
	}
	emitOpportunity(ctx, "opportunity.updated", after, 0)
	if before != nil && after.StageID != before.StageID {
		emitOpportunity(ctx, "opportunity.stage.changed", after, before.StageID)
	}
	if before != nil && after.Status != before.Status {
		emitOpportunity(ctx, "opportunity.status.changed", after, before.StageID)
		if after.Status == opportunityStatusWon {
			emitOpportunity(ctx, "opportunity.won", after, before.StageID)
		}
		if after.Status == opportunityStatusLost {
			emitOpportunity(ctx, "opportunity.lost", after, before.StageID)
		}
	}
	return map[string]any{"opportunity": after, "found": true}, nil
}

func (a *App) toolOpportunitiesSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, total, err := dbOpportunitiesSearch(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"opportunities": out, "count": len(out), "total": total, "offset": intArg(args, "offset", 0)}, nil
}

func (a *App) toolOpportunitiesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "opportunity_id")
	if id == 0 {
		id = int64Arg(args, "id")
	}
	o, err := dbOpportunityGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return map[string]any{"opportunity": nil, "found": false}, nil
	}
	hist, err := dbOpportunityHistory(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	contact, err := dbGetByID(ctx.AppDB(), pid, o.ContactID)
	if err != nil {
		return nil, err
	}
	activities, err := dbActivities(ctx.AppDB(), pid, o.ContactID, intArg(args, "activity_limit", 10))
	if err != nil {
		return nil, err
	}
	conversations, err := dbConversationsList(ctx.AppDB(), pid, o.ContactID, "", "", intArg(args, "conversation_limit", 10))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"opportunity":   o,
		"history":       hist,
		"contact":       contact,
		"activities":    activities,
		"conversations": conversations,
		"found":         true,
	}, nil
}

func (a *App) handleHTTPPipelines(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPPipelinesGet(w, r)
	case http.MethodPost:
		a.handleHTTPPipelineCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPPipelineItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/pipelines/")
	parts := strings.Split(rest, "/")
	pipelineID, _ := strconv.ParseInt(parts[0], 10, 64)
	if pipelineID == 0 {
		httpErr(w, http.StatusBadRequest, "pipeline id required")
		return
	}
	if len(parts) >= 2 && parts[1] == "stages" {
		if len(parts) == 2 && r.Method == http.MethodPost {
			a.handleHTTPPipelineStageCreate(w, r, pipelineID)
			return
		}
		if len(parts) >= 3 {
			stageID, _ := strconv.ParseInt(parts[2], 10, 64)
			switch r.Method {
			case http.MethodPatch, http.MethodPut:
				a.handleHTTPPipelineStageUpdate(w, r, stageID)
			case http.MethodDelete:
				a.handleHTTPPipelineStageArchive(w, r, stageID)
			default:
				httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := dbPipelineGet(globalCtx.AppDB(), pid, pipelineID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpJSON(w, map[string]any{"pipeline": p})
}

func (a *App) handleHTTPPipelinesGet(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	includeArchived := r.URL.Query().Get("include_archived") == "true"
	out, err := dbPipelinesList(globalCtx.AppDB(), pid, includeArchived)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"pipelines": out, "count": len(out)})
}

func (a *App) handleHTTPPipelineCreate(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := decodeJSONBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	p := &Pipeline{Name: strArg(body, "name"), Description: strArg(body, "description")}
	if v, ok := body["is_default"].(bool); ok {
		p.IsDefault = v
	}
	out, err := dbPipelineCreate(globalCtx.AppDB(), pid, p, pipelineStagesFromAny(body["stages"]))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	globalCtx.Emit("pipeline.created", map[string]any{"pipeline_id": out.ID})
	httpJSON(w, map[string]any{"pipeline": out})
}

func (a *App) handleHTTPPipelineStageCreate(w http.ResponseWriter, r *http.Request, pipelineID int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := decodeJSONBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	st := &PipelineStage{
		PipelineID:  pipelineID,
		Name:        strArg(body, "name"),
		Position:    intArg(body, "position", 0),
		Category:    strArg(body, "category"),
		Probability: floatFromAnyPtr(body["probability"]),
	}
	out, err := dbPipelineStageCreate(globalCtx.AppDB(), pid, st)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	globalCtx.Emit("pipeline.stage.created", map[string]any{"pipeline_id": out.PipelineID, "stage_id": out.ID})
	httpJSON(w, map[string]any{"stage": out})
}

func (a *App) handleHTTPPipelineStageUpdate(w http.ResponseWriter, r *http.Request, stageID int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var patch map[string]any
	if err := decodeJSONBody(w, r, &patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	out, err := dbPipelineStageUpdate(globalCtx.AppDB(), pid, stageID, patch)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	globalCtx.Emit("pipeline.stage.updated", map[string]any{"pipeline_id": out.PipelineID, "stage_id": out.ID})
	httpJSON(w, map[string]any{"stage": out})
}

func (a *App) handleHTTPPipelineStageArchive(w http.ResponseWriter, r *http.Request, stageID int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := dbPipelineStageArchive(globalCtx.AppDB(), pid, stageID); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	globalCtx.Emit("pipeline.stage.updated", map[string]any{"stage_id": stageID, "archived": true})
	httpJSON(w, map[string]any{"archived": true, "id": stageID})
}

func (a *App) handleHTTPOpportunities(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPOpportunitiesSearch(w, r)
	case http.MethodPost:
		a.handleHTTPOpportunityCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPOpportunityItem(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/opportunities/"), 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPOpportunityGet(w, r, id)
	case http.MethodPatch, http.MethodPut:
		a.handleHTTPOpportunityUpdate(w, r, id)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPOpportunitiesSearch(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args := map[string]any{}
	for _, k := range []string{"q", "status", "offer_key", "sender_identity", "source_site", "owner", "tag"} {
		if v := r.URL.Query().Get(k); v != "" {
			args[k] = v
		}
	}
	for _, k := range []string{"contact_id", "pipeline_id", "stage_id", "list_id", "limit", "offset"} {
		if v := r.URL.Query().Get(k); v != "" {
			args[k] = v
		}
	}
	out, total, err := dbOpportunitiesSearch(globalCtx.AppDB(), pid, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"opportunities": out, "count": len(out), "total": total})
}

func (a *App) handleHTTPOpportunityCreate(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := decodeJSONBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	out, err := dbOpportunityCreate(globalCtx.AppDB(), pid, opportunityCreateInputFromArgs(body))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitOpportunity(globalCtx, "opportunity.created", out, 0)
	httpJSON(w, map[string]any{"opportunity": out})
}

func (a *App) handleHTTPOpportunityGet(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	o, err := dbOpportunityGet(globalCtx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if o == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	hist, err := dbOpportunityHistory(globalCtx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"opportunity": o, "history": hist})
}

func (a *App) handleHTTPOpportunityUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var patch map[string]any
	if err := decodeJSONBody(w, r, &patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	before, after, err := dbOpportunityUpdate(globalCtx.AppDB(), pid, id, patch)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if after == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	emitOpportunity(globalCtx, "opportunity.updated", after, 0)
	if before != nil && after.StageID != before.StageID {
		emitOpportunity(globalCtx, "opportunity.stage.changed", after, before.StageID)
	}
	if before != nil && after.Status != before.Status {
		emitOpportunity(globalCtx, "opportunity.status.changed", after, before.StageID)
		if after.Status == opportunityStatusWon {
			emitOpportunity(globalCtx, "opportunity.won", after, before.StageID)
		}
		if after.Status == opportunityStatusLost {
			emitOpportunity(globalCtx, "opportunity.lost", after, before.StageID)
		}
	}
	httpJSON(w, map[string]any{"opportunity": after})
}

func (a *App) handleHTTPContactOpportunities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/contacts/")
	parts := strings.SplitN(rest, "/", 2)
	cid, _ := strconv.ParseInt(parts[0], 10, 64)
	if cid == 0 {
		httpErr(w, http.StatusBadRequest, "contact id required")
		return
	}
	args := map[string]any{"contact_id": cid}
	if status := r.URL.Query().Get("status"); status != "" {
		args["status"] = status
	}
	out, total, err := dbOpportunitiesSearch(globalCtx.AppDB(), pid, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"opportunities": out, "count": len(out), "total": total})
}
