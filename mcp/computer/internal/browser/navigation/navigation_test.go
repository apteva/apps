package navigation

import (
	"context"
	"strings"
	"testing"
)

func TestUsableURL(t *testing.T) {
	for _, raw := range []string{"https://example.com/path", "http://localhost:8080/", "data:text/html,ok", "file:///tmp/page.html"} {
		if !UsableURL(raw) {
			t.Errorf("UsableURL(%q) = false", raw)
		}
	}
	for _, raw := range []string{"", "about:blank", "chrome-error://chromewebdata/", "https://"} {
		if UsableURL(raw) {
			t.Errorf("UsableURL(%q) = true", raw)
		}
	}
}

func TestRunRejectsInvalidNavigationBeforeCDP(t *testing.T) {
	if err := Run(context.Background(), "navigate", "not a URL", 0); err == nil || !strings.Contains(err.Error(), "invalid navigation URL") {
		t.Fatalf("invalid URL: got %v", err)
	}
	if err := Run(context.Background(), "forward", "", 0); err == nil || !strings.Contains(err.Error(), "unsupported navigation action") {
		t.Fatalf("unsupported action: got %v", err)
	}
}
