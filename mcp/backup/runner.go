package main

// Runner — orchestrates a single backup.
//
// Steps for a successful run:
//   1. Insert a runs row in 'running' state (so the UI can see it live)
//   2. Stream a platform snapshot through the app-authorized SDK, hashing the
//      bytes as they fly past, into a temp file
//   3. Read the snapshot's manifest.json out of the tar without
//      decompressing the whole archive into memory
//   4. Re-open the temp file and Put it on the destination
//   5. Update the runs row with bytes/sha/key/manifest
//   6. Prune older runs against the policy's retention_keep
//
// Errors mid-flight flip the row to 'failed' with the error message;
// the file (local) or partial upload (s3) is best-effort cleaned up.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	transferTimeout     = 2 * time.Hour
	responseHeaderLimit = 30 * time.Second
	maxManifestBytes    = 4 << 20
)

var platformTransferClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = responseHeaderLimit
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{Transport: transport}
}()

const platformBackupUnsupportedMessage = "server does not support app-authorized platform backups; update Apteva Server"

var operationState struct {
	sync.Mutex
	active string
}

func acquireOperation(kind string) (func(), error) {
	operationState.Lock()
	defer operationState.Unlock()
	if operationState.active != "" {
		return nil, fmt.Errorf("%s cannot start while %s is running", kind, operationState.active)
	}
	operationState.active = kind
	return func() {
		operationState.Lock()
		operationState.active = ""
		operationState.Unlock()
	}, nil
}

