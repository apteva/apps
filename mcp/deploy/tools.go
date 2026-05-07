package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// MCPTools — agent-facing surface. Each tool's HTTP twin lives in
// handlers.go and shares the underlying logic where possible.
func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "deploy_init", Handler: a.toolInit,
			Description: "Bind a source to a new deployment. Args: name (slug), source_kind (code|local), source_ref (slug or path), framework? (go|node|bun|static|blank|''), build_cmd?, start_cmd?, port_hint?, env_json?, domain?, description?",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"source_kind": map[string]any{"type": "string", "enum": []string{"code", "local"}},
					"source_ref":  map[string]any{"type": "string"},
					"framework":   map[string]any{"type": "string"},
					"build_cmd":   map[string]any{"type": "string"},
					"start_cmd":   map[string]any{"type": "string"},
					"port_hint":   map[string]any{"type": "integer"},
					"env_json":    map[string]any{"type": "string"},
					"domain":      map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
				},
				"required": []string{"name", "source_kind", "source_ref"},
			},
		},
		{
			Name: "deploy_list", Handler: a.toolList,
			Description: "List deployments in this project. Args: include_archived?",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"include_archived": map[string]any{"type": "boolean"},
				},
			},
		},
		{
			Name: "deploy_get", Handler: a.toolGet,
			Description: "Full detail for one deployment. Args: name OR id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
					"id":   map[string]any{"type": "integer"},
				},
			},
		},
		{
			Name: "deploy_build", Handler: a.toolBuild,
			Description: "Fetch source, run the framework build, return build_id. Args: name OR id, release? (auto-release on success).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":    map[string]any{"type": "string"},
					"id":      map[string]any{"type": "integer"},
					"release": map[string]any{"type": "boolean"},
				},
			},
		},
		{
			Name: "deploy_release", Handler: a.toolRelease,
			Description: "Promote a build_id to live. Args: build_id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"build_id": map[string]any{"type": "integer"},
				},
				"required": []string{"build_id"},
			},
		},
		{
			Name: "deploy_status", Handler: a.toolStatus,
			Description: "Current build + release status, URL, last 10 builds. Args: name OR id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
					"id":   map[string]any{"type": "integer"},
				},
			},
		},
		{
			Name: "deploy_logs", Handler: a.toolLogs,
			Description: "Tail build or runtime logs. Args: build_id OR release_id, tail? (lines, default 200).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"build_id":   map[string]any{"type": "integer"},
					"release_id": map[string]any{"type": "integer"},
					"tail":       map[string]any{"type": "integer"},
				},
			},
		},
		{
			Name: "deploy_stop", Handler: a.toolStop,
			Description: "Stop the live release. Args: name OR id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
					"id":   map[string]any{"type": "integer"},
				},
			},
		},
		{
			Name: "deploy_destroy", Handler: a.toolDestroy,
			Description: "Stop, drop deployment, delete builds and artifacts. Args: name OR id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
					"id":   map[string]any{"type": "integer"},
				},
			},
		},
		{
			Name: "deploy_attach_domain", Handler: a.toolAttachDomain,
			Description: "Attach an FQDN to a deployment via the Domains app. Validates the FQDN sits under a registered domain, then upserts a DNS record (CNAME by default) pointing at the deploy's public_host. Args: name OR id, fqdn, target?, type? (CNAME|A, default CNAME), ttl?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":   map[string]any{"type": "string"},
					"id":     map[string]any{"type": "integer"},
					"fqdn":   map[string]any{"type": "string"},
					"target": map[string]any{"type": "string"},
					"type":   map[string]any{"type": "string", "enum": []string{"CNAME", "A"}},
					"ttl":    map[string]any{"type": "integer"},
				},
				"required": []string{"fqdn"},
			},
		},
		{
			Name: "deploy_detach_domain", Handler: a.toolDetachDomain,
			Description: "Clear a deployment's domain link. Best-effort deletes the DNS record via the Domains app and clears the deployment's domain field. Args: name OR id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
					"id":   map[string]any{"type": "integer"},
				},
			},
		},
		{
			Name: "deploy_list_routes", Handler: a.toolListRoutes,
			Description: "List live deployments as a route table for the host-based proxy. Returns [{slug, port, domain, status}]; only deployments with a current_release in 'live' or 'starting' status are returned. Used by the server, not by agents.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

// ─── tool handlers ────────────────────────────────────────────────

func (a *App) toolInit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	domainArg := strings.TrimSpace(strArg(args, "domain"))
	// When the Domains app is installed, route the inline `domain` arg
	// through the attach flow (validates ownership, writes DNS). When
	// it isn't, fall through to the historical free-text behavior so
	// installs without Domains still work.
	domainsOn := domainArg != "" && a.domainsAvailable(ctx)
	in := CreateDeploymentInput{
		Name:        strArg(args, "name"),
		Description: strArg(args, "description"),
		SourceKind:  strArg(args, "source_kind"),
		SourceRef:   strArg(args, "source_ref"),
		Framework:   strArg(args, "framework"),
		BuildCmd:    strArg(args, "build_cmd"),
		StartCmd:    strArg(args, "start_cmd"),
		PortHint:    intArg(args, "port_hint"),
		EnvJSON:     strArg(args, "env_json"),
	}
	if !domainsOn {
		in.Domain = domainArg
	}
	if err := validateName(in.Name); err != nil {
		return nil, err
	}
	d, err := dbCreateDeployment(ctx.AppDB(), pid, in)
	if err != nil {
		return nil, err
	}
	emit("deploy.created", map[string]any{
		"deployment_id": d.ID, "name": d.Name, "source_kind": d.SourceKind,
	})
	if domainsOn {
		if err := a.attachDomain(ctx, d, attachDomainSpec{FQDN: domainArg}); err != nil {
			// Don't roll back the deployment — the user can fix the
			// domain wiring (or detach) without losing the binding.
			return map[string]any{"deployment": d, "domain_error": err.Error()}, nil
		}
		d, _ = dbGetDeployment(ctx.AppDB(), pid, d.ID)
	}
	return map[string]any{"deployment": d}, nil
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	include := boolArg(args, "include_archived")
	rows, err := dbListDeployments(ctx.AppDB(), pid, include)
	if err != nil {
		return nil, err
	}
	return map[string]any{"deployments": rows, "count": len(rows)}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	builds, _ := dbListBuilds(ctx.AppDB(), d.ID, 10)
	releases, _ := dbListReleases(ctx.AppDB(), d.ID, 10)
	var current *Release
	if d.CurrentReleaseID != nil {
		current, _ = dbGetRelease(ctx.AppDB(), *d.CurrentReleaseID)
	}
	return map[string]any{
		"deployment":      d,
		"builds":          builds,
		"releases":        releases,
		"current_release": current,
		"url":             a.deploymentURL(d, current),
	}, nil
}

