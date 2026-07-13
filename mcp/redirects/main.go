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
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ─────────────────────────────────────────────

//go:embed apteva.yaml
var manifestYAML string

// ─── App ───────────────────────────────────────────────────────────

type App struct {
	hitQueue chan int64
	hitStop  chan struct{}
	hitWG    sync.WaitGroup
	stopOnce sync.Once
}

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
	a.hitQueue = make(chan int64, 4096)
	a.hitStop = make(chan struct{})
	a.hitWG.Add(1)
	go a.runHitCounter(ctx)
	ctx.Logger().Info("redirects mounted", "data_dir", ctx.DataDir())
	go reconcileRegisteredRoutes(ctx)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	a.stopOnce.Do(func() {
		if a.hitStop != nil {
			close(a.hitStop)
		}
	})
	a.hitWG.Wait()
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&App{}) }

func (a *App) enqueueHit(ctx *sdk.AppCtx, rule *Redirect) {
	if rule == nil {
		return
	}
	if a.hitQueue != nil {
		select {
		case a.hitQueue <- rule.ID:
		default:
			ctx.Logger().Warn("redirect hit queue full; dropping analytics increment", "id", rule.ID)
		}
	}
	if shouldEmitHit(rule.ID) {
		emitHit(ctx, rule)
	}
}

func (a *App) runHitCounter(ctx *sdk.AppCtx) {
	defer a.hitWG.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	pending := make(map[int64]int64)
	flush := func() {
		if len(pending) == 0 {
			return
		}
		batch := pending
		pending = make(map[int64]int64)
		if err := dbRecordHits(ctx.AppDB(), batch); err != nil {
			ctx.Logger().Warn("record redirect hits", "rules", len(batch), "err", err.Error())
		}
	}
	for {
		select {
		case id := <-a.hitQueue:
			pending[id]++
			if len(pending) >= 256 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-a.hitStop:
			for {
				select {
				case id := <-a.hitQueue:
					pending[id]++
				default:
					flush()
					return
				}
			}
		}
	}
}

// ─── HTTP routes ───────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Admin / panel surface — auth required.
		{Pattern: "/api/_meta", Handler: a.handleMeta},
		{Pattern: "/api/redirects", Handler: a.handleRedirectsCollection},
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
