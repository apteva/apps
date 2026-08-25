package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ObjectiveMetricQuery struct {
	Aggregation       string         `json:"aggregation"`
	App               string         `json:"app,omitempty"`
	Topic             string         `json:"topic,omitempty"`
	Source            string         `json:"source,omitempty"`
	Value             string         `json:"value,omitempty"`
	By                string         `json:"by,omitempty"`
	Where             map[string]any `json:"where,omitempty"`
	CurrencyField     string         `json:"currency_field,omitempty"`
	ReportingCurrency string         `json:"reporting_currency,omitempty"`
	AmountUnit        string         `json:"amount_unit,omitempty"`
	RateDateField     string         `json:"rate_date_field,omitempty"`

	// These selectors are owned by the target and platform context. Keeping
	// them in the decoder lets us reject attempts to smuggle scope into a query.
	ProjectID string `json:"project_id,omitempty"`
	Since     int64  `json:"since,omitempty"`
	Until     int64  `json:"until,omitempty"`
	Window    string `json:"window,omitempty"`
}

type ObjectiveTarget struct {
	ID           int64                `json:"id"`
	ObjectiveID  int64                `json:"objective_id,omitempty"`
	Name         string               `json:"name"`
	MetricKey    string               `json:"metric_key"`
	TargetValue  float64              `json:"target_value"`
	Unit         string               `json:"unit"`
	Currency     string               `json:"currency,omitempty"`
	Direction    string               `json:"direction"`
	PeriodStart  int64                `json:"period_start"`
	PeriodEnd    int64                `json:"period_end"`
	Timezone     string               `json:"timezone"`
	Query        ObjectiveMetricQuery `json:"query"`
	CreatedAt    int64                `json:"created_at,omitempty"`
	UpdatedAt    int64                `json:"updated_at,omitempty"`
	LastProgress *TargetProgress      `json:"last_progress,omitempty"`
}

type Objective struct {
	ID          int64             `json:"id"`
	ProjectID   string            `json:"project_id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	OwnerType   string            `json:"owner_type,omitempty"`
	OwnerID     string            `json:"owner_id,omitempty"`
	Status      string            `json:"status"`
	CreatedBy   string            `json:"created_by,omitempty"`
	CreatedAt   int64             `json:"created_at"`
	UpdatedAt   int64             `json:"updated_at"`
	ArchivedAt  *int64            `json:"archived_at,omitempty"`
	Targets     []ObjectiveTarget `json:"targets"`
}

type ObjectiveWrite struct {
	ProjectID   string            `json:"project_id,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	OwnerType   string            `json:"owner_type,omitempty"`
	OwnerID     string            `json:"owner_id,omitempty"`
	Status      string            `json:"status,omitempty"`
	CreatedBy   string            `json:"created_by,omitempty"`
	Targets     []ObjectiveTarget `json:"targets"`
}

type TargetProgress struct {
	TargetID    int64                `json:"target_id"`
	ObjectiveID int64                `json:"objective_id"`
	Name        string               `json:"name"`
	ActualValue *float64             `json:"actual_value,omitempty"`
	TargetValue float64              `json:"target_value"`
	Unit        string               `json:"unit"`
	Currency    string               `json:"currency,omitempty"`
	Direction   string               `json:"direction"`
	ProgressPct *float64             `json:"progress_percent,omitempty"`
	Achieved    bool                 `json:"achieved"`
	PeriodState string               `json:"period_state"`
	PeriodStart int64                `json:"period_start"`
	PeriodEnd   int64                `json:"period_end"`
	Query       ObjectiveMetricQuery `json:"query"`
	Status      string               `json:"status"`
	Error       string               `json:"error,omitempty"`
	Details     map[string]any       `json:"details,omitempty"`
	MeasuredAt  *int64               `json:"measured_at,omitempty"`
	UpdatedAt   int64                `json:"updated_at"`
}

