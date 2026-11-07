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

func (c topologyCache) cachedAt() time.Time { return c.at }

const (
	topologyTTL   = 45 * time.Second
	cacheCapacity = 1024
)

// storeCached sweeps expired entries at most once per TTL and, when the cache
// is still full, replaces the oldest entry instead of silently not caching.
func storeCached[V interface{ cachedAt() time.Time }](cache map[string]V, swept *time.Time, key string, value V, ttl time.Duration) map[string]V {
	if cache == nil {
		cache = map[string]V{}
	}
	now := time.Now()
	if now.Sub(*swept) >= ttl {
		*swept = now
		for k, entry := range cache {
			if now.Sub(entry.cachedAt()) >= ttl {
				delete(cache, k)
			}
		}
	}
	if _, exists := cache[key]; !exists && len(cache) >= cacheCapacity {
		oldest, oldestAt := "", now
		for k, entry := range cache {
			if entry.cachedAt().Before(oldestAt) {
				oldest, oldestAt = k, entry.cachedAt()
			}
		}
		delete(cache, oldest)
	}
	cache[key] = value
	return cache
}

func (a *App) topology(ctx context.Context, r sceneReader, id int64) (*sdk.AgentTopology, error) {
	scope, _ := ctx.Value(viewerKey{}).(string)
	key := r.identity + ":" + scope + ":" + r.source("", "", 0).ID + ":" + r.projectID + ":" + strconv.FormatInt(id, 10)
	a.catalogMu.Lock()
	cached, ok := a.catalog[key]
	a.catalogMu.Unlock()
	if ok && time.Since(cached.at) < topologyTTL {
		return cached.value, cached.err
	}
	value, err := r.client.GetAgentTopology(ctx, r.projectID, id)
	a.catalogMu.Lock()
	a.catalog = storeCached(a.catalog, &a.swept, key, topologyCache{at: time.Now(), value: value, err: err}, topologyTTL)
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
			if attachment.Name == selfName {
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
