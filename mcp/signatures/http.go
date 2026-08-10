package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) handleEnvelopes(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	if ctx == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("app is not mounted"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		envelopes, err := listEnvelopes(ctx.AppDB(), ctx.CurrentProject(), strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status"))), queryInt(r, "limit", 50))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"envelopes": envelopes})
	case http.MethodPost:
		args, err := requestArgs(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		out, err := a.toolEnvelopeCreate(ctx, args)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) handleEnvelopeItem(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	if ctx == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("app is not mounted"))
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/envelopes/"), "/")
	parts := strings.Split(rest, "/")
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		writeError(w, http.StatusBadRequest, errors.New("envelope id required"))
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if r.Method == http.MethodGet && action == "" {
		detail, err := getEnvelopeDetail(ctx.AppDB(), ctx.CurrentProject(), id, true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"envelope": detail})
		return
	}
	args, err := requestArgsOptional(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	args["envelope_id"] = id
	var out any
	switch {
	case r.Method == http.MethodPatch && action == "":
		out, err = a.toolEnvelopeUpdate(ctx, args)
	case r.Method == http.MethodPost && action == "recipients":
		out, err = a.toolRecipientsSet(ctx, args)
	case r.Method == http.MethodPost && action == "fields":
		out, err = a.toolFieldsSet(ctx, args)
	case r.Method == http.MethodPost && action == "validate":
		out, err = a.toolEnvelopeValidate(ctx, args)
	case r.Method == http.MethodPost && action == "send":
		out, err = a.toolEnvelopeSend(ctx, args)
	case r.Method == http.MethodPost && action == "link":
		out, err = a.toolRecipientLinkCreate(ctx, args)
	case r.Method == http.MethodPost && action == "remind":
		out, err = a.toolEnvelopeRemind(ctx, args)
	case r.Method == http.MethodPost && action == "void":
		out, err = a.toolEnvelopeVoid(ctx, args)
	case r.Method == http.MethodPost && action == "finalize":
		out, err = a.toolEnvelopeFinalize(ctx, args)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func requestArgs(r *http.Request) (map[string]any, error) {
	args := map[string]any{}
	dec := json.NewDecoder(io.LimitReader(r.Body, (1<<20)+1))
	dec.UseNumber()
	if err := dec.Decode(&args); err != nil {
		return nil, err
	}
	return args, nil
}

func requestArgsOptional(r *http.Request) (map[string]any, error) {
	if r.Body == nil || r.ContentLength == 0 {
		return map[string]any{}, nil
	}
	return requestArgs(r)
}

func queryInt(r *http.Request, key string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil || n == 0 {
		return fallback
	}
	return n
}
