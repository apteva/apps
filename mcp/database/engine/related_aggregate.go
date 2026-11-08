package engine

// aggregateRelatedParents consumes one row per matching parent. Context rows
// decide eligibility after the shared related-read evaluator has capped them;
// their multiplicity never weights a parent's metrics.
func aggregateRelatedParents(tx transaction, c Collection, r Request, alias string, budget *relBudget, state *existsState, related func(Record) ([]Record, error), matchView Collection, order []Order, limit int, sources []*relChain) (any, error) {
	chain, e := newRelChain(tx, rootView(c, alias), []string{alias}, budget, nil, r.Computed, nil, nil)
	if e != nil {
		return nil, e
	}
	view := cloneCollection(c)
	for _, field := range chain.view.Fields {
		if _, e := view.field(field.Name); e == nil {
			return nil, Invalid("computed field %s conflicts with a parent field", field.Name)
		}
		view.Fields = append(view.Fields, field)
	}
	final := Request{Query: Query{OrderBy: order, Limit: limit}, GroupBy: r.GroupBy, Metrics: r.Metrics, Derived: r.Derived}
	if e := noExistsInMetrics(final.Metrics); e != nil {
		return nil, e
	}
	output, e := validateAggregate(view, &final)
	if e != nil {
		return nil, e
	}
	if e := validateDerived(output, final.Derived); e != nil {
		return nil, e
	}
	use := func(name string) {
		if _, e := c.field(name); e == nil {
			chain.use(alias + "." + name)
		} else {
			chain.use(name)
		}
	}
	for _, field := range final.GroupBy {
		use(field)
	}
	for _, m := range final.Metrics {
		if m.Field != "" {
			use(m.Field)
		}
		visitFilterFields(m.Where, use)
	}
	chain.finish()
	fields := rootFields(c, alias, chain)
	add := func(name string) {
		if !contains(fields, name) {
			fields = append(fields, name)
		}
	}
	visitFilterFields(r.Where, add)
	for _, source := range sources {
		for _, name := range rootFields(c, alias, source) {
			add(name)
		}
	}
	for _, o := range r.OrderBy {
		add(o.Field)
	}
	rows, e := aggregateRecordsSharedBudget(budget.ctx, view, final, output, MaxQueryWorkBytes, func(_ Query, _ []string, visit func(Record, []byte) bool) error {
		return eachRelatedAggregateParent(tx, c, r, state, fields, budget, func(parent Record) (bool, error) {
			if e := budget.spend(1); e != nil {
				return false, e
			}
			size := retainedBytes(parent)
			if e := budget.retain(size); e != nil {
				return false, e
			}
			defer budget.retain(-size)
			if r.Match != nil {
				entries, e := related(parent)
				if e != nil {
					return false, e
				}
				size := retainedBytes(entries)
				if e := budget.retain(size); e != nil {
					return false, e
				}
				hit := false
				for _, entry := range entries {
					if e := budget.spend(1); e != nil {
						budget.retain(-size)
						return false, e
					}
					if matches(matchView, entry, r.Match) {
						hit = true
						break
					}
				}
				budget.retain(-size)
				if !hit {
					return true, nil
				}
			}
			proceed := true
			_, e := chain.run(rootRow(c, alias, parent), func(row Record) (bool, error) {
				combined := make(Record, len(parent)+len(row))
				for k, v := range parent {
					combined[k] = v
				}
				for k, v := range row {
					combined[k] = v
				}
				size := retainedBytes(combined)
				if e := budget.retain(size); e != nil {
					return false, e
				}
				proceed = visit(combined, nil)
				budget.retain(-size)
				return proceed, nil
			})
			chain.forget()
			return proceed, e
		})
	}, budget.retain)
	if e != nil {
		return nil, e
	}
	return finishAggregate(rows, final.Limit, final.Derived, budget.retain)
}

// Adapters that permit nested reads during a scan stream directly. Other
// adapters use keyset batches bounded conservatively by the record-size limit.
func eachRelatedAggregateParent(tx transaction, c Collection, r Request, state *existsState, fields []string, budget *relBudget, yield func(Record) (bool, error)) error {
	if reader, ok := tx.(unorderedRootReader); ok && reader.unorderedRoots() {
		return eachRootOnce(tx, c, r.Where, state, r.OrderBy, fields, budget, yield)
	}
	var after Record
	for {
		batch := (relationalRetained - budget.retained) / (2 * MaxBytes)
		if batch < 1 {
			batch = 1
		}
		if batch > relBatch {
			batch = relBatch
		}
		q := Query{Where: r.Where, OrderBy: r.OrderBy, Select: fields, Limit: batch}
		if after != nil {
			q.Where = and(r.Where, keysetFrom(c, r.OrderBy, after))
		}
		rows, e := tx.find(c, q, batch)
		if e != nil {
			return e
		}
		if e := state.failure(); e != nil {
			return e
		}
		if e := budget.spend(int64(len(rows))); e != nil {
			return e
		}
		size := queryRowsBytes(rows)
		if e := budget.retain(size); e != nil {
			return e
		}
		stopped := false
		for _, row := range rows {
			more, err := yield(row)
			if !more || err != nil {
				e, stopped = err, true
				break
			}
		}
		budget.retain(-size)
		if stopped {
			return e
		}
		if len(rows) < batch {
			return nil
		}
		last := rows[len(rows)-1]
		after = project(last, orderNames(r.OrderBy))
	}
}
