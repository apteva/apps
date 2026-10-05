package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	maxCrawlRoutes       = 50
	maxCrawlSeeds        = 1000
	maxCrawlExtracts     = 100
	maxCrawlFields       = 100
	maxCrawlFrontierSize = 100000
)

type crawlDefinition struct {
	Seeds    []string                `json:"seeds"`
	Routes   map[string]crawlRoute   `json:"routes"`
	Frontier crawlFrontier           `json:"frontier,omitempty"`
	Datasets map[string]crawlDataset `json:"datasets"`
}

type crawlFrontier struct {
	Concurrency int    `json:"concurrency,omitempty"`
	MaxDepth    int    `json:"max_depth,omitempty"`
	MaxPages    int    `json:"max_pages,omitempty"`
	MaxItems    int    `json:"max_items,omitempty"`
	MaxRetries  int    `json:"max_retries,omitempty"`
	DelayMS     int    `json:"delay_ms,omitempty"`
	Dedupe      string `json:"dedupe,omitempty"`
}

type crawlRoute struct {
	Match    string           `json:"match"`
	Steps    []actorStep      `json:"steps,omitempty"`
	Extract  []crawlExtract   `json:"extract"`
	Follow   []crawlFollow    `json:"follow,omitempty"`
	Paginate *crawlPagination `json:"paginate,omitempty"`
}

type crawlPagination struct {
	Locator  actorLocator `json:"locator"`
	MaxPages int          `json:"max_pages,omitempty"`
}

type crawlExtract struct {
	Repeat     int                   `json:"repeat,omitempty"`
	Required   bool                  `json:"required,omitempty"`
	Dataset    string                `json:"dataset"`
	Items      string                `json:"items"`
	Fields     map[string]crawlField `json:"fields"`
	Key        string                `json:"key,omitempty"`
	Transforms map[string]string     `json:"transforms,omitempty"`
}

type crawlFollow struct {
	Field string `json:"field"`
	Route string `json:"route"`
}

type crawlDataset struct {
	KeyFields []string          `json:"key_fields,omitempty"`
	Schema    map[string]string `json:"schema,omitempty"`
	Key       string            `json:"key,omitempty"`
}

type crawlQueueItem struct {
	ID        int64
	URL       string
	Route     string
	Depth     int
	ParentKey string
	Attempts  int
	Fence     int
}

func validateCrawlDefinition(c crawlDefinition) error {
	if len(c.Seeds) == 0 || len(c.Seeds) > maxCrawlSeeds {
		return fmt.Errorf("crawl.seeds must contain 1-%d URLs", maxCrawlSeeds)
	}
	if len(c.Routes) == 0 || len(c.Routes) > maxCrawlRoutes {
		return fmt.Errorf("crawl.routes must contain 1-%d routes", maxCrawlRoutes)
	}
	if len(c.Datasets) == 0 || len(c.Datasets) > maxCrawlRoutes {
		return fmt.Errorf("crawl.datasets must contain 1-%d datasets", maxCrawlRoutes)
	}
	for _, seed := range c.Seeds {
		if strings.Contains(seed, "{{") {
			continue
		}
		if err := validateHTTPURL(seed); err != nil {
			return fmt.Errorf("crawl seed %q: %w", seed, err)
		}
	}
	for name, route := range c.Routes {
		if !operationNamePattern.MatchString(name) || strings.TrimSpace(route.Match) == "" {
			return fmt.Errorf("crawl route %q must have a name and match", name)
		}
		if len(route.Extract) == 0 || len(route.Extract) > maxCrawlExtracts {
			return fmt.Errorf("crawl route %q must contain 1-%d extract definitions", name, maxCrawlExtracts)
		}
		for i, extraction := range route.Extract {
			if _, ok := c.Datasets[extraction.Dataset]; !ok {
				return fmt.Errorf("route %q extract %d references unknown dataset %q", name, i, extraction.Dataset)
			}
			if strings.TrimSpace(extraction.Items) == "" || len(extraction.Fields) == 0 || len(extraction.Fields) > maxCrawlFields {
				return fmt.Errorf("route %q extract %d requires items and 1-%d fields", name, i, maxCrawlFields)
			}
			for field, typ := range extraction.Transforms {
				if _, ok := extraction.Fields[field]; !ok {
					return fmt.Errorf("route %q transform %q has no field", name, field)
				}
				if !validCrawlTransform(typ) {
					return fmt.Errorf("route %q field %q has unsupported transform %q", name, field, typ)
				}
			}
		}
		for _, follow := range route.Follow {
			if strings.TrimSpace(follow.Field) == "" || c.Routes[follow.Route].Match == "" {
				return fmt.Errorf("route %q has invalid follow target", name)
			}
		}
		if route.Paginate != nil {
			if route.Paginate.Locator.Selector == "" {
				return fmt.Errorf("route %q pagination requires a CSS selector", name)
			}
			if route.Paginate.MaxPages < 0 || route.Paginate.MaxPages > maxActorPages {
				return fmt.Errorf("route %q pagination max_pages is invalid", name)
			}
		}
		for i, step := range route.Steps {
			if step.Action != "wait" && step.Action != "assert_element" && step.Action != "assert_url" && step.Action != "scroll" {
				return fmt.Errorf("route %q step %d supports only wait, assert_element, assert_url, or scroll", name, i)
			}
		}
	}
	for name, dataset := range c.Datasets {
		if !operationNamePattern.MatchString(name) {
			return fmt.Errorf("invalid crawl dataset name %q", name)
		}
		if dataset.Key != "" && dataset.Schema != nil {
			if _, ok := dataset.Schema[dataset.Key]; !ok {
				return fmt.Errorf("dataset %q key %q is absent from schema", name, dataset.Key)
			}
		}
		for field, typ := range dataset.Schema {
			switch typ {
			case "string", "number", "integer", "boolean", "url", "date", "object", "array", "string?", "number?", "integer?", "boolean?", "url?", "date?", "object?", "array?":
			default:
				return fmt.Errorf("dataset %q field %q has unsupported type %q", name, field, typ)
			}
		}
	}
	if c.Frontier.Concurrency < 0 || c.Frontier.Concurrency > 8 {
		return errors.New("crawl concurrency must be between 0 and 8 (0 uses one worker)")
	}
	if c.Frontier.MaxDepth < 0 || c.Frontier.MaxDepth > 20 {
		return errors.New("crawl.frontier.max_depth must be between 0 and 20")
	}
	if c.Frontier.MaxPages < 0 || c.Frontier.MaxPages > maxCrawlFrontierSize {
		return fmt.Errorf("crawl.frontier.max_pages must be between 0 and %d", maxCrawlFrontierSize)
	}
	if c.Frontier.MaxItems < 0 || c.Frontier.MaxItems > maxActorItems {
		return fmt.Errorf("crawl.frontier.max_items must be between 0 and %d", maxActorItems)
	}
	if c.Frontier.MaxRetries < 0 || c.Frontier.MaxRetries > 10 {
		return errors.New("crawl.frontier.max_retries must be between 0 and 10")
	}
	if c.Frontier.DelayMS < 0 || c.Frontier.DelayMS > 60000 {
		return errors.New("crawl.frontier.delay_ms must be between 0 and 60000")
	}
	if c.Frontier.Dedupe != "" && c.Frontier.Dedupe != "canonical_url" {
		return errors.New("crawl.frontier.dedupe must be canonical_url")
	}
	return nil
}

