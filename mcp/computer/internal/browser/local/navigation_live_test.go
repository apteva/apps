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
	"github.com/chromedp/chromedp"
)

// TestLocalGuardedPublishClickLive reproduces the Patreon failure shape with a
// real Chromium page: a spinner-only Publish button is initially loading, and
// the dangerous coordinate is exactly where an imagined scheduling caret
// would be. The test proves SoM state, stability waiting, semantic mismatch
// rejection, dangerous-coordinate confirmation, and a final guarded real click.
func TestLocalGuardedPublishClickLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_GUARDED_CLICK_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_GUARDED_CLICK_TESTS=1")
	}
	var published atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/published" {
			published.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><style>
body{font-family:system-ui;margin:0}.bar{height:72px;display:flex;justify-content:flex-end;align-items:center;padding:0 32px;background:#111}
button{width:150px;height:42px;border:0;border-radius:8px;background:white;color:#111;font-weight:700}.spinner{display:inline-block;width:18px;height:18px;border:3px solid #bbb;border-top-color:#111;border-radius:50%}
</style><body><div class="bar"><button id="publish">Publish</button></div>
<main style="padding:32px"><h1>Create a post</h1><label>Title <input id="title" placeholder="Post title"></label><p id="status">Autosaving draft…</p></main>
<script>
window.startLoading=function(){var b=document.getElementById('publish');b.disabled=true;b.setAttribute('aria-busy','true');b.setAttribute('data-loading','true');b.innerHTML='<span class="spinner" role="progressbar" aria-label="Saving"></span>';};
window.releaseLoading=function(){var b=document.getElementById('publish');b.disabled=false;b.removeAttribute('aria-busy');b.removeAttribute('data-loading');b.innerHTML='Publish';document.getElementById('status').textContent='Draft saved';};
document.getElementById('publish').addEventListener('click',function(){fetch('/published',{method:'POST'});});
</script></body></html>`))
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
	var stablePublish computer.SetOfMarkTarget
	for _, target := range c.LastSetOfMark() {
		if target.AccessibleName == "Publish" {
			stablePublish = target
			break
		}
	}
	if stablePublish.ID == "" {
		t.Fatalf("stable Publish target missing identity: %+v", c.LastSetOfMark())
	}
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(`window.startLoading()`, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: true}); err != nil {
		t.Fatal(err)
	}
	var publish computer.SetOfMarkTarget
	for _, target := range c.LastSetOfMark() {
		if target.AccessibleName == "Publish" {
			publish = target
			break
		}
	}
	if publish.Label == 0 || !publish.Disabled || !publish.Loading || !publish.Dangerous || publish.DestructiveEffect != "immediate_publish" {
		t.Fatalf("loading Publish SoM target missing state: %+v all=%+v", publish, c.LastSetOfMark())
	}
	if publish.ID != stablePublish.ID {
		t.Fatalf("loading replacement lost stable target identity: before=%+v after=%+v", stablePublish, publish)
	}
	if _, err := c.Execute(computer.Action{Type: "wait_for_stable", QuietMS: 300, TimeoutMS: 500}); err == nil || !strings.Contains(err.Error(), "did not stabilize") {
		t.Fatalf("loading page reported stable: %v", err)
	}
	x, y := publish.X+publish.W/2, publish.Y+publish.H/2
	if _, err := c.Execute(computer.Action{Type: "click", X: x, Y: y, ExpectedText: "Schedule", GuardDangerousCoordinate: true}); err == nil || !strings.Contains(err.Error(), "loading") {
		t.Fatalf("loading publish coordinate was not rejected: %v", err)
	}
	if published.Load() != 0 {
		t.Fatal("loading Publish control was activated")
	}
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(`window.releaseLoading()`, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute(computer.Action{Type: "wait_for_stable", QuietMS: 300, TimeoutMS: 3000}); err != nil {
		t.Fatalf("wait_for_stable: %v", err)
	}
	publish = computer.SetOfMarkTarget{}
	var title computer.SetOfMarkTarget
	for _, target := range c.LastSetOfMark() {
		if target.AccessibleName == "Publish" {
			publish = target
		}
		if target.AccessibleName == "Title" {
			title = target
		}
	}
	if publish.Label == 0 || publish.Disabled || publish.Loading {
		t.Fatalf("stable Publish SoM target has stale state: %+v all=%+v", publish, c.LastSetOfMark())
	}
	if title.Label == 0 {
		t.Fatalf("ordinary associated-label input missing from SoM: %+v", c.LastSetOfMark())
	}
	if _, err := c.Execute(computer.Action{Type: "click", Label: title.Label}); err != nil {
		t.Fatalf("ordinary associated-label click regressed: %v", err)
	}
	publish = computer.SetOfMarkTarget{}
	for _, target := range c.LastSetOfMark() {
		if target.AccessibleName == "Publish" {
			publish = target
			break
		}
	}
	x, y = publish.X+publish.W/2, publish.Y+publish.H/2
	if _, err := c.Execute(computer.Action{Type: "click", X: x, Y: y, ExpectedText: "Schedule", GuardDangerousCoordinate: true}); err == nil || !strings.Contains(err.Error(), `expected target "Schedule"`) {
		t.Fatalf("Schedule/Publish mismatch was not rejected: %v", err)
	}
	if _, err := c.Execute(computer.Action{Type: "click", X: x, Y: y, GuardDangerousCoordinate: true}); err == nil || !strings.Contains(err.Error(), "consequential target") {
		t.Fatalf("unconfirmed dangerous coordinate was not rejected: %v", err)
	}
	if published.Load() != 0 {
		t.Fatal("rejected coordinate activated Publish")
	}
	if _, err := c.Execute(computer.Action{Type: "click", Label: publish.Label}); err != nil {
		t.Fatalf("guarded label click: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for published.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if published.Load() != 1 {
		t.Fatalf("guarded Publish label did not dispatch exactly once: %d", published.Load())
	}
}

func TestLocalSOMRetainsSafetyControlsBeyondOrdinaryCapLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_GUARDED_CLICK_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_GUARDED_CLICK_TESTS=1")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><style>body{margin:0}button{width:110px;height:34px;margin:2px}</style><div id="ordinary"></div>
<button id="publish" aria-label="Publish">Publish</button><button id="schedule">Schedule</button><button id="withdraw">Withdraw funds</button>
<script>for(let i=0;i<60;i++){let b=document.createElement('button');b.textContent='Ordinary '+i;document.querySelector('#ordinary').appendChild(b)}</script>`))
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
	found := map[string]computer.SetOfMarkTarget{}
	for _, target := range c.LastSetOfMark() {
		found[target.AccessibleName] = target
	}
	for name, effect := range map[string]string{"Publish": "immediate_publish", "Schedule": "schedule_publish", "Withdraw funds": "financial_action"} {
		target, ok := found[name]
		if !ok || !target.Dangerous || target.DestructiveEffect != effect || target.ID == "" {
			t.Fatalf("safety target %q was truncated or incomplete: target=%+v all=%+v", name, target, c.LastSetOfMark())
		}
	}
}

