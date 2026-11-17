package screenmask

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/chromedp"
)

func TestValidate(t *testing.T) {
	if err := Validate(computer.ScreenshotMask{Secrets: []string{""}}); err == nil {
		t.Fatal("empty secret accepted")
	}
	many := make([]string, MaxSecrets+1)
	for i := range many {
		many[i] = "x"
	}
	if err := Validate(computer.ScreenshotMask{Secrets: many}); err == nil {
		t.Fatal("too many secrets accepted")
	}
}

func TestApplyHidesAndRestores(t *testing.T) {
	if testing.Short() {
		t.Skip("real Chromium masking is covered by tier 2")
	}
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	defer cancelTimeout()
	html := `<html><body><p id="greet">Hello</p><p id="tok">token s3cr3t-value here</p>
<input id="user" value="ada"><input id="pw" type="password" value="pw"><textarea id="note">has s3cr3t-value</textarea></body></html>`
	if err := chromedp.Run(ctx, chromedp.Navigate("data:text/html,"+url.PathEscape(html)), chromedp.WaitReady("body")); err != nil {
		if strings.Contains(err.Error(), "exec") || strings.Contains(err.Error(), "Chrome") {
			t.Skipf("chrome unavailable: %v", err)
		}
		t.Fatal(err)
	}
	visibility := func() map[string]string {
		var out map[string]string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`Object.fromEntries(["greet","tok","user","pw","note"].map(id=>[id,getComputedStyle(document.getElementById(id)).visibility]))`, &out)); err != nil {
			t.Fatal(err)
		}
		return out
	}

	restore, err := Apply(ctx, computer.ScreenshotMask{Secrets: []string{"s3cr3t-value"}})
	if err != nil {
		t.Fatal(err)
	}
	got := visibility()
	if got["greet"] != "visible" || got["user"] != "visible" || got["tok"] != "hidden" || got["pw"] != "hidden" || got["note"] != "hidden" {
		t.Fatalf("secrets-only mask: %v", got)
	}
	restore()
	for id, v := range visibility() {
		if v != "visible" {
			t.Fatalf("%s not restored: %v", id, v)
		}
	}

	restore, err = Apply(ctx, computer.ScreenshotMask{Inputs: true})
	if err != nil {
		t.Fatal(err)
	}
	got = visibility()
	if got["user"] != "hidden" || got["greet"] != "visible" || got["tok"] != "visible" {
		t.Fatalf("inputs mask: %v", got)
	}
	var shot []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&shot)); err != nil || len(shot) == 0 {
		t.Fatalf("capture while masked: %v", err)
	}
	restore()
	if visibility()["user"] != "visible" {
		t.Fatal("inputs not restored")
	}
}
