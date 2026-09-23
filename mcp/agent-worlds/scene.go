package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type Source struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Agents int    `json:"agents"`
}

type SceneAgent struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Model      string `json:"model,omitempty"`
	MainThread string `json:"main_thread,omitempty"`
}

type SceneApp struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type SceneDestination struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	Tools     []string   `json:"tools,omitempty"`
	CallCount int        `json:"call_count"`
	LastCall  *time.Time `json:"last_call,omitempty"`
}

type SceneEvent struct {
	ID       string    `json:"id"`
	AgentID  string    `json:"agent_id"`
	ThreadID string    `json:"thread_id,omitempty"`
	Type     string    `json:"type"`
	Kind     string    `json:"kind"`
	Label    string    `json:"label"`
	Target   string    `json:"target,omitempty"`
	TargetID string    `json:"target_id,omitempty"`
	Success  *bool     `json:"success,omitempty"`
	Time     time.Time `json:"time"`
	toolName string
}

type Scene struct {
	Source       Source             `json:"source"`
	Agents       []SceneAgent       `json:"agents"`
	Apps         []SceneApp         `json:"apps"`
	Destinations []SceneDestination `json:"destinations"`
	Events       []SceneEvent       `json:"events"`
	At           time.Time          `json:"at"`
}

func (a *App) sources() ([]Source, error) {
	agents, err := sdk.ListAgentsVia(a.ctx.PlatformAPI(), a.ctx.CurrentProject())
	if err != nil {
		return nil, err
	}
	out := []Source{{ID: "main", Label: "Main server", Kind: "main", Status: "running", Agents: len(agents)}}
	runtimes, err := a.ctx.RuntimeAPI().ListRuntimes()
	if err != nil {
		return nil, err
	}
	for _, rt := range runtimes {
		if rt.ProjectID != a.ctx.CurrentProject() {
			continue
		}
		out = append(out, Source{ID: "runtime:" + rt.ID, Label: "Environment · " + rt.ID, Kind: "runtime", Status: rt.Status, Agents: len(rt.Agents)})
	}
	if a.hasRemote() {
		status, count := "running", 0
		if remote, err := a.remoteAgents(); err == nil {
			count = len(remote)
		} else {
			status = "error"
		}
		label := strings.TrimSpace(a.ctx.Config().Get("remote_label"))
		if label == "" {
			label = "Remote server"
		}
		out = append(out, Source{ID: "remote", Label: label, Kind: "remote", Status: status, Agents: count})
		if definitions, err := a.remoteDefinitions(); err == nil {
			for _, definition := range definitions {
				if definition.ActiveRun == nil || definition.Runtime == nil {
					continue
				}
				if definition.ActiveRun.Status != "running" {
					continue
				}
				out = append(out, Source{ID: "remote-run:" + definition.ActiveRun.ID, Label: label + " · " + definition.Name, Kind: "remote-runtime", Status: definition.Runtime.Status, Agents: len(definition.Runtime.Agents)})
			}
		}
	}
	return out, nil
}

func (a *App) scene(sourceID string) (*Scene, error) {
	if sourceID == "" {
		sourceID = "main"
	}
	switch {
	case sourceID == "main":
		return a.mainScene()
	case strings.HasPrefix(sourceID, "runtime:"):
		return a.runtimeScene(strings.TrimPrefix(sourceID, "runtime:"))
	case sourceID == "remote" && a.hasRemote():
		return a.remoteScene()
	case strings.HasPrefix(sourceID, "remote-run:") && a.hasRemote():
		return a.remoteRunScene(strings.TrimPrefix(sourceID, "remote-run:"))
	default:
		return nil, fmt.Errorf("unknown source %q", sourceID)
	}
}

func (a *App) mainScene() (*Scene, error) {
	agents, err := sdk.ListAgentsVia(a.ctx.PlatformAPI(), a.ctx.CurrentProject())
	if err != nil {
		return nil, err
	}
	scene := &Scene{Source: Source{ID: "main", Label: "Main server", Kind: "main", Status: "running", Agents: len(agents)}, Agents: make([]SceneAgent, 0, len(agents)), Apps: []SceneApp{}, Events: []SceneEvent{}, At: time.Now().UTC()}
	allowed := make(map[int64]bool, len(agents))
	for _, agent := range agents {
		allowed[agent.ID] = true
		scene.Agents = append(scene.Agents, SceneAgent{ID: strconv.FormatInt(agent.ID, 10), Name: agent.Name, Status: agent.Status, MainThread: agent.DefaultThreadID})
	}
	a.mu.RLock()
	for _, ev := range a.events {
		if allowed[ev.AgentID] {
			scene.Events = append(scene.Events, normalizeEvent(ev.ID, ev.AgentID, ev.ThreadID, ev.Type, ev.Time, ev.Data))
		}
	}
	a.mu.RUnlock()
	finishScene(scene)
	a.enrichScene(scene)
	return scene, nil
}

