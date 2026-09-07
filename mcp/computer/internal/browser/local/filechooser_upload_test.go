package local

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"github.com/apteva/apps/mcp/computer/internal/browser/fileupload"
	"github.com/chromedp/chromedp"
)

func TestFileChooserUploadResolutionLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1")
	}
	c, err := New(computer.DisplaySize{Width: 1000, Height: 700})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.OpenSession(computer.OpenOptions{URL: "data:text/html," + url.PathEscape("<html><body>Upload fixture</body></html>")}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "native-image.png")
	if err := os.WriteFile(path, []byte("test image bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dynamic := range []bool{false, true} {
		for _, payload := range []bool{false, true} {
			t.Run(fmt.Sprintf("dynamic=%t/payload=%t", dynamic, payload), func(t *testing.T) {
				html := fmt.Sprintf(`<html><body><input id="unrelated" type="file" hidden>
<section><div><p><button id="browse" type="button" onclick="pick()">Browse</button></p></div></section>
<input id="camera" type="file" hidden><input id="wanted" type="file" accept="image/png" hidden>
<output id="status">waiting</output><script>
function pick(){var input=document.querySelector('#wanted');if(%t){input.remove();input=document.createElement('input');input.type='file';input.id='wanted';input.accept='image/png';input.hidden=true;document.body.append(input);}
input.onchange=function(){document.querySelector('#status').textContent=this.files[0].name;this.value='';};input.click();}
</script></body></html>`, dynamic)
				if err := c.ExecuteAction(computer.Action{Type: "navigate", URL: "data:text/html," + url.PathEscape(html)}); err != nil {
					t.Fatal(err)
				}
				if _, err := c.Screenshot(); err != nil {
					t.Fatal(err)
				}
				var id string
				for _, target := range c.LastSetOfMark() {
					if target.AccessibleName == "Browse" {
						id = target.ID
					}
				}
				if id == "" {
					t.Fatal("Browse target missing")
				}
				target := fileupload.Target{ID: id}
				var result fileupload.Result
				var err error
				if payload {
					result, err = fileupload.SetPayloads(c.ctx, target, []fileupload.Payload{{Name: "native-image.png", MIME: "image/png", Data: []byte("test image bytes")}})
				} else {
					result, err = fileupload.SetFiles(c.ctx, target, []string{path})
				}
				if err != nil {
					t.Fatal(err)
				}
				if result.ID != "wanted" {
					t.Fatalf("wrong file input: %+v", result)
				}
				var state []any
				if err := cdputil.Run(c.ctx, chromedp.Evaluate(`[document.querySelector('#status').textContent,document.querySelector('#unrelated').files.length,document.querySelector('#camera').files.length]`, &state)); err != nil {
					t.Fatal(err)
				}
				if fmt.Sprint(state) != "[native-image.png 0 0]" {
					t.Fatalf("upload routed to wrong input or change event lost: %v", state)
				}
			})
		}
	}
	t.Run("stale_and_non_upload_controls", func(t *testing.T) {
		html := `<html><body><button id="browse" type="button" onclick="window.clicked=true">Browse</button><button id="delete" type="button" onclick="window.clicked=true">Delete</button><button id="submit" onclick="window.clicked=true">Upload</button><button id="covered" type="button" style="position:absolute;left:100px;top:100px;width:100px;height:40px" onclick="window.clicked=true">Browse</button><div style="position:absolute;left:100px;top:100px;width:100px;height:40px;z-index:10;background:white"></div><input type="file" id="a" hidden><input type="file" id="b" hidden></body></html>`
		if err := c.ExecuteAction(computer.Action{Type: "navigate", URL: "data:text/html," + url.PathEscape(html)}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Screenshot(); err != nil {
			t.Fatal(err)
		}
		var id string
		for _, target := range c.LastSetOfMark() {
			if target.AccessibleName == "Browse" {
				id = target.ID
			}
		}
		if err := cdputil.Run(c.ctx, chromedp.Evaluate(`var e=document.querySelector('#browse');e.replaceWith(e.cloneNode(true));`, nil)); err != nil {
			t.Fatal(err)
		}
		for _, target := range []fileupload.Target{{ID: id, HasPoint: true, X: 20, Y: 20}, {Selector: "#delete"}, {Selector: "#submit"}, {Selector: "#covered"}} {
			if _, err := fileupload.SetFiles(c.ctx, target, []string{path}); err == nil {
				t.Fatalf("unsafe upload target accepted: %+v", target)
			}
		}
		var clicked bool
		if err := cdputil.Run(c.ctx, chromedp.Evaluate(`!!window.clicked`, &clicked)); err != nil {
			t.Fatal(err)
		}
		if clicked {
			t.Fatal("fallback activated a stale, unrelated, or submit control")
		}
	})
	t.Run("ambiguous_selector", func(t *testing.T) {
		// Even when the first match has a related input, a broad selector must
		// not silently upload into it or activate its chooser.
		html := `<html><body><section><button type="button" onclick="window.clicked=true">Browse</button><input type="file" id="first"></section><section><button type="button" onclick="window.clicked=true">Browse</button><input type="file" id="second"></section></body></html>`
		if err := c.ExecuteAction(computer.Action{Type: "navigate", URL: "data:text/html," + url.PathEscape(html)}); err != nil {
			t.Fatal(err)
		}
		for _, selector := range []string{"button", "input[type=file]", "#missing"} {
			if _, err := fileupload.SetFiles(c.ctx, fileupload.Target{Selector: selector}, []string{path}); err == nil || !strings.Contains(err.Error(), "selector must match exactly one element") {
				t.Fatalf("selector %q: %v", selector, err)
			}
		}
		var state []any
		if err := cdputil.Run(c.ctx, chromedp.Evaluate(`[!!window.clicked,document.querySelector('#first').files.length,document.querySelector('#second').files.length]`, &state)); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(state) != "[false 0 0]" {
			t.Fatalf("ambiguous upload dispatched: %v", state)
		}
		if _, err := fileupload.SetFiles(c.ctx, fileupload.Target{Selector: "#second"}, []string{path}); err != nil {
			t.Fatalf("unique selector rejected: %v", err)
		}
	})
	t.Run("resolved_input_removed_before_assignment", func(t *testing.T) {
		html := `<html><body><input id="removed" type="file"><script>new MutationObserver(()=>{let input=document.querySelector('#removed');if(input&&input.hasAttribute('data-apteva-upload-token'))input.remove();}).observe(document.body,{subtree:true,attributes:true});</script></body></html>`
		if err := c.ExecuteAction(computer.Action{Type: "navigate", URL: "data:text/html," + url.PathEscape(html)}); err != nil {
			t.Fatal(err)
		}
		if _, err := fileupload.SetPayloads(c.ctx, fileupload.Target{Selector: "#removed"}, []fileupload.Payload{{Name: "image.png", Data: []byte("image bytes")}}); err == nil {
			t.Fatal("reported upload success after the resolved input disappeared")
		}
	})
}
