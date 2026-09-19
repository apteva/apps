package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// tables_batch is a transport-level executor. It deliberately delegates every
// operation to the same typed handler used by the standalone MCP tool, so
// validation, authorization, row limits, and SQL construction stay unified.
var batchHandlers = map[string]func(*App, *sdk.AppCtx, map[string]any) (any, error){
	"tables_list":     func(a *App, c *sdk.AppCtx, x map[string]any) (any, error) { return a.toolTablesList(c, x) },
	"tables_describe": func(a *App, c *sdk.AppCtx, x map[string]any) (any, error) { return a.toolTablesDescribe(c, x) },
	"rows_search":     func(a *App, c *sdk.AppCtx, x map[string]any) (any, error) { return a.toolRowsSearch(c, x) },
	"rows_get":        func(a *App, c *sdk.AppCtx, x map[string]any) (any, error) { return a.toolRowsGet(c, x) },
	"rows_count":      func(a *App, c *sdk.AppCtx, x map[string]any) (any, error) { return a.toolRowsCount(c, x) },
	"rows_aggregate":  func(a *App, c *sdk.AppCtx, x map[string]any) (any, error) { return a.toolRowsAggregate(c, x) },
	"tables_query":    func(a *App, c *sdk.AppCtx, x map[string]any) (any, error) { return a.toolTablesQuery(c, x) },
	"rows_insert":     func(a *App, c *sdk.AppCtx, x map[string]any) (any, error) { return a.toolRowsInsert(c, x) },
	"rows_update":     func(a *App, c *sdk.AppCtx, x map[string]any) (any, error) { return a.toolRowsUpdate(c, x) },
	"rows_upsert":     func(a *App, c *sdk.AppCtx, x map[string]any) (any, error) { return a.toolRowsUpsert(c, x) },
	"rows_delete":     func(a *App, c *sdk.AppCtx, x map[string]any) (any, error) { return a.toolRowsDelete(c, x) },
}

var batchWriteOperations = map[string]bool{
	"rows_insert": true, "rows_update": true, "rows_upsert": true, "rows_delete": true,
}

type batchSchemaCacheKey struct{}

type batchOperation struct {
	id       string
	name     string
	args     map[string]any
	deps     map[string]bool
	position int
}

type batchResult struct {
	status string
	value  any
	err    error
}

