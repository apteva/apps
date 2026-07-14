package browserbase

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
)

func TestPickInitialPageTargetReusesExistingContentPage(t *testing.T) {
	infos := []*target.Info{
		{TargetID: "worker", Type: "service_worker", URL: "https://example.test/sw.js"},
		{TargetID: "blank", Type: "page", URL: "about:blank"},
		{TargetID: "prerender", Type: "page", Subtype: "prerender", URL: "https://future.test"},
		{TargetID: "content", Type: "page", URL: "https://example.test/app"},
	}
	if got := pickInitialPageTarget(infos); got != target.ID("content") {
		t.Fatalf("pickInitialPageTarget = %q, want content", got)
	}
}

func TestPickInitialPageTargetKeepsProviderBlankPage(t *testing.T) {
	infos := []*target.Info{{TargetID: "provider-page", Type: "page", URL: "about:blank"}}
	if got := pickInitialPageTarget(infos); got != target.ID("provider-page") {
		t.Fatalf("pickInitialPageTarget = %q, want provider-page", got)
	}
	if got := pickInitialPageTarget(nil); got != "" {
		t.Fatalf("pickInitialPageTarget(nil) = %q", got)
	}
}

func TestActionTimeoutsAreBounded(t *testing.T) {
	cases := map[string]time.Duration{
		"click":         clickActionTimeout,
		"double_click":  clickActionTimeout,
		"type":          textActionTimeout,
		"key":           keyActionTimeout,
		"scroll":        scrollActionTimeout,
		"wait":          waitActionTimeout,
		"navigate":      navigateActionTimeout,
		"back":          navigateActionTimeout,
		"reload":        navigateActionTimeout,
		"upload_file":   30 * time.Second,
		"select_option": 20 * time.Second,
		"set_checked":   20 * time.Second,
		"set_temporal":  20 * time.Second,
	}
	for action, want := range cases {
		if got := actionTimeout(action); got != want {
			t.Fatalf("actionTimeout(%q): want %s, got %s", action, want, got)
		}
	}
}

func TestSleepWithContextReportsActionTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	err := sleepWithContext(ctx, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "action_timeout") {
		t.Fatalf("sleepWithContext timeout: want action_timeout, got %v", err)
	}
}

func TestUploadSessionFileUsesBrowserbaseUploadsAPI(t *testing.T) {
	dir := t.TempDir()
	localFile := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(localFile, []byte("image bytes"), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	var sawPath, sawAPIKey, sawFilename, sawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		sawAPIKey = r.Header.Get("X-BB-API-Key")
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer file.Close()
		raw, _ := io.ReadAll(file)
		sawFilename = header.Filename
		sawBody = string(raw)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "ok"})
	}))
	defer srv.Close()

	prev := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = prev })

	c := &Computer{
		apiKey:    "bb_key",
		sessionID: "bb_session",
		http:      srv.Client(),
	}
	remote, err := c.uploadSessionFile(localFile)
	if err != nil {
		t.Fatalf("uploadSessionFile: %v", err)
	}
	if remote != "/tmp/.uploads/photo.jpg" {
		t.Fatalf("remote path: got %q", remote)
	}
	if sawPath != "/sessions/bb_session/uploads" || sawAPIKey != "bb_key" {
		t.Fatalf("request path/key: path=%q key=%q", sawPath, sawAPIKey)
	}
	if sawFilename != "photo.jpg" || sawBody != "image bytes" {
		t.Fatalf("multipart file: filename=%q body=%q", sawFilename, sawBody)
	}
}
