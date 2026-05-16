//go:build integration

package main

// Tier 2 — the real binary, real HTTP. Boots the sidecar and drives it
// over MCP + REST: validates manifest parsing at boot, on-disk
// migrations, JSON-RPC dispatch, route mounting, /health, the public
// RSS route, and the auth boundary end-to-end.
//
// No dependency sidecars are spawned — episode_set_audio (the only
// path that needs storage + media) is covered by the Tier 1 stub in
// handlers_test.go. These tests stick to the surface that boots
// standalone.
//
// Run with:  go test -tags integration ./...

import (
	"strconv"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_BootsAndHealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	if resp := sc.GET("/health", &got); resp.Status != 200 {
		t.Fatalf("/health status = %d", resp.Status)
	}
	if got["ok"] != true {
		t.Errorf("/health body = %v", got)
	}
}

func TestSidecar_ShowEpisodeAndFeedFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// Create a show via MCP.
	showRes := sc.MCP("show_create", map[string]any{
		"title":       "Sidecar Show",
		"author":      "Marco",
		"owner_email": "marco@example.com",
		"category":    "Technology",
	})
	show := showRes["show"].(map[string]any)
	showID := show["id"].(float64)
	slug := show["slug"].(string)
	if slug == "" {
		t.Fatal("show_create returned no slug")
	}

	// Add an episode and confirm it lists.
	sc.MCP("episode_create", map[string]any{
		"show_id": showID,
		"title":   "Episode One",
	})
	list := sc.MCP("episode_list", map[string]any{"show_id": showID})
	if list["count"].(float64) != 1 {
		t.Errorf("episode_list count = %v, want 1", list["count"])
	}

	// The public RSS route serves XML — no auth token required.
	resp := sc.GET("/feed/"+slug+".xml", nil)
	if resp.Status != 200 {
		t.Fatalf("GET /feed/%s.xml status = %d body = %s", slug, resp.Status, resp.Body)
	}
	body := string(resp.Body)
	if !strings.Contains(body, `<rss version="2.0"`) {
		t.Errorf("feed is not RSS 2.0:\n%s", body)
	}
	if !strings.Contains(body, "<title>Sidecar Show</title>") {
		t.Errorf("feed missing show title:\n%s", body)
	}

	// feed_validate flags the unpublished/audio-less episode.
	v := sc.MCP("feed_validate", map[string]any{"show_id": showID})
	if v["ok"].(bool) {
		t.Error("feed_validate should report ok=false (episode has no audio, none published)")
	}
}

func TestSidecar_DownloadRedirectMissingEpisodeIs404(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	resp := sc.GET("/e/no-such-guid-"+strconv.Itoa(1), nil)
	if resp.Status != 404 {
		t.Errorf("unknown /e/{guid} status = %d, want 404", resp.Status)
	}
}
