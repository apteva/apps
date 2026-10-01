package engine

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

func validateFilter(c Collection, f *Filter, depth int, terms *int) error {
	if f == nil {
		return nil
	}
	*terms++
	if depth > 8 || *terms > 100 {
		return Invalid("filter is too complex")
	}
	branches := 0
	if f.And != nil {
		branches++
	}
	if f.Or != nil {
		branches++
	}
	if f.Field != "" {
		branches++
	}
	if branches != 1 {
		return Invalid("filter requires exactly one of field, and, or")
	}
	if f.And != nil || f.Or != nil {
		if f.Op != "" || f.Value != nil {
			return Invalid("logical filter cannot have op/value")
		}
		children := f.And
		if children == nil {
			children = f.Or
		}
		if len(children) == 0 {
			return Invalid("empty logical filter")
		}
		for i := range children {
			if e := validateFilter(c, &children[i], depth+1, terms); e != nil {
				return e
			}
		}
		return nil
	}
	fld, e := c.field(f.Field)
	if e != nil {
		return e
	}
	if fld.Type == "json" {
		return Invalid("JSON predicates are not supported")
	}
	if f.Op == "is_null" || f.Op == "is_not_null" {
		if f.Value != nil {
			return Invalid("null predicates do not accept value")
		}
		return nil
	}
	if f.Value == nil {
		return Invalid("use is_null to test null")
	}
	fld.Nullable = false
	if f.Op == "in" {
		a, ok := f.Value.([]any)
		if !ok || len(a) == 0 || len(a) > 100 {
			return Invalid("in requires 1–100 values")
		}
		for i, v := range a {
			n, e := normalize(fld, v)
			if e != nil {
				return e
			}
			a[i] = n
		}
		return nil
	}
	switch f.Op {
	case "eq", "ne", "gt", "gte", "lt", "lte":
	case "starts_with":
		if fld.Type != "text" {
			return Invalid("starts_with requires text")
		}
	default:
		return Invalid("unknown filter operator %s", f.Op)
	}
	f.Value, e = normalize(fld, f.Value)
	return e
}
func validateQuery(c Collection, q *Query) error {
	if len(q.OrderBy) > 8 {
		return Invalid("at most 8 explicit ordering fields")
	}
	n := 0
	if e := validateFilter(c, q.Where, 0, &n); e != nil {
		return e
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 1000 {
		return Invalid("limit must be 1–1000")
	}
	seen := map[string]bool{}
	for _, s := range q.Select {
		if _, e := c.field(s); e != nil {
			return e
		}
		if seen[s] {
			return Invalid("duplicate selected field")
		}
		seen[s] = true
	}
	seen = map[string]bool{}
	for i, o := range q.OrderBy {
		f, e := c.field(o.Field)
		if e != nil {
			return e
		}
		if f.Type == "json" || seen[o.Field] {
			return Invalid("order fields must be distinct scalars")
		}
		seen[o.Field] = true
		if o.Direction == "" {
			q.OrderBy[i].Direction = "asc"
		} else if o.Direction != "asc" && o.Direction != "desc" {
			return Invalid("invalid order direction")
		}
	}
	for _, p := range c.PrimaryKey {
		if !seen[p] {
			q.OrderBy = append(q.OrderBy, Order{p, "asc"})
		}
	}
	return nil
}
func compare(f Field, a, b any) int {
	if a == nil {
		if b == nil {
			return 0
		}
		return -1
	}
	if b == nil {
		return 1
	}
	switch f.Type {
	case "integer":
		x, _ := strconv.ParseInt(a.(string), 10, 64)
		y, _ := strconv.ParseInt(b.(string), 10, 64)
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	case "number":
		x := number(a)
		y := number(b)
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	case "boolean":
		x := a.(bool)
		y := b.(bool)
		if !x && y {
			return -1
		}
		if x && !y {
			return 1
		}
	default:
		return strings.Compare(a.(string), b.(string))
	}
	return 0
}
func number(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		n, _ := x.Float64()
		return n
	}
	return 0
}
func matches(c Collection, r Record, f *Filter) bool {
	if f == nil {
		return true
	}
	if f.And != nil {
		for i := range f.And {
			if !matches(c, r, &f.And[i]) {
				return false
			}
		}
		return true
	}
	if f.Or != nil {
		for i := range f.Or {
			if matches(c, r, &f.Or[i]) {
				return true
			}
		}
		return false
	}
	v := r[f.Field]
	if f.Op == "is_null" {
		return v == nil
	}
	if f.Op == "is_not_null" {
		return v != nil
	}
	if v == nil {
		return false
	}
	fld, _ := c.field(f.Field)
	if f.Op == "in" {
		for _, w := range f.Value.([]any) {
			if compare(fld, v, w) == 0 {
				return true
			}
		}
		return false
	}
	if f.Op == "starts_with" {
		return strings.HasPrefix(v.(string), f.Value.(string))
	}
	d := compare(fld, v, f.Value)
	switch f.Op {
	case "eq":
		return d == 0
	case "ne":
		return d != 0
	case "gt":
		return d > 0
	case "gte":
		return d >= 0
	case "lt":
		return d < 0
	case "lte":
		return d <= 0
	}
	return false
}
func less(c Collection, a, b Record, order []Order) bool {
	for _, o := range order {
		f, _ := c.field(o.Field)
		d := compare(f, a[o.Field], b[o.Field])
		if d != 0 {
			if o.Direction == "desc" {
				return d > 0
			}
			return d < 0
		}
	}
	return false
}
func sorted(c Collection, rows []Record, order []Order) {
	sort.Slice(rows, func(i, j int) bool { return less(c, rows[i], rows[j], order) })
}

