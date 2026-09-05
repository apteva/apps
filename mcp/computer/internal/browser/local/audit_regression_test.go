package local

import (
	"context"
	"fmt"
	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdptabs"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"github.com/apteva/apps/mcp/computer/internal/browser/domextract"
	"github.com/apteva/apps/mcp/computer/internal/browser/fileupload"
	"github.com/apteva/apps/mcp/computer/internal/browser/selectinput"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAuditProxyOrigin(t *testing.T) {
	for _, tc := range []struct {
		origin string
		want   bool
	}{{"http://proxy.test:8080", true}, {"http://site.test:8080", false}, {"https://proxy.test:8080", false}, {"http://proxy.test:8081", false}, {"", false}} {
		if got := sameProxyOrigin(tc.origin, "http://user:secret@proxy.test:8080"); got != tc.want {
			t.Errorf("origin %q: %v", tc.origin, got)
		}
	}
}
func TestAuditLocalReadOnlyContextRejected(t *testing.T) {
	c := &Computer{}
	if err := c.OpenSession(computer.OpenOptions{ContextID: "saved", Persist: false}); err == nil || !strings.Contains(err.Error(), "persist=false") {
		t.Fatalf("silently writes saved context: %v", err)
	}
}

// Patreon inserts an asynchronous media preview between observation and input.
// Stable controls must keep their identity when that moves their coordinates.
func TestAuditStructuredInputsSurviveLayoutShift(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<style>input,select{position:absolute;left:20px;width:200px;height:35px}#title{top:20px}#date{top:80px}#choice{top:140px}#file{top:200px}#decoy{top:20px;left:600px}</style><input id=title aria-label=Title><input id=date aria-label=Date type=date><select id=choice aria-label=Choice><option value=a>A</option><option value=b>B</option></select><input id=file type=file aria-label=Upload><input id=decoy aria-label=Decoy value=untouched>`)
	}))
	defer server.Close()
	c, err := New(computer.DisplaySize{Width: 1000, Height: 700})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.OpenSession(computer.OpenOptions{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, action, value, selector string }{{"Title", "set_text", "Correct title", "#title"}, {"Date", "set_temporal", "2026-09-19", "#date"}, {"Choice", "select_option", "b", "#choice"}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.Screenshot(); err != nil {
				t.Fatal(err)
			}
			var target computer.SetOfMarkTarget
			for _, candidate := range c.LastSetOfMark() {
				if candidate.AccessibleName == tc.name {
					target = candidate
					break
				}
			}
			if target.ID == "" {
				t.Fatalf("missing %s", tc.name)
			}
			js := fmt.Sprintf(`(function(){var e=document.querySelector(%q),d=document.querySelector('#decoy');var r=e.getBoundingClientRect();d.style.left=r.left+'px';d.style.top=r.top+'px';e.style.left='320px';})()`, tc.selector)
			if err := cdputil.Run(c.ctx, chromedp.Evaluate(js, nil)); err != nil {
				t.Fatal(err)
			}
			if err := c.ExecuteAction(computer.Action{Type: tc.action, TargetID: target.ID, Label: target.Label, Text: tc.value, Value: tc.value}); err != nil {
				t.Fatal(err)
			}
			var got []string
			if err := cdputil.Run(c.ctx, chromedp.Evaluate(fmt.Sprintf(`[document.querySelector(%q).value,document.querySelector('#decoy').value]`, tc.selector), &got)); err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0] != tc.value || got[1] != "untouched" {
				t.Fatalf("layout shift targeted wrong control: %v", got)
			}
		})
	}
	if _, err := c.Screenshot(); err != nil {
		t.Fatal(err)
	}
	var upload computer.SetOfMarkTarget
	for _, target := range c.LastSetOfMark() {
		if target.AccessibleName == "Upload" {
			upload = target
		}
	}
	if upload.ID == "" {
		t.Fatal("missing upload target")
	}
	if err := cdputil.Run(c.ctx, chromedp.Evaluate(`document.querySelector('#file').style.left='320px'`, nil)); err != nil {
		t.Fatal(err)
	}
	resolved, err := fileupload.ResolveInput(c.ctx, fileupload.Target{ID: upload.ID, HasPoint: true, X: upload.X + upload.W/2, Y: upload.Y + upload.H/2})
	if err != nil || resolved.ID != "file" {
		t.Fatalf("shifted upload target: %+v %v", resolved, err)
	}
	var title computer.SetOfMarkTarget
	for _, target := range c.LastSetOfMark() {
		if target.AccessibleName == "Title" {
			title = target
		}
	}
	if err := cdputil.Run(c.ctx, chromedp.Evaluate(`var old=document.querySelector('#title');old.replaceWith(old.cloneNode(true));`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := c.ExecuteAction(computer.Action{Type: "set_text", TargetID: title.ID, Label: title.Label, Text: "wrong"}); err == nil {
		t.Fatal("detached stable target silently fell back to coordinates")
	}
}
func TestAuditControlsAndTabsLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1")
	}
	const html = `<html><body>
 <div id="ambiguous"><button id="upload">Upload</button><input type="file" id="a"><input type="file" id="b"></div>
 <label for="b" id="label-b">Second file</label>
 <input id="clear" value="remove me">
 <select id="native"><option value="">Choose</option value="x" selected>X</option></select>
 <select id="revert" onchange="this.value='x'"><option value="x">X</option><option value="y">Y</option></select>
 <div role="listbox" id="unrelated"><div role="option" aria-selected="false">Blue</div></div>
 <button id="colors" role="combobox" aria-expanded="true" aria-controls="choices">Colors</button>
 <div role="listbox" id="choices" aria-multiselectable="true"><div role="option" aria-selected="true" onclick="this.setAttribute('aria-selected',this.getAttribute('aria-selected')==='true'?'false':'true')">Red</div><div role="option" aria-selected="false" onclick="this.setAttribute('aria-selected',this.getAttribute('aria-selected')==='true'?'false':'true')">Blue</div></div>
 <select id="duplicate"><option value="a">Same</option><option value="b">Same</option></select><p>Fixture content</p></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/file" {
			w.Header().Set("Content-Disposition", `attachment; filename="tab.txt"`)
			w.Write([]byte("download after original tab closed"))
			return
		}
		w.Write([]byte(html))
	}))
	defer server.Close()
	c, err := New(computer.DisplaySize{Width: 1000, Height: 700})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.OpenSession(computer.OpenOptions{URL: server.URL, Environment: computer.EnvironmentOptions{Timezone: "Europe/Berlin"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fileupload.ResolveInput(c.ctx, fileupload.Target{Selector: "#upload"}); err == nil {
		t.Fatal("ambiguous upload chose an input")
	}
	if got, err := fileupload.ResolveInput(c.ctx, fileupload.Target{Selector: "#label-b"}); err != nil || got.ID != "b" {
		t.Fatalf("explicit label: %+v %v", got, err)
	}
	if err := c.ExecuteAction(computer.Action{Type: "set_text", Selector: "#clear", Text: "", Mode: "replace"}); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := cdputil.Run(c.ctx, chromedp.Evaluate(`document.querySelector('#clear').value`, &value)); err != nil || value != "" {
		t.Fatalf("field not cleared %q %v", value, err)
	}
	if _, err := selectinput.Select(c.ctx, selectinput.Target{Selector: "#native"}, selectinput.Request{Values: []string{""}}); err != nil {
		t.Fatal(err)
	}
	if _, err := selectinput.Select(c.ctx, selectinput.Target{Selector: "#revert"}, selectinput.Request{Value: "y"}); err == nil {
		t.Fatal("reverted value was reported verified")
	}
	if _, err := selectinput.Select(c.ctx, selectinput.Target{Selector: "#duplicate"}, selectinput.Request{Text: "Same"}); err == nil {
		t.Fatal("duplicate labels guessed an option")
	}

	result, err := selectinput.Select(c.ctx, selectinput.Target{Selector: "#colors"}, selectinput.Request{Text: "Blue", Mode: "replace"})
	if err != nil || len(result.Selected) != 1 || result.Selected[0] != "Blue" {
		t.Fatalf("replace multiselect %+v %v", result, err)
	}
	var unrelated string
	cdputil.Run(c.ctx, chromedp.Evaluate(`document.querySelector('#unrelated [role=option]').getAttribute('aria-selected')`, &unrelated))
	if unrelated != "false" {
		t.Fatal("unrelated popup was modified")
	}
	extracted, err := domextract.Run(c.ctx, computer.ExtractOptions{Formats: []string{"text"}, MaxChars: 16})
	if err != nil || len(extracted.Text) > 16 || extracted.HTML != "" || len(extracted.Regions) > 0 {
		t.Fatalf("bounded text extract: %+v %v", extracted, err)
	}
	start := time.Now()
	if err := c.ExecuteAction(computer.Action{Type: "wait", Duration: 1}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("wait interpreted milliseconds as seconds")
	}
	original := cdptabs.ActiveID(c.ctx)
	var second target.ID
	if err := cdputil.Run(c.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		second, err = target.CreateTarget(server.URL).Do(ctx)
		return err
	})); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if err := c.SwitchTab(string(second)); err != nil {
			t.Fatal(err)
		}
		if err := c.SwitchTab(original); err != nil {
			t.Fatal(err)
		}
		var visibility string
		if err := cdputil.Run(c.ctx, chromedp.Evaluate(`document.visibilityState`, &visibility)); err != nil || visibility != "visible" {
			t.Fatalf("tab switch did not activate the browser page: visibility=%q err=%v", visibility, err)
		}
	}
	if len(c.tabTrackers) != 2 {
		t.Fatalf("repeated switch leaked trackers: %d", len(c.tabTrackers))
	}
	if err := c.SwitchTab(string(second)); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseTab(original); err != nil {
		t.Fatal(err)
	}
	if err := cdputil.Run(c.ctx, chromedp.Evaluate(`Intl.DateTimeFormat().resolvedOptions().timeZone`, &value)); err != nil || value != "Europe/Berlin" {
		t.Fatalf("environment after root close %q %v", value, err)
	}
	if err := cdputil.Run(c.ctx, chromedp.Evaluate(`location.href='/file'`, nil)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := waitForDownloadStart(t, c, 0)
	item, err := c.WaitForDownload(ctx, started.ID)
	if err != nil || item.Filename != "tab.txt" {
		t.Fatalf("download after root closed: %+v %v", item, err)
	}
	// Caller cancellation must interrupt waits independently of browser lifetime.
	cancelled, cancelRequest := context.WithCancel(context.Background())
	unbind := c.BindRequest(cancelled)
	cancelRequest()
	start = time.Now()
	err = c.ExecuteAction(computer.Action{Type: "wait", Duration: 30000})
	unbind()
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("request cancellation ignored: %v", err)
	}
}

func TestAuditProxyAuthNeverAnswersOriginAndBoundsRetries(t *testing.T) {
	attempts := &proxyAuthAttempts{seen: map[fetch.RequestID]bool{}}
	e := &fetch.EventAuthRequired{RequestID: "r", AuthChallenge: &fetch.AuthChallenge{Source: fetch.AuthChallengeSourceServer, Origin: "http://proxy.test:8080"}}
	if attempts.allow(e, "http://proxy.test:8080") {
		t.Fatal("origin received proxy credentials")
	}
	e.AuthChallenge.Source = fetch.AuthChallengeSourceProxy
	if !attempts.allow(e, "http://proxy.test:8080") || attempts.allow(e, "http://proxy.test:8080") {
		t.Fatal("proxy auth first attempt/retry policy incorrect")
	}
	for i := 0; i < 512; i++ {
		e.RequestID = fetch.RequestID(fmt.Sprint(i))
		if !attempts.allow(e, "http://proxy.test:8080") {
			t.Fatal("bounded cache blocked a new request")
		}
	}
	if len(attempts.seen) > 128 {
		t.Fatal("unbounded auth attempts cache")
	}
}
