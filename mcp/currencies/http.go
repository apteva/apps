package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) handleCurrencies(w http.ResponseWriter, r *http.Request) {
	args := queryArgs(r, "q", "kind", "active", "limit")
	if raw := r.URL.Query().Get("active"); raw != "" {
		args["active"] = raw == "true" || raw == "1"
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		args["limit"], _ = strconv.Atoi(raw)
	}
	out, err := a.toolCurrenciesList(httpAppCtx(r, args), args)
	writeHTTPResult(w, out, err)
}

func (a *App) handleRates(w http.ResponseWriter, r *http.Request) {
	args := selectionQueryArgs(r)
	out, err := a.toolRateGet(httpAppCtx(r, args), args)
	writeHTTPResult(w, out, err)
}

func (a *App) handleHistory(w http.ResponseWriter, r *http.Request) {
	args := queryArgs(r, "base", "quote", "from", "to", "providers", "rate_kinds", "limit")
	if raw := r.URL.Query().Get("providers"); raw != "" {
		args["providers"] = splitCSV(raw)
	}
	if raw := r.URL.Query().Get("rate_kinds"); raw != "" {
		args["rate_kinds"] = splitCSV(raw)
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		args["limit"], _ = strconv.Atoi(raw)
	}
	out, err := a.toolRatesHistory(httpAppCtx(r, args), args)
	writeHTTPResult(w, out, err)
}

func (a *App) handleConvert(w http.ResponseWriter, r *http.Request) {
	args, err := decodeHTTPArgs(r)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, err)
		return
	}
	out, err := a.toolConvert(httpAppCtx(r, args), args)
	writeHTTPResult(w, out, err)
}

func (a *App) handleManualRate(w http.ResponseWriter, r *http.Request) {
	args, err := decodeHTTPArgs(r)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, err)
		return
	}
	out, err := a.toolRateSetManual(httpAppCtx(r, args), args)
	writeHTTPResult(w, out, err)
}

func (a *App) handleSources(w http.ResponseWriter, r *http.Request) {
	args := queryArgs(r)
	out, err := a.toolSourcesStatus(httpAppCtx(r, args), args)
	writeHTTPResult(w, out, err)
}

func (a *App) handleSync(w http.ResponseWriter, r *http.Request) {
	args, err := decodeHTTPArgs(r)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, err)
		return
	}
	out, err := a.toolSyncNow(httpAppCtx(r, args), args)
	writeHTTPResult(w, out, err)
}

func selectionQueryArgs(r *http.Request) map[string]any {
	args := queryArgs(r, "base", "quote", "as_of", "selection", "rate_kinds", "providers",
		"max_age_seconds", "allow_inverse", "allow_triangulation", "allow_stale", "fetch")
	for _, key := range []string{"rate_kinds", "providers"} {
		if raw := r.URL.Query().Get(key); raw != "" {
			args[key] = splitCSV(raw)
		}
	}
	for _, key := range []string{"allow_inverse", "allow_triangulation", "allow_stale", "fetch"} {
		if raw := r.URL.Query().Get(key); raw != "" {
			if v, err := strconv.ParseBool(raw); err == nil {
				args[key] = v
			}
		}
	}
	if raw := r.URL.Query().Get("max_age_seconds"); raw != "" {
		args["max_age_seconds"], _ = strconv.Atoi(raw)
	}
	return args
}

func queryArgs(r *http.Request, keys ...string) map[string]any {
	args := map[string]any{}
	for _, key := range keys {
		if v := strings.TrimSpace(r.URL.Query().Get(key)); v != "" {
			args[key] = v
		}
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		args["_project_id"] = pid
	}
	return args
}

func decodeHTTPArgs(r *http.Request) (map[string]any, error) {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.UseNumber()
	args := map[string]any{}
	if err := dec.Decode(&args); err != nil {
		return nil, err
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		args["_project_id"] = pid
	}
	return args, nil
}

func httpAppCtx(r *http.Request, args map[string]any) *sdk.AppCtx {
	if globalCtx == nil {
		return nil
	}
	pid := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if pid == "" {
		pid, _ = args["_project_id"].(string)
	}
	if pid != "" {
		return globalCtx.WithProject(pid)
	}
	return globalCtx
}

func writeHTTPResult(w http.ResponseWriter, out any, err error) {
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errRateMissing) || strings.Contains(err.Error(), errRateMissing.Error()) {
			status = http.StatusNotFound
		}
		writeHTTPError(w, status, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

func writeHTTPError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
}
