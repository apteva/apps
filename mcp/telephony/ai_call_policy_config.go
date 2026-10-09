package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type aiCallPolicy struct {
	MaxDurationSeconds       int `json:"max_duration_seconds"`
	InactivityTimeoutSeconds int `json:"inactivity_timeout_seconds"`
	ResponseWindowSeconds    int `json:"response_window_seconds"`
}

func projectAICallPolicy(ctx *sdk.AppCtx) aiCallPolicy {
	var config map[string]string
	if ctx != nil {
		config = ctx.Config()
	}
	return aiCallPolicy{
		durationSetting(config, "ai_call_max_duration_seconds", 1200, 60, 14400),
		durationSetting(config, "ai_call_inactivity_timeout_seconds", 30, 0, 300),
		durationSetting(config, "ai_call_response_window_seconds", 15, 5, 120),
	}
}

func destinationAICallPolicy(kind, raw string, base aiCallPolicy) (aiCallPolicy, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return base, err
	}
	value, present := config["ai_call_policy"]
	if !present {
		return base, nil
	}
	if kind != "ai" && kind != "agent" {
		return base, errors.New("ai_call_policy is only supported for AI and agent destinations")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(value, &fields) != nil || fields == nil {
		return base, errors.New("ai_call_policy must be an object")
	}
	for key, raw := range fields {
		var n int
		if string(raw) == "null" || json.Unmarshal(raw, &n) != nil {
			return base, fmt.Errorf("ai_call_policy.%s must be an integer", key)
		}
		low, high := 0, 0
		switch key {
		case "max_duration_seconds":
			low, high = 60, 14400
		case "inactivity_timeout_seconds":
			low, high = 0, 300
		case "response_window_seconds":
			low, high = 5, 120
		default:
			return base, fmt.Errorf("unsupported ai_call_policy field %q", key)
		}
		if n < low || n > high {
			return base, fmt.Errorf("ai_call_policy.%s must be between %d and %d", key, low, high)
		}
		switch key {
		case "max_duration_seconds":
			base.MaxDurationSeconds = n
		case "inactivity_timeout_seconds":
			base.InactivityTimeoutSeconds = n
		case "response_window_seconds":
			base.ResponseWindowSeconds = n
		}
	}
	return base, nil
}

// Use selected immutable routing data. Destination edits cannot change an
// active call or a bounded startup retry. The policy table freezes project defaults too.
func (a *App) aiPolicyForNewSession(ctx *sdk.AppCtx, row *callRow) (aiCallPolicy, error) {
	base := projectAICallPolicy(ctx)
	var frozen string
	err := a.db().db.QueryRow(`SELECT state_json FROM ai_call_policies WHERE call_id=? AND project_id=?`, row.ID, row.ProjectID).Scan(&frozen)
	if err == nil {
		var s aiPolicyState
		if err = json.Unmarshal([]byte(frozen), &s); err != nil {
			return base, err
		}
		return s.Policy, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return base, err
	}
	if row.Direction != "inbound" || row.RoutingDestinationID == "" {
		return base, nil
	}
	var kind, raw string
	err = a.db().db.QueryRow(`SELECT kind,config_json FROM call_offers WHERE call_id=? AND project_id=? AND destination_id=? AND status='claimed' ORDER BY claimed_at DESC LIMIT 1`, row.ID, row.ProjectID, row.RoutingDestinationID).Scan(&kind, &raw)
	if err == nil {
		return destinationAICallPolicy(kind, raw, base)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return base, err
	}
	if row.RoutingFlowVersionID != "" {
		err = a.db().db.QueryRow(`SELECT context_json FROM call_route_executions WHERE call_id=? AND project_id=?`, row.ID, row.ProjectID).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return base, err
		}
		if err == nil {
			var snapshot routingExecutionContext
			if err = json.Unmarshal([]byte(raw), &snapshot); err != nil {
				return base, err
			}
			if dest, ok := snapshot.Definition.Destinations[row.RoutingDestinationID]; ok {
				return destinationAICallPolicy(dest.Kind, dest.ConfigJSON, base)
			}
		}
	}
	return base, nil
}

func aiPolicyDirective(directive string, policy aiCallPolicy) string {
	return strings.TrimSpace(directive) + fmt.Sprintf("\nTelephony enforces a maximum AI handling time of %d seconds. Do not issue unsolicited repeated silence prompts. If a Telephony inactivity reminder event arrives, say one brief 'Are you still there?' reminder in the caller's language, then listen. If the caller has resumed speaking or you are doing a tool operation, do not interrupt them with that reminder. Telephony owns the response window and carrier termination; do not repeatedly prompt or extend its timers.", policy.MaxDurationSeconds)
}
