package local

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

// TestLocalBrowserDownloadsLive proves that Computer exports the bytes Chrome
// actually received. It covers ordinary, authenticated, POST-generated, and
// blob: downloads through the same lifecycle used by the MCP adapter.
func TestLocalBrowserDownloadsLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_DOWNLOAD_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_DOWNLOAD_TESTS=1")
	}
	zipPayload := zipFixture(t, "bid.txt", []byte("browser-only bid package"))
	postPayload := []byte("POST-generated export")
	blobPayload := []byte("blob-download")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.SetCookie(w, &http.Cookie{Name: "download_auth", Value: "allowed", Path: "/", HttpOnly: true})
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><body>
<a id="get" download href="/get">Download ZIP</a>
<a id="auth" download href="/auth">Download authenticated ZIP</a>
<form method="POST" action="/post"><input type="hidden" name="nonce" value="from-page"><button id="post" type="submit">Generate POST export</button></form>
<button id="blob">Generate blob export</button>
<script>document.getElementById('blob').addEventListener('click', function () {
  const a = document.createElement('a');
  a.download = 'blob-result.txt';
  a.href = URL.createObjectURL(new Blob(['blob-download'], {type:'text/plain'}));
  document.body.appendChild(a); a.click(); a.remove();
});</script></body></html>`)
		case "/get":
			writeAttachment(w, "fixture.zip", "application/zip", zipPayload)
		case "/auth":
			cookie, err := r.Cookie("download_auth")
			if err != nil || cookie.Value != "allowed" {
				http.Error(w, "missing browser cookie", http.StatusUnauthorized)
				return
			}
			writeAttachment(w, "fixture.zip", "application/zip", zipPayload)
		case "/post":
			if r.Method != http.MethodPost || r.FormValue("nonce") != "from-page" {
				http.Error(w, "invalid browser POST", http.StatusBadRequest)
				return
			}
			writeAttachment(w, "post-result.txt", "text/plain", postPayload)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	c, err := New(computer.DisplaySize{Width: 900, Height: 650})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.OpenSession(computer.OpenOptions{URL: server.URL}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		selector string
		wantName string
		want     []byte
	}{
		{name: "GET", selector: "#get", wantName: "fixture.zip", want: zipPayload},
		{name: "authenticated", selector: "#auth", wantName: "fixture.zip", want: zipPayload},
		{name: "POST", selector: "#post", wantName: "post-result.txt", want: postPayload},
		{name: "blob", selector: "#blob", wantName: "blob-result.txt", want: blobPayload},
	}
	seenIDs := map[string]bool{}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cursor := c.DownloadEventCursor()
			if _, err := c.Execute(computer.Action{Type: "click", Selector: test.selector}); err != nil {
				t.Fatalf("click %s: %v", test.selector, err)
			}
			started := waitForDownloadStart(t, c, cursor)
			if seenIDs[started.ID] {
				t.Fatalf("download id was reused: %s", started.ID)
			}
			seenIDs[started.ID] = true
			if started.Filename != test.wantName || (started.Status != computer.DownloadInProgress && started.Status != computer.DownloadCompleted) {
				t.Fatalf("start metadata: %+v", started)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			completed, err := c.WaitForDownload(ctx, started.ID)
			if err != nil || completed.Status != computer.DownloadCompleted {
				t.Fatalf("completion=%+v err=%v", completed, err)
			}
			reader, meta, err := c.OpenDownload(ctx, started.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(reader)
			_ = reader.Close()
			if readErr != nil || !bytes.Equal(got, test.want) {
				t.Fatalf("browser bytes mismatch: got=%q err=%v", got, readErr)
			}
			if meta.SHA256 == "" || meta.Size != int64(len(test.want)) {
				t.Fatalf("integrity metadata missing: %+v", meta)
			}
		})
	}

	listed, err := c.ListDownloads(context.Background())
	if err != nil || len(listed) != len(cases) {
		t.Fatalf("list downloads=%+v err=%v", listed, err)
	}
	if listed[0].Filename == listed[1].Filename && listed[0].ID == listed[1].ID {
		t.Fatalf("duplicate filenames were conflated: %+v", listed[:2])
	}
}

func waitForDownloadStart(t *testing.T, c *Computer, cursor uint64) computer.Download {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if started := c.DownloadsStartedSince(cursor); len(started) > 0 {
			return started[0]
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("browser did not emit downloadWillBegin")
	return computer.Download{}
}

func writeAttachment(w http.ResponseWriter, filename, contentType string, payload []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
	_, _ = w.Write(payload)
}

func zipFixture(t *testing.T, name string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	entry, err := writer.Create(strings.TrimSpace(name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
