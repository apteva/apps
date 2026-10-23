package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type firewallDriver struct {
	ctx  *sdk.AppCtx
	inst *Instance
}

func (d firewallDriver) call(tool string, args map[string]any) (json.RawMessage, error) {
	var data json.RawMessage
	var e error
	if tool == "firewall_create" {
		bound, err := storageBinding(d.ctx, d.inst.Provider, d.inst.ProviderConnectionID)
		if err != nil {
			return nil, err
		}
		res, err := d.ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, args)
		if err != nil {
			return nil, err
		}
		if res == nil || !res.Success {
			if res != nil && (res.Status == 400 || res.Status == 401 || res.Status == 403 || res.Status == 404 || res.Status == 422) {
				return nil, firewallRejectedCreate{}
			}
			return nil, errors.New("firewall create outcome unknown; inspect/retry")
		}
		data = res.Data
	} else {
		data, e = executeProviderToolOnConnection(d.ctx, d.inst.ProviderConnectionID, d.inst.Provider, tool, args)
	}
	if e != nil {
		return nil, e
	}
	if d.inst.Provider == "hetzner" && (tool == "firewall_attach" || tool == "firewall_detach" || tool == "firewall_set_rules") {
		root, e := firewallMap(data)
		if e != nil {
			return nil, e
		}
		actions := firewallRows(root["actions"])
		if a, ok := root["action"].(map[string]any); ok {
			actions = append(actions, a)
		}
		for _, action := range actions {
			for n := 0; n < 10 && mapString(action, "status") != "success"; n++ {
				if mapString(action, "status") == "error" {
					return nil, errors.New("provider firewall action failed")
				}
				id := mapString(action, "id")
				if id == "" {
					return nil, errors.New("provider firewall action identity missing")
				}
				select {
				case <-d.ctx.Done():
					return nil, context.Canceled
				case <-time.After(time.Second):
				}
				body, e := executeProviderToolOnConnection(d.ctx, d.inst.ProviderConnectionID, d.inst.Provider, "firewall_action_get", map[string]any{"id": id})
				if e != nil {
					return nil, e
				}
				m, e := firewallMap(body)
				if e != nil {
					return nil, e
				}
				action, _ = m["action"].(map[string]any)
			}
			if mapString(action, "status") != "success" {
				return nil, errFirewallPending
			}
		}
	}
	return data, nil
}
func (d firewallDriver) paged(tool string, args map[string]any) (map[string]any, error) {
	args = cloneFields(args)
	switch d.inst.Provider {
	case "clouding":
		delete(args, "per_page")
		delete(args, "page_size")
	case "hetzner", "digitalocean":
		delete(args, "pageSize")
		delete(args, "page_size")
	case "vultr":
		delete(args, "page")
		delete(args, "pageSize")
		delete(args, "page_size")
	case "linode":
		delete(args, "pageSize")
		delete(args, "per_page")
	}
	bound, e := storageBinding(d.ctx, d.inst.Provider, d.inst.ProviderConnectionID)
	if e != nil {
		return nil, e
	}
	data, e := executePagedProviderTool(d.ctx, bound.ConnectionID, d.inst.Provider, tool, args)
	if e != nil {
		return nil, e
	}
	return firewallMap(data)
}
func firewallMap(data json.RawMessage) (map[string]any, error) {
	var m map[string]any
	e := json.Unmarshal(data, &m)
	if e == nil && m == nil {
		e = errors.New("invalid firewall response")
	}
	return m, e
}
func (d firewallDriver) get(id string) (map[string]any, error) {
	data, e := d.call("firewall_get", map[string]any{"id": id})
	if e != nil {
		return nil, e
	}
	m, e := firewallMap(data)
	if e != nil {
		return nil, e
	}
	for _, key := range []string{"firewall", "firewall_group"} {
		if v, ok := m[key].(map[string]any); ok {
			return v, nil
		}
	}
	return m, nil
}
func (d firewallDriver) list() ([]map[string]any, error) {
	m, e := d.paged("firewall_list", map[string]any{"page": 1, "pageSize": 200, "per_page": 200, "page_size": 100})
	if e != nil {
		return nil, e
	}
	key := map[string]string{"clouding": "values", "hetzner": "firewalls", "digitalocean": "firewalls", "vultr": "firewall_groups", "linode": "data"}[d.inst.Provider]
	if _, ok := m[key]; !ok {
		return nil, errors.New("provider firewall list is incomplete")
	}
	return firewallRows(m[key]), nil
}
func firewallName(m map[string]any) string {
	for _, k := range []string{"name", "label", "description"} {
		if v := mapString(m, k); v != "" {
			return v
		}
	}
	return ""
}
func (d firewallDriver) find(name string) (string, error) {
	groups, e := d.list()
	if e != nil {
		return "", e
	}
	found := ""
	for _, g := range groups {
		if firewallName(g) == name {
			if found != "" {
				return "", errors.New("multiple firewalls have the ownership name; manual reconciliation required")
			}
			found = mapString(g, "id")
			if found == "" {
				return "", errors.New("firewall identity missing")
			}
		}
	}
	return found, nil
}
func (d firewallDriver) create(name string, rules []FirewallRule) (string, error) {
	args := map[string]any{}
	switch d.inst.Provider {
	case "clouding":
		args = map[string]any{"name": name, "description": name}
	case "hetzner":
		args = map[string]any{"name": name, "rules": d.wireRules(rules), "labels": map[string]string{"managed-by": "apteva-instances"}}
	case "digitalocean":
		args = map[string]any{"name": name, "inbound_rules": d.wireRules(rules), "outbound_rules": firewallDOOutbound(), "droplet_ids": []int{}, "tags": []string{}}
	case "vultr":
		args = map[string]any{"description": name}
	case "linode":
		args = map[string]any{"label": name, "rules": map[string]any{"inbound": d.wireRules(rules), "inbound_policy": "DROP", "outbound": []any{}, "outbound_policy": "ACCEPT"}, "tags": []string{"apteva-instances"}}
	}
	data, e := d.call("firewall_create", args)
	if e != nil {
		return "", e
	}
	m, e := firewallMap(data)
	if e != nil {
		return "", e
	}
	for _, key := range []string{"firewall", "firewall_group"} {
		if v, ok := m[key].(map[string]any); ok {
			m = v
		}
	}
	return mapString(m, "id"), nil
}
func firewallDOOutbound() []map[string]any {
	out := []map[string]any{}
	for _, proto := range []string{"tcp", "udp", "icmp"} {
		r := map[string]any{"protocol": proto, "destinations": map[string]any{"addresses": []string{"0.0.0.0/0", "::/0"}}}
		if proto != "icmp" {
			r["ports"] = "1-65535"
		}
		out = append(out, r)
	}
	return out
}
func (d firewallDriver) wireRules(rules []FirewallRule) []map[string]any {
	out := []map[string]any{}
	for i, r := range rules {
		ports := strconv.Itoa(r.PortStart)
		if r.PortEnd != r.PortStart {
			ports += "-" + strconv.Itoa(r.PortEnd)
		}
		v := map[string]any{}
		switch d.inst.Provider {
		case "hetzner":
			v = map[string]any{"direction": "in", "protocol": r.Protocol, "source_ips": r.SourceCIDRs}
			if r.Protocol != "icmp" {
				v["port"] = ports
			}
		case "digitalocean":
			v = map[string]any{"protocol": r.Protocol, "sources": map[string]any{"addresses": r.SourceCIDRs}}
			if r.Protocol != "icmp" {
				v["ports"] = ports
			}
		case "linode":
			family := "ipv4"
			if strings.Contains(r.SourceCIDRs[0], ":") {
				family = "ipv6"
			}
			v = map[string]any{"label": fmt.Sprintf("rule-%d", i), "action": "ACCEPT", "protocol": strings.ToUpper(r.Protocol), "addresses": map[string]any{family: r.SourceCIDRs}}
			if r.Protocol != "icmp" {
				v["ports"] = ports
			}
		}
		out = append(out, v)
	}
	return out
}
func (d firewallDriver) serverGroup() (string, error) {
	tool := "firewall_server_get"
	if isVultrMetal(d.inst) {
		tool = "firewall_metal_get"
	}
	data, e := d.call(tool, map[string]any{"server_id": d.inst.ProviderID})
	if e != nil {
		return "", e
	}
	m, e := firewallMap(data)
	if e != nil {
		return "", e
	}
	for _, k := range []string{"instance", "bare_metal"} {
		if v, ok := m[k].(map[string]any); ok {
			m = v
		}
	}
	if _, ok := m["firewall_group_id"]; !ok {
		return "", errors.New("server firewall identity missing")
	}
	return mapString(m, "firewall_group_id"), nil
}
func (d firewallDriver) attached() ([]string, error) {
	if d.inst.ProviderID == "" {
		return []string{}, nil
	}
	if d.inst.Provider == "vultr" {
		id, e := d.serverGroup()
		if id == "" {
			return []string{}, e
		}
		return []string{id}, e
	}
	if d.inst.Provider == "linode" {
		data, e := d.call("firewall_server_get", map[string]any{"server_id": d.inst.ProviderID})
		if e != nil {
			return nil, e
		}
		m, e := firewallMap(data)
		if e != nil {
			return nil, e
		}
		if gen := mapString(m, "interface_generation"); gen != "" && gen != "legacy_config" {
			return nil, errors.New("Linode interface-based firewalls require a separate adapter; only legacy_config is supported")
		}
		m, e = d.paged("firewall_server_list", map[string]any{"server_id": d.inst.ProviderID, "page": 1, "page_size": 100})
		if e != nil {
			return nil, e
		}
		if _, ok := m["data"]; !ok {
			return nil, errors.New("incomplete Linode firewall attachments")
		}
		out := []string{}
		for _, g := range firewallRows(m["data"]) {
			if mapString(g, "status") == "enabled" {
				out = append(out, mapString(g, "id"))
			}
		}
		return out, nil
	}
	groups, e := d.list()
	if e != nil {
		return nil, e
	}
	out := []string{}
	for _, g := range groups {
		if d.hasServer(g, d.inst.ProviderID) {
			out = append(out, mapString(g, "id"))
		}
	}
	return out, nil
}
func (d firewallDriver) hasServer(g map[string]any, id string) bool {
	switch d.inst.Provider {
	case "clouding":
		for _, a := range firewallRows(g["attachments"]) {
			if mapString(a, "serverId") == id && len(firewallRows(a["publicPorts"])) > 0 {
				return true
			}
		}
	case "digitalocean":
		for _, v := range jsonSlice(g["droplet_ids"]) {
			if fmt.Sprint(v) == id {
				return true
			}
		}
	case "hetzner":
		for _, a := range firewallRows(g["applied_to"]) {
			if server, ok := a["server"].(map[string]any); ok && mapString(server, "id") == id {
				return true
			}
		}
	}
	return false
}
func jsonSlice(v any) []any { out, _ := v.([]any); return out }
func (d firewallDriver) ensureOwned(s firewallState) error {
	g, e := d.get(s.ResourceID)
	if e != nil {
		return e
	}
	if mapString(g, "id") != s.ResourceID || firewallName(g) != s.Name {
		return errors.New("refusing to mutate firewall with different ownership identity")
	}
	switch d.inst.Provider {
	case "clouding":
		for _, a := range firewallRows(g["attachments"]) {
			if mapString(a, "serverId") != d.inst.ProviderID || len(firewallRows(a["vpcPorts"])) > 0 {
				return errors.New("owned firewall is attached to another host/network; refusing mutation")
			}
		}
	case "digitalocean":
		if len(jsonSlice(g["tags"])) > 0 {
			return errors.New("owned firewall has shared tags")
		}
		for _, id := range jsonSlice(g["droplet_ids"]) {
			if fmt.Sprint(id) != d.inst.ProviderID {
				return errors.New("owned firewall is shared with another host")
			}
		}
	case "hetzner":
		for _, a := range firewallRows(g["applied_to"]) {
			server, ok := a["server"].(map[string]any)
			if !ok || mapString(a, "type") != "server" || mapString(server, "id") != d.inst.ProviderID {
				return errors.New("owned firewall has a different target or label selector")
			}
		}
	case "linode":
		m, e := d.paged("firewall_devices", map[string]any{"id": s.ResourceID, "page": 1, "page_size": 100})
		if e != nil {
			return e
		}
		if _, ok := m["data"]; !ok {
			return errors.New("firewall devices missing")
		}
		for _, a := range firewallRows(m["data"]) {
			entity, ok := a["entity"].(map[string]any)
			if !ok || mapString(entity, "type") != "linode" || mapString(entity, "id") != d.inst.ProviderID {
				return errors.New("owned firewall has another device/interface")
			}
		}
	case "vultr":
		n := anyToInt(g["instance_count"]) + anyToInt(g["bare_metal_count"])
		if n > 1 {
			return errors.New("owned firewall is shared with another host")
		}
		if n == 1 {
			id, e := d.serverGroup()
			if e != nil || id != s.ResourceID {
				return errors.New("owned firewall is attached to a different host")
			}
		}
	}
	return nil
}
func (d firewallDriver) attach(id string) error {
	current, e := d.attached()
	if e != nil {
		return e
	}
	if containsFirewall(current, id) {
		return nil
	}
	args := map[string]any{"id": id, "server_id": d.inst.ProviderID}
	switch d.inst.Provider {
	case "hetzner":
		n, e := strconv.ParseInt(d.inst.ProviderID, 10, 64)
		if e != nil {
			return e
		}
		delete(args, "server_id")
		args["apply_to"] = []map[string]any{{"type": "server", "server": map[string]any{"id": n}}}
	case "digitalocean":
		n, e := strconv.ParseInt(d.inst.ProviderID, 10, 64)
		if e != nil {
			return e
		}
		delete(args, "server_id")
		args["droplet_ids"] = []int64{n}
	case "linode":
		n, e := strconv.ParseInt(d.inst.ProviderID, 10, 64)
		if e != nil {
			return e
		}
		delete(args, "id")
		args["firewall_ids"] = []int64{mustFirewallInt(id)}
		_ = n
	case "vultr":
		delete(args, "id")
		args["firewall_group_id"] = id
	}
	tool := "firewall_attach"
	if isVultrMetal(d.inst) {
		tool = "firewall_metal_attach"
	}
	_, e = d.call(tool, args)
	return e
}
func (d firewallDriver) detach(id string) error {
	if d.inst.Provider == "vultr" || d.inst.Provider == "linode" {
		return nil
	} // Atomic replacement of the server assignment.
	args := map[string]any{"id": id, "server_id": d.inst.ProviderID}
	switch d.inst.Provider {
	case "hetzner":
		n, e := strconv.ParseInt(d.inst.ProviderID, 10, 64)
		if e != nil {
			return e
		}
		delete(args, "server_id")
		args["remove_from"] = []map[string]any{{"type": "server", "server": map[string]any{"id": n}}}
	case "digitalocean":
		n, e := strconv.ParseInt(d.inst.ProviderID, 10, 64)
		if e != nil {
			return e
		}
		delete(args, "server_id")
		args["body"] = map[string]any{"droplet_ids": []int64{n}}
	}
	_, e := d.call("firewall_detach", args)
	return e
}
func (d firewallDriver) setRules(id string, rules []FirewallRule) error {
	if e := d.verifyRules(id, rules); e == nil || errors.Is(e, errFirewallPending) {
		return nil
	}
	args := map[string]any{"id": id}
	switch d.inst.Provider {
	case "hetzner":
		args["rules"] = d.wireRules(rules)
	case "digitalocean":
		g, e := d.get(id)
		if e != nil {
			return e
		}
		args["name"] = firewallName(g)
		args["inbound_rules"] = d.wireRules(rules)
		args["outbound_rules"] = firewallDOOutbound()
		args["droplet_ids"] = g["droplet_ids"]
		args["tags"] = []string{}
	case "linode":
		args["inbound"] = d.wireRules(rules)
		args["inbound_policy"] = "DROP"
		args["outbound"] = []any{}
		args["outbound_policy"] = "ACCEPT"
	default:
		return d.setIndividualRules(id, rules)
	}
	_, e := d.call("firewall_set_rules", args)
	return e
}

