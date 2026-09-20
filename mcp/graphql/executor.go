package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/vektah/gqlparser/v2/ast"
)

type graphqlRequest struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
	Environment   string         `json:"environment,omitempty"`
	Extensions    map[string]any `json:"extensions,omitempty"`
}

type requestMethodKey struct{}

type executeResult struct {
	Data          map[string]any
	HasData       bool
	Errors        []map[string]any
	OperationName string
	OperationType string
	Timings       executionTimings
	Release       int
	OperationHash string
	Rows          int
	Resolvers     int
	SourceTimings map[string]float64
	AuthScope     string
}

type executionTimings struct {
	Config  time.Duration
	Prepare time.Duration
	Plan    time.Duration
	Execute time.Duration
	Source  time.Duration
	Fast    bool
}

type preparedOperation struct {
	doc              *ast.QueryDocument
	op               *ast.OperationDefinition
	fields           int
	depth            int
	permissionFields []string
}

const compiledCacheLimit = 256

func (a *App) compiledSchema(key, sdl string) (*ast.Schema, []string) {
	a.cacheMu.RLock()
	schema := a.schemaCache[key]
	a.cacheMu.RUnlock()
	if schema != nil {
		return schema, nil
	}
	compiled, validationErrors := validateSDL(sdl)
	if len(validationErrors) != 0 {
		return nil, validationErrors
	}
	a.cacheMu.Lock()
	if a.schemaCache == nil {
		a.schemaCache = make(map[string]*ast.Schema)
	}
	if a.queryCache == nil {
		a.queryCache = make(map[string]*ast.QueryDocument)
	}
	if len(a.schemaCache) >= compiledCacheLimit {
		a.schemaCache = make(map[string]*ast.Schema)
		a.queryCache = make(map[string]*ast.QueryDocument)
		a.operationCache = make(map[string]*preparedOperation)
	}
	a.schemaCache[key] = compiled
	a.cacheMu.Unlock()
	return compiled, nil
}

func (a *App) parsedQuery(key, query string, schema *ast.Schema) (*ast.QueryDocument, []string) {
	a.cacheMu.RLock()
	doc := a.queryCache[key]
	a.cacheMu.RUnlock()
	if doc != nil {
		return doc, nil
	}
	parsed, validationErrors := parseAndValidateQuery(schema, query)
	if len(validationErrors) != 0 {
		return nil, validationErrors
	}
	a.cacheMu.Lock()
	if a.queryCache == nil || len(a.queryCache) >= compiledCacheLimit*4 {
		a.queryCache = make(map[string]*ast.QueryDocument)
		a.operationCache = make(map[string]*preparedOperation)
	}
	a.queryCache[key] = parsed
	a.cacheMu.Unlock()
	return parsed, nil
}

func (a *App) prepareOperation(key, query, operationName string, schema *ast.Schema) (*preparedOperation, []string, error) {
	a.cacheMu.RLock()
	prepared := a.operationCache[key]
	a.cacheMu.RUnlock()
	if prepared != nil {
		return prepared, nil, nil
	}
	doc, validationErrors := a.parsedQuery(key, query, schema)
	if len(validationErrors) > 0 {
		return nil, validationErrors, nil
	}
	op, err := operationFor(doc, operationName)
	if err != nil {
		return nil, nil, err
	}
	fields, depth := queryCost(op.SelectionSet, 0)
	prepared = &preparedOperation{
		doc:              doc,
		op:               op,
		fields:           fields,
		depth:            depth,
		permissionFields: selectedPermissionFields(op.SelectionSet),
	}
	a.cacheMu.Lock()
	if a.operationCache == nil || len(a.operationCache) >= compiledCacheLimit*4 {
		a.operationCache = make(map[string]*preparedOperation)
	}
	a.operationCache[key] = prepared
	a.cacheMu.Unlock()
	return prepared, nil, nil
}

