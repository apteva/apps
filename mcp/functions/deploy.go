package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	sdk "github.com/apteva/app-sdk"
)

// deployVersion creates a new immutable version for fn, builds it
// once, and — on a successful build — makes it active. Used by both
// the initial create (v1) and functions_deploy (v2+).
func deployVersion(ctx *sdk.AppCtx, fn *Function, sourceKind, source string, repoID *int64, repoPath, packageJSON string) (*FunctionVersion, error) {
	return deployVersionContext(context.Background(), ctx, fn, sourceKind, source, repoID, repoPath, packageJSON)
}
func deployVersionContext(parent context.Context, ctx *sdk.AppCtx, fn *Function, sourceKind, source string, repoID *int64, repoPath, packageJSON string) (*FunctionVersion, error) {
	p := currentPool()
	if p == nil {
		return nil, errors.New("pool unavailable")
	}
	parent = context.WithValue(parent, poolContextKey{}, p)
	parent, cancel := context.WithTimeout(parent, buildTimeout)
	defer cancel()
	stop := context.AfterFunc(p.life, cancel)
	defer stop()
	db := dbFor(ctx)
	spec, err := resolveRuntime(fn.Runtime)
	if err != nil {
		return nil, err
	}
	if sourceKind != "inline" && sourceKind != "repo" {
		return nil, fmt.Errorf("source_kind %q must be inline|repo", sourceKind)
	}

	// Resolve + hash the source up front so the version row is
	// complete before the build runs.
	probe := &FunctionVersion{
		SourceKind: sourceKind, Source: source, RepoID: repoID, RepoPath: repoPath,
	}
	src, err := resolveVersionSourceContext(parent, ctx, probe)
	if err != nil {
		return nil, fmt.Errorf("resolve source: %w", err)
	}

	ver, err := dbCreateVersion(db, fn.ProjectID, &FunctionVersion{
		ArtifactKey: fn.InstanceKey, FunctionID: fn.ID, SourceKind: sourceKind, Source: string(src),
		RepoID: repoID, RepoPath: repoPath, SourceHash: hashSource(src),
		PackageJSON: packageJSON, BuildStatus: "building",
	})
	if err != nil {
		return nil, err
	}

	base, err := poolBuildBase()
	if err != nil {
		return nil, err
	}
	defer func() {
		current, _ := dbGetFunction(db, fn.ProjectID, fn.ID, "")
		if current == nil || current.InstanceKey != fn.InstanceKey {
			_ = removeTree(filepath.Join(base, fn.InstanceKey))
		}
	}()
	dir, buildErr := ensureBuiltContext(parent, base, ver, spec, src)
	if buildErr != nil {
		_ = dbUpdateVersionBuild(db, fn.ProjectID, ver.ID, "failed", buildErr.Error(), "", fn.InstanceKey)
		ver.BuildStatus = "failed"
		ver.BuildLog = buildErr.Error()
		return ver, fmt.Errorf("build v%d failed: %w", ver.Version, buildErr)
	}
	ver.BuildDir = dir
	if lock, err := os.ReadFile(filepath.Join(dir, "package-lock.json")); err == nil {
		ver.PackageLock = string(lock)
		if _, err = db.Exec(`UPDATE function_versions SET package_lock=? WHERE id=? AND project_id=? AND artifact_key=?`, ver.PackageLock, ver.ID, fn.ProjectID, fn.InstanceKey); err != nil {
			return nil, err
		}
	}
	candidate, err := p.start(parent, fn, ver, spec, dir)
	if err != nil {
		_ = dbUpdateVersionBuild(db, fn.ProjectID, ver.ID, "failed", err.Error(), dir, fn.InstanceKey)
		return ver, fmt.Errorf("worker validation: %w", err)
	}

	release, err := lockBuild(parent, fmt.Sprintf("activation-%d", fn.ID))
	if err != nil {
		p.discard(candidate)
		return nil, err
	}
	defer release()
	if err := dbUpdateVersionBuild(db, fn.ProjectID, ver.ID, "ready", "", dir, fn.InstanceKey); err != nil {
		p.discard(candidate)
		return nil, err
	}
	ver.BuildStatus = "ready"
	if err := dbSetActiveVersion(db, fn.ProjectID, fn.ID, ver); err != nil {
		p.discard(candidate)
		if current, _ := dbGetFunction(db, fn.ProjectID, fn.ID, ""); current == nil || current.InstanceKey != fn.InstanceKey {
			_ = removeTree(filepath.Dir(dir))
		}
		return nil, err
	}
	if p != nil {
		p.cacheVersion(ver)
		updated, _ := dbGetFunction(db, fn.ProjectID, fn.ID, "")
		if updated == nil || updated.InstanceKey != fn.InstanceKey {
			p.discard(candidate)
			return nil, errors.New("function deleted during deployment")
		}
		p.refreshFunction(updated)
		p.put(updated, p.poolFor(fn.ID), candidate)
	}
	ctx.WithProject(fn.ProjectID).Emit("function.deployed", map[string]any{"id": fn.ID, "version": ver.Version})
	return ver, nil
}

