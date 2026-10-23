package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
)

type firewallFixture struct {
	createEnabled, enableRace, loseEnable, rejectEnable bool
	enableCalls                                         int
	provider, name, id, server                          string
	rules                                               []map[string]any
	attached                                            []string
	calls                                               []string
	creates, mutations                                  int
	loseCreate, hideCreated                             bool
}

func (f *firewallFixture) group() map[string]any {
	g := map[string]any{"id": f.id, "name": f.name, "label": f.name, "description": f.name, "rules": f.rules}
	switch f.provider {
	case "clouding":
		a := []any{}
		if containsFirewall(f.attached, f.id) {
			a = append(a, map[string]any{"serverId": f.server, "publicPorts": []any{map[string]any{"id": "public"}}, "vpcPorts": []any{}})
		}
		g["attachments"] = a
	case "hetzner":
		a := []any{}
		if containsFirewall(f.attached, f.id) {
			a = append(a, map[string]any{"type": "server", "server": map[string]any{"id": mustFirewallInt(f.server)}})
		}
		g["applied_to"] = a
	case "digitalocean":
		ids := []int64{}
		if containsFirewall(f.attached, f.id) {
			ids = append(ids, mustFirewallInt(f.server))
		}
		g["droplet_ids"] = ids
		g["tags"] = []string{}
		g["inbound_rules"] = f.rules
		g["outbound_rules"] = firewallDOOutbound()
		g["pending_changes"] = []any{}
	case "vultr":
		n := 0
		if containsFirewall(f.attached, f.id) {
			n = 1
		}
		g["instance_count"] = n
		g["bare_metal_count"] = 0
	case "linode":
		g["rules"] = map[string]any{"inbound": f.rules, "outbound": []any{}, "inbound_policy": "DROP", "outbound_policy": "ACCEPT"}
		g["status"] = "enabled"
	}
	return g
}
func firewallTestMaps(v any) []map[string]any {
	var out []map[string]any
	_ = json.Unmarshal([]byte(nativeJSON(v)), &out)
	return out
}
func (f *firewallFixture) hook(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	f.calls = append(f.calls, tool)
	answer := func(v any) (*sdk.ExecuteResult, error) { return providerJSON(nativeJSON(v)), nil }
	switch tool {
	case "firewall_list":
		rows := []any{}
		if f.id != "" && !f.hideCreated {
			rows = append(rows, f.group())
		}
		if containsFirewall(f.attached, "foreign") {
			rows = append(rows, map[string]any{"id": "foreign", "name": "shared-default", "attachments": []any{map[string]any{"serverId": f.server, "publicPorts": []any{map[string]any{"id": "public"}}}}, "droplet_ids": []int64{mustFirewallInt(f.server)}, "applied_to": []any{map[string]any{"type": "server", "server": map[string]any{"id": mustFirewallInt(f.server)}}}})
		}
		key := map[string]string{"clouding": "values", "hetzner": "firewalls", "digitalocean": "firewalls", "vultr": "firewall_groups", "linode": "data"}[f.provider]
		return answer(map[string]any{key: rows})
	case "firewall_get":
		if f.id == "" {
			return missingProviderResource(), nil
		}
		switch f.provider {
		case "digitalocean", "hetzner":
			return answer(map[string]any{"firewall": f.group()})
		case "vultr":
			return answer(map[string]any{"firewall_group": f.group()})
		default:
			return answer(f.group())
		}
	case "firewall_server_get", "firewall_metal_get":
		if f.provider == "linode" {
			return answer(map[string]any{"interface_generation": "legacy_config"})
		}
		id := ""
		if len(f.attached) > 0 {
			id = f.attached[0]
		}
		return answer(map[string]any{"instance": map[string]any{"firewall_group_id": id}})
	case "firewall_server_list":
		rows := []any{}
		if containsFirewall(f.attached, f.id) && f.id != "" {
			rows = append(rows, f.group())
		}
		if containsFirewall(f.attached, "foreign") {
			rows = append(rows, map[string]any{"id": "foreign", "status": "enabled"})
		}
		return answer(map[string]any{"data": rows})
	case "firewall_devices":
		rows := []any{}
		if containsFirewall(f.attached, f.id) {
			rows = append(rows, map[string]any{"id": 1, "entity": map[string]any{"id": mustFirewallInt(f.server), "type": "linode"}})
		}
		return answer(map[string]any{"data": rows})
	case "firewall_rules":
		return answer(map[string]any{"firewall_rules": f.rules})
	}
	f.mutations++
	switch tool {
	case "firewall_create":
		f.creates++
		f.id = "123"
		f.name = firstNonEmpty(strArg(args, "name"), strArg(args, "label"), strArg(args, "description"))
		f.rules = []map[string]any{}
		switch f.provider {
		case "hetzner":
			f.rules = firewallTestMaps(args["rules"])
		case "digitalocean":
			f.rules = firewallTestMaps(args["inbound_rules"])
		case "linode":
			f.rules = firewallTestMaps(args["rules"].(map[string]any)["inbound"])
		}
		if f.loseCreate {
			f.loseCreate = false
			return nil, errors.New("lost create response")
		}
		switch f.provider {
		case "digitalocean", "hetzner":
			return answer(map[string]any{"firewall": f.group()})
		case "vultr":
			return answer(map[string]any{"firewall_group": f.group()})
		default:
			return answer(f.group())
		}
	case "firewall_set_rules":
		switch f.provider {
		case "hetzner":
			f.rules = firewallTestMaps(args["rules"])
		case "digitalocean":
			f.rules = firewallTestMaps(args["inbound_rules"])
		case "linode":
			f.rules = firewallTestMaps(args["inbound"])
		}
	case "firewall_rule_create":
		r := cloneFields(args)
		delete(r, "id")
		r["id"] = fmt.Sprint(len(f.rules) + 1)
		if f.provider == "clouding" {
			r["enabled"] = f.createEnabled
		} else {
			r["action"] = "accept"
		}
		f.rules = append(f.rules, r)
		return answer(map[string]any{"id": r["id"]})
	case "firewall_rule_enable":
		f.enableCalls++
		if f.rejectEnable {
			return &sdk.ExecuteResult{Success: false, Status: 403}, nil
		}
		for _, r := range f.rules {
			if fmt.Sprint(r["id"]) == strArg(args, "rule_id") {
				if r["enabled"] == true {
					return &sdk.ExecuteResult{Success: false, Status: 400, Data: json.RawMessage(`{"errors":{"rule":["Security group rule is already enabled"]}}`)}, nil
				}
				r["enabled"] = true
				if f.enableRace {
					f.enableRace = false
					return &sdk.ExecuteResult{Success: false, Status: 400}, nil
				}
				if f.loseEnable {
					f.loseEnable = false
					return nil, errors.New("enable response lost")
				}
			}
		}
		for _, r := range f.rules {
			if fmt.Sprint(r["id"]) == strArg(args, "rule_id") {
				r["enabled"] = true
			}
		}
	case "firewall_rule_delete":
		out := []map[string]any{}
		for _, r := range f.rules {
			if fmt.Sprint(r["id"]) != strArg(args, "rule_id") {
				out = append(out, r)
			}
		}
		f.rules = out
	case "firewall_attach", "firewall_metal_attach":
		if f.provider == "vultr" || f.provider == "linode" {
			f.attached = []string{f.id}
		} else {
			f.attached = append(f.attached, f.id)
		}
		if f.provider == "digitalocean" {
			if _, ok := args["droplet_ids"]; !ok {
				return nil, errors.New("missing DigitalOcean droplet_ids")
			}
			if _, ok := args["server_id"]; ok {
				return nil, errors.New("unexpected server_id in JSON body")
			}
		}
	case "firewall_detach":
		if f.provider == "digitalocean" {
			if _, ok := args["body"]; !ok {
				return nil, errors.New("DELETE requires a JSON root body")
			}
		}
		out := []string{}
		for _, id := range f.attached {
			if id != strArg(args, "id") {
				out = append(out, id)
			}
		}
		f.attached = out
	case "firewall_delete":
		f.id = ""
		f.rules = nil
	default:
		return nil, errors.New("unexpected firewall operation " + tool)
	}
	return answer(map[string]any{})
}
func newFirewallTest(t *testing.T, provider string) (*sdk.AppCtx, *Instance, *firewallFixture) {
	t.Helper()
	f := &firewallFixture{provider: provider, server: "42"}
	ctx, _ := cloudingUpcloudTestCtx(t, provider, f.hook)
	inst, e := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: "firewall-test", Provider: provider, ProviderID: f.server, ProviderConnectionID: 7, Status: "ready", SSHPort: 2222})
	if e != nil {
		t.Fatal(e)
	}
	return ctx, inst, f
}
func fwRule(proto string, start, end int) FirewallRule {
	return FirewallRule{Protocol: proto, PortStart: start, PortEnd: end, SourceCIDRs: []string{"0.0.0.0/0"}}
}
func TestFirewallProviderLifecycleAndSharedOwners(t *testing.T) {
	for _, provider := range []string{"clouding", "hetzner", "digitalocean", "vultr", "linode"} {
		t.Run(provider, func(t *testing.T) {
			ctx, inst, f := newFirewallTest(t, provider)
			p := FirewallPolicy{Rules: []FirewallRule{fwRule("udp", 3478, 3478), fwRule("udp", 49160, 49223)}}
			s, e := firewallSet(ctx, inst, "first", &p)
			if e != nil || s.Phase != "applied" {
				t.Fatalf("first apply: %+v %v", s, e)
			}
			if f.creates != 1 {
				t.Fatal("duplicate firewall")
			}
			before := f.mutations
			if _, e = firewallSet(ctx, inst, "first", &p); e != nil {
				t.Fatal(e)
			}
			if before != f.mutations {
				t.Fatal("idempotent retry mutated provider")
			}
			p2 := FirewallPolicy{Rules: []FirewallRule{fwRule("udp", 3478, 3478), fwRule("tcp", 8080, 8080)}}
			if _, e = firewallSet(ctx, inst, "second", &p2); e != nil {
				t.Fatal(e)
			}
			if _, e = firewallSet(ctx, inst, "first", nil); e != nil {
				t.Fatal(e)
			}
			s, _ = firewallLoad(ctx.AppDB(), inst.ID)
			rules, e := firewallDesired(ctx.AppDB(), inst, s)
			if e != nil {
				t.Fatal(e)
			}
			hasShared, hasSSH := false, false
			for _, r := range rules {
				if r.Protocol == "udp" && r.PortStart == 3478 {
					hasShared = true
				}
				if r.Protocol == "tcp" && r.PortStart == 2222 {
					hasSSH = true
				}
				if r.PortStart == 49160 {
					t.Fatal("released owner's range remains")
				}
			}
			if !hasShared || !hasSSH {
				t.Fatal("removed shared rule or custom SSH port")
			}
			before = f.mutations
			view, e := firewallView(ctx, inst, true)
			if e != nil || view["provider_verified"] != true {
				t.Fatalf("view=%v err=%v", view, e)
			}
			if f.mutations != before {
				t.Fatal("inspection mutated provider")
			}
			calls := len(f.calls)
			reconcilePendingFirewalls(ctx)
			if len(f.calls) != calls {
				t.Fatal("applied firewall polled on recovery")
			}
			if _, e = firewallSet(ctx, inst, "second", nil); e != nil {
				t.Fatal(e)
			}
			if f.id == "" {
				t.Fatal("baseline firewall removed")
			}
			f.attached = nil
			if e = firewallDestroy(ctx, inst); e != nil {
				t.Fatal(e)
			}
			if f.id != "" {
				t.Fatal("owned firewall leaked")
			}
		})
	}
}
func TestFirewallLostCreateResponseRecovery(t *testing.T) {
	ctx, inst, f := newFirewallTest(t, "clouding")
	f.loseCreate = true
	p := FirewallPolicy{Rules: []FirewallRule{fwRule("udp", 3478, 3478)}}
	if _, e := firewallSet(ctx, inst, "app", &p); e == nil {
		t.Fatal("lost create falsely confirmed")
	}
	if _, e := firewallSet(ctx, inst, "app", &p); e != nil {
		t.Fatal(e)
	}
	if f.creates != 1 {
		t.Fatal("replayed creation")
	}
}
func TestFirewallUnknownCreateDoesNotReplay(t *testing.T) {
	ctx, inst, f := newFirewallTest(t, "clouding")
	f.loseCreate = true
	f.hideCreated = true
	p := FirewallPolicy{Rules: []FirewallRule{fwRule("tcp", 8080, 8080)}}
	_, _ = firewallSet(ctx, inst, "app", &p)
	if _, e := firewallSet(ctx, inst, "app", &p); e == nil || !strings.Contains(e.Error(), "unknown") {
		t.Fatal(e)
	}
	if f.creates != 1 {
		t.Fatal("unknown create replayed")
	}
}
func TestFirewallExplicitTakeoverKeepsSharedFirewall(t *testing.T) {
	for _, provider := range []string{"clouding", "hetzner", "digitalocean", "vultr", "linode"} {
		t.Run(provider, func(t *testing.T) {
			ctx, inst, f := newFirewallTest(t, provider)
			f.attached = []string{"foreign"}
			p := FirewallPolicy{Rules: []FirewallRule{fwRule("udp", 3478, 3478)}}
			if _, e := firewallSet(ctx, inst, "app", &p); e == nil {
				t.Fatal("implicit takeover")
			}
			if f.mutations != 0 {
				t.Fatal("provider changed before explicit takeover")
			}
			p.ReplaceExisting = true
			if _, e := firewallSet(ctx, inst, "app", &p); e != nil {
				t.Fatal(e)
			}
			if len(f.attached) != 1 || f.attached[0] != f.id {
				t.Fatal(f.attached)
			}
			for _, call := range f.calls {
				if call == "firewall_delete" {
					t.Fatal("shared firewall deleted")
				}
			}
		})
	}
}
func TestFirewallValidationAndBounds(t *testing.T) {
	c := firewallCapabilities(&Instance{ID: 1, Provider: "hetzner"})
	for _, r := range []FirewallRule{{Protocol: "all", SourceCIDRs: []string{"0.0.0.0/0"}}, fwRule("udp", 0, 65535), fwRule("tcp", 100, 90), {Protocol: "udp", PortStart: 3478, SourceCIDRs: []string{"invalid"}}} {
		if _, e := normalizeFirewallRules([]FirewallRule{r}, c); e == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	if _, e := normalizeFirewallRules([]FirewallRule{{Protocol: "tcp", PortStart: 80, SourceCIDRs: []string{"::/0"}}}, firewallCapabilities(&Instance{ID: 1, Provider: "clouding"})); e == nil {
		t.Fatal("unsupported IPv6")
	}
	for _, p := range []string{"ovhcloud", "aws-ec2", "upcloud", "external", "local", "scaleway"} {
		if firewallCapabilities(&Instance{ID: 1, Provider: p}).Supported {
			t.Fatal("advertised unimplemented adapter", p)
		}
	}
	ctx, inst, _ := newFirewallTest(t, "linode")
	rules := []FirewallRule{}
	for i := 0; i < 24; i++ {
		rules = append(rules, fwRule("tcp", 10000+i, 10000+i))
	}
	if e := firewallQueue(ctx.AppDB(), inst, "app", FirewallPolicy{Rules: rules}); e == nil {
		t.Fatal("accepted union beyond provider limit")
	}
	if e := firewallOwner("native:someone"); e == nil {
		t.Fatal("reserved ownership accepted")
	}
}
func TestNativeFirewallKeepsControlPrivate(t *testing.T) {
	inst := &Instance{ID: 1, Provider: "clouding"}
	o := nativeOptions{Firewall: &FirewallPolicy{Rules: []FirewallRule{fwRule("tcp", 50000, 60000)}}}
	if _, e := nativeFirewallPolicy(inst, o, nil, 55000); e == nil {
		t.Fatal("exposed private control port")
	}
	o.Firewall = &FirewallPolicy{ExposeDeclaredPorts: true}
	p, e := nativeFirewallPolicy(inst, o, []nativePort{{Protocol: "tcp", Start: 55000, End: 55000}, {Protocol: "udp", Start: 3478, End: 3478}}, 55000)
	if e != nil || len(p.Rules) != 1 || p.Rules[0].PortStart != 3478 {
		t.Fatalf("%+v %v", p, e)
	}
}

func TestNativeFirewallAutomaticLifecycle(t *testing.T) {
	b, f, req := newNativeTest(t)
	ctx, inst, provider := newFirewallTest(t, "clouding")
	a, e := b.artifact("fixture", true)
	if e != nil {
		t.Fatal(e)
	}
	b.db = ctx.AppDB()
	b.appCtx = ctx
	if e = b.registerArtifact(a); e != nil {
		t.Fatal(e)
	}
	if e = b.sealArtifact(a); e != nil {
		t.Fatal(e)
	}
	req.Target.ID = fmt.Sprint(inst.ID)
	req.Options["control_port"] = 55000
	req.Options["firewall"] = FirewallPolicy{Rules: []FirewallRule{fwRule("udp", 3478, 3478), fwRule("udp", 49160, 49223)}}
	req.Execution = prepareNativeTest(t, b, req)
	if provider.mutations != 0 {
		t.Fatal("prepare changed firewall")
	}
	if _, w := nativeCall(t, b, "start", startNativeRequest(req)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.starts != 1 {
		t.Fatal("service not started")
	}
	calls := len(provider.calls)
	if _, w := nativeCall(t, b, "inspect", req); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if len(provider.calls) != calls {
		t.Fatal("normal inspection calls provider firewall")
	}
	if _, w := nativeCall(t, b, "stop", req); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s, _ := firewallLoad(ctx.AppDB(), inst.ID)
	rules, e := firewallDesired(ctx.AppDB(), inst, s)
	if e != nil || len(rules) != 1 || rules[0].PortStart != 2222 {
		t.Fatalf("stop lost baseline or retained app rules %+v %v", rules, e)
	}
}
func TestFirewallTargetDriftAndUnexpectedAttachment(t *testing.T) {
	ctx, inst, f := newFirewallTest(t, "clouding")
	p := FirewallPolicy{Rules: []FirewallRule{fwRule("udp", 3478, 3478)}}
	if _, e := firewallSet(ctx, inst, "app", &p); e != nil {
		t.Fatal(e)
	}
	before := f.mutations
	f.attached = append(f.attached, "foreign")
	if _, e := firewallSet(ctx, inst, "app", &p); e == nil {
		t.Fatal("unexpected attachment silently removed")
	}
	if f.mutations != before {
		t.Fatal("changed unknown attachment")
	}
	_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"provider_id": "different"})
	if _, e := firewallSet(ctx, inst, "app", &p); e == nil {
		t.Fatal("changed provider identity accepted")
	}
}