func (a *App) execute(ctx context.Context, project, apiSlug, environment string, req graphqlRequest) (executeResult, error) {
	configStart := time.Now()
	release, err := a.cachedActiveRelease(project, apiSlug, environment)
	if err != nil {
		return executeResult{}, err
	}
	policy := securityPolicy{}
	limits := defaultReleaseLimits()
	var schemaRow *schemaRecord
	var bindings *executionBindings
	if release != nil {
		policy = release.Security
		limits = release.Limits
		schemaRow = &schemaRecord{Version: release.SchemaVersion, Hash: release.SchemaHash, SDL: release.SchemaSDL, Status: "published"}
		bindings = a.bindingsFromRelease(release)
	} else {
		policy, err = a.cachedSecurity(project, apiSlug)
		if err != nil {
			return executeResult{}, err
		}
		schemaRow, err = a.cachedPublishedSchema(project, apiSlug, environment)
		if err != nil {
			return executeResult{}, err
		}
	}
	if err := authorizeIdentity(ctx, project, apiSlug, policy, environment); err != nil {
		return executeResult{}, err
	}
	if strings.TrimSpace(req.Query) == "" {
		return executeResult{}, invalid("query is required")
	}
	if req.Variables == nil {
		req.Variables = map[string]any{}
	}
	if schemaRow == nil {
		return executeResult{}, invalid("no published schema for environment %q", normalizeEnvironment(environment))
	}
	schemaKey := project + "\x00" + apiSlug + "\x00" + normalizeEnvironment(environment) + "\x00" + fmt.Sprint(schemaRow.Version) + "\x00" + schemaRow.Hash
	if release != nil {
		schemaKey = releaseRuntimeKey(project, apiSlug, environment, release.ID) + "\x00" + release.SchemaHash
	}
	schema, schemaErrors := a.compiledSchema(schemaKey, schemaRow.SDL)
	if len(schemaErrors) > 0 {
		return executeResult{}, internal("published schema is invalid")
	}
	timings := executionTimings{Config: time.Since(configStart)}
	prepareStart := time.Now()
	queryKey := schemaKey + "\x00" + req.OperationName + "\x00" + req.Query
	prepared, queryErrors, err := a.prepareOperation(queryKey, req.Query, req.OperationName, schema)
	if len(queryErrors) > 0 {
		return executeResult{Errors: queryErrorObjects(schema, req.Query)}, nil
	}
	if err != nil {
		return executeResult{}, err
	}
	op := prepared.op
	if ctx.Value(requestMethodKey{}) == http.MethodGet && op.Operation != ast.Query {
		return executeResult{}, &graphqlError{Code: "method_not_allowed", Message: "GET supports query operations only"}
	}
	cost, estimatedRows, estimatedResolvers := cardinalityCost(op.SelectionSet, req.Variables, limits.DefaultListSize)
	if cost > limits.MaxCost {
		return executeResult{}, &graphqlError{Code: "query_cost_exceeded", Message: fmt.Sprintf("query cost %d exceeds limit %d", cost, limits.MaxCost)}
	}
	if prepared.depth > limits.MaxDepth {
		return executeResult{}, &graphqlError{Code: "query_depth_exceeded", Message: fmt.Sprintf("query depth %d exceeds limit %d", prepared.depth, limits.MaxDepth)}
	}
	if estimatedRows > limits.MaxRows {
		return executeResult{}, &graphqlError{Code: "row_limit_exceeded", Message: fmt.Sprintf("estimated rows %d exceeds limit %d", estimatedRows, limits.MaxRows)}
	}
	if estimatedResolvers > limits.MaxNestedResolvers {
		return executeResult{}, &graphqlError{Code: "resolver_limit_exceeded", Message: fmt.Sprintf("estimated resolver calls %d exceeds limit %d", estimatedResolvers, limits.MaxNestedResolvers)}
	}
	if err := authorizePermissionFields(ctx, policy, prepared.permissionFields); err != nil {
		return executeResult{}, err
	}
	timings.Prepare = time.Since(prepareStart)
	planStart := time.Now()
	if bindings == nil {
		bindings, err = a.executionPlan(project, apiSlug)
		if err != nil {
			return executeResult{}, err
		}
	}
	timings.Plan = time.Since(planStart)
	deadline := releaseDeadline(deadlineFromContext(ctx), limits)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	ctx = context.WithValue(ctx, executionBindingsKey{}, bindings)
	ctx = context.WithValue(ctx, executionLimitsKey{}, limits)
	executeStart := time.Now()
	result, err := a.executeStandard(ctx, project, apiSlug, schemaKey, schema, req, op, prepared.doc, policy)
	timings.Execute = time.Since(executeStart)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return executeResult{OperationName: op.Name, OperationType: string(op.Operation), Timings: timings}, &graphqlError{Code: "execution_timeout", Message: "GraphQL execution exceeded its release deadline"}
	}
	timings.Source = result.Timings.Source
	timings.Fast = result.Timings.Fast
	result.Timings = timings
	if release != nil {
		result.Release = release.Version
	}
	hash := sha256.Sum256([]byte(req.Query + "\x00" + req.OperationName))
	result.OperationHash = hex.EncodeToString(hash[:])
	if identity := securityIdentity(ctx); identity != nil {
		result.AuthScope = identity.Issuer + ":" + identity.Subject
	} else {
		result.AuthScope = "platform:" + project
	}
	return result, err
}

