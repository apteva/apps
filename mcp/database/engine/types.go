// Package engine implements the local, adapter-independent database contract.
package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"
)

type Record map[string]any
type Field struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable,omitempty"`
}
type Order struct {
	Field     string `json:"field"`
	Direction string `json:"direction,omitempty"`
}
type Index struct {
	Name   string  `json:"name"`
	Fields []Order `json:"fields"`
	Unique bool    `json:"unique,omitempty"`
}
type Collection struct {
	Name       string   `json:"name"`
	Fields     []Field  `json:"fields"`
	PrimaryKey []string `json:"primaryKey"`
	Indexes    []Index  `json:"indexes"`
	Version    int64    `json:"version"`
	// StorageVersion is internal SQLite physical-layout metadata. It is not
	// part of the portable collection schema or cursor hash.
	StorageVersion int `json:"-"`
}
type Filter struct {
	And   []Filter `json:"and,omitempty"`
	Or    []Filter `json:"or,omitempty"`
	Field string   `json:"field,omitempty"`
	Op    string   `json:"op,omitempty"`
	Value any      `json:"value,omitempty"`
}
type Metric struct {
	Name  string `json:"name"`
	Op    string `json:"op"`
	Field string `json:"field,omitempty"`
}
type Query struct {
	after        Record   // authenticated cursor values, never accepted from JSON
	Where        *Filter  `json:"where,omitempty"`
	Select       []string `json:"select,omitempty"`
	OrderBy      []Order  `json:"orderBy,omitempty"`
	Limit        int      `json:"limit,omitempty"`
	Cursor       string   `json:"cursor,omitempty"`
	RequireIndex bool     `json:"requireIndex,omitempty"`
}
type Request struct {
	TimeoutMS     int      `json:"timeoutMs,omitempty"`
	Database      string   `json:"database,omitempty"`
	Collection    string   `json:"collection,omitempty"`
	Name          string   `json:"name,omitempty"`
	Adapter       string   `json:"adapter,omitempty"`
	Fields        []Field  `json:"fields,omitempty"`
	PrimaryKey    []string `json:"primaryKey,omitempty"`
	Index         *Index   `json:"index,omitempty"`
	Key           Record   `json:"key,omitempty"`
	Records       []Record `json:"records,omitempty"`
	Set           Record   `json:"set,omitempty"`
	Increment     Record   `json:"increment,omitempty"`
	ConflictIndex string   `json:"conflictIndex,omitempty"`
	IfVersion     int64    `json:"ifVersion,omitempty"`
	MaxAffected   int      `json:"maxAffected,omitempty"`
	All           bool     `json:"all,omitempty"`
	Confirm       bool     `json:"confirm,omitempty"`
	Query
	GroupBy    []string    `json:"groupBy,omitempty"`
	Metrics    []Metric    `json:"metrics,omitempty"`
	Operations []Operation `json:"operations,omitempty"`
}
type Operation struct {
	Op      string  `json:"op"`
	Request Request `json:"args"`
}
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string                    { return e.Code + ": " + e.Message }
func fail(code, format string, args ...any) error { return &Error{code, fmt.Sprintf(format, args...)} }
func Invalid(format string, args ...any) error    { return fail("invalid_argument", format, args...) }

const MaxRecords = 1000
const MaxIndexBuildRecords = 10000
const MaxQueryWorkBytes = 16 << 20
const MaxBytes = 4 << 20