func TestLocalSemanticNestedScrollAndRenderedTextLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_SEMANTIC_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_SEMANTIC_TESTS=1")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><style>
html,body{margin:0;height:100%;overflow:hidden;font-family:system-ui}.top{height:56px;display:flex;justify-content:flex-end;align-items:center;background:#111;padding:0 20px}.top button{height:36px;width:120px}.layout{height:544px;display:grid;grid-template-columns:1fr 320px;gap:12px}.pane{overflow-y:auto;border:1px solid #aaa;padding:12px}.spacer{height:700px}
</style><div class="top"><button id="publish">Publish</button></div><div class="layout">
<main id="editor" class="pane" contenteditable="true" role="textbox" aria-label="Post body"><p>Draft introduction</p><div class="spacer"></div><p>Editor footer</p></main>
<aside id="settings" class="pane" role="region" aria-label="Settings"><h2>Settings</h2><label>Audience <select><option>Everyone</option></select></label><div class="spacer"></div><button id="schedule">Set publish date</button></aside>
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

	var body, publish computer.SetOfMarkTarget
	for _, target := range c.LastSetOfMark() {
		switch target.AccessibleName {
		case "Post body":
			body = target
		case "Publish":
			publish = target
		}
	}
	if body.ID == "" || body.Dangerous {
		t.Fatalf("draft editor risk classification is wrong: %+v", body)
	}
	if !publish.Dangerous || publish.DestructiveEffect != "immediate_publish" {
		t.Fatalf("Publish lost risk classification: %+v", publish)
	}
	initialBodyID := body.ID
	var editorRegion, settingsRegion computer.ScrollRegion
	for _, region := range c.ScrollRegions() {
		switch region.Name {
		case "Post body":
			editorRegion = region
		case "Settings":
			settingsRegion = region
		}
	}
	if editorRegion.ID == "" || settingsRegion.ID == "" || editorRegion.ID == settingsRegion.ID {
		t.Fatalf("semantic scroll regions missing: %+v", c.ScrollRegions())
	}

	if _, err := c.Execute(computer.Action{Type: "scroll", Direction: "down", Amount: 420, TargetID: settingsRegion.ID, ExpectedName: "Post body", ExpectedRole: "region"}); err == nil || !strings.Contains(err.Error(), "expected target name") {
		t.Fatalf("intent/target mismatch was not rejected: %v", err)
	}
	if _, err := c.Execute(computer.Action{Type: "scroll", Direction: "down", Amount: 760, TargetID: settingsRegion.ID, ExpectedName: "Settings", ExpectedRole: "region"}); err != nil {
		t.Fatalf("settings scroll: %v", err)
	}
	result := c.LastScrollResult()
	if result == nil || !result.Moved || result.WrongTarget || result.ActualTargetID != settingsRegion.ID || result.DeltaY <= 0 {
		t.Fatalf("wrong scroll movement report: %+v", result)
	}
	var offsets struct {
		Editor   int `json:"editor"`
		Settings int `json:"settings"`
	}
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(`({editor:document.querySelector('#editor').scrollTop,settings:document.querySelector('#settings').scrollTop})`, &offsets)); err != nil {
		t.Fatal(err)
	}
	if offsets.Editor != 0 || offsets.Settings <= 0 {
		t.Fatalf("wrong region moved: %+v", offsets)
	}
	var schedule computer.SetOfMarkTarget
	for _, target := range c.LastSetOfMark() {
		if target.AccessibleName == "Set publish date" {
			schedule = target
		}
		if target.AccessibleName == "Post body" && target.ID != initialBodyID {
			t.Fatalf("stable body identity changed: before=%s after=%s", initialBodyID, target.ID)
		}
	}
	if schedule.ID == "" {
		t.Fatalf("newly revealed scheduling control missing: %+v", c.LastSetOfMark())
	}
	if _, err := c.Execute(computer.Action{Type: "scroll", Direction: "down", Amount: 300, TargetID: settingsRegion.ID, ExpectedName: "Settings"}); err != nil {
		t.Fatalf("boundary scroll: %v", err)
	}
	if boundary := c.LastScrollResult(); boundary == nil || boundary.Moved || !boundary.AtEnd {
		t.Fatalf("no-movement boundary report: %+v", boundary)
	}

	if _, err := c.Execute(computer.Action{Type: "set_text", Selector: "#editor", Text: "First paragraph.\n\nSecond paragraph.", NewlineMode: "preserve"}); err != nil {
		t.Fatalf("set rich text: %v", err)
	}
	textResult := c.LastTextResult()
	if textResult == nil || !textResult.Verified || textResult.RenderedText != "First paragraph.\n\nSecond paragraph." || len(textResult.Paragraphs) != 2 {
		t.Fatalf("rendered text readback mismatch: %+v", textResult)
	}
	var paragraphCount int
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(`document.querySelectorAll('#editor > p').length`, &paragraphCount)); err != nil {
		t.Fatal(err)
	}
	if paragraphCount != 2 {
		t.Fatalf("rich text was not rendered as two paragraphs: %d", paragraphCount)
	}
}

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

