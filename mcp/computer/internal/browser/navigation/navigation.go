package navigation

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// RecoverTimeout accepts a navigation timeout only when Chrome reached a real
// page. Some sites keep loading analytics, ads, or long-lived resources after
// their usable UI is rendered.
func RecoverTimeout(parent context.Context, cause error) (string, bool) {
	if !errors.Is(cause, context.DeadlineExceeded) || parent == nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	var current string
	err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_ = page.StopLoading().Do(ctx)
			return nil
		}),
		chromedp.Location(&current),
	)
	return current, err == nil && UsableURL(current)
}

func UsableURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "about:blank" || strings.HasPrefix(raw, "chrome-error://") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch parsed.Scheme {
	case "http", "https":
		return parsed.Host != ""
	case "data", "file":
		return true
	default:
		return false
	}
}