type cursorData struct {
	Hash    string `json:"hash"`
	Version int64  `json:"version"`
	Expires int64  `json:"expires"`
	Values  Record `json:"values"`
}

func queryHash(c Collection, q Query) string {
	q.Cursor = ""
	digest := sha256.Sum256(marshal(struct {
		C string
		Q Query
	}{c.Name, q}))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
func signCursor(key []byte, p cursorData) string {
	b := marshal(p)
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func afterCursor(c Collection, q *Query, key []byte) (*Filter, error) {
	parts := strings.Split(q.Cursor, ".")
	if len(parts) != 2 || len(q.Cursor) > 32768 {
		return nil, Invalid("invalid cursor")
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return nil, Invalid("invalid cursor")
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	if e != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, Invalid("invalid cursor signature")
	}
	var p cursorData
	if decode(b, &p) != nil || p.Version != c.Version || p.Hash != queryHash(c, *q) || p.Expires < time.Now().Unix() {
		return nil, Invalid("expired or incompatible cursor")
	}
	q.after = p.Values
	branches := []Filter{}
	prefix := []Filter{}
	for _, o := range q.OrderBy {
		v := p.Values[o.Field]
		eq := Filter{Field: o.Field, Op: "eq", Value: v}
		var next *Filter
		if v == nil {
			eq.Op = "is_null"
			if o.Direction != "desc" {
				next = &Filter{Field: o.Field, Op: "is_not_null"}
			}
		} else {
			op := "gt"
			if o.Direction == "desc" {
				op = "lt"
			}
			f := Filter{Field: o.Field, Op: op, Value: v}
			if o.Direction == "desc" {
				f = Filter{Or: []Filter{f, {Field: o.Field, Op: "is_null"}}}
			}
			next = &f
		}
		if next != nil {
			terms := append(append([]Filter{}, prefix...), *next)
			branches = append(branches, Filter{And: terms})
		}
		prefix = append(prefix, eq)
	}
	if len(branches) == 0 {
		return &Filter{Field: c.PrimaryKey[0], Op: "is_null"}, nil
	}
	return &Filter{Or: branches}, nil
}
func and(a, b *Filter) *Filter {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &Filter{And: []Filter{*a, *b}}
}
func page(c Collection, q Query, rows []Record, key []byte) (any, error) {
	more := len(rows) > q.Limit
	if more {
		rows = rows[:q.Limit]
	}
	out := []Record{}
	size := 0
	last := Record{}
	for _, r := range rows {
		p := r
		if len(q.Select) > 0 {
			p = Record{}
			for _, f := range q.Select {
				p[f] = r[f]
			}
		}
		n := len(marshal(p))
		if size+n > MaxBytes {
			more = true
			break
		}
		size += n
		out = append(out, p)
		last = r
	}
	cur := ""
	if more && len(out) > 0 {
		values := Record{}
		for _, o := range q.OrderBy {
			values[o.Field] = last[o.Field]
		}
		cur = signCursor(key, cursorData{queryHash(c, q), c.Version, time.Now().Add(time.Hour).Unix(), values})
		if len(cur) > 32768 {
			return nil, fail("resource_limit", "ordering values are too large for a continuation cursor")
		}
	}
	return map[string]any{"records": out, "hasMore": more, "nextCursor": cur}, nil
}
func validateAggregate(c Collection, r *Request) (Collection, error) {
	if r.Cursor != "" || len(r.Select) > 0 {
		return c, Invalid("aggregate does not accept cursor or select")
	}
	n := 0
	if e := validateFilter(c, r.Where, 0, &n); e != nil {
		return c, e
	}
	if len(r.GroupBy) > 8 || len(r.Metrics) == 0 || len(r.Metrics) > 16 {
		return c, Invalid("aggregate requires 1–16 metrics and at most 8 grouping fields")
	}
	out := Collection{Fields: []Field{}}
	seen := map[string]bool{}
	for _, g := range r.GroupBy {
		f, e := c.field(g)
		if e != nil {
			return c, e
		}
		if f.Type == "json" || seen[g] {
			return c, Invalid("grouping fields must be distinct scalars")
		}
		seen[g] = true
		out.Fields = append(out.Fields, f)
	}
	for _, m := range r.Metrics {
		if e := nameOK(m.Name); e != nil {
			return c, e
		}
		if seen[m.Name] {
			return c, Invalid("duplicate aggregate output %s", m.Name)
		}
		seen[m.Name] = true
		f := Field{Name: m.Name, Type: "integer"}
		if m.Op == "count" {
			if m.Field != "" {
				if _, e := c.field(m.Field); e != nil {
					return c, e
				}
			}
		} else {
			src, e := c.field(m.Field)
			if e != nil {
				return c, e
			}
			f.Type = src.Type
			f.Nullable = true
			switch m.Op {
			case "sum", "avg":
				if src.Type != "number" && src.Type != "integer" {
					return c, Invalid("sum and avg require numeric fields")
				}
				if m.Op == "avg" {
					f.Type = "number"
				}
			case "min", "max":
				if src.Type == "json" {
					return c, Invalid("min and max require scalar fields")
				}
			default:
				return c, Invalid("unknown metric %s", m.Op)
			}
		}
		out.Fields = append(out.Fields, f)
	}
	out.PrimaryKey = r.GroupBy
	q := Query{OrderBy: r.OrderBy, Limit: r.Limit}
	if e := validateQuery(out, &q); e != nil {
		return c, e
	}
	r.OrderBy = q.OrderBy
	r.Limit = q.Limit
	return out, nil
}

// equalJSON compares normalized definitions, never user-controlled SQL.
func equalJSON(a, b any) bool { return bytes.Equal(marshal(a), marshal(b)) }
