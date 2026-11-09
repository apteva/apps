package engine

import (
	"runtime"
	"sync"

	"golang.org/x/sync/semaphore"
)

// readBatchConcurrency bounds how many reads of one batch run at once on a
// backend whose read transaction allows it. Each native aggregate already
// runs its own workers; eight measured no faster than four. Every read keeps
// its own work budget. Tests change it.
var readBatchConcurrency = min(4, runtime.GOMAXPROCS(0))

// concurrentReader is a read transaction whose operations may overlap: they
// share one immutable snapshot and no per-transaction state.
type concurrentReader interface {
	concurrentReads() bool
}

// executeReads runs a read batch in one transaction, so every result comes
// from the same snapshot. Results keep request order, the first error in
// request order fails the whole batch, and no operation starts once one has
// failed or the responses exceed MaxBytes.
func executeReads(tx transaction, r Request, cursorKey []byte) (any, error) {
	for _, o := range r.Operations {
		if o.Request.TimeoutMS != 0 {
			return nil, Invalid("batch operations do not accept timeoutMs; set it on the batch")
		}
		if o.Request.Database != "" && o.Request.Database != r.Database {
			return nil, Invalid("batch cannot span databases")
		}
	}
	n := len(r.Operations)
	out, errs, sizes := make([]any, n), make([]error, n), make([]int, n)
	tooLarge := fail("resource_limit", "read batch response exceeds 4 MiB")
	workers := 1
	if c, ok := tx.(concurrentReader); ok && c.concurrentReads() {
		workers = min(readBatchConcurrency, n)
	}
	ops := r.Operations
	if workers > 1 {
		// Validation normalizes a request in place and operations may share
		// filters, so each gets its own copy, decoded as from the wire.
		ops = make([]Operation, n)
		for i, o := range r.Operations {
			ops[i].Op = o.Op
			if e := decode(marshal(o.Request), &ops[i].Request); e != nil {
				return nil, e
			}
		}
	}
	native := nativeBatchTx(tx)
	groups := batchAggregateGroups(tx, ops, workers)
	// A fused group can include later operations. Reserve all of its reducer
	// budgets together so sharing never increases the batch's active work.
	var work *semaphore.Weighted
	if len(groups) > 0 {
		work = semaphore.NewWeighted(int64(workers))
	}
	run := func(i int) (any, error) {
		if group := groups[i]; group != nil {
			group.once.Do(func() {
				weight := int64(len(group.members))
				if err := work.Acquire(native.ctx, weight); err != nil {
					return
				}
				defer work.Release(weight)
				options := native.backend.options
				budget := len(group.members) * options.QueryWorkBytes
				group.results = group.run(native.ctx, native.batchAggregateSource(group.collection, group.input), budget, options.AggregateMaxGroups)
			})
			if group.results != nil {
				return group.results[i], nil
			}
		}
		if work != nil {
			if err := work.Acquire(native.ctx, 1); err != nil {
				return nil, err
			}
			defer work.Release(1)
		}
		return execute(tx, ops[i].Op, ops[i].Request, cursorKey)
	}

	if workers <= 1 {
		total := 0
		for i := range n {
			v, e := run(i)
			if e != nil {
				return nil, e
			}
			if total += len(marshal(v)); total > MaxBytes {
				return nil, tooLarge
			}
			out[i] = v
		}
		return map[string]any{"results": out, "affected": total}, nil
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	stopped, total := false, 0
	next := make(chan int)
	for range workers {
		wg.Go(func() {
			for i := range next {
				v, e := run(i)
				size := 0
				if e == nil {
					size = len(marshal(v))
				}
				mu.Lock()
				out[i], errs[i], sizes[i] = v, e, size
				if total += size; e != nil || total > MaxBytes {
					stopped = true
				}
				mu.Unlock()
			}
		})
	}
	for i := range n {
		mu.Lock()
		stop := stopped
		mu.Unlock()
		if stop {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	// Operations start in request order, so an unstarted one follows every
	// started one: the first error or overflow in order decides the outcome.
	total = 0
	for i := range n {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if out[i] == nil {
			return nil, tooLarge
		}
		if total += sizes[i]; total > MaxBytes {
			return nil, tooLarge
		}
	}
	return map[string]any{"results": out, "affected": total}, nil
}
