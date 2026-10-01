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
	defaultActorMaxPages    = 10
	defaultActorMaxItems    = 1000
	defaultActorMaxSeconds  = 600
	defaultActorRetries     = 2
	maxActorPages           = 500
	maxActorItems           = 100000
	maxActorSeconds         = 3600
	maxActorSteps           = 100
	maxActorTraceEvents     = 500
	maxActorPreviewItems    = 20
	maxActorFields          = 100
	maxActorDatasetBytes    = 32 * 1024 * 1024
	maxActorItemBytes       = 256 * 1024
	maxActorDefinitionBytes = 1024 * 1024
	maxActorInputBytes      = 256 * 1024
	maxActorPreviewBytes    = 32 * 1024
)

type actorDefinition struct {
	Operations    map[string]actorOperation `json:"operations,omitempty"`
	SchemaVersion int                       `json:"schema_version"`
	Defaults      map[string]any            `json:"defaults,omitempty"`
	Presets       map[string]map[string]any `json:"presets,omitempty"`
	Browser       actorBrowser              `json:"browser"`
	AllowedHosts  []string                  `json:"allowed_hosts"`
	Limits        actorLimits               `json:"limits"`
	Steps         []actorStep               `json:"steps"`
	OutputSchema  map[string]string         `json:"output_schema"`
}

type actorBrowser struct {
	ContextID    string         `json:"context_id,omitempty"`
	Backend      string         `json:"backend,omitempty"`
	ProxyMode    string         `json:"proxy_mode,omitempty"`
	ProxyProfile string         `json:"proxy_profile,omitempty"`
	ProxyCountry string         `json:"proxy_country,omitempty"`
	ProxySticky  string         `json:"proxy_sticky,omitempty"`
	Persist      bool           `json:"persist,omitempty"`
	Viewport     map[string]any `json:"viewport,omitempty"`
	Environment  map[string]any `json:"environment,omitempty"`
}

type actorLimits struct {
	MaxPages           any `json:"max_pages,omitempty"`
	MaxItems           any `json:"max_items,omitempty"`
	MaxDurationSeconds any `json:"max_duration_seconds,omitempty"`
	StepRetries        any `json:"step_retries,omitempty"`
}

type actorStep struct {
	Text       string                `json:"text,omitempty"`
	Key        string                `json:"key,omitempty"`
	Direction  string                `json:"direction,omitempty"`
	Amount     int                   `json:"amount,omitempty"`
	Action     string                `json:"action"`
	URL        string                `json:"url,omitempty"`
	Locator    actorLocator          `json:"locator,omitempty"`
	Optional   bool                  `json:"optional,omitempty"`
	Items      string                `json:"items,omitempty"`
	Fields     map[string]actorField `json:"fields,omitempty"`
	MaxPages   any                   `json:"max_pages,omitempty"`
	Duration   any                   `json:"duration_ms,omitempty"`
	Label      string                `json:"label,omitempty"`
	Host       string                `json:"host,omitempty"`
	PathPrefix string                `json:"path_prefix,omitempty"`
}

type actorLocator struct {
	Text     string `json:"text,omitempty"`
	Role     string `json:"role,omitempty"`
	Selector string `json:"selector,omitempty"`
}

type actorField struct {
	Selector  string `json:"selector,omitempty"`
	Type      string `json:"type,omitempty"`
	Attribute string `json:"attribute,omitempty"`
	Required  bool   `json:"required,omitempty"`
}

