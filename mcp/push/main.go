package main

import (
	_ "embed"
	"errors"
	"fmt"
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
	// encryptionKey is set only by tests. Production always reads the
	// server-generated relay secret from the bound APNs connection.
	encryptionKey string
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
	encryptionKey := a.encryptionKey
	if encryptionKey == "" {
		var err error
		encryptionKey, err = connectionEncryptionKey(ctx)
		if err != nil {
			return err
		}
	}
	cipher, err := newTokenCipher(encryptionKey)
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

func connectionEncryptionKey(ctx *sdk.AppCtx) (string, error) {
	bound := ctx.IntegrationFor("ios_provider")
	if bound == nil || bound.ConnectionID == 0 {
		return "", errors.New("Apple Push Notifications integration is not connected")
	}
	credentials, err := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		return "", fmt.Errorf("read Apple Push Notifications connection credentials: %w", err)
	}
	if credentials == nil || credentials.Fields["relay_encryption_key"] == "" {
		return "", errors.New("Apple Push Notifications connection is missing its generated relay encryption key; recreate the connection with Apteva Server v0.6.62 or later")
	}
	return credentials.Fields["relay_encryption_key"], nil
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
