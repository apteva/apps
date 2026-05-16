package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// TestIntegrationStep_HappyPath: integration step calls
// ExecuteIntegrationTool with the templated args and exposes the
// upstream's response as the step output (with downstream
// {{ steps.<id>.<field> }} access working).
func TestIntegrationStep_HappyPath(t *testing.T) {
	plat := &stubPlatform{
		integrationResults: map[int64]*sdk.ExecuteResult{
			17: {Success: true, Status: 200, Data: []byte(`{"request":"abc-123","status":1}`)},
		},
	}
	ctx := newRunCtx(t, plat)

	src := `name: ping-pushover
trigger:
  kind: manual
steps:
  - id: send
    kind: integration
    connection_id: 17
    tool: pushover_send_message
    input:
      message: "hello {{ input.who }}"
      title: "Apteva"
`
	wf := mustCreateWorkflow(t, ctx, src)
	run, err := RunWorkflow(context.Background(), ctx, testProj, wf,
		map[string]any{"who": "marco"},
		runOptions{triggerKind: "manual"})
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("status = %q (steps=%+v)", run.Status, run.Steps)
	}
	if len(plat.integrationCalls) != 1 {
		t.Fatalf("integrationCalls = %d, want 1", len(plat.integrationCalls))
	}
	call := plat.integrationCalls[0]
	if call.connID != 17 || call.tool != "pushover_send_message" {
		t.Errorf("call = %d/%s, want 17/pushover_send_message", call.connID, call.tool)
	}
	if call.input["message"] != "hello marco" {
		t.Errorf("templated message not passed through: %v", call.input["message"])
	}
}

// TestIntegrationStep_NonSuccessReturnsError: when the platform
// reports Success=false (upstream returned 4xx/5xx), the step
// status becomes error and the upstream body is still surfaced as
// Output so the workflow author can inspect it from an on_error
// branch.
func TestIntegrationStep_NonSuccessReturnsError(t *testing.T) {
	plat := &stubPlatform{
		integrationResults: map[int64]*sdk.ExecuteResult{
			17: {Success: false, Status: 401, Data: []byte(`{"error":"invalid token"}`)},
		},
	}
	ctx := newRunCtx(t, plat)

	src := `name: fails
trigger:
  kind: manual
steps:
  - id: send
    kind: integration
    connection_id: 17
    tool: pushover_send_message
    input: { message: "x" }
`
	wf := mustCreateWorkflow(t, ctx, src)
	run, err := RunWorkflow(context.Background(), ctx, testProj, wf, nil,
		runOptions{triggerKind: "manual"})
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}
	if run.Status != "failed" {
		t.Fatalf("expected failed status, got %q", run.Status)
	}
}

// TestIntegrationStep_DownstreamSeesOutput: a second step can
// reference {{ steps.<id>.<field> }} where the field is from the
// integration response — proves the JSON-decode of res.Data lands
// in the template context (rather than the raw bytes).
func TestIntegrationStep_DownstreamSeesOutput(t *testing.T) {
	plat := &stubPlatform{
		integrationResults: map[int64]*sdk.ExecuteResult{
			17: {Success: true, Status: 200, Data: []byte(`{"request":"abc-123"}`)},
		},
	}
	ctx := newRunCtx(t, plat)

	src := `name: chain
trigger:
  kind: manual
steps:
  - id: send
    kind: integration
    connection_id: 17
    tool: pushover_send_message
    input: { message: "first" }
  - id: log
    kind: emit
    topic: workflow.sent
    input:
      request_id: "{{ steps.send.request }}"
`
	wf := mustCreateWorkflow(t, ctx, src)
	run, err := RunWorkflow(context.Background(), ctx, testProj, wf, nil,
		runOptions{triggerKind: "manual"})
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("status = %q", run.Status)
	}
	// The emit step's data was templated against steps.send.request —
	// check by reading the run record (steps[1] is the emit, whose
	// Output is the rendered data map).
	if len(run.Steps) < 2 {
		t.Fatalf("steps = %d, want at least 2", len(run.Steps))
	}
	var emitOutput map[string]any
	_ = json.Unmarshal([]byte(run.Steps[1].OutputJSON), &emitOutput)
	data, _ := emitOutput["data"].(map[string]any)
	if data == nil || data["request_id"] != "abc-123" {
		t.Errorf("downstream template miss: emitOutput=%v", emitOutput)
	}
}

// TestParseRejectsBadIntegrationStep: validation catches a step
// missing connection_id or tool.
func TestParseRejectsBadIntegrationStep(t *testing.T) {
	cases := []string{
		// no connection_id
		`name: x
trigger: { kind: manual }
steps:
  - id: a
    kind: integration
    tool: pushover_send_message
`,
		// no tool
		`name: x
trigger: { kind: manual }
steps:
  - id: a
    kind: integration
    connection_id: 17
`,
	}
	for i, src := range cases {
		def, err := ParseDefinition([]byte(src))
		if err != nil {
			continue // also fine — fail at parse
		}
		if err := def.Validate(); err == nil {
			t.Errorf("case %d: expected validate error, got nil", i)
		} else if !strings.Contains(err.Error(), "integration step needs") {
			t.Errorf("case %d: unexpected error: %v", i, err)
		}
	}
}
