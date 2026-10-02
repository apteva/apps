package main

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// logFilter is shared by the MCP tool and the admin endpoint so automation
// and the GraphQL panel have identical server-side filtering semantics.
type logFilter struct {
	limit            int
	minDurationMS    *int64
	maxDurationMS    *int64
	minResponseBytes *int64
	maxResponseBytes *int64
	minRows          *int64
	maxRows          *int64
	minResolvers     *int64
	maxResolvers     *int64
	statusCode       *int
	operationName    string
	operationType    string
	since            string
	until            string
	sortBy           string
	sortOrder        string
}

func defaultLogFilter() logFilter {
	return logFilter{limit: 100, sortBy: "created_at", sortOrder: "desc"}
}

func (f *logFilter) normalize() error {
	if f.limit <= 0 {
		f.limit = 100
	}
	if f.limit > 500 {
		f.limit = 500
	}
	for name, value := range map[string]*int64{
		"min_duration_ms":    f.minDurationMS,
		"max_duration_ms":    f.maxDurationMS,
		"min_response_bytes": f.minResponseBytes,
		"max_response_bytes": f.maxResponseBytes,
		"min_rows":           f.minRows,
		"max_rows":           f.maxRows,
		"min_resolvers":      f.minResolvers,
		"max_resolvers":      f.maxResolvers,
	} {
		if value != nil && *value < 0 {
			return invalid("%s must be non-negative", name)
		}
	}
	for _, bounds := range []struct {
		name string
		min  *int64
		max  *int64
	}{
		{"duration", f.minDurationMS, f.maxDurationMS},
		{"response_bytes", f.minResponseBytes, f.maxResponseBytes},
		{"rows", f.minRows, f.maxRows},
		{"resolvers", f.minResolvers, f.maxResolvers},
	} {
		if bounds.min != nil && bounds.max != nil && *bounds.min > *bounds.max {
			return invalid("min_%s cannot exceed max_%s", bounds.name, bounds.name)
		}
	}
	if f.statusCode != nil && (*f.statusCode < 100 || *f.statusCode > 599) {
		return invalid("status_code must be between 100 and 599")
	}
	f.operationName = strings.TrimSpace(f.operationName)
	f.operationType = strings.ToLower(strings.TrimSpace(f.operationType))
	if f.operationType != "" && f.operationType != "query" && f.operationType != "mutation" && f.operationType != "subscription" {
		return invalid("operation_type must be query, mutation, or subscription")
	}
	if f.sortBy == "" {
		f.sortBy = "created_at"
	}
	switch f.sortBy {
	case "created_at", "duration_ms", "response_bytes", "row_count", "resolver_count", "status_code", "operation_name":
	default:
		return invalid("sort_by must be created_at, duration_ms, response_bytes, row_count, resolver_count, status_code, or operation_name")
	}
	f.sortOrder = strings.ToLower(strings.TrimSpace(f.sortOrder))
	if f.sortOrder == "" {
		f.sortOrder = "desc"
	}
	if f.sortOrder != "asc" && f.sortOrder != "desc" {
		return invalid("sort_order must be asc or desc")
	}
	for name, value := range map[string]string{"since": f.since, "until": f.until} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
		if err != nil {
			return invalid("%s must be an RFC3339 timestamp", name)
		}
		if name == "since" {
			f.since = parsed.UTC().Format(time.RFC3339Nano)
		} else {
			f.until = parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	if f.since != "" && f.until != "" && f.since > f.until {
		return invalid("since cannot be after until")
	}
	return nil
}

func optionalLogIntArg(args map[string]any, key string) (*int64, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return nil, nil
	}
	var parsed int64
	switch value := value.(type) {
	case int:
		parsed = int64(value)
	case int8:
		parsed = int64(value)
	case int16:
		parsed = int64(value)
	case int32:
		parsed = int64(value)
	case int64:
		parsed = value
	case float64:
		if value != float64(int64(value)) {
			return nil, invalid("%s must be an integer", key)
		}
		parsed = int64(value)
	case json.Number:
		var err error
		parsed, err = strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return nil, invalid("%s must be an integer", key)
		}
	default:
		return nil, invalid("%s must be an integer", key)
	}
	return &parsed, nil
}

func parseLogFiltersArgs(args map[string]any) (logFilter, error) {
	filter := defaultLogFilter()
	filter.limit = intArg(args, "limit", filter.limit)
	var err error
	fields := []struct {
		key    string
		target **int64
	}{
		{"min_duration_ms", &filter.minDurationMS}, {"max_duration_ms", &filter.maxDurationMS},
		{"min_response_bytes", &filter.minResponseBytes}, {"max_response_bytes", &filter.maxResponseBytes},
		{"min_rows", &filter.minRows}, {"max_rows", &filter.maxRows},
		{"min_resolvers", &filter.minResolvers}, {"max_resolvers", &filter.maxResolvers},
	}
	for _, field := range fields {
		if *field.target, err = optionalLogIntArg(args, field.key); err != nil {
			return filter, err
		}
	}
	if value, ok := args["status_code"]; ok && value != nil {
		parsed, parseErr := optionalLogIntArg(args, "status_code")
		if parseErr != nil {
			return filter, parseErr
		}
		if parsed != nil {
			status := int(*parsed)
			filter.statusCode = &status
		}
	}
	filter.operationName = stringArg(args, "operation_name", "")
	filter.operationType = stringArg(args, "operation_type", "")
	filter.since = stringArg(args, "since", "")
	filter.until = stringArg(args, "until", "")
	filter.sortBy = stringArg(args, "sort_by", filter.sortBy)
	filter.sortOrder = stringArg(args, "sort_order", filter.sortOrder)
	return filter, filter.normalize()
}