type executionBindingsKey struct{}
type executionLimitsKey struct{}
type executionBindings struct {
	resolvers map[string]resolverRecord
	sources   map[int64]sourceRecord
	modules   map[string]resolverModule
}

func (a *App) bindingsFromRelease(release *apiRelease) *executionBindings {
	key := releaseRuntimeKey(release.ProjectID, release.APISlug, release.Environment, release.ID)
	a.cacheMu.RLock()
	cached := a.releasePlanCache[key]
	a.cacheMu.RUnlock()
	if cached != nil {
		return cached
	}
	bindings := &executionBindings{resolvers: map[string]resolverRecord{}, sources: map[int64]sourceRecord{}, modules: map[string]resolverModule{}}
	for _, resolver := range release.Resolvers {
		bindings.resolvers[resolver.ParentType+"."+resolver.FieldName] = resolver
	}
	for _, source := range release.Sources {
		bindings.sources[source.ID] = source
	}
	for _, module := range release.Modules {
		bindings.modules[moduleKey(module.Name, module.Version)] = module
	}
	a.cacheMu.Lock()
	if a.releasePlanCache == nil || len(a.releasePlanCache) >= compiledCacheLimit {
		a.releasePlanCache = map[string]*executionBindings{}
	}
	if existing := a.releasePlanCache[key]; existing != nil {
		bindings = existing
	} else {
		a.releasePlanCache[key] = bindings
	}
	a.cacheMu.Unlock()
	return bindings
}

func deadlineFromContext(ctx context.Context) time.Time {
	deadline, _ := ctx.Deadline()
	return deadline
}

func cardinalityCost(selection ast.SelectionSet, vars map[string]any, defaultList int) (cost, rows, resolvers int) {
	var walk func(ast.SelectionSet, int)
	walk = func(set ast.SelectionSet, fanout int) {
		if fanout < 1 {
			fanout = 1
		}
		for _, item := range set {
			field, ok := item.(*ast.Field)
			if !ok {
				switch fragment := item.(type) {
				case *ast.FragmentSpread:
					if fragment.Definition != nil {
						walk(fragment.Definition.SelectionSet, fanout)
					}
				case *ast.InlineFragment:
					walk(fragment.SelectionSet, fanout)
				}
				continue
			}
			resolvers += fanout
			cost += fanout
			next := fanout
			if field.Definition != nil && field.Definition.Type.Elem != nil {
				size := defaultList
				args := field.ArgumentMap(vars)
				for _, key := range []string{"first", "limit"} {
					if n := moduleInt(args[key]); n > 0 {
						size = n
					}
				}
				if fanout > 1_000_000_001/max(1, size) {
					next = 1_000_000_001
				} else {
					next *= size
				}
				rows = min(1_000_000_001, rows+next)
			}
			walk(field.SelectionSet, next)
			cost = min(1_000_000_001, cost)
			resolvers = min(1_000_000_001, resolvers)
		}
	}
	walk(selection, 1)
	return
}

