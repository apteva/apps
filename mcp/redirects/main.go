// Apteva Redirects v0.1.0 — branded short links and domain redirects.
//
// One sidecar per install. The catch-all HTTP handler on `/` looks up
// the inbound (Host, Path) in the redirects table and answers with a
// 30x + Location header. The /api/redirects/* surface is the panel +
// agent REST mirror.
//
// Boundary with ingress: every redirect_add exposes its hostname via
// platform ingress so apteva-server reverse-proxies inbound HTTP here.
// TLS is owned by platform ingress through the route's cert_fqdn.
//
// Boundary with domains: when the hostname is registered in domains,
// redirect_add upserts a DNS record so DNS points at the platform.
// When it isn't managed here (user manages DNS elsewhere), we skip
// silently and just record the rule.
package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ─────────────────────────────────────────────

//go:embed apteva.yaml
var manifestYAML string

// ─── App ───────────────────────────────────────────────────────────

type App struct{}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("redirects requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("redirects mounted", "data_dir", ctx.DataDir())
	go reconcileRegisteredRoutes(ctx)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&App{}) }

func (a *App) recordHit(ctx *sdk.AppCtx, rule *Redirect, target string) error {
	if rule == nil {
		return errors.New("redirect rule required")
	}
	at := time.Now().UTC()
	counts, err := dbRecordHit(ctx.AppDB(), rule.ID, rule.ProjectID, at)
	if err != nil {
		return err
	}
	emitHit(ctx, rule, target, counts, at)
	return nil
}

// ─── HTTP routes ───────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Admin / panel surface — auth required.
		{Pattern: "/api/_meta", Handler: a.handleMeta},
		{Pattern: "/api/redirects", Handler: a.handleRedirectsCollection},
		{Pattern: "/api/redirects/stats", Handler: a.handleRedirectStats},
		{Pattern: "/api/redirects/test", Handler: a.handleRedirectTest},
		{Pattern: "/api/redirects/", Handler: a.handleRedirectItem},

		// Public catch-all that actually issues the 30x. Has to come
		// last; ServeMux longest-prefix routing keeps the /api/* paths
		// above this one. Public traffic from any browser, so NoAuth.
		{Pattern: "/", Handler: a.handlePublicRedirect, NoAuth: true},
	}
}

// ─── helpers shared with handlers + tools ──────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
	}
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg))
}
