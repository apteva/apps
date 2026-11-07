package engine

// relationAnalytics reuses typed lookup/date-bucket planning on an intermediate
// relation. Rows are copied before enrichment so reused input nodes stay immutable.
type relationBudget interface {
	spend(int64) error
	retain(int) error
}

type relationAnalytics struct {
	plan *analyticsPlan
	view Collection
}

func prepareRelationAnalytics(tx transaction, view Collection, r Request) (*relationAnalytics, error) {
	// Where on a joined request belongs to the stored root. The caller applies
	// any terminal predicate separately after enrichment.
	definition := Request{Lookups: r.Lookups, Buckets: r.Buckets}
	if e := prepareAnalytics(tx, view, &definition); e != nil {
		return nil, e
	}
	known := Collection{Fields: append([]Field(nil), view.Fields...)}
	for _, lookup := range definition.analytics.lookups {
		if _, e := queryField(known, lookup.LocalField); e != nil {
			return nil, e
		}
		for i, f := range lookup.fields {
			f.Name = lookup.names[i]
			known.Fields = append(known.Fields, f)
		}
	}
	for _, bucket := range definition.analytics.buckets {
		if _, e := queryField(known, bucket.Field); e != nil {
			return nil, e
		}
	}
	// Intermediate plans share a memory budget; avoid an unaccounted bucket cache.
	for i := range definition.analytics.buckets {
		definition.analytics.buckets[i].hours = nil
	}
	return &relationAnalytics{plan: definition.analytics, view: definition.analytics.view}, nil
}
func (a *relationAnalytics) stream(input func(func(Record) (bool, error)) error, yield func(Record) (bool, error), runtime relationBudget) error {
	cache := newLookupCache(len(a.plan.lookups), MaxQueryWorkBytes/4)
	charged := 0
	if runtime != nil {
		defer func() { runtime.retain(-charged) }()
	}
	return input(func(row Record) (bool, error) {
		out := queryCopy(row)
		if runtime != nil {
			if e := runtime.spend(int64(1 + len(a.plan.lookups)*relProbeWork)); e != nil {
				return false, e
			}
		}
		if e := a.plan.enrich(out, cache); e != nil {
			return false, e
		}
		if runtime != nil {
			if e := runtime.retain(cache.bytes - charged); e != nil {
				return false, e
			}
			charged = cache.bytes
		}
		return yield(out)
	})
}
