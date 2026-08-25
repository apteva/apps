package main

import (
	"context"
	_ "embed"
	"errors"
	"net/http"
	"time"

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
	if configBool(ctx, "ecb_bootstrap_enabled", true) {
		bootstrapCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		created, latest, err := a.refreshECBIfDue(bootstrapCtx, ctx, false)
		cancel()
		if err != nil {
			// Public reference data is a convenience bootstrap. The app must
			// remain installable and usable with manual/cached rates when ECB is
			// unavailable.
			ctx.Logger().Warn("ECB reference-rate bootstrap failed", "error", err)
		} else if latest != "" {
			ctx.Logger().Info("ECB reference rates ready", "created", created, "latest", latest)
		}
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
			var failures []error
			if _, _, err := a.refreshECBIfDue(ctx, app, false); err != nil {
				failures = append(failures, err)
			}
			if err := a.refreshTrackedPairs(ctx, app); err != nil {
				failures = append(failures, err)
			}
			return errors.Join(failures...)
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
