package main

import (
	"context"
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
	ctx, finish, err := a.beginOperation(ctx, args, "tables_query", false)
	if err != nil {
		return nil, err
	}
	defer finish()
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
	if err := authorizeQuery(qctx, conn, ctx, a, pid, rawSQL, resolved, bound); err != nil {
		return nil, err
	}
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
	seenColumns := map[string]bool{}
	for _, col := range cols {
		if seenColumns[col] {
			return nil, errf("duplicate output column %q; use unique aliases", col)
		}
		seenColumns[col] = true
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
			if err := finiteResult(v); err != nil {
				return nil, err
			}
			row[c] = v
		}
		size, err := jsonSize(row, byteCap)
		if err != nil {
			return nil, err
		}
		usedBytes += size + 1
		if usedBytes > byteCap {
			if len(out) == 0 {
				return nil, oversized()
			}
			truncated = true
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
	internalSQLNameRe  = regexp.MustCompile(`(?i)(?:\b(?:t_[0-9]+|tables_meta|columns_meta|indexes_meta|index_columns|table_identity|sqlite_[a-z0-9_]*|pragma_[a-z0-9_]*|dbstat)\b|\b_migrations\b)`)
)

// validateReadOnlySQL rejects anything but a single SELECT or WITH
// statement. It does not try to defend against truly hostile input —
// the agent operates inside the install's permission scope already.
func validateReadOnlySQL(s string) error {
	tokens, err := sqlTokens(s)
	if err != nil {
		return err
	}
	if len(tokens) == 0 || tokens[0].kind != "identifier" || (strings.ToLower(tokens[0].value) != "select" && strings.ToLower(tokens[0].value) != "with") {
		return errf("only SELECT and WITH statements allowed")
	}
	terminated := false
	for _, t := range tokens {
		if t.kind == "symbol" && t.value == ";" {
			terminated = true
			continue
		}
		if terminated {
			return errf("multi-statement queries not allowed")
		}
		if t.kind == "identifier" && internalSQLNameRe.MatchString(t.value) {
			return errf("direct access to internal or physical table %q is not allowed; use {table_name}", t.value)
		}
	}
	return nil
}
func (a *App) substitutePlaceholders(ctx *sdk.AppCtx, projectID, query string) (string, error) {
	tokens, err := sqlTokens(query)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	last := 0
	for _, token := range tokens {
		if token.kind != "placeholder" {
			continue
		}
		table, err := a.loadTableSchema(ctx, projectID, token.value)
		if err != nil {
			return "", err
		}
		out.WriteString(query[last:token.start])
		out.WriteString(quote(table.PhysicalName))
		last = token.end
	}
	out.WriteString(query[last:])
	return out.String(), nil
}

func normaliseScanValue(v any) any {
	switch n := v.(type) {
	case []byte:
		// sqlite returns BLOB and TEXT both as []byte. Try to parse as
		// JSON first (round-trips json columns); fall back to string.
		j, err := jsonParse(string(n))
		if err == nil {
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
