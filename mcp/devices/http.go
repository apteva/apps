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
	routes := []sdk.Route{
		{Pattern: "/summary", Handler: a.handleSummary},
		{Pattern: "/broker", Handler: a.handleBroker},
		{Pattern: "/devices", Handler: a.handleDevices},
		{Pattern: "/devices/", Handler: a.handleDevice},
		{Pattern: "/commands", Handler: a.handleCommands},
		{Pattern: "/commands/", Handler: a.handleCommand},
	}
	routes = append(routes, a.extraRoutes()...)
	for i := range routes {
		routes[i].Handler = a.scopedHTTP(routes[i].Handler)
	}
	return routes
}

func (a *App) handleSummary(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	var total, online, offline, disabled, commands, failed int
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	defer tx.Rollback()
	err = tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(status='online'),0),COALESCE(SUM(status='offline'),0),COALESCE(SUM(enabled=0),0) FROM devices WHERE project_id=?`, a.projectID).Scan(&total, &online, &offline, &disabled)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	cutoff := formatTime(time.Now().Add(-24 * time.Hour))
	err = tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(status IN ('failed','timed_out')),0) FROM device_commands WHERE device_id IN(SELECT id FROM devices WHERE project_id=?) AND created_at>=?`, a.projectID, cutoff).Scan(&commands, &failed)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"devices": total, "online": online, "offline": offline, "disabled": disabled, "commands_24h": commands, "failed_commands_24h": failed})
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
		if !idempotencyHeader(w, r, input) {
			return
		}
		result, err := a.provision(input)
		if err != nil {
			respond(w, nil, err)
			return
		}
		status := http.StatusCreated
		if str(result, "status") == "pending" {
			status = http.StatusAccepted
		}
		writeJSON(w, status, result)
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
			value, err := a.scopedCommands(id, r.URL.Query().Get("status"), limit)
			respond(w, value, err)
			return
		}
		if r.Method == http.MethodPost {
			var input map[string]any
			if !decodeBody(w, r, &input) {
				return
			}
			input["device_id"] = id
			if !idempotencyHeader(w, r, input) {
				return
			}
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
		value, err := a.toolRotateSecret(a.ctx, map[string]any{"device_id": id, "idempotency_key": r.Header.Get("Idempotency-Key")})
		respond(w, value, err)
	case "enable":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		value, err := a.lifecycle("enable", map[string]any{"device_id": id, "idempotency_key": r.Header.Get("Idempotency-Key")})
		respond(w, value, err)
	case "disable":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		value, err := a.lifecycle("disable", map[string]any{"device_id": id, "idempotency_key": r.Header.Get("Idempotency-Key")})
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
		result, err := a.updateMetadata(id, patch)
		respond(w, result, err)
	case http.MethodDelete:
		value, err := a.toolDelete(a.ctx, map[string]any{"device_id": id, "confirm": true, "idempotency_key": r.Header.Get("Idempotency-Key")})
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
	value, err := a.scopedCommands(r.URL.Query().Get("device_id"), r.URL.Query().Get("status"), limit)
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
	if value, ok := out.(*map[string]any); ok && *value == nil {
		writeError(w, 400, errors.New("body must be a JSON object"))
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
		if errors.Is(err, ErrCursorExpired) {
			status = http.StatusGone
		}
		if errors.Is(err, ErrConflict) {
			status = http.StatusConflict
		}
		if errors.Is(err, ErrLimit) {
			status = http.StatusTooManyRequests
		}
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	status := http.StatusOK
	if result, ok := value.(map[string]any); ok {
		if str(result, "status") == "pending" {
			status = http.StatusAccepted
		}
		if c, ok := result["command"].(Command); ok && !commandTerminal(c.Status) {
			status = http.StatusAccepted
		}
	}
	writeJSON(w, status, value)
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

func idempotencyHeader(w http.ResponseWriter, r *http.Request, args map[string]any) bool {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return true
	}
	if existing := str(args, "idempotency_key"); existing != "" && existing != key {
		writeError(w, 409, ErrConflict)
		return false
	}
	args["idempotency_key"] = key
	return true
}
