package main

// Test workspaces (kind=test) run untrusted test suites for apps. One
// workspace is one hardened container: no capabilities, read-only root,
// numeric non-root users, no shell session, no previews and no file tools.
// Two users stand in for read-only mounts: builder owns the source and the
// dependency cache, runner owns only the results folder. Without
// capabilities neither can bypass file permissions, so source and cache are
// read-only to test commands.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	testBuilderUser = "1000:1000"
	testRunnerUser  = "1001:1001"
	testSourcePath  = "src"
	testResultsPath = "out"
	testSourceDir   = "/workspace/" + testSourcePath
	testResultsDir  = "/workspace/" + testResultsPath
	testProfileBun  = "bun-test"

	// Digest-pinned so every command result names the exact image it ran.
	defaultBunTestImage = "oven/bun:1-debian@sha256:93d5032fe06d163427822dc1598f420363759bb18155c9cc90eb1be05a64a19e"

	sourceDigestMethod = "workspaces-tree-v1"

	networkNone         = "none"
	networkRegistryOnly = "registry_only"
	networkDefault      = "default"
)

var testLockfiles = map[string]bool{
	"bun.lock": true, "bun.lockb": true, "package-lock.json": true,
	"yarn.lock": true, "pnpm-lock.yaml": true,
}

type testSourceInfo struct {
	Digest   string
	CacheKey string
	Files    int
}

// inspectTestSource validates a source archive and computes, outside the
// container, the tree digest and the dependency cache key.
//
// Digest (workspaces-tree-v1): sha256 over sorted lines
// "path NUL type NUL mode NUL sha256(content or link target) LF" for every
// file and symlink, where type is f or l and mode is 755 for executable files
// and 644 otherwise (symlinks 777). Folders are implied by their files.
// Anything under node_modules is left out. The cache key hashes the
// lockfiles at the archive root; no lockfile means no shared cache.
func inspectTestSource(encoded string) (testSourceInfo, error) {
	var info testSourceInfo
	compressed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return info, fmt.Errorf("decode source archive: %w", err)
	}
	if len(compressed) > maxManagedSourceArchiveBytes {
		return info, errors.New("source archive exceeds 8 MiB compressed")
	}
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return info, fmt.Errorf("open source archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	lines := map[string]string{}
	locks := map[string]string{}
	var expanded int64
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return info, fmt.Errorf("read source archive: %w", err)
		}
		name := strings.TrimPrefix(strings.TrimSpace(strings.ReplaceAll(header.Name, "\\", "/")), "./")
		if name == "" || name == "." || header.Typeflag == tar.TypeDir {
			continue
		}
		clean, err := normalizeSourcePath(name)
		if err != nil {
			return info, err
		}
		var kind, mode, sum string
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			expanded += header.Size
			if expanded > maxManagedSourceExpandedBytes {
				return info, errors.New("source archive exceeds 128 MiB expanded")
			}
			h := sha256.New()
			if _, err := io.Copy(h, io.LimitReader(tr, header.Size)); err != nil {
				return info, fmt.Errorf("read source archive: %w", err)
			}
			kind, mode, sum = "f", "644", hex.EncodeToString(h.Sum(nil))
			if header.Mode&0o111 != 0 {
				mode = "755"
			}
		case tar.TypeSymlink:
			if header.Linkname == "" || path.IsAbs(header.Linkname) {
				return info, fmt.Errorf("source symlink %q has an unsafe target", clean)
			}
			resolved := path.Clean(path.Join(path.Dir(clean), header.Linkname))
			if resolved == ".." || strings.HasPrefix(resolved, "../") {
				return info, fmt.Errorf("source symlink %q escapes the workspace source", clean)
			}
			linkSum := sha256.Sum256([]byte(header.Linkname))
			kind, mode, sum = "l", "777", hex.EncodeToString(linkSum[:])
		default:
			return info, fmt.Errorf("unsupported source archive entry %q", clean)
		}
		if inDependencyFolder(clean) {
			continue
		}
		lines[clean] = clean + "\x00" + kind + "\x00" + mode + "\x00" + sum + "\n"
		if len(lines) > maxManagedSourceFiles {
			return info, errors.New("source archive exceeds 20000 files")
		}
		if kind == "f" && testLockfiles[clean] {
			locks[clean] = sum
		}
	}
	if len(lines) == 0 {
		return info, errors.New("source archive contains no files")
	}
	info.Files = len(lines)
	info.Digest = "sha256:" + hashSortedValues(lines)
	if len(locks) > 0 {
		lockLines := make(map[string]string, len(locks))
		for name, sum := range locks {
			lockLines[name] = name + "\x00" + sum + "\n"
		}
		info.CacheKey = hashSortedValues(lockLines)[:32]
	}
	return info, nil
}

