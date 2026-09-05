package browserbase

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestOpenDownloadedFileUsesBrowserbaseDownloadsAPI(t *testing.T) {
	oldBase := apiBase
	t.Cleanup(func() { apiBase = oldBase })
	payload := "browserbase bytes"
	listCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-BB-API-Key") != "secret" {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/downloads":
			listCalls++
			if r.URL.Query().Get("sessionId") != "session-1" || r.URL.Query().Get("filename") != "report name.zip" {
				http.Error(w, "wrong filters", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if listCalls < 3 {
				_, _ = io.WriteString(w, `{"downloads":[],"total":0}`)
				return
			}
			_, _ = io.WriteString(w, `{"downloads":[{"id":"provider-download","sessionId":"session-1","filename":"report name.zip","size":17}],"total":1}`)
		case "/downloads/provider-download":
			if r.Header.Get("Accept") != "application/octet-stream" {
				http.Error(w, "wrong accept", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, payload)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	apiBase = server.URL
	c := &Computer{apiKey: "secret", sessionID: "session-1", http: server.Client(), downloadProviderIDs: map[string]string{}}
	meta := computer.Download{ID: "dl_opaque", Filename: "report name.zip", Size: int64(len(payload)), Status: computer.DownloadCompleted}
	for range 2 {
		reader, err := c.openDownloadedFile(context.Background(), "ignored", meta)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil || string(got) != payload {
			t.Fatalf("bytes=%q err=%v", got, err)
		}
	}
	if listCalls != 3 {
		t.Fatalf("provider id mapping should be cached, list calls=%d", listCalls)
	}
}

func TestBrowserbaseDownloadRejectsCrossSessionCandidate(t *testing.T) {
	oldBase := apiBase
	t.Cleanup(func() { apiBase = oldBase })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"downloads":[{"id":"wrong","sessionId":"another-session","filename":"same.zip","size":3}]}`)
	}))
	t.Cleanup(server.Close)
	apiBase = server.URL
	c := &Computer{apiKey: "secret", sessionID: "session-1", http: server.Client(), downloadProviderIDs: map[string]string{}}
	_, err := c.browserbaseDownloadID(context.Background(), computer.Download{ID: "dl_local", Filename: "same.zip", Size: 3})
	if err == nil || !strings.Contains(err.Error(), "not yet synchronized") {
		t.Fatalf("cross-session file should not resolve: %v", err)
	}
}

func TestBrowserbaseDownloadAmbiguityNeverGuessesByRetrievalOrder(t *testing.T) {
	old := apiBase
	t.Cleanup(func() { apiBase = old })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filename") != "original/name.zip" {
			t.Errorf("lost original filename: %s", r.URL.RawQuery)
		}
		io.WriteString(w, `{"downloads":[{"id":"first","filename":"original/name.zip","size":3},{"id":"second","filename":"original/name.zip","size":3}]}`)
	}))
	defer server.Close()
	apiBase = server.URL
	c := &Computer{sessionID: "s", http: server.Client()}
	for _, id := range []string{"newer", "older"} {
		_, err := c.browserbaseDownloadID(context.Background(), computer.Download{ID: id, Filename: "name.zip", OriginalFilename: "original/name.zip", Size: 3})
		if err == nil || !strings.Contains(err.Error(), "download_identity_ambiguous") {
			t.Fatalf("guessed identity for %s: %v", id, err)
		}
	}
	if len(c.downloadProviderIDs) != 0 {
		t.Fatal("ambiguous mappings were cached")
	}
}
