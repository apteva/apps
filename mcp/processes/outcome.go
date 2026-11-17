package main

import "strings"

// outcomeField is the one text field every step_update and run_update call
// carries. The schema marks it required because some models omit optional
// fields deep into a long context, and a completion without its result is a
// wasted turn followed by retries. The server stays lenient: outcome only
// fills a field the caller left empty, so older callers using output,
// handoff, result or error keep their exact behaviour.
func outcomeField() map[string]any {
	return textField("Required. completed: the exact result to keep. waiting/blocked/failed/cancelled: the reason. running: a short progress note.")
}

// applyStepOutcome maps outcome onto step_update's output or error.
func applyStepOutcome(args map[string]any) map[string]any {
	outcome := strings.TrimSpace(str(args, "outcome"))
	if outcome == "" {
		delete(args, "outcome")
		return args
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if k != "outcome" {
			out[k] = v
		}
	}
	switch str(args, "state") {
	case "completed":
		if _, ok := out["handoff"]; !ok && strings.TrimSpace(str(out, "output")) == "" {
			out["output"] = outcome
		}
	case "waiting", "blocked", "failed", "cancelled":
		if strings.TrimSpace(str(out, "error")) == "" {
			out["error"] = outcome
		}
	}
	return out
}

// applyRunOutcome maps outcome onto run_update's result, error or current_step.
func applyRunOutcome(args map[string]any) map[string]any {
	outcome := strings.TrimSpace(str(args, "outcome"))
	if outcome == "" {
		delete(args, "outcome")
		return args
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if k != "outcome" {
			out[k] = v
		}
	}
	field := "current_step"
	switch str(args, "state") {
	case "completed":
		field = "result"
	case "waiting", "blocked", "failed", "cancelled":
		field = "error"
	}
	if strings.TrimSpace(str(out, field)) == "" {
		out[field] = outcome
	}
	return out
}