func inDependencyFolder(clean string) bool {
	for _, part := range strings.Split(clean, "/") {
		if part == "node_modules" {
			return true
		}
	}
	return false
}

func hashSortedValues(entries map[string]string) string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, key := range keys {
		io.WriteString(h, entries[key])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// emptyArchiveBase64 is a tar.gz with no entries. Importing it with an owner
// creates the target folder owned by that user and nothing else.
func emptyArchiveBase64() string {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.Close()
	_ = gz.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func testAppAllowed(app *sdk.AppCtx, appName string) bool {
	allowed := strings.FieldsFunc(configString(app, "test_workspace_apps", ""), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r'
	})
	if len(allowed) == 0 {
		return true
	}
	for _, name := range allowed {
		if name == appName {
			return true
		}
	}
	return false
}

func testEgressAllow(app *sdk.AppCtx) []string {
	return strings.FieldsFunc(configString(app, "test_egress_allow", "registry.npmjs.org"), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r'
	})
}

func requireDevWorkspace(w *Workspace, what string) error {
	if w != nil && w.Kind == kindTest {
		return fmt.Errorf("%s is not available on test workspaces", what)
	}
	return nil
}

func requireTestWorkspace(w *Workspace) error {
	if w == nil || w.Kind != kindTest {
		return errors.New("this tool works only on test workspaces")
	}
	return nil
}

func testMaxTTL(app *sdk.AppCtx) int { return configInt(app, "test_max_ttl_minutes", 120) }

