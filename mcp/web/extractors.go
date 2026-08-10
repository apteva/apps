package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	defaultExtractorMaxPages    = 10
	defaultExtractorMaxItems    = 1000
	defaultExtractorMaxSeconds  = 600
	defaultExtractorRetries     = 2
	maxExtractorPages           = 500
	maxExtractorItems           = 100000
	maxExtractorSeconds         = 3600
	maxExtractorSteps           = 100
	maxExtractorTraceEvents     = 500
	maxExtractorPreviewItems    = 20
	maxExtractorFields          = 100
	maxExtractorDatasetBytes    = 32 * 1024 * 1024
	maxExtractorDefinitionBytes = 1024 * 1024
	maxExtractorInputBytes      = 256 * 1024
	maxExtractorPreviewBytes    = 32 * 1024
)

type extractorDefinition struct {
	SchemaVersion int                       `json:"schema_version"`
	Defaults      map[string]any            `json:"defaults,omitempty"`
	Presets       map[string]map[string]any `json:"presets,omitempty"`
	Browser       extractorBrowser          `json:"browser"`
	AllowedHosts  []string                  `json:"allowed_hosts"`
	Limits        extractorLimits           `json:"limits"`
	Steps         []extractorStep           `json:"steps"`
	OutputSchema  map[string]string         `json:"output_schema"`
}

type extractorBrowser struct {
	Backend      string         `json:"backend,omitempty"`
	ProxyMode    string         `json:"proxy_mode,omitempty"`
	ProxyProfile string         `json:"proxy_profile,omitempty"`
	ProxyCountry string         `json:"proxy_country,omitempty"`
	ProxySticky  string         `json:"proxy_sticky,omitempty"`
	Persist      bool           `json:"persist,omitempty"`
	Viewport     map[string]any `json:"viewport,omitempty"`
}

type extractorLimits struct {
	MaxPages           any `json:"max_pages,omitempty"`
	MaxItems           any `json:"max_items,omitempty"`
	MaxDurationSeconds any `json:"max_duration_seconds,omitempty"`
	StepRetries        any `json:"step_retries,omitempty"`
}

type extractorStep struct {
	Action     string                    `json:"action"`
	URL        string                    `json:"url,omitempty"`
	Locator    extractorLocator          `json:"locator,omitempty"`
	Optional   bool                      `json:"optional,omitempty"`
	Items      string                    `json:"items,omitempty"`
	Fields     map[string]extractorField `json:"fields,omitempty"`
	MaxPages   any                       `json:"max_pages,omitempty"`
	Duration   any                       `json:"duration_ms,omitempty"`
	Label      string                    `json:"label,omitempty"`
	Host       string                    `json:"host,omitempty"`
	PathPrefix string                    `json:"path_prefix,omitempty"`
}

type extractorLocator struct {
	Text     string `json:"text,omitempty"`
	Role     string `json:"role,omitempty"`
	Selector string `json:"selector,omitempty"`
}

type extractorField struct {
	Selector  string `json:"selector,omitempty"`
	Type      string `json:"type,omitempty"`
	Attribute string `json:"attribute,omitempty"`
	Required  bool   `json:"required,omitempty"`
}

