package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const sceneEventLimit = 240

var sceneEventTypes = []string{
	"thread.spawn", "thread.done", "thread.message", "thread.renamed",
	"tool.call", "tool.result", "tool.before", "tool.after",
	"llm.start", "llm.done", "llm.error", "iteration.done", "event.received",
	"execution.waiting", "execution.released", "execution.cancelled",
}

type SceneProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Source struct {
	ProjectID string `json:"project_id,omitempty"`
	ID        string `json:"id"`
	Label     string `json:"label"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Agents    int    `json:"agents"`
}

type SceneAgent struct {
	ProjectID string               `json:"project_id"`
	Threads   []sdk.ObservedThread `json:"threads,omitempty"`
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	Status    string               `json:"status"`
	Model     string               `json:"model,omitempty"`
}

type SceneApp struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type SceneEvent struct {
	TargetAgentID string `json:"target_agent_id,omitempty"`
	TargetID      string `json:"target_id,omitempty"`
	Success       *bool  `json:"success,omitempty"`
	toolName      string
	ID            string    `json:"id"`
	AgentID       string    `json:"agent_id"`
	ThreadID      string    `json:"thread_id,omitempty"`
	Type          string    `json:"type"`
	Kind          string    `json:"kind"`
	Label         string    `json:"label"`
	Target        string    `json:"target,omitempty"`
	Time          time.Time `json:"time"`
}

type Scene struct {
	Scope        string             `json:"scope"`
	Projects     []SceneProject     `json:"projects"`
	Destinations []SceneDestination `json:"destinations"`
	Warnings     []string           `json:"warnings,omitempty"`
	Source       Source             `json:"source"`
	Agents       []SceneAgent       `json:"agents"`
	Apps         []SceneApp         `json:"apps"`
	Events       []SceneEvent       `json:"events"`
	At           time.Time          `json:"at"`
}

// A source only changes the SDK client, project and optional runtime filter.
// Local and remote servers share discovery, history and metadata contracts.
type sceneReader struct {
	client      sdk.TelemetryClient
	projectID   string
	remote      bool
	label       string
	projectName string
	worlds      *App
	identity    string
}

func newSceneReader(app *sdk.AppCtx, remote bool) (sceneReader, error) {
	reader := sceneReader{client: app.TelemetryAPI(), projectID: app.CurrentProject(), label: "Main server"}
	if remote {
		config := app.Config()
		origin, key := strings.TrimSpace(config.Get("remote_url")), config.Get("remote_api_key")
		if origin == "" || strings.TrimSpace(key) == "" {
			return reader, errors.New("remote server is not configured")
		}
		var err error
		reader.client, err = sdk.NewTelemetryClient(origin, key)
		if err != nil {
			return reader, err
		}
		reader.projectID = strings.TrimSpace(config.Get("remote_project_id"))
		reader.label = strings.TrimSpace(config.Get("remote_label"))
		if reader.label == "" {
			reader.label = "Remote server"
		}
		reader.remote = true
		reader.identity = fmt.Sprintf("%x", sha256.Sum256([]byte(origin+"\x00"+key)))
	}
	if reader.client == nil {
		return reader, errors.New("telemetry observation API unavailable")
	}
	return reader, nil
}

func (r sceneReader) source(runtimeID, status string, agents int) Source {
	source := Source{ID: "main", Label: r.label, Kind: "main", Status: status, Agents: agents, ProjectID: r.projectID}
	if r.remote {
		source.ID, source.Kind = "remote", "remote"
	}
	if runtimeID != "" {
		source.ID, source.Kind = "runtime:"+runtimeID, "runtime"
		source.Label = "Environment · " + runtimeID
		if r.remote {
			source.ID, source.Kind = "remote-runtime:"+runtimeID, "remote-runtime"
			source.Label = r.label + " · " + runtimeID
		}
	}
	return source
}

func (r sceneReader) sources(ctx context.Context) ([]Source, error) {
	agents, err := r.client.ListObservedAgents(ctx, r.projectID)
	if err != nil {
		return nil, err
	}
	count := 0
	for _, agent := range agents {
		if agent.RuntimeID == "" {
			count++
		}
	}
	runtimes, err := r.client.ListObservedRuntimes(ctx, r.projectID)
	if err != nil {
		return nil, err
	}
	out := []Source{r.source("", "running", count)}
	for _, runtime := range runtimes {
		out = append(out, r.source(runtime.ID, runtime.Status, len(runtime.Agents)))
	}
	return out, nil
}

func readers(ctx context.Context, app *sdk.AppCtx, remote bool) ([]sceneReader, error) {
	r, err := newSceneReader(app, remote)
	if err != nil {
		return nil, err
	}
	if r.projectID != "" {
		if !remote {
			if projects, e := app.PlatformAPI().ListProjects(); e == nil {
				for _, p := range projects {
					if p.ID == r.projectID {
						r.projectName = p.Name
						break
					}
				}
			}
		}
		return []sceneReader{r}, nil
	}
	var projects []sdk.PlatformProject
	if !remote {
		projects, err = app.PlatformAPI().ListProjects()
	} else {
		// API-key clients may enumerate their authorized observation directory. The
		// public SDK carries project IDs; do not duplicate the remote REST client.
		var agents []sdk.ObservedAgent
		agents, err = r.client.ListObservedAgents(ctx, "")
		seen := map[string]bool{}
		for _, agent := range agents {
			if agent.ProjectID != "" && !seen[agent.ProjectID] {
				seen[agent.ProjectID] = true
				projects = append(projects, sdk.PlatformProject{ID: agent.ProjectID, Name: agent.ProjectID})
			}
		}
	}
	if err != nil {
		return nil, err
	}
	out := make([]sceneReader, 0, len(projects))
	for _, project := range projects {
		if project.ID != "" {
			copy := r
			copy.projectID = project.ID
			copy.projectName = project.Name
			out = append(out, copy)
		}
	}
	return out, nil
}

func (a *App) sources(ctx context.Context, app *sdk.AppCtx) ([]Source, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	out := []Source{}
	for _, remote := range []bool{false, true} {
		if remote && strings.TrimSpace(app.Config().Get("remote_url")) == "" {
			continue
		}
		group, err := readers(ctx, app, remote)
		if err != nil {
			if !remote {
				return nil, err
			}
			out = append(out, Source{ID: "remote", Label: "Remote server", Kind: "remote", Status: "error"})
			continue
		}
		main := Source{ID: "main", Label: "Main server", Kind: "main", Status: "running"}
		if remote {
			main.ID, main.Kind, main.Label = "remote", "remote", "Remote server"
			if len(group) > 0 {
				main.Label = group[0].label
			}
		}
		runtimes := []Source{}
		found, failures := readProjects(ctx, group, func(reader sceneReader) ([]Source, error) { return reader.sources(ctx) })
		for i, items := range found {
			if failures[i] != nil {
				main.Status = "error"
				continue
			}
			main.Label = group[i].label
			main.Agents += items[0].Agents
			if len(group) == 1 {
				main.ProjectID = group[i].projectID
			}
			runtimes = append(runtimes, items[1:]...)
		}
		out = append(out, main)
		out = append(out, runtimes...)
	}
	return out, nil
}

func (a *App) scene(ctx context.Context, app *sdk.AppCtx, sourceID string) (*Scene, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var remote bool
	var runtimeID string
	switch {
	case sourceID == "" || sourceID == "main":
	case sourceID == "remote":
		remote = true
	case strings.HasPrefix(sourceID, "runtime:"):
		runtimeID = strings.TrimPrefix(sourceID, "runtime:")
		if runtimeID == "" {
			return nil, errors.New("runtime ID required")
		}
	case strings.HasPrefix(sourceID, "remote-runtime:"):
		remote = true
		runtimeID = strings.TrimPrefix(sourceID, "remote-runtime:")
		if runtimeID == "" {
			return nil, errors.New("runtime ID required")
		}
	default:
		return nil, fmt.Errorf("unknown source %q", sourceID)
	}
	group, err := readers(ctx, app, remote)
	if err != nil {
		return nil, err
	}
	combined := &Scene{Source: Source{ID: sourceID, Label: "Main server", Kind: "main", Status: "running"}, Scope: "instance", Projects: []SceneProject{}, Agents: []SceneAgent{}, Apps: []SceneApp{}, Events: []SceneEvent{}, Destinations: []SceneDestination{}, At: time.Now().UTC()}
	if sourceID == "" {
		combined.Source.ID = "main"
	}
	if remote {
		combined.Source.Kind = "remote"
		combined.Source.Label = "Remote server"
	}
	if (!remote && app.CurrentProject() != "") || (remote && app.Config().Get("remote_project_id") != "") || runtimeID != "" {
		combined.Scope = "project"
	}
	parts, failures := readProjects(ctx, group, func(reader sceneReader) (*Scene, error) {
		reader.worlds = a
		if runtimeID != "" {
			runtimes, e := reader.client.ListObservedRuntimes(ctx, reader.projectID)
			if e != nil {
				return nil, e
			}
			found := false
			for _, rt := range runtimes {
				if rt.ID == runtimeID {
					found = true
					break
				}
			}
			if !found {
				return nil, nil
			}
		}
		return reader.scene(ctx, runtimeID)
	})
	matched := false
	var firstError error
	for i, part := range parts {
		reader := group[i]
		if reader.projectName == "" {
			reader.projectName = reader.projectID
		}
		if failures[i] != nil {
			if firstError == nil {
				firstError = failures[i]
			}
			if len(group) == 1 {
				return nil, failures[i]
			}
			combined.Warnings = append(combined.Warnings, "Some projects could not be read; showing authorized, available projects.")
			continue
		}
		if part == nil {
			continue
		}
		matched = true
		combined.Projects = append(combined.Projects, SceneProject{ID: reader.projectID, Name: reader.projectName})
		combined.Agents = append(combined.Agents, part.Agents...)
		combined.Apps = append(combined.Apps, part.Apps...)
		combined.Events = append(combined.Events, part.Events...)
		combined.Destinations = append(combined.Destinations, part.Destinations...)
		combined.Warnings = append(combined.Warnings, part.Warnings...)
		combined.Source = reader.source(runtimeID, part.Source.Status, 0)
	}
	if !matched && firstError != nil {
		return nil, firstError
	}
	if runtimeID != "" && !matched {
		return nil, errors.New("runtime not found in authorized projects")
	}
	if len(combined.Warnings) > 0 {
		combined.Source.Status = "partial"
	}
	if combined.Scope == "instance" {
		combined.Source.ProjectID = ""
	}
	combined.Source.Agents = len(combined.Agents)
	finishScene(combined)
	return combined, nil
}

func (r sceneReader) scene(ctx context.Context, runtimeID string) (*Scene, error) {
	scene := &Scene{Source: r.source(runtimeID, "running", 0), Agents: []SceneAgent{}, Apps: []SceneApp{}, Events: []SceneEvent{}, At: time.Now().UTC()}
	if runtimeID == "" {
		agents, err := r.client.ListObservedAgents(ctx, r.projectID)
		if err != nil {
			return nil, err
		}
		for _, agent := range agents {
			if agent.RuntimeID == "" {
				scene.Agents = append(scene.Agents, SceneAgent{ID: strconv.FormatInt(agent.ID, 10), Name: agent.Name, Status: agent.Status, ProjectID: r.projectID})
			}
		}
	} else {
		runtime, err := r.client.GetObservedRuntime(ctx, r.projectID, runtimeID)
		if err != nil {
			return nil, err
		}
		scene.Source.Status = runtime.Status
		for _, app := range runtime.Apps {
			id := app.Name
			if app.InstallID != 0 {
				id = strconv.FormatInt(app.InstallID, 10)
			}
			scene.Apps = append(scene.Apps, SceneApp{ID: id, Name: app.Name, Status: app.Status})
		}
		for _, agent := range runtime.Agents {
			id := strconv.FormatInt(agent.ID, 10)
			name := agent.Alias
			if name == "" {
				name = id
			}
			scene.Agents = append(scene.Agents, SceneAgent{ID: id, Name: name, Status: agent.Status, Model: agent.Model, ProjectID: r.projectID})
		}
	}
	scene.Source.Agents = len(scene.Agents)
	if len(scene.Agents) == 0 {
		return scene, nil
	}
	// One bounded history request for the source, independent of agent count.
	// The server owns retention and metadata projection; there is no app event log.
	page, err := r.client.QueryTelemetry(ctx, sdk.TelemetryQuery{
		ProjectID: r.projectID, RuntimeID: runtimeID, Events: sceneEventTypes,
		Projection: "metadata", Since: scene.At.Add(-15 * time.Minute), Limit: 500,
	})
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(scene.Agents))
	for _, agent := range scene.Agents {
		allowed[agent.ID] = true
	}
	// History is newest-ingestion-first. Reverse before the stable time sort so
	// events with the same timestamp keep their actual ingestion order.
	for i := len(page.Events) - 1; i >= 0; i-- {
		event := page.Events[i]
		if event.RuntimeID == runtimeID && allowed[strconv.FormatInt(event.AgentID, 10)] {
			scene.Events = append(scene.Events, normalizeEvent(event))
		}
	}
	finishScene(scene)
	if r.worlds != nil {
		r.worlds.enrichScene(ctx, r, scene)
	}
	return scene, nil
}

func normalizeEvent(event sdk.TelemetryStreamEvent) SceneEvent {
	metadata, _ := event.Metadata()
	kind, label, target := "activity", event.Type, ""
	switch event.Type {
	case "thread.spawn":
		kind, label = "spawn", "started a thread"
	case "thread.done":
		kind, label = "done", "finished a thread"
	case "thread.message":
		kind, label = "message", "sent a message"
	case "tool.call", "tool.before":
		kind, target = "tool", cleanLabel(metadata.ToolName)
		if target == "" {
			target = "tool"
		}
		label = "called " + target
	case "tool.result", "tool.after":
		kind, label, target = "result", "tool returned", cleanLabel(metadata.ToolName)
		if metadata.Success != nil && !*metadata.Success {
			kind, label = "error", "tool failed"
		}
	case "llm.start":
		kind, label = "thinking", "started thinking"
	case "llm.done", "iteration.done":
		kind, label = "done", "finished a step"
	case "llm.error":
		kind, label = "error", "encountered an error"
	case "event.received":
		kind, label = "message", "received an event"
	case "execution.waiting":
		kind, label = "waiting", "waiting"
	case "execution.released":
		kind, label = "activity", "resumed"
	case "execution.cancelled":
		kind, label = "error", "execution cancelled"
	}
	id := event.ID
	if event.ServerID != "" {
		id = event.ServerID + ":" + id
	}
	targetAgent := ""
	if metadata.TargetAgentID != 0 {
		targetAgent = strconv.FormatInt(metadata.TargetAgentID, 10)
	}
	return SceneEvent{TargetAgentID: targetAgent, Success: metadata.Success, toolName: metadata.ToolName, ID: id, AgentID: strconv.FormatInt(event.AgentID, 10), ThreadID: event.ThreadID, Type: event.Type, Kind: kind, Label: label, Target: target, Time: event.Time}
}

func cleanLabel(raw string) string {
	raw = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, strings.TrimSpace(raw))
	runes := []rune(raw)
	if len(runes) > 64 {
		raw = string(runes[:64]) + "…"
	}
	return raw
}

func finishScene(scene *Scene) {
	seenWarnings := map[string]bool{}
	warnings := scene.Warnings[:0]
	for _, warning := range scene.Warnings {
		if !seenWarnings[warning] {
			seenWarnings[warning] = true
			warnings = append(warnings, warning)
		}
	}
	scene.Warnings = warnings
	sort.Slice(scene.Agents, func(i, j int) bool { return scene.Agents[i].Name < scene.Agents[j].Name })
	sort.SliceStable(scene.Events, func(i, j int) bool { return scene.Events[i].Time.Before(scene.Events[j].Time) })
	limit := sceneEventLimit
	if scene.Scope == "instance" && len(scene.Projects) > 1 {
		limit *= len(scene.Projects)
	}
	if len(scene.Events) > limit {
		scene.Events = scene.Events[len(scene.Events)-limit:]
	}
}

// Bound concurrent SDK work while preserving project order. Reads share the
// caller's cancellation and never spawn a goroutine per project.
func readProjects[T any](ctx context.Context, readers []sceneReader, load func(sceneReader) (T, error)) ([]T, []error) {
	values, failures := make([]T, len(readers)), make([]error, len(readers))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < 4 && worker < len(readers); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				if err := ctx.Err(); err != nil {
					failures[i] = err
					continue
				}
				values[i], failures[i] = load(readers[i])
			}
		}()
	}
	for i := range readers {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	return values, failures
}