func (a *App) toolTablesBatch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	parent, ok := args["_request_context"].(context.Context)
	if !ok || parent == nil {
		parent = context.Background()
	}
	projectID, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	mode := strArg(args, "mode")
	if mode == "" {
		mode = "best_effort"
	}
	if mode != "read_snapshot" && mode != "write_transaction" && mode != "best_effort" {
		return nil, errf("mode must be read_snapshot, write_transaction, or best_effort")
	}
	rawOps, ok := args["operations"].([]any)
	if !ok || len(rawOps) == 0 {
		return nil, errf("operations is required and must be non-empty")
	}
	if len(rawOps) > maxBatchOperations(ctx) {
		return nil, errf("operations exceeds maximum of %d", maxBatchOperations(ctx))
	}
	ops, err := parseBatchOperations(rawOps, projectID)
	if err != nil {
		return nil, err
	}
	if mode == "write_transaction" {
		for _, op := range ops {
			if !batchWriteOperations[op.name] {
				return nil, errf("write_transaction only accepts row write operations; %q is read-only or unsupported", op.name)
			}
		}
	}
	if err := validateBatchGraph(ops); err != nil {
		return nil, err
	}

	batchCtx, cancel := context.WithTimeout(parent, time.Duration(maxBatchMs(ctx))*time.Millisecond)
	defer cancel()
	preloaded, err := a.preloadBatchSchemas(ctx, projectID, ops)
	if err != nil {
		return nil, err
	}
	batchCtx = context.WithValue(batchCtx, batchSchemaCacheKey{}, preloaded)
	var releaseSnapshot func()
	if mode == "read_snapshot" {
		releaseSnapshot, err = a.acquireBatchReadLocks(batchCtx, ops)
		if err != nil {
			return nil, queryStageErr("schema_queue", "tables_batch", err)
		}
		defer releaseSnapshot()
	}
	var readState *batchReadState
	if mode == "read_snapshot" {
		conn, err := ctx.AppReadDB().Conn(batchCtx)
		if err != nil {
			return nil, queryStageErr("read_queue", "tables_batch", err)
		}
		tx, err := conn.BeginTx(batchCtx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			_ = conn.Close()
			return nil, queryStageErr("select", "tables_batch", err)
		}
		readState = &batchReadState{conn: conn, tx: tx}
		batchCtx = context.WithValue(batchCtx, batchReadStateKey{}, readState)
		defer func() {
			_ = readState.tx.Rollback()
			_ = readState.conn.Close()
		}()
	}
	results := make(map[string]batchResult, len(ops))
	resultJSONBytes := int64(0)
	resultRows := 0
	counted := map[string]bool{}
	var shared *writeTx
	var eventBuffer *batchEventBuffer
	if mode == "write_transaction" {
		tx, err := ctx.AppDB().BeginTx(batchCtx, nil)
		if err != nil {
			return nil, queryStageErr("writer_queue", "tables_batch", err)
		}
		shared = &writeTx{Tx: tx, ctx: batchCtx, counts: map[int64]int64{}}
		eventBuffer = &batchEventBuffer{}
		batchCtx = context.WithValue(batchCtx, batchWriteTxKey{}, shared)
		batchCtx = context.WithValue(batchCtx, batchEventBufferKey{}, eventBuffer)
	}

	failedTransaction := false
	for len(results) < len(ops) {
		ready := readyBatchOperations(ops, results)
		if len(ready) == 0 {
			break
		}
		parallel := mode == "best_effort" && allBatchReads(ready) && batchReadEstimateLarge(ctx, ready)
		if parallel && len(ready) > 1 {
			var wg sync.WaitGroup
			out := make(chan struct {
				op batchOperation
				r  batchResult
			}, len(ready))
			for _, op := range ready {
				op := op
				wg.Add(1)
				go func() {
					defer wg.Done()
					value, err := a.runBatchOperation(ctx, batchCtx, op, results)
					out <- struct {
						op batchOperation
						r  batchResult
					}{op, batchResult{status: batchStatus(err), value: value, err: err}}
				}()
			}
			wg.Wait()
			close(out)
			for item := range out {
				results[item.op.id] = item.r
			}
		} else {
			for _, op := range ready {
				value, err := a.runBatchOperation(ctx, batchCtx, op, results)
				results[op.id] = batchResult{status: batchStatus(err), value: value, err: err}
				if mode == "write_transaction" && err != nil {
					failedTransaction = true
					break
				}
			}
		}
		if failedTransaction {
			break
		}
		for id, item := range results {
			if counted[id] {
				continue
			}
			if item.status != "ok" || item.value == nil {
				continue
			}
			b, err := jsonSize(item.value, maxBatchResultBytes(ctx)-resultJSONBytes)
			if err != nil {
				if shared != nil {
					_ = shared.Rollback()
				}
				return nil, err
			}
			resultJSONBytes += b
			resultRows += countBatchRows(item.value)
			if resultJSONBytes > maxBatchResultBytes(ctx) {
				if shared != nil {
					_ = shared.Rollback()
				}
				return nil, errf("batch result exceeds max_batch_result_bytes")
			}
			if resultRows > maxBatchResultRows(ctx) {
				if shared != nil {
					_ = shared.Rollback()
				}
				return nil, errf("batch result exceeds max_batch_result_rows")
			}
			counted[id] = true
		}
	}

	if mode == "write_transaction" {
		if failedTransaction {
			_ = shared.Rollback()
			for id, item := range results {
				if item.status == "ok" {
					results[id] = batchResult{status: "rolled_back", value: item.value}
				}
			}
		} else if err := shared.Tx.Commit(); err != nil {
			return nil, err
		} else if eventBuffer != nil {
			for _, event := range eventBuffer.snapshot() {
				ctx.Emit(event.topic, event.data)
			}
		}
	}
	for _, op := range ops {
		if _, exists := results[op.id]; !exists {
			results[op.id] = batchResult{status: "skipped_dependency"}
		}
	}
	return formatBatchResults(mode, ops, results), nil
}

// Small independent reads use the shared metadata/plan path. Once the
// estimated result is large, keep the existing pooled parallel behavior so a
// batch cannot turn several fast native reads into one serialized response.
func batchReadEstimateLarge(ctx *sdk.AppCtx, ops []batchOperation) bool {
	rows := 0
	bytes := int64(0)
	for _, op := range ops {
		limit := 50
		if raw, ok := op.args["limit"]; ok {
			if n, err := exactInteger(raw); err == nil && n > 0 {
				limit = int(n)
			}
		}
		if op.name == "rows_get" {
			limit = 1
		}
		if op.name == "rows_count" {
			limit = 1
		}
		rows += limit
		bytes += int64(limit) * 512
	}
	return rows > maxBatchOptimizedRows(ctx) || bytes > maxBatchOptimizedBytes(ctx)
}

