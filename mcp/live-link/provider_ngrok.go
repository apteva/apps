// provider_ngrok.go — ngrok provider for live-link.
//
// Mirrors cloudflareQuickProvider's shape (no persistent state, fresh
// public hostname per start on the free tier) but spawns the ngrok
// agent instead of cloudflared and reads the authtoken from the
// bound ngrok integration's credentials.
//
// Two future polish items deferred from v0.5.0:
//   - Reserved-domain support: paid ngrok plans can pin a hostname.
//     We pass StartParams.Hostname through as --domain when set, but
//     the panel doesn't yet have a "reserved domain" field. Operators
//     who want it can set live-link's `ngrok_domain` config; the
//     provider picks it up automatically.
//   - Region selection: ngrok supports --region us|eu|ap|au|sa|jp|in.
//     Defaults to ngrok's automatic region for now.

package main

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const providerNameNgrok = "ngrok"

type ngrokProvider struct {
	app *App
}

func (p *ngrokProvider) Name() string       { return providerNameNgrok }
func (p *ngrokProvider) Snapshot() Snapshot { return p.app.mgr.Snapshot() }
func (p *ngrokProvider) Stop() error        { return p.app.mgr.Stop() }

// Configured is true when the operator has bound the ngrok integration
// on this install. Same shape as cloudflareNamedProvider's "row exists"
// check but for the integration binding instead of a local DB row.
func (p *ngrokProvider) Configured(ctx *sdk.AppCtx) bool {
	if ctx == nil {
		return false
	}
	bound := ctx.IntegrationFor("ngrok")
	return bound != nil && bound.ConnectionID != 0
}

// Destroy is a no-op for ngrok — there's no persistent state on the
// app's side (no DB row, no upstream resource to revoke). The
// operator removes the ngrok integration binding via the platform if
// they want this provider to stop being Configured.
func (p *ngrokProvider) Destroy(_ *sdk.AppCtx) (bool, error) { return false, nil }

func (p *ngrokProvider) Start(ctx *sdk.AppCtx) (string, error) {
	target := p.app.resolveTargetURL(ctx)
	if target == "" {
		return "", errors.New("no target URL — set target_url in app config or APTEVA_GATEWAY_URL in the env")
	}

	bound := ctx.IntegrationFor("ngrok")
	if bound == nil || bound.ConnectionID == 0 {
		return "", errors.New("ngrok integration not bound — bind your ngrok authtoken on this install first")
	}
	creds, err := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		return "", fmt.Errorf("read ngrok credentials: %w", err)
	}
	authtoken := strings.TrimSpace(credValue(creds, "authtoken"))
	if authtoken == "" {
		return "", errors.New("ngrok integration is bound but the authtoken is empty — re-bind with a valid agent authtoken")
	}

	binary, err := resolveNgrokBinary(ctx.Config().Get("ngrok_path"), ctx.DataDir(), false, ctx.Logger().Info)
	if err != nil {
		return "", err
	}

	// Reserved domain (paid plans). Optional. Setting this short-
	// circuits ngrok's random hostname assignment and pins URLs across
	// agent restarts — useful for webhook receivers that can't tolerate
	// the trycloudflare-style URL churn.
	domain := strings.TrimSpace(ctx.Config().Get("ngrok_domain"))

	runID, err := dbInsertRun(ctx.AppDB(), p.Name(), target, string(ModeNgrok))
	if err != nil {
		return "", fmt.Errorf("insert run: %w", err)
	}
	params := StartParams{
		Binary:    binary,
		Target:    target,
		Mode:      ModeNgrok,
		RunID:     runID,
		Authtoken: authtoken,
		Hostname:  domain, // optional; empty = ngrok auto-assigns
	}
	if err := p.app.mgr.Start(params); err != nil {
		_, _ = ctx.AppDB().Exec(
			`UPDATE runs SET status = 'failed', finished_at = CURRENT_TIMESTAMP, exit_reason = ?
			 WHERE id = ?`, err.Error(), runID)
		return "", err
	}
	return target, nil
}

// credValue fetches a credential field by name. ConnectionCredentials
// puts non-OAuth fields under .Fields; this helper keeps callers from
// repeating the nil-check.
func credValue(c *sdk.ConnectionCredentials, name string) string {
	if c == nil || c.Fields == nil {
		return ""
	}
	return c.Fields[name]
}
