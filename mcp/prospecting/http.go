package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	ctx := requestCtx(r)
	out, err := a.toolOverview(ctx, nil)
	respond(w, out, err)
}

func (a *App) handleProfiles(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolProfilesList(ctx, map[string]any{"status": r.URL.Query().Get("status")})
		respond(w, out, err)
	case http.MethodPost:
		args, err := decodeBody(r)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		out, err := a.toolProfilesCreate(ctx, args)
		respond(w, out, err)
	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleProfileItem(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	id, action := pathIDAction(r.URL.Path, "/profiles/")
	if id == 0 {
		http.Error(w, "profile id required", http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		out, err := a.toolProfilesGet(ctx, map[string]any{"id": id})
		respond(w, out, err)
	case r.Method == http.MethodPatch && action == "":
		args, err := decodeBody(r)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		args["id"] = id
		out, err := a.toolProfilesUpdate(ctx, args)
		respond(w, out, err)
	case r.Method == http.MethodPost && action == "archive":
		out, err := a.toolProfilesArchive(ctx, map[string]any{"id": id})
		respond(w, out, err)
	default:
		http.Error(w, "unsupported profile operation", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	args, err := decodeBody(r)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	out, err := a.toolSearchRun(requestCtx(r), args)
	respond(w, out, err)
}

func (a *App) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	args := map[string]any{
		"profile_id": queryInt64(r, "profile_id"),
		"limit":      queryInt64(r, "limit"),
	}
	out, err := a.toolRunsList(requestCtx(r), args)
	respond(w, out, err)
}

func (a *App) handleCandidates(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{
			"profile_id": queryInt64(r, "profile_id"),
			"status":     r.URL.Query().Get("status"),
			"q":          r.URL.Query().Get("q"),
			"limit":      queryInt64(r, "limit"),
			"offset":     queryInt64(r, "offset"),
		}
		out, err := a.toolCandidatesSearch(ctx, args)
		respond(w, out, err)
	case http.MethodPost:
		args, err := decodeBody(r)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		out, err := a.toolCandidatesCreate(ctx, args)
		respond(w, out, err)
	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleCandidateItem(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	id, action := pathIDAction(r.URL.Path, "/candidates/")
	if id == 0 {
		http.Error(w, "candidate id required", http.StatusBadRequest)
		return
	}
	args := map[string]any{"id": id}
	switch {
	case r.Method == http.MethodGet && action == "":
		out, err := a.toolCandidatesGet(ctx, args)
		respond(w, out, err)
	case r.Method == http.MethodPatch && action == "":
		body, err := decodeBody(r)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		body["id"] = id
		out, err := a.toolCandidatesUpdate(ctx, body)
		respond(w, out, err)
	case r.Method == http.MethodPost && action == "research":
		body, err := decodeOptionalBody(r)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		body["id"] = id
		out, err := a.toolCandidatesResearch(ctx, body)
		respond(w, out, err)
	case r.Method == http.MethodPost && action == "defer":
		body, err := decodeOptionalBody(r)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		body["id"] = id
		out, err := a.toolCandidatesDefer(ctx, body)
		respond(w, out, err)
	case r.Method == http.MethodPost && action == "reject":
		body, err := decodeOptionalBody(r)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		body["id"] = id
		out, err := a.toolCandidatesReject(ctx, body)
		respond(w, out, err)
	case r.Method == http.MethodPost && action == "accept":
		body, err := decodeOptionalBody(r)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		body["id"] = id
		out, err := a.toolCandidatesAccept(ctx, body)
		respond(w, out, err)
	default:
		http.Error(w, "unsupported candidate operation", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleExclusions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	out, err := a.toolExclusionsList(requestCtx(r), map[string]any{
		"kind": r.URL.Query().Get("kind"), "limit": queryInt64(r, "limit"),
	})
	respond(w, out, err)
}

func (a *App) handleExclusionItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE required", http.StatusMethodNotAllowed)
		return
	}
	id, _ := pathIDAction(r.URL.Path, "/exclusions/")
	if id == 0 {
		http.Error(w, "exclusion id required", http.StatusBadRequest)
		return
	}
	out, err := a.toolExclusionsRemove(requestCtx(r), map[string]any{"id": id})
	respond(w, out, err)
}

func respond(w http.ResponseWriter, out any, err error) {
	if err != nil {
		status := http.StatusBadRequest
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "unavailable") || strings.Contains(message, "web search") || strings.Contains(message, "web research") || strings.Contains(message, "crm handoff") {
			status = http.StatusServiceUnavailable
		}
		writeError(w, err, status)
		return
	}
	writeJSON(w, out)
}

func decodeBody(r *http.Request) (map[string]any, error) {
	var body map[string]any
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		return nil, err
	}
	if body == nil {
		body = map[string]any{}
	}
	return body, nil
}

func decodeOptionalBody(r *http.Request) (map[string]any, error) {
	if r.Body == nil || r.ContentLength == 0 {
		return map[string]any{}, nil
	}
	return decodeBody(r)
}

func pathIDAction(path, prefix string) (int64, string) {
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	parts := strings.Split(rest, "/")
	id := parseID(parts[0])
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	return id, action
}

func queryInt64(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get(key)), 10, 64)
	return n
}
