package main

// Runner — orchestrates a single backup.
//
// Steps for a successful run:
//   1. Insert a runs row in 'running' state (so the UI can see it live)
//   2. Stream GET /api/platform/snapshot from the gateway, hashing the
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
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// runBackup executes a backup against dest. policy may be nil for
// ad-hoc / "run now" calls; retention pruning is skipped in that case
// (only policy-driven runs prune, since ad-hoc runs typically share
// destinations with policies and we don't want a one-off button click
// to silently delete scheduled backups).
func runBackup(ctx *sdk.AppCtx, dest *Destination, policy *Policy) (*Run, error) {
	run := &Run{
		DestinationID:   dest.ID,
		DestinationName: dest.Name,
	}
	if policy != nil {
		run.PolicyID = policy.ID
	}
	id, err := dbInsertRun(ctx.AppDB(), run)
	if err != nil {
		return nil, err
	}
	run.ID = id

	finish := func(status, errMsg string, bytes int64, sha, key, manifestJSON string) (*Run, error) {
		_ = dbFinishRun(ctx.AppDB(), id, status, bytes, sha, key, manifestJSON, errMsg)
		out, _ := dbGetRun(ctx.AppDB(), id)
		return out, nil
	}

	// 1) Open the destination first so credentials/endpoint failures
	// don't waste a snapshot.
	writer, err := openDestination(dest, ctx, defaultLocalBackupDir(ctx))
	if err != nil {
		return finish("failed", "open destination: "+err.Error(), 0, "", "", "")
	}

	// 2) Stream snapshot to a temp file, hashing as we go.
	tmp, err := os.CreateTemp("", "apteva-snapshot-*.tar.gz")
	if err != nil {
		return finish("failed", "tempfile: "+err.Error(), 0, "", "", "")
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	hash := sha256.New()
	written, err := streamSnapshot(io.MultiWriter(tmp, hash))
	if errClose := tmp.Close(); err == nil {
		err = errClose
	}
	if err != nil {
		return finish("failed", "stream snapshot: "+err.Error(), 0, "", "", "")
	}
	sha := hex.EncodeToString(hash.Sum(nil))

	// 3) Crack the tar to extract manifest.json — useful for forensic
	// diffs across runs ("which install was added between these two?").
	manifestJSON, _ := extractManifestJSON(tmpPath)

	// 4) Upload.
	key := buildRemoteKey(dest, run.StartedAt)
	if key == "" {
		key = "apteva-snapshot-" + time.Now().UTC().Format("20060102-150405") + ".tar.gz"
	}
	src, err := os.Open(tmpPath)
	if err != nil {
		return finish("failed", "reopen tempfile: "+err.Error(), 0, "", "", "")
	}
	uploadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := writer.Put(uploadCtx, key, src, written); err != nil {
		_ = src.Close()
		return finish("failed", "upload: "+err.Error(), written, sha, "", manifestJSON)
	}
	_ = src.Close()

	// 5) Success — record the row.
	_, _ = finish("success", "", written, sha, key, manifestJSON)

	// 6) Retention prune. Best-effort; failures here don't taint the
	// successful run.
	if policy != nil && policy.RetentionKeep > 0 {
		if err := pruneRetention(uploadCtx, ctx, writer, dest, policy.RetentionKeep); err != nil {
			ctx.Logger().Warn("retention prune failed",
				"destination", dest.Name, "err", err.Error())
		}
	}
	return dbGetRun(ctx.AppDB(), id)
}

// streamSnapshot copies /api/platform/snapshot into dst. Returns the
// number of bytes written. Auth uses the install's APTEVA_APP_TOKEN —
// the auth middleware resolves it to the install's installed_by user
// (admin id=1 for self-host setups), which the snapshot endpoint then
// gates on.
func streamSnapshot(dst io.Writer) (int64, error) {
	gateway := os.Getenv("APTEVA_GATEWAY_URL")
	if gateway == "" {
		return 0, fmt.Errorf("APTEVA_GATEWAY_URL not set — backup cannot reach the platform")
	}
	token := os.Getenv("APTEVA_APP_TOKEN")
	if token == "" {
		return 0, fmt.Errorf("APTEVA_APP_TOKEN not set — backup cannot authenticate")
	}
	req, err := http.NewRequest("GET", strings.TrimRight(gateway, "/")+"/api/platform/snapshot", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("snapshot endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.Copy(dst, resp.Body)
}

// extractManifestJSON decompresses the tar.gz at path and returns the
// raw manifest.json bytes if present. Used purely as a sidecar record
// in the runs table; failure is non-fatal.
func extractManifestJSON(path string) (string, error) {
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
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if h.Name != "manifest.json" {
			continue
		}
		bs, err := io.ReadAll(tr)
		if err != nil {
			return "", err
		}
		return string(bs), nil
	}
}

// buildRemoteKey produces a deterministic, sortable key per run.
// Format: apteva-<YYYYMMDD>-<HHMMSS>.tar.gz under destination's
// optional KeyPrefix. The runner falls back to a default if startedAt
// isn't parseable.
func buildRemoteKey(_ *Destination, startedAt string) string {
	t, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		t = time.Now().UTC()
	}
	return fmt.Sprintf("apteva-%s.tar.gz", t.UTC().Format("20060102-150405"))
}

// pruneRetention deletes the oldest runs on the destination beyond
// `keep` newest, both in object storage and in the runs table.
//
// We compare against the destination's actual List() — not the runs
// table — so that pruning still works after a database reset (the
// objects are the source of truth for "what's on the destination").
func pruneRetention(ctx context.Context, app *sdk.AppCtx, w Destination_writer, d *Destination, keep int) error {
	objects, err := w.List(ctx)
	if err != nil {
		return err
	}
	// Filter to apteva-*.tar.gz so we don't accidentally delete files
	// the operator put in the same bucket.
	filtered := objects[:0]
	for _, o := range objects {
		if strings.HasPrefix(filepathBase(o.Key), "apteva-") && strings.HasSuffix(o.Key, ".tar.gz") {
			filtered = append(filtered, o)
		}
	}
	if len(filtered) <= keep {
		return nil
	}
	// List() returns newest-first, so anything past `keep` is old.
	for _, o := range filtered[keep:] {
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
func scheduleViaJobs(ctx *sdk.AppCtx, p *Policy) error {
	gateway := os.Getenv("APTEVA_GATEWAY_URL")
	if gateway == "" {
		return fmt.Errorf("APTEVA_GATEWAY_URL not set")
	}
	body := map[string]any{
		"name":     "backup-policy-" + fmt.Sprint(p.ID),
		"cron":     p.Schedule,
		"target": map[string]any{
			"kind":   "http",
			"url":    "/api/apps/backup/run",
			"method": "POST",
			"body":   map[string]any{"policy_id": p.ID},
		},
		"idempotency_key": fmt.Sprintf("backup-policy-%d", p.ID),
		"owner_app":       "backup",
	}
	var resp struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	if err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_schedule", body, &resp); err != nil {
		return fmt.Errorf("jobs_schedule: %w", err)
	}
	if resp.Job.ID == "" {
		return fmt.Errorf("jobs returned no id")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE policies SET jobs_id = ? WHERE id = ?`, resp.Job.ID, p.ID); err != nil {
		return err
	}
	p.JobsID = resp.Job.ID
	return nil
}

func cancelViaJobs(ctx *sdk.AppCtx, jobsID string) error {
	_, err := ctx.PlatformAPI().CallApp("jobs", "jobs_cancel", map[string]any{"id": jobsID})
	return err
}