type planCacheEntry struct {
	bindings *executionBindings
}

func (a *App) executionPlan(project, apiSlug string) (*executionBindings, error) {
	key := apiRuntimeKey(project, apiSlug)
	a.cacheMu.RLock()
	entry, found := a.planCache[key]
	generation := a.cacheGeneration[key]
	a.cacheMu.RUnlock()
	if found && entry.bindings != nil {
		return entry.bindings, nil
	}
	resolvers, err := listResolversForAPI(a.ctx.AppReadDB(), project, apiSlug)
	if err != nil {
		return nil, err
	}
	sources, err := listSourcesForAPI(a.ctx.AppReadDB(), project, apiSlug)
	if err != nil {
		return nil, err
	}
	modules, err := listResolverModulesForAPI(a.ctx.AppReadDB(), project, apiSlug)
	if err != nil {
		return nil, err
	}
	bindings := &executionBindings{resolvers: make(map[string]resolverRecord), sources: make(map[int64]sourceRecord), modules: make(map[string]resolverModule)}
	for _, resolver := range resolvers {
		bindings.resolvers[resolver.ParentType+"."+resolver.FieldName] = resolver
	}
	for _, source := range sources {
		bindings.sources[source.ID] = source
	}
	for _, module := range modules {
		bindings.modules[moduleKey(module.Name, module.Version)] = module
	}
	a.cacheMu.Lock()
	if a.planCache == nil || len(a.planCache) >= compiledCacheLimit {
		a.planCache = make(map[string]planCacheEntry)
	}
	if a.cacheGeneration[key] == generation {
		a.planCache[key] = planCacheEntry{bindings: bindings}
	}
	a.cacheMu.Unlock()
	return bindings, nil
}

func errorObjects(messages []string) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		out = append(out, map[string]any{"message": message, "extensions": map[string]any{"code": "query_validation_failed"}})
	}
	return out
}

func errorCode(err error) string {
	var e *graphqlError
	if errors.As(err, &e) {
		return e.Code
	}
	return "execution_error"
}

func maxQueryDepth(ctx *sdk.AppCtx) int {
	if value := ctx.Config()["max_query_depth"]; value != "" {
		var n int
		_, _ = fmt.Sscan(value, &n)
		if n > 0 {
			return n
		}
	}
	return 12
}

func maxQueryComplexity(ctx *sdk.AppCtx) int {
	if value := ctx.Config()["max_query_complexity"]; value != "" {
		var n int
		_, _ = fmt.Sscan(value, &n)
		if n > 0 {
			return n
		}
	}
	return 1000
}

