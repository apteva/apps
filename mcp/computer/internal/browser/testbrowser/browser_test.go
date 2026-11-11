package testbrowser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPolicyRejectsForeignRuntimeAndManagementPaths(t *testing.T) {
	base := "http://127.0.0.1:5280/api/environment-app-public/rt_test/api/apps/ui/_install/1/ui"
	policy := Policy{RuntimeID: "rt_test", Targets: map[string]string{"frontend": base}}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"http://127.0.0.1:5280/api/apps/ui/_install/1/ui",
		"http://127.0.0.1:5280/api/environment-app-public/rt_foreign/api/apps/ui/_install/1/ui",
		"https://127.0.0.1:5280/api/environment-app-public/rt_test/api/apps/ui/_install/1/ui",
		"http://example.com/api/environment-app-public/rt_test/api/apps/ui/_install/1/ui",
		"http://127.0.0.1:5280/api/environment-app-public/rt_test/api/apps/ui/_install/1/mcp",
	} {
		if err := (Policy{RuntimeID: "rt_test", Targets: map[string]string{"frontend": bad}}).Validate(); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	for _, bad := range []string{"/../mcp", "//example.com", "/%2e%2e/mcp", "/x%2f..", "/x\\.."} {
		if _, err := policy.URL("frontend", bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestLocalBrowserIsolationInputAndCancellation(t *testing.T) {
	if os.Getenv("RUN_BROWSER_TESTS") != "1" {
		t.Skip("RUN_BROWSER_TESTS=1 runs local headless Chromium")
	}
	var foreign atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreign.Add(1); fmt.Fprint(w, "escaped") }))
	defer trap.Close()
	prefix := "/api/environment-app-public/rt_test/api/apps/ui/_install/1/ui"
	var allowed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix) {
			foreign.Add(1)
			http.Error(w, "foreign", 403)
			return
		}
		allowed.Add(1)
		w.Header().Set("Content-Type", "text/html")
		if strings.HasSuffix(r.URL.Path, "/redirect") {
			http.Redirect(w, r, trap.URL+"/redirected", 302)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sw.js") {
			w.Header().Set("Content-Type", "application/javascript")
			fmt.Fprintf(w, `self.addEventListener('install',()=>self.skipWaiting());self.addEventListener('activate',event=>event.waitUntil(fetch(%q).catch(()=>{})));`, trap.URL+"/worker")
			return
		}
		if r.URL.Query().Get("attack") == "1" {
			fmt.Fprintf(w, `<body><p data-testid="attack">started</p><script>
   fetch(%q).catch(()=>{});
   fetch('/api/apps').catch(()=>{});
   fetch('/api/environment-app-public/rt_foreign/api/apps/ui/_install/1/ui').catch(()=>{});
   const image=new Image();image.src=%q;document.body.append(image);
   const frame=document.createElement('iframe');frame.src=%q;document.body.append(frame);
   try{new WebSocket(%q)}catch{}
   fetch(%q).catch(()=>{});
   navigator.serviceWorker?.register(%q);
   </script></body>`, trap.URL+"/fetch", trap.URL+"/image", trap.URL+"/frame", strings.Replace(trap.URL, "http:", "ws:", 1)+"/socket", prefix+"/redirect", prefix+"/sw.js")
			return
		}
		fmt.Fprint(w, `<body>
   <label>Email<input data-testid="email"></label>
   <label>Password<input data-testid="password" type="password"></label>
   <button data-testid="save" onclick="localStorage.setItem('saved','yes');document.cookie='saved=yes;path=/';document.querySelector('[data-testid=result]').textContent='saved'">Save</button>
   <p data-testid="result">new</p>
   <p data-testid="fresh"></p>
   <select data-testid="choice"><option value="a">A</option><option value="b">B</option></select>
   <input data-testid="checked" type="checkbox">
   <input data-testid="file" type="file">
   <script>document.querySelector('[data-testid=fresh]').textContent=localStorage.getItem('saved')||'empty'</script>
   </body>`)
	}))
	defer server.Close()
	root := t.TempDir()
	create := func(name string) *Browser {
		t.Helper()
		profile := filepath.Join(root, name)
		if err := os.Mkdir(profile, 0700); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		browser, err := New(ctx, Options{Policy: Policy{RuntimeID: "rt_test", Targets: map[string]string{"frontend": server.URL + prefix}},
			Width: 1000, Height: 700, Locale: "en-US", Timezone: "UTC", Profile: profile})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(browser.Close)
		return browser
	}
	browser := create("first")
	run := func(b *Browser, command Command) Result {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result, err := b.Do(ctx, command)
		if err != nil {
			t.Fatalf("%s: %v", command.Action, err)
		}
		return result
	}
	run(browser, Command{Action: "navigate", Target: "frontend", Path: "/"})
	run(browser, Command{Action: "fill", Locator: Locator{TestID: "email"}, Value: "operator@fixture.invalid"})
	run(browser, Command{Action: "fill", Locator: Locator{TestID: "password"}, Value: "synthetic-password", Sensitive: true})
	observed := run(browser, Command{Action: "observe", Locator: Locator{TestID: "password"}})
	if observed.Observation.Value != nil {
		t.Fatal("password value was exposed")
	}
	run(browser, Command{Action: "select", Locator: Locator{TestID: "choice"}, Value: "b"})
	run(browser, Command{Action: "check", Locator: Locator{TestID: "checked"}, Checked: true})
	run(browser, Command{Action: "upload", Locator: Locator{TestID: "file"}, File: &FixtureFile{Name: "fixture.txt", MIME: "text/plain", Base64: "bG9jYWwgZml4dHVyZQ=="}})
	run(browser, Command{Action: "click", Locator: Locator{Role: "button", Name: "Save"}})
	observed = run(browser, Command{Action: "observe", Locator: Locator{TestID: "result"}})
	if observed.Observation.Text != "saved" {
		t.Fatal(observed)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	artifact, err := browser.Screenshot(ctx)
	cancel()
	if err != nil || artifact.MIME != "image/png" || artifact.SHA256 == "" {
		t.Fatal(artifact, err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err = browser.Do(cancelled, Command{Action: "click", Locator: Locator{TestID: "save"}}); err == nil {
		t.Fatal("cancelled command was dispatched")
	}
	browser.Close()
	fresh := create("second")
	run(fresh, Command{Action: "navigate", Target: "frontend", Path: "/"})
	if got := run(fresh, Command{Action: "observe", Locator: Locator{TestID: "fresh"}}); got.Observation.Text != "empty" {
		t.Fatal("browser storage leaked", got)
	}
	run(fresh, Command{Action: "navigate", Target: "frontend", Path: "/?attack=1"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && fresh.Diagnostics().Blocked < 7 {
		time.Sleep(50 * time.Millisecond)
	}
	if foreign.Load() != 0 || fresh.Diagnostics().Blocked < 6 {
		t.Fatalf("network escaped=%d diagnostics=%+v", foreign.Load(), fresh.Diagnostics())
	}
	if allowed.Load() < 2 {
		t.Fatal("allowed runtime was not exercised")
	}
	t.Logf("local Chromium isolated cookies, handled inputs and blocked %d foreign requests", fresh.Diagnostics().Blocked)
}

func TestDiagnosticsRedactSecretsFromRequestedOrigins(t *testing.T) {
	b := &Browser{boundary: &boundary{requests: []Request{{Origin: "https://seeded-secret.invalid", Method: "GET", Status: 403}}}}
	b.RegisterSecret("seeded-secret")
	if got := b.Diagnostics().Requests[0].Origin; strings.Contains(got, "seeded-secret") {
		t.Fatal("secret leaked through origin", got)
	}
}
