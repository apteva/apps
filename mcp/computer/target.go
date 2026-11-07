package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	sdk "github.com/apteva/app-sdk"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	api "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/mobile"
)

func targetSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"kind":      map[string]any{"type": "string", "enum": []string{"browser", "mobile"}},
		"device_id": map[string]any{"type": "string"}, "platform": map[string]any{"type": "string", "enum": []string{"android", "ios"}},
		"physical": map[string]any{"type": "boolean"}, "mode": map[string]any{"type": "string", "enum": []string{"screen", "structured"}}, "app_id": map[string]any{"type": "string"},
	}}
}
func parseSessionTarget(backend string, args map[string]any) (*api.SessionTarget, error) {
	t := api.SessionTarget{Kind: "browser"}
	if backend == "mobile" {
		t.Kind = "mobile"
		t.Mode = "screen"
	}
	if raw, ok := args["target"]; ok {
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		if string(data) == "null" || len(data) == 0 || data[0] != '{' {
			return nil, errors.New("target must be an object")
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&t); err != nil {
			return nil, fmt.Errorf("invalid target: %w", err)
		}
	}
	if raw, ok := args["device_id"]; ok {
		id, valid := raw.(string)
		if !valid || strings.TrimSpace(id) == "" {
			return nil, errors.New("device_id must be a nonempty string")
		}
		if t.DeviceID != "" && t.DeviceID != id {
			return nil, errors.New("conflicting target.device_id and device_id")
		}
		t.DeviceID = id
	}
	if backend == "mobile" {
		if t.Kind != "mobile" {
			return nil, errors.New("mobile backend requires target.kind=mobile")
		}
		if t.Platform != "" && t.Platform != "android" && t.Platform != "ios" {
			return nil, errors.New("target.platform must be android or ios")
		}
		if t.Mode != "screen" && t.Mode != "structured" {
			return nil, errors.New("target.mode must be screen or structured")
		}
	} else if t.Kind != "browser" || t.DeviceID != "" || t.Platform != "" || t.Physical != nil || t.AppID != "" || t.Mode != "" {
		return nil, fmt.Errorf("backend %q only supports browser targets", backend)
	}
	return &t, nil
}
func contextVisible(ctx *sdk.AppCtx, c *ComputerContext) bool {
	return c != nil && (c.ProjectID == "" || (ctx != nil && ctx.CurrentProject() != "" && c.ProjectID == ctx.CurrentProject()))
}
func visibleContexts(ctx *sdk.AppCtx, rows []*ComputerContext) []*ComputerContext {
	out := make([]*ComputerContext, 0, len(rows))
	for _, c := range rows {
		if contextVisible(ctx, c) {
			out = append(out, c)
		}
	}
	return out
}
func resolveSessionTarget(ctx *sdk.AppCtx, backend string, args map[string]any, rc resolvedContext) (*api.SessionTarget, error) {
	target, err := parseSessionTarget(backend, args)
	if err != nil {
		return nil, err
	}
	if rc.Target != nil {
		if rc.Target.Kind != target.Kind {
			return nil, errors.New("context_target_mismatch: target kind does not match the selected backend")
		}
		if _, ok := args["target"]; ok || stringArg(args, "device_id") != "" {
			if !reflect.DeepEqual(*target, *rc.Target) {
				return nil, errors.New("context_target_mismatch: reopen the saved environment without target overrides")
			}
		}
		copy := *rc.Target
		target = &copy
	}
	if backend == "mobile" {
		if ctx == nil || ctx.CurrentProject() == "" {
			return nil, errors.New("mobile requires a project-scoped caller")
		}
		if rc.ProjectID != "" && rc.ProjectID != ctx.CurrentProject() {
			return nil, errContextNotFound
		}
		for _, key := range []string{"provider_context_id", "provider_context", "provider_session_id", "backend_session_id", "environment", "viewport", "user_agent", "proxy_profile", "proxy_country", "proxy_sticky", "proxy_url"} {
			if rawArgPresent(args, key) {
				return nil, fmt.Errorf("mobile does not support %s", key)
			}
		}
		if mode := stringArg(args, "proxy_mode"); mode != "" && mode != "auto" && mode != "direct" {
			return nil, errors.New("mobile does not support browser proxy configuration")
		}
		if boolArgDefault(args, "proxy", false) || !boolArgDefault(args, "persist", true) {
			return nil, errors.New("mobile cannot override networking or discard device app data")
		}
		if stringArg(args, "presentation_mode") == "demo" {
			return nil, errors.New("mobile does not support browser presentation overlays")
		}
	}
	return target, nil
}
func mobileCaller(ctx *sdk.AppCtx) mobile.Caller {
	if ctx != nil {
		ctx = ctx.WithProject(ctx.CurrentProject())
	}
	return func(callCtx context.Context, tool string, args map[string]any, out any) error {
		if ctx == nil || ctx.CurrentProject() == "" {
			return errors.New("mobile requires a project-scoped caller")
		}
		// The SDK's native MCPResult mode preserves image blocks for observations.
		return sdk.CallAppResultContext(callCtx, ctx.PlatformAPI(), "mobile", tool, args, out)
	}
}
func capabilitiesFor(backend string, comp backends.Computer) api.Capabilities {
	if r, ok := comp.(api.CapabilityReporter); ok {
		return r.Capabilities()
	}
	actions := []string{"screenshot", "click", "double_click", "type", "key", "scroll", "wait"}
	observations := []string{"screenshot"}
	if backend != "service" {
		actions = append(actions, "navigate", "back", "reload", "wait_for", "wait_for_stable", "select_option", "batch")
		observations = append(observations, "som", "url")
	}
	if backend == "local" || backend == "browserbase" {
		actions = append(actions, "prepare_upload", "upload_file", "set_checked", "set_temporal", "set_text")
	}
	if _, ok := comp.(api.DOMExtractor); ok {
		observations = append(observations, "dom")
	}
	return api.Capabilities{TargetKind: "browser", CoordinateSpace: "viewport_pixels", Actions: actions, Observations: observations, Context: api.ContextCapabilities{Reuse: backend != "service", Persistence: backend != "service", Isolation: backend != "service"}}
}
func targetFor(backend string, comp backends.Computer) *api.SessionTarget {
	if r, ok := comp.(api.CapabilityReporter); ok {
		t := r.Target()
		return &t
	}
	return &api.SessionTarget{Kind: "browser"}
}
func validateBackendAction(comp backends.Computer, args map[string]any) error {
	r, ok := comp.(api.CapabilityReporter)
	if !ok {
		return nil
	}
	caps := r.Capabilities()
	action := stringArg(args, "action")
	if !slices.Contains(caps.Actions, action) {
		return fmt.Errorf("backend_not_supported: %s target does not support %q", caps.TargetKind, action)
	}
	if action == "key" && !slices.Contains(caps.Keys, strings.ToLower(stringArg(args, "key"))) {
		return fmt.Errorf("backend_not_supported: unsupported key %q", stringArg(args, "key"))
	}
	if action == "swipe" {
		if !hasCoordinateArg(args) {
			return errors.New("invalid_target: swipe requires a pixel coordinate start")
		}
		for _, key := range []string{"to_x", "to_y"} {
			v, ok := numericArg(args[key])
			if !ok || v < 0 || v != float64(int(v)) {
				return fmt.Errorf("invalid_target: swipe requires integer %s", key)
			}
		}
	}
	if !slices.Contains(caps.Observations, "som") {
		for _, key := range []string{"label", "target_id", "selector", "som_revision", "expected_text", "expected_name", "expected_role", "expected_effect", "confirm_consequence"} {
			if rawArgPresent(args, key) {
				return fmt.Errorf("backend_not_supported: semantic target argument %s is unavailable", key)
			}
		}
		if stringArg(args, "observation") == "som_delta" || boolArgDefault(args, "include_som", false) {
			return errors.New("backend_not_supported: semantic observation is unavailable")
		}
	}
	return nil
}
func encodeJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func projectVisible(ctx *sdk.AppCtx, projectID string) bool {
	return projectID == "" || (ctx != nil && ctx.CurrentProject() == projectID)
}
func (a *App) scopedSessions(ctx *sdk.AppCtx, rows []sessionInfo) []sessionInfo {
	out := make([]sessionInfo, 0, len(rows))
	for _, row := range rows {
		if projectVisible(ctx, row.ProjectID) {
			out = append(out, row)
		}
	}
	return out
}
func (a *App) authorizeSessionProject(ctx *sdk.AppCtx, id string) error {
	if id == "" {
		return nil
	}
	if a.reg != nil {
		if sess, ok := a.reg.get(id); ok {
			if !projectVisible(ctx, sess.projectID) {
				return errors.New("session not found")
			}
			return nil
		}
	}
	if ctx == nil || ctx.AppDB() == nil {
		return nil
	}
	row, err := dbGetSessionMetadata(ctx.AppDB(), id)
	if err != nil {
		return nil
	}
	if !projectVisible(ctx, row.ProjectID) {
		return errors.New("session not found")
	}
	return nil
}

// Return an explicit unsuccessful tool result so agents and the panel can
// distinguish preparation from required human authorization without guessing
// from a generic JSON-RPC error. No session handle is returned on these paths.
func mobileStartupResponse(err error) (map[string]any, bool) {
	var failure *mobile.StartupError
	if !errors.As(err, &failure) {
		return nil, false
	}
	out := map[string]any{
		"success": false, "failed": true, "action_completed": false, "action_dispatched": false,
		"backend": "mobile", "status": failure.Status, "error_code": failure.Code,
		"error": failure.Message, "text": failure.Message, "retryable": failure.Retryable,
		"user_action_required": failure.UserActionRequired, "next_step": failure.NextStep,
	}
	if failure.RetryAfterMS > 0 {
		out["retry_after_ms"] = failure.RetryAfterMS
	}
	return out, true
}
