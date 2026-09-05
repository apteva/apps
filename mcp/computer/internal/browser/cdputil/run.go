package cdputil

import (
	"context"
	"errors"
	"github.com/chromedp/chromedp"
	"sync"
	"time"
)

const Timeout = 30 * time.Second

// Context bounds CDP commands and propagates the caller bound to this browser.
// Use it for commands only, never as the lifetime of a new browser/target.
func Context(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	stop := func() bool { return false }
	if c := chromedp.FromContext(parent); c != nil && c.Browser != nil {
		if value, ok := requests.Load(c.Browser); ok {
			request := value.(context.Context)
			stop = context.AfterFunc(request, cancel)
			if request.Err() != nil {
				cancel()
			}
		}
	}
	return ctx, func() { stop(); cancel() }
}
func Run(parent context.Context, actions ...chromedp.Action) error {
	if parent == nil {
		return errors.New("session_not_active")
	}
	ctx, cancel := Context(parent, Timeout)
	defer cancel()
	return chromedp.Run(ctx, actions...)
}
func Sleep(ctx context.Context, duration time.Duration) error {
	if duration < 0 || duration > Timeout {
		return errors.New("duration must be between 0 and 30000 milliseconds")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if c := chromedp.FromContext(ctx); c != nil && c.Browser != nil {
		if request, ok := requests.Load(c.Browser); ok {
			stop := context.AfterFunc(request.(context.Context), cancel)
			defer stop()
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Bind associates the caller's cancellation with a serialized browser action.
// The key is the browser connection, so a tab switch preserves the deadline.
var requests sync.Map

func Bind(browserCtx, requestCtx context.Context) func() {
	if browserCtx == nil || requestCtx == nil {
		return func() {}
	}
	c := chromedp.FromContext(browserCtx)
	if c == nil || c.Browser == nil {
		return func() {}
	}
	requests.Store(c.Browser, requestCtx)
	return func() { requests.Delete(c.Browser) }
}
