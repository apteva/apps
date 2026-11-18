package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) httpRepoVersion(w http.ResponseWriter, r *http.Request, slug, action string) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	repo, err := requireRepoSlug(globalCtx, pid, slug)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	if a.native == nil {
		httpErr(w, http.StatusServiceUnavailable, "native version control is not initialized")
		return
	}
	n := a.native
	switch action {
	case "", "status":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		value, err := n.status(repo)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, value)
	case "checkpoint":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Message      string `json:"message"`
			Actor        string `json:"actor"`
			ExpectedHead string `json:"expected_head"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if body.Message == "" {
			body.Message = "Checkpoint"
		}
		value, err := n.checkpointExpect(repo, body.Message, body.Actor, body.ExpectedHead)
		if errors.Is(err, errNativeRefMoved) || errors.Is(err, errNativeMergeInProgress) || errors.Is(err, errNativeProtected) {
			httpErr(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"revision": value})
	case "history":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		var values []nativeRevision
		if path := q.Get("path"); path != "" {
			_, values, err = n.pathHistory(repo, q.Get("ref"), path, limit)
		} else {
			values, err = n.historyRef(repo, q.Get("ref"), limit)
		}
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"revisions": values, "count": len(values)})
	case "blame":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		head, lines, truncated, err := n.blame(repo, r.URL.Query().Get("ref"), r.URL.Query().Get("path"))
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"revision": head, "lines": lines, "count": len(lines), "truncated": truncated})
	case "diff":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		q := r.URL.Query()
		from, to, files, err := n.lineDiff(repo, q.Get("from"), q.Get("to"), q["path"])
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"from_revision": from, "to_revision": to, "files": files, "count": len(files)})
	case "compare":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		value, err := n.compareRefs(repo, r.URL.Query().Get("from"), r.URL.Query().Get("to"))
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, value)
	case "branches/delete":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Name  string `json:"name"`
			Force bool   `json:"force"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		head, err := n.deleteBranch(repo, body.Name, body.Force, httpActor(r))
		if err != nil {
			httpErr(w, http.StatusConflict, err.Error())
			return
		}
		httpJSON(w, map[string]any{"deleted": body.Name, "head": head})
	case "merge":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		value, err := n.mergeStatus(repo)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, value)
	case "merge/start", "merge/resolve", "merge/continue", "merge/abort":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Source  string  `json:"source"`
			Mode    string  `json:"mode"`
			Message string  `json:"message"`
			Markers bool    `json:"markers"`
			NoFF    bool    `json:"no_ff"`
			Path    string  `json:"path"`
			Choice  string  `json:"choice"`
			Content *string `json:"content"`
		}
		if r.ContentLength != 0 && json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		var value *nativeMergeResult
		switch action {
		case "merge/start":
			value, err = n.mergeStart(repo, nativeMergeOptions{Source: body.Source, Mode: body.Mode, Message: body.Message, Actor: httpActor(r), Markers: body.Markers, NoFF: body.NoFF})
		case "merge/resolve":
			value, err = n.mergeResolve(repo, body.Path, body.Choice, body.Content)
		case "merge/continue":
			value, err = n.mergeContinue(repo, body.Message, httpActor(r))
		default:
			value, err = n.mergeAbort(repo)
		}
		if errors.Is(err, errNativeMergeInProgress) || errors.Is(err, errNativeRefMoved) || errors.Is(err, errNativeProtected) {
			httpErr(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, value)
	case "branches":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		values, err := n.branches(repo)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"branches": values, "count": len(values)})
	case "branches/create":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Name          string `json:"name"`
			StartRevision string `json:"start_revision"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := n.createBranch(repo, body.Name, body.StartRevision); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"ok": true})
	case "switch":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		result, err := n.switchBranchActor(repo, body.Name, httpActor(r))
		if err != nil {
			httpErr(w, http.StatusConflict, err.Error())
			return
		}
		value, _ := n.status(repo)
		httpJSON(w, map[string]any{"native": value.Native, "branch": value.Branch, "head": value.Head, "dirty": value.Dirty, "revision_count": value.Revision, "switch": result})
	case "tags":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		values, err := n.tags(repo)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"tags": values, "count": len(values)})
	case "tags/create":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Name     string `json:"name"`
			Revision string `json:"revision"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := n.createTag(repo, body.Name, body.Revision); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"ok": true})
	case "restore":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Revision        string   `json:"revision"`
			Paths           []string `json:"paths"`
			WorkingTreeOnly bool     `json:"working_tree_only"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		value, err := n.restore(repo, body.Revision, body.Paths, body.WorkingTreeOnly, httpActor(r))
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if body.WorkingTreeOnly {
			status, _ := n.status(repo)
			httpJSON(w, status)
			return
		}
		httpJSON(w, map[string]any{"revision": value})
	case "moves":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		branch, moves, err := n.refMoves(repo, r.URL.Query().Get("branch"), limit)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"branch": branch, "moves": moves, "count": len(moves)})
	case "export":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		revision := r.URL.Query().Get("revision")
		body, sha, err := n.exportRef(repo, revision)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(body) > 32<<20 {
			httpErr(w, http.StatusRequestEntityTooLarge, "native revision export exceeds 32 MiB")
			return
		}
		httpJSON(w, map[string]any{"revision": revision, "sha256": sha, "size": len(body), "zip_b64": base64.StdEncoding.EncodeToString(body)})
	case "change-requests":
		svc := &changeRequestService{db: globalCtx.AppDB(), n: n}
		if r.Method == http.MethodPost {
			var body struct {
				Title  string `json:"title"`
				Body   string `json:"body"`
				Source string `json:"source"`
				Target string `json:"target"`
				Issues []int  `json:"issues"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				httpErr(w, http.StatusBadRequest, "invalid JSON")
				return
			}
			cr, err := svc.create(repo, changeRequestCreate{Title: body.Title, Body: body.Body, Source: body.Source, Target: body.Target, Issues: body.Issues, Author: httpActor(r)})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			d, err := svc.detail(repo, cr.Number, false)
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			httpJSON(w, d)
			return
		}
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET, POST")
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list, err := svc.list(repo, r.URL.Query().Get("state"), limit)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"change_requests": list, "count": len(list)})
	case "sync/refs":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		refs, err := n.serveRefs(repo)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, refs)
	case "sync/fetch":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Wants []string `json:"wants"`
			Haves []string `json:"haves"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		bundle, err := n.serveFetch(repo, body.Wants, body.Haves)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, bundle)
	case "sync/receive":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Bundle  *nativeBundle     `json:"bundle"`
			Updates []nativeRefUpdate `json:"updates"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, nativeBundleMaxBytes+(4<<20))).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON or push too large")
			return
		}
		res, err := n.serveReceive(repo, body.Bundle, body.Updates, httpActor(r))
		if errors.Is(err, errNativeRefMoved) || errors.Is(err, errNativeProtected) || errors.Is(err, errNativeMergeInProgress) {
			httpErr(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, res)
	case "remotes":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		list, err := n.listRemotes(repo)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"remotes": list, "count": len(list)})
	case "protection":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Branch            string `json:"branch"`
			Protected         bool   `json:"protected"`
			RequiredApprovals int    `json:"required_approvals"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		approvals := -1
		if body.Protected {
			approvals = body.RequiredApprovals
		}
		prot, err := n.setProtection(repo, body.Branch, approvals, httpActor(r))
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"branch": body.Branch, "protected": prot != nil, "protection": prot})
	default:
		if strings.HasPrefix(action, "change-requests/") {
			a.httpChangeRequest(w, r, n, repo, strings.TrimPrefix(action, "change-requests/"))
			return
		}
		httpErr(w, http.StatusNotFound, "no such native version-control action")
	}
}

// httpChangeRequest serves version/change-requests/<number>[/<action>]:
// GET for detail (?lines=1 adds patches), POST comment|review|merge|close|reopen.
func (a *App) httpChangeRequest(w http.ResponseWriter, r *http.Request, n *nativeVCS, repo *Repo, rest string) {
	parts := strings.SplitN(rest, "/", 2)
	number, err := strconv.Atoi(parts[0])
	if err != nil || number <= 0 {
		httpErr(w, http.StatusNotFound, "no such change request")
		return
	}
	svc := &changeRequestService{db: globalCtx.AppDB(), n: n}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		d, err := svc.detail(repo, number, r.URL.Query().Get("lines") == "1")
		if err != nil {
			httpErr(w, http.StatusNotFound, err.Error())
			return
		}
		httpJSON(w, d)
		return
	}
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		Body         string `json:"body"`
		Path         string `json:"path"`
		Line         int    `json:"line"`
		Verdict      string `json:"verdict"`
		Mode         string `json:"mode"`
		Message      string `json:"message"`
		DeleteSource bool   `json:"delete_source"`
	}
	if r.ContentLength != 0 && json.NewDecoder(r.Body).Decode(&body) != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	actor := httpActor(r)
	var value any
	switch parts[1] {
	case "comment":
		value, err = svc.comment(repo, number, actor, body.Body, body.Path, body.Line)
	case "review":
		value, err = svc.review(repo, number, actor, body.Verdict, body.Body)
	case "merge":
		var d *ChangeRequestDetail
		var res *nativeRefMerge
		d, res, err = svc.merge(repo, number, actor, body.Mode, body.Message, body.DeleteSource)
		value = map[string]any{"change_request": d, "merge": res}
	case "close":
		value, err = svc.setState(repo, number, actor, "closed")
	case "reopen":
		value, err = svc.setState(repo, number, actor, "open")
	default:
		httpErr(w, http.StatusNotFound, "no such change-request action")
		return
	}
	if err != nil {
		httpErr(w, http.StatusConflict, err.Error())
		return
	}
	httpJSON(w, value)
}
