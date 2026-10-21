package main

import (
	"context"
	"sort"
	"strconv"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type SceneDestination struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"project_id"`
	AgentIDs  []string   `json:"agent_ids,omitempty"`
	Kind      string     `json:"kind"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	Tools     []string   `json:"tools,omitempty"`
	ToolCount int        `json:"tool_count"`
	CallCount int        `json:"call_count"`
	LastCall  *time.Time `json:"last_call,omitempty"`
}

type topologyCache struct {
	at    time.Time
	value *sdk.AgentTopology
	err   error
}

func (a *App) topology(ctx context.Context, r sceneReader, id int64) (*sdk.AgentTopology, error) {
	scope, _ := ctx.Value(viewerKey{}).(string)
	key := r.identity + ":" + scope + ":" + r.source("", "", 0).ID + ":" + r.projectID + ":" + strconv.FormatInt(id, 10)
	a.catalogMu.Lock()
	cached, ok := a.catalog[key]
	a.catalogMu.Unlock()
	if ok && time.Since(cached.at) < 45*time.Second {
		return cached.value, cached.err
	}
	value, err := r.client.GetAgentTopology(ctx, r.projectID, id)
	a.catalogMu.Lock()
	if a.catalog == nil {
		a.catalog = map[string]topologyCache{}
	}
	for k, entry := range a.catalog {
		if time.Since(entry.at) >= 45*time.Second {
			delete(a.catalog, k)
		}
	}
	if len(a.catalog) < 1024 {
		a.catalog[key] = topologyCache{at: time.Now(), value: value, err: err}
	}
	a.catalogMu.Unlock()
	return value, err
}

func (a *App) enrichScene(ctx context.Context, r sceneReader, scene *Scene) {
	byID := map[string]*SceneDestination{}
	for _, app := range scene.Apps {
		key := r.projectID + ":app:" + app.ID
		byID[key] = &SceneDestination{ID: key, ProjectID: r.projectID, Kind: "app", Name: app.Name, Status: app.Status}
	}
	unavailable := false
	for i := range scene.Agents {
		agent := &scene.Agents[i]
		id, err := strconv.ParseInt(agent.ID, 10, 64)
		if err != nil {
			continue
		}
		topology, err := a.topology(ctx, r, id)
		if err != nil {
			unavailable = true
			continue
		}
		agent.Threads = topology.Threads
		for _, attachment := range topology.Attachments {
			if attachment.Name == "agent-worlds" {
				continue
			}
			kind, identity := "integration", attachment.Name
			if attachment.InstallID != 0 {
				kind, identity = "app", strconv.FormatInt(attachment.InstallID, 10)
			}
			key := r.projectID + ":" + kind + ":" + identity
			dest := byID[key]
			if dest == nil {
				dest = &SceneDestination{ID: key, ProjectID: r.projectID, Kind: kind, Name: attachment.Name, Status: "attached"}
				byID[key] = dest
			}
			dest.AgentIDs = append(dest.AgentIDs, agent.ID)
			dest.Tools = append(dest.Tools, attachment.Tools...)
		}
	}
	if unavailable {
		scene.Warnings = append(scene.Warnings, "Some agent topology is unavailable; showing observed activity.")
	}
	scene.Destinations = destinationValues(byID)
	attributeEvents(scene)
}

func destinationValues(byID map[string]*SceneDestination) []SceneDestination {
	out := make([]SceneDestination, 0, len(byID))
	for _, dest := range byID {
		seen := map[string]bool{}
		tools := []string{}
		for _, tool := range dest.Tools {
			if !seen[tool] {
				seen[tool] = true
				tools = append(tools, tool)
			}
		}
		sort.Strings(tools)
		dest.Tools = tools
		out = append(out, *dest)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name || (out[i].Name == out[j].Name && out[i].ID < out[j].ID)
	})
	return out
}

func attributeEvents(scene *Scene) {
	// A name shared by multiple destinations is ambiguous, including two app
	// installs with the same name. Only a unique exact match creates an edge.
	owner := map[string]string{}
	for _, dest := range scene.Destinations {
		for _, tool := range dest.Tools {
			if previous, ok := owner[tool]; ok && previous != dest.ID {
				owner[tool] = ""
			} else if !ok {
				owner[tool] = dest.ID
			}
		}
	}
	counts := map[string]int{}
	last := map[string]time.Time{}
	unknown := false
	for i := range scene.Events {
		event := &scene.Events[i]
		if event.Kind != "tool" && event.Kind != "result" {
			continue
		}
		name := event.toolName
		if name == "" {
			name = event.Target
		}
		if id := owner[name]; id != "" {
			event.TargetID = id
			if event.Kind == "tool" {
				counts[id]++
				if event.Time.After(last[id]) {
					last[id] = event.Time
				}
			}
		} else {
			unknown = true
			event.TargetID = scene.Source.ProjectID + ":other:tools"
			if event.Kind == "tool" {
				counts[event.TargetID]++
				if event.Time.After(last[event.TargetID]) {
					last[event.TargetID] = event.Time
				}
			}
		}
	}
	if unknown {
		scene.Destinations = append(scene.Destinations, SceneDestination{ID: scene.Source.ProjectID + ":other:tools", ProjectID: scene.Source.ProjectID, Kind: "other", Name: "Other tools", Status: "observed"})
	}
	for i := range scene.Destinations {
		dest := &scene.Destinations[i]
		dest.CallCount = counts[dest.ID]
		dest.ToolCount = len(dest.Tools)
		if len(dest.Tools) > 3 {
			dest.Tools = dest.Tools[:3]
		}
		if at, ok := last[dest.ID]; ok {
			copy := at
			dest.LastCall = &copy
		}
	}
}
