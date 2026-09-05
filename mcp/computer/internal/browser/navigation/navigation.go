package navigation

import (
	"context"
	"errors"
	"fmt"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Run executes a browser-history navigation with a bounded CDP context. A
// load timeout is recoverable when Chrome reached a usable page; callers still
// verify the resulting URL when the action is expected to change it.
func Run(parent context.Context, action, rawURL string, timeout time.Duration) error {
	if parent == nil {
		return errors.New("browser context is not active")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var task chromedp.Action
	switch action {
	case "navigate":
		if !UsableURL(rawURL) {
			return fmt.Errorf("invalid navigation URL %q", rawURL)
		}
		task = chromedp.Navigate(strings.TrimSpace(rawURL))
	case "back":
		task = chromedp.NavigateBack()
	case "reload":
		task = chromedp.Reload()
	default:
		return fmt.Errorf("unsupported navigation action %q", action)
	}

	err := cdputil.Run(ctx, task)
	if err == nil {
		return nil
	}
	if _, recovered := RecoverTimeout(parent, err); recovered {
		return nil
	}
	return err
}

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
	err := cdputil.Run(ctx,
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
