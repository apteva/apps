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
	legacyBundleID := ""
	legacyEnvironment := ""
	if encryptionKey == "" {
		settings, err := readConnectionSettings(ctx)
		if err != nil {
			return err
		}
		encryptionKey = settings.EncryptionKey
		legacyBundleID = settings.LegacyBundleID
		legacyEnvironment = settings.LegacyEnvironment
	}
	cipher, err := newTokenCipher(encryptionKey)
	if err != nil {
		return err
	}
	a.ctx = ctx
	a.store = &store{db: ctx.AppDB()}
	a.cipher = cipher
	if err := a.store.backfillDeviceRouting(legacyBundleID, legacyEnvironment); err != nil {
		return fmt.Errorf("backfill device APNs routing: %w", err)
	}
	if a.provider == nil {
		a.provider = apnsProvider{}
	}
	return nil
}

type connectionSettings struct {
	EncryptionKey     string
	LegacyBundleID    string
	LegacyEnvironment string
}

func readConnectionSettings(ctx *sdk.AppCtx) (*connectionSettings, error) {
	bound := ctx.IntegrationFor("ios_provider")
	if bound == nil || bound.ConnectionID == 0 {
		return nil, errors.New("Apple Push Notifications integration is not connected")
	}
	credentials, err := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("read Apple Push Notifications connection credentials: %w", err)
	}
	if credentials == nil || credentials.Fields["relay_encryption_key"] == "" {
		return nil, errors.New("Apple Push Notifications connection is missing its generated relay encryption key")
	}
	return &connectionSettings{
		EncryptionKey:     credentials.Fields["relay_encryption_key"],
		LegacyBundleID:    credentials.Fields["bundle_id"],
		LegacyEnvironment: credentials.Fields["environment"],
	}, nil
}

func connectionEncryptionKey(ctx *sdk.AppCtx) (string, error) {
	settings, err := readConnectionSettings(ctx)
	if err != nil {
		return "", err
	}
	return settings.EncryptionKey, nil
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
