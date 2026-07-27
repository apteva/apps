package main

import (
	_ "embed"
	"errors"
	"net/http"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

type App struct {
	ctx      *sdk.AppCtx
	store    *store
	cipher   *tokenCipher
	provider pushProvider
}

func (a *App) Manifest() sdk.Manifest {
	manifest, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *manifest
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("Push requires its app database")
	}
	cipher, err := newTokenCipher(ctx.Config().Get("token_encryption_key"))
	if err != nil {
		return err
	}
	a.ctx = ctx
	a.store = &store{db: ctx.AppDB()}
	a.cipher = cipher
	if a.provider == nil {
		a.provider = apnsProvider{}
	}
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error { return nil }
func (a *App) MCPTools() []sdk.Tool        { return nil }
func (a *App) Channels() []sdk.ChannelFactory {
	return nil
}
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: http.MethodGet, Pattern: "/health", Handler: a.handleHealth},
		{Method: http.MethodPost, Pattern: "/v1/devices/register", Handler: a.handleRegister, NoAuth: true},
		{Method: http.MethodDelete, Pattern: "/v1/devices/{id}", Handler: a.handleDeleteDevice, NoAuth: true},
		{Method: http.MethodPost, Pattern: "/v1/devices/{id}/test", Handler: a.handleTestDevice, NoAuth: true},
		{Method: http.MethodPost, Pattern: "/v1/deliveries", Handler: a.handleCreateDelivery, NoAuth: true},
		{Method: http.MethodGet, Pattern: "/v1/deliveries/{id}", Handler: a.handleGetDelivery, NoAuth: true},
		{Method: http.MethodGet, Pattern: "/stats", Handler: a.handleStats},
		{Method: http.MethodGet, Pattern: "/devices", Handler: a.handleListDevices},
		{Method: http.MethodGet, Pattern: "/deliveries", Handler: a.handleListDeliveries},
		{Method: http.MethodDelete, Pattern: "/admin/devices/{id}", Handler: a.handleAdminDeleteDevice},
		{Method: http.MethodPost, Pattern: "/admin/devices/{id}/test", Handler: a.handleAdminTestDevice},
	}
}

func main() {
	sdk.Run(&App{})
}