type extractorRecord struct {
	ID          int64               `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Enabled     bool                `json:"enabled"`
	Revision    int                 `json:"revision"`
	Definition  extractorDefinition `json:"definition"`
	CreatedAt   string              `json:"created_at"`
	UpdatedAt   string              `json:"updated_at"`
}

type extractorQueuedRun struct {
	ID                 int64
	ProjectID          string
	ExtractorID        int64
	ExtractorRevision  int
	InputJSON          string
	DefinitionSnapshot string
	TriggerJSON        string
}

func (a *App) extractorTools() []sdk.Tool {
	definitionSchema := map[string]any{"type": "object", "description": "Version 1 extractor definition containing defaults, presets, browser, allowed_hosts, limits, steps, and output_schema."}
	return []sdk.Tool{
		{
			Name: "web_extractor_save", Description: "Create or update a reusable browser extractor. Updating increments its revision; expected_revision prevents lost updates.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"}, "enabled": map[string]any{"type": "boolean"},
				"expected_revision": map[string]any{"type": "integer"}, "definition": definitionSchema,
			}, []string{"name", "definition"}), Handler: a.toolExtractorSave,
		},
		{
			Name: "web_extractor_get", Description: "Get one extractor by id or name.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"}}, nil), Handler: a.toolExtractorGet,
		},
		{
			Name: "web_extractor_list", Description: "List extractor definitions for the current project.",
			InputSchema: schemaObject(map[string]any{"enabled": map[string]any{"type": "boolean"}, "limit": map[string]any{"type": "integer"}}, nil), Handler: a.toolExtractorList,
		},
		{
			Name: "web_extractor_delete", Description: "Delete an extractor definition. Existing run snapshots remain reproducible.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolExtractorDelete,
		},
		{
			Name: "web_extractor_run", Description: "Queue an extractor run and return immediately. Precedence: defaults, preset, schedule_overrides, explicit input.",
			InputSchema: schemaObject(map[string]any{
				"extractor_id": map[string]any{"type": "integer"}, "preset": map[string]any{"type": "string"},
				"schedule_overrides": map[string]any{"type": "object"}, "input": map[string]any{"type": "object"},
				"schedule_key": map[string]any{"type": "string"}, "trigger_bucket": map[string]any{"type": "string"},
				"_schedule_every_seconds": map[string]any{"type": "integer"},
			}, []string{"extractor_id"}), Handler: a.toolExtractorRun,
		},
		{
			Name: "web_run_get", Description: "Get one Web run, including extractor snapshot metadata, bounded output, and artifacts.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolWebRunGet,
		},
		{
			Name: "web_run_cancel", Description: "Request cancellation of a queued or running extractor run.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolWebRunCancel,
		},
		{
			Name: "web_run_retry", Description: "Queue a retry using the original run's immutable definition snapshot and input.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolWebRunRetry,
		},
		{
			Name: "web_extractor_schedule", Description: "Create a Jobs-owned schedule that queues this extractor. Supports once, every, cron, and deterministic random daily schedules.",
			InputSchema: schemaObject(map[string]any{
				"extractor_id": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"},
				"preset": map[string]any{"type": "string"}, "schedule": map[string]any{"type": "object"},
				"timezone": map[string]any{"type": "string"}, "input": map[string]any{"type": "object"},
				"schedule_overrides": map[string]any{"type": "object"}, "max_retries": map[string]any{"type": "integer"},
				"backoff_seconds": map[string]any{"type": "integer"}, "replace_job_id": map[string]any{"type": "integer"},
			}, []string{"extractor_id", "schedule"}), Handler: a.toolExtractorSchedule,
		},
		{
			Name: "web_extractor_schedules", Description: "List Jobs-owned Web extractor schedules, optionally with delivery runs for one job.",
			InputSchema: schemaObject(map[string]any{"job_id": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"}}, nil), Handler: a.toolExtractorSchedules,
		},
		{
			Name: "web_extractor_unschedule", Description: "Cancel a Jobs-owned extractor schedule.",
			InputSchema: schemaObject(map[string]any{"job_id": map[string]any{"type": "integer"}}, []string{"job_id"}), Handler: a.toolExtractorUnschedule,
		},
	}
}

func decodeExtractorDefinition(raw any) (extractorDefinition, string, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return extractorDefinition{}, "", fmt.Errorf("definition: %w", err)
	}
	var def extractorDefinition
	if err := json.Unmarshal(b, &def); err != nil {
		return def, "", fmt.Errorf("definition: %w", err)
	}
	if err := validateExtractorDefinition(def); err != nil {
		return def, "", err
	}
	canonical, _ := json.Marshal(def)
	if len(canonical) > maxExtractorDefinitionBytes {
		return def, "", fmt.Errorf("definition exceeds %d bytes", maxExtractorDefinitionBytes)
	}
	return def, string(canonical), nil
}

func validateExtractorDefinition(def extractorDefinition) error {
	if def.SchemaVersion != 1 {
		return errors.New("definition.schema_version must be 1")
	}
	if len(def.Steps) == 0 || len(def.Steps) > maxExtractorSteps {
		return fmt.Errorf("definition.steps must contain 1-%d steps", maxExtractorSteps)
	}
	if len(def.AllowedHosts) == 0 {
		return errors.New("definition.allowed_hosts must contain at least one host")
	}
	if len(def.AllowedHosts) > 100 || len(def.Presets) > 100 {
		return errors.New("definition supports at most 100 allowed_hosts and 100 presets")
	}
	for _, host := range def.AllowedHosts {
		if normalizeAllowedHost(host) == "" {
			return fmt.Errorf("invalid allowed host %q", host)
		}
	}
	if mode := def.Browser.ProxyMode; mode != "" && mode != "none" && mode != "auto" && mode != "direct" && mode != "managed" && mode != "profile" {
		return errors.New("definition.browser.proxy_mode must be auto, direct, managed, or profile")
	}
	if backend := def.Browser.Backend; backend != "" && !strings.Contains(backend, "{{") {
		switch backend {
		case "local", "browserbase", "steel", "browser-engine", "service":
		default:
			return fmt.Errorf("definition.browser.backend %q is unsupported", backend)
		}
	}
	mode := normalizedExtractorProxyMode(def.Browser.ProxyMode)
	if def.Browser.ProxyCountry != "" && mode != "managed" && mode != "profile" {
		return errors.New("definition.browser.proxy_country requires proxy_mode=managed or profile")
	}
	if def.Browser.ProxyProfile != "" && mode != "profile" {
		return errors.New("definition.browser.proxy_profile requires proxy_mode=profile")
	}
	if mode == "profile" && strings.TrimSpace(def.Browser.ProxyProfile) == "" {
		return errors.New("definition.browser.proxy_profile is required when proxy_mode=profile")
	}
	if def.Browser.ProxySticky != "" && mode != "profile" {
		return errors.New("definition.browser.proxy_sticky requires proxy_mode=profile")
	}
	if sticky := def.Browser.ProxySticky; sticky != "" && !strings.Contains(sticky, "{{") && sticky != "rotating" && sticky != "session" && sticky != "context" {
		return errors.New("definition.browser.proxy_sticky must be rotating, session, or context")
	}
	if country := def.Browser.ProxyCountry; country != "" && !strings.Contains(country, "{{") {
		if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
			return errors.New("definition.browser.proxy_country must be a two-letter uppercase country code")
		}
	}
	if len(def.Browser.Viewport) > 0 {
		for _, dimension := range []string{"width", "height"} {
			value, ok := def.Browser.Viewport[dimension]
			if !ok {
				return fmt.Errorf("definition.browser.viewport.%s is required", dimension)
			}
			if text, isText := value.(string); isText && strings.Contains(text, "{{") {
				continue
			}
			pixels := intFromAny(value)
			if pixels < 200 || pixels > 5000 {
				return fmt.Errorf("definition.browser.viewport.%s must be between 200 and 5000", dimension)
			}
		}
	}
	for i, step := range def.Steps {
		switch step.Action {
		case "goto":
			if strings.TrimSpace(step.URL) == "" {
				return fmt.Errorf("steps[%d].url is required", i)
			}
		case "click", "paginate":
			if step.Locator.Text == "" && step.Locator.Role == "" && step.Locator.Selector == "" {
				return fmt.Errorf("steps[%d].locator is required", i)
			}
		case "extract":
			if strings.TrimSpace(step.Items) == "" || len(step.Fields) == 0 {
				return fmt.Errorf("steps[%d] requires items and fields", i)
			}
			if len(step.Fields) > maxExtractorFields {
				return fmt.Errorf("steps[%d].fields exceeds the %d field limit", i, maxExtractorFields)
			}
		case "assert_url":
			if normalizeAllowedHost(step.Host) == "" {
				return fmt.Errorf("steps[%d].host is required and must be a valid host", i)
			}
			if step.PathPrefix != "" && !strings.HasPrefix(step.PathPrefix, "/") {
				return fmt.Errorf("steps[%d].path_prefix must start with /", i)
			}
		case "wait", "screenshot":
		default:
			return fmt.Errorf("steps[%d].action %q is unsupported", i, step.Action)
		}
	}
	if len(def.OutputSchema) > maxExtractorFields {
		return fmt.Errorf("definition.output_schema exceeds the %d field limit", maxExtractorFields)
	}
	for field, typ := range def.OutputSchema {
		switch typ {
		case "string", "number", "integer", "boolean", "url":
		default:
			return fmt.Errorf("output_schema.%s has unsupported type %q", field, typ)
		}
	}
	return nil
}

func normalizedExtractorProxyMode(mode string) string {
	if mode == "none" {
		return "direct"
	}
	return mode
}

func normalizeAllowedHost(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(raw, ".")))
	if raw == "" || strings.ContainsAny(raw, "/:@?#") {
		return ""
	}
	if strings.HasPrefix(raw, "*.") {
		raw = strings.TrimPrefix(raw, "*.")
	}
	if !strings.Contains(raw, ".") && raw != "localhost" {
		return ""
	}
	return raw
}

func (a *App) toolExtractorSave(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" || len(name) > 120 {
		return nil, errors.New("name is required and must be at most 120 characters")
	}
	if len(stringArg(args, "description")) > 4000 {
		return nil, errors.New("description must be at most 4000 characters")
	}
	def, canonical, err := decodeExtractorDefinition(args["definition"])
	if err != nil {
		return nil, err
	}
	_ = def
	rec, err := saveExtractor(ctx, int64ArgLocal(args, "id"), name, stringArg(args, "description"), boolArgDefault(args, "enabled", true), intArg(args, "expected_revision"), canonical)
	if err != nil {
		return nil, err
	}
	ctx.Emit("extractor.saved", map[string]any{"id": rec.ID, "name": rec.Name, "revision": rec.Revision})
	return map[string]any{"extractor": rec}, nil
}

func (a *App) toolExtractorGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rec, err := getExtractor(ctx, int64ArgLocal(args, "id"), stringArg(args, "name"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"extractor": rec, "found": rec != nil}, nil
}

func (a *App) toolExtractorList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	var enabled *bool
	if raw, ok := args["enabled"]; ok {
		v := boolArgDefault(map[string]any{"enabled": raw}, "enabled", false)
		enabled = &v
	}
	recs, err := listExtractors(ctx, enabled, boundedInt(intArg(args, "limit"), 100, 1, 500))
	if err != nil {
		return nil, err
	}
	return map[string]any{"extractors": recs, "count": len(recs)}, nil
}

func (a *App) toolExtractorDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64ArgLocal(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	jobIDs, err := extractorScheduleJobIDs(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(jobIDs) > 0 {
		return nil, fmt.Errorf("extractor has active Jobs schedules %v; unschedule them before deleting", jobIDs)
	}
	res, err := ctx.AppDB().Exec(`DELETE FROM web_extractors WHERE id=? AND project_id=?`, id, projectID(ctx))
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		ctx.Emit("extractor.deleted", map[string]any{"id": id})
	}
	return map[string]any{"deleted": n > 0, "id": id}, nil
}

func extractorScheduleJobIDs(ctx *sdk.AppCtx, extractorID int64) ([]int64, error) {
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_list", withProjectID(ctx, map[string]any{"owner_app": "web", "limit": 500}), &out); err != nil {
		return nil, fmt.Errorf("jobs.jobs_list: %w", err)
	}
	ids := []int64{}
	jobs, _ := out["jobs"].([]any)
	for _, raw := range jobs {
		job := mapFromAny(raw)
		if stringFromAny(job["status"]) == "cancelled" {
			continue
		}
		target := mapFromAny(job["target"])
		input := mapFromAny(target["input"])
		if target["app"] == "web" && target["tool"] == "web_extractor_run" && int64ArgLocal(input, "extractor_id") == extractorID {
			ids = append(ids, int64ArgLocal(job, "id"))
		}
	}
	return ids, nil
}

func saveExtractor(ctx *sdk.AppCtx, id int64, name, description string, enabled bool, expectedRevision int, definitionJSON string) (*extractorRecord, error) {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var currentID int64
	var revision int
	query := `SELECT id, revision FROM web_extractors WHERE project_id=? AND name=?`
	params := []any{projectID(ctx), name}
	if id > 0 {
		query = `SELECT id, revision FROM web_extractors WHERE project_id=? AND id=?`
		params = []any{projectID(ctx), id}
	}
	err = tx.QueryRow(query, params...).Scan(&currentID, &revision)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if id > 0 {
			return nil, errors.New("extractor not found")
		}
		res, insertErr := tx.Exec(`INSERT INTO web_extractors(project_id,name,description,enabled,definition_json) VALUES(?,?,?,?,?)`, projectID(ctx), name, nullIfEmpty(description), enabled, definitionJSON)
		if insertErr != nil {
			return nil, insertErr
		}
		currentID, _ = res.LastInsertId()
	case err != nil:
		return nil, err
	default:
		if expectedRevision > 0 && expectedRevision != revision {
			return nil, fmt.Errorf("revision conflict: expected %d, current %d", expectedRevision, revision)
		}
		_, err = tx.Exec(`UPDATE web_extractors SET name=?,description=?,enabled=?,revision=revision+1,definition_json=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, name, nullIfEmpty(description), enabled, definitionJSON, currentID, projectID(ctx))
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getExtractor(ctx, currentID, "")
}