func (a *App) executeSelection(ctx context.Context, project, apiSlug, parentType string, parentValue any, selection ast.SelectionSet, vars map[string]any) (any, error) {
	// Only independent query roots run concurrently. Mutations retain order.
	if parentValue == nil && parentType == "Query" && len(selection) > 1 {
		if batched, remaining, ok, err := a.executeBatchedTableRoots(ctx, project, apiSlug, selection, vars); err != nil {
			return nil, err
		} else if ok {
			if len(remaining) == 0 {
				return batched, nil
			}
			other, err := a.executeSelection(ctx, project, apiSlug, parentType, nil, remaining, vars)
			if err != nil {
				return nil, err
			}
			for key, value := range other.(map[string]any) {
				batched[key] = value
			}
			return batched, nil
		}
		values := make([]any, len(selection))
		errors := make([]error, len(selection))
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for i, item := range selection {
			sem <- struct{}{}
			wg.Add(1)
			go func(i int, item ast.Selection) {
				defer wg.Done()
				defer func() { <-sem }()
				values[i], errors[i] = a.executeSelection(ctx, project, apiSlug, parentType, nil, ast.SelectionSet{item}, vars)
			}(i, item)
		}
		wg.Wait()
		result := map[string]any{}
		for i, value := range values {
			if errors[i] != nil {
				return nil, errors[i]
			}
			for key, fieldValue := range value.(map[string]any) {
				result[key] = fieldValue
			}
		}
		return result, nil
	}
	if list, ok := parentValue.([]any); ok {
		if projected, ok := projectScalarRows(ctx, parentType, list, selection); ok {
			return projected, nil
		}
		out := make([]any, 0, len(list))
		for _, item := range list {
			value, err := a.executeSelection(ctx, project, apiSlug, parentType, item, selection, vars)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	}
	if parentValue != nil {
		if records, ok := parentValue.([]map[string]any); ok {
			rows := make([]any, len(records))
			for i := range records {
				rows[i] = records[i]
			}
			if projected, ok := projectScalarRows(ctx, parentType, rows, selection); ok {
				return projected, nil
			}
			out := make([]any, 0, len(records))
			for _, item := range records {
				value, err := a.executeSelection(ctx, project, apiSlug, parentType, item, selection, vars)
				if err != nil {
					return nil, err
				}
				out = append(out, value)
			}
			return out, nil
		}
	}
	result := map[string]any{}
	for _, selectionItem := range selection {
		field, ok := selectionItem.(*ast.Field)
		if !ok {
			continue
		}
		key := field.Name
		if field.Alias != "" {
			key = field.Alias
		}
		if field.Name == "__typename" {
			result[key] = parentType
			continue
		}
		var value any
		var err error
		if parentMap, ok := parentValue.(map[string]any); ok {
			value = parentMap[field.Name]
		}
		var resolver *resolverRecord
		var resolverErr error
		if bindings, ok := ctx.Value(executionBindingsKey{}).(*executionBindings); ok {
			if found, exists := bindings.resolvers[parentType+"."+field.Name]; exists {
				resolver = &found
			}
		} else {
			resolver, resolverErr = getResolverForAPI(a.ctx.AppReadDB(), project, apiSlug, parentType, field.Name)
		}
		if resolverErr != nil {
			return nil, resolverErr
		}
		if resolver != nil {
			value, err = a.resolveField(ctx, project, apiSlug, *resolver, parentValue, field, vars)
			if err != nil {
				return nil, err
			}
		} else if parentValue == nil && (parentType == "Query" || parentType == "Mutation" || parentType == "Subscription") {
			return nil, invalid("no resolver configured for %s.%s", parentType, field.Name)
		}
		if len(field.SelectionSet) > 0 && value != nil {
			nextType := field.Definition.Type.NamedType
			value, err = a.executeSelection(ctx, project, apiSlug, nextType, value, field.SelectionSet, vars)
			if err != nil {
				return nil, err
			}
		}
		result[key] = value
	}
	return result, nil
}

// projectScalarRows is the hot path for Tables results. Tables has already
// materialized row maps, so walking the GraphQL selection once per row is
// unnecessary when the selection contains only scalar fields. Resolver-backed
// or nested fields deliberately fall back to the general executor.
func projectScalarRows(ctx context.Context, parentType string, rows []any, selection ast.SelectionSet) ([]any, bool) {
	bindings, hasBindings := ctx.Value(executionBindingsKey{}).(*executionBindings)
	if !hasBindings {
		return nil, false
	}
	fields := make([]*ast.Field, 0, len(selection))
	for _, item := range selection {
		field, ok := item.(*ast.Field)
		if !ok || len(field.SelectionSet) > 0 {
			return nil, false
		}
		if field.Name != "__typename" {
			if _, hasResolver := bindings.resolvers[parentType+"."+field.Name]; hasResolver {
				return nil, false
			}
		}
		fields = append(fields, field)
	}
	out := make([]any, len(rows))
	for i, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			return nil, false
		}
		projected := make(map[string]any, len(fields))
		for _, field := range fields {
			key := field.Name
			if field.Alias != "" {
				key = field.Alias
			}
			if field.Name == "__typename" {
				projected[key] = parentType
			} else {
				projected[key] = row[field.Name]
			}
		}
		out[i] = projected
	}
	return out, true
}