// runBackup executes a backup against dest. policy may be nil for
// ad-hoc / "run now" calls; retention pruning is skipped in that case
// (only policy-driven runs prune, since ad-hoc runs typically share
// destinations with policies and we don't want a one-off button click
// to silently delete scheduled backups).
func runBackup(ctx *sdk.AppCtx, dest *Destination, policy *Policy, scope Scope) (*Run, error) {
	if scope.Kind == "" {
		scope = defaultScope()
	}
	run := &Run{
		DestinationID:   dest.ID,
		DestinationName: dest.Name,
		Scope:           scope,
	}
	if policy != nil {
		run.PolicyID = policy.ID
	}
	id, err := dbInsertRun(ctx.AppDB(), run)
	if err != nil {
		return nil, err
	}
	run.ID = id
	release, err := acquireOperation("backup")
	if err != nil {
		msg := err.Error()
		_ = dbFinishRun(ctx.AppDB(), id, "failed", 0, "", "", "", msg, false)
		out, _ := dbGetRun(ctx.AppDB(), id)
		return out, errors.New(msg)
	}
	defer release()
	opCtx, cancelOperation := context.WithTimeout(context.Background(), transferTimeout)
	defer cancelOperation()

	finish := func(status, errMsg string, bytes int64, sha, key, manifestJSON string, encrypted bool) (*Run, error) {
		if err := dbFinishRun(ctx.AppDB(), id, status, bytes, sha, key, manifestJSON, errMsg, encrypted); err != nil {
			return nil, fmt.Errorf("record backup result: %w", err)
		}
		_ = pruneFailedRunHistory(ctx)
		out, err := dbGetRun(ctx.AppDB(), id)
		if err != nil {
			return nil, err
		}
		ctx.Emit("run."+status, map[string]any{"run_id": id, "destination_id": dest.ID, "scope": scope})
		if status == "failed" {
			return out, errors.New(errMsg)
		}
		return out, nil
	}

	// 1) Open the destination first so credentials/endpoint failures
	// don't waste a snapshot.
	_ = dbUpdateRunStage(ctx.AppDB(), id, "opening destination")
	writer, err := openDestination(dest, ctx, defaultLocalBackupDir(ctx))
	if err != nil {
		return finish("failed", "open destination: "+err.Error(), 0, "", "", "", false)
	}

	// 2) Stream snapshot to a temp file, hashing as we go.
	tmp, err := os.CreateTemp("", "apteva-snapshot-*.tar.gz")
	if err != nil {
		return finish("failed", "tempfile: "+err.Error(), 0, "", "", "", false)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	hash := sha256.New()
	_ = dbUpdateRunStage(ctx.AppDB(), id, "snapshotting")
	written, providerManifest, err := writeSnapshot(opCtx, ctx, io.MultiWriter(tmp, hash), scope)
	if errClose := tmp.Close(); err == nil {
		err = errClose
	}
	if err != nil {
		return finish("failed", "stream snapshot: "+err.Error(), 0, "", "", "", false)
	}
	sha := hex.EncodeToString(hash.Sum(nil))

	// 3) Crack the tar to extract manifest.json — useful for forensic
	// diffs across runs ("which install was added between these two?").
	_ = dbUpdateRunStage(ctx.AppDB(), id, "validating")
	manifestJSON, err := validateSnapshotArchive(tmpPath)
	if err != nil {
		return finish("failed", "validate snapshot: "+err.Error(), written, sha, "", "", false)
	}
	if providerManifest != "" && !json.Valid([]byte(providerManifest)) {
		return finish("failed", "validate provider manifest: invalid JSON", written, sha, "", manifestJSON, false)
	}

	// 4) Upload the validated snapshot. Encryption streams through a pipe so
	// the host never needs a second full encrypted temporary file.
	encrypted := backupPassphrase(ctx) != ""
	key := buildRemoteKey(run, encrypted)
	stage := "uploading"
	if encrypted {
		stage = "encrypting and uploading"
	}
	_ = dbUpdateRunStage(ctx.AppDB(), id, stage)
	uploadSize, uploadSHA, encrypted, err := putStoredSnapshot(opCtx, ctx, writer, key, tmpPath, written, sha)
	if err != nil {
		_ = writer.Delete(opCtx, key)
		return finish("failed", "upload: "+err.Error(), uploadSize, uploadSHA, "", manifestJSON, encrypted)
	}

	// 5) Retention prune. Best-effort; failures here don't taint the
	// successful run, but the stage remains visible while large prefixes
	// are being cleaned up.
	if policy != nil && policy.RetentionKeep > 0 {
		_ = dbUpdateRunStage(ctx.AppDB(), id, "pruning")
		if err := pruneRetention(opCtx, ctx, writer, dest, policy); err != nil {
			ctx.Logger().Warn("retention prune failed",
				"destination", dest.Name, "err", err.Error())
		}
	}

	// 6) Success — record the row after all observable work is complete.
	successful, err := finish("success", "", uploadSize, uploadSHA, key, manifestJSON, encrypted)
	if err != nil {
		_ = writer.Delete(opCtx, key)
		return nil, err
	}
	return successful, nil
}

// streamSnapshot copies an app-authorized platform snapshot into dst. The SDK
// owns authentication and keeps the response streaming; Backup never receives
// an administrator API key and never calls the management route directly.
func streamSnapshot(ctx context.Context, appCtx *sdk.AppCtx, dst io.Writer) (int64, error) {
	api, err := platformBackupAPI(appCtx)
	if err != nil {
		return 0, err
	}
	reader, err := api.OpenPlatformSnapshot(ctx)
	if err != nil {
		return 0, normalizePlatformBackupError(err)
	}
	defer reader.Close()
	return io.Copy(dst, reader)
}

func writeSnapshot(opCtx context.Context, ctx *sdk.AppCtx, dst io.Writer, scope Scope) (int64, string, error) {
	if scope.Kind == "" || scope.Kind == "platform" {
		n, err := streamSnapshot(opCtx, ctx, dst)
		return n, "", err
	}
	return streamProviderSnapshot(opCtx, ctx, dst, scope)
}

func platformBackupAPI(ctx *sdk.AppCtx) (sdk.PlatformBackupClient, error) {
	if ctx == nil || ctx.PlatformBackupAPI() == nil {
		return nil, errors.New(platformBackupUnsupportedMessage)
	}
	return ctx.PlatformBackupAPI(), nil
}

func normalizePlatformBackupError(err error) error {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "http 404") {
		return errors.New(platformBackupUnsupportedMessage)
	}
	return err
}