func getExtractor(ctx *sdk.AppCtx, id int64, name string) (*extractorRecord, error) {
	query := `SELECT id,name,COALESCE(description,''),enabled,revision,definition_json,created_at,updated_at FROM web_extractors WHERE project_id=? AND id=?`
	arg := any(id)
	if id <= 0 {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("id or name required")
		}
		query = `SELECT id,name,COALESCE(description,''),enabled,revision,definition_json,created_at,updated_at FROM web_extractors WHERE project_id=? AND name=?`
		arg = name
	}
	return scanExtractor(ctx.AppDB().QueryRow(query, projectID(ctx), arg))
}

type rowScanner interface{ Scan(...any) error }

func scanExtractor(row rowScanner) (*extractorRecord, error) {
	var rec extractorRecord
	var enabled bool
	var definition string
	var created, updated time.Time
	if err := row.Scan(&rec.ID, &rec.Name, &rec.Description, &enabled, &rec.Revision, &definition, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	rec.Enabled = enabled
	rec.CreatedAt = created.UTC().Format(time.RFC3339)
	rec.UpdatedAt = updated.UTC().Format(time.RFC3339)
	if err := json.Unmarshal([]byte(definition), &rec.Definition); err != nil {
		return nil, fmt.Errorf("decode extractor %d: %w", rec.ID, err)
	}
	return &rec, nil
}

func listExtractors(ctx *sdk.AppCtx, enabled *bool, limit int) ([]extractorRecord, error) {
	query := `SELECT id,name,COALESCE(description,''),enabled,revision,definition_json,created_at,updated_at FROM web_extractors WHERE project_id=?`
	args := []any{projectID(ctx)}
	if enabled != nil {
		query += ` AND enabled=?`
		args = append(args, *enabled)
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := ctx.AppDB().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []extractorRecord{}
	for rows.Next() {
		rec, err := scanExtractor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

func int64ArgLocal(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	default:
		return 0
	}
}

func (a *App) handleExtractors(w http.ResponseWriter, r *http.Request) {
	ctx, err := extractorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	recs, err := listExtractors(ctx, nil, 500)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	writeJSON(w, map[string]any{"extractors": recs, "count": len(recs)}, nil)
}

func decodeExtractorHTTPArgs(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
	defer r.Body.Close()
	var args map[string]any
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return nil, false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		httpErr(w, http.StatusBadRequest, "expected one JSON object")
		return nil, false
	}
	return args, true
}

func extractorHTTPID(r *http.Request) int64 {
	raw := r.PathValue("id")
	if raw == "" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		for i := len(parts) - 1; i >= 0; i-- {
			if parts[i] == "cancel" || parts[i] == "retry" {
				continue
			}
			if id, err := strconv.ParseInt(parts[i], 10, 64); err == nil {
				return id
			}
		}
	}
	id, _ := strconv.ParseInt(raw, 10, 64)
	return id
}

func (a *App) handleExtractorSave(w http.ResponseWriter, r *http.Request) {
	ctx, err := extractorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	args, ok := decodeExtractorHTTPArgs(w, r)
	if !ok {
		return
	}
	out, err := a.toolExtractorSave(ctx, args)
	writeJSON(w, out, err)
}

func (a *App) handleExtractorRun(w http.ResponseWriter, r *http.Request) {
	ctx, err := extractorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	args, ok := decodeExtractorHTTPArgs(w, r)
	if !ok {
		return
	}
	out, err := a.toolExtractorRun(ctx, args)
	writeJSON(w, out, err)
}

func (a *App) handleExtractorDelete(w http.ResponseWriter, r *http.Request) {
	ctx, err := extractorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	out, err := a.toolExtractorDelete(ctx, map[string]any{"id": extractorHTTPID(r)})
	writeJSON(w, out, err)
}

func (a *App) handleRunItem(w http.ResponseWriter, r *http.Request) {
	ctx, err := extractorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	out, err := a.toolWebRunGet(ctx, map[string]any{"id": extractorHTTPID(r)})
	writeJSON(w, out, err)
}

func (a *App) handleRunCancel(w http.ResponseWriter, r *http.Request) {
	ctx, err := extractorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	out, err := a.toolWebRunCancel(ctx, map[string]any{"id": extractorHTTPID(r)})
	writeJSON(w, out, err)
}

func (a *App) handleRunRetry(w http.ResponseWriter, r *http.Request) {
	ctx, err := extractorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	out, err := a.toolWebRunRetry(ctx, map[string]any{"id": extractorHTTPID(r)})
	writeJSON(w, out, err)
}

func (a *App) handleExtractorSchedules(w http.ResponseWriter, r *http.Request) {
	ctx, err := extractorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	args := map[string]any{"limit": 500}
	if jobID, parseErr := strconv.ParseInt(r.URL.Query().Get("job_id"), 10, 64); parseErr == nil && jobID > 0 {
		args["job_id"] = jobID
	}
	out, err := a.toolExtractorSchedules(ctx, args)
	writeJSON(w, out, err)
}

func (a *App) handleExtractorSchedule(w http.ResponseWriter, r *http.Request) {
	ctx, err := extractorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	args, ok := decodeExtractorHTTPArgs(w, r)
	if !ok {
		return
	}
	out, err := a.toolExtractorSchedule(ctx, args)
	writeJSON(w, out, err)
}

func (a *App) handleExtractorUnschedule(w http.ResponseWriter, r *http.Request) {
	ctx, err := extractorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	out, err := a.toolExtractorUnschedule(ctx, map[string]any{"job_id": extractorHTTPID(r)})
	writeJSON(w, out, err)
}

func (a *App) handleExtractorScheduleRunNow(w http.ResponseWriter, r *http.Request) {
	ctx, err := extractorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	var out map[string]any
	err = ctx.PlatformAPI().CallAppResult("jobs", "jobs_run_now", withProjectID(ctx, map[string]any{"id": extractorHTTPID(r)}), &out)
	if err != nil {
		err = fmt.Errorf("jobs.jobs_run_now: %w", err)
	}
	writeJSON(w, out, err)
}

func extractorHTTPContext(r *http.Request) (*sdk.AppCtx, error) {
	if globalCtx == nil {
		return nil, errors.New("web app is not mounted")
	}
	ctx := globalCtx
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		ctx = globalCtx.WithProject(pid)
	}
	return ctx, nil
}