// executeBatchedTableRoots combines independent Tables-backed root fields
// into one tables_batch call. Fields backed by other source kinds are left for
// the normal concurrent executor. This keeps the optimization transparent to
// mixed-source queries and preserves the existing resolver semantics.
func (a *App) executeBatchedTableRoots(ctx context.Context, project, apiSlug string, selection ast.SelectionSet, vars map[string]any) (map[string]any, ast.SelectionSet, bool, error) {
	bindings, ok := ctx.Value(executionBindingsKey{}).(*executionBindings)
	if !ok {
		return nil, selection, false, nil
	}
	operations := make([]map[string]any, 0, len(selection))
	fields := make([]*ast.Field, 0, len(selection))
	remaining := make(ast.SelectionSet, 0, len(selection))
	for _, item := range selection {
		field, isField := item.(*ast.Field)
		if !isField {
			remaining = append(remaining, item)
			continue
		}
		resolver, hasResolver := bindings.resolvers["Query."+field.Name]
		source, hasSource := bindings.sources[resolver.SourceID]
		if !hasResolver || !hasSource || source.Kind != "tables" {
			remaining = append(remaining, item)
			continue
		}
		operation, args, supported := tablesBatchInput(source, resolver, field, vars)
		if !supported {
			remaining = append(remaining, item)
			continue
		}
		opID := fmt.Sprintf("op%d", len(operations))
		operations = append(operations, map[string]any{"id": opID, "operation": operation, "args": args})
		fields = append(fields, field)
	}
	if len(operations) < 2 {
		return nil, selection, false, nil
	}
	// For large result sets, independent calls are faster than the current
	// batch envelope because they stream and decode concurrently. Reserve the
	// batch path for small fan-outs where envelope overhead is negligible.
	totalLimit := 0
	for _, operation := range operations {
		args, _ := operation["args"].(map[string]any)
		limit := 100
		switch value := args["limit"].(type) {
		case int:
			limit = value
		case int64:
			limit = int(value)
		case float64:
			limit = int(value)
		}
		if limit > 0 {
			totalLimit += limit
		}
	}
	if totalLimit > 500 {
		return nil, selection, false, nil
	}
	input := map[string]any{"mode": "best_effort", "operations": operations, "_project_id": project}
	var out map[string]any
	if err := a.ctx.WithProject(project).PlatformAPI().CallAppResult("tables", "tables_batch", input, &out); err != nil {
		return nil, nil, true, err
	}
	results, _ := out["results"].(map[string]any)
	data := make(map[string]any, len(fields))
	for i, field := range fields {
		opID := fmt.Sprintf("op%d", i)
		id := field.Name
		if field.Alias != "" {
			id = field.Alias
		}
		entry, _ := results[opID].(map[string]any)
		if status, _ := entry["status"].(string); status != "ok" {
			if detail, ok := entry["error"].(map[string]any); ok {
				if message, _ := detail["message"].(string); message != "" {
					return nil, nil, true, fmt.Errorf("tables batch operation %s: %s", id, message)
				}
			}
			if message, _ := entry["error"].(string); message != "" {
				return nil, nil, true, fmt.Errorf("tables batch operation %s: %s", id, message)
			}
			return nil, nil, true, fmt.Errorf("tables batch operation %s failed", id)
		}
		value := unwrapSourceResult(bindings.resolvers["Query."+field.Name].Operation, entry["result"])
		if len(field.SelectionSet) > 0 && value != nil {
			var err error
			value, err = a.executeSelection(ctx, project, apiSlug, field.Definition.Type.NamedType, value, field.SelectionSet, vars)
			if err != nil {
				return nil, nil, true, err
			}
		}
		data[id] = value
	}
	return data, remaining, true, nil
}

func tablesBatchInput(source sourceRecord, resolver resolverRecord, field *ast.Field, vars map[string]any) (string, map[string]any, bool) {
	args := field.ArgumentMap(vars)
	config := mergeMaps(source.Config, resolver.Config)
	input := tablesReadInput(config, args)
	operation := strings.ToLower(resolver.Operation)
	switch operation {
	case "find", "list", "search":
		return "rows_search", input, true
	case "count":
		return "rows_count", input, true
	case "aggregate":
		return "rows_aggregate", input, true
	case "get":
		return "rows_get", input, true
	default:
		return "", nil, false
	}
}

