package main

import (
	"context"
	_ "embed"
	"errors"
	"net/http"
	"sync"

	sdk "github.com/apteva/app-sdk"
	gql "github.com/graphql-go/graphql"
	"github.com/vektah/gqlparser/v2/ast"
)

//go:embed apteva.yaml
var manifestYAML []byte

// App is deliberately independent from the API gateway app. It owns the
// GraphQL HTTP/WebSocket surface and calls source apps through PlatformAPI.
type App struct {
	httpClient      *http.Client
	hub             *subscriptionHub
	ctx             *sdk.AppCtx
	cacheMu         sync.RWMutex
	cacheGeneration map[string]uint64
	apiCache        map[string]*graphqlAPI
	securityCache   map[string]securityPolicy
	releaseCache    map[string]*apiRelease
	schemaRowCache  map[string]*schemaRecord
	schemaCache     map[string]*ast.Schema
	queryCache      map[string]*ast.QueryDocument
	operationCache  map[string]*preparedOperation
	planCache       map[string]planCacheEntry
	runtimeCache    map[string]*gql.Schema
	logQueue        chan requestLogEntry
	logStop         chan struct{}
	logDone         chan struct{}
	logStopOnce     sync.Once
}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("graphql requires a db block")
	}
	if a.httpClient == nil {
		a.httpClient = http.DefaultClient
	}
	a.ctx = ctx
	a.hub = newSubscriptionHub()
	a.cacheGeneration = make(map[string]uint64)
	a.apiCache = make(map[string]*graphqlAPI)
	a.securityCache = make(map[string]securityPolicy)
	a.releaseCache = make(map[string]*apiRelease)
	a.schemaRowCache = make(map[string]*schemaRecord)
	a.schemaCache = make(map[string]*ast.Schema)
	a.queryCache = make(map[string]*ast.QueryDocument)
	a.operationCache = make(map[string]*preparedOperation)
	a.planCache = make(map[string]planCacheEntry)
	a.runtimeCache = make(map[string]*gql.Schema)
	if project := ctx.CurrentProject(); project != "" {
		if _, err := ensureDefaultGraphQLAPI(ctx.AppDB(), project); err != nil {
			return err
		}
	}
	a.startRequestLogger()
	ctx.Logger().Info("graphql mounted", "project_id", ctx.CurrentProject())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.hub != nil {
		a.hub.close()
	}
	a.stopRequestLogger()
	return nil
}

func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker          { return nil }
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{
		{Event: "row.inserted", Handler: a.handleSourceEvent},
		{Event: "row.updated", Handler: a.handleSourceEvent},
		{Event: "row.deleted", Handler: a.handleSourceEvent},
	}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/graphql", Handler: a.handleGraphQL},
		{Pattern: "/graphql/", Handler: a.handleGraphQL},
		{Pattern: "/public/graphql/", Handler: a.handlePublicGraphQL, NoAuth: true},
		{Pattern: "/realtime", Handler: a.handleRealtime},
		{Pattern: "/public/realtime/", Handler: a.handleRealtime, NoAuth: true},
		{Pattern: "/admin/", Handler: a.handleAdminHTTP},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return graphqlTools(a)
}

func appProject(ctx *sdk.AppCtx, callCtx context.Context, args map[string]any) (string, error) {
	if caller := sdk.CallerFrom(callCtx); caller != nil && caller.ProjectID != "" {
		if v, ok := args["project_id"].(string); ok && v != "" && v != caller.ProjectID {
			return "", forbidden("project override is not allowed")
		}
		return caller.ProjectID, nil
	}
	if ctx != nil && ctx.CurrentProject() != "" {
		return ctx.CurrentProject(), nil
	}
	if v, ok := args["project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", invalid("project_id is required")
}
