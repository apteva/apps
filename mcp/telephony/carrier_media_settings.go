package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type carrierJitterBufferPolicy struct {
	Enabled bool `json:"enabled"`
	MinMS   int  `json:"min_ms"`
	MaxMS   int  `json:"max_ms"`
}

func (p carrierJitterBufferPolicy) resolved() carrierJitterBufferPolicy {
	if p.MinMS == 0 {
		p.MinMS = 60
	}
	if p.MaxMS == 0 {
		p.MaxMS = 200
	}
	return p
}
func (p carrierJitterBufferPolicy) validate() error {
	p = p.resolved()
	if p.MinMS < 40 || p.MaxMS > 400 || p.MinMS > p.MaxMS {
		return errors.New("carrier jitter buffer requires 40 <= min_ms <= max_ms <= 400")
	}
	return nil
}
func (p carrierJitterBufferPolicy) telnyx() map[string]any {
	p = p.resolved()
	return map[string]any{"enable_jitter_buffer": p.Enabled, "jitterbuffer_msec_min": p.MinMS, "jitterbuffer_msec_max": p.MaxMS}
}

type carrierMediaTarget struct {
	connection         int64
	provider, kind, id string
}
type carrierJitterBufferView struct {
	Requested    carrierJitterBufferPolicy  `json:"requested"`
	Effective    *carrierJitterBufferPolicy `json:"effective,omitempty"`
	State        string                     `json:"state"`
	VerifiedAt   string                     `json:"verified_at,omitempty"`
	Scope        string                     `json:"scope"`
	ResourceType string                     `json:"resource_type"`
	ResourceID   string                     `json:"resource_id"`
	Shared       bool                       `json:"shared_connection"`
	Message      string                     `json:"message,omitempty"`
}