type firewallRemoteRule struct {
	ID      string
	Rule    FirewallRule
	Enabled bool
}

func (d firewallDriver) remoteRules(id string) ([]firewallRemoteRule, error) {
	g, e := d.get(id)
	if e != nil {
		return nil, e
	}
	var rows []map[string]any
	switch d.inst.Provider {
	case "digitalocean":
		if _, ok := g["inbound_rules"]; !ok {
			return nil, errors.New("firewall rules missing")
		}
		rows = firewallRows(g["inbound_rules"])
	case "linode":
		r, ok := g["rules"].(map[string]any)
		if !ok || mapString(r, "inbound_policy") != "DROP" || mapString(r, "outbound_policy") != "ACCEPT" {
			return nil, errors.New("unexpected Linode firewall defaults")
		}
		if len(jsonSlice(r["outbound"])) != 0 {
			return nil, errors.New("unexpected Linode outbound rules")
		}
		rows = firewallRows(r["inbound"])
	case "vultr":
		r, e := d.paged("firewall_rules", map[string]any{"id": id, "per_page": 500})
		if e != nil {
			return nil, e
		}
		if _, ok := r["firewall_rules"]; !ok {
			return nil, errors.New("firewall rules missing")
		}
		rows = firewallRows(r["firewall_rules"])
	default:
		if _, ok := g["rules"]; !ok {
			return nil, errors.New("firewall rules missing")
		}
		rows = firewallRows(g["rules"])
	}
	out := []firewallRemoteRule{}
	for _, row := range rows {
		r := FirewallRule{Protocol: strings.ToLower(mapString(row, "protocol"))}
		enabled := true
		ports := ""
		sources := []string{}
		switch d.inst.Provider {
		case "clouding":
			r.PortStart = anyToInt(row["portRangeMin"])
			r.PortEnd = anyToInt(row["portRangeMax"])
			sources = []string{mapString(row, "sourceIp")}
			enabled, _ = row["enabled"].(bool)
		case "hetzner":
			if mapString(row, "direction") != "in" {
				return nil, errors.New("unexpected outbound restriction")
			}
			ports = mapString(row, "port")
			for _, v := range jsonSlice(row["source_ips"]) {
				sources = append(sources, fmt.Sprint(v))
			}
		case "digitalocean":
			src, _ := row["sources"].(map[string]any)
			for k, v := range src {
				if k != "addresses" && len(jsonSlice(v)) > 0 {
					return nil, errors.New("unexpected firewall source selector")
				}
			}
			for _, v := range jsonSlice(src["addresses"]) {
				sources = append(sources, fmt.Sprint(v))
			}
			ports = mapString(row, "ports")
			if a := mapString(row, "action"); a != "" && a != "allow" {
				return nil, errors.New("unexpected firewall action")
			}
		case "linode":
			if mapString(row, "action") != "ACCEPT" {
				return nil, errors.New("unexpected firewall action")
			}
			if r.Protocol == "6" {
				r.Protocol = "tcp"
			}
			if r.Protocol == "17" {
				r.Protocol = "udp"
			}
			if r.Protocol == "1" || r.Protocol == "58" {
				r.Protocol = "icmp"
			}
			src, _ := row["addresses"].(map[string]any)
			for _, family := range []string{"ipv4", "ipv6"} {
				for _, v := range jsonSlice(src[family]) {
					sources = append(sources, fmt.Sprint(v))
				}
			}
			ports = mapString(row, "ports")
		case "vultr":
			if mapString(row, "source") != "" {
				return nil, errors.New("unexpected firewall source selector")
			}
			sources = []string{fmt.Sprintf("%s/%d", mapString(row, "subnet"), anyToInt(row["subnet_size"]))}
			ports = strings.ReplaceAll(mapString(row, "port"), ":", "-")
			if action := mapString(row, "action"); action != "accept" {
				return nil, errors.New("unexpected firewall action")
			}
		}
		if len(sources) == 0 {
			return nil, errors.New("firewall rule has unrecognized sources")
		}
		if ports != "" {
			parts := strings.Split(ports, "-")
			if len(parts) > 2 {
				return nil, errors.New("unrecognized port range")
			}
			r.PortStart, e = strconv.Atoi(parts[0])
			if e != nil {
				return nil, e
			}
			r.PortEnd = r.PortStart
			if len(parts) == 2 {
				r.PortEnd, e = strconv.Atoi(parts[1])
				if e != nil {
					return nil, e
				}
			}
		}
		for _, src := range sources {
			p, e := netip.ParsePrefix(src)
			if e != nil {
				if ip, e2 := netip.ParseAddr(src); e2 == nil {
					bits := 128
					if ip.Is4() {
						bits = 32
					}
					p = netip.PrefixFrom(ip, bits)
				} else {
					return nil, e
				}
			}
			v := r
			v.SourceCIDRs = []string{p.Masked().String()}
			out = append(out, firewallRemoteRule{ID: mapString(row, "id"), Rule: v, Enabled: enabled})
		}
	}
	return out, nil
}
func (d firewallDriver) verifyRules(id string, want []FirewallRule) error {
	remote, e := d.remoteRules(id)
	if e != nil {
		return e
	}
	got := []FirewallRule{}
	for _, r := range remote {
		if r.Enabled {
			got = append(got, r.Rule)
		}
	}
	got, e = normalizeFirewallRules(got, firewallCapabilities(d.inst))
	if e != nil {
		return e
	}
	if nativeJSON(got) != nativeJSON(want) {
		return errors.New("firewall rule contents differ")
	}
	if d.inst.Provider == "digitalocean" {
		g, e := d.get(id)
		if e != nil {
			return e
		}
		if len(jsonSlice(g["pending_changes"])) > 0 {
			return errFirewallPending
		}
		var actual any = g["outbound_rules"] // Compare the semantics rather than object key order.
		rows := firewallRows(actual)
		if len(rows) != 3 {
			return errors.New("unexpected outbound firewall policy")
		}
		seen := map[string]bool{}
		for _, r := range rows {
			p := mapString(r, "protocol")
			dest, _ := r["destinations"].(map[string]any)
			values := jsonSlice(dest["addresses"])
			if len(values) != 2 || !containsFirewall([]string{fmt.Sprint(values[0]), fmt.Sprint(values[1])}, "0.0.0.0/0") || !containsFirewall([]string{fmt.Sprint(values[0]), fmt.Sprint(values[1])}, "::/0") {
				return errors.New("outbound destinations differ")
			}
			if p != "icmp" && mapString(r, "ports") != "1-65535" && mapString(r, "ports") != "all" {
				return errors.New("outbound ports differ")
			}
			seen[p] = true
		}
		if !seen["tcp"] || !seen["udp"] || !seen["icmp"] {
			return errors.New("outbound protocols differ")
		}
	}
	return nil
}
func (d firewallDriver) setIndividualRules(id string, want []FirewallRule) error {
	remote, e := d.remoteRules(id)
	if e != nil {
		return e
	}
	// Add and verify desired rules before removing obsolete ones. Retries re-read
	// remote state, including recovery after an interrupted create/enable response.
	for _, r := range want {
		found := false
		for _, v := range remote {
			if nativeJSON(v.Rule) == nativeJSON(r) {
				found = true
				if !v.Enabled {
					if _, e = d.call("firewall_rule_enable", map[string]any{"rule_id": v.ID}); e != nil {
						return e
					}
				}
				break
			}
		}
		if found {
			continue
		}
		args := map[string]any{"id": id}
		if d.inst.Provider == "clouding" {
			args["description"] = "Apteva managed allow rule"
			args["protocol"] = r.Protocol
			args["sourceIp"] = r.SourceCIDRs[0]
			if r.Protocol != "icmp" {
				args["portRangeMin"] = r.PortStart
				args["portRangeMax"] = r.PortEnd
			}
		} else {
			p, _ := netip.ParsePrefix(r.SourceCIDRs[0])
			family := "v4"
			if p.Addr().Is6() {
				family = "v6"
			}
			args["ip_type"] = family
			args["protocol"] = r.Protocol
			args["subnet"] = p.Addr().String()
			args["subnet_size"] = p.Bits()
			if r.Protocol != "icmp" {
				ports := strconv.Itoa(r.PortStart)
				if r.PortEnd != r.PortStart {
					ports += ":" + strconv.Itoa(r.PortEnd)
				}
				args["port"] = ports
			}
			args["notes"] = "Apteva managed allow rule"
		}
		data, e := d.call("firewall_rule_create", args)
		if e != nil {
			return e
		}
		if d.inst.Provider == "clouding" {
			ruleID := jsonStringAt(data, "id")
			if ruleID == "" {
				return errors.New("new firewall rule identity missing")
			}
			if _, e = d.call("firewall_rule_enable", map[string]any{"rule_id": ruleID}); e != nil {
				return e
			}
		}
	}
	remote, e = d.remoteRules(id)
	if e != nil {
		return e
	}
	needed := map[string]bool{}
	for _, r := range want {
		needed[nativeJSON(r)] = true
	}
	for _, r := range want {
		found := false
		for _, v := range remote {
			if v.Enabled && nativeJSON(v.Rule) == nativeJSON(r) {
				found = true
				break
			}
		}
		if !found {
			return errors.New("new rule not yet active; refusing to remove previous rules")
		}
	}
	kept := map[string]bool{}
	for _, v := range remote {
		k := nativeJSON(v.Rule)
		if needed[k] && v.Enabled && !kept[k] {
			kept[k] = true
			continue
		}
		if v.ID == "" {
			return errors.New("obsolete rule identity missing")
		}
		if _, e = d.call("firewall_rule_delete", map[string]any{"id": id, "rule_id": v.ID}); e != nil {
			return e
		}
	}
	return nil
}
func (d firewallDriver) remove(s firewallState) error {
	// A provider 404 is the only successful absent-resource result.
	bound, e := storageBinding(d.ctx, d.inst.Provider, d.inst.ProviderConnectionID)
	if e != nil {
		return e
	}
	res, e := d.ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "firewall_get", map[string]any{"id": s.ResourceID})
	if e != nil {
		return e
	}
	if res != nil && res.Status == 404 {
		return nil
	}
	if res == nil || !res.Success {
		return errors.New("cannot verify firewall before cleanup")
	}
	if e = d.ensureOwned(s); e != nil {
		return e
	}
	_, e = d.call("firewall_delete", map[string]any{"id": s.ResourceID})
	return e
}

func mustFirewallInt(id string) int64 { n, _ := strconv.ParseInt(id, 10, 64); return n }

// Firewall response arrays have security-relevant boundaries: nested metadata
// must never be interpreted as another rule, target, or firewall resource.
func firewallRows(v any) []map[string]any {
	rows := []map[string]any{}
	for _, item := range jsonSlice(v) {
		if m, ok := item.(map[string]any); ok {
			rows = append(rows, m)
		}
	}
	return rows
}

type firewallRejectedCreate struct{}

func (firewallRejectedCreate) Error() string {
	return "provider rejected firewall creation; check permissions and policy before retry"
}
