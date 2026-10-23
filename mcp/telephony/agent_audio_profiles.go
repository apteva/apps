package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strconv"
	"strings"
	"time"
)

type agentAudioProfile struct {
	Mode          string `json:"mode"`
	Interruption  string `json:"interruption"`
	Preprocessing string `json:"preprocessing"`
	SpeechStartMS int    `json:"speech_start_ms"`
	SendAheadMS   int    `json:"send_ahead_ms"`
	BacklogMS     int    `json:"backlog_ms"`
}

func resolveAgentAudioProfile(p agentAudioProfile) (agentAudioProfile, error) {
	if p.Mode == "" {
		p.Mode = "auto"
	}
	if p.Interruption == "" {
		p.Interruption = "auto"
	}
	if p.Preprocessing == "" {
		p.Preprocessing = "analysis_only"
	}
	if p.SpeechStartMS == 0 {
		p.SpeechStartMS = 160
	}
	if p.SendAheadMS == 0 {
		p.SendAheadMS = 60
	}
	if p.BacklogMS == 0 {
		p.BacklogMS = 1000
	}
	if p.Mode != "auto" && p.Mode != "traditional" && p.Mode != "continuous" {
		return p, errors.New("mode must be auto, traditional or continuous")
	}
	if p.Interruption != "auto" && p.Interruption != "local" && p.Interruption != "provider" && p.Interruption != "off" {
		return p, errors.New("invalid interruption policy")
	}
	if p.Preprocessing != "analysis_only" && p.Preprocessing != "noise_suppression" {
		return p, errors.New("invalid preprocessing policy")
	}
	if p.SpeechStartMS < 80 || p.SpeechStartMS > 400 || p.SpeechStartMS%20 != 0 {
		return p, errors.New("speech_start_ms must be 80..400 in 20 ms steps")
	}
	if p.SendAheadMS < 20 || p.SendAheadMS > 200 || p.SendAheadMS%20 != 0 {
		return p, errors.New("send_ahead_ms must be 20..200 in 20 ms steps")
	}
	if p.BacklogMS < 200 || p.BacklogMS > 2000 || p.BacklogMS%20 != 0 {
		return p, errors.New("backlog_ms must be 200..2000 in 20 ms steps")
	}
	return p, nil
}

