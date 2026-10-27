package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Opt-in only: use the existing production integration as a read proxy. All
// Social state lives in testkit's temporary DB; no sidecar workers are started,
// no OAuth credentials are copied, and provider mutation tools are refused.
type liveHistoryPlatform struct {
	*recordingPlatform
	base, key  string
	connection int64
	calls      int
}

func (p *liveHistoryPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	switch tool {
	case "get_my_channel", "get_video", "list_playlist_items":
	default:
		return nil, fmt.Errorf("live test refuses provider operation %q", tool)
	}
	p.calls++
	if p.calls > 100 {
		return nil, fmt.Errorf("live read request budget exhausted")
	}
	body, _ := json.Marshal(map[string]any{"tool": tool, "input": input})
	request, err := http.NewRequest("POST", fmt.Sprintf("%s/api/connections/%d/execute", p.base, p.connection), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+p.key)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("live read transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("live read gateway HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Success bool            `json:"success"`
		Status  int             `json:"status"`
		Data    json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	return &sdk.ExecuteResult{Success: envelope.Success, Status: envelope.Status, Data: envelope.Data}, nil
}
func TestLiveYouTubeHistoryReadOnly(t *testing.T) {
	if os.Getenv("SOCIAL_LIVE_YOUTUBE") != "1" {
		t.Skip("opt-in read-only production integration test")
	}
	path := os.Getenv("SOCIAL_LIVE_CONFIG")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".apteva-prod", "apteva.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("live config unavailable")
	}
	var config struct {
		Base string `json:"base_url"`
		Key  string `json:"api_key"`
	}
	if json.Unmarshal(raw, &config) != nil || config.Key == "" || config.Base == "" {
		t.Fatal("invalid live config")
	}
	conn, err := strconv.ParseInt(os.Getenv("SOCIAL_LIVE_CONNECTION"), 10, 64)
	if err != nil || conn <= 0 {
		t.Fatal("explicit SOCIAL_LIVE_CONNECTION required")
	}
	pf := &liveHistoryPlatform{recordingPlatform: newRecordingPlatform(), base: config.Base, key: config.Key, connection: conn}
	ctx := newSocialCtx(t, pf)
	channel, err := historyExecute(ctx, historyAccount{destinationAccount: destinationAccount{ConnID: 42}}, "get_my_channel", map[string]any{"part": "id"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := historyItems(channel, "items")
	if err != nil || len(items) != 1 {
		t.Fatal("owned channel could not be selected")
	}
	owner := historyString(items[0], "id")
	if owner == "" {
		t.Fatal("owned channel ID unavailable")
	}
	account := historyAccountFixture(t, ctx, owner)
	r := finishHistory(t, ctx, account)
	if (r.Status != "complete" && r.Status != "limited") || r.UniquePosts == 0 || r.CompletedAt == nil {
		t.Fatalf("live history did not complete: %+v", r)
	}
	var total, private, unlisted, filenames int
	_ = ctx.AppDB().QueryRow(`SELECT count(*),sum(json_extract(metadata,'$.visibility')='private'),sum(json_extract(metadata,'$.visibility')='unlisted'),sum(json_extract(metadata,'$.original_filename') IS NOT NULL) FROM social_native_posts WHERE social_account_id=?`, account).Scan(&total, &private, &unlisted, &filenames)
	var nativeID string
	_ = ctx.AppDB().QueryRow(`SELECT native_post_id FROM social_native_posts WHERE social_account_id=? AND json_extract(metadata,'$.title') IS NOT NULL ORDER BY native_post_id LIMIT 1`, account).Scan(&nativeID)
	result, err := (&App{}).toolPostNativeInspect(ctx, map[string]any{"social_account_id": account, "native_post_id": nativeID})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["status"] != "ok" {
		t.Fatal("native readback failed")
	}
	out := reportList(t, ctx, map[string]any{"social_account_id": account, "native_post_id": nativeID, "compact": true})
	if out["total"] != 1 {
		t.Fatal("live native ID search failed")
	}
	var scheduled int
	_ = ctx.AppDB().QueryRow(`SELECT count(*) FROM posts WHERE status IN ('scheduled','publishing')`).Scan(&scheduled)
	if scheduled != 0 || len(pf.callAppCalls) != 0 {
		t.Fatal("live history created publishing work")
	}
	t.Logf("Read-only YouTube history verified in temporary DB: %d distinct videos (%d private, %d unlisted), %d original filenames, %d provider reads, 0 publishing jobs; coverage=%s, limitations=%v", total, private, unlisted, filenames, pf.calls, r.Status, r.Limitations)
}