func (a *App) carrierMediaTarget(project, route string, connection int64, number string) (carrierMediaTarget, error) {
	if route != "" {
		r, err := a.db().findRoute(route)
		if err != nil {
			return carrierMediaTarget{}, err
		}
		if r == nil || r.ProjectID != project {
			return carrierMediaTarget{}, errors.New("route not found in this project")
		}
		if r.InboundTransport != inboundTransportSIPDirect {
			return carrierMediaTarget{}, errors.New("route does not use SIP")
		}
		var cfg directSIPProviderConfig
		if err = json.Unmarshal([]byte(r.TransportConfig), &cfg); err != nil {
			return carrierMediaTarget{}, errors.New("SIP route has no verified carrier resource")
		}
		target := carrierMediaTarget{r.CarrierConnectionID, r.CarrierSlug, "fqdn_connection", cfg.FQDNConnectionID}
		if target.provider != "telnyx" {
			target.kind = "unsupported"
			target.id = r.ID
		}
		if !validProviderResourceID(target.id) {
			return carrierMediaTarget{}, errors.New("carrier SIP resource is not configured")
		}
		return target, nil
	}
	v, _, _, err := a.outboundTransport(project, connection, number)
	if err != nil {
		return carrierMediaTarget{}, err
	}
	if v.Transport != inboundTransportSIPDirect || v.Config == nil {
		return carrierMediaTarget{}, errors.New("outbound SIP must be configured first")
	}
	var provider string
	if err = a.db().db.QueryRow(`SELECT provider FROM outbound_transports WHERE project_id=? AND connection_id=? AND phone_number IN ('',?) ORDER BY CASE WHEN phone_number=? THEN 0 ELSE 1 END LIMIT 1`, project, connection, number, number).Scan(&provider); err != nil {
		return carrierMediaTarget{}, err
	}
	return carrierMediaTarget{connection, provider, v.Config.ResourceType, v.Config.ResourceID}, nil
}
func (a *App) carrierBufferView(project string, target carrierMediaTarget) (carrierJitterBufferView, error) {
	v := carrierJitterBufferView{Requested: (carrierJitterBufferPolicy{}).resolved(), State: "unverified", Scope: "carrier_connection_resource", ResourceType: target.kind, ResourceID: target.id, Shared: true}
	var requested, effective string
	err := a.db().db.QueryRow(`SELECT requested_json,effective_json,state,verified_at FROM carrier_media_settings WHERE project_id=? AND connection_id=? AND resource_type=? AND resource_id=?`, project, target.connection, target.kind, target.id).Scan(&requested, &effective, &v.State, &v.VerifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if requested != "" {
		if err = json.Unmarshal([]byte(requested), &v.Requested); err != nil {
			return v, err
		}
	}
	if effective != "" {
		if err = json.Unmarshal([]byte(effective), &v.Effective); err != nil {
			return v, err
		}
	}
	return v, nil
}
func (a *App) saveCarrierBufferView(project string, target carrierMediaTarget, v carrierJitterBufferView) error {
	requested, _ := json.Marshal(v.Requested)
	effective := ""
	if v.Effective != nil {
		raw, _ := json.Marshal(v.Effective)
		effective = string(raw)
	}
	_, err := a.db().db.Exec(`INSERT INTO carrier_media_settings(project_id,connection_id,resource_type,resource_id,requested_json,effective_json,state,verified_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,connection_id,resource_type,resource_id) DO UPDATE SET requested_json=excluded.requested_json,effective_json=excluded.effective_json,state=excluded.state,verified_at=excluded.verified_at,updated_at=excluded.updated_at`, project, target.connection, target.kind, target.id, string(requested), effective, v.State, v.VerifiedAt, ringTime(time.Now()))
	return err
}

// Admission is gated only for this carrier binding, then the lock is released
// before provider I/O. Existing audio never acquires this gate. In-memory gates
// disappear on process exit, so a failed/restarted setup cannot strand admission.
func (a *App) beginCarrierMediaChange(target carrierMediaTarget) (func(), bool, error) {
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	// A carrier resource can be reused through more than one authorized binding.
	// Gate every configured alias, across projects, without reading credentials.
	bindings := map[int64]bool{target.connection: true}
	rows, err := a.db().db.Query(`SELECT carrier_connection_id,transport_config FROM inbound_routes WHERE carrier_slug=? AND inbound_transport='sip_direct' UNION ALL SELECT connection_id,config_json FROM outbound_transports WHERE provider=? AND transport='sip_direct'`, target.provider, target.provider)
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		var id int64
		var raw string
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, false, err
		}
		var cfg map[string]any
		if json.Unmarshal([]byte(raw), &cfg) == nil && ((target.kind == "fqdn_connection" && stringValue(cfg["fqdn_connection_id"]) == target.id) || (stringValue(cfg["resource_type"]) == target.kind && stringValue(cfg["resource_id"]) == target.id)) {
			bindings[id] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	for id := range bindings {
		var active int
		if err = a.db().db.QueryRow(`SELECT COUNT(*) FROM calls WHERE carrier_connection_id=? AND (status NOT IN ('completed','failed','no-answer','busy','canceled') OR media_active=1)`, id).Scan(&active); err != nil {
			return nil, false, err
		}
		if active > 0 {
			return nil, false, nil
		}
		if _, busy := a.carrierMediaDrains.Load(id); busy {
			return nil, false, nil
		}
	}
	marker := newCallID()
	for id := range bindings {
		a.carrierMediaDrains.Store(id, marker)
	}
	return func() {
		for id := range bindings {
			a.carrierMediaDrains.CompareAndDelete(id, marker)
		}
	}, true, nil
}
func parseTelnyxJitterBuffer(data map[string]any) (*carrierJitterBufferPolicy, error) {
	values, ok := data["jitter_buffer"].(map[string]any)
	if !ok {
		return nil, errors.New("carrier omitted jitter buffer verification")
	}
	enabled, ok := values["enable_jitter_buffer"].(bool)
	if !ok {
		return nil, errors.New("invalid carrier jitter buffer verification")
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, errors.New("invalid carrier jitter buffer verification")
	}
	var bounds struct {
		Min int `json:"jitterbuffer_msec_min"`
		Max int `json:"jitterbuffer_msec_max"`
	}
	if json.Unmarshal(raw, &bounds) != nil || bounds.Min < 40 || bounds.Max > 400 || bounds.Min > bounds.Max {
		return nil, errors.New("invalid carrier jitter buffer bounds")
	}
	p := carrierJitterBufferPolicy{Enabled: enabled, MinMS: bounds.Min, MaxMS: bounds.Max}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &p, nil
}
func (a *App) configureCarrierMedia(parent context.Context, ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	project := ctx.CurrentProject()
	if project == "" {
		return nil, errors.New("project context required")
	}
	target, err := a.carrierMediaTarget(project, strArg(args, "route_id", ""), int64(intArg(args, "carrier_connection_id", 0)), strArg(args, "phone_number", ""))
	if err != nil {
		return nil, err
	}
	bound := false
	for _, b := range ctx.IntegrationsFor("carrier") {
		if b != nil && b.ConnectionID == target.connection {
			bound = true
		}
	}
	if !bound {
		return nil, errors.New("carrier account is not authorized")
	}
	request, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	unlock, err := lockOutboundSetup(request, fmt.Sprintf("carrier-media:%s:%d:%s:%d:%s:%s", ctx.DataDir(), a.installID, project, target.connection, target.kind, target.id))
	if err != nil {
		return nil, err
	}
	defer unlock()
	v, err := a.carrierBufferView(project, target)
	if err != nil {
		return nil, err
	}
	if args["carrier_jitter_buffer"] != nil {
		raw, _ := json.Marshal(args["carrier_jitter_buffer"])
		var supplied map[string]json.RawMessage
		if json.Unmarshal(raw, &supplied) != nil || supplied == nil {
			return nil, errors.New("carrier_jitter_buffer must be an object")
		}
		for _, key := range []string{"min_ms", "max_ms"} {
			if value, ok := supplied[key]; ok && (string(value) == "0" || string(value) == "null") {
				return nil, errors.New("carrier jitter buffer bounds must be 40–400 ms")
			}
		}
		if value, ok := supplied["enabled"]; ok && string(value) != "true" && string(value) != "false" {
			return nil, errors.New("enabled must be a boolean")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var desired carrierJitterBufferPolicy
		if err = decoder.Decode(&desired); err != nil {
			return nil, err
		}
		if err = desired.validate(); err != nil {
			return nil, err
		}
		if desired.Enabled && target.provider != "telnyx" {
			return nil, errors.New("carrier jitter buffering is not supported by this provider")
		}
		v.Requested = desired.resolved()
		v.State = "pending"
		if err = a.saveCarrierBufferView(project, target, v); err != nil {
			return nil, err
		}
	}
	if target.provider != "telnyx" {
		v.State = "unsupported"
		return map[string]any{"ok": true, "carrier_jitter_buffer": v}, nil
	}
	switch target.kind {
	case "credential_connection", "fqdn_connection", "ip_connection":
	default:
		return nil, errors.New("unsupported carrier SIP resource type")
	}
	if !boolArg(args, "fresh", false) && !boolArg(args, "apply", false) {
		return map[string]any{"ok": true, "carrier_jitter_buffer": v}, nil
	}
	creds, err := ctx.PlatformAPI().GetConnectionCredentials(target.connection)
	if err != nil || creds == nil || !strings.EqualFold(creds.Slug, target.provider) {
		return nil, errors.New("carrier credentials unavailable")
	}
	path := "/" + target.kind + "s/" + url.PathEscape(target.id)
	if boolArg(args, "apply", false) {
		release, idle, e := a.beginCarrierMediaChange(target)
		if e != nil {
			return nil, e
		}
		if !idle {
			v.State = "pending"
			v.Message = "Carrier connection is in use. Retry applying when it is idle; existing calls are unchanged."
			if err = a.saveCarrierBufferView(project, target, v); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "carrier_jitter_buffer": v}, nil
		}
		defer release()
		// Confirm the resource with these authorized credentials before mutating it.
		before, e := sipProviderGET(request, creds, path)
		if e != nil {
			err = e
		} else if stringValue(telnyxDataMap(before)["id"]) != target.id {
			return nil, errors.New("carrier resource identity verification failed")
		} else {
			_, err = sipProviderRequest(request, creds, "", http.MethodPatch, path, map[string]any{"jitter_buffer": v.Requested.telnyx()})
		}
	}
	if err == nil {
		var result map[string]any
		result, err = sipProviderGET(request, creds, path)
		if err == nil && stringValue(telnyxDataMap(result)["id"]) != target.id {
			err = errors.New("carrier resource identity verification failed")
		}
		if err == nil {
			var effective *carrierJitterBufferPolicy
			effective, err = parseTelnyxJitterBuffer(telnyxDataMap(result))
			if err == nil {
				v.Effective = effective
			}
		}
	}
	if err != nil {
		v.State = "verification_unavailable"
		v.Message = "Carrier settings could not be verified. The last verified value and timestamp, if present, are retained."
	} else {
		v.VerifiedAt = ringTime(time.Now())
		v.State = "verified"
		if *v.Effective != v.Requested {
			v.State = "mismatch"
			v.Message = "Carrier settings differ from the requested policy."
		}
	}
	if saveErr := a.saveCarrierBufferView(project, target, v); saveErr != nil {
		return nil, saveErr
	}
	return map[string]any{"ok": true, "carrier_jitter_buffer": v}, nil
}
func (a *App) toolCarrierMedia(caller context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if callerAgentID(caller) != 0 {
		return mcpError("platform principal required for shared carrier settings"), nil
	}
	result, err := a.configureCarrierMedia(caller, ctx, args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return result, nil
}
func (a *App) carrierBufferForCall(row *callRow) *carrierJitterBufferView {
	if row == nil || row.CarrierSlug != "telnyx" || row.CarrierConnectionID <= 0 {
		return nil
	}
	target, err := a.carrierMediaTarget(row.ProjectID, row.RouteID, row.CarrierConnectionID, row.FromNumber)
	if err != nil {
		return nil
	}
	view, err := a.carrierBufferView(row.ProjectID, target)
	if err != nil {
		return nil
	}
	return &view
}
