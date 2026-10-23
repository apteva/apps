package main

import (
	"database/sql"
	"errors"
	"fmt"
)

// Firewall intent is explicit. Manifest listeners alone never imply public
// exposure, and the backend's private control endpoint can never be opened.
func nativeFirewallPolicy(inst *Instance, o nativeOptions, ports []nativePort, control int) (*FirewallPolicy, error) {
	if o.Firewall == nil {
		return nil, nil
	}
	p := *o.Firewall
	p.Rules = append([]FirewallRule{}, p.Rules...)
	if p.ExposeDeclaredPorts {
		sources := []string{"0.0.0.0/0"}
		if firewallCapabilities(inst).IPv6 {
			sources = append(sources, "::/0")
		}
		for _, v := range ports {
			if v.Protocol == "tcp" && v.Start == control && v.End == control {
				continue
			}
			p.Rules = append(p.Rules, FirewallRule{Protocol: v.Protocol, PortStart: v.Start, PortEnd: v.End, SourceCIDRs: sources})
		}
	}
	p.ExposeDeclaredPorts = false
	normalized, e := normalizeFirewallRules(p.Rules, firewallCapabilities(inst))
	if e != nil {
		return nil, e
	}
	for _, r := range normalized {
		if r.Protocol == "tcp" && r.PortStart <= control && r.PortEnd >= control {
			return nil, errors.New("firewall must not expose the private control port")
		}
	}
	p.Rules = normalized
	return &p, nil
}
func (b *nativeBackend) applyFirewall(inst *Instance, d nativeRecord, release bool) error {
	if d.Options.Firewall == nil {
		return nil
	}
	if b.appCtx == nil {
		return errors.New("firewall platform context unavailable")
	}
	p, e := nativeFirewallPolicy(inst, d.Options, d.Execution.Ports, d.ControlPort)
	if e != nil {
		return e
	}
	if release {
		p = nil
	} else if s, e := firewallLoad(b.db, inst.ID); e == nil && s.CreateAttempted {
		p.ReplaceExisting = false
	}
	_, e = firewallSet(b.appCtx, inst, fmt.Sprintf("native:%s", d.ID), p)
	return e
}
func nativeFirewallValidation(inst *Instance, o nativeOptions) error {
	if o.Firewall == nil {
		return nil
	}
	if _, e := normalizeFirewallRules(o.Firewall.Rules, firewallCapabilities(inst)); e != nil {
		return e
	}
	_, e := firewallInitial(inst, *o.Firewall)
	return e
}

func (b *nativeBackend) validateEffectiveFirewall(inst *Instance, control int) error {
	s, e := firewallLoad(b.db, inst.ID)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	rules, e := firewallDesired(b.db, inst, s)
	if e != nil {
		return e
	}
	for _, r := range rules {
		if r.Protocol == "tcp" && r.PortStart <= control && r.PortEnd >= control {
			return errors.New("existing firewall exposes proposed private control port; select another control_port")
		}
	}
	return nil
}
