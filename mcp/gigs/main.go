// Gigs — agent→human work assignment with a composable instruction
// library. See README.md and apteva.yaml for the full surface.
package main

import (
	_ "embed"
	"errors"
	"net/http"
	"os"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

// globalCtx stashes the AppCtx for HTTP handlers — the SDK's http
// signature is (w, r) only, so handlers reach the platform client +
// DB via this package var. Same pattern as CRM/storage/etc.
var globalCtx *sdk.AppCtx

func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("gigs requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("gigs mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }

func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{
		// Reply-based submissions: when a worker replies on the CRM
		// thread we opened for their gig, we try to parse the message
		// as a submission. Falls back to a "please open the link"
		// reply when the result schema needs structured fields.
		{
			Topic:   "contact.message_received",
			Handler: a.handleContactMessageReceived,
		},
	}
}

// HTTPRoutes registers everything reverse-proxied under
// /api/apps/gigs/*. The dashboard panel hits these; the worker page
// is served from /worker/<token> (the dashboard never sees it).
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Health.
		{Pattern: "/health", Handler: a.handleHealth},

		// Worker magic-link page + submission. NoAuth: the magic_token
		// in the path is the auth. Reachable directly so workers don't
		// need an Apteva login.
		{Pattern: "/worker/", Handler: a.handleWorkerRoot, NoAuth: true},

		// Workers.
		{Pattern: "/workers", Handler: a.handleHTTPWorkersCollection},
		{Pattern: "/workers/", Handler: a.handleHTTPWorkerItem},
		{Pattern: "/skills", Handler: a.handleHTTPSkills},

		// Instructions.
		{Pattern: "/instructions", Handler: a.handleHTTPInstructionsCollection},
		{Pattern: "/instructions/", Handler: a.handleHTTPInstructionItem},

		// Templates.
		{Pattern: "/templates", Handler: a.handleHTTPTemplatesCollection},
		{Pattern: "/templates/", Handler: a.handleHTTPTemplateItem},

		// Gigs.
		{Pattern: "/gigs", Handler: a.handleHTTPGigsCollection},
		{Pattern: "/gigs/", Handler: a.handleHTTPGigItem},
	}
}

func (a *App) handleHealth(w http.ResponseWriter, _ *http.Request) {
	httpJSON(w, map[string]any{"ok": true, "app": "gigs", "version": "0.1.0"})
}

// MCPTools aggregates every tool defined across the surface files.
func (a *App) MCPTools() []sdk.Tool {
	var out []sdk.Tool
	out = append(out, a.workerTools()...)
	out = append(out, a.instructionTools()...)
	out = append(out, a.templateTools()...)
	out = append(out, a.gigTools()...)
	return out
}

func main() { sdk.Run(&App{}) }
