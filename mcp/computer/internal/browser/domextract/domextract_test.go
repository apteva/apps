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
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()

	ctx, cancel = context.WithTimeout(ctx, 20*time.Second)
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
