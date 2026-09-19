package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type MetricDefinition struct {
	ID          int64          `json:"id"`
	ProjectID   string         `json:"project_id"`
	Key         string         `json:"key"`
	Label       string         `json:"label"`
	Description string         `json:"description,omitempty"`
	Expression  map[string]any `json:"expression"`
	Unit        string         `json:"unit"`
	Format      string         `json:"format"`
	Currency    string         `json:"currency,omitempty"`
	CreatedAt   int64          `json:"created_at"`
	UpdatedAt   int64          `json:"updated_at"`
}

func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	project, ok := requireRequestProject(w, r)
	if !ok && globalCtx != nil && strings.TrimSpace(globalCtx.CurrentProject()) == "" {
		project = "__global__"
		ok = true
	}
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := listMetricDefinitions(requestReadDB(r), project)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]any{"metrics": rows})
	case http.MethodPost, http.MethodPut:
		var in MetricDefinition
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&in); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		in.ProjectID = project
		if err := validateMetricDefinition(in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		out, err := upsertMetricDefinition(requestWriteDB(r), in)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "GET, POST or PUT only", http.StatusMethodNotAllowed)
	}
}

// handleGlobalMetric evaluates one global metric definition over the projects
// visible to this global install. It never accepts a caller-supplied project
// list, so the platform remains the authority for visibility.
func (a *App) handleGlobalMetric(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil || globalCtx.PlatformAPI() == nil {
		http.Error(w, "platform unavailable", 503)
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("metric"))
	if key == "" {
		http.Error(w, "metric is required", 400)
		return
	}
	projects, err := globalCtx.PlatformAPI().ListProjects()
	if err != nil {
		http.Error(w, "unable to list projects", 500)
		return
	}
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		if strings.TrimSpace(p.ID) != "" {
			ids = append(ids, p.ID)
		}
	}
	metric, err := getMetricDefinition(requestReadDB(r), "__global__", key)
	if err != nil {
		http.Error(w, "global metric not found", 404)
		return
	}
	f := Filter{ProjectIDs: ids, Since: parseInt64(r.URL.Query().Get("since")), Until: parseInt64(r.URL.Query().Get("until"))}
	value, err := evaluateMetric(requestReadDB(r), "__global__", metric, f, map[string]bool{})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]any{"metric": metric.Key, "value": value, "unit": metric.Unit, "format": metric.Format, "currency": metric.Currency, "projects": ids})
}

