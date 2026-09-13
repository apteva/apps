package main

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type assignmentAgentPlatform struct {
	directPlatform
	deleted bool
}

func (f *assignmentAgentPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	if f.deleted && id == 1104 {
		return nil, errors.New("platform /api/apps/callback/agents/1104: http 403: agent not found or not owned by this user")
	}
	return f.fakeTasks.GetInstance(id)
}

func TestReplaceDeletedAssignmentAgent(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(fmt.Sprintf("pinned_role_%t", pinned), func(t *testing.T) {
			f := &assignmentAgentPlatform{}
			a := &App{}
			if err := a.OnMount(tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))); err != nil {
				t.Fatal(err)
			}
			p, err := a.save("project-a", "", "operator", 0, workflowDefinition().procedureOnly())
			if err != nil {
				t.Fatal(err)
			}
			c := AssignmentConfig{Name: "Barcelona weather", OwnerAgentID: 1104, ExecutionMode: "agent", FollowLatest: true, Schedule: &Schedule{Kind: "cron", Cron: "0 * * * *", Timezone: "Europe/Madrid"}}
			if pinned {
				c.Roles = map[string]Executor{"researcher": {Kind: "agent", AgentID: 1104}}
			}
			x, err := a.saveAssignment(p.ProjectID, p.ID, "", 0, c)
			if err != nil {
				t.Fatal(err)
			}
			f.deleted = true
			update := func() *httptest.ResponseRecorder {
				t.Helper()
				body := jsonText(map[string]any{"assignment": c, "expected_revision": x.Revision})
				r := httptest.NewRequest("PUT", "/processes/"+p.ID+"/assignments/"+x.ID+"?project_id=project-a", strings.NewReader(body))
				w := httptest.NewRecorder()
				a.handleHTTP(w, r)
				return w
			}
			w := update()
			if w.Code != 400 || !strings.Contains(w.Body.String(), "responsible agent 1104 is unavailable") {
				t.Fatalf("%d: %s", w.Code, w.Body)
			}
			c.OwnerAgentID = 1105
			if pinned {
				w = update()
				if w.Code != 400 || !strings.Contains(w.Body.String(), "role researcher agent 1104 is unavailable") {
					t.Fatalf("%d: %s", w.Code, w.Body)
				}
				c.Roles["researcher"] = Executor{Kind: "agent", AgentID: 1105}
			}
			w = update()
			if w.Code != 200 {
				t.Fatalf("%d: %s", w.Code, w.Body)
			}
			saved, err := a.assignment(p.ProjectID, p.ID, x.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.OwnerAgentID != 1105 || saved.Revision != x.Revision+1 || saved.Status != "paused" || saved.Schedule.Cron != c.Schedule.Cron {
				t.Fatalf("unexpected assignment: %+v", saved)
			}
			if len(f.events) != 0 {
				t.Fatal("editing dispatched work")
			}
		})
	}
}
