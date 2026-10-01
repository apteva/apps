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
	ctx     *sdk.AppCtx
	db      *sql.DB
	mu      sync.Mutex
	eventMu sync.Mutex
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
	return []sdk.Worker{{Name: "publish-events", Schedule: "@every 1s", Run: func(ctx context.Context, app *sdk.AppCtx) error { return a.drainEvents(ctx, app.CurrentProject()) }}, {Name: "event-triggers", Schedule: "@every 5s", Run: func(ctx context.Context, app *sdk.AppCtx) error { return a.tickTriggers(ctx, app.CurrentProject()) }}, {Name: "process-runs", Schedule: "@every 5s", Run: func(ctx context.Context, app *sdk.AppCtx) error {
		return a.tickDirect(ctx, time.Now().UTC(), app.CurrentProject())
	}}, {Name: "process-reconciliation", Schedule: "@every 30s", Run: func(ctx context.Context, app *sdk.AppCtx) error { return a.retryPending(ctx, app.CurrentProject()) }}}
}

// The SDK dispatches global workers once per visible project. Never turn an
// empty scope into an installation-wide scan. Optional scope is for internal
// callers on project installations, not a bypass of project isolation.
func (a *App) workerProject(scope []string) string {
	if len(scope) > 0 {
		return scope[0]
	}
	return a.ctx.CurrentProject()
}
func main() { sdk.Run(&App{}) }