func (a *App) toolBuild(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	build, err := a.runBuild(d)
	if err != nil {
		return nil, err
	}
	res := map[string]any{"build": build}
	if boolArg(args, "release") && build.Status == "succeeded" {
		rel, err := a.runRelease(d, build)
		if err != nil {
			res["release_error"] = err.Error()
		} else {
			res["release"] = rel
			res["url"] = a.deploymentURL(d, rel)
		}
	}
	return res, nil
}

func (a *App) toolRelease(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	bid := int64(intArg(args, "build_id"))
	if bid == 0 {
		return nil, errors.New("build_id required")
	}
	build, err := dbGetBuild(ctx.AppDB(), bid)
	if err != nil || build == nil {
		return nil, fmt.Errorf("build %d not found", bid)
	}
	d, err := dbGetDeployment(ctx.AppDB(), pid, build.DeploymentID)
	if err != nil || d == nil {
		return nil, errors.New("deployment not found for that build")
	}
	rel, err := a.runRelease(d, build)
	if err != nil {
		return nil, err
	}
	return map[string]any{"release": rel, "url": a.deploymentURL(d, rel)}, nil
}

func (a *App) toolStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	builds, _ := dbListBuilds(ctx.AppDB(), d.ID, 10)
	var current *Release
	if d.CurrentReleaseID != nil {
		current, _ = dbGetRelease(ctx.AppDB(), *d.CurrentReleaseID)
	}
	return map[string]any{
		"deployment":      d,
		"builds":          builds,
		"current_release": current,
		"url":             a.deploymentURL(d, current),
	}, nil
}

func (a *App) toolLogs(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tail := intArg(args, "tail")
	if tail == 0 {
		tail = 200
	}
	if bid := int64(intArg(args, "build_id")); bid != 0 {
		b, err := dbGetBuild(ctx.AppDB(), bid)
		if err != nil || b == nil {
			return nil, fmt.Errorf("build %d not found", bid)
		}
		body, err := tailFile(b.LogPath, tail)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "build", "build_id": bid, "log": body}, nil
	}
	if rid := int64(intArg(args, "release_id")); rid != 0 {
		r, err := dbGetRelease(ctx.AppDB(), rid)
		if err != nil || r == nil {
			return nil, fmt.Errorf("release %d not found", rid)
		}
		body, err := tailFile(r.LogPath, tail)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "release", "release_id": rid, "log": body}, nil
	}
	return nil, errors.New("build_id or release_id required")
}

func (a *App) toolStop(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	if d.CurrentReleaseID == nil {
		return map[string]any{"stopped": false, "reason": "no live release"}, nil
	}
	rid := *d.CurrentReleaseID
	if rr := a.registry.Get(rid); rr != nil {
		if err := a.runtime.Stop(rr); err != nil {
			return nil, err
		}
	}
	a.markStopped(rid)
	_ = dbSetCurrentRelease(ctx.AppDB(), d.ID, nil)
	return map[string]any{"stopped": true, "release_id": rid}, nil
}

