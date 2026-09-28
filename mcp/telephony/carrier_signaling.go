package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
)

type carrierSIPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Only routing headers are retained. Authorization, cookies, and arbitrary
// custom headers never enter the call record, lifecycle payload, or logs.
func filteredCarrierSignaling(raw json.RawMessage) string {
	if len(raw) == 0 || len(raw) > 65536 {
		return "{}"
	}
	var headers []carrierSIPHeader
	if json.Unmarshal(raw, &headers) != nil {
		var values map[string]string
		if json.Unmarshal(raw, &values) != nil {
			return "{}"
		}
		for k, v := range values {
			headers = append(headers, carrierSIPHeader{k, v})
		}
	}
	out := map[string][]string{}
	for _, h := range headers {
		name := strings.ToLower(strings.TrimSpace(h.Name))
		if name != "diversion" && name != "history-info" && name != "referred-by" {
			continue
		}
		if len(out[name]) >= 4 {
			continue
		}
		value := strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < 32 || r == 127 {
				return -1
			}
			return r
		}, h.Value))
		if len(value) > 512 {
			value = value[:512]
		}
		if value != "" {
			out[name] = append(out[name], value)
		}
	}
	encoded, _ := json.Marshal(out)
	return string(encoded)
}

func (a *App) recordCarrierSignaling(row *callRow, legID, sessionID string, headers json.RawMessage) error {
	signaling := filteredCarrierSignaling(headers)
	legID, sessionID = boundedCarrierID(legID), boundedCarrierID(sessionID)
	_, err := a.db().db.Exec(`UPDATE calls SET carrier_leg_id=CASE WHEN carrier_leg_id='' THEN ? ELSE carrier_leg_id END,carrier_session_id=CASE WHEN carrier_session_id='' THEN ? ELSE carrier_session_id END,carrier_signaling_json=CASE WHEN ?<>'{}' THEN json_patch(CASE WHEN json_valid(carrier_signaling_json) THEN carrier_signaling_json ELSE '{}' END,?) ELSE carrier_signaling_json END WHERE id=? AND project_id=?`, legID, sessionID, signaling, signaling, row.ID, row.ProjectID)
	return err
}
func boundedCarrierID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		return ""
	}
	return value
}

func recordCarrierCommand(ctx *sdk.AppCtx, connection int64, tool string, input map[string]any, status int, success bool) {
	if ctx == nil || ctx.AppDB() == nil || ctx.CurrentProject() == "" {
		return
	}
	sid := firstNonEmpty(stringValue(input["call_control_id"]), stringValue(input["CallSid"]), stringValue(input["callId"]), stringValue(input["call_uuid"]))
	if sid == "" {
		return
	}
	// Response bodies and request fields are deliberately excluded.
	_, _ = ctx.AppDB().Exec(`INSERT INTO carrier_command_events(project_id,call_id,command,command_id,response_status,succeeded,occurred_at) SELECT project_id,id,?,?,?,?,? FROM calls WHERE project_id=? AND carrier_connection_id=? AND carrier_sid=?`, tool, stringValue(input["command_id"]), status, success, ringTime(time.Now()), ctx.CurrentProject(), connection, sid)
}

func (a *App) carrierCommandHistory(project, callID string) ([]map[string]any, error) {
	rows, err := a.db().db.Query(`SELECT command,command_id,response_status,succeeded,occurred_at FROM carrier_command_events WHERE project_id=? AND call_id=? ORDER BY id DESC LIMIT 100`, project, callID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var command, id, at string
		var status int
		var success bool
		if err = rows.Scan(&command, &id, &status, &success, &at); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"command": command, "command_id": id, "response_status": status, "succeeded": success, "occurred_at": at})
	}
	return out, rows.Err()
}
