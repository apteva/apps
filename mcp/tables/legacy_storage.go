package main

import (
	"database/sql/driver"
	"modernc.org/sqlite"
	"time"
)

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("tables_datetime", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		v, err := normalizeTimestamp(args[0])
		if err != nil {
			return args[0], nil
		}
		return v, nil
	})
}

// Legacy values stay byte-for-byte compatible with 0.1.14. Normalize only at
// the new API boundary. Historical invalid dates remain readable for repair.
func resultTimestamp(v any) any {
	normalized, err := normalizeTimestamp(v)
	if err != nil {
		return scalarString(v)
	}
	return normalized
}
func coerceForTableStorage(t *Table, col Column, v any) (any, error) {
	value, err := coerceForStorage(col, v)
	if err == nil && value != nil && t.LegacyStorage && col.Type == "datetime" {
		parsed, e := time.Parse(timestampLayout, value.(string))
		if e != nil {
			return nil, e
		}
		return parsed.UTC().Format(time.RFC3339Nano), nil
	}
	return value, err
}
func storageExpression(t *Table, name string) string {
	expression := quote(name)
	if !t.LegacyStorage {
		return expression
	}
	isDate := name == "created_at" || name == "updated_at"
	for _, c := range t.Columns {
		if c.Name == name && c.Type == "datetime" {
			isDate = true
		}
	}
	if isDate {
		return "tables_datetime(" + expression + ")"
	}
	return expression
}
func updateTimestampSQL(t *Table) string {
	if t.LegacyStorage {
		return `"updated_at" = CURRENT_TIMESTAMP, "_revision" = "_revision" + 1`
	}
	return `"updated_at" = (strftime('%Y-%m-%dT%H:%M:%f','now') || '000000Z'), "_revision" = "_revision" + 1`
}
