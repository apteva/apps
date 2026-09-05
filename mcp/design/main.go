package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

type App struct {
	ctx           *sdk.AppCtx
	store         *Store
	engine        *Engine
	artifactRoot  string
	maxOperations int
}

func (a *App) Manifest() sdk.Manifest {
	manifest, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *manifest
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("design requires a db block")
	}
	if ctx.CurrentProject() == "" {
		return errors.New("design requires a project-scoped install")
	}
	dataDir := ctx.DataDir()
	if dataDir == "" {
		return errors.New("design requires a persistent app data directory")
	}
	timeout := configInt(ctx, "geometry_timeout_seconds", 45, 5, 300)
	maxOperations := configInt(ctx, "max_operations", 256, 1, 256)
	runtimeRoot := filepath.Join(dataDir, "geometry-runtime")
	engine, err := NewEngine(runtimeRoot, ctx.Config().Get("bun_path"), time.Duration(timeout)*time.Second)
	if err != nil {
		return fmt.Errorf("initialize geometry engine: %w", err)
	}
	artifactRoot := filepath.Join(dataDir, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		return fmt.Errorf("create artifact directory: %w", err)
	}
	a.ctx = ctx
	a.store = NewStore(ctx.AppDB())
	a.engine = engine
	a.artifactRoot = artifactRoot
	a.maxOperations = maxOperations
	ctx.Logger().Info("design mounted", "engine", engineVersion, "max_operations", maxOperations, "timeout_seconds", timeout)
	return nil
}

func configInt(ctx *sdk.AppCtx, name string, fallback, min, max int) int {
	value, err := strconv.Atoi(ctx.Config().Get(name))
	if err != nil || value < min || value > max {
		return fallback
	}
	return value
}

func (a *App) service(ctx *sdk.AppCtx) (*Service, error) {
	if a.store == nil || a.engine == nil {
		return nil, errors.New("design is not mounted")
	}
	if ctx == nil {
		ctx = a.ctx
	}
	project := ctx.CurrentProject()
	if project == "" {
		return nil, errors.New("project context required")
	}
	return &Service{
		store: a.store, engine: a.engine, ctx: ctx, project: project,
		artifactRoot: a.artifactRoot, maxOperations: a.maxOperations,
	}, nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/api/designs", Handler: a.handleDesigns},
		{Pattern: "/api/designs/", Handler: a.handleDesign},
		{Pattern: "/api/revisions/", Handler: a.handleRevision},
		{Pattern: "/api/artifacts/", Handler: a.handleArtifact},
		{Pattern: "/api/examples", Handler: a.handleExamples},
		{Pattern: "/api/pcb-source", Handler: a.handlePCBSource},
		{Pattern: "/api/pcb-enclosures", Handler: a.handlePCBEnclosures},
		{Pattern: "/api/assemblies", Handler: a.handleAssemblies},
	}
}

func main() { sdk.Run(&App{}) }
