package domextract

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/chromedp"
)

func TestRunPreservesBlockSpacingInText(t *testing.T) {
	if testing.Short() {
		t.Skip("real Chromium extraction is covered by tier 2")
	}
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()

	ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	html := `<html><head><title>Example Domain</title></head><body><main><h1>Example Domain</h1><p>This domain is for use in documentation examples without needing permission.</p><p>Avoid use in operations.</p><a href="https://example.com/more">Learn more</a></main></body></html>`
	page := "data:text/html," + url.PathEscape(html)
	if err := chromedp.Run(ctx, chromedp.Navigate(page), chromedp.WaitReady("body")); err != nil {
		if strings.Contains(err.Error(), "exec") || strings.Contains(err.Error(), "Chrome") {
			t.Skipf("chrome unavailable: %v", err)
		}
		t.Fatalf("navigate: %v", err)
	}

	out, err := Run(ctx, computer.ExtractOptions{
		Formats: []string{"text", "markdown", "links", "structured_data"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantText := "Example Domain\n\nThis domain is for use in documentation examples without needing permission.\n\nAvoid use in operations.\n\nLearn more"
	if out.Text != wantText {
		t.Fatalf("text spacing mismatch:\n got: %q\nwant: %q", out.Text, wantText)
	}
	if !strings.Contains(out.Markdown, "# Example Domain\n\nThis domain is for use") {
		t.Fatalf("markdown did not preserve heading paragraph break: %q", out.Markdown)
	}
}

func TestRunExtractsRegions(t *testing.T) {
	if testing.Short() {
		t.Skip("real Chromium extraction is covered by tier 2")
	}
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()

	ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	html := `<html><head><title>Regions</title></head><body>
		<section id="contact" style="margin-top:40px;width:420px;height:140px">
			<h2>Affiliate contact</h2>
			<p>Email partners@example.com for platform partnerships.</p>
		</section>
	</body></html>`
	page := "data:text/html," + url.PathEscape(html)
	if err := chromedp.Run(ctx, chromedp.Navigate(page), chromedp.WaitReady("#contact")); err != nil {
		if strings.Contains(err.Error(), "exec") || strings.Contains(err.Error(), "Chrome") {
			t.Skipf("chrome unavailable: %v", err)
		}
		t.Fatalf("navigate: %v", err)
	}

	out, err := Run(ctx, computer.ExtractOptions{
		Formats: []string{"regions"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Regions) == 0 {
		t.Fatalf("regions empty")
	}
	got := out.Regions[0]
	if got.Selector != "#contact" || got.Heading != "Affiliate contact" {
		t.Fatalf("region identity mismatch: %#v", got)
	}
	if got.Rect.Width <= 0 || got.Rect.Height <= 0 || got.CoordinateFrame != "document_css_px" {
		t.Fatalf("region geometry mismatch: %#v", got)
	}
	if out.Text != "" || out.Markdown != "" {
		t.Fatalf("formats=regions should filter text fields: text=%q markdown=%q", out.Text, out.Markdown)
	}
}

func TestRunFullBodyPreservesContactFooterAndProtocolLinks(t *testing.T) {
	if testing.Short() {
		t.Skip("real Chromium extraction is covered by tier 2")
	}
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	html := `<html><body><header><p>Business header</p></header><main><p>Restaurant menu</p></main><footer><p>Email footer@restaurant.example</p><a href="mailto:footer@restaurant.example">Email us</a><a href="tel:+33123456789">Call</a></footer><script>fake@vendor.example</script><div hidden>hidden@vendor.example</div></body></html>`
	if err := chromedp.Run(ctx, chromedp.Navigate("data:text/html,"+url.PathEscape(html)), chromedp.WaitReady("body")); err != nil {
		if strings.Contains(err.Error(), "exec") || strings.Contains(err.Error(), "Chrome") {
			t.Skipf("chrome unavailable: %v", err)
		}
		t.Fatal(err)
	}
	for _, readability := range []bool{true, false} {
		out, err := Run(ctx, computer.ExtractOptions{Formats: []string{"text", "markdown", "html", "links"}, Readability: readability})
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{out.Text, out.Markdown, out.HTML} {
			if strings.Contains(value, "footer@restaurant.example") == readability {
				t.Fatalf("readability=%v failed footer policy: %s", readability, value)
			}
			if strings.Contains(value, "fake@") || strings.Contains(value, "hidden@") {
				t.Fatal("excluded content leaked into extraction")
			}
		}
		mail, phone := false, false
		for _, link := range out.Links {
			mail = mail || link.URL == "mailto:footer@restaurant.example"
			phone = phone || link.URL == "tel:+33123456789"
		}
		if !mail || !phone {
			t.Fatalf("contact links missing: %+v", out.Links)
		}
	}
}
