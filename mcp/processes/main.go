package main

import (
	"database/sql"
	_ "embed"
	"errors"
	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
	"sync"
)

//go:embed apteva.yaml
var manifestYAML string

//go:embed skills/how-to-use-processes.md
var skillBody string

type App struct {
	ctx           *sdk.AppCtx
	db            *sql.DB
	mu            sync.Mutex
	eventMu       sync.Mutex
	cleanupMu     sync.Mutex
	waitMu        sync.Mutex
	asyncDispatch bool
	dispatcher    *processDispatcher
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
	if a.asyncDispatch {
		a.dispatcher = newProcessDispatcher(a)
		go a.dispatcher.run()
	}
	return nil
}
func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.dispatcher != nil {
		a.dispatcher.cancel()
		<-a.dispatcher.done
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker          { return nil }

// Dispatch always supplies a visible project. An empty scope must never turn
// into an installation-wide scan.
func (a *App) workerProject(scope []string) string {
	if len(scope) > 0 {
		return scope[0]
	}
	return a.ctx.CurrentProject()
}
func main() { sdk.Run(&App{asyncDispatch: true}) }