func (a *App) createTestWorkspace(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (*Workspace, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	if err := requireOriginatingApp(actor); err != nil {
		return nil, err
	}
	if !testAppAllowed(app, actor.AppName) {
		return nil, fmt.Errorf("app %q may not create test workspaces; add it to test_workspace_apps", actor.AppName)
	}
	name, err := normalizeWorkspaceName(strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	profile := firstNonEmpty(strings.ToLower(strArg(args, "profile")), testProfileBun)
	if profile != testProfileBun {
		return nil, fmt.Errorf("unsupported test profile %q; use %s", profile, testProfileBun)
	}
	image := configString(app, "bun_test_image", defaultBunTestImage)
	if !imageDigestRE.MatchString(image) {
		return nil, errors.New("bun_test_image must be pinned with an @sha256 digest")
	}
	maxTTL := testMaxTTL(app)
	ttl := intArg(args, "ttl_minutes", configInt(app, "test_default_ttl_minutes", 30))
	if ttl < 1 || ttl > maxTTL {
		return nil, fmt.Errorf("ttl_minutes must be between 1 and %d", maxTTL)
	}
	archive := strArg(args, "source_archive_base64")
	if archive == "" {
		return nil, errors.New("source_archive_base64 is required")
	}
	source, err := inspectTestSource(archive)
	if err != nil {
		return nil, err
	}
	originHref, err := normalizeOriginHref(strArg(args, "origin_href"))
	if err != nil {
		return nil, err
	}
	idempotencyKey := callerIdempotencyKey(callCtx, actor)
	if requested := strArg(args, "idempotency_key"); requested != "" {
		if len(requested) > 200 || strings.IndexByte(requested, 0) >= 0 {
			return nil, errors.New("idempotency_key must be at most 200 characters without NUL")
		}
		idempotencyKey = actor.Kind + ":" + actor.ID + ":" + requested
	}
	if idempotencyKey != "" {
		existing, err := getWorkspaceByIdempotency(app.AppDB(), actor.ProjectID, idempotencyKey)
		if err != nil || existing != nil {
			return existing, err
		}
	}
	useCache := true
	if value, ok := args["use_cache"].(bool); ok {
		useCache = value
	}
	cacheKey := ""
	if useCache {
		cacheKey = source.CacheKey
	}
	now := time.Now().UTC()
	expires := now.Add(time.Duration(ttl) * time.Minute)
	w := &Workspace{
		ID: newID("wsp"), ProjectID: actor.ProjectID, Name: name, Kind: kindTest,
		Purpose: strArg(args, "purpose"), Profile: profile, Image: image,
		LifecycleStatus: statusProvisioning, ActivityStatus: activityIdle,
		HostLabel: "Local Docker", NetworkPolicy: "per-command", NetworkMode: networkNone,
		CPU: configFloat(app, "test_cpu", 2), MemoryMB: configInt(app, "test_memory_mb", 4096),
		ConsumerApp: actor.AppName, ConsumerInstallID: actor.InstallID,
		OwnerLabel:   firstNonEmpty(strArg(args, "owner_label"), actor.Label),
		ResourceKind: strArg(args, "resource_kind"), ResourceID: strArg(args, "resource_id"),
		RepoLabel: strArg(args, "repo_label"), BranchLabel: strArg(args, "branch_label"),
		OriginLabel: strArg(args, "origin_label"), OriginHref: originHref,
		DirtyState: "unknown", UnpushedState: "unknown",
		SourceDigest: source.Digest, CacheKey: cacheKey,
		CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
		LastActivityAt: now.Format(time.RFC3339), ExpiresAt: expires.Format(time.RFC3339),
		// No retention: an expired test workspace is destroyed right away.
		DeleteAt: expires.Format(time.RFC3339),
	}
	if err := insertWorkspace(app.AppDB(), w, idempotencyKey); err != nil {
		if idempotencyKey != "" {
			if existing, getErr := getWorkspaceByIdempotency(app.AppDB(), actor.ProjectID, idempotencyKey); getErr == nil && existing != nil {
				return existing, nil
			}
		}
		return nil, err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "workspace.provisioning", actor, "Test workspace provisioning started", map[string]any{"profile": profile, "image": image, "source_digest": source.Digest})
	fail := func(stage string, err error) (*Workspace, error) {
		if w.WorkloadID != "" {
			var destroyed map[string]any
			_ = app.PlatformAPI().CallAppResult("containers", "containers_destroy", map[string]any{"workload_id": w.WorkloadID, "delete_volumes": true}, &destroyed)
		}
		_ = updateWorkspace(app.AppDB(), w.ID, map[string]any{
			"workload_id": w.WorkloadID, "lifecycle_status": statusFailed,
			"last_error": stage + ": " + err.Error(), "updated_at": nowUTC(),
		})
		_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "workspace.failed", Actor{Kind: "runtime", Label: "Containers"}, "Test workspace provisioning failed", map[string]any{"stage": stage, "error": err.Error()})
		return nil, fmt.Errorf("%s: %w", stage, err)
	}

	cacheVolume := map[string]any{"name": "cache", "mount_path": "/cache"}
	if cacheKey != "" {
		var created struct {
			Volume struct {
				ID string `json:"id"`
			} `json:"volume"`
		}
		if err := app.PlatformAPI().CallAppResult("containers", "containers_volume_create", map[string]any{
			"name":   "wst-" + profile + "-" + cacheKey[:24],
			"labels": map[string]string{"app": "workspaces", "kind": "test-cache", "profile": profile, "cache_key": cacheKey},
		}, &created); err != nil {
			return fail("cache volume", err)
		}
		if created.Volume.ID == "" {
			return fail("cache volume", errors.New("containers_volume_create returned no volume id"))
		}
		w.CacheVolumeID = created.Volume.ID
		cacheVolume["volume_id"] = created.Volume.ID
		if _, err := app.AppDB().Exec(`UPDATE workspaces SET cache_volume_id=? WHERE id=?`, w.CacheVolumeID, w.ID); err != nil {
			return fail("cache volume", err)
		}
	}
	var runOut workloadResponse
	if err := app.PlatformAPI().CallAppResult("containers", "containers_run", map[string]any{
		"name":     "workspace-" + strings.TrimPrefix(w.ID, "wsp_")[:12],
		"image":    image,
		"hardened": true, "user": testBuilderUser, "network_mode": networkNone,
		"volumes": []map[string]any{{"name": "workspace", "mount_path": "/workspace"}, cacheVolume},
		"tmpfs":   []map[string]any{{"mount_path": "/tmp", "size_mb": configInt(app, "test_tmp_mb", 512)}},
		"env": map[string]string{
			"HOME": "/tmp/home", "TMPDIR": "/tmp", "XDG_CACHE_HOME": "/tmp/xdg", "CI": "1",
			"TESTS_OUT": testResultsDir, "BUN_INSTALL_CACHE_DIR": "/cache/bun",
		},
		"resources":      map[string]any{"cpu": w.CPU, "memory_mb": w.MemoryMB},
		"restart_policy": "no", "pull_policy": "missing", "use_local": true,
		// /workspace, not the source folder: Docker would create a missing
		// working directory as root before the source import could own it.
		"working_directory": "/workspace",
		"command":           []string{"/bin/sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 3600; done"},
	}, &runOut); err != nil {
		return fail("start container", err)
	}
	if runOut.Workload.ID == "" {
		return fail("start container", errors.New("containers_run returned an empty workload id"))
	}
	w.WorkloadID = runOut.Workload.ID
	empty := emptyArchiveBase64()
	imports := []struct {
		stage, volume, path, archive, owner string
	}{
		{"source import", "workspace", testSourcePath, archive, testBuilderUser},
		{"results folder", "workspace", testResultsPath, empty, testRunnerUser},
		{"cache folder", "cache", "bun", empty, testBuilderUser},
	}
	for _, step := range imports {
		var imported map[string]any
		if err := app.PlatformAPI().CallAppResult("containers", "containers_volume_import", map[string]any{
			"workload_id": w.WorkloadID, "volume": step.volume, "path": step.path,
			"archive_base64": step.archive, "owner": step.owner,
		}, &imported); err != nil {
			return fail(step.stage, err)
		}
	}
	syncedAt := nowUTC()
	if err := updateWorkspace(app.AppDB(), w.ID, map[string]any{
		"workload_id": w.WorkloadID, "lifecycle_status": statusRunning,
		"runtime_status": firstNonEmpty(runOut.Workload.Status, "running"),
		"health_status":  runOut.Workload.HealthStatus, "source_synced_at": syncedAt,
		"last_error": "", "updated_at": syncedAt,
	}); err != nil {
		return nil, err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "workspace.created", actor, "Test workspace is ready", map[string]any{
		"workload_id": w.WorkloadID, "source_digest": source.Digest, "files": source.Files, "cache_key": cacheKey,
	})
	app.EmitWithProject("workspace.created", w.ProjectID, map[string]any{"workspace_id": w.ID, "consumer_app": w.ConsumerApp, "kind": kindTest})
	return requireWorkspace(app.AppDB(), w.ProjectID, w.ID)
}

type testCommandOptions struct {
	User    string // builder or runner
	UID     string
	Network string
}

// parseTestCommandOptions reads user and network for a command. Test
// workspaces default to runner without network; dev workspaces accept neither.
func parseTestCommandOptions(w *Workspace, args map[string]any) (testCommandOptions, error) {
	user := strings.ToLower(strArg(args, "user"))
	network := strings.ToLower(strArg(args, "network"))
	if w.Kind != kindTest {
		if user != "" || network != "" {
			return testCommandOptions{}, errors.New("user and network apply only to test workspaces")
		}
		return testCommandOptions{}, nil
	}
	if strArg(args, "shell_command") != "" {
		return testCommandOptions{}, errors.New("test workspaces run argv without a shell; shell_command is not accepted")
	}
	opts := testCommandOptions{User: firstNonEmpty(user, "runner"), Network: firstNonEmpty(network, networkNone)}
	switch opts.User {
	case "builder":
		opts.UID = testBuilderUser
	case "runner":
		opts.UID = testRunnerUser
	default:
		return opts, errors.New("user must be builder or runner")
	}
	switch opts.Network {
	case networkNone, networkRegistryOnly, networkDefault:
	default:
		return opts, errors.New("network must be none, registry_only or default")
	}
	return opts, nil
}

// switchTestNetwork sets the workspace network before a command. Only one
// command runs at a time, so a setting cannot leak into another command.
func (a *App) switchTestNetwork(app *sdk.AppCtx, w *Workspace, mode string) error {
	if w.NetworkMode == mode {
		return nil
	}
	input := map[string]any{"workload_id": w.WorkloadID, "mode": mode}
	if mode == networkRegistryOnly {
		input["egress_allow"] = testEgressAllow(app)
	}
	var out workloadResponse
	if err := app.PlatformAPI().CallAppResult("containers", "containers_network_set", input, &out); err != nil {
		return fmt.Errorf("switch network to %s: %w", mode, err)
	}
	w.NetworkMode = mode
	return updateWorkspace(app.AppDB(), w.ID, map[string]any{"network_mode": mode, "updated_at": nowUTC()})
}

func (a *App) toolTestCreate(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := a.createTestWorkspace(callCtx, app, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workspace": w, "source_digest": w.SourceDigest, "source_dir": testSourceDir, "results_dir": testResultsDir}, nil
}

func (a *App) toolSourceDigest(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return nil, err
	}
	if err := requireTestWorkspace(w); err != nil {
		return nil, err
	}
	return map[string]any{
		"workspace_id": w.ID, "source_digest": w.SourceDigest, "method": sourceDigestMethod,
		"cache_key": w.CacheKey, "image": w.Image,
	}, nil
}

func (a *App) toolResultsExport(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	if err := requireOriginatingApp(actor); err != nil {
		return nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return nil, err
	}
	if err := requireTestWorkspace(w); err != nil {
		return nil, err
	}
	destroy := true
	if value, ok := args["destroy"].(bool); ok {
		destroy = value
	}
	release := a.lockWorkspace(w.ID)
	w, err = requireWorkspace(app.AppDB(), w.ProjectID, w.ID)
	if err != nil {
		release()
		return nil, err
	}
	if err := requireFileAccessible(w); err != nil {
		release()
		return nil, err
	}
	if err := ensureNoActiveCommand(app.AppDB(), w.ID); err != nil {
		release()
		return nil, err
	}
	var out struct {
		ArchiveBase64   string `json:"archive_base64"`
		CompressedBytes int    `json:"compressed_bytes"`
		Format          string `json:"format"`
	}
	err = app.PlatformAPI().CallAppResult("containers", "containers_volume_export", map[string]any{
		"workload_id": w.WorkloadID, "volume": "workspace", "path": testResultsPath,
	}, &out)
	if err == nil {
		now := nowUTC()
		_ = updateWorkspace(app.AppDB(), w.ID, map[string]any{"results_exported_at": now, "updated_at": now})
		_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "results.exported", actor, "Test results exported", map[string]any{"compressed_bytes": out.CompressedBytes})
	}
	release()
	if err != nil {
		return nil, err
	}
	compressed := out.CompressedBytes
	if compressed == 0 {
		if raw, decodeErr := base64.StdEncoding.DecodeString(out.ArchiveBase64); decodeErr == nil {
			compressed = len(raw)
		}
	}
	result := map[string]any{
		"workspace_id": w.ID, "format": firstNonEmpty(out.Format, "tar.gz"), "archive_base64": out.ArchiveBase64,
		"compressed_bytes": compressed, "source_digest": w.SourceDigest, "image": w.Image, "destroyed": false,
	}
	if destroy {
		if _, err := a.destroyWorkspace(app, actor, w); err != nil {
			result["destroy_error"] = err.Error()
		} else {
			result["destroyed"] = true
		}
	}
	return result, nil
}

