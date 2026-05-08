// provider_cloudflare.go — the two Cloudflare-shaped providers.
//
// Both wrap the shared Manager (cloudflared subprocess supervisor)
// because they both spawn `cloudflared tunnel ...` with different
// flag sets. Anything Cloudflare-API-specific (zones, cfd_tunnel,
// CNAME upsert) lives on the named provider; quick has no API
// surface to manage.
//
// Refactor strategy: this file delegates to the App's existing
// methods (resolveTargetURL, ensureNamedTunnel, destroyNamedTunnel,
// cfConnectionID, dbInsertRun, …) rather than absorbing them. That
// keeps the slice-1 diff small and leaves the existing test suite
// hitting App methods unchanged. Future slices can pull more logic
// down into the providers if it makes sense.

package main

import (
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// ─── cloudflareQuickProvider ────────────────────────────────────────
//
// Anonymous trycloudflare.com URL on every start. Zero persistent
// state — the URL changes every run by design. Configured() is always
// false: quick is the fallback when no other provider claims the
// install, never the actively-configured one.

type cloudflareQuickProvider struct {
	app *App
}

func (p *cloudflareQuickProvider) Name() string                          { return providerNameQuick }
func (p *cloudflareQuickProvider) Configured(_ *sdk.AppCtx) bool         { return false }
func (p *cloudflareQuickProvider) Snapshot() Snapshot                    { return p.app.mgr.Snapshot() }
func (p *cloudflareQuickProvider) Stop() error                           { return p.app.mgr.Stop() }
func (p *cloudflareQuickProvider) Destroy(_ *sdk.AppCtx) (bool, error)   { return false, nil }

func (p *cloudflareQuickProvider) Start(ctx *sdk.AppCtx) (string, error) {
	target := p.app.resolveTargetURL(ctx)
	if target == "" {
		return "", errors.New("no target URL — set target_url in app config or APTEVA_GATEWAY_URL in the env")
	}
	binary, err := resolveBinary(ctx.Config().Get("cloudflared_path"), ctx.DataDir(), false, ctx.Logger().Info)
	if err != nil {
		return "", err
	}

	runID, err := dbInsertRun(ctx.AppDB(), p.Name(), target, "quick")
	if err != nil {
		return "", fmt.Errorf("insert run: %w", err)
	}
	params := StartParams{
		Binary: binary, Target: target, Mode: ModeQuick, RunID: runID,
	}
	if err := p.app.mgr.Start(params); err != nil {
		_, _ = ctx.AppDB().Exec(
			`UPDATE runs SET status = 'failed', finished_at = CURRENT_TIMESTAMP, exit_reason = ?
			 WHERE id = ?`, err.Error(), runID)
		return "", err
	}
	return target, nil
}

// ─── cloudflareNamedProvider ────────────────────────────────────────
//
// Stable URL on a CF zone the operator owns. Persistent state lives
// in the named_tunnels table. Configured() returns true iff a row
// exists.

type cloudflareNamedProvider struct {
	app *App
}

func (p *cloudflareNamedProvider) Name() string       { return providerNameNamed }
func (p *cloudflareNamedProvider) Snapshot() Snapshot { return p.app.mgr.Snapshot() }
func (p *cloudflareNamedProvider) Stop() error        { return p.app.mgr.Stop() }

func (p *cloudflareNamedProvider) Configured(ctx *sdk.AppCtx) bool {
	if ctx == nil || ctx.AppDB() == nil {
		return false
	}
	nt, _ := dbFirstNamedTunnel(ctx.AppDB())
	return nt != nil
}

func (p *cloudflareNamedProvider) Start(ctx *sdk.AppCtx) (string, error) {
	target := p.app.resolveTargetURL(ctx)
	if target == "" {
		return "", errors.New("no target URL — set target_url in app config or APTEVA_GATEWAY_URL in the env")
	}
	binary, err := resolveBinary(ctx.Config().Get("cloudflared_path"), ctx.DataDir(), false, ctx.Logger().Info)
	if err != nil {
		return "", err
	}

	nt, err := dbFirstNamedTunnel(ctx.AppDB())
	if err != nil {
		return "", fmt.Errorf("look up named tunnel: %w", err)
	}
	if nt == nil {
		return "", errors.New("named mode: no tunnel configured — pick a hostname in the Live Link panel first")
	}

	runID, err := dbInsertRun(ctx.AppDB(), p.Name(), target, "named")
	if err != nil {
		return "", fmt.Errorf("insert run: %w", err)
	}
	params := StartParams{
		Binary:   binary,
		Target:   target,
		Mode:     ModeNamed,
		RunID:    runID,
		Token:    nt.TunnelToken,
		Hostname: nt.Hostname,
	}
	if err := p.app.mgr.Start(params); err != nil {
		_, _ = ctx.AppDB().Exec(
			`UPDATE runs SET status = 'failed', finished_at = CURRENT_TIMESTAMP, exit_reason = ?
			 WHERE id = ?`, err.Error(), runID)
		return "", err
	}
	return target, nil
}

// Destroy delegates to the App's destroyNamedTunnel — the heavy
// lifting (CF API calls + DB row removal) is shared with the v0.3
// /destroy HTTP route's implementation. We do NOT check "tunnel is
// running" here; the HTTP/MCP wrappers do that and surface a 409.
// Calling Destroy while running just fails at the CF "delete tunnel
// while in-use" upstream error, which is also fine.
func (p *cloudflareNamedProvider) Destroy(ctx *sdk.AppCtx) (bool, error) {
	return p.app.destroyNamedTunnel(ctx)
}
