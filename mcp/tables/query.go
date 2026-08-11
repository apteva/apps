package main

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	sqlite "modernc.org/sqlite"
)

// toolTablesQuery is the read-only SELECT escape hatch. Not pretty,
// but sometimes the agent needs aggregations or joins the typed tools
// can't express. Three guard rails keep this safe enough for v0.1:
//
//  1. The dedicated sqlite connection runs with PRAGMA query_only=ON.
//  2. Raw internal/physical names are blocked; project-visible tables
//     are reached only through {table_name} placeholders.
//  3. Statement count, duration, result rows, and result bytes are capped.
//
// Cross-table joins use {table_name} placeholders, which are resolved
// strictly against the current project.
func (a *App) toolTablesQuery(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.schemaMu.RLock()
	defer a.schemaMu.RUnlock()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rawSQL := strings.TrimSpace(strArg(args, "sql"))
	if rawSQL == "" {
		return nil, errf("sql is required")
	}
	if len(rawSQL) > 64<<10 {
		return nil, errf("sql exceeds 65536 bytes")
	}
	if err := validateReadOnlySQL(rawSQL); err != nil {
		return nil, err
	}
	resolved, err := a.substitutePlaceholders(ctx, pid, rawSQL)
	if err != nil {
		return nil, err
	}

	params := sliceArg(args, "params")
	if len(params) > 1000 {
		return nil, errf("params exceeds 1000 values")
	}
	bound := make([]any, len(params))
	copy(bound, params)

	read, err := acquireReadConn(ctx, "<sql>")
	if err != nil {
		return nil, err
	}
	conn := read.conn
	defer conn.Close()
	qctx, cancel := queryTimeoutContext(ctx)
	defer cancel()
	sharedWriterConnection := ctx.AppReadDB() == ctx.AppDB()
	if sharedWriterConnection {
		if _, err := conn.ExecContext(qctx, "PRAGMA query_only = ON"); err != nil {
			return nil, errf("enable sqlite query_only: %v", err)
		}
	}
	previousLengthLimit := -1
	defer func() {
		resetCtx, resetCancel := context.WithTimeout(context.Background(), time.Second)
		defer resetCancel()
		if previousLengthLimit >= 0 {
			_, _ = sqlite.Limit(conn, 0, previousLengthLimit)
		}
		if sharedWriterConnection {
			_, _ = conn.ExecContext(resetCtx, "PRAGMA query_only = OFF")
		}
	}()
	previousLengthLimit, err = sqlite.Limit(conn, 0, int(maxQueryBytes(ctx))) // SQLITE_LIMIT_LENGTH
	if err != nil {
		return nil, errf("set sqlite result length limit: %v", err)
	}

	started := time.Now()
	rows, err := conn.QueryContext(qctx, resolved, bound...)
	if err != nil {
		elapsed := time.Since(started)
		logReadQuery(ctx, "<sql>", "tables_query", read.queueWait, 0, elapsed, err, "select")
		return nil, queryStageErr("select", "<sql>", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if len(cols) > 256 {
		return nil, errf("query result exceeds maximum of 256 columns")
	}
	cap := maxQueryRows(ctx)
	byteCap := maxQueryBytes(ctx)
	var usedBytes int64
	out := make([]map[string]any, 0)
	truncated := false
	for rows.Next() {
		if len(out) >= cap {
			truncated = true
			break
		}
		dest := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, c := range cols {
			v := normaliseScanValue(dest[i])
			usedBytes += int64(len(c)) + valueSize(v)
			if usedBytes > byteCap {
				truncated = true
				break
			}
			row[c] = v
		}
		if truncated {
			break
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	elapsed := time.Since(started)
	logReadQuery(ctx, "<sql>", "tables_query", read.queueWait, 0, elapsed, nil, "")
	return map[string]any{
		"columns":   cols,
		"rows":      out,
		"truncated": truncated,
	}, nil
}

var (
	queryPlaceholderRe = regexp.MustCompile(`\{[a-z][a-z0-9_]*\}`)
	internalSQLNameRe  = regexp.MustCompile(`(?i)(?:\b(?:t_[0-9]+|tables_meta|columns_meta|sqlite_[a-z0-9_]*|pragma_[a-z0-9_]*|dbstat)\b|\b_migrations\b)`)
)

// validateReadOnlySQL rejects anything but a single SELECT or WITH
// statement. It does not try to defend against truly hostile input —
// the agent operates inside the install's permission scope already.
func validateReadOnlySQL(s string) error {
	stripped := strings.TrimRight(s, " \t\n\r;")
	if strings.Contains(stripped, ";") {
		return errf("multi-statement queries not allowed")
	}
	lower := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.HasPrefix(lower, "select"), strings.HasPrefix(lower, "with"):
		withoutPlaceholders := queryPlaceholderRe.ReplaceAllString(lower, "")
		if name := internalSQLNameRe.FindString(withoutPlaceholders); name != "" {
			return errf("direct access to internal or physical table %q is not allowed; use {table_name}", name)
		}
		return nil
	}
	return errf("only SELECT and WITH (CTE) statements allowed")
}

// substitutePlaceholders resolves {table_name} → physical table name
// for every user-table the project owns, leaving anything else alone.
// This is the only mechanism the agent has to reach into user-tables
// from raw SQL; we never expose physical names directly.
func (a *App) substitutePlaceholders(ctx *sdk.AppCtx, projectID, query string) (string, error) {
	if !strings.ContainsAny(query, "{}") {
		return query, nil
	}
	out := query
	seen := map[string]bool{}
	for _, placeholder := range queryPlaceholderRe.FindAllString(query, -1) {
		name := strings.TrimSuffix(strings.TrimPrefix(placeholder, "{"), "}")
		if seen[name] {
			continue
		}
		seen[name] = true
		table, err := a.loadTableSchema(ctx, projectID, name)
		if err != nil {
			return "", err
		}
		out = strings.ReplaceAll(out, placeholder, quote(table.PhysicalName))
	}
	if strings.ContainsAny(out, "{}") {
		// Any unresolved placeholder is almost certainly a typo — fail
		// loud rather than passing literal "{foo}" into sqlite which
		// produces a confusing parse error.
		return "", errf("unresolved {placeholder} in sql — check table names")
	}
	return out, nil
}

func normaliseScanValue(v any) any {
	switch n := v.(type) {
	case []byte:
		// sqlite returns BLOB and TEXT both as []byte. Try to parse as
		// JSON first (round-trips json columns); fall back to string.
		var j any
		if err := json.Unmarshal(n, &j); err == nil {
			switch j.(type) {
			case map[string]any, []any:
				return j
			}
		}
		return string(n)
	case time.Time:
		return n.UTC().Format(time.RFC3339)
	}
	return v
}
