package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Firewall policies are allow lists, with inbound default-deny and unrestricted
// outbound traffic. A separate owner per workload lets shared hosts keep the
// union of their requirements. The host OS firewall remains externally managed.
type FirewallRule struct {
	Protocol    string   `json:"protocol"`
	PortStart   int      `json:"port_start,omitempty"`
	PortEnd     int      `json:"port_end,omitempty"`
	SourceCIDRs []string `json:"source_cidrs"`
}
type FirewallPolicy struct {
	Rules               []FirewallRule `json:"rules"`
	ManagementCIDRs     []string       `json:"management_cidrs,omitempty"`
	ReplaceExisting     bool           `json:"replace_existing,omitempty"`
	ExposeDeclaredPorts bool           `json:"expose_declared_ports,omitempty"`
}
type FirewallCapabilities struct {
	Supported       bool   `json:"supported"`
	IPv6            bool   `json:"ipv6"`
	MaxRules        int    `json:"max_rules"`
	InboundDefault  string `json:"inbound_default"`
	OutboundDefault string `json:"outbound_default"`
	GuestFirewall   string `json:"guest_firewall"`
	Reason          string `json:"reason,omitempty"`
}
type firewallState struct {
	Name            string   `json:"name"`
	Provider        string   `json:"provider"`
	ConnectionID    int64    `json:"connection_id"`
	ServerID        string   `json:"server_id,omitempty"`
	ResourceID      string   `json:"resource_id,omitempty"`
	ManagementCIDRs []string `json:"management_cidrs"`
	Previous        []string `json:"previous_firewalls,omitempty"`
	ReplaceExisting bool     `json:"replace_existing"`
	CreateAttempted bool     `json:"create_attempted"`
	Phase           string   `json:"phase"`
	AppliedHash     string   `json:"applied_hash,omitempty"`
	LastError       string   `json:"error,omitempty"`
	UpdatedAt       string   `json:"updated_at"`
}

