package cdptabs

import (
	"context"
	"fmt"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"strings"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/cdproto/cdp"
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
	bounded, cancel := cdputil.Context(ctx, cdputil.Timeout)
	defer cancel()
	infos, err := chromedp.Targets(bounded)
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
	return hideInactiveBlankPlaceholders(out), nil
}

func hideInactiveBlankPlaceholders(tabs []computer.TabInfo) []computer.TabInfo {
	hasContent := false
	for _, tab := range tabs {
		if !isBlankPlaceholder(tab) {
			hasContent = true
			break
		}
	}
	if !hasContent {
		return tabs
	}
	out := tabs[:0]
	for _, tab := range tabs {
		if isBlankPlaceholder(tab) && !tab.Active {
			continue
		}
		out = append(out, tab)
	}
	return out
}

func isBlankPlaceholder(tab computer.TabInfo) bool {
	return strings.EqualFold(strings.TrimSpace(tab.URL), "about:blank")
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
	if err := Activate(parent, tabID); err != nil {
		return nil, nil, err
	}
	ctx, cancel := chromedp.NewContext(parent, chromedp.WithTargetID(target.ID(tabID)))
	timer := time.AfterFunc(cdputil.Timeout, cancel)
	defer timer.Stop()
	if err := chromedp.Run(ctx); err != nil {
		cancel()
		return nil, nil, err
	}
	return ctx, cancel, nil
}

// Activate selects the actual browser tab before attaching domains. Background renderers
// can suspend initialization, and a logical CDP switch alone leaves the browser
// UI on the previous tab. Use the browser executor so the old page may be closed.
func Activate(parent context.Context, tabID string) error {
	if parent == nil || strings.TrimSpace(tabID) == "" {
		return fmt.Errorf("browser context and tab_id required")
	}
	c := chromedp.FromContext(parent)
	if c == nil || c.Browser == nil {
		return fmt.Errorf("no browser connection")
	}
	ctx, cancel := cdputil.Context(parent, cdputil.Timeout)
	defer cancel()
	return target.ActivateTarget(target.ID(tabID)).Do(cdp.WithExecutor(ctx, c.Browser))
}

// Close closes a page target.
func Close(ctx context.Context, tabID string) error {
	if ctx == nil {
		return fmt.Errorf("no active browser session")
	}
	if strings.TrimSpace(tabID) == "" {
		return fmt.Errorf("tab_id required")
	}
	c := chromedp.FromContext(ctx)
	if c == nil || c.Browser == nil {
		return fmt.Errorf("no browser connection")
	}
	bounded, cancel := cdputil.Context(ctx, cdputil.Timeout)
	defer cancel()
	browserCtx := cdp.WithExecutor(bounded, c.Browser)
	return target.CloseTarget(target.ID(tabID)).Do(browserCtx)
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

// Manager owns one attachment per target under a stable browser context.
// The caller serializes access with its session action lock.
type Manager struct {
	root     context.Context
	contexts map[string]context.Context
	cancels  map[string]context.CancelFunc
}

func NewManager(root context.Context, cancel context.CancelFunc) *Manager {
	id := ActiveID(root)
	return &Manager{root: root, contexts: map[string]context.Context{id: root}, cancels: map[string]context.CancelFunc{id: cancel}}
}
func (m *Manager) Switch(id string) (context.Context, context.CancelFunc, bool, error) {
	if ctx := m.contexts[id]; ctx != nil && ctx.Err() == nil {
		if err := Activate(m.root, id); err != nil {
			return nil, nil, false, err
		}
		return ctx, m.cancels[id], false, nil
	}
	ctx, cancel, err := Switch(m.root, id)
	if err != nil {
		return nil, nil, false, err
	}
	m.contexts[id], m.cancels[id] = ctx, cancel
	return ctx, cancel, true, nil
}
func (m *Manager) Forget(id string) context.Context {
	forgotten := m.contexts[id]
	// Keep the root context alive to own the browser transport even if its
	// original page closes. All other attachments can be detached immediately.
	if ctx := m.contexts[id]; ctx != nil && ctx != m.root {
		m.cancels[id]()
	}
	delete(m.contexts, id)
	delete(m.cancels, id)
	return forgotten
}
