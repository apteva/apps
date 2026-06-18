package cdptabs

import (
	"context"
	"fmt"
	"strings"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// ActiveID returns chromedp's current page target id for ctx.
func ActiveID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	c := chromedp.FromContext(ctx)
	if c == nil || c.Target == nil {
		return ""
	}
	return string(c.Target.TargetID)
}

// List returns visible page targets for a browser session.
func List(ctx context.Context) ([]computer.TabInfo, error) {
	if ctx == nil {
		return nil, fmt.Errorf("no active browser session")
	}
	infos, err := chromedp.Targets(ctx)
	if err != nil {
		return nil, err
	}
	active := ActiveID(ctx)
	out := make([]computer.TabInfo, 0, len(infos))
	for _, info := range infos {
		if info == nil || info.Type != "page" {
			continue
		}
		id := string(info.TargetID)
		out = append(out, computer.TabInfo{
			ID:       id,
			URL:      info.URL,
			Title:    info.Title,
			Active:   id == active,
			OpenerID: string(info.OpenerID),
		})
	}
	return out, nil
}

// Switch binds a new chromedp page context to tabID using an existing browser
// context as the parent. chromedp inherits the browser connection from parent
// and binds the child context to the requested page target.
func Switch(parent context.Context, tabID string) (context.Context, context.CancelFunc, error) {
	if parent == nil {
		return nil, nil, fmt.Errorf("no browser context")
	}
	if strings.TrimSpace(tabID) == "" {
		return nil, nil, fmt.Errorf("tab_id required")
	}
	ctx, cancel := chromedp.NewContext(parent, chromedp.WithTargetID(target.ID(tabID)))
	if err := chromedp.Run(ctx); err != nil {
		cancel()
		return nil, nil, err
	}
	return ctx, cancel, nil
}

// Close closes a page target.
func Close(ctx context.Context, tabID string) error {
	if ctx == nil {
		return fmt.Errorf("no active browser session")
	}
	if strings.TrimSpace(tabID) == "" {
		return fmt.Errorf("tab_id required")
	}
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return target.CloseTarget(target.ID(tabID)).Do(ctx)
	}))
}

// PickFallback returns the first non-active page tab, preferring real web
// content over blank/internal pages.
func PickFallback(tabs []computer.TabInfo, closedID string) string {
	first := ""
	firstWeb := ""
	firstNonInternal := ""
	for _, tab := range tabs {
		if tab.ID == "" || tab.ID == closedID {
			continue
		}
		if first == "" {
			first = tab.ID
		}
		u := strings.ToLower(tab.URL)
		if firstWeb == "" && (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) {
			firstWeb = tab.ID
		}
		if firstNonInternal == "" && u != "" &&
			!strings.HasPrefix(u, "about:") &&
			!strings.HasPrefix(u, "chrome://") &&
			!strings.HasPrefix(u, "devtools://") {
			firstNonInternal = tab.ID
		}
	}
	if firstWeb != "" {
		return firstWeb
	}
	if firstNonInternal != "" {
		return firstNonInternal
	}
	return first
}
