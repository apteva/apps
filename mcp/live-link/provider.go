// provider.go — TunnelProvider interface + active-provider picker.
//
// v0.4 collapses the v0.3 "Mode" enum (quick / named) into a real
// provider strategy so future transports (self-vps, ngrok,
// tailscale-funnel) plug in behind one interface. Each provider owns
// the lifecycle of "expose a public URL pointing at the local apteva
// instance" — it decides what to spawn, what state to persist, what
// to tear down.
//
// v0.4.0 ships two providers, both Cloudflare-shaped:
//
//   - cloudflare-quick  : anonymous trycloudflare.com URL per start
//   - cloudflare-named  : stable URL on a CF zone the operator owns
//
// They share one Manager (cloudflared subprocess supervisor) because
// they both spawn the same binary with different args. Future
// providers (self-vps's frpc, ngrok's ngrok client) bring their own
// lifecycle and don't share Manager.
//
// activeProvider(ctx) reads DB state to pick which provider is the
// install's current shape. Today: a named_tunnels row → named, else
// quick. v0.5 generalizes this to a per-install "active_provider"
// row when more than two providers are realistic.

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

// activeProvider picks which provider should handle this install's
// current request, based on DB state. Order is "most-specific first":
// any provider with Configured()=true wins. Falls back to the default
// (cloudflare-quick) when nothing is configured.
//
// Why pick on every call instead of caching once at OnMount: the panel
// can flip the install from quick → named (via /named/configure) or
// named → quick (via /destroy) at any time; the next request must
// reflect the new shape immediately.
func (a *App) activeProvider(ctx *sdk.AppCtx) Provider {
	// providers slice is ordered most-specific-first. quick is last
	// because it's the default fallback (Configured() always false
	// for quick — we use it only when nothing else matched).
	for _, p := range a.providers {
		if p.Name() == providerNameQuick {
			continue
		}
		if p.Configured(ctx) {
			return p
		}
	}
	// Default: quick. Look it up in the registry rather than holding
	// a separate field so the providers slice stays the single source
	// of truth.
	for _, p := range a.providers {
		if p.Name() == providerNameQuick {
			return p
		}
	}
	// Unreachable as long as App.init wires the quick provider in,
	// but guarded so a misconfigured test doesn't nil-panic.
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
)