func agentAudioScopes(row *callRow) []string {
	s := []string{"project", row.Direction}
	if row.AgentID > 0 {
		s = append(s, fmt.Sprintf("agent/%d", row.AgentID))
	}
	if row.RouteID != "" {
		s = append(s, "route/"+row.RouteID)
	}
	if row.CarrierConnectionID > 0 {
		s = append(s, fmt.Sprintf("account/%d", row.CarrierConnectionID))
	}
	number := row.FromNumber
	if row.Direction == "inbound" {
		number = row.ToNumber
	}
	if number != "" {
		s = append(s, "number/"+number)
	}
	return s
}
func (a *App) resolveAgentAudioForCall(row *callRow) (agentAudioProfile, error) {
	marks := []string{}
	args := []any{row.ProjectID}
	scopes := agentAudioScopes(row)
	for _, scope := range scopes {
		marks = append(marks, "?")
		args = append(args, scope)
	}
	rows, err := a.db().db.Query(`SELECT scope_key,config_json,expires_at FROM agent_audio_profiles WHERE project_id=? AND scope_key IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return agentAudioProfile{}, err
	}
	defer rows.Close()
	entries := map[string]agentAudioProfile{}
	for rows.Next() {
		var scope, raw, expiry string
		if err = rows.Scan(&scope, &raw, &expiry); err != nil {
			return agentAudioProfile{}, err
		}
		if expiry != "" {
			until, e := time.Parse(time.RFC3339Nano, expiry)
			if e != nil {
				return agentAudioProfile{}, e
			}
			if !until.After(time.Now()) {
				continue
			}
		}
		var p agentAudioProfile
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return p, err
		}
		entries[scope] = p
	}
	if err = rows.Err(); err != nil {
		return agentAudioProfile{}, err
	}
	var p agentAudioProfile
	for _, scope := range scopes {
		if entry, ok := entries[scope]; ok {
			p = entry
		}
	}
	return resolveAgentAudioProfile(p)
}
func (a *App) pinAgentAudioProfile(row *callRow) (agentAudioProfile, error) {
	var raw string
	err := a.db().db.QueryRow(`SELECT config_json FROM agent_audio_call_profiles WHERE call_id=? AND project_id=?`, row.ID, row.ProjectID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		p, e := a.resolveAgentAudioForCall(row)
		if e != nil {
			return p, e
		}
		data, _ := json.Marshal(p)
		if _, err = a.db().db.Exec(`INSERT INTO agent_audio_call_profiles(call_id,project_id,config_json) VALUES(?,?,?) ON CONFLICT(call_id) DO NOTHING`, row.ID, row.ProjectID, string(data)); err != nil {
			return p, err
		}
		err = a.db().db.QueryRow(`SELECT config_json FROM agent_audio_call_profiles WHERE call_id=? AND project_id=?`, row.ID, row.ProjectID).Scan(&raw)
	}
	if err != nil {
		return agentAudioProfile{}, err
	}
	var p agentAudioProfile
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	return resolveAgentAudioProfile(p)
}

func (a *App) toolAgentAudioProfile(c context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if callerAgentID(c) != 0 {
		return nil, errors.New("platform principal required for agent audio profiles")
	}
	project := ctx.CurrentProject()
	if project == "" {
		return nil, errors.New("project context required")
	}
	for key, value := range args {
		switch key {
		case "scope":
			if _, ok := value.(string); !ok {
				return nil, errors.New("scope must be a string")
			}
		case "reset":
			if _, ok := value.(bool); !ok {
				return nil, errors.New("reset must be a boolean")
			}
		case "config", "ttl_seconds":
		default:
			return nil, fmt.Errorf("unknown argument %s", key)
		}
	}
	ttl := 0
	if v, ok := args["ttl_seconds"]; ok {
		raw, _ := json.Marshal(v)
		if json.Unmarshal(raw, &ttl) != nil || ttl < 0 || ttl > 86400 {
			return nil, errors.New("ttl_seconds must be an integer from 0 to 86400")
		}
	}
	scope := strArg(args, "scope", "project")
	if len(scope) > 160 {
		return nil, errors.New("invalid scope")
	}
	valid := scope == "project" || scope == "inbound" || scope == "outbound"
	if strings.HasPrefix(scope, "route/") {
		route, err := a.routeForCaller(ctx, strings.TrimPrefix(scope, "route/"), 0)
		if err != nil {
			return nil, err
		}
		valid = route != nil
	}
	if strings.HasPrefix(scope, "agent/") {
		id, err := strconv.ParseInt(strings.TrimPrefix(scope, "agent/"), 10, 64)
		valid = err == nil && id > 0
	}
	if strings.HasPrefix(scope, "number/") {
		valid = validE164(strings.TrimPrefix(scope, "number/"))
	}
	if strings.HasPrefix(scope, "account/") {
		id, err := strconv.ParseInt(strings.TrimPrefix(scope, "account/"), 10, 64)
		valid = false
		if err == nil && id > 0 {
			for _, b := range ctx.IntegrationsFor("carrier") {
				if b != nil && b.ConnectionID == id {
					valid = true
				}
			}
		}
	}
	if !valid {
		return nil, errors.New("invalid agent audio profile scope")
	}
	if args["config"] != nil && boolArg(args, "reset", false) {
		return nil, errors.New("reset and config are mutually exclusive")
	}
	if boolArg(args, "reset", false) {
		if _, err := ctx.AppDB().Exec(`DELETE FROM agent_audio_profiles WHERE project_id=? AND scope_key=?`, project, scope); err != nil {
			return nil, err
		}
	}
	if value, ok := args["config"]; ok {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if string(raw) == "null" {
			return nil, errors.New("config must be an object")
		}
		var p agentAudioProfile
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&p); err != nil {
			return nil, err
		}
		p, err = resolveAgentAudioProfile(p)
		if err != nil {
			return nil, err
		}
		expiry := ""
		if ttl > 0 {
			expiry = time.Now().UTC().Add(time.Duration(ttl) * time.Second).Format(time.RFC3339Nano)
		}
		raw, _ = json.Marshal(p)
		if _, err = ctx.AppDB().Exec(`INSERT INTO agent_audio_profiles(project_id,scope_key,config_json,expires_at) VALUES(?,?,?,?) ON CONFLICT(project_id,scope_key) DO UPDATE SET config_json=excluded.config_json,expires_at=excluded.expires_at,revision=revision+1`, project, scope, string(raw), expiry); err != nil {
			return nil, err
		}
	}
	var raw, expiry string
	var revision int64
	err := ctx.AppDB().QueryRow(`SELECT config_json,revision,expires_at FROM agent_audio_profiles WHERE project_id=? AND scope_key=?`, project, scope).Scan(&raw, &revision, &expiry)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	p, _ := resolveAgentAudioProfile(agentAudioProfile{})
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
	}
	active := raw != ""
	if expiry != "" {
		until, e := time.Parse(time.RFC3339Nano, expiry)
		if e != nil {
			return nil, e
		}
		active = active && until.After(time.Now())
	}
	return map[string]any{"override_active": active, "config_semantics": "complete profile per scope; most specific active scope wins", "scope": scope, "config": p, "revision": revision, "expires_at": expiry, "applies_to": "new calls", "active_calls_retained": true}, nil
}
