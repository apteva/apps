package stability

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/chromedp"
)

func TestSelectorTextWaitsForHiddenStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("real Chromium wait is covered by tier 2")
	}
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	defer cancelTimeout()
	html := `<html><body><div id="s" hidden>loading</div><input id="pw" type="password" value="ready">
<script>setTimeout(()=>{document.getElementById('s').textContent=' Ready '},600)</script></body></html>`
	if err := chromedp.Run(ctx, chromedp.Navigate("data:text/html,"+url.PathEscape(html)), chromedp.WaitReady("body")); err != nil {
		if strings.Contains(err.Error(), "exec") || strings.Contains(err.Error(), "Chrome") {
			t.Skipf("chrome unavailable: %v", err)
		}
		t.Fatal(err)
	}
	res, err := WaitForOutcome(ctx, []computer.WaitCondition{{Type: "selector_text", Selector: "#s", Value: "ready", Op: "equals"}}, "any", 0, 5000)
	if err != nil {
		t.Fatalf("selector_text equals on a hidden element: %v (%#v)", err, res)
	}
	if _, err := WaitForOutcome(ctx, []computer.WaitCondition{{Type: "selector_text", Selector: "#pw", Value: "ready", Op: "contains"}}, "any", 0, 800); err == nil {
		t.Fatal("password fields must never match selector_text")
	}
}
