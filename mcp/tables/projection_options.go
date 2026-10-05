package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
	_ "time/tzdata"
)

type projectionScopeValue struct {
	Column   string `json:"column"`
	Bucket   string `json:"bucket,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}
type projectionScopeRule struct {
	Source string                          `json:"source_table"`
	SQL    string                          `json:"sql,omitempty"`
	Params []string                        `json:"params,omitempty"`
	Values map[string]projectionScopeValue `json:"values"`
}
type projectionOptions struct {
	Params       []any                      `json:"params,omitempty"`
	ScopeSQL     string                     `json:"scope_sql,omitempty"`
	ScopeParams  []projectionScopeParameter `json:"scope_params,omitempty"`
	ScopeRules   []projectionScopeRule      `json:"scope_rules,omitempty"`
	Interval     int64                      `json:"min_refresh_interval_seconds"`
	MaxMs        int                        `json:"max_refresh_ms"`
	MaxRows      int                        `json:"max_result_rows"`
	MaxBytes     int64                      `json:"max_result_bytes"`
	PublishMs    int                        `json:"max_publication_ms"`
	BatchRows    int                        `json:"publication_batch_rows"`
	BatchBytes   int64                      `json:"publication_batch_bytes"`
	CoverageFrom string                     `json:"coverage_from,omitempty"`
	CoverageTo   string                     `json:"coverage_to,omitempty"`
}

func defaultProjectionOptions(ctx *sdk.AppCtx) projectionOptions {
	return projectionOptions{MaxMs: maxProjectionMs(ctx), MaxRows: maxProjectionTotalRows(ctx), MaxBytes: int64(maxProjectionBytes(ctx)), PublishMs: 500, BatchRows: 128, BatchBytes: 256 << 10}
}
func projectionParameterCount(s string) (int, error) {
	tokens, err := sqlTokens(s)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range tokens {
		if t.kind != "symbol" {
			continue
		}
		switch t.value {
		case ":", "$", "@":
			return 0, errf("use positional ? parameters")
		case "?":
			if t.end < len(s) && s[t.end] >= '0' && s[t.end] <= '9' {
				return 0, errf("numbered parameters are unsupported")
			}
			n++
		}
	}
	return n, nil
}
func projectionBoundValues(values []any) ([]any, error) {
	out := make([]any, len(values))
	for i, v := range values {
		switch n := v.(type) {
		case nil, string, bool, int, int64, float64:
			out[i] = v
		case json.Number:
			if x, err := n.Int64(); err == nil {
				out[i] = x
			} else {
				x, err := n.Float64()
				if err != nil {
					return nil, err
				}
				out[i] = x
			}
		default:
			return nil, errf("params[%d] must be scalar", i)
		}
		if err := finiteResult(out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func projectionDecode(raw string, out any) error {
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	return d.Decode(out)
}
func parseProjectionOptions(ctx *sdk.AppCtx, args map[string]any, sqlText string) (projectionOptions, error) {
	o := defaultProjectionOptions(ctx)
	// Copy only declared option fields; _request_context is deliberately excluded.
	picked := map[string]any{}
	for _, k := range []string{"params", "scope_sql", "scope_params", "scope_rules", "min_refresh_interval_seconds", "max_refresh_ms", "max_result_rows", "max_result_bytes", "max_publication_ms", "publication_batch_rows", "publication_batch_bytes", "coverage_from", "coverage_to"} {
		if v, ok := args[k]; ok {
			picked[k] = v
		}
	}
	b, err := json.Marshal(picked)
	if err != nil {
		return o, err
	}
	if err := projectionDecode(string(b), &o); err != nil {
		return o, errf("invalid projection options: %v", err)
	}
	if o.Interval < 0 || o.Interval > 86400 {
		return o, errf("min_refresh_interval_seconds must be 0..86400")
	}
	if o.MaxMs < 1 || o.MaxMs > maxProjectionMs(ctx) || o.MaxRows < 1 || o.MaxRows > maxProjectionTotalRows(ctx) || o.MaxBytes < 1 || o.MaxBytes > int64(maxProjectionBytes(ctx)) {
		return o, errf("projection resource limits exceed app limits")
	}
	if o.PublishMs < 1 || o.PublishMs > 5000 || o.BatchRows < 1 || o.BatchRows > 512 || o.BatchBytes < 1024 || o.BatchBytes > 1<<20 {
		return o, errf("publication limits exceed bounds")
	}
	if len(o.Params) > 1000 || len(o.ScopeRules) > 64 {
		return o, errf("projection options exceed bounds")
	}
	o.Params, err = projectionBoundValues(o.Params)
	if err != nil {
		return o, err
	}
	for _, query := range []struct {
		sql   string
		count int
	}{{sqlText, len(o.Params)}, {o.ScopeSQL, len(o.Params) + len(o.ScopeParams)}} {
		if query.sql == "" {
			if query.count != len(o.Params) {
				return o, errf("scope_params requires scope_sql")
			}
			continue
		}
		if len(query.sql) > 64<<10 {
			return o, errf("projection SQL exceeds 65536 bytes")
		}
		if err := validateReadOnlySQL(query.sql); err != nil {
			return o, err
		}
		n, err := projectionParameterCount(query.sql)
		if err != nil {
			return o, err
		}
		if n != query.count {
			return o, errf("SQL has %d parameters; expected %d", n, query.count)
		}
	}
	if (o.CoverageFrom == "") != (o.CoverageTo == "") {
		return o, errf("both coverage_from and coverage_to are required")
	}
	if o.CoverageFrom != "" {
		from, err := time.Parse(time.RFC3339, o.CoverageFrom)
		if err != nil {
			return o, err
		}
		to, err := time.Parse(time.RFC3339, o.CoverageTo)
		if err != nil || !to.After(from) {
			return o, errf("coverage_to must follow coverage_from")
		}
		o.CoverageFrom = from.UTC().Format(time.RFC3339Nano)
		o.CoverageTo = to.UTC().Format(time.RFC3339Nano)
	}
	return o, nil
}
func validateProjectionResultSchema(rows *sql.Rows, cols []Column) error {
	labels, err := rows.Columns()
	if err != nil {
		return err
	}
	if len(labels) != len(cols) {
		return errf("result schema declares %d columns; SQL returns %d", len(cols), len(labels))
	}
	seen := map[string]bool{}
	for _, label := range labels {
		if seen[label] {
			return errf("duplicate result column %q", label)
		}
		seen[label] = true
	}
	for _, col := range cols {
		if !seen[col.Name] {
			return errf("result missing %q", col.Name)
		}
	}
	return nil
}
func projectionMappedScope(row map[string]any, p *projectionDefinition, values map[string]projectionScopeValue) (string, error) {
	scope := map[string]any{}
	for _, name := range p.ScopeCols {
		spec, ok := values[name]
		if !ok {
			return "", errf("mapping missing scope column %q", name)
		}
		v, ok := row[spec.Column]
		if !ok {
			return "", errf("mapping input %q missing", spec.Column)
		}
		if spec.Bucket != "" && v != nil {
			timestamp, err := time.Parse(time.RFC3339Nano, fmt.Sprint(v))
			if err != nil {
				return "", errf("invalid scope timestamp for %q", name)
			}
			zone, err := time.LoadLocation(spec.Timezone)
			if err != nil {
				return "", err
			}
			local := timestamp.In(zone)
			switch spec.Bucket {
			case "day":
				v = local.Format("2006-01-02")
			case "month":
				v = local.Format("2006-01")
			default:
				return "", errf("unsupported scope bucket")
			}
		}
		scope[name] = v
	}
	return projectionScopeKey(p, scope)
}

// A scope parameter is either a column name or a timezone-aware day boundary.
// Values are always bound, never interpolated into SQL.
type projectionScopeParameter struct {
	Column   string `json:"scope_column"`
	Boundary string `json:"boundary,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

func (p *projectionScopeParameter) UnmarshalJSON(b []byte) error {
	var col string
	if json.Unmarshal(b, &col) == nil {
		p.Column = col
		return nil
	}
	type plain projectionScopeParameter
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = projectionScopeParameter(v)
	return nil
}
func (p projectionScopeParameter) MarshalJSON() ([]byte, error) {
	if p.Boundary == "" && p.Timezone == "" {
		return json.Marshal(p.Column)
	}
	type plain projectionScopeParameter
	return json.Marshal(plain(p))
}
func projectionScopeBindings(p *projectionDefinition, key string) ([]any, error) {
	var vals map[string]any
	if err := projectionDecode(key, &vals); err != nil {
		return nil, err
	}
	out := []any{}
	for _, param := range p.Options.ScopeParams {
		v, ok := vals[param.Column]
		if !ok {
			return nil, errf("missing scope parameter %q", param.Column)
		}
		if param.Boundary != "" {
			zone, err := time.LoadLocation(param.Timezone)
			if err != nil {
				return nil, err
			}
			day, err := time.ParseInLocation("2006-01-02", fmt.Sprint(v), zone)
			if err != nil {
				return nil, err
			}
			if param.Boundary == "end" {
				day = day.AddDate(0, 0, 1)
			}
			v = day.UTC().Format(timestampLayout)
		}
		out = append(out, v)
	}
	out = append(out, p.Options.Params...)
	return projectionBoundValues(out)
}
func projectionScopeKey(p *projectionDefinition, values map[string]any) (string, error) {
	normalized := map[string]any{}
	for _, col := range p.ResultCols {
		if _, ok := values[col.Name]; !ok {
			continue
		}
		for _, s := range p.ScopeCols {
			if s != col.Name {
				continue
			}
			v := values[s]
			if col.Type == "bool" {
				switch x := v.(type) {
				case int64:
					if x == 0 || x == 1 {
						v = x == 1
					}
				case json.Number:
					if x == "0" || x == "1" {
						v = x == "1"
					}
				}
			}
			n, err := coerceForStorage(col, v)
			if err != nil {
				return "", err
			}
			normalized[s] = n
		}
	}
	return makeScopeKey(normalized, p.ScopeCols)
}
