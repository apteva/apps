package navigation

import "testing"

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