func (a *App) resolveField(ctx context.Context, project, apiSlug string, resolver resolverRecord, parent any, field *ast.Field, vars map[string]any) (any, error) {
	var source *sourceRecord
	var err error
	if bindings, ok := ctx.Value(executionBindingsKey{}).(*executionBindings); ok {
		if found, exists := bindings.sources[resolver.SourceID]; exists {
			source = &found
		}
	} else {
		source, err = getSourceForAPI(a.ctx.AppReadDB(), project, apiSlug, resolver.SourceID, "")
	}
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, internal("resolver source not found")
	}
	args := field.ArgumentMap(vars)
	config := mergeMaps(source.Config, resolver.Config)
	config["_project_id"] = project
	config["graphql_args"] = args
	if parent != nil {
		config["parent"] = parent
	}
	switch source.Kind {
	case "database":
		return a.callDatabase(ctx, resolver.Operation, config)
	case "tables":
		return a.callTables(ctx, resolver.Operation, config)
	case "function":
		return a.callFunction(ctx, config, args)
	case "http":
		return a.callHTTP(ctx, config, args)
	case "module":
		return resolveModuleValue(ctx, source.Config, resolver.Config, parent, args, bindingsFromContext(ctx))
	default:
		return nil, invalid("unsupported source kind %q", source.Kind)
	}
}

func sourceInput(config map[string]any, args map[string]any, names ...string) map[string]any {
	out := map[string]any{}
	for _, name := range names {
		if value, ok := config[name]; ok {
			out[name] = value
		}
		if value, ok := args[name]; ok {
			out[name] = value
		}
	}
	return out
}

func resolverArgs(config map[string]any) map[string]any {
	if args, ok := config["graphql_args"].(map[string]any); ok {
		return args
	}
	return map[string]any{}
}

func (a *App) callDatabase(ctx context.Context, operation string, config map[string]any) (any, error) {
	args := resolverArgs(config)
	input := sourceInput(config, args, "database", "collection", "where", "select", "orderBy", "limit", "cursor", "key", "metrics", "groupBy", "order_by", "group_by", "timeoutMs")
	if value, ok := config["database"].(string); ok {
		input["database"] = value
	}
	if value, ok := config["collection"].(string); ok {
		input["collection"] = value
	}
	if _, ok := input["groupBy"]; !ok {
		if value, exists := input["group_by"]; exists {
			input["groupBy"] = value
		}
	}
	if _, ok := input["orderBy"]; !ok {
		if value, exists := input["order_by"]; exists {
			input["orderBy"] = value
		}
	}
	input["_project_id"] = config["_project_id"]
	tool := "db_" + strings.ToLower(operation)
	if operation == "find" || operation == "list" {
		tool = "db_find"
	}
	if operation == "aggregate" {
		tool = "db_aggregate"
	}
	var out any
	if err := a.ctx.WithProject(config["_project_id"].(string)).PlatformAPI().CallAppResult("database", tool, input, &out); err != nil {
		return nil, err
	}
	return unwrapSourceResult(operation, out), nil
}

func (a *App) callTables(ctx context.Context, operation string, config map[string]any) (any, error) {
	args := resolverArgs(config)
	input, err := mappedTablesInput(config, args)
	if err != nil {
		return nil, err
	}
	input["_project_id"] = config["_project_id"]
	tool := "rows_" + strings.ToLower(operation)
	if operation == "find" || operation == "list" {
		tool = "rows_search"
	}
	var out any
	if err := sdk.CallAppResultContext(ctx, a.ctx.WithProject(config["_project_id"].(string)).PlatformAPI(), "tables", tool, input, &out); err != nil {
		return nil, err
	}
	return unwrapSourceResult(operation, out), nil
}

