package engine

// relProbeRows bounds a lookup while decoding it, before the join memo owns the
// rows. All shipped adapters expose the projection scanner. The fallback keeps
// the transaction interface compatible with existing wrappers.
func relProbeRows(tx transaction, c Collection, q Query, limit int, budget *relBudget) ([]Record, error) {
	scanner, ok := tx.(recordScanner)
	if !ok {
		return tx.find(c, q, limit)
	}
	rows := []Record{}
	retained := 0
	defer func() { budget.retain(-retained) }()
	var failed error
	e := scanner.scanRecords(c, q, q.Select, func(row Record) bool {
		if failed = budget.spend(0); failed != nil {
			return false
		}
		row = retainSelected(row, q)
		n := 16 + retainedBytes(row)
		if failed = budget.retain(n); failed != nil {
			return false
		}
		retained += n
		rows = append(rows, row)
		return len(rows) < limit
	})
	if failed != nil {
		return nil, failed
	}
	if e != nil {
		return nil, e
	}
	return rows, nil
}