func (a *App) runtimeScene(id string) (*Scene, error) {
	if id == "" {
		return nil, errors.New("runtime ID required")
	}
	rt, err := a.ctx.RuntimeAPI().GetRuntime(id)
	if err != nil {
		return nil, err
	}
	if rt.ProjectID != a.ctx.CurrentProject() {
		return nil, errors.New("runtime is outside this project")
	}
	scene := &Scene{Source: Source{ID: "runtime:" + id, Label: "Environment · " + id, Kind: "runtime", Status: rt.Status, Agents: len(rt.Agents)}, Agents: []SceneAgent{}, Apps: []SceneApp{}, Events: []SceneEvent{}, At: time.Now().UTC()}
	for _, app := range rt.Apps {
		scene.Apps = append(scene.Apps, SceneApp{ID: app.Name, Name: app.Name, Status: app.Status})
	}
	for _, agent := range rt.Agents {
		alias := agent.Alias
		if alias == "" {
			alias = strconv.FormatInt(agent.ID, 10)
		}
		scene.Agents = append(scene.Agents, SceneAgent{ID: strconv.FormatInt(agent.ID, 10), Name: alias, Status: agent.Status, Model: agent.Model, MainThread: "main"})
		events, err := a.ctx.RuntimeAPI().ListRuntimeAgentTelemetry(id, alias, time.Now().Add(-15*time.Minute), 120)
		if err != nil {
			return nil, err
		}
		for _, ev := range events {
			scene.Events = append(scene.Events, normalizeEvent(ev.ID, ev.AgentID, ev.ThreadID, ev.Type, ev.Time, ev.Data))
		}
	}
	finishScene(scene)
	a.enrichScene(scene)
	return scene, nil
}

func (a *App) hasRemote() bool {
	return strings.TrimSpace(a.ctx.Config().Get("remote_url")) != "" && a.ctx.Config().Get("remote_api_key") != ""
}

func (a *App) remoteBase() (*url.URL, error) {
	raw := strings.TrimRight(strings.TrimSpace(a.ctx.Config().Get("remote_url")), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("remote URL must be an origin without a path")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("remote URL must use HTTPS (except loopback)")
	}
	return u, nil
}

func (a *App) remoteGET(path string, out any) error {
	base, err := a.remoteBase()
	if err != nil {
		return err
	}
	endpoint := *base
	relative, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(relative.Path, "/api/") {
		return errors.New("invalid remote API path")
	}
	endpoint.Path = relative.Path
	endpoint.RawQuery = relative.RawQuery
	req, err := http.NewRequest(http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.ctx.Config().Get("remote_api_key"))
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("remote server returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out)
}

type remoteAgent struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	DefaultThreadID string `json:"default_thread_id"`
}
type remoteEvent struct {
	ID       string          `json:"id"`
	AgentID  int64           `json:"instance_id"`
	ThreadID string          `json:"thread_id"`
	Type     string          `json:"type"`
	Time     time.Time       `json:"time"`
	Data     json.RawMessage `json:"data"`
}

func (a *App) remoteAgents() ([]remoteAgent, error) {
	var agents []remoteAgent
	path := "/api/agents"
	if project := strings.TrimSpace(a.ctx.Config().Get("remote_project_id")); project != "" {
		path += "?project_id=" + url.QueryEscape(project)
	}
	err := a.remoteGET(path, &agents)
	return agents, err
}

