package local

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestLocalNavigationLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_NAVIGATION_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_NAVIGATION_TESTS=1")
	}
	url := os.Getenv("COMPUTER_NAVIGATION_TEST_URL")
	if url == "" {
		url = "https://example.com/"
	}
	c, err := New(computer.DisplaySize{Width: 1600, Height: 800})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.OpenSession(computer.OpenOptions{URL: url}); err != nil {
		t.Fatal(err)
	}
	shot, err := c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(shot) == 0 || c.CurrentURL() == "" {
		t.Fatalf("empty browser result: url=%q bytes=%d", c.CurrentURL(), len(shot))
	}
	targets := c.LastSetOfMark()
	for _, target := range targets {
		t.Logf("label=%d role=%s text=%q", target.Label, target.Role, target.Text)
	}
	if clickText := strings.ToLower(strings.TrimSpace(os.Getenv("COMPUTER_NAVIGATION_CLICK_TEXT"))); clickText != "" {
		clicked := false
		for _, target := range targets {
			if strings.Contains(strings.ToLower(target.Text), clickText) {
				if _, err := c.Execute(computer.Action{Type: "click", Label: target.Label}); err != nil {
					t.Fatal(err)
				}
				clicked = true
				break
			}
		}
		if !clicked {
			t.Fatalf("target containing %q not found", clickText)
		}
		t.Logf("post-click url=%s som=%d", c.CurrentURL(), len(c.LastSetOfMark()))
	}
	if rawAmount := strings.TrimSpace(os.Getenv("COMPUTER_NAVIGATION_SCROLL_AMOUNT")); rawAmount != "" {
		amount, err := strconv.Atoi(rawAmount)
		if err != nil {
			t.Fatalf("invalid COMPUTER_NAVIGATION_SCROLL_AMOUNT %q: %v", rawAmount, err)
		}
		if _, err := c.Execute(computer.Action{Type: "scroll", Direction: "down", Amount: amount}); err != nil {
			t.Fatal(err)
		}
		targets = c.LastSetOfMark()
		for _, target := range targets {
			t.Logf("post-scroll label=%d role=%s text=%q", target.Label, target.Role, target.Text)
		}
	}
	for _, target := range c.LastSetOfMark() {
		if target.W < 4 || target.H < 4 {
			t.Fatalf("unusable clipped SOM target after navigation actions: %+v", target)
		}
	}
	if expectedText := strings.ToLower(strings.TrimSpace(os.Getenv("COMPUTER_NAVIGATION_EXPECT_TEXT"))); expectedText != "" {
		for _, target := range c.LastSetOfMark() {
			if strings.Contains(strings.ToLower(target.Text), expectedText) {
				return
			}
		}
		t.Fatalf("target containing %q not found after navigation actions", expectedText)
	}
	t.Logf("url=%s bytes=%d som=%d", c.CurrentURL(), len(shot), len(targets))
}

func TestLocalCrossOriginIframeSOMLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_NAVIGATION_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_NAVIGATION_TESTS=1")
	}
	var clicked atomic.Bool
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/clicked" {
			clicked.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<button onclick="fetch('/clicked')" style="margin:40px;width:220px;height:50px">Proceed to checkout</button>`))
	}))
	defer child.Close()
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<iframe tabindex="0" style="margin:100px 0 0 200px;width:700px;height:200px" src="` + child.URL + `"></iframe>`))
	}))
	defer parent.Close()

	c, err := New(computer.DisplaySize{Width: 1000, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.OpenSession(computer.OpenOptions{URL: parent.URL}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: true}); err != nil {
		t.Fatal(err)
	}
	for _, target := range c.LastSetOfMark() {
		if strings.Contains(strings.ToLower(target.Text), "proceed to checkout") {
			if target.X < 240 || target.Y < 140 {
				t.Fatalf("cross-frame coordinates were not translated: %+v", target)
			}
			if _, err := c.Execute(computer.Action{Type: "click", Label: target.Label}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for !clicked.Load() && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if !clicked.Load() {
				t.Fatal("translated cross-frame label did not click the child button")
			}
			return
		}
	}
	t.Fatalf("cross-origin iframe button missing from SOM: %+v", c.LastSetOfMark())
}

func TestLocalModalSuppressesOverlappingBackgroundControlLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_NAVIGATION_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_NAVIGATION_TESTS=1")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`
<button style="position:absolute;left:350px;top:250px;width:300px;height:60px">Background search</button>
<div role="dialog" style="position:absolute;left:300px;top:180px;width:400px;height:220px;background:white;border:1px solid black">
  <button style="margin:70px;width:220px;height:50px">Sign in or register</button>
</div>`))
	}))
	defer server.Close()
	c, err := New(computer.DisplaySize{Width: 1000, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.OpenSession(computer.OpenOptions{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: true}); err != nil {
		t.Fatal(err)
	}
	var modalButton bool
	for _, target := range c.LastSetOfMark() {
		if strings.Contains(target.Text, "Background search") {
			t.Fatalf("obscured background control received a SOM label: %+v", target)
		}
		if strings.Contains(target.Text, "Sign in or register") {
			modalButton = true
		}
	}
	if !modalButton {
		t.Fatalf("modal action missing from SOM: %+v", c.LastSetOfMark())
	}
}

func TestLocalOversizedRoleDialogKeepsPageControlsLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_NAVIGATION_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_NAVIGATION_TESTS=1")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`
<aside role="dialog" style="position:absolute;left:8px;top:-328px;width:262px;height:7811px">
  <a href="#filter" style="position:absolute;top:700px">Price filter</a>
</aside>
<button style="position:fixed;left:20px;top:-23.8px;width:80px;height:24px">Clipped size</button>
<main style="margin-left:275px">
  <a href="#product" style="display:block;width:300px;height:80px">Gala heel product</a>
</main>`))
	}))
	defer server.Close()
	c, err := New(computer.DisplaySize{Width: 1600, Height: 800})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.OpenSession(computer.OpenOptions{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: true}); err != nil {
		t.Fatal(err)
	}
	var productFound bool
	for _, target := range c.LastSetOfMark() {
		if target.W < 4 || target.H < 4 {
			t.Fatalf("clipped control received an unusable SOM label: %+v", target)
		}
		if strings.Contains(target.Text, "Gala heel product") {
			productFound = true
		}
	}
	if !productFound {
		t.Fatalf("page control was suppressed by oversized role=dialog: %+v", c.LastSetOfMark())
	}
}

func TestLocalClosedShadowConsentControlsLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_NAVIGATION_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_NAVIGATION_TESTS=1")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`
<a href="#">Customise cookie choices</a><div id="host"></div>
<script>
const root = document.querySelector('#host').attachShadow({mode:'closed'});
root.innerHTML = '<div role="dialog" style="position:absolute;left:100px;top:200px;width:700px;height:200px;background:white"><button style="margin:80px;width:180px;height:40px">Accept</button><button style="width:180px;height:40px">Decline</button></div>';
</script>`))
	}))
	defer server.Close()
	c, err := New(computer.DisplaySize{Width: 1000, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.OpenSession(computer.OpenOptions{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: true}); err != nil {
		t.Fatal(err)
	}
	var accept, decline bool
	for _, target := range c.LastSetOfMark() {
		switch target.Text {
		case "Accept":
			accept = true
		case "Decline":
			decline = true
		}
	}
	if !accept || !decline {
		t.Fatalf("closed-shadow consent controls missing: accept=%v decline=%v targets=%+v", accept, decline, c.LastSetOfMark())
	}
}
