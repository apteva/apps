package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"sync"
	"time"
)

type sqlDatabase interface {
	sqlRunner
	Begin() (*sql.Tx, error)
}

func (d contextualDB) Begin() (*sql.Tx, error) {
	db, ok := d.db.(*sql.DB)
	if !ok {
		return nil, errors.New("nested transaction unsupported")
	}
	return db.BeginTx(d.ctx, nil)
}

var toolContexts sync.Map

func toolContext(app *sdk.AppCtx) context.Context {
	if ctx, ok := toolContexts.Load(app); ok {
		return ctx.(context.Context)
	}
	return context.Background()
}
func toolReader(app *sdk.AppCtx) sqlRunner {
	return contextualDB{db: readPool(app), ctx: toolContext(app)}
}
func toolWriter(app *sdk.AppCtx) sqlDatabase {
	return contextualDB{db: app.AppDB(), ctx: toolContext(app)}
}
func requestWriteDB(r *http.Request) sqlDatabase {
	return contextualDB{db: globalCtx.AppDB(), ctx: r.Context()}
}
func withToolDeadlines(items []sdk.Tool) []sdk.Tool {
	for i := range items {
		handler := items[i].Handler
		if handler == nil {
			continue
		}
		items[i].Handler = nil
		items[i].HandlerCtx = func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
			raw, err := json.Marshal(args)
			if err != nil {
				return nil, err
			}
			if len(raw) > 512*1024 {
				return nil, errors.New("tool payload exceeds 512 KiB")
			}
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			scoped := app.WithProject(app.CurrentProject())
			toolContexts.Store(scoped, ctx)
			defer toolContexts.Delete(scoped)
			return handler(scoped, args)
		}
	}
	return items
}
