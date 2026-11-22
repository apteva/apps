package main

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestBytes []byte

var globalCtx *sdk.AppCtx

type App struct {
	scans *scanManager
}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestBytes)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("security-scanner requires a db block")
	}
	globalCtx = ctx
	apiKey := strings.TrimSpace(ctx.Config().Get("nvd_api_key"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("NVD_API_KEY"))
	}
	a.scans = newScanManager(apiKey)
	if err := markInterruptedScans(ctx.AppDB()); err != nil {
		return err
	}
	ctx.Logger().Info("security-scanner mounted", "project_id", ctx.CurrentProject())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.scans != nil {
		a.scans.mu.Lock()
		for _, cancel := range a.scans.running {
			cancel()
		}
		a.scans.mu.Unlock()
		a.scans.wg.Wait()
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) MCPTools() []sdk.Tool {
	id := map[string]any{"id": sInteger()}
	return []sdk.Tool{
		{Name: "security_overview", Description: "Open findings by severity, running and recent scans, allowlist size and rate limits. Args: none.",
			InputSchema: schemaObject(nil, nil), Handler: a.toolOverview},
		{Name: "security_targets_list", Description: "Read the scan allowlist, exclusions and rate limits. Only people can change them, in the Security panel. Args: none.",
			InputSchema: schemaObject(nil, nil), Handler: a.toolTargetsList},
		{Name: "security_scope_check", Description: "Dry run. Expand targets (IPs, CIDRs, hostnames) and show which addresses are allowed or dropped and why, plus the port plan for a profile. Sends no traffic to targets (DNS lookups only). Args: targets (array), profile?, tcp_ports?, udp_ports?.",
			InputSchema: schemaObject(map[string]any{"targets": sStringArray(), "profile": sString(), "tcp_ports": sString(), "udp_ports": sString()}, []string{"targets"}), Handler: a.toolScopeCheck},
		{Name: "security_scan_start", Description: "REAL SCAN: sends network probes to allowlisted targets. Detection only (no exploits, no credential guessing). Runs in the background; poll security_scan_status. Args: targets (array of IPs, CIDRs or hostnames on the allowlist), profile? (quick | standard (default) | full | custom), tcp_ports? and udp_ports? for custom (e.g. \"22,80,8000-8100\"), treat_all_up? (skip host discovery), timeout_ms? (100-5000, default 1000), retries? (0-2), rate_pps? and parallel_hosts? (cannot exceed the project limits), cve_lookup? (default true).",
			InputSchema: schemaObject(map[string]any{"targets": sStringArray(), "profile": sString(), "tcp_ports": sString(), "udp_ports": sString(), "treat_all_up": sBoolean(),
				"timeout_ms": sInteger(), "retries": sInteger(), "rate_pps": sInteger(), "parallel_hosts": sInteger(), "cve_lookup": sBoolean()}, []string{"targets"}),
			HandlerCtx: a.toolScanStart},
		{Name: "security_scan_status", Description: "Progress and counts for one scan, including dropped targets. Args: id.",
			InputSchema: schemaObject(id, []string{"id"}), Handler: a.toolScanStatus},
		{Name: "security_scan_cancel", Description: "Stop a running scan. Args: id.",
			InputSchema: schemaObject(id, []string{"id"}), Handler: a.toolScanCancel},
		{Name: "security_scans_list", Description: "List scans, newest first. Args: status? (queued|running|completed|failed|cancelled|all), limit?.",
			InputSchema: schemaObject(map[string]any{"status": sString(), "limit": sInteger()}, nil), Handler: a.toolScansList},
		{Name: "security_hosts_search", Description: "Hosts with open ports, services, products and versions. Args: q? (address, hostname, service, product or port), include_closed?, limit?.",
			InputSchema: schemaObject(map[string]any{"q": sString(), "include_closed": sBoolean(), "limit": sInteger()}, nil), Handler: a.toolHostsSearch},
		{Name: "security_findings_search", Description: "Search findings, most severe first. Args: severity? (array or comma list), status? (open and confirmed by default; or all, fixed, false_positive, risk_accepted), address?, port?, cve?, scan_id?, q?, limit?, offset?.",
			InputSchema: schemaObject(map[string]any{"severity": sStringArray(), "status": sStringArray(), "address": sString(), "port": sInteger(), "cve": sString(),
				"scan_id": sInteger(), "q": sString(), "limit": sInteger(), "offset": sInteger()}, nil), Handler: a.toolFindingsSearch},
		{Name: "security_findings_update", Description: "Triage a finding. Args: id, status? (open | confirmed | false_positive | risk_accepted), note?. Fixed is set automatically by rescans.",
			InputSchema: schemaObject(map[string]any{"id": sInteger(), "status": sString(), "note": sString()}, []string{"id"}), Handler: a.toolFindingsUpdate},
		{Name: "security_report_generate", Description: "Severity-grouped Markdown report with evidence and fixes. Args: scan_id? (omit for all open findings), include_info? (default false).",
			InputSchema: schemaObject(map[string]any{"scan_id": sInteger(), "include_info": sBoolean()}, nil), Handler: a.toolReport},
		{Name: "security_diff", Description: "Compare a scan with the previous completed scan of the same addresses: new and closed ports, version changes, new and fixed findings. Args: scan_id, against_scan_id?.",
			InputSchema: schemaObject(map[string]any{"scan_id": sInteger(), "against_scan_id": sInteger()}, []string{"scan_id"}), Handler: a.toolDiff},
	}
}

