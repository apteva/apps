package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/keyinput"
)

const workflowOperatorHeader = "X-Apteva-Operator-ID"

type workflowRecord struct {
	computer.WorkflowConstraint
	ContextID string `json:"context_id"`
}

// The server mints this header only for authenticated operator sessions/keys,
// never for agent gateways, delegated app users, or sibling app tokens. The
// sidecar's SDK token boundary prevents direct unauthenticated header spoofing.
func (a *App) handleWorkflowConstraints(w http.ResponseWriter, r *http.Request) {
	operator, err := strconv.ParseInt(r.Header.Get(workflowOperatorHeader), 10, 64)
	if err != nil || operator <= 0 || r.Header.Get("X-Apteva-Caller-Agent") != "" || r.Header.Get("X-Apteva-Subject-Type") != "" || r.Header.Get(sdk.HeaderBoundCallerInstallID) != "" {
		httpErr(w, http.StatusForbidden, "trusted operator identity required; agents cannot create or broaden workflow constraints")
		return
	}
	ctx := appCtxForRequest(r, nil)
	if ctx == nil || ctx.AppDB() == nil {
		httpErr(w, 503, "workflow store unavailable")
		return
	}
	if r.Method == http.MethodDelete {
		id := strings.TrimPrefix(r.URL.Path, "/workflow-constraints/")
		if id == "" || strings.Contains(id, "/") {
			httpErr(w, 400, "workflow id required")
			return
		}
		if _, err := ctx.AppDB().Exec("DELETE FROM computer_workflow_constraints WHERE id=?", id); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"removed": id})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var record workflowRecord
	if err := decoder.Decode(&record); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		httpErr(w, 400, "expected one workflow object")
		return
	}
	if err := validateWorkflowRecord(record); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if _, err := dbGetContext(ctx.AppDB(), record.ContextID); err != nil {
		httpErr(w, 400, "unknown saved browser context")
		return
	}
	u, _ := url.Parse(record.ResourceURL)
	origin := u.Scheme + "://" + u.Host
	raw, _ := json.Marshal(record)
	// No upsert: a tool cannot replace a previously registered authorization.
	// Changing/releasing a workflow requires a separate authenticated operator
	// DELETE followed by a new record, and never happens as part of a click.
	_, err = ctx.AppDB().Exec("INSERT INTO computer_workflow_constraints(id,context_id,origin,resource_url,policy_json,created_by,created_at) VALUES(?,?,?,?,?,?,?)", record.ID, record.ContextID, origin, record.ResourceURL, string(raw), strconv.FormatInt(operator, 10), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		httpErr(w, http.StatusConflict, "workflow already exists or saved context already has an active workflow")
		return
	}
	writeJSON(w, record)
}

func validateWorkflowRecord(r workflowRecord) error {
	if r.ID == "" || len(r.ID) > 128 || strings.ContainsAny(r.ID, "/\\") || r.ContextID == "" {
		return fmt.Errorf("workflow id and saved context_id required")
	}
	u, err := url.Parse(r.ResourceURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("resource_url must be an exact HTTP(S) resource URL without credentials, query or fragment")
	}
	if r.AllowedEffect != "scheduled_external_commit" {
		return fmt.Errorf("only scheduled_external_commit workflow constraints are supported")
	}
	at, err := time.Parse(time.RFC3339, r.ScheduledAt)
	if err != nil || !at.After(time.Now()) || at.Second() != 0 || at.Nanosecond() != 0 {
		return fmt.Errorf("scheduled_at must be a future RFC3339 instant at minute precision")
	}
	if _, err := time.LoadLocation(r.Timezone); err != nil || r.Timezone == "" {
		return fmt.Errorf("valid IANA timezone required")
	}
	if r.DateSelector == "" || r.TimeSelector == "" || len(r.DateSelector) > 512 || len(r.TimeSelector) > 512 {
		return fmt.Errorf("unique native date and time field selectors required for independent schedule verification")
	}
	return nil
}

func mergeWorkflowSummary(out map[string]any, act backends.Action) {
	if len(act.WorkflowConstraints) == 0 {
		return
	}
	items := make([]map[string]any, 0, len(act.WorkflowConstraints))
	for _, p := range act.WorkflowConstraints {
		items = append(items, map[string]any{"id": p.ID, "resource_url": p.ResourceURL, "allowed_effect": p.AllowedEffect, "scheduled_at": p.ScheduledAt, "timezone": p.Timezone, "authority": "operator", "mutable_by_agent": false})
	}
	out["workflow_constraints"] = items
}

func attachWorkflowConstraint(ctx *sdk.AppCtx, sess *session, act *backends.Action) error {
	if ctx == nil || ctx.AppDB() == nil {
		return nil
	}
	// Read persisted policy every time, including each batch step and after a
	// session is reopened. Exact resource matching also covers a second context.
	resourceURL := ""
	u, err := url.Parse(currentURL(sess.comp))
	if err == nil && u.Host != "" {
		u.RawQuery, u.Fragment = "", ""
		resourceURL = u.String()
	}
	rows, err := ctx.AppDB().Query("SELECT policy_json FROM computer_workflow_constraints WHERE context_id=? OR resource_url=?", sess.appContextID, resourceURL)
	if err != nil {
		return fmt.Errorf("workflow policy unavailable: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var record workflowRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return fmt.Errorf("invalid stored workflow policy")
		}
		act.WorkflowConstraints = append(act.WorkflowConstraints, record.WorkflowConstraint)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(act.WorkflowConstraints) == 0 {
		return nil
	}
	if sess.backend != "local" && sess.backend != "browserbase" {
		return fmt.Errorf("workflow constraint requires a backend with live commit enforcement")
	}
	// Keyboard submit/activation must not bypass the guarded click path. Text
	// and date entry remain available through set_text / set_temporal.
	if act.Type == "key" {
		events, known, err := keyinput.Events(act.Key)
		if err != nil || !known {
			return fmt.Errorf("workflow constraint blocks unverified key sequences; use type or set_text for text and a guarded click for submission")
		}
		for _, event := range events {
			if event.Key == "Enter" || event.Key == " " || event.Code == "Space" {
				return fmt.Errorf("workflow constraint blocks keyboard submission; use a guarded click matching the authorized schedule")
			}
		}
	}
	if act.Type == "type" && strings.ContainsAny(act.Text, "\r\n") {
		return fmt.Errorf("workflow constraint blocks typed submission; use set_text for multiline content")
	}
	return nil
}
