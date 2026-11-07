package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
)

type sipAudioProfile struct {
	Preset   string            `json:"preset"`
	Codecs   []string          `json:"codecs,omitempty"`
	Playback sipPlaybackPolicy `json:"rtp_playback"`
}

func resolveSIPAudioProfile(p sipAudioProfile) (sipAudioProfile, error) {
	switch p.Preset {
	case "", "balanced":
		p.Preset = "balanced"
	case "resilient":
		if p.Playback.TargetMS == 0 {
			p.Playback.TargetMS = 80
		}
		if p.Playback.MaxTargetMS == 0 {
			p.Playback.MaxTargetMS = 120
		}
		if p.Playback.CapacityMS == 0 {
			p.Playback.CapacityMS = 200
		}
	case "reference":
		if p.Playback.Receiver == "" {
			p.Playback.Receiver = "reference"
		}
	case "custom":
	default:
		return p, errors.New("preset must be balanced, resilient, reference or custom")
	}
	p.Playback = p.Playback.resolved()
	p.Playback.ProfileRevision = "" // Assigned by the server, never supplied by a client.
	if err := validateLocalMediaSettings(localMediaSettings{Codecs: p.Codecs, RTPPlayback: &p.Playback}); err != nil {
		return p, err
	}
	return p, nil
}

func sipAudioScope(direction, route string, connection int64, number string) string {
	if route != "" {
		return "inbound/route/" + route
	}
	if direction == "outbound" && connection > 0 {
		return fmt.Sprintf("outbound/account/%d/%s", connection, number)
	}
	if direction != "" {
		return direction
	}
	return "project"
}

