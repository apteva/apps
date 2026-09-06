package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type rowCursor struct {
	Project string `json:"p"`
	Table   int64  `json:"t"`
	Shape   string `json:"s"`
	Order   string `json:"o"`
	Filter  string `json:"f"`
	ID      int64  `json:"id"`
	Value   any    `json:"v"`
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:16])
}
func orderColumn(args map[string]any) (string, string) {
	parts := strings.Fields(strArg(args, "order_by"))
	if len(parts) == 0 {
		return "id", "desc"
	}
	dir := "asc"
	if len(parts) > 1 {
		dir = strings.ToLower(parts[1])
	}
	return parts[0], dir
}
func makeCursor(pid string, t *Table, args map[string]any, row map[string]any) string {
	col, dir := orderColumn(args)
	id, _ := exactInteger(row["id"])
	c := rowCursor{pid, t.ID, digest(t.Columns), col + " " + dir, digest(args["where"]), id, row[col]}
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func cursorWhere(pid string, t *Table, args map[string]any) (string, []any, error) {
	raw, ok := args["cursor"]
	if !ok || raw == nil {
		return "", nil, nil
	}
	s, ok := raw.(string)
	if !ok || len(s) > 16384 {
		return "", nil, errf("invalid cursor")
	}
	if s == "" {
		return "", nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", nil, errf("invalid cursor")
	}
	var c rowCursor
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err = dec.Decode(&c); err != nil {
		return "", nil, errf("invalid cursor")
	}
	col, dir := orderColumn(args)
	if c.Project != pid || c.Table != t.ID || c.Shape != digest(t.Columns) || c.Order != col+" "+dir || c.Filter != digest(args["where"]) || c.ID <= 0 {
		return "", nil, errf("cursor does not match project, table, schema, filter or ordering")
	}
	if intArg(args, "offset", 0) != 0 {
		return "", nil, errf("cursor and offset cannot be combined")
	}
	op := ">"
	if dir == "desc" {
		op = "<"
	}
	if col == "id" {
		return `"id" ` + op + " ?", []any{c.ID}, nil
	}
	q := storageExpression(t, col)
	if c.Value == nil {
		if dir == "asc" {
			return "(" + q + " IS NOT NULL OR (" + q + ` IS NULL AND "id" > ?))`, []any{c.ID}, nil
		}
		return "(" + q + ` IS NULL AND "id" < ?)`, []any{c.ID}, nil
	}
	column, err := aggregateColumn(t, col)
	if err != nil {
		return "", nil, err
	}
	value, err := coerceForStorage(column, c.Value)
	if err != nil {
		return "", nil, errf("invalid cursor value: %v", err)
	}
	clause := "(" + q + " " + op + " ? OR (" + q + ` = ? AND "id" ` + op + " ?)"
	if dir == "desc" {
		clause += " OR " + q + " IS NULL"
	}
	clause += ")"
	return clause, []any{value, value, c.ID}, nil
}