type actorRecord struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Enabled     bool            `json:"enabled"`
	Revision    int             `json:"revision"`
	Definition  actorDefinition `json:"definition"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type actorQueuedRun struct {
	ID                 int64
	ProjectID          string
	ActorID            int64
	ActorRevision      int
	InputJSON          string
	DefinitionSnapshot string
	TriggerJSON        string
}

func (a *App) actorTools() []sdk.Tool {
	definitionSchema := map[string]any{"type": "object", "description": "Version 1 actor definition: defaults, presets, browser (including saved context_id), allowed_hosts, limits, and either steps plus output_schema or named operations each containing steps and output_schema. See /actors skill for an example."}
	return []sdk.Tool{
		{
			Name: "actors_save", Description: "Create or update a reusable browser actor. Updating increments its revision; expected_revision prevents lost updates.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"}, "enabled": map[string]any{"type": "boolean"},
				"expected_revision": map[string]any{"type": "integer"}, "definition": definitionSchema,
			}, []string{"name", "definition"}), Handler: a.toolActorSave,
		},
		{
			Name: "actors_get", Description: "Get one actor by id or name.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"}}, nil), Handler: a.toolActorGet,
		},
		{
			Name: "actors_list", Description: "List actor definitions for the current project.",
			InputSchema: schemaObject(map[string]any{"enabled": map[string]any{"type": "boolean"}, "limit": map[string]any{"type": "integer"}}, nil), Handler: a.toolActorList,
		},
		{
			Name: "actors_delete", Description: "Delete an actor definition. Existing run snapshots remain reproducible.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolActorDelete,
		},
		{
			Name: "actors_run", Description: "Queue an actor run and return immediately. Precedence: defaults, selected preset, schedule_overrides, explicit input. Scheduled callers may pass preset_pool for deterministic per-occurrence profile rotation.",
			InputSchema: schemaObject(map[string]any{
				"idempotency_key": map[string]any{"type": "string", "maxLength": 200},
				"actor_id":        map[string]any{"type": "integer"}, "operation": map[string]any{"type": "string"}, "revision": map[string]any{"type": "integer"}, "preset": map[string]any{"type": "string"},
				"preset_pool":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"schedule_overrides": map[string]any{"type": "object"}, "input": map[string]any{"type": "object"},
				"schedule_key": map[string]any{"type": "string"}, "trigger_bucket": map[string]any{"type": "string"},
				"_schedule_every_seconds": map[string]any{"type": "integer"},
			}, []string{"actor_id"}), Handler: a.toolActorRun,
		},
		{
			Name: "actors_run_get", Description: "Get one Actors run, including actor snapshot metadata, bounded output, and artifacts.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolActorRunGet,
		},
		{
			Name: "actors_run_cancel", Description: "Request cancellation of a queued or running actor run.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolActorRunCancel,
		},
		{
			Name: "actors_run_retry", Description: "Queue a retry using the original run's immutable definition snapshot and input.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolActorRunRetry,
		},
		{
			Name: "actors_schedule", Description: "Create a Jobs-owned schedule that queues this actor. Supports once, every, cron, deterministic random daily schedules, and deterministic rotation across a preset_pool.",
			InputSchema: schemaObject(map[string]any{
				"actor_id": map[string]any{"type": "integer"}, "operation": map[string]any{"type": "string"}, "revision": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"},
				"preset": map[string]any{"type": "string"}, "schedule": map[string]any{"type": "object"},
				"preset_pool": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"timezone":    map[string]any{"type": "string"}, "input": map[string]any{"type": "object"},
				"schedule_overrides": map[string]any{"type": "object"}, "max_retries": map[string]any{"type": "integer"},
				"backoff_seconds": map[string]any{"type": "integer"}, "replace_job_id": map[string]any{"type": "integer"},
			}, []string{"actor_id", "schedule"}), Handler: a.toolActorSchedule,
		},
		{
			Name: "actors_schedules", Description: "List Jobs-owned Actors actor schedules, optionally with delivery runs for one job.",
			InputSchema: schemaObject(map[string]any{"job_id": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"}}, nil), Handler: a.toolActorSchedules,
		},
		{
			Name: "actors_unschedule", Description: "Cancel a Jobs-owned actor schedule.",
			InputSchema: schemaObject(map[string]any{"job_id": map[string]any{"type": "integer"}}, []string{"job_id"}), Handler: a.toolActorUnschedule,
		},
	}
}

func decodeActorDefinition(raw any) (actorDefinition, string, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return actorDefinition{}, "", fmt.Errorf("definition: %w", err)
	}
	var def actorDefinition
	if err := json.Unmarshal(b, &def); err != nil {
		return def, "", fmt.Errorf("definition: %w", err)
	}
	if err := validateActorDefinition(def); err != nil {
		return def, "", err
	}
	canonical, _ := json.Marshal(def)
	if len(canonical) > maxActorDefinitionBytes {
		return def, "", fmt.Errorf("definition exceeds %d bytes", maxActorDefinitionBytes)
	}
	return def, string(canonical), nil
}

func validateActorDefinition(def actorDefinition) error {
	if len(def.Operations) > 0 {
		if len(def.Steps) > 0 || len(def.Operations) > 50 {
			return errors.New("use either steps or up to 50 named operations")
		}
		for name, operation := range def.Operations {
			if !operationNamePattern.MatchString(name) {
				return fmt.Errorf("invalid operation name %q", name)
			}
			selected := def
			selected.Operations = nil
			selected.Steps = operation.Steps
			selected.OutputSchema = operation.OutputSchema
			if err := validateActorDefinition(selected); err != nil {
				return fmt.Errorf("operation %s: %w", name, err)
			}
		}
		return nil
	}
	if def.SchemaVersion != 1 {
		return errors.New("definition.schema_version must be 1")
	}
	if len(def.Steps) == 0 || len(def.Steps) > maxActorSteps {
		return fmt.Errorf("definition.steps must contain 1-%d steps", maxActorSteps)
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
	mode := normalizedActorProxyMode(def.Browser.ProxyMode)
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
	if err := validateActorEnvironment(def.Browser.Environment); err != nil {
		return fmt.Errorf("definition.browser.environment: %w", err)
	}
	for i, step := range def.Steps {
		switch step.Action {
		case "fill":
			if step.Locator.Selector == "" {
				return fmt.Errorf("steps[%d].locator.selector is required for fill", i)
			}
		case "key":
			if step.Key == "" {
				return fmt.Errorf("steps[%d].key is required", i)
			}
		case "scroll":
			if step.Direction != "up" && step.Direction != "down" && step.Direction != "left" && step.Direction != "right" {
				return fmt.Errorf("steps[%d].direction is invalid", i)
			}
		case "assert_element":
			if step.Locator.Selector == "" {
				return fmt.Errorf("steps[%d].locator.selector is required", i)
			}
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
			if len(step.Fields) > maxActorFields {
				return fmt.Errorf("steps[%d].fields exceeds the %d field limit", i, maxActorFields)
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
	if len(def.OutputSchema) > maxActorFields {
		return fmt.Errorf("definition.output_schema exceeds the %d field limit", maxActorFields)
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

func normalizedActorProxyMode(mode string) string {
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

func validateActorEnvironment(environment map[string]any) error {
	if len(environment) == 0 {
		return nil
	}
	allowed := map[string]bool{
		"user_agent": true, "locale": true, "languages": true, "timezone": true,
		"geolocation": true, "device_scale_factor": true, "mobile": true,
		"touch": true, "max_touch_points": true,
	}
	for key := range environment {
		if !allowed[key] {
			return fmt.Errorf("unsupported field %q", key)
		}
	}
	for _, key := range []string{"user_agent", "locale", "timezone"} {
		if value, ok := environment[key]; ok && !actorTemplateValue(value) {
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return fmt.Errorf("%s must be a non-empty string", key)
			}
			if key == "timezone" {
				if _, err := time.LoadLocation(text); err != nil {
					return fmt.Errorf("timezone must be a valid IANA timezone: %w", err)
				}
			}
		}
	}
	if value, ok := environment["languages"]; ok && !actorTemplateValue(value) {
		languages := stringSliceFromAny(value)
		if len(languages) == 0 || len(languages) > 10 {
			return errors.New("languages must contain between 1 and 10 strings")
		}
		for _, language := range languages {
			if strings.TrimSpace(language) == "" {
				return errors.New("languages cannot contain an empty value")
			}
		}
	}
	if value, ok := environment["device_scale_factor"]; ok && !actorTemplateValue(value) {
		number, ok := numericValue(value)
		if !ok || number < 0.1 || number > 10 {
			return errors.New("device_scale_factor must be between 0.1 and 10")
		}
	}
	for _, key := range []string{"mobile", "touch"} {
		if value, ok := environment[key]; ok && !actorTemplateValue(value) {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be boolean", key)
			}
		}
	}
	if value, ok := environment["max_touch_points"]; ok && !actorTemplateValue(value) {
		points, ok := numericValue(value)
		if !ok || points != float64(int(points)) || points < 1 || points > 20 {
			return errors.New("max_touch_points must be an integer between 1 and 20")
		}
		touchValue, exists := environment["touch"]
		if !exists {
			return errors.New("max_touch_points requires touch=true")
		}
		if !actorTemplateValue(touchValue) {
			if touch, concrete := touchValue.(bool); !concrete || !touch {
				return errors.New("max_touch_points requires touch=true")
			}
		}
	}
	if value, ok := environment["geolocation"]; ok {
		location, ok := value.(map[string]any)
		if !ok {
			return errors.New("geolocation must be an object")
		}
		allowedLocation := map[string]bool{"latitude": true, "longitude": true, "accuracy": true, "permission": true}
		for key := range location {
			if !allowedLocation[key] {
				return fmt.Errorf("geolocation has unsupported field %q", key)
			}
		}
		for _, key := range []string{"latitude", "longitude"} {
			value, exists := location[key]
			if !exists {
				return fmt.Errorf("geolocation.%s is required", key)
			}
			if actorTemplateValue(value) {
				continue
			}
			number, ok := numericValue(value)
			limit := 90.0
			if key == "longitude" {
				limit = 180
			}
			if !ok || number < -limit || number > limit {
				return fmt.Errorf("geolocation.%s must be between %g and %g", key, -limit, limit)
			}
		}
		if value, exists := location["accuracy"]; exists && !actorTemplateValue(value) {
			number, ok := numericValue(value)
			if !ok || number < 0 || number > 100000 {
				return errors.New("geolocation.accuracy must be between 0 and 100000")
			}
		}
		if value, exists := location["permission"]; exists && !actorTemplateValue(value) {
			permission, ok := value.(string)
			if !ok || (permission != "grant" && permission != "prompt" && permission != "deny") {
				return errors.New("geolocation.permission must be grant, prompt, or deny")
			}
		}
	}
	return nil
}

func actorTemplateValue(value any) bool {
	text, ok := value.(string)
	return ok && strings.Contains(text, "{{")
}

func (a *App) toolActorSave(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" || len(name) > 120 {
		return nil, errors.New("name is required and must be at most 120 characters")
	}
	if len(stringArg(args, "description")) > 4000 {
		return nil, errors.New("description must be at most 4000 characters")
	}
	def, canonical, err := decodeActorDefinition(args["definition"])
	if err != nil {
		return nil, err
	}
	_ = def
	rec, err := saveActor(ctx, int64ArgLocal(args, "id"), name, stringArg(args, "description"), boolArgDefault(args, "enabled", true), intArg(args, "expected_revision"), canonical)
	if err != nil {
		return nil, err
	}
	ctx.Emit("actor.saved", map[string]any{"id": rec.ID, "name": rec.Name, "revision": rec.Revision})
	return map[string]any{"actor": rec}, nil
}

func (a *App) toolActorGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rec, err := getActor(ctx, int64ArgLocal(args, "id"), stringArg(args, "name"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"actor": rec, "found": rec != nil}, nil
}

func (a *App) toolActorList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	var enabled *bool
	if raw, ok := args["enabled"]; ok {
		v := boolArgDefault(map[string]any{"enabled": raw}, "enabled", false)
		enabled = &v
	}
	recs, err := listActors(ctx, enabled, boundedInt(intArg(args, "limit"), 100, 1, 500))
	if err != nil {
		return nil, err
	}
	return map[string]any{"actors": recs, "count": len(recs)}, nil
}

func (a *App) toolActorDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64ArgLocal(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	var taskCount int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM actors_tasks WHERE project_id=? AND actor_id=?`, projectID(ctx), id).Scan(&taskCount); err != nil {
		return nil, err
	}
	if taskCount > 0 {
		return nil, errors.New("actor has saved tasks; delete those tasks first")
	}
	jobIDs, err := actorScheduleJobIDs(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(jobIDs) > 0 {
		return nil, fmt.Errorf("actor has active Jobs schedules %v; unschedule them before deleting", jobIDs)
	}
	res, err := ctx.AppDB().Exec(`DELETE FROM actors_definitions WHERE id=? AND project_id=?`, id, projectID(ctx))
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		ctx.Emit("actor.deleted", map[string]any{"id": id})
	}
	return map[string]any{"deleted": n > 0, "id": id}, nil
}