func (a *App) toolOverview(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	counts, err := severityCounts(db, pid)
	if err != nil {
		return nil, err
	}
	targets, err := listTargets(db, pid)
	if err != nil {
		return nil, err
	}
	allow, exclude := 0, 0
	for _, t := range targets {
		if t.Kind == "allow" {
			allow++
		} else {
			exclude++
		}
	}
	scans, err := listScans(db, pid, "all", 10)
	if err != nil {
		return nil, err
	}
	settings, err := getSettings(db, pid)
	if err != nil {
		return nil, err
	}
	var hosts, openServices int
	_ = db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE project_id=? AND up=1`, pid).Scan(&hosts)
	_ = db.QueryRow(`SELECT COUNT(*) FROM services WHERE project_id=? AND state='open'`, pid).Scan(&openServices)
	return map[string]any{
		"open_findings": counts, "recent_scans": scans, "allowlist_entries": allow, "exclusions": exclude,
		"hosts_up": hosts, "open_services": openServices, "settings": settings, "profiles": scanProfiles,
	}, nil
}

func (a *App) toolTargetsList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	targets, err := listTargets(ctx.AppDB(), ctx.CurrentProject())
	if err != nil {
		return nil, err
	}
	settings, err := getSettings(ctx.AppDB(), ctx.CurrentProject())
	allow, exclude := []Target{}, []Target{}
	for _, t := range targets {
		if t.Kind == "allow" {
			allow = append(allow, t)
		} else {
			exclude = append(exclude, t)
		}
	}
	return map[string]any{"allowlist": allow, "exclusions": exclude, "settings": settings,
		"note": "Only people can edit the allowlist, exclusions and limits in the Security panel."}, err
}

func (a *App) toolScopeCheck(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	opt := scanOptions{Profile: stringArg(args, "profile"), TCPSpec: stringArg(args, "tcp_ports"), UDPSpec: stringArg(args, "udp_ports")}
	s, res, plan, err := a.scans.prepare(context.Background(), ctx.AppDB(), ctx.CurrentProject(), stringSliceArg(args, "targets"), opt)
	out := map[string]any{"would_scan": err == nil}
	if res != nil {
		out["allowed"] = res.Allowed
		out["dropped"] = res.Dropped
	}
	if err != nil {
		out["error"] = err.Error()
		if res == nil {
			return nil, err
		}
		return out, nil
	}
	out["tcp_port_count"] = len(plan.TCP)
	out["udp_ports"] = nonNilInts(plan.UDP)
	out["options"] = s.Options
	return out, nil
}

func (a *App) toolScanStart(c context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	startedBy := "agent"
	if caller := sdk.CallerFrom(c); caller != nil {
		switch {
		case caller.AgentID > 0:
			startedBy = fmt.Sprintf("agent %d", caller.AgentID)
		case caller.AppName != "":
			startedBy = "app " + caller.AppName
		case caller.SubjectEmail != "":
			startedBy = caller.SubjectEmail
		}
	}
	return a.startScan(ctx, args, startedBy)
}

func (a *App) startScan(ctx *sdk.AppCtx, args map[string]any, startedBy string) (any, error) {
	opt := scanOptions{
		Profile: stringArg(args, "profile"), TCPSpec: stringArg(args, "tcp_ports"), UDPSpec: stringArg(args, "udp_ports"),
		TreatAllUp: boolArg(args, "treat_all_up", false), TimeoutMS: int(int64Arg(args, "timeout_ms")), Retries: int(int64Arg(args, "retries")),
		RatePPS: int(int64Arg(args, "rate_pps")), ParallelHosts: int(int64Arg(args, "parallel_hosts")),
		CVELookup: boolArg(args, "cve_lookup", true), StartedBy: startedBy,
	}
	s, err := a.scans.start(ctx, ctx.CurrentProject(), stringSliceArg(args, "targets"), opt)
	if err != nil {
		return nil, err
	}
	return map[string]any{"scan": s, "message": fmt.Sprintf("Scan #%d started on %d address(es); poll security_scan_status.", s.ID, len(s.Addresses))}, nil
}

func (a *App) toolScanStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := getScan(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"scan": s}, nil
}

func (a *App) toolScanCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	if _, err := getScan(ctx.AppDB(), ctx.CurrentProject(), id); err != nil {
		return nil, err
	}
	return map[string]any{"cancelled": a.scans.cancel(id), "id": id}, nil
}

func (a *App) toolScansList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	scans, err := listScans(ctx.AppDB(), ctx.CurrentProject(), stringArg(args, "status"), defaultInt(int(int64Arg(args, "limit")), 30))
	return map[string]any{"scans": scans, "count": len(scans)}, err
}

func (a *App) toolHostsSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	hosts, err := searchHosts(ctx.AppDB(), ctx.CurrentProject(), stringArg(args, "q"), boolArg(args, "include_closed", false), defaultInt(int(int64Arg(args, "limit")), 100))
	return map[string]any{"hosts": hosts, "count": len(hosts)}, err
}

func (a *App) toolFindingsSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	f := findingFilter{
		Severity: lowerAll(stringSliceArg(args, "severity")), Status: lowerAll(stringSliceArg(args, "status")),
		Address: stringArg(args, "address"), Port: int(int64Arg(args, "port")), CVE: stringArg(args, "cve"),
		ScanID: int64Arg(args, "scan_id"), Query: stringArg(args, "q"),
		Limit: int(int64Arg(args, "limit")), Offset: int(int64Arg(args, "offset")),
	}
	findings, total, err := searchFindings(ctx.AppDB(), ctx.CurrentProject(), f)
	return map[string]any{"findings": findings, "count": len(findings), "total": total}, err
}

func (a *App) toolFindingsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	var note *string
	if _, ok := args["note"]; ok {
		n := stringArg(args, "note")
		note = &n
	}
	f, err := updateFinding(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "id"), strings.ToLower(stringArg(args, "status")), note)
	return map[string]any{"finding": f}, err
}

func (a *App) toolReport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return buildReport(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "scan_id"), boolArg(args, "include_info", false))
}

func (a *App) toolDiff(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return diffScans(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "scan_id"), int64Arg(args, "against_scan_id"))
}

func lowerAll(in []string) []string {
	for i := range in {
		in[i] = strings.ToLower(in[i])
	}
	return in
}

func schemaObject(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	out := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func sString() map[string]any      { return map[string]any{"type": "string"} }
func sInteger() map[string]any     { return map[string]any{"type": "integer"} }
func sBoolean() map[string]any     { return map[string]any{"type": "boolean"} }
func sStringArray() map[string]any { return map[string]any{"type": "array", "items": sString()} }

var _ = sql.ErrNoRows