func TestFirewallInvalidUpdatePreservesIntent(t *testing.T) {
	ctx, inst, f := newFirewallTest(t, "clouding")
	p := FirewallPolicy{Rules: []FirewallRule{fwRule("tcp", 8080, 8080)}}
	if _, e := firewallSet(ctx, inst, "app", &p); e != nil {
		t.Fatal(e)
	}
	before := f.mutations
	p.Rules = []FirewallRule{fwRule("tcp", 9000, 8000)}
	if _, e := firewallSet(ctx, inst, "app", &p); e == nil {
		t.Fatal("invalid update accepted")
	}
	var raw string
	if e := ctx.AppDB().QueryRow(`SELECT rules FROM instance_firewall_owners WHERE instance_id=? AND owner='app'`, inst.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(raw, "8080") || f.mutations != before {
		t.Fatal("invalid update altered previous intent")
	}
}
func TestFirewallBlocksOtherOwnersFromExposingControl(t *testing.T) {
	ctx, inst, f := newFirewallTest(t, "clouding")
	d := nativeRecord{ID: "control", ControlPort: 55000, Execution: nativeExecution{State: "prepared"}}
	if _, e := ctx.AppDB().Exec(`INSERT INTO native_deployments(id,install_id,instance_id,record) VALUES(?,?,?,?)`, d.ID, 52, inst.ID, nativeJSON(d)); e != nil {
		t.Fatal(e)
	}
	p := FirewallPolicy{Rules: []FirewallRule{fwRule("tcp", 54000, 56000)}}
	if _, e := firewallSet(ctx, inst, "other-app", &p); e == nil {
		t.Fatal("other owner exposed control")
	}
	if f.mutations != 0 {
		t.Fatal("provider changed before private-control validation")
	}
}
func TestCloudingPreparedFirewallUsedAtCreate(t *testing.T) {
	ctx, inst, f := newFirewallTest(t, "clouding")
	if e := dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"provider_id": ""}); e != nil {
		t.Fatal(e)
	}
	inst, _ = dbGetInstance(ctx.AppDB(), inst.ID)
	p := FirewallPolicy{Rules: []FirewallRule{fwRule("udp", 3478, 3478)}}
	if e := firewallQueue(ctx.AppDB(), inst, "instance", p); e != nil {
		t.Fatal(e)
	}
	if _, e := firewallReconcile(ctx, inst, true); e != nil {
		t.Fatal(e)
	}
	if len(f.attached) != 0 {
		t.Fatal("prepared firewall attached to unrelated host")
	}
	// Clouding's existing key-transfer operation is independent of the firewall.
	platform := ctx.PlatformAPI().(*cloudingUpcloudTestPlatform)
	original := platform.hook
	platform.hook = func(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "create_ssh_key" {
			return providerJSON(`{"id":"key"}`), nil
		}
		return original(id, tool, args)
	}
	tool, args, e := cloudingUpcloudCreateRequest(ctx, inst, CreateInstanceInput{Provider: "clouding", Firewall: &p, Name: "fresh", Storage: InstanceStorageRequest{Boot: &BootStorageRequest{SizeGB: 30}}}, "ssh-ed25519 fixture")
	if e != nil {
		t.Fatal(e)
	}
	if tool != "create_server" || args["publicPortFirewallIds"].([]string)[0] != f.id {
		t.Fatal("owned firewall not selected at creation")
	}
	for _, tool := range f.calls {
		if tool == "list_firewalls" {
			t.Fatal("shared default was required despite explicit policy")
		}
	}
}