func (a *App) toolExtractorSchedule(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	extractorID := int64ArgLocal(args, "extractor_id")
	rec, err := getExtractor(ctx, extractorID, "")
	if err != nil || rec == nil {
		if err == nil {
			err = errors.New("extractor not found")
		}
		return nil, err
	}
	if !rec.Enabled {
		return nil, errors.New("extractor is disabled")
	}
	if preset := stringArg(args, "preset"); preset != "" {
		if _, ok := rec.Definition.Presets[preset]; !ok {
			return nil, fmt.Errorf("preset %q not found", preset)
		}
	}
	schedule, ok := args["schedule"].(map[string]any)
	if !ok {
		return nil, errors.New("schedule required")
	}
	scheduleKey := "sched_" + randName()
	targetInput := map[string]any{"extractor_id": extractorID, "schedule_key": scheduleKey}
	for _, key := range []string{"preset", "input", "schedule_overrides"} {
		if v, exists := args[key]; exists {
			targetInput[key] = v
		}
	}
	if strings.EqualFold(stringFromAny(schedule["kind"]), "every") {
		targetInput["_schedule_every_seconds"] = intFromAny(schedule["every_seconds"])
	}
	if encoded, _ := json.Marshal(targetInput); len(encoded) > maxExtractorInputBytes {
		return nil, fmt.Errorf("scheduled run input exceeds %d bytes", maxExtractorInputBytes)
	}
	jobArgs := map[string]any{
		"name": firstNonEmpty(stringArg(args, "name"), rec.Name), "schedule": schedule,
		"timezone": firstNonEmpty(stringArg(args, "timezone"), "UTC"), "owner_app": "web",
		"target":          map[string]any{"kind": "app_tool", "app": "web", "tool": "web_extractor_run", "input": targetInput},
		"idempotency_key": scheduleKey, "max_retries": boundedInt(intArg(args, "max_retries"), 3, 0, 20),
		"backoff_seconds": boundedInt(intArg(args, "backoff_seconds"), 30, 1, 86400),
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_schedule", withProjectID(ctx, jobArgs), &out); err != nil {
		return nil, fmt.Errorf("jobs.jobs_schedule: %w", err)
	}
	if oldID := int64ArgLocal(args, "replace_job_id"); oldID > 0 {
		var ignored map[string]any
		if err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_cancel", withProjectID(ctx, map[string]any{"id": oldID}), &ignored); err != nil {
			return nil, fmt.Errorf("new schedule created but old job %d could not be cancelled: %w", oldID, err)
		}
	}
	out["schedule_key"] = scheduleKey
	out["extractor"] = map[string]any{"id": rec.ID, "name": rec.Name, "revision": rec.Revision}
	return out, nil
}