func TestLocalSelectorClickOffscreenDigiloLink(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_SELECTOR_CLICK_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_SELECTOR_CLICK_TESTS=1")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/clicked" {
			_, _ = w.Write([]byte(`<html><body>Digilo click received</body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<html><style>html { scroll-behavior: smooth; }</style><body style="margin:0">
<div style="height:2200px">Digilo review content</div>
<a href="https://go.marcoschwartz.com/digilo" target="_blank">Visit Digilo</a>
<script>
document.querySelector('a[href="https://go.marcoschwartz.com/digilo"]').addEventListener('click', function(event) {
  event.preventDefault();
  window.location.href = '/clicked';
});
</script></body></html>`))
	}))
	defer server.Close()

	c, err := New(computer.DisplaySize{Width: 1280, Height: 720})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.OpenSession(computer.OpenOptions{URL: server.URL}); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Execute(computer.Action{Type: "click", Selector: `a[`}); err == nil || !strings.Contains(err.Error(), "invalid click selector") {
		t.Fatalf("invalid selector error=%v", err)
	}
	if _, err := c.Execute(computer.Action{Type: "click", Selector: `.missing-digilo-link`}); err == nil || !strings.Contains(err.Error(), "matched no element") {
		t.Fatalf("unmatched selector error=%v", err)
	}
	if _, err := c.Execute(computer.Action{Type: "click", Selector: `a[href="https://go.marcoschwartz.com/digilo"]`}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.CurrentURL(), "/clicked") {
		t.Fatalf("off-screen selector click did not navigate: %s", c.CurrentURL())
	}
}

func TestLocalExplicitNavigationActionsLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_NAVIGATION_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_NAVIGATION_TESTS=1")
	}
	var firstLoads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/first":
			firstLoads.Add(1)
			_, _ = w.Write([]byte(`<title>First</title><main>First page</main>`))
		case "/second":
			_, _ = w.Write([]byte(`<title>Second</title><main>Second page</main>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c, err := New(computer.DisplaySize{Width: 1000, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	firstURL := server.URL + "/first"
	secondURL := server.URL + "/second"
	if err := c.OpenSession(computer.OpenOptions{URL: firstURL}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute(computer.Action{Type: "navigate", URL: secondURL}); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if got := c.CurrentURL(); got != secondURL {
		t.Fatalf("navigate URL: want %q, got %q", secondURL, got)
	}
	if _, err := c.Execute(computer.Action{Type: "back"}); err != nil {
		t.Fatalf("back: %v", err)
	}
	if got := c.CurrentURL(); got != firstURL {
		t.Fatalf("back URL: want %q, got %q", firstURL, got)
	}
	loadsBeforeReload := firstLoads.Load()
	if _, err := c.Execute(computer.Action{Type: "reload"}); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := c.CurrentURL(); got != firstURL {
		t.Fatalf("reload URL: want %q, got %q", firstURL, got)
	}
	if firstLoads.Load() <= loadsBeforeReload {
		t.Fatalf("reload did not request the current page again: before=%d after=%d", loadsBeforeReload, firstLoads.Load())
	}
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