type providerSnapshotResponse struct {
	ArchiveURL string          `json:"archive_url"`
	Manifest   json.RawMessage `json:"manifest"`
}

func streamProviderSnapshot(opCtx context.Context, ctx *sdk.AppCtx, dst io.Writer, scope Scope) (int64, string, error) {
	if ctx == nil {
		return 0, "", fmt.Errorf("app context required for %s backup", scope.Kind)
	}
	tool, err := providerSnapshotTool(scope)
	if err != nil {
		return 0, "", err
	}
	args := map[string]any{
		"scope_kind":         scope.Kind,
		"scope_id":           scope.ID,
		"supports_streaming": true,
	}
	if scope.Kind == "fleet_tenant" {
		args["tenant_id"] = scope.ID
	}
	var resp providerSnapshotResponse
	if err := ctx.PlatformAPI().CallAppResult(scope.SourceApp, tool, args, &resp); err != nil {
		return 0, "", fmt.Errorf("%s.%s: %w", scope.SourceApp, tool, err)
	}
	manifest := ""
	if len(resp.Manifest) > 0 && string(resp.Manifest) != "null" {
		manifest = string(resp.Manifest)
	}
	if resp.ArchiveURL != "" {
		if err := validateProviderStreamURL(resp.ArchiveURL); err != nil {
			return 0, "", err
		}
		req, err := http.NewRequestWithContext(opCtx, http.MethodGet, resp.ArchiveURL, nil)
		if err != nil {
			return 0, "", err
		}
		response, err := providerHTTPClient().Do(req)
		if err != nil {
			return 0, "", fmt.Errorf("download provider snapshot: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			return 0, "", fmt.Errorf("download provider snapshot: %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
		}
		n, err := io.Copy(dst, response.Body)
		return n, manifest, err
	}
	return 0, "", fmt.Errorf("%s.%s does not support streaming snapshots; upgrade the provider app", scope.SourceApp, tool)
}

func validateProviderStreamURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("invalid provider stream URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	if u.Scheme == "http" && (host == "localhost" || net.ParseIP(host).IsLoopback()) {
		return nil
	}
	return fmt.Errorf("provider stream URL must use HTTPS")
}

func providerHTTPClient() *http.Client {
	return &http.Client{Transport: platformTransferClient.Transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many provider stream redirects")
		}
		return validateProviderStreamURL(req.URL.String())
	}}
}

func providerSnapshotTool(scope Scope) (string, error) {
	switch scope.Kind {
	case "fleet_tenant":
		if scope.SourceApp != "fleet" {
			return "", fmt.Errorf("fleet_tenant source_app must be fleet, got %q", scope.SourceApp)
		}
		return "fleet_tenant_snapshot", nil
	default:
		return "", fmt.Errorf("unsupported backup scope %q", scope.Kind)
	}
}

func providerRestoreTool(scope Scope) (string, error) {
	switch scope.Kind {
	case "fleet_tenant":
		if scope.SourceApp != "fleet" {
			return "", fmt.Errorf("fleet_tenant source_app must be fleet, got %q", scope.SourceApp)
		}
		return "fleet_tenant_restore", nil
	default:
		return "", fmt.Errorf("unsupported restore scope %q", scope.Kind)
	}
}

