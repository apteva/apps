package main

import (
	"context"
	"database/sql"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"sync/atomic"
	"time"
)

// One request owns one read snapshot and memoized evaluation results.
var queryCount atomic.Int64
var queryMillis atomic.Int64

type evaluationPlan struct {
	sqlRunner
	now     int64
	widgets map[string]cachedWidget
	fx      map[string]*fxRateIndex
}
type cachedWidget struct {
	data map[string]any
	err  error
}

func newEvaluationPlan(db sqlRunner) *evaluationPlan {
	return &evaluationPlan{sqlRunner: db, now: time.Now().UnixMilli() + 1, widgets: map[string]cachedWidget{}, fx: map[string]*fxRateIndex{}}
}
func copyResult(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func widgetCacheKey(project string, w DashboardWidget, filters map[string]any) string {
	raw, _ := json.Marshal([]any{project, w.Type, w.Config, filters})
	return string(raw)
}

type contextualDB struct {
	db interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}
	ctx context.Context
}

func (d contextualDB) Exec(q string, args ...any) (sql.Result, error) {
	return d.db.ExecContext(d.ctx, q, args...)
}
func (d contextualDB) Query(q string, args ...any) (*sql.Rows, error) {
	return d.db.QueryContext(d.ctx, q, args...)
}
func (d contextualDB) QueryRow(q string, args ...any) *sql.Row {
	return d.db.QueryRowContext(d.ctx, q, args...)
}
func readPool(ctx *sdk.AppCtx) *sql.DB {
	if db := ctx.AppReadDB(); db != nil {
		return db
	}
	return ctx.AppDB()
}
func requestReadDB(r *http.Request) sqlRunner {
	return contextualDB{db: readPool(globalCtx), ctx: r.Context()}
}
func boundedHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.RawQuery) > 64*1024 {
			http.Error(w, "query string exceeds 64 KiB", http.StatusRequestURITooLong)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, r.Body, 512*1024)
		start := time.Now()
		defer func() { queryCount.Add(1); queryMillis.Add(time.Since(start).Milliseconds()) }()
		next(w, r)
	}
}
