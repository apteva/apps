// HTTP handler for visitor-submitted core/form blocks.
//
// Path shape: /_forms/submit/<block_id>
//
// Responses follow the form partial's progressive enhancement: when
// the request asks for JSON (Accept header or Content-Type), respond
// with { ok, success | error } so the inline script can swap the
// form for the success state or surface an error. Noscript clients
// get a 303 redirect — to success.url when configured, otherwise
// back to the form's page with no flash (server-side flash needs
// session infra we don't have yet; v1.1 candidate).

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func (a *App) handleFormSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	ctx := getAppCtx(r)
	pid, err := publicProject(r)
	if err != nil {
		respondFormError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	blockID := strings.TrimPrefix(r.URL.Path, "/_forms/submit/")
	blockID = strings.Trim(blockID, "/")
	if blockID == "" {
		respondFormError(w, r, http.StatusBadRequest, "block id required")
		return
	}

	payload, err := readFormPayload(r)
	if err != nil {
		respondFormError(w, r, http.StatusBadRequest, "invalid payload")
		return
	}

	// Honeypot: if a bot filled the hidden 'website' field, drop the
	// submission silently and respond with a synthetic success so the
	// bot doesn't learn it was caught. Audit row records the rejection.
	if hp, _ := payload["website"].(string); strings.TrimSpace(hp) != "" {
		_, _ = dbInsertFormSubmission(ctx.AppDB(), FormSubmission{
			ProjectID: pid,
			BlockID:   blockID,
			Payload:   payload,
			IPHash:    extractIPHash(r),
			UserAgent: r.UserAgent(),
			Status:    "rejected_honeypot",
			Results:   []ActionResult{},
			CreatedAt: nowUnix(),
		})
		respondFormOK(w, r, nil, map[string]any{"kind": "inline", "message": "Thanks!"})
		return
	}
	delete(payload, "website")

	ipHash := extractIPHash(r)
	if !formRateLimitOK(ipHash) {
		respondFormError(w, r, http.StatusTooManyRequests, "too many submissions — please try again later")
		return
	}

	post, block, err := dbFindFormBlock(ctx.AppDB(), pid, blockID)
	if err != nil || block == nil {
		respondFormError(w, r, http.StatusNotFound, "form not found")
		return
	}
	if block.Type != "core/form" {
		respondFormError(w, r, http.StatusBadRequest, "not a form block")
		return
	}

	fields, _ := block.Attrs["fields"].([]any)
	if err := validateFormPayload(fields, payload); err != nil {
		_, _ = dbInsertFormSubmission(ctx.AppDB(), FormSubmission{
			ProjectID: pid, SiteID: post.SiteID, PostID: post.ID, BlockID: blockID,
			Payload: payload, IPHash: ipHash, UserAgent: r.UserAgent(),
			Status: "rejected_validation", Results: []ActionResult{},
			Error: err.Error(), CreatedAt: nowUnix(),
		})
		respondFormError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	actions, _ := block.Attrs["actions"].([]any)
	onFailure, _ := block.Attrs["on_failure"].(string)
	if onFailure == "" {
		onFailure = "abort"
	}
	results, status, runErr := runFormActions(ctx, pid, actions, payload, onFailure)

	sub := FormSubmission{
		ProjectID: pid,
		SiteID:    post.SiteID,
		PostID:    post.ID,
		BlockID:   blockID,
		Payload:   payload,
		IPHash:    ipHash,
		UserAgent: r.UserAgent(),
		Status:    status,
		Results:   results,
		CreatedAt: nowUnix(),
	}
	if runErr != nil {
		sub.Error = runErr.Error()
	}
	subID, _ := dbInsertFormSubmission(ctx.AppDB(), sub)

	ctx.EmitWithProject("form.submitted", pid, map[string]any{
		"submission_id": subID,
		"post_id":       post.ID,
		"block_id":      blockID,
		"status":        status,
	})

	if runErr != nil {
		// Abort-mode failure — return the error to the visitor. The
		// row is already persisted so the panel can show what failed.
		respondFormError(w, r, http.StatusBadGateway, runErr.Error())
		return
	}

	success, _ := block.Attrs["success"].(map[string]any)
	respondFormOK(w, r, results, success)
}

// readFormPayload accepts either JSON or form-urlencoded bodies.
// FormData submitted by the inline script comes as JSON; noscript
// form-POSTs come as form-urlencoded.
func readFormPayload(r *http.Request) (map[string]any, error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var out map[string]any
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if len(body) == 0 {
			return map[string]any{}, nil
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, err
		}
		if out == nil {
			out = map[string]any{}
		}
		return out, nil
	}
	// form-urlencoded or multipart.
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for k, v := range r.PostForm {
		if len(v) == 1 {
			out[k] = v[0]
		} else if len(v) > 1 {
			vs := make([]any, len(v))
			for i, s := range v {
				vs[i] = s
			}
			out[k] = vs
		}
	}
	return out, nil
}

func wantsJSON(r *http.Request) bool {
	a := r.Header.Get("Accept")
	c := r.Header.Get("Content-Type")
	return strings.Contains(a, "application/json") || strings.HasPrefix(c, "application/json")
}

func respondFormOK(w http.ResponseWriter, r *http.Request, results []ActionResult, success map[string]any) {
	if success == nil {
		success = map[string]any{"kind": "inline", "message": "Thanks!"}
	}
	if !wantsJSON(r) {
		// noscript path: redirect to success.url if present, else
		// back to Referer (or "/" as a last resort).
		if kind, _ := success["kind"].(string); kind == "redirect" {
			if u, _ := success["url"].(string); u != "" {
				http.Redirect(w, r, u, http.StatusSeeOther)
				return
			}
		}
		dest := r.Referer()
		if dest == "" {
			dest = "/"
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"success": success,
		"results": results,
	})
}

func respondFormError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if !wantsJSON(r) {
		http.Error(w, msg, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

func nowUnix() int64 { return time.Now().Unix() }
