package main

import (
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// deployVersion creates a new immutable version for fn, builds it
// once, and — on a successful build — makes it active. Used by both
// the initial create (v1) and functions_deploy (v2+).
func deployVersion(ctx *sdk.AppCtx, fn *Function, sourceKind, source string, repoID *int64, repoPath, packageJSON string) (*FunctionVersion, error) {
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
	src, err := resolveVersionSource(ctx, probe)
	if err != nil {
		return nil, fmt.Errorf("resolve source: %w", err)
	}

	ver, err := dbCreateVersion(db, fn.ProjectID, &FunctionVersion{
		FunctionID: fn.ID, SourceKind: sourceKind, Source: source,
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
	dir, buildErr := ensureBuilt(base, ver, spec, src)
	if buildErr != nil {
		_ = dbUpdateVersionBuild(db, fn.ProjectID, ver.ID, "failed", buildErr.Error(), "")
		ver.BuildStatus = "failed"
		ver.BuildLog = buildErr.Error()
		return ver, fmt.Errorf("build v%d failed: %w", ver.Version, buildErr)
	}
	if err := dbUpdateVersionBuild(db, fn.ProjectID, ver.ID, "ready", "", dir); err != nil {
		return nil, err
	}
	ver.BuildStatus = "ready"
	ver.BuildDir = dir

	if err := dbSetActiveVersion(db, fn.ProjectID, fn.ID, ver); err != nil {
		return nil, err
	}
	return ver, nil
}

// rollbackFunction repoints a function's active version at an
// existing, already-built version.
func rollbackFunction(ctx *sdk.AppCtx, pid string, fnID int64, version int) (*FunctionVersion, error) {
	db := dbFor(ctx)
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
	if err := dbSetActiveVersion(db, pid, fnID, ver); err != nil {
		return nil, err
	}
	return ver, nil
}

// deployFromArgs reads source / package_json from a tool or HTTP arg
// map and deploys a new version of an existing function.
func deployFromArgs(ctx *sdk.AppCtx, pid string, fnID int64, args map[string]any) (*Function, *FunctionVersion, error) {
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
	ver, err := deployVersion(ctx, fn, sourceKind, source, repoID, repoPath, strArg(args, "package_json"))
	if err != nil {
		return nil, ver, err
	}
	updated, _ := dbGetFunction(dbFor(ctx), pid, fnID, "")
	return updated, ver, nil
}
