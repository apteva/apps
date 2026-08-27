package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var objectSchema = map[string]any{"type": "object", "additionalProperties": true}
var voiceCallSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"run_id":           map[string]any{"type": "string"},
		"caller_goal":      map[string]any{"type": "string"},
		"caller_persona":   map[string]any{"type": "string"},
		"caller_behavior":  map[string]any{"type": "string"},
		"provider":         map[string]any{"type": "string"},
		"voice":            map[string]any{"type": "string"},
		"caller_provider":  map[string]any{"type": "string"},
		"caller_voice":     map[string]any{"type": "string"},
		"greeting":         map[string]any{"type": "string"},
		"target_agent":     map[string]any{"type": "string"},
		"target_directive": map[string]any{"type": "string"},
		"timeout_seconds":  map[string]any{"type": "integer", "minimum": 15, "maximum": 300},
		"transport":        map[string]any{"type": "string", "enum": []string{"direct", "carrier"}},
		"protocol_fixture": map[string]any{"type": "string"},
		"audio_conditions": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"preset":    map[string]any{"type": "string", "enum": []string{"clean", "office", "cafe", "street", "train_station", "poor_phone"}},
				"intensity": map[string]any{"type": "string", "enum": []string{"light", "moderate", "heavy"}},
				"codec":     map[string]any{"type": "string", "enum": []string{"none", "g711_mulaw"}},
				"seed":      map[string]any{"type": "integer", "minimum": 0},
			},
			"additionalProperties": false,
		},
	},
	"required":             []string{"run_id", "caller_goal"},
	"additionalProperties": true,
}