func parseLogFiltersQuery(values url.Values) (logFilter, error) {
	filter := defaultLogFilter()
	parseInt := func(key string) (*int64, error) {
		value := strings.TrimSpace(values.Get(key))
		if value == "" {
			return nil, nil
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, invalid("%s must be an integer", key)
		}
		return &parsed, nil
	}
	if value := strings.TrimSpace(values.Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return filter, invalid("limit must be an integer")
		}
		filter.limit = parsed
	}
	fields := []struct {
		key    string
		target **int64
	}{
		{"min_duration_ms", &filter.minDurationMS}, {"max_duration_ms", &filter.maxDurationMS},
		{"min_response_bytes", &filter.minResponseBytes}, {"max_response_bytes", &filter.maxResponseBytes},
		{"min_rows", &filter.minRows}, {"max_rows", &filter.maxRows},
		{"min_resolvers", &filter.minResolvers}, {"max_resolvers", &filter.maxResolvers},
	}
	for _, field := range fields {
		var err error
		if *field.target, err = parseInt(field.key); err != nil {
			return filter, err
		}
	}
	if value := strings.TrimSpace(values.Get("status_code")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return filter, invalid("status_code must be an integer")
		}
		filter.statusCode = &parsed
	}
	filter.operationName = values.Get("operation_name")
	filter.operationType = values.Get("operation_type")
	filter.since = values.Get("since")
	filter.until = values.Get("until")
	filter.sortBy = values.Get("sort_by")
	filter.sortOrder = values.Get("sort_order")
	return filter, filter.normalize()
}

func publicLogsFiltered(db *sql.DB, project string, filter logFilter) ([]map[string]any, error) {
	if err := filter.normalize(); err != nil {
		return nil, err
	}
	query := `SELECT id, operation_name, operation_type, status_code, duration_ms, error, created_at, operation_hash, api_release, response_bytes, row_count, resolver_count, source_timings_json, error_codes_json, authorization_scope, request_id
        FROM graphql_request_logs WHERE project_id=?`
	args := []any{project}
	conditions := []struct {
		value *int64
		sql   string
	}{
		{filter.minDurationMS, " AND duration_ms>=?"}, {filter.maxDurationMS, " AND duration_ms<=?"},
		{filter.minResponseBytes, " AND response_bytes>=?"}, {filter.maxResponseBytes, " AND response_bytes<=?"},
		{filter.minRows, " AND row_count>=?"}, {filter.maxRows, " AND row_count<=?"},
		{filter.minResolvers, " AND resolver_count>=?"}, {filter.maxResolvers, " AND resolver_count<=?"},
	}
	for _, condition := range conditions {
		if condition.value != nil {
			query += condition.sql
			args = append(args, *condition.value)
		}
	}
	if filter.statusCode != nil {
		query += " AND status_code=?"
		args = append(args, *filter.statusCode)
	}
	if filter.operationName != "" {
		query += " AND operation_name=?"
		args = append(args, filter.operationName)
	}
	if filter.operationType != "" {
		query += " AND operation_type=?"
		args = append(args, filter.operationType)
	}
	if filter.since != "" {
		query += " AND created_at>=?"
		args = append(args, filter.since)
	}
	if filter.until != "" {
		query += " AND created_at<=?"
		args = append(args, filter.until)
	}
	query += " ORDER BY " + filter.sortBy + " " + strings.ToUpper(filter.sortOrder) + ", id DESC LIMIT ?"
	args = append(args, filter.limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, duration int64
		var operationName, operationType, message, created, operationHash, sourceTimings, errorCodes, authScope, requestID string
		var status int
		var release, responseBytes, rowCount, resolverCount int
		if err := rows.Scan(&id, &operationName, &operationType, &status, &duration, &message, &created, &operationHash, &release, &responseBytes, &rowCount, &resolverCount, &sourceTimings, &errorCodes, &authScope, &requestID); err != nil {
			return nil, err
		}
		var timings any
		_ = json.Unmarshal([]byte(sourceTimings), &timings)
		var codes any
		_ = json.Unmarshal([]byte(errorCodes), &codes)
		out = append(out, map[string]any{"id": id, "operation_name": operationName, "operation_type": operationType, "status_code": status, "duration_ms": duration, "error": message, "created_at": created, "operation_hash": operationHash, "api_release": release, "response_bytes": responseBytes, "row_count": rowCount, "resolver_count": resolverCount, "source_timings": timings, "error_codes": codes, "authorization_scope": authScope, "request_id": requestID})
	}
	return out, rows.Err()
}
