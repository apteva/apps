package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// A cancellable RW lock: waiting requests do not leave background goroutines.
type contextRWMutex struct {
	mu      sync.Mutex
	readers int
	writer  bool
	waiting int
	changed chan struct{}
}

func (m *contextRWMutex) signal() {
	if m.changed != nil {
		close(m.changed)
	}
	m.changed = make(chan struct{})
}
func (m *contextRWMutex) acquire(ctx context.Context, write bool) error {
	m.mu.Lock()
	if write {
		m.waiting++
	}
	for {
		if err := ctx.Err(); err != nil {
			if write {
				m.waiting--
				m.signal()
			}
			m.mu.Unlock()
			return err
		}
		if !m.writer && ((!write && m.waiting == 0) || (write && m.readers == 0)) {
			if write {
				m.waiting--
				m.writer = true
			} else {
				m.readers++
			}
			m.mu.Unlock()
			return nil
		}
		if m.changed == nil {
			m.changed = make(chan struct{})
		}
		changed := m.changed
		m.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		m.mu.Lock()
	}
}
func (m *contextRWMutex) release(write bool) {
	m.mu.Lock()
	if write {
		m.writer = false
	} else {
		m.readers--
	}
	m.signal()
	m.mu.Unlock()
}
func (m *contextRWMutex) Lock()    { _ = m.acquire(context.Background(), true) }
func (m *contextRWMutex) Unlock()  { m.release(true) }
func (m *contextRWMutex) RLock()   { _ = m.acquire(context.Background(), false) }
func (m *contextRWMutex) RUnlock() { m.release(false) }

type tableLockRef struct {
	lock  contextRWMutex
	users int
}

var activeContexts sync.Map // request-scoped AppCtx pointer -> context.Context
func requestContext(ctx *sdk.AppCtx) context.Context {
	if v, ok := activeContexts.Load(ctx); ok {
		return v.(context.Context)
	}
	return context.Background()
}

func (a *App) beginOperation(ctx *sdk.AppCtx, args map[string]any, operation string, schemaWrite bool) (*sdk.AppCtx, func(), error) {
	started := time.Now()
	parent, _ := args["_request_context"].(context.Context)
	if parent == nil {
		parent = context.Background()
	}
	duration := maxQueryMs(ctx) + maxReadQueueMs(ctx)
	if schemaWrite || operation == "rows_insert" || operation == "rows_upsert" || operation == "rows_update" || operation == "rows_delete" {
		duration = int(cfgInt64Range(ctx, "max_write_ms", 30000, 1, 300000))
	}
	callCtx, cancel := context.WithTimeout(parent, time.Duration(duration)*time.Millisecond)
	if err := validateArguments(args); err != nil {
		cancel()
		return nil, nil, err
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	scoped := ctx.WithProject(pid)
	activeContexts.Store(scoped, callCtx)
	cleanup := func() { activeContexts.Delete(scoped); cancel() }
	if err := a.schemaMu.acquire(callCtx, false); err != nil {
		cleanup()
		return nil, nil, queryStageErr("schema_queue", operation, err)
	}
	releases := []func(){a.schemaMu.RUnlock}
	names := []string{}
	if n := strArg(args, "table"); n != "" {
		names = append(names, n)
	} else if n := strArg(args, "name"); n != "" {
		names = append(names, n)
	}
	if operation == "tables_query" {
		names, err = placeholderNames(strArg(args, "sql"))
		if err != nil {
			a.schemaMu.RUnlock()
			cleanup()
			return nil, nil, err
		}
	}
	for _, name := range names {
		key := schemaCacheKey{pid, name}
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
		dropRef := func() {
			a.locksMu.Lock()
			ref.users--
			if ref.users == 0 {
				delete(a.tableLocks, key)
			}
			a.locksMu.Unlock()
		}
		if err = ref.lock.acquire(callCtx, schemaWrite); err != nil {
			dropRef()
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
			cleanup()
			return nil, nil, queryStageErr("schema_queue", name, err)
		}
		releases = append(releases, func() { ref.lock.release(schemaWrite); dropRef() })
	}
	return scoped, func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
		elapsed := time.Since(started)
		if elapsed >= time.Duration(slowQueryMs(ctx))*time.Millisecond {
			ctx.Logger().Info("tables operation", "operation", operation, "project_id", pid, "elapsed_ms", elapsed.Milliseconds())
		}
		cleanup()
	}, nil
}

