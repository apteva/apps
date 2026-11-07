package main

// The runtime additionally checks operation-specific fields, dependencies and
// types against the snapshot's collection schemas.
func queryInputProperties() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	obj := func() map[string]any { return map[string]any{"type": "object"} }
	arr := func(items any) map[string]any { return map[string]any{"type": "array", "items": items} }
	enum := func(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	integer := func(lo, hi int) map[string]any {
		return map[string]any{"type": "integer", "minimum": lo, "maximum": hi}
	}
	order := map[string]any{"type": "object", "properties": map[string]any{"field": str(), "direction": enum("asc", "desc")}, "required": []string{"field"}, "additionalProperties": false}
	computed := map[string]any{"type": "object", "properties": map[string]any{"name": str(), "type": enum("text", "integer", "number", "boolean", "datetime"), "expr": obj()}, "required": []string{"name", "expr"}, "additionalProperties": false}
	metric := map[string]any{"type": "object", "properties": map[string]any{"name": str(), "op": enum("count", "count_distinct", "sum", "avg", "min", "max"), "field": str(), "where": obj()}, "required": []string{"name", "op"}, "additionalProperties": false}
	pair := map[string]any{"type": "object", "properties": map[string]any{"local": str(), "foreign": str(), "comparison": enum("text", "numeric")}, "required": []string{"local", "foreign"}, "additionalProperties": false}
	join := map[string]any{"type": "object", "properties": map[string]any{"relation": str(), "as": str(), "kind": enum("inner", "left", "semi", "anti"), "on": map[string]any{"type": "array", "items": pair, "minItems": 1, "maxItems": 8}, "cardinality": enum("many_to_one"), "where": obj()}, "required": []string{"relation", "as", "on"}, "additionalProperties": false}
	node := map[string]any{"type": "object", "properties": map[string]any{"name": str(), "op": enum("scan", "compute", "join", "aggregate"), "from": str(), "collection": str(), "where": obj(), "select": arr(str()), "computed": map[string]any{"type": "array", "items": computed, "maxItems": 32}, "predicate": obj(), "groupBy": arr(str()), "metrics": arr(metric), "as": str(), "joins": map[string]any{"type": "array", "items": join, "maxItems": 8}, "maxRows": integer(0, 100000)}, "required": []string{"name", "op"}, "additionalProperties": false}
	return map[string]any{
		"nodes":  map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "items": node},
		"result": map[string]any{"type": "object", "properties": map[string]any{"from": str(), "orderBy": arr(order), "limit": integer(0, 1000)}, "required": []string{"from"}, "additionalProperties": false},
		"limits": map[string]any{"type": "object", "properties": map[string]any{"intermediateRows": integer(0, 100000), "memoryBytes": integer(0, 128<<20), "work": integer(0, 16<<20)}, "additionalProperties": false},
	}
}
