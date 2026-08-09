package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) httpRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/summary", Handler: a.handleSummary},
		{Pattern: "/broker", Handler: a.handleBroker},
		{Pattern: "/devices", Handler: a.handleDevices},
		{Pattern: "/devices/", Handler: a.handleDevice},
		{Pattern: "/commands", Handler: a.handleCommands},
		{Pattern: "/commands/", Handler: a.handleCommand},
	}
}

func (a *App) handleSummary(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	var total, online, offline, disabled, commands, failed int
	db := a.ctx.AppDB()
	_ = db.QueryRow(`SELECT COUNT(*) FROM devices WHERE project_id=?`, a.projectID).Scan(&total)
	_ = db.QueryRow(`SELECT COUNT(*) FROM devices WHERE project_id=? AND status='online'`, a.projectID).Scan(&online)
	_ = db.QueryRow(`SELECT COUNT(*) FROM devices WHERE project_id=? AND status='offline'`, a.projectID).Scan(&offline)
	_ = db.QueryRow(`SELECT COUNT(*) FROM devices WHERE project_id=? AND enabled=0`, a.projectID).Scan(&disabled)
	cutoff := formatTime(time.Now().Add(-24 * time.Hour))
	_ = db.QueryRow(`SELECT COUNT(*) FROM device_commands WHERE created_at>=?`, cutoff).Scan(&commands)
	_ = db.QueryRow(`SELECT COUNT(*) FROM device_commands WHERE created_at>=? AND status IN ('failed','timed_out')`, cutoff).Scan(&failed)
	writeJSON(w, http.StatusOK, map[string]any{
		"devices": total, "online": online, "offline": offline, "disabled": disabled,
		"commands_24h": commands, "failed_commands_24h": failed,
	})
}

func (a *App) handleBroker(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if err := a.refreshBrokerStatus(); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, a.currentBroker())
}

func (a *App) handleDevices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := listDevices(a.ctx.AppDB(), a.projectID, r.URL.Query().Get("status"), r.URL.Query().Get("q"), limit)
		respond(w, rows, err)
	case http.MethodPost:
		var input map[string]any
		if !decodeBody(w, r, &input) {
			return
		}
		result, err := a.provision(input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	default:
		methodNotAllowed(w)
	}
}

func (a *App) handleDevice(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/devices/"), "/")
	parts := strings.Split(rest, "/")
	id, _ := url.PathUnescape(parts[0])
	if !validDeviceID(id) {
		writeError(w, http.StatusBadRequest, errors.New("invalid device id"))
		return
	}
	action := ""
	if len(parts) > 1 {
		action = strings.Join(parts[1:], "/")
	}
	if action == "" {
		a.handleDeviceRoot(w, r, id)
		return
	}
	switch action {
	case "state":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		if _, err := getDevice(a.ctx.AppDB(), a.projectID, id, false); err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		value, err := listState(a.ctx.AppDB(), id, r.URL.Query().Get("key"))
		respond(w, value, err)
	case "commands":
		if r.Method == http.MethodGet {
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			value, err := listCommands(a.ctx.AppDB(), id, r.URL.Query().Get("status"), limit)
			respond(w, value, err)
			return
		}
		if r.Method == http.MethodPost {
			var input map[string]any
			if !decodeBody(w, r, &input) {
				return
			}
			input["device_id"] = id
			result, err := a.toolCommandSend(a.ctx, input)
			respond(w, result, err)
			return
		}
		methodNotAllowed(w)
	case "capabilities":
		if !requireMethod(w, r, http.MethodPut) {
			return
		}
		var manifest map[string]any
		if !decodeBody(w, r, &manifest) {
			return
		}
		value, err := a.toolCapabilitiesSet(a.ctx, map[string]any{"device_id": id, "manifest": manifest})
		respond(w, value, err)
	case "capabilities/refresh":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		value, err := a.toolCapabilitiesRefresh(a.ctx, map[string]any{"device_id": id, "wait": true})
		respond(w, value, err)
	case "credentials/rotate":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		value, err := a.toolRotateSecret(a.ctx, map[string]any{"device_id": id})
		respond(w, value, err)
	case "enable":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		value, err := a.setEnabled(id, true)
		respond(w, value, err)
	case "disable":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		value, err := a.setEnabled(id, false)
		respond(w, value, err)
	default:
		writeError(w, http.StatusNotFound, errors.New("route not found"))
	}
}

func (a *App) handleDeviceRoot(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		value, err := getDevice(a.ctx.AppDB(), a.projectID, id, true)
		respond(w, value, err)
	case http.MethodPatch:
		var patch map[string]any
		if !decodeBody(w, r, &patch) {
			return
		}
		result, err := updateDeviceFields(a.ctx.AppDB(), a.projectID, id, patch)
		if err == nil {
			a.ctx.Emit("devices.device.updated", map[string]any{"device_id": id, "changed": "metadata"})
		}
		respond(w, result, err)
	case http.MethodDelete:
		value, err := a.toolDelete(a.ctx, map[string]any{"device_id": id, "confirm": true})
		respond(w, value, err)
	default:
		methodNotAllowed(w)
	}
}

func (a *App) handleCommands(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	value, err := listCommands(a.ctx.AppDB(), r.URL.Query().Get("device_id"), r.URL.Query().Get("status"), limit)
	respond(w, value, err)
}

func (a *App) handleCommand(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	id, _ := url.PathUnescape(strings.Trim(strings.TrimPrefix(r.URL.Path, "/commands/"), "/"))
	c, err := getCommand(a.ctx.AppDB(), id)
	if err == nil {
		_, err = getDevice(a.ctx.AppDB(), a.projectID, c.DeviceID, false)
	}
	respond(w, commandResult(c), err)
}

func decodeBody(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	if err := ensureEOF(dec); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		methodNotAllowed(w)
		return false
	}
	return true
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}
