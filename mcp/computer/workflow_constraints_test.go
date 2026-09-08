package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestWorkflowPolicyRequiresOperatorAndPersistsOutsideSessions(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	prev := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = prev })
	app := &App{reg: &registry{m: map[string]*session{}}}
	created, err := app.toolContextCreate(ctx, map[string]any{"name": "schedule test", "backend": "local"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(created)
	var result map[string]any
	_ = json.Unmarshal(raw, &result)
	contextID := result["context"].(map[string]any)["id"].(string)
	record := workflowRecord{ContextID: contextID, WorkflowConstraint: computer.WorkflowConstraint{ID: "task-123", ResourceURL: "https://example.com/posts/1/edit", AllowedEffect: "scheduled_external_commit", ScheduledAt: time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute).Format(time.RFC3339), Timezone: "UTC", DateSelector: "input[type=date]", TimeSelector: "input[type=time]"}}
	request := func(method, path string, body any, headers map[string]string) int {
		data, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		app.handleWorkflowConstraints(w, req)
		return w.Code
	}
	for _, headers := range []map[string]string{nil, {"X-Apteva-Caller-Agent": "2"}, {workflowOperatorHeader: "1", "X-Apteva-Caller-Agent": "2"}, {workflowOperatorHeader: "1", "X-Apteva-Subject-Type": "user"}, {workflowOperatorHeader: "1", "X-Apteva-Bound-Caller-Install-ID": "9"}} {
		if got := request("POST", "/workflow-constraints", record, headers); got != http.StatusForbidden {
			t.Fatalf("non-operator created policy: %d", got)
		}
	}
	operator := map[string]string{workflowOperatorHeader: "1"}
	if got := request("POST", "/workflow-constraints", record, operator); got != 200 {
		t.Fatalf("operator create: %d", got)
	}
	if got := request("POST", "/workflow-constraints", record, operator); got != 409 {
		t.Fatalf("policy replacement allowed: %d", got)
	}
	for _, scope := range []struct{ contextID, resourceURL string }{
		{contextID, record.ResourceURL},
		{contextID, "about:blank"},
		{contextID, "https://another.example/editor"},
		{"another-context", record.ResourceURL + "?view=edit#composer"},
	} {
		// A fresh session object has no in-memory policy. Persistent lookup
		// protects both the original and a replacement session.
		sess := &session{backend: "local", appContextID: scope.contextID, comp: &fakeComp{url: scope.resourceURL}}
		act := computer.Action{Type: "click", ExpectedEffect: "immediate_external_commit", ConfirmConsequence: "immediate_external_commit"}
		if err := attachWorkflowConstraint(ctx, sess, &act); err != nil {
			t.Fatal(err)
		}
		if len(act.WorkflowConstraints) != 1 || act.WorkflowConstraints[0].AllowedEffect != "scheduled_external_commit" {
			t.Fatalf("missing persisted policy: %+v", act)
		}
		summary := map[string]any{}
		mergeWorkflowSummary(summary, act)
		items := summary["workflow_constraints"].([]map[string]any)
		if len(items) != 1 || items[0]["authority"] != "operator" || items[0]["mutable_by_agent"] != false || items[0]["scheduled_at"] != record.ScheduledAt {
			t.Fatalf("missing read-only authorization evidence: %+v", summary)
		}
		for _, value := range []string{"Enter", "Control+Enter", "Return", "Space", " ", "\r", "\n", "unverified"} {
			key := computer.Action{Type: "key", Key: value}
			if err := attachWorkflowConstraint(ctx, sess, &key); err == nil {
				t.Fatalf("keyboard submit/unverified sequence bypass: %q", value)
			}
		}
		for _, value := range []string{"Backspace", "Control+Backspace", "Tab", "ArrowDown", "Control+A"} {
			key := computer.Action{Type: "key", Key: value}
			if err := attachWorkflowConstraint(ctx, sess, &key); err != nil {
				t.Fatalf("editing/navigation key %q rejected: %v", value, err)
			}
		}
	}
	if got := request("DELETE", "/workflow-constraints/task-123", nil, map[string]string{"X-Apteva-Caller-Agent": "2"}); got != 403 {
		t.Fatal("agent removed policy")
	}
	if got := request("DELETE", "/workflow-constraints/task-123", nil, operator); got != 200 {
		t.Fatal("operator could not remove policy")
	}
}
