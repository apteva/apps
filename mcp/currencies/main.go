package main

import (
	"context"
	_ "embed"
	"errors"
	"net/http"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

var globalCtx *sdk.AppCtx

type App struct{}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("currencies requires a db block")
	}
	ctx.AppDB().SetMaxOpenConns(1)
	if err := seedCurrencyDefinitions(ctx.AppDB()); err != nil {
		return err
	}
	globalCtx = ctx
	ctx.Logger().Info("currencies mounted", "project_id", ctx.CurrentProject())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "currencies-refresh",
		Schedule: "@every 15m",
		Run: func(ctx context.Context, app *sdk.AppCtx) error {
			return a.refreshTrackedPairs(ctx, app)
		},
	}}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: http.MethodGet, Pattern: "/currencies", Handler: a.handleCurrencies},
		{Method: http.MethodGet, Pattern: "/rates", Handler: a.handleRates},
		{Method: http.MethodGet, Pattern: "/history", Handler: a.handleHistory},
		{Method: http.MethodPost, Pattern: "/convert", Handler: a.handleConvert},
		{Method: http.MethodPost, Pattern: "/manual-rate", Handler: a.handleManualRate},
		{Method: http.MethodGet, Pattern: "/sources", Handler: a.handleSources},
		{Method: http.MethodPost, Pattern: "/sync", Handler: a.handleSync},
	}
}