type standaloneVolume struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	CreatedAt  string   `json:"created_at"`
	LastUsedAt string   `json:"last_used_at"`
	MountedBy  []string `json:"mounted_by"`
}

// collectTestCaches deletes dependency caches no workload has used for
// test_cache_retention_days. It runs at most once an hour.
func (a *App) collectTestCaches(app *sdk.AppCtx, now time.Time) error {
	a.cacheGCMu.Lock()
	if !a.lastCacheGC.IsZero() && now.Sub(a.lastCacheGC) < time.Hour {
		a.cacheGCMu.Unlock()
		return nil
	}
	a.lastCacheGC = now
	a.cacheGCMu.Unlock()
	var listed struct {
		Volumes []standaloneVolume `json:"volumes"`
	}
	if err := app.PlatformAPI().CallAppResult("containers", "containers_volume_list", map[string]any{
		"labels": map[string]string{"app": "workspaces", "kind": "test-cache"},
	}, &listed); err != nil {
		return fmt.Errorf("list test caches: %w", err)
	}
	cutoff := now.Add(-time.Duration(configInt(app, "test_cache_retention_days", 7)) * 24 * time.Hour)
	var errs []error
	for _, v := range listed.Volumes {
		if len(v.MountedBy) > 0 {
			continue
		}
		last := parseTime(firstNonEmpty(v.LastUsedAt, v.CreatedAt))
		if last.IsZero() || last.After(cutoff) {
			continue
		}
		var deleted map[string]any
		if err := app.PlatformAPI().CallAppResult("containers", "containers_volume_delete", map[string]any{"volume_id": v.ID}, &deleted); err != nil {
			errs = append(errs, fmt.Errorf("delete test cache %s: %w", v.Name, err))
		}
	}
	return errors.Join(errs...)
}