func requiredSchema(fields ...string) map[string]any {
	props := map[string]any{}
	for _, f := range fields {
		props[f] = map[string]any{"type": "string"}
	}
	return map[string]any{"type": "object", "properties": props, "required": fields, "additionalProperties": true}
}
func decodeArgs(args map[string]any, out any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func decodeStrictArgs(args map[string]any, out any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

var pseudoEvaluationFields = []string{"environment_id", "suite_id", "case_id", "prompt", "input", "assertions", "goals", "targets", "repetitions"}

func containsPseudoEvaluationArgs(args map[string]any) bool {
	for _, field := range pseudoEvaluationFields {
		if _, found := args[field]; found {
			return true
		}
	}
	spec, _ := args["spec"].(map[string]any)
	for _, field := range pseudoEvaluationFields {
		if _, found := spec[field]; found {
			return true
		}
	}
	return false
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "environment_list", Description: "List environment definitions and active runs.", InputSchema: objectSchema, Handler: func(*sdk.AppCtx, map[string]any) (any, error) { return a.svc.listDefinitions() }},
		{Name: "environment_get", Description: "Get one environment definition and runtime.", InputSchema: requiredSchema("id"), Handler: a.toolGet},
		{Name: "environment_create", Description: "Create a durable environment definition.", InputSchema: requiredSchema("name"), Handler: a.toolSave(false)},
		{Name: "environment_update", Description: "Update a durable environment definition.", InputSchema: requiredSchema("id", "name"), Handler: a.toolSave(true)},
		{Name: "environment_delete", Description: "Delete a stopped environment definition.", InputSchema: requiredSchema("id"), Handler: a.toolDelete},
		{Name: "environment_start", Description: "Start a defined environment.", InputSchema: requiredSchema("id"), Handler: a.toolStart},
		{Name: "environment_stop", Description: "Stop a defined environment.", InputSchema: requiredSchema("id"), Handler: a.toolStop},
		{Name: "environment_run_create", Description: "Start an isolated runtime from an EnvironmentSpec. This does not execute Evals cases or assertions.", InputSchema: environmentRunCreateSchema, Handler: a.toolRunCreate},
		{Name: "environment_run_get", Description: "Get a run and live runtime.", InputSchema: requiredSchema("id"), Handler: a.toolRunGet},
		{Name: "environment_run_stop", Description: "Stop a run.", InputSchema: requiredSchema("id"), Handler: a.toolRunStop},
		{Name: "environment_catalog", Description: "List selectable apps, managed MCP servers, connections, integrations, web and protocol fixtures, agents, and snapshots.", InputSchema: objectSchema, Handler: a.toolCatalog},
		{Name: "environment_seed", Description: "Call a tool on a runtime app to seed data.", InputSchema: requiredSchema("run_id", "app", "tool"), Handler: a.toolCall},
		{Name: "environment_call", Description: "Call a tool on a runtime app.", InputSchema: requiredSchema("run_id", "app", "tool"), Handler: a.toolCall},
		{Name: "environment_mcp_call", Description: "Call a tool on a managed MCP cloned into a runtime.", InputSchema: requiredSchema("run_id", "mcp", "tool"), Handler: a.toolMCPCall},
		{Name: "environment_inspect", Description: "Inspect runtime state, edge calls, and optional agent telemetry.", InputSchema: requiredSchema("run_id"), Handler: a.toolInspect},
		{Name: "environment_assert", Description: "Run an app, edge, telemetry, web state, or web event assertion.", InputSchema: requiredSchema("run_id", "type"), Handler: a.toolAssert},
		{Name: "environment_snapshot", Description: "Snapshot a running defined environment.", InputSchema: requiredSchema("id"), Handler: a.toolSnapshot},
		{Name: "environment_snapshot_list", Description: "List snapshots.", InputSchema: objectSchema, Handler: func(*sdk.AppCtx, map[string]any) (any, error) { return a.svc.runtime().ListRuntimeSnapshots() }},
		{Name: "environment_snapshot_delete", Description: "Delete a snapshot.", InputSchema: requiredSchema("id"), Handler: a.toolSnapshotDelete},
		{Name: "environment_agent_spawn", Description: "Spawn a runtime agent.", InputSchema: requiredSchema("run_id"), Handler: a.toolAgentSpawn},
		{Name: "environment_agent_send", Description: "Send a message to a runtime agent.", InputSchema: requiredSchema("run_id", "agent", "message"), Handler: a.toolAgentSend},
		{Name: "environment_agent_control", Description: "Pause, resume, or stop a runtime agent.", InputSchema: requiredSchema("run_id", "agent", "action"), Handler: a.toolAgentControl},
		{Name: "environment_agent_wait", Description: "Wait for a runtime agent to finish and return its normalized trace and metrics.", InputSchema: requiredSchema("run_id", "agent"), Handler: a.toolAgentWait},
		{Name: "environment_voice_call", Description: "Run a full-duplex simulated caller against a realtime runtime agent, with optional deterministic background and line conditions.", InputSchema: voiceCallSchema, Handler: a.toolVoiceCall},
		{Name: "environment_voice_call_get", Description: "Get a voice call transcript, metrics, and recording handles.", InputSchema: requiredSchema("id"), Handler: a.toolVoiceCallGet},
		{Name: "environment_voice_recording_get", Description: "Get a base64-encoded WAV recording for receptionist, clean caller, or caller-delivered audio.", InputSchema: requiredSchema("id", "speaker"), Handler: a.toolVoiceRecordingGet},
	}
}

