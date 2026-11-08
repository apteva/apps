package engine

import (
	"sort"
	"strings"

	native "github.com/apteva/db"
)

// Fully bound non-null keys have a cardinality guarantee. Other equality
// indexes are compared with capped index-only counts, doubling the cap until
// one finishes, up to a fixed planning allowance. Inconclusive probes retain
// the ordinary planner. Planning work never follows an entire broad index.
// Internal join hints always win; the query's complete predicate is unchanged.
func (t *aptevaTx) selectLookupIndex(c Collection, q Query) (Query, error) {
	if q.hint != "" {
		return q, nil
	}
	terms := conjuncts(q.Where)
	type candidate struct {
		index  Index
		prefix Filter
	}
	candidates := []candidate{}
	seen := map[string]bool{}
	for _, idx := range indexes(c) {
		prefix := []Filter{}
		names := []string{}
		for _, field := range idx.Fields {
			for _, term := range terms {
				if term.Field == field.Field && term.Op == "eq" && term.Value != nil {
					prefix = append(prefix, term)
					names = append(names, field.Field)
					break
				}
			}
		}
		if len(prefix) == 0 || len(prefix) != len(idx.Fields) {
			continue
		}
		if idx.Unique {
			q.hint = idx.Name
			return q, nil
		}
		sort.Strings(names)
		key := strings.Join(names, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, candidate{idx, Filter{And: prefix}})
	}
	if len(candidates) < 2 {
		return q, nil
	}
	h, e := t.readCollection(c)
	if e != nil {
		return q, e
	}
	const maxSelectionEntries = 1024
	work := int64(0)
	for cap := 1; cap <= 64; cap *= 2 {
		best := ""
		least := int64(cap)
		for _, candidate := range candidates {
			if work+int64(cap) > maxSelectionEntries {
				return q, nil
			}
			var stats native.QueryStats
			n, e := h.CountDocuments(t.ctx, marshal(nativeFilter(c, &candidate.prefix)), native.CountOptions{Hint: candidate.index.Name, Limit: cap, MaxMemoryBytes: int64(t.backend.options.QueryWorkBytes), Stats: &stats})
			if e != nil {
				return q, nativeError(e)
			}
			work += stats.IndexEntriesExamined
			if q.diag != nil {
				q.diag.selectionProbes++
				q.diag.selectionEntries += stats.IndexEntriesExamined
			}
			if n < least {
				least, best = n, candidate.index.Name
			}
		}
		if best != "" {
			q.hint = best
			return q, nil
		}
	}
	return q, nil
}

// Native CountDocuments drops non-covering hints on fallback. Such a count
// must evaluate residual predicates through the selected streaming lookup.
func nativeCountHasResidual(c Collection, q Query, p accessPlan) bool {
	if !p.IndexedLookup || p.ranges != nil {
		return false
	}
	fields := map[string]bool{}
	for _, idx := range indexes(c) {
		if idx.Name == p.Index {
			for _, field := range idx.Fields {
				fields[field.Field] = true
			}
			break
		}
	}
	residual := false
	visitFilterFields(q.Where, func(field string) { residual = residual || !fields[field] })
	return residual
}
