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
