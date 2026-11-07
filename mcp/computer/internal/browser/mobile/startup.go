package mobile

import (
	"context"
	"errors"
	"strings"
	"time"
)

const defaultStartupWindow = 18 * time.Second
const defaultStartupRetryDelay = time.Second

// StartupError separates safe-to-retry preparation from a phone or Xcode
// authorization gate. It is only used while opening a lease, never for input.
type StartupError struct {
	Code               string
	Status             string
	Retryable          bool
	UserActionRequired bool
	RetryAfterMS       int
	Message            string
	NextStep           string
	Cause              error
}

func (e *StartupError) Error() string { return e.Message }
func (e *StartupError) Unwrap() error { return e.Cause }

func classifyStartup(err error) *StartupError {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "timed out while enabling automation mode"), strings.Contains(message, "enter iphone passcode"), strings.Contains(message, "passcode for xctest"), strings.Contains(message, "unlock the device"), strings.Contains(message, "unlock the iphone"):
		return &StartupError{Code: "mobile_authorization_required", Status: "needs_user_action", UserActionRequired: true, Message: "Mobile needs authorization on the iPhone before Computer can open a session.", NextStep: "Keep the iPhone unlocked and complete any XCTest / Enable UI Automation passcode prompt directly on the phone, then retry browser_session(action=open).", Cause: err}
	case strings.Contains(message, "developer mode"), strings.Contains(message, "tap trust"), strings.Contains(message, "trust the developer"), strings.Contains(message, "could not sign"), strings.Contains(message, "xcode > settings > accounts"):
		return &StartupError{Code: "mobile_setup_required", Status: "needs_user_action", UserActionRequired: true, Message: "Mobile needs device or Xcode setup before Computer can open a session.", NextStep: "Inspect mobile_setup(action=status) and complete the reported device trust, Developer Mode or Xcode signing step, then retry open.", Cause: err}
	case strings.Contains(message, "setup_in_progress"), strings.Contains(message, "the ios runner is still starting"):
		return &StartupError{Code: "mobile_starting", Status: "starting", Retryable: true, RetryAfterMS: 2000, Message: "Mobile's native runner is still preparing; no Computer session has been opened.", NextStep: "Retry browser_session(action=open) shortly. Keep the phone unlocked; if iOS shows an XCTest / Enable UI Automation passcode prompt, complete it directly on the phone. Preparation may take a few minutes.", Cause: err}
	case strings.Contains(message, "the ios runner stopped while starting"), strings.Contains(message, "the ios runner did not answer within"):
		return &StartupError{Code: "mobile_startup_failed", Status: "needs_user_action", UserActionRequired: true, Message: "Mobile's native runner could not finish starting; no Computer session has been opened.", NextStep: "Inspect Mobile setup and the phone for an XCTest / Enable UI Automation authorization prompt before retrying. If setup is ready and no prompt appears, inspect Xcode's runner diagnostics.", Cause: err}
	}
	return nil
}

// openLease only retries the explicit preparation response. A transport error
// can mean a lease was created with a lost reply; never replay that request.
func (c *Computer) openLease(args map[string]any, out any) error {
	window, delay := c.startupWindow, c.startupRetryDelay
	if window <= 0 {
		window = defaultStartupWindow
	}
	if delay <= 0 {
		delay = defaultStartupRetryDelay
	}
	parent := c.ctx()
	ctx, cancel := context.WithTimeout(parent, window)
	defer cancel()
	for {
		err := c.invoke(ctx, "mobile_session", args, out)
		if err == nil {
			return nil
		}
		if parent.Err() != nil {
			return parent.Err()
		}
		failure := classifyStartup(err)
		if failure == nil {
			return err
		}
		if !failure.Retryable {
			return failure
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			if parent.Err() != nil {
				return parent.Err()
			}
			return failure
		case <-timer.C:
			// Avoid dispatching another open after the known-safe preparation window.
			if ctx.Err() != nil {
				if parent.Err() != nil {
					return parent.Err()
				}
				return failure
			}
		}
	}
}
