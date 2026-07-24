// provider.go — TunnelProvider interface + active-provider picker.
//
// The Provider strategy separates Cloudflare quick/named and ngrok
// provider strategy so future transports (self-vps, ngrok,
// tailscale-funnel) plug in behind one interface. Each provider owns
// the lifecycle of "expose a public URL pointing at the local apteva
// instance" — it decides what to spawn, what state to persist, what
// to tear down.
//
// v0.6.0 ships four providers:
//
//   - cloudflare-quick  : anonymous trycloudflare.com URL per start
//   - cloudflare-named  : stable URL on a CF zone the operator owns
//   - ngrok             : random or reserved ngrok URL
//   - zrok              : stable reserved name on zrok's public namespace
//
// They share one Manager because only one public tunnel may run per install.
// The operator's explicit selection is persisted in runtime_state; merely
// binding an integration never changes the active provider.

package main

import sdk "github.com/apteva/app-sdk"

// Provider is the v0.4 strategy interface. Each provider owns the
// "expose a public URL" intent end-to-end: validation, lifecycle
// (Start/Stop), and persistent-state cleanup (Destroy).
type Provider interface {
	// Name is the canonical provider identifier persisted to
	// runs.provider. Stable across restarts; never user-facing string.
	Name() string

	// Configured reports whether this provider's persistent state is
	// in place — i.e. whether it should be the "active" provider for
	// this install if asked. Evaluated at request time so the answer
	// reflects current DB state, not install-time config.
	Configured(ctx *sdk.AppCtx) bool

	// Start brings up the tunnel. Returns the resolved target URL on
	// success; the public URL may still be assigning asynchronously
	// (see Snapshot()).
	Start(ctx *sdk.AppCtx) (target string, err error)

	// Stop is a graceful shutdown of any local subprocess this
	// provider supervises. Persistent state (e.g. named_tunnels row)
	// is preserved — restart picks up where it left off. Idempotent.
	Stop() error

	// Destroy reverses persistent state. For cloudflare-named that's
	// the CF tunnel + CNAME + the named_tunnels row. For quick
	// (no persistent state) it's a no-op. Returns whether anything
	// was actually destroyed. Refuses while the tunnel is up — caller
	// must Stop first.
	Destroy(ctx *sdk.AppCtx) (bool, error)

	// Snapshot is the current lifecycle state of the supervised
	// subprocess. Same shape across providers so the panel doesn't
	// have to special-case each one.
	Snapshot() Snapshot
}

// activeProvider resolves the operator's explicit runtime_state selection.
// Merely binding an optional integration must not silently switch providers.
func (a *App) activeProvider(ctx *sdk.AppCtx) Provider {
	selected := providerNameQuick
	if ctx != nil && ctx.AppDB() != nil {
		if state, err := dbRuntimeState(ctx.AppDB()); err == nil && state != nil && validProviderName(state.ActiveProvider) {
			selected = state.ActiveProvider
		}
	}
	for _, p := range a.providers {
		if p.Name() == selected {
			return p
		}
	}
	for _, p := range a.providers {
		if p.Name() == providerNameQuick {
			return p
		}
	}
	return nil
}

// activeProviderName is a string-only convenience for callers that
// just need the label (e.g. /status response).
func (a *App) activeProviderName(ctx *sdk.AppCtx) string {
	if p := a.activeProvider(ctx); p != nil {
		return p.Name()
	}
	return providerNameQuick
}

// Provider name constants. Kept as strings (not a Go enum) so DB
// values, MCP responses, and panel labels can all reference them
// without a translation layer.
const (
	providerNameQuick = "cloudflare-quick"
	providerNameNamed = "cloudflare-named"
	providerNameZrok  = "zrok"
)
