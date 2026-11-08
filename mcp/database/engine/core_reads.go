package engine

import "math"

// ReadPipeline is the optional named input plan shared by find and aggregate.
// Its output is consumed by the ordinary top-level paging/grouping arguments.
type ReadPipeline struct {
	Version int         `json:"version"`
	Nodes   []QueryNode `json:"nodes"`
	From    string      `json:"from"`
}

func hasRelationalInput(r Request) bool {
	return r.As != "" || len(r.Joins) > 0 || len(r.Computed) > 0 || r.Filter != nil || len(r.Distinct) > 0 || r.Pick != nil || r.Group != nil || len(r.Branches) > 0 || r.Rollup != nil
}

// planCoreRead chooses existing optimized readers without changing the public
// operation. Legacy operation names use those same readers directly.
func planCoreRead(op string, r Request) (string, error) {
	if r.Version != 0 || r.Nodes != nil || r.Result != nil {
		return "", Invalid("use pipeline:{version,nodes,from} with %s", op)
	}
	if r.Pipeline != nil {
		if r.Collection != "" || hasRelationalInput(r) || r.Relation != nil || r.Match != nil {
			return "", Invalid("pipeline cannot be mixed with a collection, joins, computed, branches, rollup or relation; put input stages in pipeline")
		}
		if op == "find" && len(r.Lookups) > 0 {
			return "", Invalid("put lookups in pipeline join nodes")
		}
		if r.RequireIndex {
			return "", Invalid("requireIndex belongs on stored-collection reads")
		}
	} else {
		if r.Limits != nil {
			return "", Invalid("limits requires pipeline")
		}
		if r.Collection == "" && !(op == "aggregate" && len(r.Branches) > 0) {
			return "", Invalid("%s requires collection or pipeline", op)
		}
	}
	if op == "aggregate" {
		if len(r.Metrics) == 0 {
			return "", Invalid("aggregate requires metrics")
		}
		if r.Relation != nil {
			if r.Cursor != "" || len(r.Select) > 0 || r.Group != nil || r.Offset != 0 || r.Count || len(r.Joins) > 0 || r.Filter != nil || len(r.Distinct) > 0 || r.Pick != nil || len(r.Branches) > 0 || r.Rollup != nil || len(r.Lookups) > 0 || len(r.Buckets) > 0 || r.RequireIndex {
				return "", Invalid("related aggregate accepts parent computed values, groupBy and metrics; put related-row transformations in relation.sources")
			}
			return opJoinedAggregate, nil
		}
		if r.Match != nil || r.Group != nil || r.Offset != 0 || r.Count {
			return "", Invalid("relation, match, group, offset and count belong to find")
		}
		if r.Cursor != "" || len(r.Select) > 0 {
			return "", Invalid("aggregate does not accept cursor or select")
		}
		if hasRelationalInput(r) {
			if r.RequireIndex {
				return "", Invalid("requireIndex is supported on ordinary stored-collection aggregates")
			}
			return opJoinedAggregate, nil
		}
		return op, nil
	}
	if len(r.Metrics) > 0 || len(r.GroupBy) > 0 || len(r.Derived) > 0 || len(r.Branches) > 0 || r.Rollup != nil || len(r.Buckets) > 0 {
		return "", Invalid("metrics, groupBy, derived, branches, rollup and buckets belong to aggregate")
	}
	if r.Offset < 0 || r.Offset > maxRelOffset || r.Offset > 0 && r.Cursor != "" {
		return "", Invalid("find accepts offset 0–100000 or cursor, not both")
	}
	if r.Relation != nil {
		if r.RequireIndex {
			return "", Invalid("requireIndex is supported on ordinary stored-collection finds")
		}
		if len(r.Joins) > 0 || len(r.Computed) > 0 || r.Filter != nil || len(r.Distinct) > 0 || r.Pick != nil || r.Group != nil || len(r.Lookups) > 0 {
			return "", Invalid("put related-row joins/computed/filter/distinct in relation.sources")
		}
		return opRelatedRead, nil
	}
	if r.Match != nil {
		return "", Invalid("match requires relation")
	}
	if hasRelationalInput(r) {
		if len(r.Lookups) > 0 || r.RequireIndex {
			return "", Invalid("lookups/requireIndex cannot be combined with relational find; use explicit joins")
		}
		return opJoinedPage, nil
	}
	return op, nil
}
func finishAggregate(rows []Record, limit int, derived []RelDerived, reserve ...func(int) error) (any, error) {
	retained := 0
	charge := func(n int) error {
		retained += n
		if retained > relationalRetained {
			return fail("resource_limit", "aggregate result memory budget exceeded")
		}
		if len(reserve) > 0 {
			return reserve[0](n)
		}
		return nil
	}
	if e := charge(queryRowsBytes(rows)); e != nil {
		return nil, e
	}
	for _, row := range rows {
		for _, d := range derived {
			value := relDivide(row[d.Numerator], row[d.Denominator], d.Round)
			if number, ok := value.(float64); ok && (math.IsInf(number, 0) || math.IsNaN(number)) {
				return nil, fail("resource_limit", "derived %s numeric overflow", d.Name)
			}
			if e := charge(96 + len(d.Name) + retainedBytes(value)); e != nil {
				return nil, e
			}
			row[d.Name] = value
		}
	}
	if len(marshal(rows)) > MaxBytes {
		return nil, fail("resource_limit", "aggregate response exceeds 4 MiB")
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	return map[string]any{"rows": rows, "truncated": more}, nil
}

// ValidateCoreRead checks public input forms without consulting data or schemas.
func ValidateCoreRead(op string, r Request) error { _, e := planCoreRead(op, r); return e }