type remoteDefinition struct {
	Name      string `json:"name"`
	ActiveRun *struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"active_run"`
	Runtime *struct {
		Status string             `json:"status"`
		Apps   []sdk.RuntimeApp   `json:"apps"`
		Agents []sdk.RuntimeAgent `json:"agents"`
	} `json:"runtime"`
}

func (a *App) remoteEnvironmentPath(path string) string {
	query := url.Values{}
	if project := strings.TrimSpace(a.ctx.Config().Get("remote_project_id")); project != "" {
		query.Set("project_id", project)
	}
	if install := strings.TrimSpace(a.ctx.Config().Get("remote_environments_install_id")); install != "" {
		query.Set("install_id", install)
	}
	if len(query) == 0 {
		return path
	}
	return path + "?" + query.Encode()
}

func (a *App) remoteDefinitions() ([]remoteDefinition, error) {
	var definitions []remoteDefinition
	err := a.remoteGET(a.remoteEnvironmentPath("/api/apps/environments/api/environments"), &definitions)
	return definitions, err
}

func (a *App) remoteRunScene(runID string) (*Scene, error) {
	definitions, err := a.remoteDefinitions()
	if err != nil {
		return nil, err
	}
	var selected *remoteDefinition
	for index := range definitions {
		if definitions[index].ActiveRun != nil && definitions[index].ActiveRun.ID == runID {
			selected = &definitions[index]
			break
		}
	}
	if selected == nil || selected.Runtime == nil {
		return nil, errors.New("remote environment is not running")
	}
	label := strings.TrimSpace(a.ctx.Config().Get("remote_label"))
	if label == "" {
		label = "Remote server"
	}
	scene := &Scene{Source: Source{ID: "remote-run:" + runID, Label: label + " · " + selected.Name, Kind: "remote-runtime", Status: selected.Runtime.Status, Agents: len(selected.Runtime.Agents)}, Agents: []SceneAgent{}, Apps: []SceneApp{}, Events: []SceneEvent{}, At: time.Now().UTC()}
	for _, app := range selected.Runtime.Apps {
		scene.Apps = append(scene.Apps, SceneApp{ID: app.Name, Name: app.Name, Status: app.Status})
	}
	for _, agent := range selected.Runtime.Agents {
		alias := agent.Alias
		if alias == "" {
			alias = strconv.FormatInt(agent.ID, 10)
		}
		scene.Agents = append(scene.Agents, SceneAgent{ID: strconv.FormatInt(agent.ID, 10), Name: alias, Status: agent.Status, Model: agent.Model, MainThread: "main"})
		path := "/api/apps/environments/api/runs/" + url.PathEscape(runID) + "/inspect"
		query := url.Values{"agent": {alias}}
		if project := strings.TrimSpace(a.ctx.Config().Get("remote_project_id")); project != "" {
			query.Set("project_id", project)
		}
		if install := strings.TrimSpace(a.ctx.Config().Get("remote_environments_install_id")); install != "" {
			query.Set("install_id", install)
		}
		var inspected struct {
			Telemetry []remoteEvent `json:"telemetry"`
		}
		if err := a.remoteGET(path+"?"+query.Encode(), &inspected); err != nil {
			return nil, err
		}
		for _, ev := range inspected.Telemetry {
			scene.Events = append(scene.Events, normalizeEvent(ev.ID, ev.AgentID, ev.ThreadID, ev.Type, ev.Time, ev.Data))
		}
	}
	finishScene(scene)
	a.enrichScene(scene)
	return scene, nil
}

func (a *App) remoteScene() (*Scene, error) {
	agents, err := a.remoteAgents()
	if err != nil {
		return nil, err
	}
	label := strings.TrimSpace(a.ctx.Config().Get("remote_label"))
	if label == "" {
		label = "Remote server"
	}
	scene := &Scene{Source: Source{ID: "remote", Label: label, Kind: "remote", Status: "running", Agents: len(agents)}, Agents: []SceneAgent{}, Apps: []SceneApp{}, Events: []SceneEvent{}, At: time.Now().UTC()}
	for _, agent := range agents {
		scene.Agents = append(scene.Agents, SceneAgent{ID: strconv.FormatInt(agent.ID, 10), Name: agent.Name, Status: agent.Status, MainThread: agent.DefaultThreadID})
		var events []remoteEvent
		path := "/api/telemetry?" + url.Values{"agent_id": {strconv.FormatInt(agent.ID, 10)}, "limit": {"80"}}.Encode()
		if err := a.remoteGET(path, &events); err != nil {
			return nil, err
		}
		for _, ev := range events {
			scene.Events = append(scene.Events, normalizeEvent(ev.ID, ev.AgentID, ev.ThreadID, ev.Type, ev.Time, ev.Data))
		}
	}
	finishScene(scene)
	a.enrichScene(scene)
	return scene, nil
}

func normalizeEvent(id string, agentID int64, threadID, eventType string, at time.Time, raw json.RawMessage) SceneEvent {
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	kind, label, target := "activity", eventType, ""
	toolName := ""
	switch eventType {
	case "thread.spawn":
		kind, label = "spawn", "started a thread"
	case "thread.done":
		kind, label = "done", "finished a thread"
	case "thread.message":
		kind, label = "message", "sent a message"
	case "tool.call", "tool.before":
		kind = "tool"
		name, _ := data["name"].(string)
		if name == "" {
			name, _ = data["tool"].(string)
		}
		if name == "" {
			name = "tool"
		}
		label = "called " + cleanLabel(name)
		target = cleanLabel(name)
		toolName = name
	case "tool.result", "tool.after":
		kind, label = "result", "tool returned"
		name, _ := data["name"].(string)
		if name == "" {
			name, _ = data["tool"].(string)
		}
		target = cleanLabel(name)
		toolName = name
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
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if id == "" {
		id = fmt.Sprintf("%d:%s:%s:%d", agentID, threadID, eventType, at.UnixNano())
	}
	event := SceneEvent{ID: id, AgentID: strconv.FormatInt(agentID, 10), ThreadID: threadID, Type: eventType, Kind: kind, Label: label, Target: target, Time: at, toolName: toolName}
	if kind == "result" {
		if success, ok := data["success"].(bool); ok {
			event.Success = &success
		}
	}
	return event
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
	sort.Slice(scene.Agents, func(i, j int) bool { return scene.Agents[i].Name < scene.Agents[j].Name })
	sort.Slice(scene.Events, func(i, j int) bool { return scene.Events[i].Time.Before(scene.Events[j].Time) })
	if len(scene.Events) > 240 {
		scene.Events = scene.Events[len(scene.Events)-240:]
	}
}