func (a *App) preloadBatchSchemas(ctx *sdk.AppCtx, projectID string, ops []batchOperation) (map[schemaCacheKey]*Table, error) {
	names := map[string]bool{}
	for _, op := range ops {
		if table, ok := op.args["table"].(string); ok && table != "" {
			names[table] = true
		}
		if op.name == "tables_describe" {
			if name, ok := op.args["name"].(string); ok && name != "" {
				names[name] = true
			}
		}
		if op.name == "tables_query" {
			placeholders, err := placeholderNames(strArg(op.args, "sql"))
			if err != nil {
				continue
			}
			for _, name := range placeholders {
				names[name] = true
			}
		}
	}
	cache := make(map[schemaCacheKey]*Table, len(names))
	scoped := ctx.WithProject(projectID)
	for name := range names {
		if err := validateIdentifier("table", name); err != nil {
			continue
		}
		table, err := a.loadTableSchema(scoped, projectID, name)
		if err != nil {
			// Preserve per-operation isolation. A missing or malformed table
			// is reported by its nested handler rather than aborting siblings.
			continue
		}
		cache[schemaCacheKey{projectID: projectID, tableName: name}] = table
	}
	return cache, nil
}

// SQLite connections in the current deployment do not all share one
// snapshot when the read pool is enabled. Holding the schema read lock and
// the per-table read locks for the whole batch gives read_snapshot equivalent
// consistency: schema changes and writes to every referenced table are
// excluded between the first and last operation. Operations then run
// sequentially, while best_effort retains the read pool's parallelism.
func (a *App) acquireBatchReadLocks(ctx context.Context, ops []batchOperation) (func(), error) {
	if err := a.schemaMu.acquire(ctx, false); err != nil {
		return nil, err
	}
	releases := []func(){a.schemaMu.RUnlock}
	names := map[string]bool{}
	for _, op := range ops {
		if table, ok := op.args["table"].(string); ok && table != "" {
			names[table] = true
		}
		if op.name == "tables_describe" {
			if name, ok := op.args["name"].(string); ok && name != "" {
				names[name] = true
			}
		}
		if op.name == "tables_query" {
			placeholderList, err := placeholderNames(strArg(op.args, "sql"))
			if err != nil {
				a.schemaMu.RUnlock()
				return nil, err
			}
			for _, name := range placeholderList {
				names[name] = true
			}
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	projectID, err := resolveProjectFromArgs(ops[0].args)
	if err != nil {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
		return nil, err
	}
	for _, name := range ordered {
		key := schemaCacheKey{projectID, name}
		a.locksMu.Lock()
		if a.tableLocks == nil {
			a.tableLocks = make(map[schemaCacheKey]*tableLockRef)
		}
		ref := a.tableLocks[key]
		if ref == nil {
			ref = &tableLockRef{}
			a.tableLocks[key] = ref
		}
		ref.users++
		a.locksMu.Unlock()
		if err := ref.lock.acquire(ctx, false); err != nil {
			a.locksMu.Lock()
			ref.users--
			if ref.users == 0 {
				delete(a.tableLocks, key)
			}
			a.locksMu.Unlock()
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
			return nil, err
		}
		releases = append(releases, func() {
			ref.lock.release(false)
			a.locksMu.Lock()
			ref.users--
			if ref.users == 0 {
				delete(a.tableLocks, key)
			}
			a.locksMu.Unlock()
		})
	}
	return func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}, nil
}

func parseBatchOperations(raw []any, projectID string) ([]batchOperation, error) {
	ops := make([]batchOperation, 0, len(raw))
	seen := map[string]bool{}
	for i, value := range raw {
		obj, ok := value.(map[string]any)
		if !ok {
			return nil, errf("operations[%d] must be an object", i)
		}
		id, _ := obj["id"].(string)
		name, _ := obj["operation"].(string)
		if err := validateIdentifier("operation id", id); err != nil {
			return nil, errf("operations[%d]: %v", i, err)
		}
		if seen[id] {
			return nil, errf("operations[%d]: duplicate id %q", i, id)
		}
		seen[id] = true
		if _, ok := batchHandlers[name]; !ok {
			return nil, errf("operations[%d]: unsupported operation %q", i, name)
		}
		rawArgs, ok := obj["args"].(map[string]any)
		if !ok {
			return nil, errf("operations[%d]: args must be an object", i)
		}
		args := cloneBatchMap(rawArgs)
		args["_project_id"] = projectID
		if name == "rows_search" {
			if _, requested := args["include_total"]; !requested {
				// A batch commonly combines several reads; avoid an implicit
				// COUNT(*) for every search unless the caller asks for total.
				args["include_total"] = false
			}
		}
		deps := map[string]bool{}
		collectBatchRefs(args, deps)
		ops = append(ops, batchOperation{id: id, name: name, args: args, deps: deps, position: i})
	}
	return ops, nil
}

func validateBatchGraph(ops []batchOperation) error {
	ids := map[string]bool{}
	for _, op := range ops {
		ids[op.id] = true
		for dep := range op.deps {
			if !ids[dep] {
				// Forward references are valid; check after collecting all IDs.
			}
		}
	}
	for _, op := range ops {
		for dep := range op.deps {
			if !ids[dep] {
				return errf("operation %q references unknown operation %q", op.id, dep)
			}
		}
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return errf("batch dependency cycle includes %q", id)
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		for _, op := range ops {
			if op.id == id {
				for dep := range op.deps {
					if err := visit(dep); err != nil {
						return err
					}
				}
			}
		}
		state[id] = 2
		return nil
	}
	for _, op := range ops {
		if err := visit(op.id); err != nil {
			return err
		}
	}
	return nil
}

func readyBatchOperations(ops []batchOperation, results map[string]batchResult) []batchOperation {
	ready := []batchOperation{}
	for _, op := range ops {
		if _, done := results[op.id]; done {
			continue
		}
		allDone := true
		blocked := false
		for dep := range op.deps {
			result, done := results[dep]
			if !done {
				allDone = false
				break
			}
			if result.status != "ok" {
				blocked = true
			}
		}
		if blocked {
			results[op.id] = batchResult{status: "skipped_dependency"}
			continue
		}
		if allDone {
			ready = append(ready, op)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].position < ready[j].position })
	return ready
}