func TestFirewallRejectedCreateCanRetryWithoutUnknownState(t *testing.T) {
	ctx, inst, f := newFirewallTest(t, "clouding")
	platform := ctx.PlatformAPI().(*cloudingUpcloudTestPlatform)
	hook := platform.hook
	reject := true
	platform.hook = func(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "firewall_create" && reject {
			reject = false
			return &sdk.ExecuteResult{Success: false, Status: 403}, nil
		}
		return hook(id, tool, args)
	}
	p := FirewallPolicy{Rules: []FirewallRule{fwRule("udp", 3478, 3478)}}
	if _, e := firewallSet(ctx, inst, "app", &p); e == nil {
		t.Fatal("rejected create accepted")
	}
	if _, e := firewallSet(ctx, inst, "app", &p); e != nil {
		t.Fatal(e)
	}
	if f.creates != 1 {
		t.Fatal("unexpected create count")
	}
}

func TestFirewallManyOwnersDeduplicateAndRemainBounded(t *testing.T) {
	ctx, inst, _ := newFirewallTest(t, "clouding")
	p := FirewallPolicy{Rules: []FirewallRule{fwRule("udp", 3478, 3478)}}
	for n := 0; n < 64; n++ {
		if e := firewallQueue(ctx.AppDB(), inst, fmt.Sprintf("owner-%d", n), p); e != nil {
			t.Fatal(n, e)
		}
	}
	s, _ := firewallLoad(ctx.AppDB(), inst.ID)
	rules, e := firewallDesired(ctx.AppDB(), inst, s)
	if e != nil || len(rules) != 2 {
		t.Fatal("shared requirements did not deduplicate", len(rules), e)
	}
	if e = firewallQueue(ctx.AppDB(), inst, "extra", p); e == nil {
		t.Fatal("unbounded owners")
	}
}

