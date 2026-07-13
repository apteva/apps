package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

type App struct{ svc *service }

func (a *App) Manifest() sdk.Manifest {
	manifest, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic(err)
	}
	return *manifest
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("evals requires a database")
	}
	if ctx.RuntimeAPI() == nil {
		return errors.New("runtime catalog API unavailable")
	}
	a.svc = &service{ctx: ctx, db: store{db: ctx.AppDB()}}
	ctx.Logger().Info("evals mounted", "project_id", ctx.CurrentProject())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{Name: "runner", Schedule: "@every 2s", Run: func(ctx context.Context, app *sdk.AppCtx) error { a.svc.ctx = app; return a.svc.runNext(ctx) }},
		{Name: "scheduler", Schedule: "@every 1m", Run: func(ctx context.Context, app *sdk.AppCtx) error { a.svc.ctx = app; return a.svc.schedule(ctx) }},
	}
}
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/api/suites", Handler: a.handleSuites}, {Pattern: "/api/suites/", Handler: a.handleSuite},
		{Pattern: "/api/cases", Handler: a.handleCases}, {Pattern: "/api/cases/", Handler: a.handleCase},
		{Pattern: "/api/experiments", Handler: a.handleExperiments}, {Pattern: "/api/experiments/", Handler: a.handleExperiment},
		{Pattern: "/api/runs/", Handler: a.handleRun}, {Pattern: "/api/catalog", Handler: a.handleCatalog},
		{Pattern: "/api/environments", Handler: a.handleEnvironments}, {Pattern: "/api/environment-tools/", Handler: a.handleEnvironmentTools},
		{Pattern: "/api/agent-capabilities/", Handler: a.handleAgentCapabilities},
		{Pattern: "/api/suggestions/", Handler: a.handleSuggestion},
	}
}

func main() { sdk.Run(&App{}) }

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func httpError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
