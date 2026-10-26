package domextract

import (
	"context"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/chromedp"
)

func TestRunPreservesHTMLTailBeyondOneMillionCharacters(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chromium")
	}
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Navigate("data:text/html,<body><main></main></body>"), chromedp.Evaluate(`document.querySelector('main').textContent='x'.repeat(1500000)+'VERIFIED_END_OF_HTML'`, nil)); err != nil {
		if strings.Contains(err.Error(), "exec") || strings.Contains(err.Error(), "Chrome") {
			t.Skipf("Chrome unavailable: %v", err)
		}
		t.Fatal(err)
	}
	out, err := Run(ctx, computer.ExtractOptions{Formats: []string{"html"}, MaxChars: computer.MaxExtractChars, Readability: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.HTML) <= 1_000_000 || !strings.Contains(out.HTML, "VERIFIED_END_OF_HTML") {
		t.Fatal("DOM backend still capped HTML at the former one-million limit", len(out.HTML))
	}
}
