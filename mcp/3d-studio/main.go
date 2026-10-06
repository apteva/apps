package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

type App struct {
	ctx    *sdk.AppCtx
	store  *Store
	editMu sync.Mutex
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic(err)
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil || ctx.CurrentProject() == "" {
		return errors.New("3D Studio requires a project-scoped database")
	}
	a.ctx = ctx
	ctx.AppDB().SetMaxOpenConns(1)
	a.store = &Store{ctx.AppDB()}
	return nil
}
func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{{Pattern: "/api/tools/", Handler: a.handleTool}, {Pattern: "/api/artifacts/", Handler: a.handleArtifact}}
}
func (a *App) project(ctx *sdk.AppCtx) (string, error) {
	if a.store == nil || ctx == nil || ctx.CurrentProject() == "" {
		return "", errors.New("project context required")
	}
	if a.ctx != nil && a.ctx.CurrentProject() != ctx.CurrentProject() {
		return "", errNotFound
	}
	return ctx.CurrentProject(), nil
}
func (a *App) httpProject(r *http.Request) (string, error) {
	p, err := a.project(a.ctx)
	if err != nil {
		return "", err
	}
	if requested := r.Header.Get("X-Apteva-Project-ID"); requested != "" && requested != p {
		return "", errNotFound
	}
	if requested := r.URL.Query().Get("project_id"); requested != "" && requested != p {
		return "", errNotFound
	}
	return p, nil
}
func decode(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("expected exactly one JSON object")
	}
	return nil
}
func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func httpError(w http.ResponseWriter, err error) {
	status := 400
	if errors.Is(err, errNotFound) {
		status = 404
	}
	if errors.Is(err, errConflict) {
		status = 409
	}
	jsonReply(w, status, map[string]string{"error": err.Error()})
}
func (a *App) handleTool(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		jsonReply(w, 405, map[string]string{"error": "POST required"})
		return
	}
	project, err := a.httpProject(r)
	if err != nil {
		httpError(w, err)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		httpError(w, err)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/tools/")
	result, err := a.call(r.Context(), project, name, raw)
	if err != nil {
		httpError(w, err)
		return
	}
	jsonReply(w, 200, result)
}
func (a *App) handleArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		jsonReply(w, 405, map[string]string{"error": "GET required"})
		return
	}
	project, err := a.httpProject(r)
	if err != nil {
		httpError(w, err)
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/artifacts/"), 10, 64)
	if err != nil || id <= 0 {
		httpError(w, errNotFound)
		return
	}
	data, format, err := a.store.artifactContent(project, id)
	if err != nil {
		httpError(w, err)
		return
	}
	mime := map[string]string{"png": "image/png", "glb": "model/gltf-binary", "json": "application/json"}[format]
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"studio-%d.%s\"", id, format))
	w.Write(data)
}
func main() { sdk.Run(&App{}) }