// This is a call-setup read only. Audio owners receive an immutable resolved
// copy, with a safe fingerprint; they never query policy or perform carrier I/O.
func (a *App) localMediaSettingsForCall(project, route, direction string, connection int64, number string) (localMediaSettings, error) {
	cfg, err := a.localMediaSettings(project, route)
	if err != nil {
		return cfg, err
	}
	scopes := []string{"project", direction}
	if direction == "inbound" && route != "" {
		scopes = append(scopes, sipAudioScope(direction, route, 0, ""))
	}
	if direction == "outbound" {
		scopes = append(scopes, sipAudioScope(direction, "", connection, ""))
		if number != "" {
			scopes = append(scopes, sipAudioScope(direction, "", connection, number))
		}
	}
	// At most five indexed rows. Apply from least to most specific; expiration
	// reveals the previous policy without a timer, worker or deletion race.
	marks := make([]string, len(scopes))
	args := []any{project}
	for i, s := range scopes {
		marks[i] = "?"
		args = append(args, s)
	}
	rows, err := a.db().db.Query(`SELECT scope_key,revision,config_json,expires_at FROM sip_audio_profiles WHERE project_id=? AND scope_key IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return cfg, err
	}
	defer rows.Close()
	type entry struct {
		profile  sipAudioProfile
		revision int64
	}
	found := map[string]entry{}
	now := time.Now().UTC()
	for rows.Next() {
		var scope, raw, expiry string
		var revision int64
		if err = rows.Scan(&scope, &revision, &raw, &expiry); err != nil {
			return cfg, err
		}
		if expiry != "" {
			until, e := time.Parse(time.RFC3339Nano, expiry)
			if e != nil {
				return cfg, e
			}
			if !until.After(now) {
				continue
			}
		}
		var p sipAudioProfile
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return cfg, err
		}
		p, err = resolveSIPAudioProfile(p)
		if err != nil {
			return cfg, err
		}
		found[scope] = entry{p, revision}
	}
	if err = rows.Err(); err != nil {
		return cfg, err
	}
	var origin string
	for _, scope := range scopes {
		if p, ok := found[scope]; ok {
			policy := p.profile.Playback
			cfg.RTPPlayback = &policy
			if len(p.profile.Codecs) > 0 {
				cfg.Codecs = append([]string(nil), p.profile.Codecs...)
				cfg.AudioProfileCodecs = true
			}
			origin = fmt.Sprintf("%s:%d", scope, p.revision)
		}
	}
	if cfg.RTPPlayback == nil {
		cfg.RTPPlayback = &sipPlaybackPolicy{}
	}
	policy := cfg.RTPPlayback.resolved()
	raw, _ := json.Marshal(struct {
		Scope  string
		Policy sipPlaybackPolicy
		Codecs []string
	}{origin, policy, cfg.Codecs})
	hash := sha256.Sum256(raw)
	policy.ProfileRevision = fmt.Sprintf("%x", hash[:8])
	cfg.RTPPlayback = &policy
	return cfg, nil
}

func (a *App) sipAudioProfileSettings(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	for _, key := range []string{"direction", "route_id", "phone_number", "expires_at"} {
		if value, ok := args[key]; ok {
			if _, valid := value.(string); !valid {
				return nil, fmt.Errorf("%s must be a string", key)
			}
		}
	}
	if value, ok := args["carrier_connection_id"]; ok {
		raw, _ := json.Marshal(value)
		var id int64
		if json.Unmarshal(raw, &id) != nil || id <= 0 {
			return nil, errors.New("carrier_connection_id must be a positive integer")
		}
	}
	project := ctx.CurrentProject()
	if project == "" {
		return nil, errors.New("project context required")
	}
	direction := strArg(args, "direction", "")
	route := strArg(args, "route_id", "")
	connection := int64(intArg(args, "carrier_connection_id", 0))
	number := strArg(args, "phone_number", "")
	if direction != "" && direction != "inbound" && direction != "outbound" {
		return nil, errors.New("invalid direction")
	}
	if route != "" {
		if direction == "outbound" || connection != 0 || number != "" {
			return nil, errors.New("route profile requires inbound scope")
		}
		r, e := a.routeForCaller(ctx, route, 0)
		if e != nil {
			return nil, e
		}
		direction = "inbound"
		connection = r.CarrierConnectionID
	} else if connection != 0 || number != "" {
		if direction != "outbound" || connection <= 0 || (number != "" && !validE164(number)) {
			return nil, errors.New("outbound number profile requires authorized carrier binding")
		}
		bound := false
		for _, b := range ctx.IntegrationsFor("carrier") {
			if b != nil && b.ConnectionID == connection {
				bound = true
			}
		}
		if !bound {
			return nil, errors.New("carrier binding is not authorized")
		}
	}
	scope := sipAudioScope(direction, route, connection, number)
	if boolArg(args, "reset", false) && args["config"] != nil {
		return nil, errors.New("reset and config are mutually exclusive")
	}
	if boolArg(args, "reset", false) {
		if _, err := ctx.AppDB().Exec(`DELETE FROM sip_audio_profiles WHERE project_id=? AND scope_key=?`, project, scope); err != nil {
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
		var profile sipAudioProfile
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&profile); err != nil {
			return nil, err
		}
		profile, err = resolveSIPAudioProfile(profile)
		if err != nil {
			return nil, err
		}
		expiry := strArg(args, "expires_at", "")
		if v, ok := args["ttl_seconds"]; ok {
			if expiry != "" {
				return nil, errors.New("use ttl_seconds or expires_at, not both")
			}
			raw, _ := json.Marshal(v)
			var ttl int
			if json.Unmarshal(raw, &ttl) != nil || ttl < 0 || ttl > 86400 {
				return nil, errors.New("ttl_seconds must be an integer from 0 to 86400")
			}
			if ttl > 0 {
				expiry = time.Now().UTC().Add(time.Duration(ttl) * time.Second).Format(time.RFC3339Nano)
			}
		}
		if expiry != "" {
			until, e := time.Parse(time.RFC3339Nano, expiry)
			if e != nil || !until.After(time.Now()) || until.After(time.Now().Add(24*time.Hour)) {
				return nil, errors.New("expires_at must be within the next 24 hours")
			}
			expiry = until.UTC().Format(time.RFC3339Nano)
		}
		raw, _ = json.Marshal(profile)
		_, err = ctx.AppDB().Exec(`INSERT INTO sip_audio_profiles(project_id,scope_key,revision,config_json,expires_at,updated_at) VALUES(?,?,1,?,?,?) ON CONFLICT(project_id,scope_key) DO UPDATE SET revision=revision+1,config_json=excluded.config_json,expires_at=excluded.expires_at,updated_at=excluded.updated_at`, project, scope, string(raw), expiry, ringTime(time.Now()))
		if err != nil {
			return nil, err
		}
	}
	effective, err := a.localMediaSettingsForCall(project, route, direction, connection, number)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"scope": scope, "effective": effective, "applies_to": "new calls", "active_calls_retained": true}
	var revision int64
	var raw, expiry string
	err = ctx.AppDB().QueryRow(`SELECT revision,config_json,expires_at FROM sip_audio_profiles WHERE project_id=? AND scope_key=?`, project, scope).Scan(&revision, &raw, &expiry)
	if err == nil {
		var p sipAudioProfile
		if e := json.Unmarshal([]byte(raw), &p); e != nil {
			return nil, e
		}
		result["config"] = p
		result["revision"] = revision
		result["expires_at"] = expiry
		active := true
		if expiry != "" {
			until, e := time.Parse(time.RFC3339Nano, expiry)
			active = e == nil && until.After(time.Now())
		}
		result["active_override"] = active
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return result, nil
}

func (a *App) toolSIPAudioProfile(c context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if callerAgentID(c) != 0 {
		return nil, errors.New("platform principal required for SIP audio profiles")
	}
	return a.sipAudioProfileSettings(ctx, args)
}
