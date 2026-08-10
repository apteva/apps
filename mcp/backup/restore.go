package main

// Restore — pulls a past run's bytes back from its destination and
// POSTs them to /api/platform/restore. The platform handles the
// actual swap (live for app DBs, staged-for-next-boot for the
// platform DB itself).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func restoreFromRun(ctx *sdk.AppCtx, runID int64) (map[string]any, error) {
	run, err := dbGetRun(ctx.AppDB(), runID)
	if err != nil {
		return nil, err
	}
	if run.Status != "success" {
		return nil, fmt.Errorf("run %d has status %q — only successful runs can be restored", runID, run.Status)
	}
	if run.RemoteKey == "" {
		return nil, fmt.Errorf("run %d has no remote_key — destination did not return one", runID)
	}
	release, err := acquireOperation("restore")
	if err != nil {
		return nil, err
	}
	defer release()
	opCtx, cancelOperation := context.WithTimeout(context.Background(), transferTimeout)
	defer cancelOperation()
	dest, err := dbGetDestination(ctx.AppDB(), run.DestinationID)
	if err != nil {
		return nil, fmt.Errorf("destination %d for run %d: %w", run.DestinationID, runID, err)
	}
	writer, err := openDestination(dest, ctx, defaultLocalBackupDir(ctx))
	if err != nil {
		return nil, fmt.Errorf("open destination: %w", err)
	}

	body, err := writer.Get(opCtx, run.RemoteKey)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", run.RemoteKey, err)
	}
	defer body.Close()
	stored, err := os.CreateTemp("", "apteva-restore-object-*")
	if err != nil {
		return nil, err
	}
	storedPath := stored.Name()
	defer os.Remove(storedPath)
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(stored, hash), contextReader{ctx: opCtx, r: body}); err != nil {
		_ = stored.Close()
		return nil, fmt.Errorf("download %s: %w", run.RemoteKey, err)
	}
	if err := stored.Close(); err != nil {
		return nil, err
	}
	actualSHA := hex.EncodeToString(hash.Sum(nil))
	if run.SHA256 != "" && !strings.EqualFold(actualSHA, run.SHA256) {
		return nil, fmt.Errorf("backup integrity check failed: expected sha256 %s, got %s", run.SHA256, actualSHA)
	}
	var restoreBody io.ReadCloser
	if run.Encrypted || strings.HasSuffix(run.RemoteKey, ".age") {
		restoreBody, err = openDecryptedSnapshot(ctx, storedPath)
		if err != nil {
			return nil, err
		}
	} else {
		restoreBody, err = os.Open(storedPath)
		if err != nil {
			return nil, err
		}
	}
	defer restoreBody.Close()
	if run.Scope.Kind != "" && run.Scope.Kind != "platform" {
		return restoreProviderRunStream(opCtx, ctx, run, restoreBody)
	}
	restoreSize := int64(-1)
	if file, ok := restoreBody.(*os.File); ok {
		info, statErr := file.Stat()
		if statErr != nil {
			return nil, statErr
		}
		restoreSize = info.Size()
	}
	report, err := postRestoreReader(opCtx, restoreBody, restoreSize)
	if err != nil {
		return nil, err
	}
	return report, nil
}

func restoreProviderRunStream(opCtx context.Context, ctx *sdk.AppCtx, run *Run, body io.Reader) (map[string]any, error) {
	tool, err := providerRestoreTool(run.Scope)
	if err != nil {
		return nil, err
	}
	args := map[string]any{
		"scope_kind":     run.Scope.Kind,
		"scope_id":       run.Scope.ID,
		"prepare_stream": true,
	}
	if run.Scope.Kind == "fleet_tenant" {
		args["tenant_id"] = run.Scope.ID
	}
	var prepared struct {
		UploadURL string `json:"upload_url"`
	}
	if err := ctx.PlatformAPI().CallAppResult(run.Scope.SourceApp, tool, args, &prepared); err != nil {
		return nil, fmt.Errorf("%s.%s prepare stream: %w", run.Scope.SourceApp, tool, err)
	}
	if prepared.UploadURL == "" {
		return nil, fmt.Errorf("%s.%s does not support streaming restores; upgrade the provider app", run.Scope.SourceApp, tool)
	}
	if err := validateProviderStreamURL(prepared.UploadURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(opCtx, http.MethodPost, prepared.UploadURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/gzip")
	resp, err := providerHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("stream provider restore: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("stream provider restore: %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("decode provider restore report: %w", err)
	}
	return annotateRestoreReport(report), nil
}

func postRestore(body []byte) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), transferTimeout)
	defer cancel()
	return postRestoreReader(ctx, bytes.NewReader(body), int64(len(body)))
}

func postRestoreReader(ctx context.Context, body io.Reader, size int64) (map[string]any, error) {
	gateway := os.Getenv("APTEVA_GATEWAY_URL")
	if gateway == "" {
		return nil, fmt.Errorf("APTEVA_GATEWAY_URL not set")
	}
	token := os.Getenv("APTEVA_APP_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("APTEVA_APP_TOKEN not set")
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(gateway, "/")+"/api/platform/restore",
		body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("X-Confirm-Restore", "yes")
	if size >= 0 {
		req.ContentLength = size
	}

	resp, err := platformTransferClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if readErr != nil {
		return nil, fmt.Errorf("read restore report: %w", readErr)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("restore endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var report map[string]any
	if err := json.Unmarshal(respBody, &report); err != nil {
		return nil, fmt.Errorf("decode restore report: %w", err)
	}
	return annotateRestoreReport(report), nil
}

func annotateRestoreReport(report map[string]any) map[string]any {
	if report == nil {
		return report
	}
	textValue := func(value any) string {
		if value == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(value))
	}
	failures := []string{}
	if installs, ok := report["installs"].([]any); ok {
		for _, raw := range installs {
			entry, ok := raw.(map[string]any)
			if !ok || !strings.EqualFold(textValue(entry["status"]), "error") {
				continue
			}
			label := "install"
			if installID := textValue(entry["install_id"]); installID != "" {
				label += " " + installID
			}
			if path := textValue(entry["archive_path"]); path != "" {
				label = path
			}
			if note := textValue(entry["note"]); note != "" {
				label += ": " + note
			}
			failures = append(failures, label)
		}
	}
	if status := strings.ToLower(textValue(report["status"])); status == "error" || status == "failed" {
		message := textValue(report["error"])
		if message == "" {
			message = "restore reported " + status
		}
		failures = append(failures, message)
	}
	if len(failures) > 0 {
		report["partial_failure"] = true
		report["failures"] = failures
		report["failure_count"] = len(failures)
	} else {
		report["partial_failure"] = false
		report["failure_count"] = 0
	}
	return report
}
