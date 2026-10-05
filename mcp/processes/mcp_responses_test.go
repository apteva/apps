package main

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func mcpAs(t *testing.T, a *App, project string, agent int64, thread, tool string, args map[string]any) (any, error) {
	t.Helper()
	for _, spec := range a.MCPTools() {
		if spec.Name == tool {
			return spec.HandlerCtx(sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: agent, ThreadID: thread, ProjectID: project}), a.ctx, args)
		}
	}
	t.Fatal("missing tool", tool)
	return nil, nil
}
func mcpOK(t *testing.T, a *App, project, thread, tool string, args map[string]any) map[string]any {
	t.Helper()
	raw, err := mcpAs(t, a, project, 7, thread, tool, args)
	if err != nil {
		t.Fatal(tool, err)
	}
	return raw.(map[string]any)
}
func followReread(t *testing.T, a *App, project, thread string, ref RereadReference) map[string]any {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal([]byte(responseJSON(t, ref.Args)), &args); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.Tool, "processes_") {
		t.Fatal("unknown reread namespace", ref)
	}
	return mcpOK(t, a, project, thread, strings.TrimPrefix(ref.Tool, "processes_"), args)
}
func httpObject(t *testing.T, a *App, path string) string {
	t.Helper()
	request := httptest.NewRequest("GET", path, nil)
	request.Header.Set("X-Apteva-Project-ID", "project-a")
	out := httptest.NewRecorder()
	a.handleHTTP(out, request)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	return strings.TrimSpace(out.Body.String())
}
func TestMCPDiscoveryAndVersionsPreserveExactDefinitionsAndHTTP(t *testing.T) {
	a, _, _ := directSetup(t)
	d := workflowDefinition()
	d.Instructions = strings.Repeat("FROZEN-v1-policy;", 700)
	d.DefaultInputs = "Exact standing inputs"
	d.ApprovalRequirements = "Operator approval only"
	// save creates an unassigned draft; metadata must also handle zero owners.
	p, err := a.save("project-a", "", "operator", 0, d)
	if err != nil {
		t.Fatal(err)
	}
	v1 := p.Definition
	for i := 2; i <= 4; i++ {
		d.Instructions = strings.Repeat(fmt.Sprintf("FROZEN-v%d-policy;", i), 700)
		p, err = a.save(p.ProjectID, p.ID, "operator", p.Version, d)
		if err != nil {
			t.Fatal(err)
		}
	}
	path := "/processes/" + p.ID
	before := httpObject(t, a, path)
	list := mcpOK(t, a, p.ProjectID, "main", "list", map[string]any{"search": p.Name})
	metadata := list["processes"].([]ProcessMetadata)
	text := responseJSON(t, list)
	if len(metadata) != 2 || strings.Contains(text, "FROZEN") || strings.Contains(text, "instructions") || strings.Contains(text, "parameters") {
		t.Fatal("discovery included definition", text)
	}
	got := mcpOK(t, a, p.ProjectID, "main", "get", map[string]any{"process_id": p.ID})
	versions := got["versions"].([]VersionMetadata)
	if len(versions) != 4 || got["process"].(*Process).Instructions != d.Instructions || strings.Count(responseJSON(t, got), d.Instructions) != 1 || strings.Contains(responseJSON(t, versions), "definition") {
		t.Fatal("history duplicated procedure")
	}
	legacy, err := a.execute(p.ProjectID, "operator", "get", map[string]any{"process_id": p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(responseJSON(t, got))*2 >= len(responseJSON(t, legacy)) {
		t.Fatal("get insufficient payload reduction")
	}
	for _, v := range versions {
		selected := followReread(t, a, p.ProjectID, "main", v.Reread)
		expected, e := a.definition(p.ID, v.Version)
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(selected["definition"], expected) {
			t.Fatal("historical fidelity lost", v.Version)
		}
		if _, ok := selected["process"].(ProcessMetadata); !ok {
			t.Fatal("selected version echoed current definition")
		}
	}
	recovered := followReread(t, a, p.ProjectID, "main", procedureReread(p.ID, 1))
	if !reflect.DeepEqual(recovered["definition"], v1) {
		t.Fatal("old version changed")
	}
	historicalHTTP, e := a.execute(p.ProjectID, "operator", "get", map[string]any{"process_id": p.ID, "version": float64(1)})
	if e != nil || httpObject(t, a, path+"?version=1") != responseJSON(t, historicalHTTP) {
		t.Fatal("HTTP historical object changed", e)
	}
	if before != httpObject(t, a, path) {
		t.Fatal("MCP changed HTTP object")
	}
	var body struct {
		Versions []Version `json:"versions"`
	}
	if err = json.Unmarshal([]byte(before), &body); err != nil || len(body.Versions) != 4 || body.Versions[3].Definition.Instructions != v1.Instructions {
		t.Fatal("HTTP history no longer full", err)
	}
	expectedList, err := a.execute(p.ProjectID, "operator", "list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if httpObject(t, a, "/processes") != responseJSON(t, expectedList) {
		t.Fatal("HTTP list object changed")
	}
	if _, err = mcpAs(t, a, "other", 7, "main", "get", map[string]any{"process_id": p.ID}); err == nil {
		t.Fatal("cross-project reread accepted")
	}
	if _, err = mcpAs(t, a, p.ProjectID, 7, "main", "get", map[string]any{"process_id": p.ID, "version": float64(99)}); err == nil {
		t.Fatal("missing version accepted")
	}
	t.Logf("get: MCP %d bytes vs HTTP %d bytes; list metadata %d bytes", len(responseJSON(t, got)), len(before), len(text))
}

func TestMCPMetadataPreservesNumericAgentIDsWithoutRounding(t *testing.T) {
	a, _, p := directSetup(t)
	const agent int64 = 9007199254740993
	c := p.Assignments[0].AssignmentConfig
	c.OwnerAgentID = agent
	if _, err := a.db.Exec(`UPDATE process_assignments SET body_json=? WHERE id=?`, jsonText(c), p.Assignments[0].ID); err != nil {
		t.Fatal(err)
	}
	result := mcpOK(t, a, p.ProjectID, "main", "list", map[string]any{"search": p.Name})
	item := result["processes"].([]ProcessMetadata)[0]
	if item.OwnerAgentIDs[0] != agent || !strings.Contains(responseJSON(t, result), "9007199254740993") {
		t.Fatal("numeric identity rounded", item.OwnerAgentIDs)
	}
	selected := followReread(t, a, p.ProjectID, "main", item.Reread)
	if selected["process"].(ProcessMetadata).OwnerAgentIDs[0] != agent {
		t.Fatal("reread changed exact identity")
	}
}

func TestMCPDirectReceiptsAndRecoveryKeepFrozenStateAndHTTP(t *testing.T) {
	a, _, _ := directSetup(t)
	d := def()
	d.Instructions = strings.Repeat("FROZEN-direct-policy;", 700)
	p := status(t, a, create(t, a, d).ID, "active")
	raw, err := a.start(p.ProjectID, p.ID, "mcp-response", "exact run input")
	if err != nil {
		t.Fatal(err)
	}
	r := raw.(map[string]any)["run"].(Run)
	read := mcpOK(t, a, p.ProjectID, "main", "run_get", map[string]any{"run_id": r.ID})
	if _, ok := read["snapshot"]; ok {
		t.Fatal("duplicate textual snapshot")
	}
	frozen := read["definition"].(Definition)
	if frozen.Instructions != d.Instructions || read["run"].(Run).Inputs != "exact run input" {
		t.Fatal("frozen inputs lost")
	}
	// Change the current procedure while preserving the run's immutable revision.
	p = status(t, a, p.ID, "paused")
	changed := p.Definition
	changed.Instructions = "different current content"
	p, err = a.save(p.ProjectID, p.ID, "operator", p.Version, changed)
	if err != nil || p.Version != r.Version+1 {
		t.Fatal("failed to create a new current revision", err)
	}
	output := strings.Repeat("exact-evidence;", 1500)
	args := map[string]any{"process_id": p.ID, "run_id": r.ID, "state": "running", "progress": float64(33), "result": output}
	ack := mcpOK(t, a, p.ProjectID, "main", "run_update", args)
	if ack["state"] != "running" || ack["progress"] != 33 || ack["done"] != false || len(responseJSON(t, ack)) > 1000 || strings.Contains(responseJSON(t, ack), "exact-evidence") {
		t.Fatal("oversized or wrong update receipt", ack)
	}
	recovered := followReread(t, a, p.ProjectID, "main", ack["reread"].(RereadReference))
	if recovered["run"].(Run).Result != output || !reflect.DeepEqual(recovered["definition"], frozen) {
		t.Fatal("receipt recovery lost state or policy")
	}
	args["state"], args["error"] = "blocked", "exact blocker"
	blocked := mcpOK(t, a, p.ProjectID, "main", "run_update", args)
	if blocked["error"] != "exact blocker" || blocked["done"] != false {
		t.Fatal("blocker lost")
	}
	args["state"], args["error"] = "completed", ""
	ack = mcpOK(t, a, p.ProjectID, "main", "run_update", args)
	before := totalChanges(t, a)
	replay := mcpOK(t, a, p.ProjectID, "main", "run_update", args)
	if responseJSON(t, ack) != responseJSON(t, replay) || totalChanges(t, a) != before || ack["done"] != true || ack["progress"] != 100 {
		t.Fatal("completion replay changed state")
	}
	inspected := httpObject(t, a, "/processes/"+p.ID+"/runs/"+r.ID)
	full, err := a.directRun(p.ProjectID, "operator", p.ID, r.ID, "run_get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if inspected != responseJSON(t, full) || !strings.Contains(inspected, "snapshot") {
		t.Fatal("HTTP run object changed")
	}
	updateFull, err := a.execute(p.ProjectID, "operator", "run_update", args)
	if err != nil || updateFull.(map[string]any)["snapshot"] == nil {
		t.Fatal("operator update contract changed", err)
	}
	if _, err = mcpAs(t, a, p.ProjectID, 8, "main", "run_update", args); err == nil {
		t.Fatal("update authorization changed")
	}
	t.Logf("run_update acknowledgement %d bytes; full HTTP %d bytes", len(responseJSON(t, ack)), len(inspected))
}

func TestMCPWorkflowReadKeepsApprovalReceiptsAndOneDefinition(t *testing.T) {
	d := mediaContinuityDefinition()
	d.Steps[0].Instructions = strings.Repeat("EXACT-step-instructions;", 500)
	a, _, p, r := executorSetup(t, d, "per_executor", map[string]Executor{"operator": {Kind: "human"}})
	actor := "agent:7:" + stepBy(t, a, r, "inventory").ThreadID
	for _, key := range []string{"inventory", "portrait_3", "portrait_4", "validate"} {
		finishStep(t, a, p, r, key, actor, "exact receipt:"+key, "")
	}
	finishStep(t, a, p, r, "approve", "operator", "Approved exact portrait identities", "")
	read := mcpOK(t, a, p.ProjectID, "main", "run_get", map[string]any{"process_id": p.ID, "run_id": r.ID})
	text := responseJSON(t, read)
	if strings.Count(text, d.Steps[0].Instructions) != 1 || strings.Contains(text, "snapshot") {
		t.Fatal("run repeated frozen step instructions")
	}
	states := read["steps"].([]MCPRunStep)
	for _, s := range states {
		if strings.Contains(responseJSON(t, s), "\"definition\"") {
			t.Fatal("step state repeated definition")
		}
		if s.Key == "approve" && (s.Output != "Approved exact portrait identities" || s.UpdatedBy != "operator" || s.Executor.Kind != "human") {
			t.Fatal("lost approval evidence")
		}
		recovered := followReread(t, a, p.ProjectID, "main", s.Reread)
		if !reflect.DeepEqual(recovered["step"].(WorkerStep).Definition, s.StepRun.Definition) || recovered["step"].(WorkerStep).Output != s.Output {
			t.Fatal("invalid step recovery reference")
		}
	}
	legacy, err := a.directRun(p.ProjectID, "operator", p.ID, r.ID, "run_get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if httpObject(t, a, "/processes/"+p.ID+"/runs/"+r.ID) != responseJSON(t, legacy) {
		t.Fatal("workflow HTTP object changed")
	}
	ack := mcpOK(t, a, p.ProjectID, strings.TrimPrefix(actor, "agent:7:"), "step_update", map[string]any{"run_id": r.ID, "step_id": stepBy(t, a, r, "publish").ID, "state": "completed", "output": "published approved artifacts"})
	recovered := followReread(t, a, p.ProjectID, "main", ack["reread"].(RereadReference))
	if recovered["step"].(WorkerStep).Output != "published approved artifacts" || ack["done"] != true {
		t.Fatal("step receipt recovery failed")
	}
}