func validateArguments(args map[string]any) error {
	for _, key := range []string{"where", "params", "select", "columns", "key", "rows", "metrics", "group_by"} {
		if v, ok := args[key]; ok && v != nil {
			if _, ok := v.([]any); !ok {
				return errf("%s must be an array", key)
			}
		}
	}
	for _, key := range []string{"id", "expected_revision", "expected_table_id", "limit", "offset"} {
		if v, ok := args[key]; ok {
			n, err := exactInteger(v)
			if err != nil || n < 0 || ((key == "id" || key == "expected_revision" || key == "expected_table_id") && n == 0) {
				return errf("%s must be an exact %s integer", key, map[bool]string{true: "positive", false: "nonnegative"}[key == "id" || key == "expected_revision" || key == "expected_table_id"])
			}
		}
	}
	for _, key := range []string{"confirm", "include_total", "hydrate_files", "unique", "summary", "release_managed"} {
		if v, ok := args[key]; ok {
			if _, ok := v.(bool); !ok {
				return errf("%s must be boolean", key)
			}
		}
	}
	return nil
}
func exactInteger(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case json.Number:
		return n.Int64()
	case string:
		return json.Number(n).Int64()
	case float64:
		if !math.IsNaN(n) && !math.IsInf(n, 0) && math.Trunc(n) == n && n >= -9007199254740991 && n <= 9007199254740991 {
			return int64(n), nil
		}
	}
	return 0, errf("invalid integer")
}

type writeTx struct {
	*sql.Tx
	ctx    context.Context
	counts map[int64]int64
}

func beginWrite(ctx *sdk.AppCtx) (*writeTx, error) {
	tx, err := ctx.AppDB().BeginTx(requestContext(ctx), nil)
	if err != nil {
		return nil, queryStageErr("writer_queue", "", err)
	}
	return &writeTx{Tx: tx, ctx: requestContext(ctx), counts: map[int64]int64{}}, nil
}
func (t *writeTx) Exec(q string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(t.ctx, q, args...)
}
func (t *writeTx) Query(q string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(t.ctx, q, args...)
}
func (t *writeTx) QueryRow(q string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(t.ctx, q, args...)
}
func (t *writeTx) Prepare(q string) (*sql.Stmt, error) { return t.Tx.PrepareContext(t.ctx, q) }

type statusError struct {
	status  int
	message string
}

func (e *statusError) Error() string { return e.message }
func notFound(format string, args ...any) error {
	return &statusError{http.StatusNotFound, fmt.Sprintf(format, args...)}
}
func conflict(format string, args ...any) error {
	return &statusError{http.StatusConflict, fmt.Sprintf(format, args...)}
}
func oversized() error {
	return &statusError{http.StatusRequestEntityTooLarge, "row exceeds result byte budget; select fewer columns"}
}
func errorStatus(err error) int {
	var e *statusError
	if errors.As(err, &e) {
		return e.status
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	if errors.Is(err, context.Canceled) {
		return http.StatusRequestTimeout
	}
	var stage *stagedQueryError
	if errors.As(err, &stage) {
		return http.StatusInternalServerError
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

func checkBatchBytes(ctx *sdk.AppCtx, rows any) error {
	size, err := jsonSize(rows, cfgInt64Range(ctx, "max_batch_bytes", 8<<20, 1024, 64<<20))
	if err != nil {
		return err
	}
	if size > cfgInt64Range(ctx, "max_batch_bytes", 8<<20, 1024, 64<<20) {
		return &statusError{413, "write batch exceeds max_batch_bytes"}
	}
	return nil
}

// Include expanded defaults in write limits, so a small input cannot materialize
// an unbounded batch of large default JSON/text values.
func chargeStoredValue(ctx *sdk.AppCtx, used *int64, name string, value any) error {
	cap := cfgInt64Range(ctx, "max_batch_bytes", 8<<20, 1024, 64<<20)
	size, err := jsonSize(value, cap-*used)
	if err != nil {
		return err
	}
	*used += size + jsonStringSize(name) + 4
	if *used > cap {
		return &statusError{413, "expanded write batch exceeds max_batch_bytes"}
	}
	return nil
}
