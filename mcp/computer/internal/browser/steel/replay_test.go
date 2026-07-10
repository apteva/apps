package steel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apteva/apps/mcp/computer/internal/browser/replay"
)

func TestReplayResolverAuthenticatesAndSignsProviderResources(t *testing.T) {
	const apiKey = "steel-secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Steel-Api-Key"); got != apiKey {
			t.Fatalf("Steel-Api-Key = %q", got)
		}
		switch r.URL.Path {
		case "/v1/sessions/provider-1/hls":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\nchild.m3u8\nhttps://cdn.steel.test/signed.ts?sig=ok\n"))
		case "/v1/sessions/provider-1/child.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\nsegment.ts\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	previous := apiBase
	apiBase = server.URL + "/v1"
	t.Cleanup(func() { apiBase = previous })

	resolver := NewReplayResolver(apiKey)
	metadata, err := resolver.Metadata(context.Background(), "provider-1")
	if err != nil || metadata.Status != "ready" || len(metadata.Streams) != 1 || metadata.Streams[0].ID != "0" {
		t.Fatalf("Metadata = %+v, %v", metadata, err)
	}

	playlist, _, err := resolver.Playlist(context.Background(), "provider-1", "0")
	if err != nil {
		t.Fatalf("Playlist: %v", err)
	}
	childURL := server.URL + "/v1/sessions/provider-1/child.m3u8"
	if !strings.Contains(string(playlist), childURL) {
		t.Fatalf("relative child URL was not normalized: %q", playlist)
	}
	token, err := resolver.SignResource("provider-1", childURL)
	if err != nil {
		t.Fatalf("SignResource: %v", err)
	}
	if strings.Contains(token, apiKey) || strings.Contains(token, childURL) {
		t.Fatalf("resource token exposes provider data: %q", token)
	}
	resource, contentType, err := resolver.Resource(context.Background(), "provider-1", token)
	if err != nil {
		t.Fatalf("Resource: %v", err)
	}
	if contentType != "application/vnd.apple.mpegurl" || !strings.Contains(string(resource), server.URL+"/v1/sessions/provider-1/segment.ts") {
		t.Fatalf("resource = %q content-type=%q", resource, contentType)
	}
	if _, _, err := resolver.Resource(context.Background(), "provider-1", token+"tampered"); err == nil {
		t.Fatal("tampered token was accepted")
	}
	if _, err := resolver.SignResource("provider-1", "https://cdn.steel.test/signed.ts"); !errors.Is(err, replay.ErrExternalResource) {
		t.Fatalf("external URL error = %v", err)
	}
}
