package main

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const browserOriginRegistrationPrefix = "oauth-client-"

func browserOriginRegistrationKey(clientID string) string {
	return browserOriginRegistrationPrefix + clientID
}

// replaceClientBrowserOrigins reconciles one active OAuth client's complete
// desired origin set. The capability is optional so existing test doubles and
// custom AppCtx implementations without it remain compatible.
func replaceClientBrowserOrigins(ctx *sdk.AppCtx, client Client) (bool, error) {
	if ctx.BrowserOriginsAPI() == nil {
		return false, nil
	}
	_, err := ctx.ReplaceBrowserOrigins(
		browserOriginRegistrationKey(client.ClientID),
		client.AllowedOrigins,
	)
	return true, err
}

func deleteClientBrowserOrigins(ctx *sdk.AppCtx, clientID string) (bool, error) {
	if ctx.BrowserOriginsAPI() == nil {
		return false, nil
	}
	return true, ctx.DeleteBrowserOrigins(browserOriginRegistrationKey(clientID))
}

// reconcileBrowserOrigins makes Auth's client table authoritative at mount:
// every active client is replaced under a stable key, then stale Auth-owned
// registrations are deleted. All clients are attempted before errors return.
func reconcileBrowserOrigins(ctx *sdk.AppCtx, projectID string) error {
	if ctx.BrowserOriginsAPI() == nil || projectID == "" {
		return nil
	}

	clients, err := dbListClients(ctx.AppDB(), projectID, 0, true)
	if err != nil {
		return fmt.Errorf("list OAuth clients: %w", err)
	}
	registrations, listErr := ctx.ListBrowserOriginRegistrations()

	desired := make(map[string]struct{}, len(clients))
	var errs []error
	if listErr != nil {
		errs = append(errs, fmt.Errorf("list browser-origin registrations: %w", listErr))
	}
	for _, client := range clients {
		if client.DisabledAt != "" {
			continue
		}
		key := browserOriginRegistrationKey(client.ClientID)
		desired[key] = struct{}{}
		if _, err := ctx.ReplaceBrowserOrigins(key, client.AllowedOrigins); err != nil {
			errs = append(errs, fmt.Errorf("replace %s: %w", key, err))
		}
	}

	// Never clean up from an incomplete listing, and never touch another
	// registration family that Auth may add later.
	if listErr == nil {
		for _, registration := range registrations {
			if !strings.HasPrefix(registration.Key, browserOriginRegistrationPrefix) {
				continue
			}
			if _, ok := desired[registration.Key]; ok {
				continue
			}
			if err := ctx.DeleteBrowserOrigins(registration.Key); err != nil {
				errs = append(errs, fmt.Errorf("delete stale %s: %w", registration.Key, err))
			}
		}
	}
	return errors.Join(errs...)
}

func recordBrowserOriginSync(out map[string]any, attempted bool, err error) {
	if !attempted {
		return
	}
	out["browser_origins_synced"] = err == nil
	if err != nil {
		out["browser_origins_error"] = err.Error()
	}
}
