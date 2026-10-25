package main

import (
	"errors"
	"net/url"
	"sort"

	sdk "github.com/apteva/app-sdk"
)

const runtimeDashboardRegistration = "telephony-runtime"

// Called only by the active owner. Replaces (rather than appends) exact HTTPS
// destinations, so moving back to local execution removes the old registration.
// Never clear this on unmount: a replacement may already own the installation.
func (a *App) registerRuntimeDashboard(api sdk.DashboardConnectClient) error {
	if api == nil {
		return nil
	}
	origins := []string{}
	seen := map[string]bool{}
	if a.runtime != nil {
		if !a.runtimeInitializationAllowed() {
			return errRuntimeAdmission
		}
		for _, name := range []string{"browser", "media", "signaling"} {
			raw := a.runtimeBrowserBase(name)
			if raw == "" {
				continue
			}
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return errors.New("invalid runtime dashboard destination")
			}
			if u.Scheme != "https" {
				if u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1") {
					continue
				}
				return errors.New("runtime dashboard destination requires HTTPS")
			}
			origin := u.Scheme + "://" + u.Host
			if !seen[origin] {
				seen[origin] = true
				origins = append(origins, origin)
			}
		}
	}
	sort.Strings(origins)
	_, err := api.ReplaceDashboardConnectOrigins(runtimeDashboardRegistration, origins)
	return err
}
