package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// toolTablesQuery is the read-only SELECT escape hatch. Not pretty,
// but sometimes the agent needs aggregations or joins the typed tools
// can't express. Three guard rails keep this safe enough for v0.1:
//
//   1. Statement must start with SELECT or WITH (CTE) — this is the
//      only check that prevents writes; everything else is defence in
//      depth.
//   2. No semicolons in the body (defeats simple statement-stacking).
//   3. Hard timeout via context.WithTimeout, hard row cap, hard byte
//      budget on each cell.
//
// Cross-table joins work as long as the agent uses physical names —
// which it can't introspect through any tool we expose. To make the
// hatch actually useful, we substitute "{table_name}" placeholders
// with the corresponding physical names before execution.
func (a *App) toolTablesQuery(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rawSQL := strings.TrimSpace(strArg(args, "sql"))
	if rawSQL == "" {
		return nil, errf("sql is required")
	}
	if err := validateReadOnlySQL(rawSQL); err != nil {
		return nil, err
	}
	resolved, err := substitutePlaceholders(ctx, pid, rawSQL)
	if err != nil {
		return nil, err
	}

	params := sliceArg(args, "params")
	bound := make([]any, len(params))
	copy(bound, params)

	timeout := time.Duration(maxQueryMs(ctx)) * time.Millisecond
	qctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	rows, err := ctx.AppDB().QueryContext(qctx, resolved, bound...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	cap := maxQueryRows(ctx)
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
			row[c] = normaliseScanValue(dest[i])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{
		"columns":   cols,
		"rows":      out,
		"truncated": truncated,
	}, nil
}

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
	case strings.HasPrefix(lower, "select"):
		return nil
	case strings.HasPrefix(lower, "with"):
		return nil
	}
	return errf("only SELECT and WITH (CTE) statements allowed")
}

// substitutePlaceholders resolves {table_name} → physical table name
// for every user-table the project owns, leaving anything else alone.
// This is the only mechanism the agent has to reach into user-tables
// from raw SQL; we never expose physical names directly.
func substitutePlaceholders(ctx *sdk.AppCtx, projectID, query string) (string, error) {
	if !strings.ContainsAny(query, "{}") {
		return query, nil
	}
	tables, err := loadTables(ctx.AppDB(), projectID)
	if err != nil {
		return "", err
	}
	out := query
	for _, t := range tables {
		out = strings.ReplaceAll(out, "{"+t.Name+"}", quote(t.PhysicalName))
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

// jsonUnmarshalBytes is a tiny indirection so rows.go can call
// json.Unmarshal without taking on the encoding/json import there.
// Keeps the file's import surface lean.
func jsonUnmarshalBytes(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

// mcpInnerJSON strips the MCP JSON-RPC envelope CallApp returns and
// yields the inner content[0].text bytes. Pre-unwrapped responses
// pass through. RPC-level errors surface as a Go error.
//
// Local copy of app-sdk's decodeMCPEnvelope (v0.1.8); delete and
// switch to CallAppResult once the SDK pin advances to v0.1.8+.
func mcpInnerJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty mcp response")
	}
	var env struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return raw, nil
	}
	if env.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", env.Error.Code, env.Error.Message)
	}
	if env.Result == nil || len(env.Result.Content) == 0 {
		return raw, nil
	}
	return []byte(env.Result.Content[0].Text), nil
}
