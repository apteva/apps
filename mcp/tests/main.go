package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

type App struct {
	ctx           *sdk.AppCtx
	client        *http.Client
	functionSlots chan struct{}
	publish       func(context.Context, *sdk.AppCtx, string, map[string]any) error
}

func main() { sdk.Run(&App{}) }
func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic(err)
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("tests requires an app database")
	}
	a.ctx = ctx
	if a.client == nil {
		a.client = httpClient(os.Getenv("APTEVA_TESTS_ALLOW_PRIVATE_NETWORK") == "true")
	}
	a.functionSlots = make(chan struct{}, 4)
	if a.publish == nil {
		a.publish = publishEvent
	}
	// Do not replay in-flight tests after a crash: they may have side effects.
	rows, err := ctx.AppDB().Query(`SELECT project_id,id FROM test_runs WHERE status='running'`)
	if err != nil {
		return err
	}
	type item struct {
		project string
		id      int64
	}
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.project, &v.id); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		if err = (store{ctx.AppDB(), v.project}).finish(v.id, "runner restarted during execution; run again manually"); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.client != nil {
		a.client.CloseIdleConnections()
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{Name: "runner", Schedule: "@every 1s", Run: a.work}, {Name: "events", Schedule: "@every 2s", Run: a.flushEvents}}
}
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{{Event: "tests.run.requested", Handler: func(ctx *sdk.AppCtx, e sdk.Event) error {
		if strings.TrimSpace(e.ProjectID) == "" {
			return errors.New("run event requires project_id")
		}
		if fixed := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); fixed != "" && e.ProjectID != fixed {
			return errors.New("project scope mismatch")
		}
		var args struct {
			SuiteID int64 `json:"suite_id"`
		}
		if err := decodeArgs(e.Data, &args); err != nil {
			return err
		}
		key := ""
		if e.DeliveryID != "" {
			key = "event:" + e.DeliveryID
		}
		_, err := (store{ctx.AppDB(), e.ProjectID}).queue(args.SuiteID, "event", key)
		return err
	}}}
}
func (a *App) flushEvents(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().Query(`SELECT id,project_id,topic,payload FROM test_outbox WHERE project_id=? ORDER BY id LIMIT 50`, app.CurrentProject())
	if err != nil {
		return err
	}
	type event struct {
		id                  int64
		project, topic, raw string
	}
	items := []event{}
	for rows.Next() {
		var e event
		if err = rows.Scan(&e.id, &e.project, &e.topic, &e.raw); err != nil {
			rows.Close()
			return err
		}
		items = append(items, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range items {
		var payload map[string]any
		if err = json.Unmarshal([]byte(e.raw), &payload); err != nil {
			return err
		}
		payload["event_id"] = e.id // stable across outbox retries; consumers can deduplicate.
		if err = a.publish(ctx, app.WithProject(e.project), e.topic, payload); err != nil {
			return err
		}
		if _, err = app.AppDB().Exec(`DELETE FROM test_outbox WHERE id=? AND project_id=?`, e.id, e.project); err != nil {
			return err
		}
	}
	return nil
}

// Publish through the SDK's acknowledged emitter so the outbox is only
// removed once the platform accepts the event.
func publishEvent(ctx context.Context, app *sdk.AppCtx, topic string, payload map[string]any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return app.EmitWithProjectAck(ctx, topic, app.CurrentProject(), payload)
}
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{{Pattern: "/tools/call", Handler: a.httpTool}}
}

func scoped(ctx context.Context, app *sdk.AppCtx, requested string) (*sdk.AppCtx, error) {
	project := app.CurrentProject()
	if caller := sdk.CallerFrom(ctx); caller != nil && caller.ProjectID != "" {
		project = caller.ProjectID
	}
	if fixed := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); fixed != "" {
		if project != "" && project != fixed {
			return nil, errors.New("project scope mismatch")
		}
		project = fixed
	}
	if project != "" && requested != "" && project != requested {
		return nil, errors.New("project scope mismatch")
	}
	if project == "" {
		project = requested
	}
	if project == "" {
		return nil, errors.New("project_id required")
	}
	return app.WithProject(project), nil
}
func (a *App) httpTool(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, err error) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(405, errors.New("POST required"))
		return
	}
	if a.ctx == nil {
		fail(503, errors.New("app is not mounted"))
		return
	}
	var body struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPayload+1024))
	if err := dec.Decode(&body); err != nil {
		fail(400, err)
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		fail(400, errors.New("expected one JSON object"))
		return
	}
	requestCtx := r.Context()
	if pid := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID")); pid != "" {
		requestCtx = sdk.WithCaller(requestCtx, &sdk.Caller{ProjectID: pid})
	}
	app, err := scoped(requestCtx, a.ctx, r.URL.Query().Get("project_id"))
	if err != nil {
		fail(403, err)
		return
	}
	for _, t := range a.MCPTools() {
		if t.Name == body.Tool {
			out, err := t.HandlerCtx(requestCtx, app, body.Args)
			if err != nil {
				status := 400
				if errors.Is(err, sql.ErrNoRows) {
					status = 404
				}
				fail(status, err)
				return
			}
			if body.Tool == "tests_run" {
				w.WriteHeader(http.StatusAccepted)
			}
			if err = json.NewEncoder(w).Encode(out); err != nil {
				app.Logger().Warn("response encoding failed", "error", err)
			}
			return
		}
	}
	fail(404, fmt.Errorf("unknown tool %q", body.Tool))
}