func (a *App) handleGlobalQueryWidget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil || globalCtx.PlatformAPI() == nil {
		http.Error(w, "platform unavailable", 503)
		return
	}
	var body struct {
		Widget DashboardWidget `json:"widget"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	projects, err := globalCtx.PlatformAPI().ListProjects()
	if err != nil {
		http.Error(w, "unable to list projects", 500)
		return
	}
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		if strings.TrimSpace(p.ID) != "" {
			ids = append(ids, p.ID)
		}
	}
	metric, ok, err := metricFromConfig(requestReadDB(r), "__global__", body.Widget.Config)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if !ok {
		http.Error(w, "global widgets must reference a calculated metric", 400)
		return
	}
	f := Filter{ProjectIDs: ids}
	data := map[string]any{}
	switch body.Widget.Type {
	case "stat":
		value, evalErr := evaluateMetric(requestReadDB(r), "__global__", metric, f, map[string]bool{})
		if evalErr != nil {
			http.Error(w, evalErr.Error(), 400)
			return
		}
		data = map[string]any{"type": "stat", "value": value, "metric": metric.Key, "unit": metric.Unit, "format": metric.Format, "currency": metric.Currency}
	case "timeseries":
		rows, evalErr := metricSeries(requestReadDB(r), "__global__", metric, f, stringConfig(body.Widget.Config, "interval", "day"))
		if evalErr != nil {
			http.Error(w, evalErr.Error(), 400)
			return
		}
		data = map[string]any{"type": "timeseries", "series": rows, "metric": metric.Key, "unit": metric.Unit, "format": metric.Format, "currency": metric.Currency}
	case "table":
		rows, evalErr := groupedCalculatedMetricRows(requestReadDB(r), "__global__", metric, f, stringConfig(body.Widget.Config, "by", "project_id"), intConfig(body.Widget.Config, "limit", 25))
		if evalErr != nil {
			http.Error(w, evalErr.Error(), 400)
			return
		}
		data = map[string]any{"type": "table", "rows": rows, "metric": metric.Key, "unit": metric.Unit, "format": metric.Format, "currency": metric.Currency}
	default:
		http.Error(w, "global widgets support stat, timeseries, and table", 400)
		return
	}
	writeJSON(w, data)
}

func listMetricDefinitions(db sqlRunner, project string) ([]MetricDefinition, error) {
	rows, err := db.Query(`SELECT id,project_id,key,label,description,expression_json,unit,format,currency,created_at,updated_at FROM metric_definitions WHERE project_id=? ORDER BY key`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricDefinition
	for rows.Next() {
		var m MetricDefinition
		var raw string
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Key, &m.Label, &m.Description, &raw, &m.Unit, &m.Format, &m.Currency, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &m.Expression); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func getMetricDefinition(db sqlRunner, project, key string) (MetricDefinition, error) {
	var m MetricDefinition
	var raw string
	err := db.QueryRow(`SELECT id,project_id,key,label,description,expression_json,unit,format,currency,created_at,updated_at FROM metric_definitions WHERE project_id=? AND key=?`, project, key).Scan(&m.ID, &m.ProjectID, &m.Key, &m.Label, &m.Description, &raw, &m.Unit, &m.Format, &m.Currency, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal([]byte(raw), &m.Expression); err != nil {
		return m, err
	}
	return m, nil
}

func upsertMetricDefinition(db sqlRunner, m MetricDefinition) (MetricDefinition, error) {
	now := time.Now().UnixMilli()
	raw, _ := json.Marshal(m.Expression)
	_, err := db.Exec(`INSERT INTO metric_definitions(project_id,key,label,description,expression_json,unit,format,currency,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,key) DO UPDATE SET label=excluded.label,description=excluded.description,expression_json=excluded.expression_json,unit=excluded.unit,format=excluded.format,currency=excluded.currency,updated_at=excluded.updated_at`, m.ProjectID, m.Key, m.Label, m.Description, string(raw), m.Unit, m.Format, m.Currency, now, now)
	if err != nil {
		return m, err
	}
	return getMetricDefinition(db, m.ProjectID, m.Key)
}

func validateMetricDefinition(m MetricDefinition) error {
	if strings.TrimSpace(m.Key) == "" || strings.ContainsAny(m.Key, " /") {
		return errors.New("metric key must be a non-empty identifier")
	}
	if m.Label == "" {
		m.Label = m.Key
	}
	if len(m.Expression) == 0 {
		return errors.New("metric expression is required")
	}
	return validateMetricExpr(m.Expression, map[string]bool{})
}
func validateMetricExpr(expr map[string]any, stack map[string]bool) error {
	if key, ok := expr["metric"].(string); ok {
		if key == "" {
			return errors.New("metric reference cannot be empty")
		}
		if stack[key] {
			return fmt.Errorf("metric dependency cycle at %q", key)
		}
		return nil
	}
	if _, ok := expr["source"].(map[string]any); ok {
		return nil
	}
	op, _ := expr["op"].(string)
	supported := map[string]bool{"add": true, "subtract": true, "multiply": true, "divide": true, "safe_divide": true}
	if !supported[op] {
		return fmt.Errorf("unsupported metric operator %q", op)
	}
	left, lok := expr["left"].(map[string]any)
	right, rok := expr["right"].(map[string]any)
	if !lok || !rok {
		return errors.New("metric operators require left and right expressions")
	}
	if err := validateMetricExpr(left, stack); err != nil {
		return err
	}
	return validateMetricExpr(right, stack)
}

func evaluateMetric(db sqlRunner, project string, metric MetricDefinition, f Filter, stack map[string]bool) (float64, error) {
	if stack[metric.Key] {
		return 0, fmt.Errorf("metric dependency cycle at %q", metric.Key)
	}
	stack[metric.Key] = true
	defer delete(stack, metric.Key)
	return evaluateMetricExpr(db, project, metric.Expression, f, stack)
}
func evaluateMetricExpr(db sqlRunner, project string, expr map[string]any, f Filter, stack map[string]bool) (float64, error) {
	if contextName, ok := expr["context"].(string); ok {
		now := time.Now().UnixMilli()
		start := f.Since
		if start <= 0 {
			start = now - 24*3600*1000
		}
		switch contextName {
		case "elapsed_days":
			return float64(maxInt64(1, (now-start)/(24*3600*1000))), nil
		case "days_in_period":
			t := time.Now().UTC()
			next := time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			current := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
			return next.Sub(current).Hours() / 24, nil
		default:
			return 0, fmt.Errorf("unsupported metric context %q", contextName)
		}
	}
	if key, ok := expr["metric"].(string); ok {
		m, err := getMetricDefinition(db, project, key)
		if err != nil {
			return 0, err
		}
		return evaluateMetric(db, project, m, f, stack)
	}
	if source, ok := expr["source"].(map[string]any); ok {
		sf := f
		if v, s := source["app"].(string); s {
			sf.App = v
		}
		if v, s := source["topic"].(string); s {
			sf.Topic = v
		}
		if v, s := source["source"].(string); s {
			sf.Source = v
		}
		if raw, s := source["where"].(map[string]any); s {
			sf.Where = raw
		}
		value, _ := source["value"].(string)
		agg, _ := source["aggregation"].(string)
		if agg == "" {
			agg = "sum"
		}
		if agg == "weighted_sum" {
			weight, _ := source["weight"].(string)
			return weightedSourceSum(db, sf, value, weight)
		}
		if value == "" && agg != "count" {
			return 0, errors.New("metric source value is required")
		}
		if agg == "count" {
			n, err := countEvents(db, sf)
			return float64(n), err
		}
		out, err := numericScalarForWidget(db, sf, value, agg)
		if err != nil {
			return 0, err
		}
		n, ok := numericMapValue(out["value"])
		if !ok {
			return 0, errors.New("metric source returned no numeric value")
		}
		return n, nil
	}
	leftRaw, _ := expr["left"].(map[string]any)
	rightRaw, _ := expr["right"].(map[string]any)
	left, err := evaluateMetricExpr(db, project, leftRaw, f, stack)
	if err != nil {
		return 0, err
	}
	right, err := evaluateMetricExpr(db, project, rightRaw, f, stack)
	if err != nil {
		return 0, err
	}
	op, _ := expr["op"].(string)
	switch op {
	case "add":
		return left + right, nil
	case "subtract":
		return left - right, nil
	case "multiply":
		return left * right, nil
	case "divide":
		if right == 0 {
			return 0, errors.New("division by zero")
		}
		return left / right, nil
	case "safe_divide":
		if right == 0 {
			return 0, nil
		}
		return left / right, nil
	}
	return 0, fmt.Errorf("unsupported metric operator %q", op)
}

func weightedSourceSum(db sqlRunner, f Filter, valueKey, weightKey string) (float64, error) {
	valueExpr, valuePredicate, ok := numericValueExtract(valueKey)
	if !ok {
		return 0, errors.New("weighted source value must be numeric")
	}
	weightExpr, weightPredicate, ok := numericValueExtract(weightKey)
	if !ok {
		return 0, errors.New("weighted source weight must be numeric")
	}
	where, args, err := f.buildWhere()
	if err != nil {
		return 0, err
	}
	q := "SELECT COALESCE(SUM(CASE WHEN " + valuePredicate + " AND " + weightPredicate + " THEN CAST(" + valueExpr + " AS REAL) * CAST(" + weightExpr + " AS REAL) END),0) FROM events"
	if where != "" {
		q += " WHERE " + where
	}
	var out float64
	if err := db.QueryRow(q, args...).Scan(&out); err != nil {
		return 0, err
	}
	return out, nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func metricFromConfig(db sqlRunner, project string, cfg map[string]any) (MetricDefinition, bool, error) {
	key, _ := cfg["metric"].(string)
	if strings.TrimSpace(key) == "" {
		return MetricDefinition{}, false, nil
	}
	m, err := getMetricDefinition(db, project, key)
	return m, true, err
}

func metricWindowFilter(cfg map[string]any) Filter { f := filterFromWidget("", cfg); return f }

func metricSeries(db sqlRunner, project string, metric MetricDefinition, f Filter, interval string) ([]map[string]any, error) {
	start, end, step, err := seriesGrid(db, f, interval)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0)
	for bucket := start; bucket < end; bucket += step {
		bf := f
		bf.Since = bucket
		bf.Until = bucket + step
		value, evalErr := evaluateMetric(db, project, metric, bf, map[string]bool{})
		row := map[string]any{"bucket": time.UnixMilli(bucket).UTC().Format("2006-01-02"), "ts": bucket, "aggregation": "calculated"}
		if evalErr != nil {
			row["error"] = evalErr.Error()
		} else {
			row["value"] = value
		}
		out = append(out, row)
	}
	return out, nil
}

func groupedCalculatedMetricRows(db sqlRunner, project string, metric MetricDefinition, f Filter, by string, limit int) ([]map[string]any, error) {
	groupExpr, ok := dashboardGroupExpr(by)
	if !ok {
		return nil, errors.New("invalid metric table group")
	}
	where, args, err := f.buildWhere()
	if err != nil {
		return nil, err
	}
	q := "SELECT DISTINCT " + groupExpr + " FROM events"
	if where != "" {
		q += " WHERE " + where
	}
	q += " ORDER BY " + groupExpr + " LIMIT ?"
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var group sql.NullString
		if err := rows.Scan(&group); err != nil {
			return nil, err
		}
		label := "(none)"
		if group.Valid && group.String != "" {
			label = group.String
		}
		gf := f
		if by == "project_id" {
			gf.ProjectID = group.String
		} else {
			if gf.Where == nil {
				gf.Where = map[string]any{}
			}
			gf.Where[by] = group.String
		}
		value, evalErr := evaluateMetric(db, project, metric, gf, map[string]bool{})
		row := map[string]any{"group": label, "value": value, "count": 0}
		if evalErr != nil {
			row["error"] = evalErr.Error()
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
