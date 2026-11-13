package main

import (
	"encoding/json"
	"fmt"

	api "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

// actionRecoveryError retains the MCP error bit and a machine-readable repair.
// A repair never repeats inputs that have already completed in a batch.
type actionRecoveryError struct{ details map[string]any }

func (e *actionRecoveryError) Error() string {
	b, _ := json.Marshal(e.details)
	return fmt.Sprintf("%s: %s; recover=%s", e.details["error_code"], e.details["error"], b)
}
func observationRecoveryFailure(id string, sess *session, action string, args map[string]any, cause error) error {
	next := map[string]any{"session_id": id, "action": action}
	guidance := "Omit observation for the action default, or choose none, som_delta, or screenshot for a post-action observation."
	recovery := map[string]any{"arguments": next, "next_step": guidance}
	if action == "screenshot" {
		next["annotate"] = annotateArg(args, true)
		next["include_som"] = boolArgDefault(args, "include_som", false)
		recovery["omit_arguments"] = []string{"observation"}
		recovery["next_step"] = "Retry screenshot without observation. Use annotate for image labels and include_som for structured targets."
	} else {
		recovery["allowed_observations"] = []string{"none", "som_delta", "screenshot"}
	}
	return &actionRecoveryError{map[string]any{"error_code": "invalid_observation", "error": cause.Error(), "session_id": id, "action": action, "action_dispatched": false, "recovery": recovery}}
}
func ambiguousScrollFailure(id string, sess *session, direction string, regions []api.ScrollRegion, completed []map[string]any, failedStep int) error {
	action := "scroll"
	if failedStep > 0 {
		action = "batch"
	}
	details := map[string]any{
		"error_code": "ambiguous_scroll_target", "error": "Multiple regions can scroll in this direction; choose an observed scroll_regions target_id.",
		"session_id": id, "action": action, "action_dispatched": len(completed) > 0, "failed_action_dispatched": false,
		"scroll_regions": regions, "som_revision": sess.somRevision,
		"recovery": map[string]any{"action": "scroll", "required_arguments": []string{"target_id"}, "direction": direction, "retry_scope": "failed_step_only", "next_step": "Choose the intended returned scroll_regions ID; pass it as target_id. Copy name/role only when supplied. Do not guess scroll_document or replay completed batch steps."},
	}
	if failedStep > 0 {
		details["failed_step"] = failedStep
		details["completed_steps"] = len(completed)
		details["steps"] = completed
	}
	return &actionRecoveryError{details}
}
func ambiguousScrollRegions(sess *session, act api.Action, args map[string]any) []api.ScrollRegion {
	if act.TargetID != "" || act.Label > 0 || hasCoordinateArg(args) {
		return nil
	}
	var available []api.ScrollRegion
	horizontal := act.Direction == "left" || act.Direction == "right"
	for _, region := range scrollRegionsFor(sess.comp) {
		if (horizontal && region.CanScrollX) || (!horizontal && region.CanScrollY) {
			available = append(available, region)
		}
	}
	if len(available) < 2 {
		return nil
	}
	return available
}

// compact is opt-in so existing app clients retain their response contract.
// Each target's full metadata occurs once. IDs reference the canonical som or
// delta target arrays; explicit screenshots always retain their fresh pixels.
const compactObservationBudget = 64 * 1024

func compactComputerObservation(out map[string]any, args map[string]any) map[string]any {
	if !boolArgDefault(args, "compact", false) {
		return out
	}
	delete(out, "png_b64")
	out["observation_format"] = "compact"
	ids := func(raw any) []string {
		b, _ := json.Marshal(raw)
		var targets []map[string]any
		_ = json.Unmarshal(b, &targets)
		result := make([]string, 0, len(targets))
		for _, t := range targets {
			if id, ok := t["id"].(string); ok && id != "" {
				result = append(result, id)
			}
		}
		return result
	}
	if raw, ok := out["som_delta"].(map[string]any); ok {
		delta := map[string]any{}
		for k, v := range raw {
			delta[k] = v
		}
		if _, full := out["som"]; full {
			for _, key := range []string{"added", "changed"} {
				delta[key+"_ids"] = ids(delta[key])
				delete(delta, key)
			}
		}
		out["som_delta"] = delta
		if targets, ok := out["safety_targets"]; ok {
			// Safety targets absent from added/changed still carry their current state.
			known := map[string]bool{}
			for _, key := range []string{"added", "changed"} {
				for _, id := range ids(delta[key]) {
					known[id] = true
				}
			}
			if full, ok := out["som"]; ok {
				for _, id := range ids(full) {
					known[id] = true
				}
			}
			b, _ := json.Marshal(targets)
			var all []map[string]any
			_ = json.Unmarshal(b, &all)
			var retained []map[string]any
			for _, target := range all {
				if !known[fmt.Sprint(target["id"])] {
					retained = append(retained, target)
				}
			}
			out["safety_target_ids"] = ids(targets)
			if len(retained) > 0 {
				out["safety_targets"] = retained
			} else {
				delete(out, "safety_targets")
			}
		}
	}
	if revealed, ok := out["revealed_targets"]; ok {
		out["revealed_target_ids"] = ids(revealed)
		delete(out, "revealed_targets")
	}
	// Budget observation arrays independently of required action outcome values
	// and image bytes. Truncation is explicit, never evidence of absence.
	remaining := compactObservationBudget
	omitted := 0
	trim := func(raw any) any {
		b, _ := json.Marshal(raw)
		var targets []json.RawMessage
		if json.Unmarshal(b, &targets) != nil {
			return raw
		}
		kept := make([]json.RawMessage, 0, len(targets))
		for _, target := range targets {
			if len(target)+1 <= remaining {
				kept = append(kept, target)
				remaining -= len(target) + 1
			} else {
				omitted++
			}
		}
		return kept
	}
	for _, key := range []string{"safety_targets", "som"} {
		if raw, ok := out[key]; ok {
			out[key] = trim(raw)
		}
	}
	if delta, ok := out["som_delta"].(map[string]any); ok {
		for _, key := range []string{"added", "changed"} {
			if raw, ok := delta[key]; ok {
				delta[key] = trim(raw)
			}
		}
	}
	if omitted > 0 {
		out["observation_truncated"] = true
		out["omitted_target_count"] = omitted
		out["coverage_incomplete"] = true
		out["next_step"] = "Observation target budget exhausted. Do not infer absence from omitted targets; narrow the visible region and observe again before deciding coverage is complete."
	}
	out["observation_target_bytes"] = compactObservationBudget - remaining
	return out
}
