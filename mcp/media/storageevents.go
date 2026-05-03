package main

// SSE subscriber for storage's app-event stream. Lets media react to
// file.deleted events instantly instead of waiting up to 30s for the
// indexer's purgeOrphans sweep.
//
// Architecture
//
//   media boot (OnMount)
//     └─► startStorageEventSubscriber goroutine
//           └─► open SSE: GET <PUBLIC_URL>/api/app-events/storage?project_id=...&since=...
//                 └─► parse `id:` + `data:` lines
//                       └─► dispatch by topic:
//                             - file.deleted → cascadeDeleteFromEvent
//                             - other topics → ignored
//                 └─► on disconnect: backoff + reconnect with last seq as since
//
// The platform's appbus.go ring buffers 256 recent events per
// (app, project), so a brief disconnect replays everything we
// missed. Longer outages fall back to the indexer's purgeOrphans
// sweep — the safety net we deliberately kept.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// startStorageEventSubscriber spawns the SSE reader. Returns
// immediately — the goroutine runs until app.Done() fires.
func startStorageEventSubscriber(app *sdk.AppCtx) {
	go runStorageEventSubscriber(app)
	app.Logger().Info("storage event subscriber started")
}

func runStorageEventSubscriber(app *sdk.AppCtx) {
	log := app.Logger()
	publicURL := strings.TrimRight(os.Getenv("APTEVA_PUBLIC_URL"), "/")
	if publicURL == "" {
		log.Warn("storage event subscriber: APTEVA_PUBLIC_URL not set; reverting to indexer-poll-only delete cleanup")
		return
	}
	projectID := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
	if projectID == "" {
		log.Info("storage event subscriber: no APTEVA_PROJECT_ID; skipping")
		return
	}
	token := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if token == "" {
		token = os.Getenv("APTEVA_APP_TOKEN")
	}
	if token == "" {
		log.Warn("storage event subscriber: no install token; cannot subscribe")
		return
	}

	var lastSeq uint64
	backoff := 1 * time.Second
	const backoffCap = 30 * time.Second

	for {
		select {
		case <-app.Done():
			return
		default:
		}

		err := connectAndStream(app, publicURL, projectID, token, &lastSeq)
		if errors.Is(err, context.Canceled) || app.Done() == nil {
			return
		}
		select {
		case <-app.Done():
			return
		default:
		}

		// Jittered backoff. Don't hammer the platform on a flapping
		// connection; the periodic indexer sweep covers correctness.
		jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
		wait := backoff + jitter
		log.Info("storage event subscriber disconnected; reconnecting", "after", wait, "err", err)
		select {
		case <-app.Done():
			return
		case <-time.After(wait):
		}
		backoff *= 2
		if backoff > backoffCap {
			backoff = backoffCap
		}
	}
}

// connectAndStream opens the SSE connection and processes events
// until the connection drops. Updates *sinceSeq as it goes so a
// reconnect resumes correctly.
func connectAndStream(app *sdk.AppCtx, publicURL, projectID, token string, sinceSeq *uint64) error {
	url := fmt.Sprintf("%s/api/app-events/storage?project_id=%s&since=%d",
		publicURL, projectID, *sinceSeq)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-app.Done()
		cancel()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")

	// Long timeout per-conn — SSE is supposed to be open for the long
	// haul. Disconnects come from network, not our timer.
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("subscribe HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	app.Logger().Info("storage event stream connected", "since", *sinceSeq)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var (
		dataLine string
		idLine   string
	)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			// End of event frame. Process accumulated data.
			if dataLine != "" {
				if seq, err := strconv.ParseUint(idLine, 10, 64); err == nil {
					*sinceSeq = seq
				}
				handleStorageEvent(app, []byte(dataLine))
			}
			dataLine, idLine = "", ""
		case strings.HasPrefix(line, "data: "):
			dataLine = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, "id: "):
			idLine = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, ":"):
			// Comment / heartbeat — platform sends `: ping` every 15s.
		}
	}
	return scanner.Err()
}