func actorScheduleJobIDs(ctx *sdk.AppCtx, actorID int64) ([]int64, error) {
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_list", withProjectID(ctx, map[string]any{"owner_app": "actors", "limit": 500}), &out); err != nil {
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
		if target["app"] == "actors" && target["tool"] == "actors_run" && int64ArgLocal(input, "actor_id") == actorID {
			ids = append(ids, int64ArgLocal(job, "id"))
		}
	}
	return ids, nil
}

func saveActor(ctx *sdk.AppCtx, id int64, name, description string, enabled bool, expectedRevision int, definitionJSON string) (*actorRecord, error) {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var currentID int64
	var revision int
	query := `SELECT id, revision FROM actors_definitions WHERE project_id=? AND name=?`
	params := []any{projectID(ctx), name}
	if id > 0 {
		query = `SELECT id, revision FROM actors_definitions WHERE project_id=? AND id=?`
		params = []any{projectID(ctx), id}
	}
	err = tx.QueryRow(query, params...).Scan(&currentID, &revision)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if id > 0 {
			return nil, errors.New("actor not found")
		}
		res, insertErr := tx.Exec(`INSERT INTO actors_definitions(project_id,name,description,enabled,definition_json) VALUES(?,?,?,?,?)`, projectID(ctx), name, nullIfEmpty(description), enabled, definitionJSON)
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
		_, err = tx.Exec(`UPDATE actors_definitions SET name=?,description=?,enabled=?,revision=revision+1,definition_json=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, name, nullIfEmpty(description), enabled, definitionJSON, currentID, projectID(ctx))
		if err != nil {
			return nil, err
		}
	}
	_, err = tx.Exec(`INSERT INTO actors_versions(project_id,actor_id,revision,definition_json) SELECT project_id,id,revision,definition_json FROM actors_definitions WHERE id=? AND project_id=?`, currentID, projectID(ctx))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getActor(ctx, currentID, "")
}

func getActor(ctx *sdk.AppCtx, id int64, name string) (*actorRecord, error) {
	query := `SELECT id,name,COALESCE(description,''),enabled,revision,definition_json,created_at,updated_at FROM actors_definitions WHERE project_id=? AND id=?`
	arg := any(id)
	if id <= 0 {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("id or name required")
		}
		query = `SELECT id,name,COALESCE(description,''),enabled,revision,definition_json,created_at,updated_at FROM actors_definitions WHERE project_id=? AND name=?`
		arg = name
	}
	return scanActor(ctx.AppDB().QueryRow(query, projectID(ctx), arg))
}

type rowScanner interface{ Scan(...any) error }

func scanActor(row rowScanner) (*actorRecord, error) {
	var rec actorRecord
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
		return nil, fmt.Errorf("decode actor %d: %w", rec.ID, err)
	}
	return &rec, nil
}

func listActors(ctx *sdk.AppCtx, enabled *bool, limit int) ([]actorRecord, error) {
	query := `SELECT id,name,COALESCE(description,''),enabled,revision,definition_json,created_at,updated_at FROM actors_definitions WHERE project_id=?`
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
	out := []actorRecord{}
	for rows.Next() {
		rec, err := scanActor(rows)
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

func (a *App) handleActors(w http.ResponseWriter, r *http.Request) {
	ctx, err := actorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	recs, err := listActors(ctx, nil, 500)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	writeJSON(w, map[string]any{"actors": recs, "count": len(recs)}, nil)
}

func decodeActorHTTPArgs(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
	defer r.Body.Close()
	var args map[string]any
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return nil, false
	}
	if args == nil {
		httpErr(w, http.StatusBadRequest, "expected a JSON object")
		return nil, false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		httpErr(w, http.StatusBadRequest, "expected one JSON object")
		return nil, false
	}
	return args, true
}

func actorHTTPID(r *http.Request) int64 {
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

func (a *App) handleActorSave(w http.ResponseWriter, r *http.Request) {
	ctx, err := actorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	args, ok := decodeActorHTTPArgs(w, r)
	if !ok {
		return
	}
	out, err := a.toolActorSave(ctx, args)
	writeJSON(w, out, err)
}

func (a *App) handleActorRun(w http.ResponseWriter, r *http.Request) {
	ctx, err := actorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	args, ok := decodeActorHTTPArgs(w, r)
	if !ok {
		return
	}
	out, err := a.toolActorRun(ctx, args)
	writeJSON(w, out, err)
}

func (a *App) handleActorDelete(w http.ResponseWriter, r *http.Request) {
	ctx, err := actorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	out, err := a.toolActorDelete(ctx, map[string]any{"id": actorHTTPID(r)})
	writeJSON(w, out, err)
}

func (a *App) handleRunItem(w http.ResponseWriter, r *http.Request) {
	ctx, err := actorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	out, err := a.toolActorRunGet(ctx, map[string]any{"id": actorHTTPID(r)})
	writeJSON(w, out, err)
}

func (a *App) handleRunCancel(w http.ResponseWriter, r *http.Request) {
	ctx, err := actorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	out, err := a.toolActorRunCancel(ctx, map[string]any{"id": actorHTTPID(r)})
	writeJSON(w, out, err)
}

func (a *App) handleRunRetry(w http.ResponseWriter, r *http.Request) {
	ctx, err := actorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	out, err := a.toolActorRunRetry(ctx, map[string]any{"id": actorHTTPID(r)})
	writeJSON(w, out, err)
}

func (a *App) handleActorSchedules(w http.ResponseWriter, r *http.Request) {
	ctx, err := actorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	args := map[string]any{"limit": 500}
	if jobID, parseErr := strconv.ParseInt(r.URL.Query().Get("job_id"), 10, 64); parseErr == nil && jobID > 0 {
		args["job_id"] = jobID
	}
	out, err := a.toolActorSchedules(ctx, args)
	writeJSON(w, out, err)
}

func (a *App) handleActorSchedule(w http.ResponseWriter, r *http.Request) {
	ctx, err := actorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	args, ok := decodeActorHTTPArgs(w, r)
	if !ok {
		return
	}
	out, err := a.toolActorSchedule(ctx, args)
	writeJSON(w, out, err)
}

func (a *App) handleActorUnschedule(w http.ResponseWriter, r *http.Request) {
	ctx, err := actorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	out, err := a.toolActorUnschedule(ctx, map[string]any{"job_id": actorHTTPID(r)})
	writeJSON(w, out, err)
}

func (a *App) handleActorScheduleRunNow(w http.ResponseWriter, r *http.Request) {
	ctx, err := actorHTTPContext(r)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err := requireActorJob(ctx, actorHTTPID(r)); err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	var out map[string]any
	err = ctx.PlatformAPI().CallAppResult("jobs", "jobs_run_now", withProjectID(ctx, map[string]any{"id": actorHTTPID(r)}), &out)
	if err != nil {
		err = fmt.Errorf("jobs.jobs_run_now: %w", err)
	}
	writeJSON(w, out, err)
}

func actorHTTPContext(r *http.Request) (*sdk.AppCtx, error) {
	if globalCtx == nil {
		return nil, errors.New("actors app is not mounted")
	}
	ctx := globalCtx
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		if installed := globalCtx.CurrentProject(); installed != "" && installed != pid {
			return nil, errors.New("project does not match this installation")
		}
		ctx = globalCtx.WithProject(pid)
	}
	return ctx, nil
}

func (a *App) toolActorSchedule(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if oldID := int64ArgLocal(args, "replace_job_id"); oldID > 0 {
		if err := requireActorJob(ctx, oldID); err != nil {
			return nil, err
		}
	}
	actorID := int64ArgLocal(args, "actor_id")
	rec, err := getActor(ctx, actorID, "")
	if err != nil || rec == nil {
		if err == nil {
			err = errors.New("actor not found")
		}
		return nil, err
	}
	if !rec.Enabled {
		return nil, errors.New("actor is disabled")
	}
	if revision := intArg(args, "revision"); revision > 0 && revision != rec.Revision {
		var raw string
		if err := ctx.AppDB().QueryRow(`SELECT definition_json FROM actors_versions WHERE project_id=? AND actor_id=? AND revision=?`, projectID(ctx), rec.ID, revision).Scan(&raw); err != nil {
			return nil, errors.New("actor revision not found")
		}
		if err := json.Unmarshal([]byte(raw), &rec.Definition); err != nil {
			return nil, err
		}
		rec.Revision = revision
	}
	if _, err := selectOperation(rec.Definition, firstNonEmpty(stringArg(args, "operation"), "run")); err != nil {
		return nil, err
	}
	preset := strings.TrimSpace(stringArg(args, "preset"))
	if preset != "" {
		if _, ok := rec.Definition.Presets[preset]; !ok {
			return nil, fmt.Errorf("preset %q not found", preset)
		}
	}
	presetPool, err := actorPresetPool(args["preset_pool"], rec.Definition.Presets)
	if err != nil {
		return nil, err
	}
	if preset != "" && len(presetPool) > 0 {
		return nil, errors.New("preset and preset_pool are mutually exclusive")
	}
	schedule, ok := args["schedule"].(map[string]any)
	if !ok {
		return nil, errors.New("schedule required")
	}
	scheduleKey := "sched_" + randName()
	targetInput := map[string]any{"actor_id": actorID, "schedule_key": scheduleKey, "revision": rec.Revision}
	for _, key := range []string{"preset", "input", "schedule_overrides", "operation"} {
		if v, exists := args[key]; exists {
			targetInput[key] = v
		}
	}
	if len(presetPool) > 0 {
		targetInput["preset_pool"] = presetPool
	}
	if strings.EqualFold(stringFromAny(schedule["kind"]), "every") {
		targetInput["_schedule_every_seconds"] = intFromAny(schedule["every_seconds"])
	}
	if encoded, _ := json.Marshal(targetInput); len(encoded) > maxActorInputBytes {
		return nil, fmt.Errorf("scheduled run input exceeds %d bytes", maxActorInputBytes)
	}
	jobArgs := map[string]any{
		"name": firstNonEmpty(stringArg(args, "name"), rec.Name), "schedule": schedule,
		"timezone": firstNonEmpty(stringArg(args, "timezone"), "UTC"), "owner_app": "actors",
		"target":          map[string]any{"kind": "app_tool", "app": "actors", "tool": "actors_run", "input": targetInput},
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
	out["actor"] = map[string]any{"id": rec.ID, "name": rec.Name, "revision": rec.Revision}
	return out, nil
}

func actorPresetPool(raw any, presets map[string]map[string]any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	pool := dedupeStrings(stringSliceFromAny(raw), 100)
	if len(pool) == 0 {
		return nil, errors.New("preset_pool must contain at least one preset name")
	}
	for _, preset := range pool {
		if _, ok := presets[preset]; !ok {
			return nil, fmt.Errorf("preset_pool contains unknown preset %q", preset)
		}
	}
	return pool, nil
}

func (a *App) toolActorSchedules(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if jobID := int64ArgLocal(args, "job_id"); jobID > 0 {
		if err := requireActorJob(ctx, jobID); err != nil {
			return nil, err
		}
		var out map[string]any
		err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_runs", withProjectID(ctx, map[string]any{"id": jobID, "limit": boundedInt(intArg(args, "limit"), 50, 1, 200)}), &out)
		if err != nil {
			return nil, fmt.Errorf("jobs.jobs_runs: %w", err)
		}
		return out, nil
	}
	var out map[string]any
	err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_list", withProjectID(ctx, map[string]any{"owner_app": "actors", "limit": boundedInt(intArg(args, "limit"), 100, 1, 500)}), &out)
	if err != nil {
		return nil, fmt.Errorf("jobs.jobs_list: %w", err)
	}
	if jobs, ok := out["jobs"].([]any); ok {
		filtered := make([]any, 0, len(jobs))
		for _, raw := range jobs {
			job, _ := raw.(map[string]any)
			target, _ := job["target"].(map[string]any)
			if target["app"] == "actors" && target["tool"] == "actors_run" {
				filtered = append(filtered, job)
			}
		}
		out["jobs"] = filtered
		out["count"] = len(filtered)
	}
	return out, nil
}

func (a *App) toolActorUnschedule(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64ArgLocal(args, "job_id")
	if id <= 0 {
		return nil, errors.New("job_id required")
	}
	if err := requireActorJob(ctx, id); err != nil {
		return nil, err
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

func (a *App) runActorWorker(workerCtx context.Context, app *sdk.AppCtx) (workerErr error) {
	run, err := claimActorRun(app)
	if err != nil || run == nil {
		return err
	}
	ctx := app.WithProject(run.ProjectID)
	defer func() {
		if recovered := recover(); recovered != nil {
			workerErr = finishActorRun(ctx, run.ID, "failed", nil, errors.New("actor worker interrupted by internal error; inspect server logs"))
			ctx.Logger().Error("actor worker panic", "run_id", run.ID, "panic", fmt.Sprint(recovered))
		}
	}()
	return a.executeActorRun(workerCtx, ctx, run)
}

func claimActorRun(ctx *sdk.AppCtx) (*actorQueuedRun, error) {
	tx, err := ctx.AppDB().BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := `SELECT id,project_id,actor_id,actor_revision,input_json,definition_snapshot_json,COALESCE(trigger_json,'{}') FROM actors_runs WHERE status='queued' AND actor_id IS NOT NULL`
	args := []any{}
	if pid := ctx.CurrentProject(); pid != "" {
		query += ` AND project_id=?`
		args = append(args, pid)
	}
	query += ` ORDER BY created_at,id LIMIT 1`
	var run actorQueuedRun
	if err := tx.QueryRow(query, args...).Scan(&run.ID, &run.ProjectID, &run.ActorID, &run.ActorRevision, &run.InputJSON, &run.DefinitionSnapshot, &run.TriggerJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	res, err := tx.Exec(`UPDATE actors_runs SET status='running' WHERE id=? AND status='queued'`, run.ID)
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
	ctx.EmitWithProject("actor.run.started", run.ProjectID, map[string]any{"run_id": run.ID, "actor_id": run.ActorID})
	return &run, nil
}
