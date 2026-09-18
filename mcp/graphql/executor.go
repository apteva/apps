package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
	"github.com/vektah/gqlparser/v2/ast"
)

type graphqlRequest struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
	Environment   string         `json:"environment,omitempty"`
}

type executeResult struct {
	Data          map[string]any
	Errors        []map[string]any
	OperationName string
	OperationType string
}

func (a *App) execute(ctx context.Context, project, environment string, req graphqlRequest) (executeResult, error) {
	if strings.TrimSpace(req.Query) == "" {
		return executeResult{}, invalid("query is required")
	}
	if req.Variables == nil {
		req.Variables = map[string]any{}
	}
	schemaRow, err := getSchema(a.ctx.AppReadDB(), project, environment, 0, true)
	if err != nil {
		return executeResult{}, err
	}
	if schemaRow == nil {
		return executeResult{}, invalid("no published schema for environment %q", normalizeEnvironment(environment))
	}
	schema, schemaErrors := validateSDL(schemaRow.SDL)
	if len(schemaErrors) > 0 {
		return executeResult{}, internal("published schema is invalid")
	}
	doc, queryErrors := parseAndValidateQuery(schema, req.Query)
	if len(queryErrors) > 0 {
		return executeResult{Errors: errorObjects(queryErrors)}, nil
	}
	op, err := operationFor(doc, req.OperationName)
	if err != nil {
		return executeResult{}, err
	}
	fields, depth := queryCost(op.SelectionSet, 0)
	if fields > maxQueryComplexity(a.ctx) {
		return executeResult{}, invalid("query complexity %d exceeds limit %d", fields, maxQueryComplexity(a.ctx))
	}
	if depth > maxQueryDepth(a.ctx) {
		return executeResult{}, invalid("query depth %d exceeds limit %d", depth, maxQueryDepth(a.ctx))
	}
	rootType := operationType(op)
	data, err := a.executeSelection(ctx, project, rootType, nil, op.SelectionSet, req.Variables)
	if err != nil {
		return executeResult{OperationName: op.Name, OperationType: string(op.Operation), Errors: []map[string]any{{"message": err.Error(), "extensions": map[string]any{"code": errorCode(err)}}}}, nil
	}
	return executeResult{Data: data.(map[string]any), OperationName: op.Name, OperationType: string(op.Operation)}, nil
}

func errorObjects(messages []string) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		out = append(out, map[string]any{"message": message, "extensions": map[string]any{"code": "query_validation_failed"}})
	}
	return out
}

func errorCode(err error) string {
	if e, ok := err.(*graphqlError); ok {
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

func (a *App) executeSelection(ctx context.Context, project, parentType string, parentValue any, selection ast.SelectionSet, vars map[string]any) (any, error) {
	if list, ok := parentValue.([]any); ok {
		out := make([]any, 0, len(list))
		for _, item := range list {
			value, err := a.executeSelection(ctx, project, parentType, item, selection, vars)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	}
	if parentValue != nil {
		if records, ok := parentValue.([]map[string]any); ok {
			out := make([]any, 0, len(records))
			for _, item := range records {
				value, err := a.executeSelection(ctx, project, parentType, item, selection, vars)
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
		resolver, resolverErr := getResolver(a.ctx.AppReadDB(), project, parentType, field.Name)
		if resolverErr != nil {
			return nil, resolverErr
		}
		if resolver != nil {
			value, err = a.resolveField(ctx, project, *resolver, parentValue, field, vars)
			if err != nil {
				return nil, err
			}
		} else if parentValue == nil && (parentType == "Query" || parentType == "Mutation" || parentType == "Subscription") {
			return nil, invalid("no resolver configured for %s.%s", parentType, field.Name)
		}
		if len(field.SelectionSet) > 0 && value != nil {
			nextType := field.Definition.Type.NamedType
			value, err = a.executeSelection(ctx, project, nextType, value, field.SelectionSet, vars)
			if err != nil {
				return nil, err
			}
		}
		result[key] = value
	}
	return result, nil
}

func (a *App) resolveField(ctx context.Context, project string, resolver resolverRecord, parent any, field *ast.Field, vars map[string]any) (any, error) {
	source, err := getSource(a.ctx.AppReadDB(), project, resolver.SourceID, "")
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
	input := sourceInput(config, args, "table", "where", "select", "order_by", "limit", "offset", "metrics", "group_by", "orderBy", "groupBy")
	input["_project_id"] = config["_project_id"]
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
	tool := "rows_" + strings.ToLower(operation)
	if operation == "find" || operation == "list" {
		tool = "rows_search"
	}
	var out any
	if err := a.ctx.WithProject(config["_project_id"].(string)).PlatformAPI().CallAppResult("tables", tool, input, &out); err != nil {
		return nil, err
	}
	return unwrapSourceResult(operation, out), nil
}

func (a *App) callFunction(ctx context.Context, config map[string]any, args map[string]any) (any, error) {
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
