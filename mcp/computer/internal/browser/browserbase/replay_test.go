package browserbase

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apteva/apps/mcp/computer/internal/browser/replay"
)

func TestReplayResolverNormalizesMultitabMetadataAndForwardsPlaylist(t *testing.T) {
	const apiKey = "bb-secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-BB-API-Key"); got != apiKey {
			t.Fatalf("X-BB-API-Key = %q", got)
		}
		switch r.URL.Path {
		case "/sessions/provider-1/replays":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"pages":[{"pageId":"0","url":"/v1/sessions/provider-1/replays/0","startTimeMs":0,"endTimeMs":1200},{"pageId":"1","url":"/v1/sessions/provider-1/replays/1","startTimeMs":250,"endTimeMs":1200}],"pageCount":2}`))
		case "/sessions/provider-1/replays/1":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\nhttps://cdn.browserbase.test/segment.ts?signature=signed\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	previous := apiBase
	apiBase = server.URL
	t.Cleanup(func() { apiBase = previous })

	resolver := NewReplayResolver(apiKey)
	metadata, err := resolver.Metadata(context.Background(), "provider-1")
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if !metadata.Supported || metadata.Status != "ready" || len(metadata.Streams) != 2 {
		t.Fatalf("metadata = %+v", metadata)
	}
	if got := metadata.Streams[1]; got.ID != "1" || got.StartMS != 250 || got.EndMS != 1200 {
		t.Fatalf("second stream = %+v", got)
	}

	playlist, contentType, err := resolver.Playlist(context.Background(), "provider-1", "1")
	if err != nil {
		t.Fatalf("Playlist: %v", err)
	}
	if contentType != "application/vnd.apple.mpegurl" {
		t.Fatalf("content type = %q", contentType)
	}
	if got := string(playlist); !strings.Contains(got, "signature=signed") || strings.Contains(got, apiKey) {
		t.Fatalf("playlist = %q", got)
	}
}

func TestReplayResolverMapsNotFound(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	previous := apiBase
	apiBase = server.URL
	t.Cleanup(func() { apiBase = previous })

	_, err := NewReplayResolver("key").Metadata(context.Background(), "missing")
	if !errors.Is(err, replay.ErrNotFound) {
		t.Fatalf("Metadata error = %v", err)
	}
}
