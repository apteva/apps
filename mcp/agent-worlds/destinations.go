package main

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type cachedDestinations struct {
	at    time.Time
	items []SceneDestination
}

// destinationCatalog reads metadata only. Tool names are retained to establish
// ownership; descriptions, credentials and invocation arguments never leave here.
func (a *App) destinationCatalog(scene *Scene) []SceneDestination {
	key := scene.Source.ID
	a.destinationMu.Lock()
	if cached, ok := a.destinationCache[key]; ok && time.Since(cached.at) < 45*time.Second {
		a.destinationMu.Unlock()
		return cloneDestinations(cached.items)
	}
	a.destinationMu.Unlock()
	var items []SceneDestination
	switch scene.Source.Kind {
	case "main":
		items = a.mainDestinations(scene)
	case "runtime":
		items = a.runtimeDestinations(scene)
	case "remote":
		items = a.remoteDestinations()
	case "remote-runtime":
		for _, app := range scene.Apps {
			items = append(items, SceneDestination{ID: "app:" + app.ID, Kind: "app", Name: app.Name, Status: app.Status})
		}
	}
	a.destinationMu.Lock()
	if a.destinationCache == nil {
		a.destinationCache = make(map[string]cachedDestinations)
	}
	a.destinationCache[key] = cachedDestinations{at: time.Now(), items: cloneDestinations(items)}
	a.destinationMu.Unlock()
	return items
}

func cloneDestinations(items []SceneDestination) []SceneDestination {
	out := append([]SceneDestination(nil), items...)
	for i := range out {
		out[i].Tools = append([]string(nil), out[i].Tools...)
	}
	return out
}

func (a *App) mainDestinations(scene *Scene) []SceneDestination {
	rt := a.ctx.RuntimeAPI()
	catalog, _ := rt.ListRuntimeCatalogApps(a.ctx.CurrentProject())
	byInstall := map[int64]sdk.RuntimeCatalogApp{}
	for _, app := range catalog {
		byInstall[app.InstallID] = app
	}
	byID := map[string]*SceneDestination{}
	for _, agent := range scene.Agents {
		id, err := strconv.ParseInt(agent.ID, 10, 64)
		if err != nil {
			continue
		}
		capabilities, err := rt.GetRuntimeAgentCapabilities(id)
		if err != nil {
			continue
		}
		for _, capability := range capabilities {
			if capability.AppName == "" || capability.AppName == "agent-worlds" {
				continue
			}
			key := "app:" + capability.AppName
			dest := byID[key]
			if dest == nil {
				meta := byInstall[capability.InstallID]
				name := meta.DisplayName
				if name == "" {
					name = capability.AppName
				}
				status := meta.Status
				if status == "" {
					status = "bound"
				}
				dest = &SceneDestination{ID: key, Kind: "app", Name: name, Status: status}
				byID[key] = dest
			}
			for _, tool := range capability.Tools {
				if tool.Name != "" {
					dest.Tools = append(dest.Tools, tool.Name)
				}
			}
		}
	}
	// Connections are project-scoped metadata; never request their credentials.
	connections, err := a.ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{ProjectID: a.ctx.CurrentProject()})
	if err == nil {
		names := map[string]string{}
		if integrations, err := rt.ListRuntimeCatalogIntegrations(); err == nil {
			for _, item := range integrations {
				names[item.Slug] = item.Name
			}
		}
		for _, connection := range connections {
			if connection.AppSlug == "" {
				continue
			}
			key := "integration:" + connection.AppSlug
			if dest := byID[key]; dest != nil {
				if connection.Status == "connected" || connection.Status == "active" {
					dest.Status = connection.Status
				}
				continue
			}
			name := names[connection.AppSlug]
			if name == "" {
				name = connection.AppSlug
			}
			dest := &SceneDestination{ID: key, Kind: "integration", Name: name, Status: connection.Status}
			byID[key] = dest
			if tools, err := rt.ListRuntimeCatalogIntegrationTools(connection.AppSlug); err == nil {
				for _, tool := range tools {
					if tool.Name != "" {
						dest.Tools = append(dest.Tools, tool.Name)
					}
				}
			}
		}
	}
	return destinationValues(byID)
}

func (a *App) runtimeDestinations(scene *Scene) []SceneDestination {
	byID := map[string]*SceneDestination{}
	runtimeID := strings.TrimPrefix(scene.Source.ID, "runtime:")
	for _, app := range scene.Apps {
		dest := &SceneDestination{ID: "app:" + app.ID, Kind: "app", Name: app.Name, Status: app.Status}
		if tools, err := a.ctx.RuntimeAPI().ListRuntimeAppTools(runtimeID, app.ID); err == nil {
			for _, tool := range tools {
				if tool.Name != "" {
					dest.Tools = append(dest.Tools, tool.Name)
				}
			}
		}
		byID[dest.ID] = dest
	}
	return destinationValues(byID)
}

func (a *App) remoteDestinations() []SceneDestination {
	project := strings.TrimSpace(a.ctx.Config().Get("remote_project_id"))
	query := ""
	if project != "" {
		query = "?project_id=" + url.QueryEscape(project)
	}
	var apps []struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Status      string `json:"status"`
		Surfaces    struct {
			MCPToolNames []string `json:"mcp_tool_names"`
		} `json:"surfaces"`
	}
	byID := map[string]*SceneDestination{}
	if a.remoteGET("/api/apps"+query, &apps) == nil {
		for _, app := range apps {
			if app.Name == "" || app.Name == "agent-worlds" {
				continue
			}
			name := app.DisplayName
			if name == "" {
				name = app.Name
			}
			byID["app:"+app.Name] = &SceneDestination{ID: "app:" + app.Name, Kind: "app", Name: name, Status: app.Status, Tools: app.Surfaces.MCPToolNames}
		}
	}
	var connections []struct {
		AppSlug string `json:"app_slug"`
		Status  string `json:"status"`
	}
	if a.remoteGET("/api/connections"+query, &connections) == nil {
		for _, connection := range connections {
			if connection.AppSlug == "" {
				continue
			}
			key := "integration:" + connection.AppSlug
			byID[key] = &SceneDestination{ID: key, Kind: "integration", Name: connection.AppSlug, Status: connection.Status}
		}
	}
	return destinationValues(byID)
}

func destinationValues(byID map[string]*SceneDestination) []SceneDestination {
	out := make([]SceneDestination, 0, len(byID))
	for _, dest := range byID {
		seen := map[string]bool{}
		tools := make([]string, 0, len(dest.Tools))
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
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (a *App) enrichScene(scene *Scene) {
	scene.Destinations = a.destinationCatalog(scene)
	attributeEvents(scene)
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
			event.TargetID = "other:tools"
			if event.Kind == "tool" {
				counts[event.TargetID]++
				if event.Time.After(last[event.TargetID]) {
					last[event.TargetID] = event.Time
				}
			}
		}
	}
	if unknown {
		scene.Destinations = append(scene.Destinations, SceneDestination{ID: "other:tools", Kind: "other", Name: "Other tools", Status: "observed"})
	}
	for i := range scene.Destinations {
		dest := &scene.Destinations[i]
		dest.CallCount = counts[dest.ID]
		if at, ok := last[dest.ID]; ok {
			copy := at
			dest.LastCall = &copy
		}
	}
}
