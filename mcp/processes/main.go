package main

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
	"sync"
	"time"
)

//go:embed apteva.yaml
var manifestYAML string

//go:embed skills/how-to-use-processes.md
var skillBody string

type App struct {
	ctx *sdk.AppCtx
	db  *sql.DB
	mu  sync.Mutex
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic(err)
	}
	for i := range m.Provides.Skills {
		m.Provides.Skills[i].Body = skillBody
		m.Provides.Skills[i].BodyFile = ""
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx == nil || ctx.AppDB() == nil {
		return errors.New("processes requires a database")
	}
	a.ctx = ctx
	a.db = ctx.AppDB()
	return nil
}
func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{Name: "event-triggers", Schedule: "@every 5s", Run: func(ctx context.Context, app *sdk.AppCtx) error { return a.tickTriggers(ctx) }}, {Name: "direct-runs", Schedule: "@every 5s", Run: func(ctx context.Context, app *sdk.AppCtx) error { return a.tickDirect(ctx, time.Now().UTC()) }}, {Name: "task-sync", Schedule: "@every 30s", Run: func(ctx context.Context, app *sdk.AppCtx) error { return a.retryPending(ctx) }}}
}
func main() { sdk.Run(&App{}) }
