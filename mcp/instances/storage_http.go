package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (a *App) handleStorageCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	args := map[string]any{"provider": r.URL.Query().Get("provider")}
	if id, err := parseID(r.URL.Query().Get("provider_connection_id")); err == nil {
		args["provider_connection_id"] = id
	}
	result, err := a.toolStorageCapabilities(appCtxForRequest(r), args)
	if err != nil {
		httpProviderErr(w, err)
		return
	}
	httpJSON(w, result)
}

func (a *App) handleVolumesCollection(w http.ResponseWriter, r *http.Request) {
	ctx := appCtxForRequest(r)
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{"provider": r.URL.Query().Get("provider")}
		if id, err := parseID(r.URL.Query().Get("instance_id")); err == nil {
			args["instance_id"] = id
		}
		result, err := a.toolVolumeList(ctx, args)
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
		result, err := a.toolVolumeCreate(ctx, args)
		if err != nil {
			httpProviderErr(w, err)
			return
		}
		httpJSON(w, result)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleVolumeItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/instance-volumes/")
	parts := strings.SplitN(rest, "/", 2)
	id, err := parseID(parts[0])
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid volume id")
		return
	}
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	ctx := appCtxForRequest(r)
	args := map[string]any{"id": id}
	decode := func() bool {
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return false
		}
		args["id"] = id
		return true
	}
	var result any
	switch tail {
	case "":
		if r.Method == http.MethodGet {
			result, err = a.toolVolumeGet(ctx, args)
		} else if r.Method == http.MethodDelete {
			args["confirm"] = r.URL.Query().Get("confirm") == "true"
			result, err = a.toolVolumeDelete(ctx, args)
		} else {
			httpErr(w, http.StatusMethodNotAllowed, "GET or DELETE")
			return
		}
	case "attach":
		if r.Method != http.MethodPost || !decode() {
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "POST only")
			}
			return
		}
		result, err = a.toolVolumeAttach(ctx, args)
	case "detach":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		result, err = a.toolVolumeDetach(ctx, args)
	case "resize":
		if r.Method != http.MethodPost || !decode() {
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "POST only")
			}
			return
		}
		result, err = a.toolVolumeResize(ctx, args)
	default:
		httpErr(w, http.StatusNotFound, "no such volume operation")
		return
	}
	if err != nil {
		httpProviderErr(w, err)
		return
	}
	httpJSON(w, result)
}