func firewallCapabilities(inst *Instance) FirewallCapabilities {
	c := FirewallCapabilities{InboundDefault: "deny", OutboundDefault: "allow", GuestFirewall: "externally_managed", MaxRules: 50}
	switch inst.Provider {
	case "clouding":
		c.Supported = true
	case "hetzner", "digitalocean", "linode":
		c.Supported = true
		c.IPv6 = true
	case "vultr":
		c.Supported = true
		c.IPv6 = true
	default:
		c.Reason = "provider/product has no managed-firewall adapter; no changes will be made"
	}
	if inst.IsLocal() || inst.ID == 0 || inst.ResourceClass == "container" {
		c.Supported = false
		c.Reason = "requires a provider-managed remote compute host"
	}
	if inst.ResourceClass == "bare_metal" && inst.Provider != "vultr" {
		c.Supported = false
		c.Reason = "this provider's bare-metal firewall is not supported"
	}
	if inst.Provider == "linode" {
		c.MaxRules = 25
	}
	return c
}
func firewallCIDRs(values []string, ipv6 bool) ([]string, error) {
	if len(values) == 0 || len(values) > 16 {
		return nil, errors.New("each rule requires 1 to 16 explicit source_cidrs")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		p, e := netip.ParsePrefix(v)
		if e != nil || p.Addr().Is4In6() {
			return nil, fmt.Errorf("invalid source CIDR %q", v)
		}
		if !ipv6 && p.Addr().Is6() {
			return nil, errors.New("IPv6 firewall rules are unsupported on this target")
		}
		s := p.Masked().String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}
func normalizeFirewallRules(rules []FirewallRule, c FirewallCapabilities) ([]FirewallRule, error) {
	if !c.Supported {
		return nil, errors.New(c.Reason)
	}
	if len(rules) > 3202 {
		return nil, errors.New("too many firewall rules")
	}
	out := []FirewallRule{}
	seen := map[string]bool{}
	for _, r := range rules {
		r.Protocol = strings.ToLower(r.Protocol)
		if r.Protocol != "tcp" && r.Protocol != "udp" && r.Protocol != "icmp" {
			return nil, errors.New("firewall protocol must be tcp, udp or icmp")
		}
		if r.Protocol == "icmp" {
			if r.PortStart != 0 || r.PortEnd != 0 {
				return nil, errors.New("ICMP rules cannot specify ports")
			}
		} else {
			if r.PortEnd == 0 {
				r.PortEnd = r.PortStart
			}
			if r.PortStart < 1 || r.PortEnd < r.PortStart || r.PortEnd > 65535 {
				return nil, errors.New("invalid firewall port range")
			}
		}
		sources, e := firewallCIDRs(r.SourceCIDRs, c.IPv6)
		if e != nil {
			return nil, e
		}
		// One source per normalized rule makes ownership union and provider limits exact.
		for _, s := range sources {
			v := r
			v.SourceCIDRs = []string{s}
			key := nativeJSON(v)
			if !seen[key] {
				seen[key] = true
				out = append(out, v)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return nativeJSON(out[i]) < nativeJSON(out[j]) })
	if len(out) > c.MaxRules {
		return nil, fmt.Errorf("effective firewall exceeds target limit of %d rules", c.MaxRules)
	}
	return out, nil
}
func firewallLoad(db *sql.DB, id int64) (firewallState, error) {
	var s firewallState
	var raw string
	e := db.QueryRow(`SELECT record FROM instance_firewalls WHERE instance_id=?`, id).Scan(&raw)
	if e == nil {
		e = json.Unmarshal([]byte(raw), &s)
	}
	return s, e
}
func firewallSave(db *sql.DB, id int64, s firewallState) error {
	s.UpdatedAt = nowUTC()
	_, e := db.Exec(`INSERT INTO instance_firewalls(instance_id,record) VALUES(?,?) ON CONFLICT(instance_id) DO UPDATE SET record=excluded.record`, id, nativeJSON(s))
	return e
}
func firewallDesired(db *sql.DB, inst *Instance, s firewallState) ([]FirewallRule, error) {
	port := inst.SSHPort
	if port == 0 {
		port = 22
	}
	rules := []FirewallRule{{Protocol: "tcp", PortStart: port, PortEnd: port, SourceCIDRs: s.ManagementCIDRs}}
	rows, e := db.Query(`SELECT rules FROM instance_firewall_owners WHERE instance_id=? ORDER BY owner`, inst.ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var r []FirewallRule
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &r); e != nil {
			return nil, e
		}
		rules = append(rules, r...)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return normalizeFirewallRules(rules, firewallCapabilities(inst))
}
func firewallInitial(inst *Instance, p FirewallPolicy) (firewallState, error) {
	c := firewallCapabilities(inst)
	if !c.Supported {
		return firewallState{}, errors.New(c.Reason)
	}
	sources := p.ManagementCIDRs
	if len(sources) == 0 {
		sources = []string{"0.0.0.0/0"}
		if c.IPv6 {
			sources = append(sources, "::/0")
		}
	}
	sources, e := firewallCIDRs(sources, c.IPv6)
	if e != nil {
		return firewallState{}, e
	}
	return firewallState{Name: "apteva-fw-" + newRequestID()[:20], Provider: inst.Provider, ConnectionID: inst.ProviderConnectionID, ServerID: inst.ProviderID, ManagementCIDRs: sources, ReplaceExisting: p.ReplaceExisting, Phase: "pending"}, nil
}
func firewallQueue(db *sql.DB, inst *Instance, owner string, p FirewallPolicy) error {
	if len(p.Rules) > 50 {
		return errors.New("maximum 50 rules per firewall policy")
	}
	if inst.ProviderConnectionID <= 0 {
		return errors.New("managed firewall requires a recorded provider_connection_id")
	}
	var ownerCount int
	if e := db.QueryRow(`SELECT count(*) FROM instance_firewall_owners WHERE instance_id=? AND owner!=?`, inst.ID, owner).Scan(&ownerCount); e != nil {
		return e
	}
	if ownerCount >= 64 {
		return errors.New("maximum 64 firewall policy owners per host")
	}
	rules, e := normalizeFirewallRules(p.Rules, firewallCapabilities(inst))
	if e != nil {
		return e
	}
	s, e := firewallLoad(db, inst.ID)
	if errors.Is(e, sql.ErrNoRows) {
		s, e = firewallInitial(inst, p)
	}
	if e != nil {
		return e
	}
	if len(p.ManagementCIDRs) > 0 {
		v, e := firewallCIDRs(p.ManagementCIDRs, firewallCapabilities(inst).IPv6)
		if e != nil {
			return e
		}
		if nativeJSON(v) != nativeJSON(s.ManagementCIDRs) {
			return errors.New("management_cidrs are fixed when firewall management begins")
		}
	}
	if s.Provider != inst.Provider || s.ConnectionID != inst.ProviderConnectionID || s.ServerID != "" && s.ServerID != inst.ProviderID {
		return errors.New("firewall target identity changed; explicit recovery required")
	}
	// Check union before writing anything, so rejected input cannot poison recovery.
	rows, e := db.Query(`SELECT rules FROM instance_firewall_owners WHERE instance_id=? AND owner!=?`, inst.ID, owner)
	if e != nil {
		return e
	}
	all := append([]FirewallRule{}, rules...)
	port := inst.SSHPort
	if port == 0 {
		port = 22
	}
	all = append(all, FirewallRule{Protocol: "tcp", PortStart: port, PortEnd: port, SourceCIDRs: s.ManagementCIDRs})
	for rows.Next() {
		var raw string
		var r []FirewallRule
		if e = rows.Scan(&raw); e != nil {
			break
		}
		if e = json.Unmarshal([]byte(raw), &r); e != nil {
			break
		}
		all = append(all, r...)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if re != nil {
		return re
	}
	normalized, normErr := normalizeFirewallRules(all, firewallCapabilities(inst))
	if normErr != nil {
		return normErr
	}
	private, e := db.Query(`SELECT json_extract(record,'$.control_port') FROM native_deployments WHERE instance_id=? AND json_extract(record,'$.execution.state')!='deleted'`, inst.ID)
	if e != nil {
		return e
	}
	for private.Next() {
		var port int
		if e = private.Scan(&port); e != nil {
			break
		}
		for _, r := range normalized {
			if r.Protocol == "tcp" && r.PortStart <= port && r.PortEnd >= port {
				e = errors.New("effective firewall policy exposes a retained native control port")
				break
			}
		}
		if e != nil {
			break
		}
	}
	privateErr := private.Err()
	private.Close()
	if e != nil {
		return e
	}
	if privateErr != nil {
		return privateErr
	}
	if _, e = normalizeFirewallRules(all, firewallCapabilities(inst)); e != nil {
		return e
	}
	tx, e := db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.Exec(`INSERT INTO instance_firewall_owners(instance_id,owner,rules) VALUES(?,?,?) ON CONFLICT(instance_id,owner) DO UPDATE SET rules=excluded.rules`, inst.ID, owner, nativeJSON(rules))
	if e != nil {
		return e
	}
	if p.ReplaceExisting {
		s.ReplaceExisting = true
	}
	s.Phase = "pending"
	s.LastError = ""
	s.UpdatedAt = nowUTC()
	_, e = tx.Exec(`INSERT INTO instance_firewalls(instance_id,record) VALUES(?,?) ON CONFLICT(instance_id) DO UPDATE SET record=excluded.record`, inst.ID, nativeJSON(s))
	if e != nil {
		return e
	}
	return tx.Commit()
}
func firewallSet(ctx *sdk.AppCtx, inst *Instance, owner string, p *FirewallPolicy) (firewallState, error) {
	unlock, e := lockResource(ctx.AppDB(), "firewall", inst.ID)
	if e != nil {
		return firewallState{}, e
	}
	defer unlock()
	fresh, e := dbGetInstance(ctx.AppDB(), inst.ID)
	if e != nil {
		return firewallState{}, e
	}
	inst = fresh
	if inst.Status != "ready" && inst.Status != "provisioning" && !(inst.Status == "error" && inst.LifecycleStage == "Firewall") {
		return firewallState{}, errors.New("firewall changes require a ready/provisioning instance")
	}
	if p == nil {
		s, e := firewallLoad(ctx.AppDB(), inst.ID)
		if errors.Is(e, sql.ErrNoRows) {
			return firewallState{Phase: "unmanaged"}, nil
		}
		if e != nil {
			return s, e
		}
		s.Phase = "pending"
		s.UpdatedAt = nowUTC()
		tx, e := ctx.AppDB().Begin()
		if e != nil {
			return s, e
		}
		defer tx.Rollback()
		if _, e = tx.Exec(`DELETE FROM instance_firewall_owners WHERE instance_id=? AND owner=?`, inst.ID, owner); e != nil {
			return s, e
		}
		if _, e = tx.Exec(`UPDATE instance_firewalls SET record=? WHERE instance_id=?`, nativeJSON(s), inst.ID); e != nil {
			return s, e
		}
		if e = tx.Commit(); e != nil {
			return s, e
		}

	} else {
		if e = firewallQueue(ctx.AppDB(), inst, owner, *p); e != nil {
			return firewallState{}, e
		}
	}
	return firewallReconcileRetryLocked(ctx, inst, false)
}
func firewallReconcile(ctx *sdk.AppCtx, inst *Instance, prepare bool) (firewallState, error) {
	unlock, e := lockResource(ctx.AppDB(), "firewall", inst.ID)
	if e != nil {
		return firewallState{}, e
	}
	defer unlock()
	return firewallReconcileRetryLocked(ctx, inst, prepare)
}

var errFirewallPending = errors.New("provider firewall rollout pending; retry")

func firewallReconcileRetryLocked(ctx *sdk.AppCtx, inst *Instance, prepare bool) (firewallState, error) {
	var s firewallState
	var e error
	for attempt := 0; attempt < 10; attempt++ {
		s, e = firewallReconcileLocked(ctx, inst, prepare)
		if !errors.Is(e, errFirewallPending) {
			return s, e
		}
		if attempt < 9 {
			select {
			case <-ctx.Done():
				return s, context.Canceled
			case <-time.After(time.Second):
			}
		}
	}
	return s, e
}
func firewallReconcileLocked(ctx *sdk.AppCtx, inst *Instance, prepare bool) (s firewallState, err error) {
	s, err = firewallLoad(ctx.AppDB(), inst.ID)
	if err != nil {
		return s, err
	}
	if s.Provider != inst.Provider || s.ConnectionID != inst.ProviderConnectionID || s.ServerID != "" && s.ServerID != inst.ProviderID {
		return s, errors.New("firewall target identity changed")
	}
	defer func() {
		if err != nil {
			s.Phase = "pending"
			s.LastError = "provider firewall not confirmed; inspect/retry"
			_ = firewallSave(ctx.AppDB(), inst.ID, s)
		}
	}()
	rules, e := firewallDesired(ctx.AppDB(), inst, s)
	if e != nil {
		return s, e
	}
	d := firewallDriver{ctx: ctx, inst: inst}
	if inst.ProviderID != "" && !prepare {
		attached, e := d.attached()
		if e != nil {
			return s, e
		}
		for _, id := range attached {
			if id != s.ResourceID && !s.ReplaceExisting && !containsFirewall(s.Previous, id) {
				return s, errors.New("existing firewall attachment requires explicit replace_existing=true; shared rules will not be modified")
			}
		}
		s.ServerID = inst.ProviderID
		if s.ReplaceExisting {
			for _, id := range attached {
				if id != s.ResourceID && !containsFirewall(s.Previous, id) {
					s.Previous = append(s.Previous, id)
				}
			}
			s.ReplaceExisting = false
		}
		if e = firewallSave(ctx.AppDB(), inst.ID, s); e != nil {
			return s, e
		}
	}
	if s.ResourceID == "" {
		found, e := d.find(s.Name)
		if e != nil {
			return s, e
		}
		if found == "" {
			if s.CreateAttempted {
				return s, errors.New("firewall creation outcome unknown; resource not yet visible; retry inspection without creating another")
			}
			s.CreateAttempted = true
			if e = firewallSave(ctx.AppDB(), inst.ID, s); e != nil {
				return s, e
			}
			found, e = d.create(s.Name, rules)
			if e != nil {
				var rejected firewallRejectedCreate
				if errors.As(e, &rejected) {
					s.CreateAttempted = false
				}
				return s, e
			}
			if found == "" {
				return s, errors.New("provider created no identifiable firewall")
			}
		}
		s.ResourceID = found
		if e = firewallSave(ctx.AppDB(), inst.ID, s); e != nil {
			return s, e
		}
	}
	if e = d.ensureOwned(s); e != nil {
		return s, e
	}
	if e = d.setRules(s.ResourceID, rules); e != nil {
		return s, e
	}
	if e = d.verifyRules(s.ResourceID, rules); e != nil {
		return s, fmt.Errorf("%w: rules not yet confirmed", errFirewallPending)
	}
	if !prepare && inst.ProviderID != "" {
		if e = d.attach(s.ResourceID); e != nil {
			return s, e
		}
		// Never detach the last management firewall before the replacement is visible.
		attached, e := d.attached()
		if e != nil {
			return s, e
		}
		if !containsFirewall(attached, s.ResourceID) {
			return s, errFirewallPending
		}
		for _, old := range attached {
			if old != s.ResourceID {
				if !containsFirewall(s.Previous, old) {
					return s, errors.New("unexpected firewall attachment")
				}
				if e = d.detach(old); e != nil {
					return s, e
				}
			}
		}
		attached, e = d.attached()
		if e != nil {
			return s, e
		}
		if len(attached) != 1 || attached[0] != s.ResourceID {
			return s, errFirewallPending
		}
		if e = d.verifyRules(s.ResourceID, rules); e != nil {
			return s, e
		}
		s.Phase = "applied"
	} else {
		s.Phase = "prepared"
	}
	s.AppliedHash = nativeHash(nativeJSON(rules))
	s.LastError = ""
	err = firewallSave(ctx.AppDB(), inst.ID, s)
	return s, err
}
func containsFirewall(v []string, id string) bool {
	for _, x := range v {
		if x == id {
			return true
		}
	}
	return false
}

// Recovery is attempted once at controller mount and on explicit requests, never
// on each metrics import or health inspection. Applied policies do no polling.
func reconcilePendingFirewalls(ctx *sdk.AppCtx) {
	rows, e := ctx.AppDB().Query(`SELECT instance_id FROM instance_firewalls WHERE json_extract(record,'$.phase')='pending'`)
	if e != nil {
		return
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		select {
		case <-ctx.Done():
			return
		default:
		}
		inst, e := dbGetInstance(ctx.AppDB(), id)
		if e == nil && inst.ProviderID != "" && (inst.Status == "ready" || inst.Status == "provisioning") {
			_, _ = firewallReconcile(ctx, inst, false)
		}
	}
}
func firewallBeforeReady(ctx *sdk.AppCtx, inst *Instance) error {
	_, e := firewallLoad(ctx.AppDB(), inst.ID)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	_, e = firewallReconcile(ctx, inst, false)
	return e
}
func firewallDestroy(ctx *sdk.AppCtx, inst *Instance) error {
	unlock, e := lockResource(ctx.AppDB(), "firewall", inst.ID)
	if e != nil {
		return e
	}
	defer unlock()
	s, e := firewallLoad(ctx.AppDB(), inst.ID)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if s.Provider != inst.Provider || s.ConnectionID != inst.ProviderConnectionID || s.ServerID != "" && s.ServerID != inst.ProviderID {
		return errors.New("firewall target changed; cleanup refused")
	}
	d := firewallDriver{ctx: ctx, inst: inst}
	if s.ResourceID == "" && s.CreateAttempted {
		s.ResourceID, e = d.find(s.Name)
		if e != nil {
			return e
		}
		if s.ResourceID == "" {
			return errors.New("unknown firewall creation must be reconciled before forgetting the instance")
		}
	}
	if s.ResourceID != "" {
		if e = d.remove(s); e != nil {
			return e
		}
	}
	_, e = ctx.AppDB().Exec(`DELETE FROM instance_firewall_owners WHERE instance_id=?`, inst.ID)
	if e != nil {
		return e
	}
	_, e = ctx.AppDB().Exec(`DELETE FROM instance_firewalls WHERE instance_id=?`, inst.ID)
	return e
}