// rollbackFunction repoints a function's active version at an
// existing, already-built version.
func rollbackFunction(ctx *sdk.AppCtx, pid string, fnID int64, version int) (*FunctionVersion, error) {
	return rollbackFunctionContext(context.Background(), ctx, pid, fnID, version)
}
func rollbackFunctionContext(parent context.Context, ctx *sdk.AppCtx, pid string, fnID int64, version int) (*FunctionVersion, error) {
	p := currentPool()
	if p == nil {
		return nil, errors.New("pool unavailable")
	}
	parent = context.WithValue(parent, poolContextKey{}, p)
	parent, cancel := context.WithTimeout(parent, buildTimeout)
	defer cancel()
	stop := context.AfterFunc(p.life, cancel)
	defer stop()
	db := dbFor(ctx)
	release, err := lockBuild(parent, fmt.Sprintf("activation-%d", fnID))
	if err != nil {
		return nil, err
	}
	defer release()
	ver, err := dbGetVersionByNumber(db, pid, fnID, version)
	if err != nil {
		return nil, err
	}
	if ver == nil {
		return nil, fmt.Errorf("version %d not found", version)
	}
	if ver.BuildStatus != "ready" {
		return nil, fmt.Errorf("version %d build_status=%s — only a ready version can be activated", version, ver.BuildStatus)
	}
	fn, err := dbGetFunction(db, pid, fnID, "")
	if err != nil {
		return nil, err
	}
	if fn == nil {
		return nil, errors.New("function not found")
	}
	spec, err := resolveRuntime(fn.Runtime)
	if err != nil {
		return nil, err
	}
	src, err := resolveVersionSourceContext(parent, ctx, ver)
	if err != nil {
		return nil, err
	}
	dir, err := ensureBuiltContext(parent, p.buildBase, ver, spec, src)
	if err != nil {
		return nil, err
	}
	candidate, err := p.start(parent, fn, ver, spec, dir)
	if err != nil {
		return nil, err
	}
	kept := false
	defer func() {
		if !kept {
			p.discard(candidate)
		}
	}()
	if err := db.QueryRow(`UPDATE functions SET deployment_revision=deployment_revision+1 WHERE id=? AND project_id=? RETURNING deployment_revision`, fnID, pid).Scan(&ver.DeploymentRevision); err != nil {
		return nil, err
	}
	if err := dbSetActiveVersion(db, pid, fnID, ver); err != nil {
		return nil, err
	}
	if p != nil {
		p.cacheVersion(ver)
		updated, _ := dbGetFunction(db, pid, fnID, "")
		p.refreshFunction(updated)
		if updated != nil {
			p.put(updated, p.poolFor(fnID), candidate)
			kept = true
		}
	}
	ctx.WithProject(pid).Emit("function.deployed", map[string]any{"id": fnID, "version": ver.Version, "rollback": true})
	return ver, nil
}

// deployFromArgs reads source / package_json from a tool or HTTP arg
// map and deploys a new version of an existing function.
func deployFromArgs(ctx *sdk.AppCtx, pid string, fnID int64, args map[string]any) (*Function, *FunctionVersion, error) {
	return deployFromArgsContext(context.Background(), ctx, pid, fnID, args)
}
func deployFromArgsContext(parent context.Context, ctx *sdk.AppCtx, pid string, fnID int64, args map[string]any) (*Function, *FunctionVersion, error) {
	if err := validateFunctionArgs(args, false); err != nil {
		return nil, nil, err
	}
	fn, err := dbGetFunction(dbFor(ctx), pid, fnID, "")
	if err != nil {
		return nil, nil, err
	}
	if fn == nil {
		return nil, nil, errors.New("function not found")
	}
	sourceKind := strArg(args, "source_kind")
	source := strArg(args, "source")
	repoPath := strArg(args, "repo_path")
	var repoID *int64
	if rid := int64Arg(args, "repo_id"); rid != 0 {
		repoID = &rid
	}
	if sourceKind == "" {
		if source != "" {
			sourceKind = "inline"
		} else if repoID != nil {
			sourceKind = "repo"
		}
	}
	if sourceKind == "" {
		return nil, nil, errors.New("deploy needs source (inline) or repo_id + repo_path")
	}
	// Carry forward the prior active version's package_json when the
	// caller omits the field. An explicit "" still clears deps; a
	// present value overrides. Without this, a source-only redeploy
	// ships an empty node_modules and breaks at cold-start.
	pkg := strArg(args, "package_json")
	if _, present := args["package_json"]; !present && fn.ActiveVersionID != nil {
		if prior, perr := dbGetVersion(dbFor(ctx), pid, *fn.ActiveVersionID); perr == nil && prior != nil {
			pkg = prior.PackageJSON
		}
	}
	ver, err := deployVersionContext(parent, ctx, fn, sourceKind, source, repoID, repoPath, pkg)
	if err != nil {
		return nil, ver, err
	}
	updated, _ := dbGetFunction(dbFor(ctx), pid, fnID, "")
	return updated, ver, nil
}