type ObjectiveMetricDefinition struct {
	Key         string               `json:"key"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	DefaultUnit string               `json:"default_unit"`
	Query       ObjectiveMetricQuery `json:"query"`
}

func objectiveMetricDefinitions() []ObjectiveMetricDefinition {
	return []ObjectiveMetricDefinition{
		{Key: "events", Name: "Events", Description: "Count matching events.", DefaultUnit: "count", Query: ObjectiveMetricQuery{Aggregation: "count"}},
		{Key: "page_views", Name: "Page views", Description: "Count page_view events.", DefaultUnit: "count", Query: ObjectiveMetricQuery{Aggregation: "count", Topic: "page_view"}},
		{Key: "unique_sessions", Name: "Unique sessions", Description: "Count distinct sessions for matching events.", DefaultUnit: "count", Query: ObjectiveMetricQuery{Aggregation: "distinct", By: "session_id"}},
		{Key: "custom_sum", Name: "Sum a property", Description: "Sum a numeric event property such as props.amount_usd.", DefaultUnit: "number", Query: ObjectiveMetricQuery{Aggregation: "sum", Value: "props.value"}},
		{Key: "money_sum", Name: "Money total", Description: "Convert mixed-currency event amounts into one reporting currency and sum them without changing source events.", DefaultUnit: "money", Query: ObjectiveMetricQuery{Aggregation: "sum_money", Value: "props.amount_cents", CurrencyField: "props.currency", ReportingCurrency: "EUR", AmountUnit: "minor"}},
		{Key: "custom_average", Name: "Average a property", Description: "Average a numeric event property.", DefaultUnit: "number", Query: ObjectiveMetricQuery{Aggregation: "average", Value: "props.value"}},
		{Key: "custom_min", Name: "Minimum property value", Description: "Measure the minimum numeric value in the target period.", DefaultUnit: "number", Query: ObjectiveMetricQuery{Aggregation: "min", Value: "props.value"}},
		{Key: "custom_max", Name: "Maximum property value", Description: "Measure the maximum numeric value in the target period.", DefaultUnit: "number", Query: ObjectiveMetricQuery{Aggregation: "max", Value: "props.value"}},
		{Key: "custom_latest", Name: "Latest property value", Description: "Measure the latest numeric observation in the target period.", DefaultUnit: "number", Query: ObjectiveMetricQuery{Aggregation: "latest", Value: "props.value"}},
		{Key: "custom_change", Name: "Property change", Description: "Measure latest minus first numeric observation in the target period.", DefaultUnit: "number", Query: ObjectiveMetricQuery{Aggregation: "change", Value: "props.value"}},
	}
}

func validateObjectiveWrite(in *ObjectiveWrite, requireTargets bool) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.OwnerType = strings.TrimSpace(in.OwnerType)
	in.OwnerID = strings.TrimSpace(in.OwnerID)
	in.Status = strings.TrimSpace(in.Status)
	in.CreatedBy = strings.TrimSpace(in.CreatedBy)
	if in.Name == "" {
		return errors.New("name required")
	}
	if len(in.Name) > 160 || len(in.Description) > 2000 {
		return errors.New("name or description too long")
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if !oneOf(in.Status, "draft", "active", "paused") {
		return errors.New("status must be draft, active or paused")
	}
	if !oneOf(in.OwnerType, "", "user", "agent", "team") {
		return errors.New("owner_type must be user, agent or team")
	}
	if in.OwnerType == "" && in.OwnerID != "" {
		return errors.New("owner_type required when owner_id is set")
	}
	if requireTargets && len(in.Targets) == 0 {
		return errors.New("at least one target required")
	}
	if len(in.Targets) > 25 {
		return errors.New("an objective may have at most 25 targets")
	}
	for i := range in.Targets {
		if err := validateObjectiveTarget(&in.Targets[i]); err != nil {
			return fmt.Errorf("target %d: %w", i+1, err)
		}
	}
	return nil
}

func validateObjectiveTarget(t *ObjectiveTarget) error {
	t.Name = strings.TrimSpace(t.Name)
	t.MetricKey = strings.TrimSpace(t.MetricKey)
	t.Unit = strings.ToLower(strings.TrimSpace(t.Unit))
	t.Currency = strings.ToUpper(strings.TrimSpace(t.Currency))
	t.Direction = strings.TrimSpace(t.Direction)
	t.Timezone = strings.TrimSpace(t.Timezone)
	if t.Name == "" {
		return errors.New("name required")
	}
	if len(t.Name) > 160 {
		return errors.New("name too long")
	}
	if t.MetricKey == "" {
		t.MetricKey = "custom"
	}
	if math.IsNaN(t.TargetValue) || math.IsInf(t.TargetValue, 0) {
		return errors.New("target_value must be finite")
	}
	if !oneOf(t.Unit, "money", "count", "percent", "number") {
		return errors.New("unit must be money, count, percent or number")
	}
	if t.Unit == "money" {
		if len(t.Currency) != 3 {
			return errors.New("a three-letter currency is required for money targets")
		}
		for _, r := range t.Currency {
			if r < 'A' || r > 'Z' {
				return errors.New("currency must be a three-letter ISO code")
			}
		}
	} else {
		t.Currency = ""
	}
	if !oneOf(t.Direction, "at_least", "at_most") {
		return errors.New("direction must be at_least or at_most")
	}
	if t.PeriodStart <= 0 || t.PeriodEnd <= t.PeriodStart {
		return errors.New("period_end must be after period_start")
	}
	if t.Timezone == "" {
		t.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(t.Timezone); err != nil {
		return fmt.Errorf("invalid timezone %q", t.Timezone)
	}
	if err := validateObjectiveMetricQuery(&t.Query); err != nil {
		return err
	}
	if t.Query.Aggregation == "sum_money" {
		if t.Unit != "money" {
			return errors.New("sum_money targets must use the money unit")
		}
		if t.Currency != t.Query.ReportingCurrency {
			return errors.New("money target currency must match query reporting_currency")
		}
	}
	return nil
}

func validateObjectiveMetricQuery(q *ObjectiveMetricQuery) error {
	q.Aggregation = strings.ToLower(strings.TrimSpace(q.Aggregation))
	if q.Aggregation == "avg" {
		q.Aggregation = "average"
	}
	q.App = strings.TrimSpace(q.App)
	q.Topic = strings.TrimSpace(q.Topic)
	q.Source = strings.TrimSpace(q.Source)
	q.Value = strings.TrimSpace(q.Value)
	q.By = strings.TrimSpace(q.By)
	q.CurrencyField = strings.TrimSpace(q.CurrencyField)
	q.ReportingCurrency = strings.ToUpper(strings.TrimSpace(q.ReportingCurrency))
	q.AmountUnit = strings.ToLower(strings.TrimSpace(q.AmountUnit))
	q.RateDateField = strings.TrimSpace(q.RateDateField)
	if q.ProjectID != "" || q.Since != 0 || q.Until != 0 || q.Window != "" {
		return errors.New("project_id and time range are assigned by the objective target")
	}
	if !oneOf(q.Aggregation, "count", "distinct", "sum", "sum_money", "average", "min", "max", "latest", "change") {
		return errors.New("query aggregation must be count, distinct, sum_money, sum, average, min, max, latest or change")
	}
	if q.Source != "" && !oneOf(q.Source, "track", "auto") {
		return errors.New("query source must be track or auto")
	}
	switch q.Aggregation {
	case "count":
		q.Value, q.By = "", ""
		q.CurrencyField, q.ReportingCurrency, q.AmountUnit, q.RateDateField = "", "", "", ""
	case "sum", "average", "min", "max", "latest", "change":
		if _, _, ok := numericValueExtract(q.Value); !ok {
			return fmt.Errorf("%s query value must be a numeric event field or props.X", q.Aggregation)
		}
		q.By = ""
		q.CurrencyField, q.ReportingCurrency, q.AmountUnit, q.RateDateField = "", "", "", ""
	case "sum_money":
		if _, err := moneyQueryFromConfig(map[string]any{
			"value": q.Value, "currency_field": q.CurrencyField,
			"reporting_currency": q.ReportingCurrency, "amount_unit": q.AmountUnit,
			"rate_date_field": q.RateDateField,
		}); err != nil {
			return err
		}
		q.By = ""
	case "distinct":
		if _, ok := dashboardGroupExpr(q.By); !ok || q.By == "project_id" {
			return errors.New("distinct query by must be session_id, user_id, app, topic, source or props.X")
		}
		q.Value = ""
		q.CurrencyField, q.ReportingCurrency, q.AmountUnit, q.RateDateField = "", "", "", ""
	}
	for key := range q.Where {
		if _, ok := propsExtract(key); !ok {
			return fmt.Errorf("where key %q must be props.X", key)
		}
	}
	q.ProjectID, q.Since, q.Until, q.Window = "", 0, 0, ""
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func createObjective(db *sql.DB, projectID string, in ObjectiveWrite) (*Objective, error) {
	if err := validateObjectiveWrite(&in, true); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO objectives
		(project_id, name, description, owner_type, owner_id, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, projectID, in.Name, in.Description, in.OwnerType, in.OwnerID, in.Status, in.CreatedBy, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := replaceObjectiveTargets(tx, id, in.Targets, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getObjective(db, projectID, id)
}

func updateObjective(db *sql.DB, projectID string, id int64, in ObjectiveWrite) (*Objective, error) {
	if err := validateObjectiveWrite(&in, false); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE objectives SET name=?, description=?, owner_type=?, owner_id=?, status=?, updated_at=?
		WHERE id=? AND project_id=? AND status!='archived'`, in.Name, in.Description, in.OwnerType, in.OwnerID, in.Status, now, id, projectID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	if in.Targets != nil {
		if len(in.Targets) == 0 {
			return nil, errors.New("at least one target required")
		}
		if _, err := tx.Exec(`DELETE FROM objective_targets WHERE objective_id=?`, id); err != nil {
			return nil, err
		}
		if err := replaceObjectiveTargets(tx, id, in.Targets, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getObjective(db, projectID, id)
}

func replaceObjectiveTargets(tx *sql.Tx, objectiveID int64, targets []ObjectiveTarget, now int64) error {
	for _, target := range targets {
		queryJSON, err := json.Marshal(target.Query)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO objective_targets
			(objective_id, name, metric_key, target_value, unit, currency, direction, period_start, period_end, timezone, query_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, objectiveID, target.Name, target.MetricKey, target.TargetValue,
			target.Unit, target.Currency, target.Direction, target.PeriodStart, target.PeriodEnd, target.Timezone, string(queryJSON), now, now); err != nil {
			return err
		}
	}
	return nil
}

func getObjective(db *sql.DB, projectID string, id int64) (*Objective, error) {
	var o Objective
	var archived sql.NullInt64
	err := db.QueryRow(`SELECT id, project_id, name, description, owner_type, owner_id, status, created_by, created_at, updated_at, archived_at
		FROM objectives WHERE id=? AND project_id=?`, id, projectID).Scan(&o.ID, &o.ProjectID, &o.Name, &o.Description, &o.OwnerType,
		&o.OwnerID, &o.Status, &o.CreatedBy, &o.CreatedAt, &o.UpdatedAt, &archived)
	if err != nil {
		return nil, err
	}
	if archived.Valid {
		o.ArchivedAt = &archived.Int64
	}
	targets, err := listObjectiveTargets(db, o.ID)
	if err != nil {
		return nil, err
	}
	o.Targets = targets
	return &o, nil
}

func listObjectives(db *sql.DB, projectID, status, search string, includeArchived bool, limit int) ([]Objective, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	conds := []string{"project_id=?"}
	args := []any{projectID}
	if status != "" {
		if !oneOf(status, "draft", "active", "paused", "archived") {
			return nil, errors.New("invalid status")
		}
		conds = append(conds, "status=?")
		args = append(args, status)
	} else if !includeArchived {
		conds = append(conds, "status!='archived'")
	}
	if search = strings.TrimSpace(search); search != "" {
		conds = append(conds, "(name LIKE ? OR description LIKE ?)")
		args = append(args, "%"+search+"%", "%"+search+"%")
	}
	args = append(args, limit)
	rows, err := db.Query(`SELECT id FROM objectives WHERE `+strings.Join(conds, " AND ")+` ORDER BY updated_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Objective, 0, len(ids))
	for _, id := range ids {
		o, err := getObjective(db, projectID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, nil
}

func listObjectiveTargets(db *sql.DB, objectiveID int64) ([]ObjectiveTarget, error) {
	rows, err := db.Query(`SELECT id, objective_id, name, metric_key, target_value, unit, currency, direction,
		period_start, period_end, timezone, query_json, created_at, updated_at
		FROM objective_targets WHERE objective_id=? ORDER BY period_start, id`, objectiveID)
	if err != nil {
		return nil, err
	}
	var out []ObjectiveTarget
	for rows.Next() {
		var t ObjectiveTarget
		var raw string
		if err := rows.Scan(&t.ID, &t.ObjectiveID, &t.Name, &t.MetricKey, &t.TargetValue, &t.Unit, &t.Currency,
			&t.Direction, &t.PeriodStart, &t.PeriodEnd, &t.Timezone, &raw, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &t.Query); err != nil {
			return nil, fmt.Errorf("decode objective target %d query: %w", t.ID, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// SDK app databases intentionally use one SQLite connection. Finish the
	// target cursor before reading progress rows to avoid self-deadlock.
	for i := range out {
		progress, err := cachedTargetProgress(db, out[i])
		if err != nil {
			return nil, err
		}
		out[i].LastProgress = progress
	}
	return out, nil
}

func archiveObjective(db *sql.DB, projectID string, id int64) error {
	now := time.Now().UnixMilli()
	res, err := db.Exec(`UPDATE objectives SET status='archived', archived_at=?, updated_at=? WHERE id=? AND project_id=? AND status!='archived'`, now, now, id, projectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func evaluateObjective(db *sql.DB, projectID string, objectiveID int64) ([]TargetProgress, error) {
	o, err := getObjective(db, projectID, objectiveID)
	if err != nil {
		return nil, err
	}
	out := make([]TargetProgress, 0, len(o.Targets))
	for _, target := range o.Targets {
		out = append(out, evaluateObjectiveTarget(db, projectID, target))
	}
	return out, nil
}

func evaluateObjectiveTarget(db *sql.DB, projectID string, target ObjectiveTarget) TargetProgress {
	return measureObjectiveTarget(db, projectID, target, true)
}

// measureObjectiveTarget powers live dashboard goal badges without turning a
// read-only dashboard refresh into objective-progress history writes.
func measureObjectiveTarget(db *sql.DB, projectID string, target ObjectiveTarget, persist bool) TargetProgress {
	now := time.Now().UnixMilli()
	progress := progressBase(target, now)
	f := Filter{ProjectID: projectID, App: target.Query.App, Topic: target.Query.Topic, Source: target.Query.Source,
		Since: target.PeriodStart, Until: target.PeriodEnd, Where: target.Query.Where}
	var actual float64
	details := map[string]any{"aggregation": target.Query.Aggregation}
	var err error
	switch target.Query.Aggregation {
	case "count":
		var count int64
		count, err = countEvents(db, f)
		actual = float64(count)
		details["matched_events"] = count
	case "sum", "average", "min", "max", "latest", "change":
		var result map[string]any
		result, err = numericScalarForWidget(db, f, target.Query.Value, target.Query.Aggregation)
		if result != nil {
			actual, _ = result["value"].(float64)
			details["matched_values"] = result["count"]
			for _, key := range []string{"previous", "current", "change", "change_percent"} {
				if value, ok := result[key]; ok {
					details[key] = value
				}
			}
		}
		details["value"] = target.Query.Value
	case "sum_money":
		var result map[string]any
		result, err = moneyScalarForWidget(db, f, map[string]any{
			"value":              target.Query.Value,
			"currency_field":     target.Query.CurrencyField,
			"reporting_currency": target.Query.ReportingCurrency,
			"amount_unit":        target.Query.AmountUnit,
			"rate_date_field":    target.Query.RateDateField,
		})
		if result != nil {
			actual, _ = result["value"].(float64)
			details["matched_values"] = result["count"]
			details["value"] = target.Query.Value
			details["currency"] = result["currency"]
			details["breakdown"] = result["breakdown"]
			details["fx"] = result["fx"]
		}
	case "distinct":
		var count int64
		count, err = distinctCount(db, f, target.Query.By)
		actual = float64(count)
		details["by"] = target.Query.By
	}
	if err != nil {
		progress.Status = "error"
		progress.Error = err.Error()
		if persist {
			cacheObjectiveProgressError(db, target.ID, progress.Error, now)
		}
		if cached, cacheErr := cachedTargetProgress(db, target); cacheErr == nil && cached != nil {
			cached.Status = "error"
			cached.Error = progress.Error
			cached.UpdatedAt = now
			return *cached
		}
		return progress
	}
	progress.ActualValue = &actual
	progress.Status = "ok"
	progress.Details = details
	progress.MeasuredAt = &now
	progress.ProgressPct, progress.Achieved = objectiveCompletion(actual, target.TargetValue, target.Direction)
	if persist {
		cacheObjectiveProgressOK(db, target.ID, actual, details, now)
	}
	return progress
}

type DashboardGoalProgress struct {
	ObjectiveName   string `json:"objective_name"`
	ObjectiveStatus string `json:"objective_status"`
	TargetProgress
}

type dashboardGoalTarget struct {
	ObjectiveName   string
	ObjectiveStatus string
	Target          ObjectiveTarget
}

func dashboardGoalTargetIDs(config map[string]any) ([]int64, error) {
	raw, found := config["objective_target_ids"]
	if !found || raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		switch typed := raw.(type) {
		case []int64:
			values = make([]any, len(typed))
			for i, value := range typed {
				values[i] = value
			}
		case []int:
			values = make([]any, len(typed))
			for i, value := range typed {
				values[i] = value
			}
		default:
			return nil, errors.New("objective_target_ids must be an array of positive integers")
		}
	}
	if len(values) > 10 {
		return nil, errors.New("a dashboard metric may link at most 10 objective targets")
	}
	seen := map[int64]bool{}
	out := make([]int64, 0, len(values))
	for _, rawValue := range values {
		var id int64
		switch value := rawValue.(type) {
		case int:
			id = int64(value)
		case int64:
			id = value
		case float64:
			if math.Trunc(value) != value || value > math.MaxInt64 {
				return nil, errors.New("objective_target_ids must contain positive integers")
			}
			id = int64(value)
		case json.Number:
			parsed, err := value.Int64()
			if err != nil {
				return nil, errors.New("objective_target_ids must contain positive integers")
			}
			id = parsed
		default:
			return nil, errors.New("objective_target_ids must contain positive integers")
		}
		if id <= 0 {
			return nil, errors.New("objective_target_ids must contain positive integers")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

func validateDashboardGoalLinks(db *sql.DB, projectID string, config map[string]any) error {
	ids, err := dashboardGoalTargetIDs(config)
	if err != nil || len(ids) == 0 {
		return err
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, projectID)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM objective_targets t JOIN objectives o ON o.id=t.objective_id
		WHERE o.project_id=? AND o.status!='archived' AND t.id IN (`+strings.Join(placeholders, ",")+`)`, args...).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return errors.New("every objective_target_id must reference a non-archived target in this project")
	}
	return nil
}

// dashboardGoalsForWidgets resolves all linked targets in one project-scoped
// query, evaluates each unique target once, and never updates progress cache.
func dashboardGoalsForWidgets(db *sql.DB, projectID string, widgets []DashboardWidget) (map[int64][]DashboardGoalProgress, map[int64]string, error) {
	widgetTargets := map[int64][]int64{}
	errorsByWidget := map[int64]string{}
	unique := map[int64]bool{}
	var targetIDs []int64
	for _, widget := range widgets {
		ids, err := dashboardGoalTargetIDs(widget.Config)
		if err != nil {
			errorsByWidget[widget.ID] = err.Error()
			continue
		}
		widgetTargets[widget.ID] = ids
		for _, id := range ids {
			if !unique[id] {
				unique[id] = true
				targetIDs = append(targetIDs, id)
			}
		}
	}
	if len(targetIDs) == 0 {
		return map[int64][]DashboardGoalProgress{}, errorsByWidget, nil
	}
	placeholders := make([]string, len(targetIDs))
	args := make([]any, 0, len(targetIDs)+1)
	args = append(args, projectID)
	for i, id := range targetIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := db.Query(`SELECT t.id, t.objective_id, t.name, t.metric_key, t.target_value, t.unit, t.currency,
		t.direction, t.period_start, t.period_end, t.timezone, t.query_json, t.created_at, t.updated_at,
		o.name, o.status
		FROM objective_targets t JOIN objectives o ON o.id=t.objective_id
		WHERE o.project_id=? AND o.status!='archived' AND t.id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, nil, err
	}
	loaded := map[int64]dashboardGoalTarget{}
	for rows.Next() {
		var item dashboardGoalTarget
		var rawQuery string
		target := &item.Target
		if err := rows.Scan(&target.ID, &target.ObjectiveID, &target.Name, &target.MetricKey, &target.TargetValue,
			&target.Unit, &target.Currency, &target.Direction, &target.PeriodStart, &target.PeriodEnd, &target.Timezone,
			&rawQuery, &target.CreatedAt, &target.UpdatedAt, &item.ObjectiveName, &item.ObjectiveStatus); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(rawQuery), &target.Query); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("decode objective target %d query: %w", target.ID, err)
		}
		loaded[target.ID] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	measured := map[int64]DashboardGoalProgress{}
	for id, item := range loaded {
		measured[id] = DashboardGoalProgress{
			ObjectiveName: item.ObjectiveName, ObjectiveStatus: item.ObjectiveStatus,
			TargetProgress: measureObjectiveTarget(db, projectID, item.Target, false),
		}
	}
	out := map[int64][]DashboardGoalProgress{}
	for widgetID, ids := range widgetTargets {
		for _, id := range ids {
			goal, found := measured[id]
			if !found {
				errorsByWidget[widgetID] = fmt.Sprintf("linked objective target %d is unavailable in this project", id)
				continue
			}
			out[widgetID] = append(out[widgetID], goal)
		}
	}
	return out, errorsByWidget, nil
}

func progressBase(target ObjectiveTarget, now int64) TargetProgress {
	state := "active"
	if now < target.PeriodStart {
		state = "upcoming"
	} else if now >= target.PeriodEnd {
		state = "ended"
	}
	return TargetProgress{TargetID: target.ID, ObjectiveID: target.ObjectiveID, Name: target.Name, TargetValue: target.TargetValue,
		Unit: target.Unit, Currency: target.Currency, Direction: target.Direction, PeriodState: state, PeriodStart: target.PeriodStart,
		PeriodEnd: target.PeriodEnd, Query: target.Query, Status: "error", UpdatedAt: now}
}

func objectiveCompletion(actual, target float64, direction string) (*float64, bool) {
	achieved := (direction == "at_least" && actual >= target) || (direction == "at_most" && actual <= target)
	var pct float64
	if direction == "at_least" {
		if target <= 0 {
			if achieved {
				pct = 100
			}
		} else {
			pct = actual / target * 100
		}
	} else if achieved {
		pct = 100
	} else if actual > 0 {
		pct = target / actual * 100
	}
	if pct < 0 {
		pct = 0
	}
	return &pct, achieved
}

func cacheObjectiveProgressOK(db *sql.DB, targetID int64, actual float64, details map[string]any, now int64) {
	raw, _ := json.Marshal(details)
	_, _ = db.Exec(`INSERT INTO objective_progress (target_id, actual_value, measured_at, status, error, details_json, updated_at)
		VALUES (?, ?, ?, 'ok', '', ?, ?)
		ON CONFLICT(target_id) DO UPDATE SET actual_value=excluded.actual_value, measured_at=excluded.measured_at,
		status='ok', error='', details_json=excluded.details_json, updated_at=excluded.updated_at`, targetID, actual, now, string(raw), now)
}

func cacheObjectiveProgressError(db *sql.DB, targetID int64, message string, now int64) {
	_, _ = db.Exec(`INSERT INTO objective_progress (target_id, actual_value, measured_at, status, error, details_json, updated_at)
		VALUES (?, NULL, NULL, 'error', ?, '{}', ?)
		ON CONFLICT(target_id) DO UPDATE SET status='error', error=excluded.error, updated_at=excluded.updated_at`, targetID, message, now)
}

func cachedTargetProgress(db *sql.DB, target ObjectiveTarget) (*TargetProgress, error) {
	var actual sql.NullFloat64
	var measured sql.NullInt64
	var status, message, detailsJSON string
	var updated int64
	err := db.QueryRow(`SELECT actual_value, measured_at, status, error, details_json, updated_at FROM objective_progress WHERE target_id=?`, target.ID).
		Scan(&actual, &measured, &status, &message, &detailsJSON, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := progressBase(target, time.Now().UnixMilli())
	p.Status, p.Error, p.UpdatedAt = status, message, updated
	if actual.Valid {
		p.ActualValue = &actual.Float64
		p.ProgressPct, p.Achieved = objectiveCompletion(actual.Float64, target.TargetValue, target.Direction)
	}
	if measured.Valid {
		p.MeasuredAt = &measured.Int64
	}
	_ = json.Unmarshal([]byte(detailsJSON), &p.Details)
	return &p, nil
}

func (a *App) handleObjectives(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		includeArchived := r.URL.Query().Get("include_archived") == "true"
		rows, err := listObjectives(globalCtx.AppDB(), projectID, r.URL.Query().Get("status"), r.URL.Query().Get("search"), includeArchived, intQuery(r, "limit"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"objectives": rows})
	case http.MethodPost:
		var body ObjectiveWrite
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if _, err := assignRequestProject(body.ProjectID, projectID); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		o, err := createObjective(globalCtx.AppDB(), projectID, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		progress, _ := evaluateObjective(globalCtx.AppDB(), projectID, o.ID)
		writeJSON(w, map[string]any{"objective": o, "progress": progress})
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleObjectiveItem(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/objectives/"))
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid objective id", http.StatusBadRequest)
		return
	}
	if len(parts) == 2 && parts[1] == "progress" {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
			return
		}
		progress, err := evaluateObjective(globalCtx.AppDB(), projectID, id)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"objective_id": id, "progress": progress})
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		o, err := getObjective(globalCtx.AppDB(), projectID, id)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"objective": o})
	case http.MethodPut:
		var body ObjectiveWrite
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if _, err := assignRequestProject(body.ProjectID, projectID); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		o, err := updateObjective(globalCtx.AppDB(), projectID, id, body)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"objective": o})
	case http.MethodDelete:
		if err := archiveObjective(globalCtx.AppDB(), projectID, id); errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			writeJSON(w, map[string]any{"ok": true})
		}
	default:
		http.Error(w, "GET, PUT or DELETE only", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleObjectiveMetrics(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireRequestProject(w, r); !ok {
		return
	}
	writeJSON(w, map[string]any{"metrics": objectiveMetricDefinitions()})
}

func intQuery(r *http.Request, key string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(key))
	return n
}
