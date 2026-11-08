package engine

import native "github.com/apteva/db"

// scanFields promises no result order. Request the selected index's complete
// physical order so the native engine yields documents directly, even when an
// IN predicate otherwise selects multiple ranges. The consumer owns the final
// ordering and bounded page. No native sorting heap or candidate-key set is
// needed. Without an indexed lookup, $natural explicitly requests streaming.
// Ordinary find/nativeQueryOptions retains its existing ordering semantics.
func nativeScanOptions(c Collection, q Query) native.QueryOptions {
	options := native.QueryOptions{Hint: "$natural", MaxMemoryBytes: MaxQueryWorkBytes}
	p := plan(c, q)
	if p.IndexedLookup || q.hint != "" {
		for _, index := range indexes(c) {
			if index.Name == p.Index {
				options.Hint = index.Name
				for _, field := range index.Fields {
					options.Sort = append(options.Sort, native.IndexField{Field: field.Field, Desc: field.Direction == "desc"})
				}
				break
			}
		}
	}
	return options
}

// Expand only the leading IN bindings of the bounded multi-range plan. Each
// emitted filter retains the entire original predicate, adding a disjoint
// equality prefix. One reused binding slice suffices; no documents or primary
// key candidate sets are retained between scans. Pending rows are visited once
// by scanFields after all committed ranges, using the original predicate.
func eachNativeScanFilter(c Collection, q Query, emit func(*Filter) bool) {
	p := plan(c, q)
	if p.ranges == nil {
		emit(q.Where)
		return
	}
	type binding struct {
		field  string
		values []any
	}
	var bindings []binding
	terms := conjuncts(q.Where)
	for _, index := range indexes(c) {
		if index.Name != p.Index {
			continue
		}
		for _, order := range index.Fields {
			var equal, listed *Filter
			for i := range terms {
				term := &terms[i]
				if term.Field != order.Field {
					continue
				}
				if term.Op == "eq" || term.Op == "is_null" {
					equal = term
				} else if term.Op == "in" {
					listed = term
				}
			}
			if equal != nil {
				continue
			}
			if listed == nil {
				break
			}
			field, _ := c.field(order.Field)
			b := binding{field: order.Field}
			seen := map[string]bool{}
			for _, value := range listed.Value.([]any) {
				key := string(scalar(field, value, false))
				if !seen[key] {
					seen[key] = true
					b.values = append(b.values, value)
				}
			}
			if len(b.values) == 0 {
				return
			}
			bindings = append(bindings, b)
		}
		break
	}
	prefix := make([]Filter, len(bindings)+1)
	prefix[0] = *q.Where
	filter := Filter{And: prefix}
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth == len(bindings) {
			return emit(&filter)
		}
		b := bindings[depth]
		for _, value := range b.values {
			prefix[depth+1] = Filter{Field: b.field, Op: "eq", Value: value}
			if !walk(depth + 1) {
				return false
			}
		}
		return true
	}
	walk(0)
}