// validateSnapshotArchive reads the entire gzip/tar stream, verifies its
// checksum/trailer, and returns the required bounded manifest.json. A digest
// alone only proves stored bytes were unchanged; this pass proves those bytes
// are structurally restorable before a run is marked successful.
func validateSnapshotArchive(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	manifest := ""
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if h.Name != "manifest.json" {
			continue
		}
		if manifest != "" {
			return "", errors.New("snapshot contains multiple manifest.json entries")
		}
		if h.Size < 0 || h.Size > maxManifestBytes {
			return "", fmt.Errorf("manifest.json is too large: %d bytes", h.Size)
		}
		bs, err := io.ReadAll(io.LimitReader(tr, maxManifestBytes+1))
		if err != nil {
			return "", err
		}
		if len(bs) > maxManifestBytes || !json.Valid(bs) {
			return "", errors.New("manifest.json is invalid")
		}
		manifest = string(bs)
	}
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return "", fmt.Errorf("gzip trailer: %w", err)
	}
	if manifest == "" {
		return "", errors.New("snapshot missing manifest.json")
	}
	return manifest, nil
}

// buildRemoteKey produces a deterministic, sortable key per run.
// Format: apteva-<YYYYMMDD>-<HHMMSS>.tar.gz under destination's
// optional KeyPrefix. The runner falls back to a default if startedAt
// isn't parseable.
func buildRemoteKey(run *Run, encrypted bool) string {
	t, err := time.Parse(time.RFC3339Nano, run.StartedAt)
	if err != nil {
		t = time.Now().UTC()
	}
	ext := ".tar.gz"
	if encrypted {
		ext += ".age"
	}
	return fmt.Sprintf("%sapteva-%s-run-%d%s", storagePrefix(run.Scope, run.PolicyID), t.UTC().Format("20060102-150405.000000000"), run.ID, ext)
}

func storagePrefix(scope Scope, policyID int64) string {
	kind := safeKeySegment(scope.Kind)
	if kind == "" {
		kind = "platform"
	}
	prefix := kind + "/"
	if scope.ID != "" {
		prefix += safeKeySegment(scope.ID) + "/"
	}
	if policyID > 0 {
		return prefix + fmt.Sprintf("policy-%d/", policyID)
	}
	return prefix + "adhoc/"
}

func safeKeySegment(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), ".")
}

// pruneRetention deletes the oldest runs on the destination beyond
// `keep` newest, both in object storage and in the runs table.
//
// We compare against the destination's actual List() — not the runs
// table — so that pruning still works after a database reset (the
// objects are the source of truth for "what's on the destination").
func pruneRetention(ctx context.Context, app *sdk.AppCtx, w Destination_writer, d *Destination, policy *Policy) error {
	prefix := storagePrefix(policy.Scope, policy.ID)
	objects, err := w.List(ctx, prefix)
	if err != nil {
		return err
	}
	// Filter to apteva-*.tar.gz so we don't accidentally delete files
	// the operator put in the same bucket.
	filtered := objects[:0]
	for _, o := range objects {
		if strings.HasPrefix(o.Key, prefix) && strings.HasPrefix(filepathBase(o.Key), "apteva-") &&
			(strings.HasSuffix(o.Key, ".tar.gz") || strings.HasSuffix(o.Key, ".tar.gz.age")) {
			filtered = append(filtered, o)
		}
	}
	if len(filtered) <= policy.RetentionKeep {
		return nil
	}
	// List() returns newest-first, so anything past `keep` is old.
	for _, o := range filtered[policy.RetentionKeep:] {
		if err := w.Delete(ctx, o.Key); err != nil {
			app.Logger().Warn("retention delete failed", "key", o.Key, "err", err.Error())
			continue
		}
		// Also clear the matching runs row so the UI history stops
		// showing a key that no longer exists. We match on
		// (destination_id, remote_key) not (id) — the rows came from a
		// previous run that may not even be in this DB anymore.
		_, _ = app.AppDB().Exec(
			`DELETE FROM runs WHERE destination_id = ? AND remote_key = ?`, d.ID, o.Key)
	}
	return nil
}

