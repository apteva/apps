package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// HTTP routes mirror the MCP tools: arguments come from the query string,
// the JSON body and path parameters (in that order of precedence, lowest
// first). project_id comes from ?project_id= or the install's project.
func (a *App) HTTPRoutes() []sdk.Route {
	r := func(method, pattern string, h sdk.ToolHandler, pathArgs ...string) sdk.Route {
		return sdk.Route{Method: method, Pattern: pattern, Handler: a.adapt(h, pathArgs...)}
	}
	return []sdk.Route{
		r("GET", "/wallets", a.toolWalletsList),
		r("POST", "/wallets", a.toolWalletEnsure),
		r("GET", "/wallets/lookup", a.toolWalletGet),
		r("GET", "/wallets/{wallet_id}", a.toolWalletGet, "wallet_id"),
		r("PATCH", "/wallets/{wallet_id}", a.toolWalletUpdate, "wallet_id"),
		r("POST", "/wallets/{wallet_id}/freeze", a.toolWalletFreeze, "wallet_id"),
		r("POST", "/wallets/{wallet_id}/unfreeze", a.toolWalletUnfreeze, "wallet_id"),
		r("GET", "/wallets/{wallet_id}/entries", a.toolEntriesList, "wallet_id"),
		r("GET", "/wallets/{wallet_id}/holds", a.toolHoldsList, "wallet_id"),
		r("GET", "/wallets/{wallet_id}/lots", a.toolLotsList, "wallet_id"),
		r("GET", "/wallets/{wallet_id}/topups", a.toolTopupsList, "wallet_id"),
		r("GET", "/wallets/{wallet_id}/caps", a.toolCapsGet, "wallet_id"),
		r("POST", "/wallets/{wallet_id}/caps", a.toolCapsSet, "wallet_id"),
		r("POST", "/grant", a.toolGrant),
		r("POST", "/topups", a.toolTopupCreate),
		r("POST", "/reserve", a.toolReserve),
		r("POST", "/spend", a.toolSpend),
		r("POST", "/holds/{hold_id}/commit", a.toolCommit, "hold_id"),
		r("POST", "/holds/{hold_id}/release", a.toolRelease, "hold_id"),
		r("POST", "/adjust", a.toolAdjust),
		r("POST", "/transfer", a.toolTransfer),
		r("GET", "/shortfalls", a.toolShortfallsList),
		r("POST", "/shortfalls/{id}/resolve", a.toolShortfallResolve, "id"),
		r("GET", "/reconcile", a.toolReconcile),
		r("GET", "/reports/liability", a.toolLiability),
	}
}

func (a *App) adapt(h sdk.ToolHandler, pathArgs ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		args := map[string]any{}
		for k, v := range r.URL.Query() {
			if len(v) > 0 && k != "project_id" {
				args[k] = v[0]
			}
		}
		if r.Body != nil && r.ContentLength != 0 && r.Method != http.MethodGet {
			var body map[string]any
			dec := json.NewDecoder(r.Body)
			if err := dec.Decode(&body); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			for k, v := range body {
				if !strings.HasPrefix(k, "_") {
					args[k] = v
				}
			}
		}
		for _, name := range pathArgs {
			args[name] = r.PathValue(name)
		}
		if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
			args["_project_id"] = pid
		}
		out, err := h(globalCtx, args)
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, errNotFound) {
				code = http.StatusNotFound
			}
			httpErr(w, code, err.Error())
			return
		}
		httpJSON(w, out)
	}
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}
