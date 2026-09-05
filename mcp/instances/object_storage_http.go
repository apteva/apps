package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (a *App) handleObjectStorageProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	result, err := a.toolObjectStorageListProviders(appCtxForRequest(r), map[string]any{})
	if err != nil {
		httpProviderErr(w, err)
		return
	}
	httpJSON(w, result)
}

func (a *App) handleObjectStoragePlans(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	args := map[string]any{"provider": r.URL.Query().Get("provider")}
	if id, err := parseID(r.URL.Query().Get("provider_connection_id")); err == nil {
		args["provider_connection_id"] = id
	}
	result, err := a.toolObjectStorageListPlans(appCtxForRequest(r), args)
	if err != nil {
		httpProviderErr(w, err)
		return
	}
	httpJSON(w, result)
}

func (a *App) handleObjectStoragesCollection(w http.ResponseWriter, r *http.Request) {
	ctx := appCtxForRequest(r)
	switch r.Method {
	case http.MethodGet:
		result, err := a.toolObjectStorageList(ctx, map[string]any{"provider": r.URL.Query().Get("provider")})
		if err != nil {
			httpProviderErr(w, err)
			return
		}
		httpJSON(w, result)
	case http.MethodPost:
		args := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		result, err := a.toolObjectStorageCreate(ctx, args)
		if err != nil {
			httpProviderErr(w, err)
			return
		}
		httpJSON(w, result)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleObjectStorageItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/object-storage/")
	parts := strings.SplitN(rest, "/", 2)
	id, err := parseID(parts[0])
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid object-storage id")
		return
	}
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	ctx := appCtxForRequest(r)
	args := map[string]any{"id": id}
	switch tail {
	case "":
		if r.Method == http.MethodGet {
			result, callErr := a.toolObjectStorageGet(ctx, args)
			if callErr != nil {
				httpProviderErr(w, callErr)
				return
			}
			httpJSON(w, result)
			return
		}
		if r.Method == http.MethodDelete {
			args["confirm"] = r.URL.Query().Get("confirm") == "true"
			result, callErr := a.toolObjectStorageDestroy(ctx, args)
			if callErr != nil {
				httpProviderErr(w, callErr)
				return
			}
			httpJSON(w, result)
			return
		}
		httpErr(w, http.StatusMethodNotAllowed, "GET or DELETE")
	case "rotate-credentials":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		result, callErr := a.toolObjectStorageRotateCredentials(ctx, args)
		if callErr != nil {
			httpProviderErr(w, callErr)
			return
		}
		httpJSON(w, result)
	default:
		httpErr(w, http.StatusNotFound, "no such resource")
	}
}