func (a *App) toolDestroy(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	// Drop the DNS record before deleting the row so the deployment's
	// link metadata is still around for detach to work.
	if d.DomainRecordID != "" {
		_ = a.detachDomain(ctx, d)
	}
	// Stop any live release first.
	if d.CurrentReleaseID != nil {
		if rr := a.registry.Get(*d.CurrentReleaseID); rr != nil {
			_ = a.runtime.Stop(rr)
		}
		a.markStopped(*d.CurrentReleaseID)
	}
	// Delete the row (CASCADE wipes builds + releases + events + leases).
	if err := dbDeleteDeployment(ctx.AppDB(), pid, d.ID); err != nil {
		return nil, err
	}
	// Best-effort: nuke the artifact dirs. No fatal-on-error since
	// the DB row is already gone.
	builds, _ := dbListBuilds(ctx.AppDB(), d.ID, 1000)
	for _, b := range builds {
		if b.ArtifactPath != "" {
			_ = os.RemoveAll(b.ArtifactPath)
		}
	}
	emit("deploy.destroyed", map[string]any{"deployment_id": d.ID, "name": d.Name})
	return map[string]any{"destroyed": true, "id": d.ID}, nil
}

func (a *App) toolAttachDomain(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	spec := attachDomainSpec{
		FQDN:   strArg(args, "fqdn"),
		Target: strArg(args, "target"),
		Type:   strArg(args, "type"),
		TTL:    intArg(args, "ttl"),
	}
	if err := a.attachDomain(ctx, d, spec); err != nil {
		return nil, err
	}
	pid, _ := resolveProjectFromArgs(args)
	out, _ := dbGetDeployment(ctx.AppDB(), pid, d.ID)
	return map[string]any{"deployment": out}, nil
}

func (a *App) toolDetachDomain(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	res := map[string]any{"detached": true, "id": d.ID, "fqdn": d.Domain}
	if err := a.detachDomain(ctx, d); err != nil {
		// Domain row was cleared either way; surface the registrar
		// error so the user can clean it up manually if needed.
		res["registrar_error"] = err.Error()
	}
	return res, nil
}

type RouteEntry struct {
	Slug      string `json:"slug"`
	ProjectID string `json:"project_id,omitempty"`
	Port      int    `json:"port"`
	Domain    string `json:"domain,omitempty"`
	Status    string `json:"status"`
}

// toolListRoutes is the server's pull-side: a small, no-secrets shape
// it can refresh into its route table on a 5-second tick.
func (a *App) toolListRoutes(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	releases, err := dbListLiveReleases(ctx.AppDB())
	if err != nil {
		return nil, err
	}
	out := make([]RouteEntry, 0, len(releases))
	for _, r := range releases {
		// Cross-project lookup: fetch the deployment without scoping.
		// Cheap because we only have a handful of live releases.
		row := ctx.AppDB().QueryRow(
			`SELECT id, project_id, name, domain FROM deployments WHERE id = ?`,
			r.DeploymentID)
		var id int64
		var projectID, name, domain string
		if err := row.Scan(&id, &projectID, &name, &domain); err != nil {
			continue
		}
		out = append(out, RouteEntry{
			Slug: name, ProjectID: projectID, Port: r.Port,
			Domain: domain, Status: r.Status,
		})
	}
	return map[string]any{"routes": out, "count": len(out)}, nil
}

// ─── helpers ──────────────────────────────────────────────────────

func (a *App) lookupDeployment(args map[string]any) (*Deployment, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if id := int64(intArg(args, "id")); id != 0 {
		d, err := dbGetDeployment(globalCtx.AppDB(), pid, id)
		if err != nil || d == nil {
			return nil, fmt.Errorf("deployment %d not found", id)
		}
		return d, nil
	}
	if name := strArg(args, "name"); name != "" {
		d, err := dbGetDeploymentByName(globalCtx.AppDB(), pid, name)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, fmt.Errorf("deployment %q not found", name)
		}
		return d, nil
	}
	return nil, errors.New("name or id required")
}

func (a *App) deploymentURL(d *Deployment, current *Release) string {
	if d.Domain != "" {
		return "https://" + d.Domain + "/"
	}
	if current == nil || current.Status != "live" {
		return ""
	}
	return fmt.Sprintf("http://localhost:%d/", current.Port)
}

// tailFile returns the last n lines of a log file. Cheap O(file
// size) read since logs are bounded; replace with reverse-seek when
// they grow.
func tailFile(path string, n int) (string, error) {
	if path == "" {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return tailLines(string(body), n), nil
}

func tailLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	count := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			count++
			if count > n {
				return s[i+1:]
			}
		}
	}
	return s
}

// ─── arg helpers ──────────────────────────────────────────────────

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}
