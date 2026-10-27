package engine

import (
	"bytes"
	"encoding/json"
)

// The complete primary key identifies exactly one input row. For COUNT(*)-only
// groups every metric is therefore constant 1; ordering reduces to group keys.
// Keep this rewrite narrow: nullable field counts and other reducers are not
// constant, and grouping by only part of a composite primary key is not unique.
func (t *aptevaTx) aggregatePrimaryCount(c Collection, r Request) ([]Record, bool, error) {
	if len(r.GroupBy) == 0 || len(r.GroupBy) != len(c.PrimaryKey) {
		return nil, false, nil
	}
	group := map[string]bool{}
	for _, f := range r.GroupBy {
		group[f] = true
	}
	for _, f := range c.PrimaryKey {
		if !group[f] {
			return nil, false, nil
		}
	}
	for _, m := range r.Metrics {
		if m.Op != "count" || m.Field != "" {
			return nil, false, nil
		}
	}
	q := Query{Where: r.Where, RequireIndex: r.RequireIndex, Select: append([]string(nil), r.GroupBy...), Limit: r.Limit}
	for _, o := range r.OrderBy {
		if group[o.Field] {
			q.OrderBy = append(q.OrderBy, o)
		}
	}
	if err := validateQuery(c, &q); err != nil {
		return nil, true, err
	}
	records, err := t.find(c, q, r.Limit+1)
	if err != nil {
		return nil, true, err
	}
	out := make([]Record, 0, len(records))
	for _, record := range records {
		row := Record{}
		for _, f := range r.GroupBy {
			row[f] = record[f]
		}
		for _, m := range r.Metrics {
			row[m.Name] = "1"
		}
		out = append(out, row)
	}
	return out, true, nil
}

// Count-only outputs cannot hide numeric overflow in omitted groups. Other
// reducers retain full validation before pagination. Native sort+limit avoids
// materializing every result map, but the engine still retains hash group state.
func nativeAggregatePipeline(r Request, spec nativeAggregationPlan) []byte {
	stages := []json.RawMessage{marshal(map[string]any{"$group": spec.group})}
	eligible := len(r.OrderBy) > 0
	for _, m := range r.Metrics {
		if m.Op != "count" {
			eligible = false
		}
	}
	if eligible {
		group := map[string]bool{}
		for _, f := range r.GroupBy {
			group[f] = true
		}
		var sort bytes.Buffer
		sort.WriteString(`{"$sort":{`)
		for i, o := range r.OrderBy {
			if i > 0 {
				sort.WriteByte(',')
			}
			field := o.Field
			if group[field] {
				field = "_id." + field
			}
			sort.Write(marshal(field))
			sort.WriteByte(':')
			if o.Direction == "desc" {
				sort.WriteString("-1")
			} else {
				sort.WriteByte('1')
			}
		}
		sort.WriteString(`}}`)
		// RawMessage preserves sort precedence; marshaling a Go map would reorder keys.
		stages = append(stages, sort.Bytes(), marshal(map[string]any{"$limit": r.Limit + 1}))
	}
	return marshal(stages)
}
