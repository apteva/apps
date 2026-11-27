package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func (a *App) handleProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	profiles := make([]map[string]any, 0, 4)
	for _, profile := range []struct{ key, label, config string }{
		{"go", "Go", "go_image"},
		{"bun", "Bun / TypeScript", "bun_image"},
		{"python", "Python", "python_image"},
		{"apteva", "Apteva (Go + Bun + Python)", "apteva_image"},
	} {
		image := configString(globalCtx, profile.config, "")
		if profile.key == "go" && image == "" {
			image = "golang:1.25-bookworm"
		}
		if profile.key == "bun" && image == "" {
			image = "oven/bun:1-debian"
		}
		if profile.key == "python" && image == "" {
			image = "python:3.13-bookworm"
		}
		entry := map[string]any{"key": profile.key, "label": profile.label, "image": image, "available": image != ""}
		if image == "" {
			entry["reason"] = "no image is set for the containers driver (" + profile.key + "_image); the local driver accepts it as a label"
		}
		// On the local driver a profile is only a label; say whether its
		// toolchain is on this machine.
		if tool := profileTool(profile.key); tool != "" {
			_, err := exec.LookPath(tool)
			entry["local_tool"], entry["local_tool_installed"] = tool, err == nil
		}
		profiles = append(profiles, entry)
	}
	localDriver := map[string]any{"key": driverLocal, "label": "This machine (sandbox)", "available": true}
	if err := localDriverAvailable(globalCtx); err != nil {
		localDriver["available"], localDriver["reason"] = false, err.Error()
	} else if driver, err := localRuntime.get(globalCtx); err == nil {
		if kind, _ := driver.mode(); kind == sandboxContainer {
			localDriver["label"], localDriver["sandbox"] = "This container (own user)", string(kind)
			if exposed := containerExposures(); len(exposed) > 0 {
				localDriver["warning"] = "Workspace users can read " + strings.Join(exposed, ", ") + "; make them readable by their owner only (chmod 600) or the data folder 700."
			}
		} else {
			localDriver["sandbox"] = string(kind)
		}
	}
	containersDriver := map[string]any{"key": driverContainers, "label": "Docker container", "available": true}
	if !containersInstalled(globalCtx) {
		containersDriver["available"], containersDriver["reason"] = false, "the Containers app is not installed"
	}
	drivers := []map[string]any{containersDriver, localDriver}
	writeJSON(w, http.StatusOK, map[string]any{
		"profiles": profiles, "default": configString(globalCtx, "default_profile", "go"),
		"drivers": drivers, "default_driver": defaultDriver(globalCtx),
	})
}

func (a *App) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	actor := operatorActor(globalCtx)
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		rows, err := listWorkspacesFiltered(globalCtx.AppDB(), actor.ProjectID, workspaceFilter{
			Status: q.Get("status"), IncludeDestroyed: q.Get("include_destroyed") == "1", Limit: queryInt(r, "limit", 100),
			Kind: q.Get("kind"), Host: q.Get("host"),
			OwnerAgentID: int64(queryInt(r, "owner_agent_id", 0)), OwnerThreadID: strings.TrimSpace(q.Get("owner_thread_id")),
		})
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		previews, err := listProjectPreviews(globalCtx.AppDB(), actor.ProjectID)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		var buckets map[string][]int
		if includes(q.Get("include"), "activity_buckets") {
			if buckets, err = activityBucketsFor(globalCtx.AppDB(), actor.ProjectID, time.Now().UTC()); err != nil {
				writeHTTPError(w, err)
				return
			}
		}
		for _, workspace := range rows {
			active, _ := listActiveCommands(globalCtx.AppDB(), workspace.ID)
			if len(active) > 0 {
				workspace.CurrentCommand = active[0]
			}
			workspace.Preview = previews[workspace.ID]
			if buckets != nil {
				workspace.ActivityBuckets = buckets[workspace.ID]
				if workspace.ActivityBuckets == nil {
					workspace.ActivityBuckets = make([]int, activityBuckets)
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"workspaces": rows, "count": len(rows), "summary": workspaceSummary(rows)})
	case http.MethodPost:
		var args map[string]any
		if err := decodeBody(r, &args); err != nil {
			writeHTTPError(w, err)
			return
		}
		if strArg(args, "owner_label") == "" {
			args["owner_label"] = "Operator"
		}
		workspace, err := a.createWorkspace(r.Context(), globalCtx, args, false)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"workspace": workspace})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// handleActivity is the project-wide activity feed for the overview widget.