func (a *App) toolExtractorSchedules(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if jobID := int64ArgLocal(args, "job_id"); jobID > 0 {
		var out map[string]any
		err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_runs", withProjectID(ctx, map[string]any{"id": jobID, "limit": boundedInt(intArg(args, "limit"), 50, 1, 200)}), &out)
		if err != nil {
			return nil, fmt.Errorf("jobs.jobs_runs: %w", err)
		}
		return out, nil
	}
	var out map[string]any
	err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_list", withProjectID(ctx, map[string]any{"owner_app": "web", "limit": boundedInt(intArg(args, "limit"), 100, 1, 500)}), &out)
	if err != nil {
		return nil, fmt.Errorf("jobs.jobs_list: %w", err)
	}
	if jobs, ok := out["jobs"].([]any); ok {
		filtered := make([]any, 0, len(jobs))
		for _, raw := range jobs {
			job, _ := raw.(map[string]any)
			target, _ := job["target"].(map[string]any)
			if target["app"] == "web" && target["tool"] == "web_extractor_run" {
				filtered = append(filtered, job)
			}
		}
		out["jobs"] = filtered
		out["count"] = len(filtered)
	}
	return out, nil
}

func (a *App) toolExtractorUnschedule(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64ArgLocal(args, "job_id")
	if id <= 0 {
		return nil, errors.New("job_id required")
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_cancel", withProjectID(ctx, map[string]any{"id": id}), &out); err != nil {
		return nil, fmt.Errorf("jobs.jobs_cancel: %w", err)
	}
	return out, nil
}

