package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"strings"
)

func firewallRuleSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"protocol": map[string]any{"type": "string", "enum": []string{"tcp", "udp", "icmp"}}, "port_start": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}, "port_end": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}, "source_cidrs": map[string]any{"type": "array", "minItems": 1, "maxItems": 16, "items": map[string]any{"type": "string"}}}, "required": []string{"protocol", "source_cidrs"}}
}
func firewallPolicySchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "description": "Opt-in inbound allow list; outbound remains allowed. SSH is always retained. management_cidrs default to public IPv4/IPv6 SSH and are fixed at first adoption. replace_existing explicitly replaces this host's existing firewall attachments without changing shared rules. The Linux firewall is not changed.", "properties": map[string]any{"rules": map[string]any{"type": "array", "maxItems": 50, "items": firewallRuleSchema()}, "management_cidrs": map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"type": "string"}}, "replace_existing": map[string]any{"type": "boolean"}, "expose_declared_ports": map[string]any{"type": "boolean", "description": "Native backend only: allow declared app listeners from public IPv4/IPv6, excluding private control."}}, "required": []string{"rules"}}
}
func firewallPolicyArg(args map[string]any) (*FirewallPolicy, error) {
	v, ok := args["firewall"]
	if !ok {
		return nil, nil
	}
	if v == nil {
		return nil, errors.New("firewall must be an object")
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	var p FirewallPolicy
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&p); e != nil {
		return nil, e
	}
	return &p, nil
}
func firewallOwner(v string) error {
	if !nativeID(v) || strings.HasPrefix(v, "native") || v == "instance" {
		return errors.New("owner must be an identifier; instance and native owners are reserved")
	}
	return nil
}
func firewallView(ctx *sdk.AppCtx, inst *Instance, refresh bool) (map[string]any, error) {
	out := map[string]any{"capabilities": firewallCapabilities(inst), "guest_firewall": "externally_managed", "guest_reachability_verified": false}
	s, e := firewallLoad(ctx.AppDB(), inst.ID)
	if errors.Is(e, sql.ErrNoRows) {
		out["status"] = "unmanaged"
		return out, nil
	}
	if e != nil {
		return nil, e
	}
	out["firewall"] = s
	rules, e := firewallDesired(ctx.AppDB(), inst, s)
	if e != nil {
		return nil, e
	}
	out["effective_rules"] = rules
	owners := map[string]json.RawMessage{}
	rows, e := ctx.AppDB().Query(`SELECT owner,rules FROM instance_firewall_owners WHERE instance_id=? ORDER BY owner`, inst.ID)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var k, v string
		if e = rows.Scan(&k, &v); e != nil {
			break
		}
		owners[k] = json.RawMessage(v)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if re != nil {
		return nil, re
	}
	out["owners"] = owners
	if refresh {
		verified := false
		d := firewallDriver{ctx: ctx, inst: inst}
		attached, e := d.attached()
		if e != nil {
			return nil, e
		}
		out["attached_firewall_ids"] = attached
		if s.ResourceID != "" && len(attached) == 1 && attached[0] == s.ResourceID {
			if e = d.ensureOwned(s); e != nil {
				return nil, e
			}
			verified = d.verifyRules(s.ResourceID, rules) == nil
		}
		out["provider_verified"] = verified
	}
	return out, nil
}
func (a *App) toolFirewallCapabilities(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, ok := args["id"]; ok {
		inst, e := dbGetInstance(ctx.AppDB(), int64Arg(args, "id"))
		if e != nil {
			return nil, e
		}
		return map[string]any{"capabilities": firewallCapabilities(inst)}, nil
	}
	provider, e := resolveInstanceProvider(ctx, strArg(args, "provider"))
	if e != nil {
		return nil, e
	}
	if _, e = storageBinding(ctx, provider, int64Arg(args, "provider_connection_id")); e != nil {
		return nil, e
	}
	return map[string]any{"capabilities": firewallCapabilities(&Instance{ID: 1, Provider: provider})}, nil
}
func (a *App) toolFirewallGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	inst, e := dbGetInstance(ctx.AppDB(), int64Arg(args, "id"))
	if e != nil {
		return nil, e
	}
	return firewallView(ctx, inst, args["refresh"] == true)
}
func (a *App) toolFirewallApply(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	owner := strArg(args, "owner")
	if e := firewallOwner(owner); e != nil {
		return nil, e
	}
	p, e := firewallPolicyArg(args)
	if e != nil {
		return nil, e
	}
	if p == nil || p.ExposeDeclaredPorts {
		return nil, errors.New("explicit firewall rules required; expose_declared_ports is native-only")
	}
	inst, e := dbGetInstance(ctx.AppDB(), int64Arg(args, "id"))
	if e != nil {
		return nil, e
	}
	_, e = firewallSet(ctx, inst, owner, p)
	if e != nil {
		return nil, e
	}
	return firewallView(ctx, inst, false)
}
func (a *App) toolFirewallRelease(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	owner := strArg(args, "owner")
	if e := firewallOwner(owner); e != nil {
		return nil, e
	}
	inst, e := dbGetInstance(ctx.AppDB(), int64Arg(args, "id"))
	if e != nil {
		return nil, e
	}
	_, e = firewallSet(ctx, inst, owner, nil)
	if e != nil {
		return nil, e
	}
	return firewallView(ctx, inst, false)
}
func (a *App) handleFirewallCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, 405, "GET only")
		return
	}
	args := map[string]any{"provider": r.URL.Query().Get("provider")}
	if n, e := parseID(r.URL.Query().Get("provider_connection_id")); e == nil {
		args["provider_connection_id"] = n
	}
	out, e := a.toolFirewallCapabilities(appCtxForRequest(r), args)
	if e != nil {
		httpProviderErr(w, e)
		return
	}
	httpJSON(w, out)
}
func (a *App) httpFirewall(w http.ResponseWriter, r *http.Request, id int64) {
	ctx := appCtxForRequest(r)
	args := map[string]any{"id": id}
	var handler func(*sdk.AppCtx, map[string]any) (any, error)
	switch r.Method {
	case http.MethodGet:
		args["refresh"] = r.URL.Query().Get("refresh") == "true"
		handler = a.toolFirewallGet
	case http.MethodPut:
		dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
		if e := dec.Decode(&args); e != nil {
			httpErr(w, 400, "invalid firewall request")
			return
		}
		if e := dec.Decode(new(any)); e != io.EOF {
			httpErr(w, 400, "unexpected trailing request content")
			return
		}
		args["id"] = id
		handler = a.toolFirewallApply
	case http.MethodDelete:
		args["owner"] = r.URL.Query().Get("owner")
		handler = a.toolFirewallRelease
	default:
		httpErr(w, 405, "GET, PUT or DELETE")
		return
	}
	out, e := handler(ctx, args)
	if e != nil {
		httpProviderErr(w, e)
		return
	}
	httpJSON(w, out)
}
