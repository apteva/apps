package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Frozen for one media attachment, like TURN policy. No database reads in RTP.
type phoneNetworkPolicy struct {
	UploadBitrateControl string `json:"upload_bitrate_control"`
	Transport            string `json:"media_transport"`
	InitialBitrate       int    `json:"initial_bitrate_bps"`
	MinBitrate           int    `json:"min_bitrate_bps"`
	MaxBitrate           int    `json:"max_bitrate_bps"`
	PlaybackTarget       int    `json:"playback_target_ms"`
	PlaybackMax          int    `json:"playback_max_ms"`
	QualityWarnings      bool   `json:"quality_warnings"`
	DegradedSeconds      int    `json:"degraded_seconds"`
	WarningCooldown      int    `json:"warning_cooldown_seconds"`
	BackgroundCheck      bool   `json:"background_check"`
	AllowUserOverrides   bool   `json:"allow_user_overrides"`
}

func defaultPhoneNetworkPolicy() phoneNetworkPolicy {
	return phoneNetworkPolicy{UploadBitrateControl: "standard", Transport: "inherit", InitialBitrate: 32000, MinBitrate: 16000, MaxBitrate: 32000, PlaybackTarget: 60, PlaybackMax: 160, QualityWarnings: false, DegradedSeconds: 6, WarningCooldown: 60, BackgroundCheck: false}
}
func (p phoneNetworkPolicy) validate() error {
	if p.UploadBitrateControl != "standard" && p.UploadBitrateControl != "guarded" {
		return errors.New("invalid upload_bitrate_control")
	}
	if p.Transport != "inherit" && p.Transport != "auto" && p.Transport != "webrtc" && p.Transport != "websocket" {
		return errors.New("invalid media_transport")
	}
	if p.MinBitrate < 16000 || p.MaxBitrate > 32000 || p.MinBitrate > p.InitialBitrate || p.InitialBitrate > p.MaxBitrate {
		return errors.New("bitrate bounds must satisfy 16000 <= minimum <= initial <= maximum <= 32000")
	}
	if p.PlaybackTarget < 40 || p.PlaybackTarget > 120 || p.PlaybackMax < p.PlaybackTarget || p.PlaybackMax > 200 {
		return errors.New("playback targets must satisfy 40 <= target <= 120 and target <= maximum <= 200 ms")
	}
	if p.DegradedSeconds < 4 || p.DegradedSeconds > 30 || p.WarningCooldown < 30 || p.WarningCooldown > 300 {
		return errors.New("quality notices require 4–30 seconds sustained degradation and 30–300 seconds cooldown")
	}
	return nil
}
func (a *App) readNetworkPolicy(project, principal string) (phoneNetworkPolicy, bool, error) {
	p := defaultPhoneNetworkPolicy()
	var raw string
	err := a.db().db.QueryRow(`SELECT policy_json FROM telephony_network_policies WHERE project_id=? AND principal=?`, project, principal).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if err = json.Unmarshal([]byte(raw), &p); err == nil {
		err = p.validate()
	}
	return p, true, err
}
func (a *App) effectiveNetworkPolicy(project string, identity phoneIdentity) (phoneNetworkPolicy, bool, error) {
	p, configured, err := a.readNetworkPolicy(project, "")
	if err != nil {
		return p, false, err
	}
	if identity.valid() {
		v, have, e := a.readNetworkPolicy(project, identity.key())
		if e != nil {
			return p, false, e
		}
		if have {
			p = v
			configured = true
		}
	}
	return p, configured, nil
}
func (a *App) resolvedNetworkPolicy(project string, identity phoneIdentity) (phoneNetworkPolicy, error) {
	p, _, err := a.effectiveNetworkPolicy(project, identity)
	return p, err
}
func (a *App) networkPolicies(ctx *sdk.AppCtx, args map[string]any, self *phonePrincipal) (map[string]any, error) {
	project := ctx.CurrentProject()
	if project == "" {
		return nil, errors.New("project required")
	}
	var identity phoneIdentity
	if self != nil {
		identity = self.Identity
		if args["identity"] != nil {
			return nil, errors.New("identity comes from authentication")
		}
	} else if args["identity"] != nil {
		raw, _ := json.Marshal(args["identity"])
		if json.Unmarshal(raw, &identity) != nil || !identity.valid() {
			return nil, errors.New("invalid adviser identity")
		}
		if _, err := a.phonePrincipal(project, identity); err != nil {
			return nil, errors.New("adviser not authorized in project")
		}
	}
	base, _, err := a.readNetworkPolicy(project, "")
	if err != nil {
		return nil, err
	}
	principal := ""
	if identity.valid() {
		principal = identity.key()
	}
	if args["policy"] != nil || boolArg(args, "inherit", false) {
		if self != nil && !base.AllowUserOverrides {
			return nil, errPhoneAccessDenied
		}
		if boolArg(args, "inherit", false) {
			if principal == "" {
				return nil, errors.New("project defaults cannot inherit")
			}
			_, err = a.db().db.Exec(`DELETE FROM telephony_network_policies WHERE project_id=? AND principal=?`, project, principal)
		} else {
			p := defaultPhoneNetworkPolicy()
			raw, _ := json.Marshal(args["policy"])
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&p) != nil {
				return nil, errors.New("invalid network policy")
			}
			if err = p.validate(); err != nil {
				return nil, err
			}
			if principal != "" && p.AllowUserOverrides {
				return nil, errors.New("only project defaults control override permissions")
			}
			raw, _ = json.Marshal(p)
			_, err = a.db().db.Exec(`INSERT INTO telephony_network_policies(project_id,principal,policy_json,updated_at) VALUES(?,?,?,?) ON CONFLICT(project_id,principal) DO UPDATE SET policy_json=excluded.policy_json,updated_at=excluded.updated_at`, project, principal, string(raw), ringTime(time.Now()))
		}
		if err != nil {
			return nil, err
		}
	}
	selected, have, err := a.readNetworkPolicy(project, principal)
	if err != nil {
		return nil, err
	}
	effective, configured, err := a.effectiveNetworkPolicy(project, identity)
	if err != nil {
		return nil, err
	}
	if !have && principal != "" {
		selected = base
	}
	result := map[string]any{"configured": configured, "policy": selected, "effective": effective, "override": have && principal != "", "allow_user_overrides": base.AllowUserOverrides, "scope": map[bool]string{true: "adviser", false: "project"}[principal != ""]}
	if self == nil {
		policy, e := a.phonePolicy(project)
		if e != nil {
			return nil, e
		}
		identities := []phoneIdentity{}
		for _, u := range policy.Users {
			if u.Enabled {
				identities = append(identities, u.Identity)
			}
		}
		result["advisers"] = identities
	}
	return result, nil
}
func (a *App) handleSelfNetworkPolicy(w http.ResponseWriter, r *http.Request, project string) {
	args := map[string]any{}
	if r.Method == http.MethodPost {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		if d.Decode(&args) != nil {
			http.Error(w, "invalid network policy", 400)
			return
		}
	}
	result, err := a.networkPolicies(globalCtx.WithProject(project), args, phoneUserFrom(r))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, result)
}
func (a *App) configureNetworkPolicy(c *softphoneRTCConfig, project string, identity phoneIdentity) error {
	p, configured, err := a.effectiveNetworkPolicy(project, identity)
	if err != nil {
		return err
	}
	if !configured {
		return nil
	}
	c.NetworkPolicy = &p
	c.Bitrate = p.InitialBitrate
	return nil
}
