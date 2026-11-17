package elementread

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/chromedp"
)

func openPage(t *testing.T, html string) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("real Chromium read is covered by tier 2")
	}
	ctx, cancel := chromedp.NewContext(context.Background())
	t.Cleanup(cancel)
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	t.Cleanup(cancelTimeout)
	if err := chromedp.Run(ctx, chromedp.Navigate("data:text/html,"+url.PathEscape(html)), chromedp.WaitReady("body")); err != nil {
		if strings.Contains(err.Error(), "exec") || strings.Contains(err.Error(), "Chrome") {
			t.Skipf("chrome unavailable: %v", err)
		}
		t.Fatalf("navigate: %v", err)
	}
	return ctx
}

const page = `<html><body>
<div id="fx-api-test-status" hidden data-status="ready">ready</div>
<div id="card" data-test-sensitive>4111 1111</div>
<input id="pin" data-test-sensitive value="9999">
<span class="item">one</span><span class="item" style="display:none">two</span>
<label>Name <input id="name" value="Ada"></label>
<input id="password" type="password" value="hunter2">
<input id="otp" autocomplete="one-time-code" value="123456">
<input id="marked" sensitive value="secret">
<div data-sensitive><input id="nested" value="inside"></div>
<input id="agree" type="checkbox" checked>
<textarea id="notes">line</textarea>
</body></html>`

func TestReadHiddenStatusAndRedactsSensitiveValues(t *testing.T) {
	ctx := openPage(t, page)

	status, err := Run(ctx, "#fx-api-test-status", computer.ElementReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if status.Count != 1 || status.Elements[0].Text != "ready" || status.Elements[0].Visible || status.Elements[0].Attributes["data-status"] != "ready" {
		t.Fatalf("hidden status: %#v", status)
	}
	if visibleOnly, _ := Run(ctx, "#fx-api-test-status", computer.ElementReadOptions{VisibleOnly: true}); visibleOnly.Count != 0 {
		t.Fatalf("include_hidden=false must skip hidden matches: %#v", visibleOnly)
	}
	card, _ := Run(ctx, "#card", computer.ElementReadOptions{})
	if card.Elements[0].Text != "" || !card.Elements[0].Redacted {
		t.Fatalf("sensitive element text must be withheld: %#v", card)
	}

	for _, sel := range []string{"#password", "#otp", "#marked", "#nested", "#pin"} {
		res, err := Run(ctx, sel, computer.ElementReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		el := res.Elements[0]
		if _, leaked := el.Attributes["value"]; leaked || el.Value != nil || !el.ValueRedacted {
			t.Fatalf("%s must not expose a value: %#v", sel, el)
		}
	}

	name, err := Run(ctx, "#name", computer.ElementReadOptions{})
	if err != nil || name.Elements[0].Value == nil || *name.Elements[0].Value != "Ada" || !name.Elements[0].Visible {
		t.Fatalf("plain input value: %#v %v", name, err)
	}
	agree, _ := Run(ctx, "#agree", computer.ElementReadOptions{})
	if agree.Elements[0].Checked == nil || !*agree.Elements[0].Checked {
		t.Fatalf("checkbox state: %#v", agree)
	}

	items, err := Run(ctx, ".item", computer.ElementReadOptions{All: true})
	if err != nil || items.Count != 2 || len(items.Elements) != 2 || !items.Elements[0].Visible || items.Elements[1].Visible || items.Elements[1].Text != "two" {
		t.Fatalf("all matches: %#v %v", items, err)
	}
	firstOnly, _ := Run(ctx, ".item", computer.ElementReadOptions{})
	if firstOnly.Count != 2 || len(firstOnly.Elements) != 1 {
		t.Fatalf("first match only, total count: %#v", firstOnly)
	}

	missing, err := Run(ctx, "#nope", computer.ElementReadOptions{})
	if err != nil || missing.Count != 0 || len(missing.Elements) != 0 {
		t.Fatalf("missing: %#v %v", missing, err)
	}
	if _, err := Run(ctx, "div[", computer.ElementReadOptions{}); !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("invalid selector: %v", err)
	}
}