// Both dispatch paths must use identical filter, ordering and projection inputs.
// GraphQL list fields return rows, not pagination totals, so counting is opt-in.
func tablesReadInput(config, args map[string]any) map[string]any {
	input := sourceInput(config, args, "table", "where", "select", "order_by", "limit", "offset", "metrics", "group_by", "orderBy", "groupBy", "id", "key", "include_total", "includeTotal", "cursor", "first", "after", "hydrate_files")
	if value, exists := input["first"]; exists {
		input["limit"] = value
		delete(input, "first")
	}
	if value, exists := input["after"]; exists {
		input["cursor"] = value
		delete(input, "after")
	}
	if value, exists := input["includeTotal"]; exists {
		input["include_total"] = value
		delete(input, "includeTotal")
	}
	if _, exists := input["include_total"]; !exists {
		input["include_total"] = false
	}
	if _, ok := input["group_by"]; !ok {
		if value, exists := input["groupBy"]; exists {
			input["group_by"] = value
		}
	}
	if _, ok := input["order_by"]; !ok {
		if value, exists := input["orderBy"]; exists {
			input["order_by"] = value
		}
	}
	return input
}

func (a *App) callFunction(ctx context.Context, config map[string]any, args map[string]any) (any, error) {
	security, err := functionSecurity(config)
	if err != nil {
		return nil, err
	}
	if security.Authenticated {
		return a.callTrustedFunction(ctx, config, args, security)
	}
	name, _ := config["name"].(string)
	if name == "" {
		return nil, invalid("function source requires config.name")
	}
	event := map[string]any{"arguments": args, "parent": config["parent"], "project_id": config["_project_id"]}
	var out struct {
		Status   string `json:"status"`
		Response string `json:"response"`
		Error    string `json:"error"`
	}
	if err := a.ctx.WithProject(config["_project_id"].(string)).PlatformAPI().CallAppResult("functions", "functions_invoke", map[string]any{"name": name, "event": event, "_project_id": config["_project_id"]}, &out); err != nil {
		return nil, err
	}
	if out.Status != "" && out.Status != "ok" {
		if out.Error == "" {
			out.Error = "function invocation failed"
		}
		return nil, fmt.Errorf("%s", out.Error)
	}
	if out.Response == "" {
		return nil, nil
	}
	var value any
	if json.Unmarshal([]byte(out.Response), &value) == nil {
		return value, nil
	}
	return out.Response, nil
}

func (a *App) callHTTP(ctx context.Context, config map[string]any, args map[string]any) (any, error) {
	endpoint, _ := config["url"].(string)
	if endpoint == "" {
		return nil, invalid("HTTP source requires config.url")
	}
	method := strings.ToUpper(stringValue(config["method"], "GET"))
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, invalid("invalid HTTP source URL")
	}
	q := u.Query()
	if configured, ok := config["query"].(map[string]any); ok {
		for key, value := range configured {
			q.Set(key, fmt.Sprint(value))
		}
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	for key, value := range stringMap(config["headers"]) {
		req.Header.Set(key, value)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP source returned %s", resp.Status)
	}
	var value any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func unwrapSourceResult(operation string, value any) any {
	obj, ok := value.(map[string]any)
	if !ok {
		return value
	}
	switch strings.ToLower(operation) {
	case "find", "list":
		if rows, ok := obj["records"]; ok {
			return rows
		}
		if rows, ok := obj["rows"]; ok {
			return rows
		}
	case "aggregate":
		if rows, ok := obj["rows"]; ok {
			return rows
		}
	case "get":
		if row, ok := obj["record"]; ok {
			return row
		}
		if row, ok := obj["row"]; ok {
			return row
		}
	case "count":
		if count, ok := obj["count"]; ok {
			return count
		}
	}
	return value
}

func stringValue(value any, fallback string) string {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return text
	}
	return fallback
}

func stringMap(value any) map[string]string {
	out := map[string]string{}
	if input, ok := value.(map[string]any); ok {
		for key, item := range input {
			out[key] = fmt.Sprint(item)
		}
	}
	return out
}