func allBatchReads(ops []batchOperation) bool {
	for _, op := range ops {
		if batchWriteOperations[op.name] {
			return false
		}
	}
	return true
}

func (a *App) runBatchOperation(root *sdk.AppCtx, callCtx context.Context, op batchOperation, results map[string]batchResult) (any, error) {
	resolved, err := resolveBatchRefs(op.args, results)
	if err != nil {
		return nil, err
	}
	resolved["_request_context"] = callCtx
	return batchHandlers[op.name](a, root, resolved)
}

func collectBatchRefs(value any, deps map[string]bool) {
	switch v := value.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok && len(v) == 1 {
			if dot := strings.IndexByte(ref, '.'); dot > 0 {
				deps[ref[:dot]] = true
			}
			return
		}
		for _, child := range v {
			collectBatchRefs(child, deps)
		}
	case []any:
		for _, child := range v {
			collectBatchRefs(child, deps)
		}
	}
}

func resolveBatchRefs(value any, results map[string]batchResult) (map[string]any, error) {
	out, err := resolveBatchValue(value, results)
	if err != nil {
		return nil, err
	}
	return out.(map[string]any), nil
}

func resolveBatchValue(value any, results map[string]batchResult) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok && len(v) == 1 {
			parts := strings.Split(ref, ".")
			if len(parts) < 2 {
				return nil, errf("reference %q must include a result path", ref)
			}
			item, ok := results[parts[0]]
			if !ok || item.status != "ok" {
				return nil, errf("reference %q is unavailable", ref)
			}
			current := item.value
			for _, part := range parts[1:] {
				obj, ok := current.(map[string]any)
				if !ok {
					return nil, errf("reference %q traverses a non-object", ref)
				}
				current, ok = obj[part]
				if !ok {
					return nil, errf("reference %q path not found", ref)
				}
			}
			return current, nil
		}
		out := make(map[string]any, len(v))
		for key, child := range v {
			resolved, err := resolveBatchValue(child, results)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			resolved, err := resolveBatchValue(child, results)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return value, nil
	}
}

func cloneBatchMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func batchStatus(err error) string {
	if err == nil {
		return "ok"
	}
	return "error"
}

func formatBatchResults(mode string, ops []batchOperation, results map[string]batchResult) map[string]any {
	out := make(map[string]any, len(ops))
	for _, op := range ops {
		item := results[op.id]
		entry := map[string]any{"status": item.status}
		if item.value != nil && item.status != "error" && item.status != "skipped_dependency" {
			entry["result"] = item.value
		}
		if item.err != nil {
			entry["error"] = map[string]any{"code": batchErrorCode(item.err), "message": item.err.Error()}
		}
		out[op.id] = entry
	}
	return map[string]any{"mode": mode, "results": out}
}

func batchErrorCode(err error) string {
	return fmt.Sprintf("http_%d", errorStatus(err))
}

func countBatchRows(value any) int {
	count := 0
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if key == "rows" {
				if rows, ok := child.([]map[string]any); ok {
					count += len(rows)
				} else if rows, ok := child.([]any); ok {
					count += len(rows)
				}
			}
			count += countBatchRows(child)
		}
	case []any:
		for _, child := range v {
			count += countBatchRows(child)
		}
	}
	return count
}
