package main

// Restore — pulls a past run's bytes back from its destination and
// POSTs them to /api/platform/restore. The platform handles the
// actual swap (live for app DBs, staged-for-next-boot for the
// platform DB itself).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

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
	dest, err := dbGetDestination(ctx.AppDB(), run.DestinationID)
	if err != nil {
		return nil, fmt.Errorf("destination %d for run %d: %w", run.DestinationID, runID, err)
	}
	writer, err := openDestination(dest, makeConnAdapter(ctx))
	if err != nil {
		return nil, fmt.Errorf("open destination: %w", err)
	}

	dlCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	body, err := writer.Get(dlCtx, run.RemoteKey)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", run.RemoteKey, err)
	}
	defer body.Close()

	// We need a Content-Length on the POST so the server's middleware /
	// proxy doesn't choke. Buffer to memory — even a 1 GB compressed
	// snapshot fits comfortably in a server's RAM, and the restore is
	// a rare-use operation. If this becomes a problem, switch to a
	// chunked-encoding POST and confirm the server middleware copes.
	buf, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	report, err := postRestore(buf)
	if err != nil {
		return nil, err
	}
	return report, nil
}

func postRestore(body []byte) (map[string]any, error) {
	gateway := os.Getenv("APTEVA_GATEWAY_URL")
	if gateway == "" {
		return nil, fmt.Errorf("APTEVA_GATEWAY_URL not set")
	}
	token := os.Getenv("APTEVA_APP_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("APTEVA_APP_TOKEN not set")
	}
	req, err := http.NewRequest("POST",
		strings.TrimRight(gateway, "/")+"/api/platform/restore",
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("X-Confirm-Restore", "yes")
	req.ContentLength = int64(len(body))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("restore endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var report map[string]any
	if err := json.Unmarshal(respBody, &report); err != nil {
		return nil, fmt.Errorf("decode restore report: %w", err)
	}
	return report, nil
}