var ident = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func nameOK(s string) error {
	if !ident.MatchString(s) {
		return Invalid("invalid name %q; use lowercase letters, digits and underscores", s)
	}
	return nil
}
func decode(b []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(dst)
}
func marshal(v any) []byte { b, _ := json.Marshal(v); return b }
func (c Collection) field(n string) (Field, error) {
	for _, f := range c.Fields {
		if f.Name == n {
			return f, nil
		}
	}
	switch n {
	case "_created_at", "_updated_at":
		return Field{Name: n, Type: "datetime"}, nil
	case "_version":
		return Field{Name: n, Type: "integer"}, nil
	}
	return Field{}, Invalid("unknown field %q", n)
}
func (c Collection) isPK(n string) bool {
	for _, p := range c.PrimaryKey {
		if p == n {
			return true
		}
	}
	return false
}
func normalize(f Field, v any) (any, error) {
	if v == nil {
		if f.Nullable {
			return nil, nil
		}
		return nil, Invalid("%s cannot be null", f.Name)
	}
	switch f.Type {
	case "text":
		if s, ok := v.(string); ok && utf8.ValidString(s) {
			return s, nil
		}
	case "integer":
		s, ok := v.(string)
		if !ok {
			return nil, Invalid("%s must be a decimal string (int64)", f.Name)
		}
		n, e := strconv.ParseInt(s, 10, 64)
		if e == nil {
			return strconv.FormatInt(n, 10), nil
		}
	case "number":
		var n float64
		var err error
		switch x := v.(type) {
		case json.Number:
			n, err = x.Float64()
		case float64:
			n = x
		case int:
			n = float64(x)
		default:
			return nil, Invalid("%s must be a number", f.Name)
		}
		if err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) {
			if n == 0 {
				n = 0
			}
			return n, nil
		}
	case "boolean":
		if b, ok := v.(bool); ok {
			return b, nil
		}
	case "datetime":
		if s, ok := v.(string); ok {
			t, e := time.Parse(time.RFC3339Nano, s)
			if e == nil && t.Year() >= 1 && t.Year() <= 9999 && t.Nanosecond()%1000 == 0 {
				return t.UTC().Format("2006-01-02T15:04:05.000000Z"), nil
			}
		}
	case "json":
		b, e := json.Marshal(v)
		if e == nil {
			var out any
			if decode(b, &out) == nil {
				return out, nil
			}
		}
	}
	return nil, Invalid("invalid %s value for %s", f.Type, f.Name)
}
func newCollection(r Request) (Collection, error) {
	c := Collection{Name: r.Collection, Fields: r.Fields, PrimaryKey: r.PrimaryKey, Indexes: []Index{}, Version: 1}
	if err := nameOK(c.Name); err != nil {
		return c, err
	}
	if len(c.Fields) > 128 {
		return c, Invalid("at most 128 fields")
	}
	seen := map[string]bool{}
	for _, f := range c.Fields {
		if err := nameOK(f.Name); err != nil {
			return c, err
		}
		if seen[f.Name] {
			return c, Invalid("duplicate field %s", f.Name)
		}
		seen[f.Name] = true
		switch f.Type {
		case "text", "integer", "number", "boolean", "datetime", "json":
		default:
			return c, Invalid("unknown field type %s", f.Type)
		}
	}
	if len(c.PrimaryKey) == 0 {
		c.PrimaryKey = []string{"id"}
		if !seen["id"] {
			c.Fields = append([]Field{{Name: "id", Type: "text"}}, c.Fields...)
		}
	}
	if len(c.PrimaryKey) > 8 {
		return c, Invalid("at most 8 primary key fields")
	}
	if len(c.Fields) > 128 {
		return c, Invalid("at most 128 fields including the default id")
	}
	seen = map[string]bool{}
	for _, p := range c.PrimaryKey {
		f, e := c.field(p)
		if e != nil {
			return c, e
		}
		if seen[p] || f.Nullable || f.Type == "json" || p[0] == '_' {
			return c, Invalid("primary key fields must be distinct, declared, non-null scalars")
		}
		seen[p] = true
	}
	return c, nil
}
func validateIndex(c Collection, idx *Index) error {
	if idx == nil {
		return Invalid("index is required")
	}
	if e := nameOK(idx.Name); e != nil {
		return e
	}
	if idx.Name == "primary" {
		return Invalid("primary is reserved")
	}
	if len(idx.Fields) == 0 || len(idx.Fields) > 8 {
		return Invalid("indexes require 1–8 fields")
	}
	seen := map[string]bool{}
	for i, o := range idx.Fields {
		f, e := c.field(o.Field)
		if e != nil {
			return e
		}
		if f.Type == "json" || seen[o.Field] {
			return Invalid("index fields must be distinct scalars")
		}
		seen[o.Field] = true
		if o.Direction == "" {
			idx.Fields[i].Direction = "asc"
		} else if o.Direction != "asc" && o.Direction != "desc" {
			return Invalid("direction must be asc or desc")
		}
	}
	return nil
}
func normalizeRecord(c Collection, r Record) (Record, error) {
	out := Record{}
	for k := range r {
		if _, e := c.field(k); e != nil {
			return nil, e
		}
		if k[0] == '_' {
			return nil, Invalid("metadata is read-only")
		}
	}
	for _, f := range c.Fields {
		v, e := normalize(f, r[f.Name])
		if e != nil {
			return nil, e
		}
		out[f.Name] = v
	}
	if len(marshal(out)) > 1<<20 {
		return nil, fail("resource_limit", "record exceeds 1 MiB")
	}
	return out, nil
}
func primary(c Collection, r Record) (string, error) {
	vals := []any{}
	for _, p := range c.PrimaryKey {
		f, _ := c.field(p)
		v, e := normalize(f, r[p])
		if e != nil {
			return "", e
		}
		vals = append(vals, v)
	}
	b := marshal(vals)
	if len(b) > 2048 {
		return "", Invalid("primary key exceeds 2048 bytes")
	}
	return string(b), nil
}
func keyOf(c Collection, r Record) Record {
	k := Record{}
	for _, p := range c.PrimaryKey {
		k[p] = r[p]
	}
	return k
}
