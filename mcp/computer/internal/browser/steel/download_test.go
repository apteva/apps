package steel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestOpenDownloadedFileUsesSteelSessionFilesAPI(t *testing.T) {
	oldBase := apiBase
	t.Cleanup(func() { apiBase = oldBase })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/sessions/session-1/files/nested/report%20name.zip" || r.Header.Get("Steel-Api-Key") != "secret" {
			http.Error(w, "wrong request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "steel bytes")
	}))
	t.Cleanup(server.Close)
	apiBase = server.URL
	c := &Computer{apiKey: "secret", sessionID: "session-1", http: server.Client()}
	reader, err := c.openDownloadedFile(context.Background(), "/files/nested/report name.zip", computer.Download{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(got) != "steel bytes" {
		t.Fatalf("bytes=%q err=%v", got, err)
	}
}

func TestOpenDownloadedFileRejectsPathsOutsideSteelDownloadDirectory(t *testing.T) {
	c := &Computer{}
	for _, unsafe := range []string{"/tmp/report.zip", "/files/../secret", "files/nested/../../secret", ""} {
		if _, err := c.openDownloadedFile(context.Background(), unsafe, computer.Download{}); err == nil || !strings.Contains(err.Error(), "invalid provider path") {
			t.Fatalf("path %q was not rejected: %v", unsafe, err)
		}
	}
}
