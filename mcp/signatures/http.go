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

func (a *App) handleSeal(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	if ctx == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("app is not mounted"))
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	out, err := a.toolSealCertificateGet(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSealCertificate serves the seal certificate as DER, the format
// Acrobat's "Import" in Trusted Certificates expects.
func (a *App) handleSealCertificate(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	if ctx == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("app is not mounted"))
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	seal, err := loadSealIdentity(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/pkix-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="signatures-seal.cer"`)
	_, _ = w.Write(seal.Certificate.Raw)
}

func (a *App) handleSealVerify(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	if ctx == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("app is not mounted"))
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	args, err := requestArgs(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	out, err := a.toolSealVerify(ctx, args)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleLayouts(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	if ctx == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("app is not mounted"))
		return
	}
	var out any
	var err error
	switch r.Method {
	case http.MethodGet:
		out, err = a.toolLayoutsList(ctx, nil)
	case http.MethodPost:
		var args map[string]any
		if args, err = requestArgs(r); err == nil {
			if args["envelope_id"] != nil {
				out, err = a.toolLayoutSaveFromEnvelope(ctx, args)
			} else {
				out, err = a.toolLayoutSave(ctx, args)
			}
		}
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

func (a *App) handleLayoutItem(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	if ctx == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("app is not mounted"))
		return
	}
	id, _ := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/layouts/"), "/"), 10, 64)
	if id == 0 {
		writeError(w, http.StatusBadRequest, errors.New("layout id required"))
		return
	}
	var out any
	var err error
	switch r.Method {
	case http.MethodGet:
		out, err = a.toolLayoutGet(ctx, map[string]any{"layout_id": id})
	case http.MethodDelete:
		out, err = a.toolLayoutDelete(ctx, map[string]any{"layout_id": id})
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
