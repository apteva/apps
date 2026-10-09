package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Only the turn boundary is overridden here. Leaving the other SDK fields
// unset preserves Core's profile sensitivity, padding and interruption defaults.
func destinationTurnDetection(kind, configJSON string) (*sdk.RealtimeTurnDetection, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil, fmt.Errorf("invalid destination configuration: %w", err)
	}
	raw, present := config["turn_detection"]
	if !present {
		return nil, nil
	}
	if kind != "ai" && kind != "agent" {
		return nil, errors.New("turn_detection is only supported for AI and agent destinations")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("turn_detection must be an object")
	}
	turn := telephonyTurnDetection()
	for key, value := range fields {
		switch key {
		case "profile":
			if string(value) == "null" || json.Unmarshal(value, &turn.Profile) != nil {
				return nil, errors.New("turn_detection.profile must be telephony or default")
			}
			turn.Profile = strings.ToLower(strings.TrimSpace(turn.Profile))
			if turn.Profile != "telephony" && turn.Profile != "default" {
				return nil, errors.New("turn_detection.profile must be telephony or default")
			}
		case "silence_duration_ms":
			if json.Unmarshal(value, &turn.SilenceDurationMS) != nil || turn.SilenceDurationMS < 1 || turn.SilenceDurationMS > 60_000 {
				return nil, errors.New("turn_detection.silence_duration_ms must be an integer between 1 and 60000; omit it to inherit the profile default")
			}
		default:
			return nil, fmt.Errorf("unsupported turn_detection field %q", key)
		}
	}
	return turn, nil
}

// Read the call's frozen routing/offer configuration only at new session spawn.
// Never look up the live destination: editing it must not change an in-flight
// call's selected policy, including a bounded startup retry.
func (a *App) turnDetectionForNewSession(row *callRow) (*sdk.RealtimeTurnDetection, error) {
	if row.Direction != "inbound" || row.RoutingDestinationID == "" {
		return telephonyTurnDetection(), nil
	}
	var kind, raw string
	err := a.db().db.QueryRow(`SELECT kind,config_json FROM call_offers WHERE call_id=? AND project_id=? AND destination_id=? AND status='claimed' ORDER BY claimed_at DESC LIMIT 1`, row.ID, row.ProjectID, row.RoutingDestinationID).Scan(&kind, &raw)
	if err == nil {
		turn, err := destinationTurnDetection(kind, raw)
		if err != nil || turn != nil {
			return turn, err
		}
		return telephonyTurnDetection(), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if row.RoutingFlowVersionID != "" {
		// Read the selected destination directly from the committed snapshot.
		// Re-simulating the graph here would couple session startup to unrelated
		// branches (including an invalid legacy failure branch).
		err := a.db().db.QueryRow(`SELECT context_json FROM call_route_executions WHERE call_id=? AND project_id=?`, row.ID, row.ProjectID).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			var snapshot routingExecutionContext
			if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
				return nil, fmt.Errorf("invalid routing snapshot: %w", err)
			}
			if dest, ok := snapshot.Definition.Destinations[row.RoutingDestinationID]; ok {
				turn, err := destinationTurnDetection(dest.Kind, dest.ConfigJSON)
				if err != nil || turn != nil {
					return turn, err
				}
			}
		}
	}
	return telephonyTurnDetection(), nil
}
