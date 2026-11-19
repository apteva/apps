//go:build integration

package main

// Tier 2 coverage for Code 0.17 through the real sidecar: two agents, a
// protected main that refuses direct checkpoints, a work branch that carries
// unsaved changes, a change request reviewed by the second agent, stale
// approval after a new revision, and the merge — over MCP and REST.
//
// Run with:  go test -tags integration -run TestSidecar_ChangeRequests ./...

import (
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_ChangeRequests_ProtectedMainReviewMerge(t *testing.T) {
	const project = "native-cr"
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(project), tk.WithEnv("CODE_REPOS_DIR", t.TempDir()))
	author := func(tool string, args map[string]any) map[string]any {
		return sc.MCPAs(tool, args, 1, "t-author", project)
	}
	reviewer := func(tool string, args map[string]any) map[string]any {
		return sc.MCPAs(tool, args, 2, "t-review", project)
	}
	author("repos_create", map[string]any{"name": "Review Demo", "framework": "blank"})
	slug := "review-demo"
	author("code_write_file", map[string]any{"slug": slug, "path": "app.txt", "content": "color=blue\n"})
	author("repos_checkpoint", map[string]any{"slug": slug, "message": "Base"})

	prot := author("repos_branch", map[string]any{"slug": slug, "action": "protect", "name": "main", "required_approvals": 1})
	if prot["protected"] != true {
		t.Fatalf("protect=%v", prot)
	}
	author("code_write_file", map[string]any{"slug": slug, "path": "app.txt", "content": "color=green\n"})
	if err := nativeMCPError(sc, "repos_checkpoint", map[string]any{"slug": slug, "message": "direct"}); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("direct checkpoint on protected main: %v", err)
	}
	var restRefused map[string]any
	if resp := sc.POST("/api/repos/"+slug+"/version/checkpoint", map[string]any{"message": "x"}, &restRefused); resp.Status != 409 {
		t.Fatalf("REST checkpoint on protected main status=%d", resp.Status)
	}
	work := author("repos_branch", map[string]any{"slug": slug, "action": "create", "name": "green", "switch": true})
	if work["branch"] != "green" || work["dirty"] != true {
		t.Fatalf("work branch=%v", work)
	}
	author("repos_checkpoint", map[string]any{"slug": slug, "message": "Green"})

	opened := author("change_requests", map[string]any{"slug": slug, "action": "create", "title": "Make it green", "source": "green", "body": "Brand refresh"})
	if opened["number"].(float64) != 1 || opened["author"] != "agent:1" || opened["can_merge"] != false || opened["mergeable"] != "mergeable" {
		t.Fatalf("opened=%v", opened)
	}
	if err := nativeMCPError(sc, "change_requests", map[string]any{"slug": slug, "action": "merge", "number": 1}); err == nil || !strings.Contains(err.Error(), "approval") {
		t.Fatalf("merge without approval: %v", err)
	}
	reviewed := reviewer("change_requests", map[string]any{"slug": slug, "action": "review", "number": 1, "verdict": "approve", "body": "ok"})
	approvals := reviewed["reviews"].(map[string]any)["approved_by"].([]any)
	if reviewed["can_merge"] != true || len(approvals) < 1 {
		t.Fatalf("reviewed=%v", reviewed)
	}

	// A new revision makes the approval stale.
	author("code_write_file", map[string]any{"slug": slug, "path": "notes.txt", "content": "late\n"})
	author("repos_checkpoint", map[string]any{"slug": slug, "message": "Late"})
	var detail map[string]any
	if resp := sc.GET("/api/repos/"+slug+"/version/change-requests/1?lines=1", &detail); resp.Status != 200 || detail["can_merge"] != false {
		t.Fatalf("REST detail after new revision status=%d %v", resp.Status, detail)
	}
	if files := detail["files"].([]any); len(files) != 2 {
		t.Fatalf("detail files=%v", files)
	}
	reviewer("change_requests", map[string]any{"slug": slug, "action": "review", "number": 1, "verdict": "approve"})
	merged := reviewer("change_requests", map[string]any{"slug": slug, "action": "merge", "number": 1, "mode": "squash"})
	cr := merged["change_request"].(map[string]any)
	if cr["state"] != "merged" || cr["merged_by"] != "agent:2" {
		t.Fatalf("merged=%v", merged)
	}
	var history map[string]any
	sc.GET("/api/repos/"+slug+"/version/history?ref=main", &history)
	head := history["revisions"].([]any)[0].(map[string]any)
	if !strings.Contains(head["message"].(string), "Merge change request #1") {
		t.Fatalf("main head=%v", head)
	}
	var moves map[string]any
	sc.GET("/api/repos/"+slug+"/version/moves?branch=main", &moves)
	if moves["moves"].([]any)[0].(map[string]any)["action"] != "change_request.squash" {
		t.Fatalf("main moves=%v", moves)
	}
	var list map[string]any
	if resp := sc.GET("/api/repos/"+slug+"/version/change-requests?state=merged", &list); resp.Status != 200 || list["count"].(float64) != 1 {
		t.Fatalf("REST list=%v", list)
	}
}
