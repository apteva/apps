package main

import (
	_ "embed"
	"errors"
	"net/http"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

//go:embed skills/building-with-helper.md
var builderSkillBody string

type App struct {
	ctx   *sdk.AppCtx
	store *builderStore

	setupMu sync.RWMutex
	setup   SetupStatus

	// retryDelays is overridden by tests. Production uses a short bounded
	// activation reconciliation, not a recurring worker or heartbeat.
	retryDelays []time.Duration
}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded Builder manifest: " + err.Error())
	}
	for i := range m.Provides.Skills {
		if m.Provides.Skills[i].Name == "building-with-helper" {
			m.Provides.Skills[i].Body = builderSkillBody
			m.Provides.Skills[i].BodyFile = ""
		}
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx == nil || ctx.AppDB() == nil {
		return errors.New("builder requires its database")
	}
	a.ctx = ctx
	a.store = newBuilderStore(ctx.AppDB())
	a.setSetupStatus(SetupStatus{State: setupPending, UpdatedAt: nowUTC()})

	if api := ctx.AgentToolsAPI(); api != nil {
		go a.reconcileHelperLoop(ctx, api)
	} else {
		a.setSetupStatus(SetupStatus{
			State: setupUnsupported, LastError: "this platform does not expose AgentToolsAPI",
			UpdatedAt: nowUTC(),
		})
	}
	ctx.Logger().Info("builder mounted", "version", "0.2.2")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/goals", Handler: a.handleGoals},
		{Pattern: "/goals/", Handler: a.handleGoals},
		{Pattern: "/setup/status", Handler: a.handleSetupStatus},
		{Pattern: "/setup/reconcile", Handler: a.handleSetupReconcile},
	}
}

func (a *App) MCPTools() []sdk.Tool { return a.tools() }

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, a.currentSetupStatus())
}