// defaultLocalBackupDir is the path used when a kindLocal destination
// has no explicit Path. We root it under the install's data dir,
// which the platform creates and guarantees writable — that's where
// every other piece of per-install state already lives.
//
// Returns "" only if the SDK didn't provide a data dir (shouldn't
// happen in production; openDestination handles it as an error).
func defaultLocalBackupDir(ctx *sdk.AppCtx) string {
	if ctx == nil {
		return ""
	}
	dd := ctx.DataDir()
	if dd == "" {
		return ""
	}
	return filepath.Join(dd, "backups")
}

// filepathBase is filepath.Base inlined to avoid pulling the import
// in this file (linting nit; cheap to write).
func filepathBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// scheduleViaJobs / cancelViaJobs proxy through the platform's
// app-to-app call. Lives in this file because the cron registration
// is a runner-adjacent concern (every policy creation needs it).
//
// callerProjectID is the operator's currently-selected project from
// the dashboard URL — used as _project_id so the scheduled job
// lands in that project's Jobs panel. Backup is scope:global so
// its own ctx has no project, but the operator's view does.
func scheduleViaJobs(ctx *sdk.AppCtx, p *Policy, callerProjectID string) error {
	body := map[string]any{
		"name": "backup-policy-" + fmt.Sprint(p.ID),
		// jobs_schedule expects {schedule: {kind, cron}}; the older
		// "cron" top-level field was rejected as "schedule required".
		"schedule": map[string]any{
			"kind": "cron",
			"cron": p.Schedule,
		},
		"target": map[string]any{
			"kind": "app_tool",
			"app":  "backup",
			"tool": "backup_now",
			// Jobs' app-to-app request has a shorter deadline than a large
			// snapshot. Queue the durable run and return immediately; the run
			// row and Backup history remain the source of execution status.
			"input": map[string]any{"policy_id": p.ID, "async": true},
		},
		"idempotency_key": fmt.Sprintf("backup-policy-%d", p.ID),
		"owner_app":       "backup",
		// Tag the cron job with the operator's currently-selected
		// project so it shows up in that project's Jobs panel.
		// Backup is scope:global and its sidecar has no APTEVA_PROJECT_ID,
		// but the dashboard sends ?project_id=<pid> on every call and
		// we forward it here. Empty (""), with the present-empty
		// semantics that jobs >= 0.1.8 accepts, marks the job as
		// "global / no project" — a safe fallback for tool-driven
		// scheduling that didn't supply one.
		"_project_id": callerProjectID,
	}
	// jobs_schedule returns the job's id as a number — record it as a
	// string in our policies.jobs_id (TEXT column) and convert back to
	// int64 in cancelViaJobs.
	var resp struct {
		Job struct {
			ID int64 `json:"id"`
		} `json:"job"`
	}
	if err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_schedule", body, &resp); err != nil {
		return fmt.Errorf("jobs_schedule: %w", err)
	}
	if resp.Job.ID == 0 {
		return fmt.Errorf("jobs returned no id")
	}
	jobsID := strconv.FormatInt(resp.Job.ID, 10)
	if _, err := ctx.AppDB().Exec(`UPDATE policies SET jobs_id = ?, jobs_project_id = ? WHERE id = ?`, jobsID, callerProjectID, p.ID); err != nil {
		if cancelErr := cancelViaJobs(ctx, jobsID, callerProjectID); cancelErr != nil {
			return fmt.Errorf("store jobs id: %v; cancel orphan job: %w", err, cancelErr)
		}
		return fmt.Errorf("store jobs id: %w", err)
	}
	p.JobsID = jobsID
	p.JobsProjectID = callerProjectID
	return nil
}

func cancelViaJobs(ctx *sdk.AppCtx, jobsID, callerProjectID string) error {
	id, err := strconv.ParseInt(jobsID, 10, 64)
	if err != nil {
		return fmt.Errorf("bad jobs_id %q: %w", jobsID, err)
	}
	_, err = ctx.PlatformAPI().CallApp("jobs", "jobs_cancel", map[string]any{
		"id":          id,
		"_project_id": callerProjectID,
	})
	return err
}