func (a *App) handleActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	actor := operatorActor(globalCtx)
	q := r.URL.Query()
	lines, err := listProjectActivity(globalCtx.AppDB(), actor.ProjectID, activityQuery{
		Limit: queryInt(r, "limit", 5), Kinds: q.Get("kinds"), IncludeTest: q.Get("include_test") == "1",
		WorkspaceID: strings.TrimSpace(q.Get("workspace_id")),
	})
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"activity": lines, "count": len(lines)})
}

func includes(list, item string) bool {
	for _, part := range strings.Split(list, ",") {
		if strings.TrimSpace(part) == item {
			return true
		}
	}
	return false
}

func (a *App) handleWorkspaceItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/workspaces/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "workspace id required", http.StatusBadRequest)
		return
	}
	actor := operatorActor(globalCtx)
	workspace, err := requireWorkspace(globalCtx.AppDB(), actor.ProjectID, parts[0])
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		result, err := a.workspaceDetail(globalCtx, workspace)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		result["destroy_risk"] = describeDestroyRisk(workspace)
		writeJSON(w, http.StatusOK, result)
	case r.Method == http.MethodPost && action == "stop":
		workspace, err = a.stopWorkspace(globalCtx, actor, workspace, "workspace.suspended")
		writeWorkspaceResult(w, workspace, err)
	case r.Method == http.MethodPost && action == "resume":
		workspace, err = a.resumeWorkspace(globalCtx, actor, workspace)
		writeWorkspaceResult(w, workspace, err)
	case r.Method == http.MethodPost && action == "extend":
		var args map[string]any
		if err := decodeBody(r, &args); err != nil {
			writeHTTPError(w, err)
			return
		}
		workspace, err = a.extendWorkspace(globalCtx, actor, workspace, intArg(args, "ttl_minutes", 0))
		writeWorkspaceResult(w, workspace, err)
	case r.Method == http.MethodDelete && action == "":
		if r.URL.Query().Get("confirm") != "1" {
			writeHTTPError(w, errors.New("confirm=1 is required because destruction permanently deletes workspace volumes"))
			return
		}
		workspace, err = a.destroyWorkspace(globalCtx, actor, workspace)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"workspace": workspace, "destroyed": true})
	case action == "commands":
		a.handleWorkspaceCommands(w, r, actor, workspace, parts[2:])
	case r.Method == http.MethodGet && action == "activity":
		rows, err := listActivity(globalCtx.AppDB(), workspace.ProjectID, workspace.ID, queryInt(r, "limit", 100))
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"activity": rows, "count": len(rows)})
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (a *App) handleWorkspaceCommands(w http.ResponseWriter, r *http.Request, actor Actor, workspace *Workspace, rest []string) {
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			commands, err := listCommands(globalCtx.AppDB(), workspace.ProjectID, workspace.ID, queryInt(r, "limit", 100))
			if err != nil {
				writeHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"commands": commands, "count": len(commands)})
		case http.MethodPost:
			var args map[string]any
			if err := decodeBody(r, &args); err != nil {
				writeHTTPError(w, err)
				return
			}
			args["workspace_id"] = workspace.ID
			command, err := a.startCommand(r.Context(), globalCtx, args, actor, workspace)
			if err != nil {
				writeHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"command": command})
		default:
			http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		}
		return
	}
	command, err := requireCommand(globalCtx.AppDB(), workspace.ProjectID, workspace.ID, rest[0])
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	action := ""
	if len(rest) > 1 {
		action = rest[1]
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		command, err = a.refreshCommand(globalCtx, command)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"command": command})
	case r.Method == http.MethodGet && action == "logs":
		_, _ = a.refreshCommand(globalCtx, command)
		result, err := a.commandLogs(globalCtx, command, queryInt(r, "tail", 500))
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case r.Method == http.MethodPost && action == "cancel":
		command, err = a.cancelCommand(globalCtx, actor, workspace, command)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"command": command})
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func writeWorkspaceResult(w http.ResponseWriter, workspace *Workspace, err error) {
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace": workspace})
}

func decodeBody(r *http.Request, out any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	if err := decoder.Decode(out); err != nil {
		return errors.New("invalid JSON: " + err.Error())
	}
	return nil
}

func queryInt(r *http.Request, key string, fallback int) int {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func writeHTTPError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if strings.Contains(strings.ToLower(err.Error()), "not found") {
		status = http.StatusNotFound
	}
	http.Error(w, err.Error(), status)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
