package browserbase

import (
	"context"
	"errors"
	"fmt"

	api "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdptabs"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"github.com/apteva/apps/mcp/computer/internal/browser/navigation"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

func (c *Computer) LastNavigation() *api.NavigationDiagnostics { return c.lastNavigation }

// Recovery keeps the same provider session, browser context, cookies, routing
// and persisted context. A new page does not promise a new HTTP/2 connection.
func (c *Computer) executeNavigation(action api.Action) error {
	d := &api.NavigationDiagnostics{RequestedURL: navigation.SafeURL(action.URL), ReadOnly: action.Type == "navigate" && action.ReadOnly}
	c.lastNavigation = d
	attempt, err := navigation.Observed(c.ctx, action.Type, action.URL, navigateActionTimeout)
	d.Attempts = append(d.Attempts, attempt)
	if err == nil {
		return nil
	}
	if !d.ReadOnly || !navigation.Retryable(err) || attempt.UnsafeMethod || attempt.Canceled {
		return &navigation.Failure{Diagnostics: d, Cause: err}
	}
	previous := c.ActiveTabID()
	fresh, freshErr := c.freshNavigationPage()
	if freshErr != nil {
		if c.ActiveTabID() != previous {
			_ = c.SwitchTab(previous)
		}
		return &navigation.Failure{Diagnostics: d, Cause: errors.Join(err, fmt.Errorf("fresh page: %w", freshErr))}
	}
	second, retryErr := navigation.Observed(c.ctx, "navigate", action.URL, navigateActionTimeout)
	d.Attempts = append(d.Attempts, second)
	if retryErr != nil {
		d.RetryExhausted = true
		// Retain the original failed page for inspection; discard the failed retry.
		_ = c.SwitchTab(previous)
		_ = cdptabs.Close(c.ctx, fresh)
		return &navigation.Failure{Diagnostics: d, Cause: retryErr}
	}
	d.Recovered = true
	return nil
}

func (c *Computer) freshNavigationPage() (string, error) {
	state := chromedp.FromContext(c.ctx)
	if state == nil || state.Browser == nil {
		return "", errors.New("no browser connection")
	}
	var id target.ID
	err := cdputil.Run(c.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		executor := cdp.WithExecutor(ctx, state.Browser)
		info, err := target.GetTargetInfo().WithTargetID(state.Target.TargetID).Do(executor)
		if err != nil {
			return err
		}
		create := target.CreateTarget("about:blank")
		if info.BrowserContextID != "" {
			create = create.WithBrowserContextID(info.BrowserContextID)
		}
		id, err = create.Do(executor)
		return err
	}))
	if err != nil {
		return "", err
	}
	if err = c.SwitchTab(string(id)); err != nil {
		_ = cdptabs.Close(c.ctx, string(id))
		return "", err
	}
	return string(id), nil
}