func TestNativeFirewallDoesNotReuseTakeoverApproval(t *testing.T) {
	b, _, req := newNativeTest(t)
	ctx, inst, provider := newFirewallTest(t, "clouding")
	a, e := b.artifact("fixture", true)
	if e != nil {
		t.Fatal(e)
	}
	b.db = ctx.AppDB()
	b.appCtx = ctx
	if e = b.registerArtifact(a); e != nil {
		t.Fatal(e)
	}
	if e = b.sealArtifact(a); e != nil {
		t.Fatal(e)
	}
	req.Target.ID = fmt.Sprint(inst.ID)
	req.Options["firewall"] = FirewallPolicy{ReplaceExisting: true, Rules: []FirewallRule{fwRule("udp", 3478, 3478)}}
	req.Execution = prepareNativeTest(t, b, req)
	start := startNativeRequest(req)
	if _, w := nativeCall(t, b, "start", start); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	provider.attached = append(provider.attached, "foreign")
	before := provider.mutations
	if _, w := nativeCall(t, b, "start", start); w.Code != 503 {
		t.Fatal("native retry reused takeover approval", w.Code)
	}
	if provider.mutations != before {
		t.Fatal("changed new unrelated attachment")
	}
}

func TestCloudingRulesCreatedEnabled(t *testing.T) {
	ctx, inst, f := newFirewallTest(t, "clouding")
	f.createEnabled = true
	p := FirewallPolicy{Rules: []FirewallRule{fwRule("udp", 3478, 3478)}}
	s, e := firewallSet(ctx, inst, "app", &p)
	if e != nil || s.Phase != "applied" {
		t.Fatal(s, e)
	}
	if f.enableCalls != 0 {
		t.Fatal("already enabled rule redundantly enabled")
	}
	before := f.mutations
	if _, e = firewallSet(ctx, inst, "app", &p); e != nil {
		t.Fatal(e)
	}
	if f.mutations != before {
		t.Fatal("retry changed active rules")
	}
}
func TestCloudingEnableAcknowledgementRecovery(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(fmt.Sprint(race), func(t *testing.T) {
			ctx, inst, f := newFirewallTest(t, "clouding")
			f.enableRace = race
			f.loseEnable = !race
			p := FirewallPolicy{Rules: []FirewallRule{fwRule("udp", 3478, 3478)}}
			s, e := firewallSet(ctx, inst, "app", &p)
			if e != nil || s.Phase != "applied" {
				t.Fatal(s, e)
			}
		})
	}
}
func TestCloudingRejectedEnableRemainsUnconfirmed(t *testing.T) {
	ctx, inst, f := newFirewallTest(t, "clouding")
	f.rejectEnable = true
	p := FirewallPolicy{Rules: []FirewallRule{fwRule("udp", 3478, 3478)}}
	if _, e := firewallSet(ctx, inst, "app", &p); e == nil {
		t.Fatal("rejected enable falsely confirmed")
	}
	s, _ := firewallLoad(ctx.AppDB(), inst.ID)
	if s.Phase == "applied" || len(f.attached) > 0 {
		t.Fatal("inactive policy attached")
	}
	f.rejectEnable = false
	if _, e := firewallSet(ctx, inst, "app", &p); e != nil {
		t.Fatal(e)
	}
}