func str(args map[string]any, k string) string { v, _ := args[k].(string); return v }
func (a *App) runFor(args map[string]any) (*Run, error) {
	id := str(args, "run_id")
	if id == "" {
		id = str(args, "id")
	}
	r, err := a.svc.db.getRun(id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("run not found")
	}
	a.svc.decorateRun(r)
	return r, nil
}
func (a *App) toolGet(_ *sdk.AppCtx, args map[string]any) (any, error) {
	return a.svc.getDefinition(str(args, "id"))
}
func (a *App) toolSave(update bool) sdk.ToolHandler {
	return func(_ *sdk.AppCtx, args map[string]any) (any, error) {
		var d Definition
		if err := decodeArgs(args, &d); err != nil {
			return nil, err
		}
		if update && d.ID == "" {
			return nil, errors.New("id required")
		}
		saved, err := a.svc.saveDefinition(&d)
		if err == nil && !update {
			a.svc.ctx.Emit("environment.created", saved)
		}
		return saved, err
	}
}
func (a *App) toolDelete(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := str(args, "id")
	if active, _ := a.svc.db.activeRun(id); active != nil {
		return nil, errors.New("stop environment before deleting")
	}
	return map[string]bool{"ok": true}, a.svc.db.deleteDefinition(id)
}
func (a *App) toolStart(_ *sdk.AppCtx, args map[string]any) (any, error) {
	return a.svc.startDefinition(str(args, "id"))
}
func (a *App) toolStop(_ *sdk.AppCtx, args map[string]any) (any, error) {
	return map[string]bool{"ok": true}, a.svc.stopDefinition(str(args, "id"))
}
func (a *App) toolRunCreate(_ *sdk.AppCtx, args map[string]any) (any, error) {
	if containsPseudoEvaluationArgs(args) {
		return nil, errors.New("environment_run_create creates runtime infrastructure only; prompts, cases, suites and assertions must be executed through Evals")
	}
	var in struct {
		Kind string           `json:"kind"`
		Spec *EnvironmentSpec `json:"spec"`
	}
	if err := decodeStrictArgs(args, &in); err != nil {
		return nil, err
	}
	if in.Spec == nil {
		return nil, errors.New("spec required")
	}
	if in.Kind == "" {
		in.Kind = "eval"
	}
	return a.svc.start("", in.Kind, *in.Spec)
}
func (a *App) toolRunGet(_ *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	rt, rtErr := a.svc.runtime().GetRuntime(r.RuntimeID)
	return map[string]any{"run": r, "runtime": rt}, rtErr
}
func (a *App) toolRunStop(_ *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, a.svc.stopRun(r)
}
func (a *App) toolCatalog(_ *sdk.AppCtx, args map[string]any) (any, error) {
	apps, err := a.svc.runtime().ListRuntimeCatalogApps(a.svc.ctx.CurrentProject())
	if err != nil {
		return nil, err
	}
	connections, err := a.svc.ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{ProjectID: a.svc.ctx.CurrentProject()})
	if err != nil {
		return nil, err
	}
	integrations, err := a.svc.runtime().ListRuntimeCatalogIntegrations()
	if err != nil {
		return nil, err
	}
	managedMCPs, err := a.svc.runtime().ListRuntimeCatalogManagedMCPServers(a.svc.ctx.CurrentProject())
	if err != nil {
		return nil, err
	}
	agents, err := a.svc.runtime().ListRuntimeCatalogAgents(a.svc.ctx.CurrentProject())
	if err != nil {
		return nil, err
	}
	snapshots, err := a.svc.runtime().ListRuntimeSnapshots()
	realtimeProviders, _ := a.svc.runtime().ListRuntimeRealtimeProviders(a.svc.ctx.CurrentProject())
	return map[string]any{"apps": apps, "connections": connections, "integrations": integrations, "managed_mcps": managedMCPs, "agents": agents, "snapshots": snapshots, "web_fixtures": webFixtureCatalog(), "protocol_fixtures": protocolFixtureCatalog(), "realtime_providers": realtimeProviders}, err
}
func (a *App) toolCall(_ *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	input, _ := args["input"].(map[string]any)
	var out any
	err = a.svc.runtime().CallRuntimeAppResult(r.RuntimeID, str(args, "app"), str(args, "tool"), input, &out)
	return out, err
}
func (a *App) toolMCPCall(_ *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	input, _ := args["input"].(map[string]any)
	var out any
	if err := a.svc.runtime().CallRuntimeManagedMCPResult(r.RuntimeID, str(args, "mcp"), str(args, "tool"), input, &out); err != nil {
		return nil, err
	}
	return out, nil
}
func (a *App) toolInspect(_ *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	rt, err := a.svc.runtime().GetRuntime(r.RuntimeID)
	if err != nil {
		return nil, err
	}
	edge, err := a.svc.runtime().ListRuntimeEdgeCalls(r.RuntimeID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"run": r, "runtime": rt, "edge_calls": edge, "web_fixtures": r.WebFixtures, "protocol_fixtures": r.ProtocolFixtures}
	if agent := str(args, "agent"); agent != "" {
		events, err := a.svc.runtime().ListRuntimeAgentTelemetry(r.RuntimeID, agent, time.Time{}, 500)
		if err != nil {
			return nil, err
		}
		out["telemetry"] = events
	}
	return out, nil
}
func (a *App) toolAssert(_ *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	var assertion Assertion
	if err := decodeArgs(args, &assertion); err != nil {
		return nil, err
	}
	return a.svc.assert(r.RuntimeID, assertion)
}
func (a *App) toolSnapshot(_ *sdk.AppCtx, args map[string]any) (any, error) {
	return a.svc.snapshot(str(args, "id"), str(args, "description"))
}
func (a *App) toolSnapshotDelete(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := str(args, "id")
	if err := a.svc.runtime().DeleteRuntimeSnapshot(id); err != nil {
		return nil, err
	}
	_ = a.svc.db.deleteSnapshot(id)
	_ = a.svc.db.deleteWebFixtureSnapshots(id)
	return map[string]bool{"ok": true}, nil
}
func (a *App) toolAgentSpawn(_ *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	var req sdk.RuntimeAgentSpawnRequest
	if raw, ok := args["agent"].(map[string]any); ok {
		if err := decodeArgs(raw, &req); err != nil {
			return nil, err
		}
	} else {
		if err := decodeArgs(args, &req); err != nil {
			return nil, err
		}
	}
	return a.svc.runtime().SpawnRuntimeAgent(r.RuntimeID, req)
}
func (a *App) toolAgentSend(_ *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	err = a.svc.runtime().SendRuntimeAgentEvent(r.RuntimeID, str(args, "agent"), sdk.RuntimeAgentEventRequest{Message: str(args, "message"), ThreadID: str(args, "thread_id")})
	return map[string]bool{"ok": err == nil}, err
}
func (a *App) toolAgentControl(_ *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	action := str(args, "action")
	if action == "stop" {
		err = a.svc.runtime().StopRuntimeAgent(r.RuntimeID, str(args, "agent"))
		return map[string]bool{"ok": err == nil}, err
	}
	if action != "run" && action != "pause" && action != "step" {
		return nil, fmt.Errorf("invalid action %q", action)
	}
	err = a.svc.runtime().ControlRuntimeAgent(r.RuntimeID, str(args, "agent"), action)
	return map[string]bool{"ok": err == nil}, err
}