func validCrawlTransform(transform string) bool {
	switch transform {
	case "trim", "lowercase", "integer", "number", "date", "duration_seconds", "ratio_landed", "ratio_attempted", "url_id", "percent", "height_inches", "weight_lbs":
		return true
	default:
		return false
	}
}

func (a *App) crawlTools() []sdk.Tool {
	definition := map[string]any{"type": "object", "description": "Schema version 2 crawl definition with seeds, routes, datasets, follow fan-out, pagination, and durable frontier settings."}
	return []sdk.Tool{
		{Name: "actors_crawl_save", Description: "Create or update a schema version 2 crawl actor.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "enabled": map[string]any{"type": "boolean"}, "expected_revision": map[string]any{"type": "integer"}, "definition": definition}, []string{"name", "definition"}), Handler: a.toolCrawlSave},
		{Name: "actors_crawl_run", Description: "Queue a crawl actor run. Computer owns browser execution and JavaScript rendering.", InputSchema: schemaObject(map[string]any{"actor_id": map[string]any{"type": "integer"}, "revision": map[string]any{"type": "integer"}, "input": map[string]any{"type": "object"}, "idempotency_key": map[string]any{"type": "string"}}, []string{"actor_id"}), Handler: a.toolCrawlRun},
		{Name: "actors_crawl_status", Description: "Get crawl run status and frontier counts.", InputSchema: schemaObject(map[string]any{"run_id": map[string]any{"type": "integer"}}, []string{"run_id"}), Handler: a.toolCrawlStatus},
		{Name: "actors_crawl_resume", Description: "Resume a partial crawl run from its durable frontier, optionally retrying failed URLs.", InputSchema: schemaObject(map[string]any{"run_id": map[string]any{"type": "integer"}, "retry_failed": map[string]any{"type": "boolean"}}, []string{"run_id"}), Handler: a.toolCrawlResume},
		{Name: "actors_frontier_list", Description: "List queued and failed crawl URLs for a run.", InputSchema: schemaObject(map[string]any{"run_id": map[string]any{"type": "integer"}, "status": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, []string{"run_id"}), Handler: a.toolFrontierList},
		{Name: "actors_dataset_query", Description: "Read the materialized named dataset produced by crawl runs.", InputSchema: schemaObject(map[string]any{"dataset": map[string]any{"type": "string"}, "after": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"}}, []string{"dataset"}), Handler: a.toolDatasetQuery},
	}
}

func (a *App) toolCrawlSave(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	definition := mapFromAny(args["definition"])
	if intFromAny(definition["schema_version"]) != 2 {
		return nil, errors.New("actors_crawl_save requires definition.schema_version=2")
	}
	return a.toolActorSave(ctx, args)
}

func (a *App) toolCrawlRun(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.toolActorRun(ctx, args)
}

func (a *App) toolCrawlStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	runID := int64ArgLocal(args, "run_id")
	var status string
	if err := ctx.AppDB().QueryRow(`SELECT status FROM actors_runs WHERE id=? AND project_id=?`, runID, projectID(ctx)).Scan(&status); err != nil {
		return nil, errors.New("run not found")
	}
	counts := map[string]int{}
	rows, err := ctx.AppDB().Query(`SELECT status,COUNT(*) FROM actors_crawl_queue WHERE run_id=? AND project_id=? GROUP BY status`, runID, projectID(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		counts[key] = count
	}
	return map[string]any{"run_id": runID, "status": status, "frontier": counts}, rows.Err()
}

func (a *App) toolCrawlResume(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	runID := int64ArgLocal(args, "run_id")
	var status string
	var schemaVersion int
	if err := ctx.AppDB().QueryRow(`SELECT status,COALESCE(json_extract(definition_snapshot_json,'$.schema_version'),0) FROM actors_runs WHERE id=? AND project_id=? AND kind='actor'`, runID, projectID(ctx)).Scan(&status, &schemaVersion); err != nil {
		return nil, errors.New("run not found")
	}
	if schemaVersion != 2 {
		return nil, errors.New("run is not a schema version 2 crawl")
	}
	if status == "queued" || status == "running" {
		return nil, errors.New("crawl is already active")
	}
	retryFailed := boolArgDefault(args, "retry_failed", true)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if retryFailed {
		if _, err := tx.Exec(`UPDATE actors_crawl_queue SET status='pending',next_attempt_at=NULL,lease_until=NULL WHERE project_id=? AND run_id=? AND status IN ('failed','running')`, projectID(ctx), runID); err != nil {
			return nil, err
		}
	} else if _, err := tx.Exec(`UPDATE actors_crawl_queue SET status='pending',next_attempt_at=NULL,lease_until=NULL WHERE project_id=? AND run_id=? AND status='running'`, projectID(ctx), runID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE actors_runs SET status='queued',error=NULL,completed_at=NULL WHERE project_id=? AND id=?`, projectID(ctx), runID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Emit("actor.run.queued", map[string]any{"run_id": runID, "resume": true})
	return map[string]any{"run_id": runID, "status": "queued", "retry_failed": retryFailed}, nil
}

func (a *App) toolFrontierList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	limit := boundedInt(intArg(args, "limit"), 50, 1, 200)
	status := strings.TrimSpace(stringArg(args, "status"))
	query := `SELECT id,url,route,depth,COALESCE(parent_key,''),status,attempts,COALESCE(last_error,'') FROM actors_crawl_queue WHERE run_id=? AND project_id=?`
	params := []any{int64ArgLocal(args, "run_id"), projectID(ctx)}
	if status != "" {
		query += ` AND status=?`
		params = append(params, status)
	}
	query += ` ORDER BY id LIMIT ?`
	params = append(params, limit)
	rows, err := ctx.AppDB().Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, depth, attempts int
		var itemURL, route, parent, itemStatus, lastError string
		if err := rows.Scan(&id, &itemURL, &route, &depth, &parent, &itemStatus, &attempts, &lastError); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"id": id, "url": itemURL, "route": route, "depth": depth, "parent_key": parent, "status": itemStatus, "attempts": attempts, "last_error": lastError})
	}
	return map[string]any{"items": items}, rows.Err()
}

func (a *App) toolDatasetQuery(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	dataset := strings.TrimSpace(stringArg(args, "dataset"))
	if dataset == "" {
		return nil, errors.New("dataset is required")
	}
	limit := boundedInt(intArg(args, "limit"), 50, 1, 200)
	rows, err := ctx.AppDB().Query(`SELECT id,item_json FROM actors_crawl_materialized WHERE project_id=? AND dataset=? AND id>? ORDER BY id LIMIT ?`, projectID(ctx), dataset, int64ArgLocal(args, "after"), limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	next := int64ArgLocal(args, "after")
	hasMore := false
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if len(items) >= limit {
			hasMore = true
			break
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
		next = id
	}
	return map[string]any{"dataset": dataset, "items": items, "next_cursor": next, "has_more": hasMore}, rows.Err()
}

func crawlRouteForURL(c crawlDefinition, raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	best := ""
	bestLen := -1
	for name, route := range c.Routes {
		if strings.Contains(u.Path, route.Match) && len(route.Match) > bestLen {
			best, bestLen = name, len(route.Match)
		}
	}
	return best, best != ""
}

func canonicalCrawlURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	u.RawQuery = u.Query().Encode()
	u.Scheme = strings.ToLower(u.Scheme)
	if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
		u.Host = u.Hostname()
	}
	return u.String()
}