func sortedSchemaFields(schema map[string]string) []string {
	keys := make([]string, 0, len(schema))
	for key := range schema {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func hostAllowed(rawURL string, hosts []string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	for _, raw := range hosts {
		wildcard := strings.HasPrefix(strings.TrimSpace(raw), "*.")
		allowed := normalizeAllowedHost(raw)
		if host == allowed || (wildcard && strings.HasSuffix(host, "."+allowed)) {
			return true
		}
	}
	return false
}

func (a *App) runExtractorWorker(workerCtx context.Context, app *sdk.AppCtx) error {
	run, err := claimExtractorRun(app)
	if err != nil || run == nil {
		return err
	}
	ctx := app.WithProject(run.ProjectID)
	return a.executeExtractorRun(workerCtx, ctx, run)
}

func claimExtractorRun(ctx *sdk.AppCtx) (*extractorQueuedRun, error) {
	tx, err := ctx.AppDB().BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := `SELECT id,project_id,extractor_id,extractor_revision,input_json,definition_snapshot_json,COALESCE(trigger_json,'{}') FROM web_runs WHERE status='queued' AND extractor_id IS NOT NULL`
	args := []any{}
	if pid := ctx.CurrentProject(); pid != "" {
		query += ` AND project_id=?`
		args = append(args, pid)
	}
	query += ` ORDER BY created_at,id LIMIT 1`
	var run extractorQueuedRun
	if err := tx.QueryRow(query, args...).Scan(&run.ID, &run.ProjectID, &run.ExtractorID, &run.ExtractorRevision, &run.InputJSON, &run.DefinitionSnapshot, &run.TriggerJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	res, err := tx.Exec(`UPDATE web_runs SET status='running' WHERE id=? AND status='queued'`, run.ID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.EmitWithProject("extractor.run.started", run.ProjectID, map[string]any{"run_id": run.ID, "extractor_id": run.ExtractorID})
	return &run, nil
}