func (a *App) toolAgentWait(_ *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	var req sdk.RuntimeAgentWaitRequest
	if raw, ok := args["wait"].(map[string]any); ok {
		if err := decodeArgs(raw, &req); err != nil {
			return nil, err
		}
	} else if err := decodeArgs(args, &req); err != nil {
		return nil, err
	}
	return a.svc.runtime().WaitRuntimeAgent(r.RuntimeID, str(args, "agent"), req)
}

func (a *App) toolVoiceCall(_ *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	var spec VoiceFixtureSpec
	if raw, ok := args["voice"].(map[string]any); ok {
		err = decodeArgs(raw, &spec)
	} else {
		err = decodeArgs(args, &spec)
	}
	if err != nil {
		return nil, err
	}
	return a.svc.runVoiceCall(context.Background(), r, spec)
}

func (a *App) toolVoiceCallGet(_ *sdk.AppCtx, args map[string]any) (any, error) {
	call, err := a.svc.db.getVoiceCall(str(args, "id"))
	if err != nil {
		return nil, err
	}
	if call == nil {
		return nil, errors.New("voice call not found")
	}
	return call, nil
}

func (a *App) toolVoiceRecordingGet(_ *sdk.AppCtx, args map[string]any) (any, error) {
	path, err := a.svc.voiceRecordingPath(str(args, "id"), str(args, "speaker"))
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"content_type": "audio/wav",
		"encoding":     "base64",
		"data":         base64.StdEncoding.EncodeToString(raw),
	}, nil
}
