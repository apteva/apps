package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

type App struct {
	ctx          *sdk.AppCtx
	store        *Store
	artifactRoot string
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("PCB Studio requires a db block")
	}
	if ctx.CurrentProject() == "" {
		return errors.New("PCB Studio requires a project-scoped install")
	}
	root := ctx.Config().Get("artifact_dir")
	if root == "" {
		if ctx.DataDir() == "" {
			return errors.New("PCB Studio requires a persistent data directory")
		}
		root = filepath.Join(ctx.DataDir(), "artifacts")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create artifact directory: %w", err)
	}
	a.ctx = ctx
	a.store = NewStore(ctx.AppDB())
	a.artifactRoot = root
	ctx.Logger().Info("PCB Studio mounted", "engine", engineVersion, "schema", pcbSchema)
	return nil
}
func (a *App) service(ctx *sdk.AppCtx) (*Service, error) {
	if a.store == nil {
		return nil, errors.New("PCB Studio is not mounted")
	}
	if ctx == nil {
		ctx = a.ctx
	}
	if ctx == nil || ctx.CurrentProject() == "" {
		return nil, errors.New("project context required")
	}
	return &Service{store: a.store, ctx: ctx, project: ctx.CurrentProject(), artifactRoot: a.artifactRoot}, nil
}
func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{{Pattern: "/api/designs", Handler: a.handleDesigns}, {Pattern: "/api/designs/", Handler: a.handleDesign}, {Pattern: "/api/revisions/", Handler: a.handleRevision}, {Pattern: "/api/artifacts/", Handler: a.handleArtifact}, {Pattern: "/api/examples", Handler: a.handleExamples}, {Pattern: "/api/providers", Handler: a.handleProviders}}
}
func main() { sdk.Run(&App{}) }