// handleStorageEvent decodes one event and dispatches it. Only
// file.deleted is interesting today; other topics are ignored.
func handleStorageEvent(app *sdk.AppCtx, raw []byte) {
	var ev struct {
		Topic     string         `json:"topic"`
		App       string         `json:"app"`
		ProjectID string         `json:"project_id"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		app.Logger().Warn("storage event decode failed", "err", err, "raw", string(raw))
		return
	}
	if ev.App != "storage" {
		return
	}
	switch ev.Topic {
	case "file.deleted":
		cascadeDeleteFromEvent(app, ev.Data, ev.ProjectID)
	case "file.added":
		indexFromEvent(app, ev.Data, ev.ProjectID)
	}
}

// indexFromEvent triggers a single-file indexing pass off the
// file.added event payload. Skips when:
//   - the upload was dedup-resolved (was_existing=true) — the
//     existing row didn't change, no point re-probing
//   - the file lives under /.media/ — our own derivations
//   - content_type / extension isn't a media type
//
// The actual probe + thumbnail + waveform work happens in
// indexOneFile; this is just the event-payload → StorageFile
// adapter.
func indexFromEvent(app *sdk.AppCtx, data map[string]any, projectID string) {
	if existed, _ := data["was_existing"].(bool); existed {
		return
	}
	if projectID == "" {
		projectID = strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
	}
	if projectID == "" {
		return
	}

	f := storageFileFromEvent(data)
	if f == nil {
		return
	}

	// Spawn a goroutine: indexOneFile downloads bytes, runs ffprobe,
	// generates derivations — all of which can take seconds. We
	// don't want to block the SSE reader from picking up the next
	// event while one file is being processed.
	go indexOneFile(context.Background(), app, projectID, *f)
}

// storageFileFromEvent reconstructs a StorageFile from the event's
// data payload. Returns nil on missing required fields.
func storageFileFromEvent(data map[string]any) *StorageFile {
	idVal, ok := data["id"]
	if !ok {
		return nil
	}
	var id int64
	switch v := idVal.(type) {
	case float64:
		id = int64(v)
	case string:
		var err error
		id, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil
		}
	default:
		return nil
	}
	if id == 0 {
		return nil
	}
	out := &StorageFile{ID: id}
	if v, ok := data["name"].(string); ok {
		out.Name = v
	}
	if v, ok := data["folder"].(string); ok {
		out.Folder = v
	}
	if v, ok := data["content_type"].(string); ok {
		out.ContentType = v
	}
	if v, ok := data["sha256"].(string); ok {
		out.SHA256 = v
	}
	if v, ok := data["size_bytes"].(float64); ok {
		out.SizeBytes = int64(v)
	}
	if v, ok := data["visibility"].(string); ok {
		out.Visibility = v
	}
	return out
}

// cascadeDeleteFromEvent extracts the file id from the storage event
// payload and runs the same cascade logic the indexer's orphan
// sweep does — but for one row, immediately. Also hard-deletes the
// derivation blobs (thumbnail / waveform) from storage so they don't
// linger as orphan bytes.
func cascadeDeleteFromEvent(app *sdk.AppCtx, data map[string]any, projectID string) {
	log := app.Logger()
	if projectID == "" {
		projectID = strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
	}
	if projectID == "" {
		return
	}
	idVal, ok := data["id"]
	if !ok {
		return
	}
	var fileID string
	switch v := idVal.(type) {
	case float64:
		fileID = strconv.FormatInt(int64(v), 10)
	case string:
		fileID = v
	default:
		return
	}
	if fileID == "" {
		return
	}

	// Lookup our row + its derivations BEFORE deleting; we'll need
	// the derivations.storage_file_id list to clean their blobs.
	row, err := getMedia(app.AppDB(), projectID, fileID)
	if err != nil {
		// notFound is the common case — storage delete fired for a
		// non-media file (a doc, a render output we already cleaned, etc).
		return
	}

	for _, d := range row.Derivations {
		if d.StorageFileID == "" {
			continue
		}
		// Hard-delete the thumbnail/waveform blob in storage. Best-
		// effort: storage may already be in the process of cleaning
		// (cascading delete cascade), or the derivation file may
		// already be gone — both fine.
		sid, err := strconv.ParseInt(d.StorageFileID, 10, 64)
		if err != nil {
			continue
		}
		if err := deleteStorageFile(app, projectID, sid); err != nil {
			log.Info("derivation blob delete failed (probably already gone)",
				"file_id", fileID, "kind", d.Kind, "storage_file_id", d.StorageFileID, "err", err)
		}
	}

	// Cascade the DB rows. purgeOrphans takes a current-storage list
	// and deletes media rows NOT in it; we synthesise the inverse —
	// pass everything BUT this one file_id. Cleaner approach: a
	// targeted helper.
	if err := cascadeDeleteOne(app.AppDB(), projectID, fileID); err != nil {
		log.Warn("cascade delete failed", "file_id", fileID, "err", err)
		return
	}
	log.Info("cascade-deleted media row from storage event",
		"file_id", fileID, "derivations", len(row.Derivations))
	app.Emit("media.deleted", map[string]any{"file_id": fileID})
}

// deleteStorageFile calls storage's HTTP DELETE for one file, with
// the gateway URL + bearer plumbing.
func deleteStorageFile(app *sdk.AppCtx, projectID string, fileID int64) error {
	gw := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if gw == "" {
		return errors.New("APTEVA_GATEWAY_URL not set")
	}
	token := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if token == "" {
		token = os.Getenv("APTEVA_APP_TOKEN")
	}
	url := fmt.Sprintf("%s/api/apps/storage/files/%d?project_id=%s",
		gw, fileID, projectID)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
