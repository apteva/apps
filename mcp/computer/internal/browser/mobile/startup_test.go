package mobile

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	api "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestStartupRetriesPreparationUntilReady(t *testing.T) {
	f := &fakeMobile{width: 400, height: 800}
	opens := 0
	caller := func(ctx context.Context, tool string, args map[string]any, out any) error {
		if tool == "mobile_session" && args["action"] == "open" {
			opens++
			if opens < 3 {
				return errors.New("mobile.mobile_session: the iOS runner is still starting; retry shortly")
			}
		}
		return f.call(ctx, tool, args, out)
	}
	c, _ := New(api.SessionTarget{Kind: "mobile", DeviceID: "iphone"}, caller)
	c.startupRetryDelay = time.Millisecond
	if err := c.OpenSession(api.OpenOptions{}); err != nil {
		t.Fatal(err)
	}
	if opens != 3 || c.SessionID() != "ses_test" {
		t.Fatalf("open attempts=%d, lease=%s", opens, c.SessionID())
	}
}
func TestStartupBudgetReturnsRetryableStateWithoutLease(t *testing.T) {
	opens := 0
	c, _ := New(api.SessionTarget{Kind: "mobile"}, func(context.Context, string, map[string]any, any) error {
		opens++
		return errors.New("setup_in_progress: the iOS runner is still starting")
	})
	c.startupWindow = 15 * time.Millisecond
	c.startupRetryDelay = 3 * time.Millisecond
	err := c.OpenSession(api.OpenOptions{})
	var failure *StartupError
	if !errors.As(err, &failure) || !failure.Retryable || failure.Code != "mobile_starting" || failure.UserActionRequired || failure.RetryAfterMS <= 0 {
		t.Fatalf("preparation error: %#v", err)
	}
	if opens < 2 || c.SessionID() != "" {
		t.Fatalf("attempts=%d, lease=%q", opens, c.SessionID())
	}
}
func TestStartupDoesNotReplayAuthorizationOrUnknownOutcomes(t *testing.T) {
	for _, tc := range []struct {
		message string
		code    string
	}{
		{"Timed out while enabling automation mode.", "mobile_authorization_required"},
		{"unlock the device and keep it unlocked", "mobile_authorization_required"},
		{"enable Developer Mode on the device", "mobile_setup_required"},
		{"the iOS runner stopped while starting", "mobile_startup_failed"},
		{"the iOS runner did not answer within 4 minutes", "mobile_startup_failed"},
		{"platform.apps.call permission denied", ""},
		{"connection reset after request write", ""},
	} {
		t.Run(tc.message, func(t *testing.T) {
			calls := 0
			cause := errors.New(tc.message)
			c, _ := New(api.SessionTarget{Kind: "mobile"}, func(context.Context, string, map[string]any, any) error { calls++; return cause })
			err := c.OpenSession(api.OpenOptions{})
			if calls != 1 || !errors.Is(err, cause) {
				t.Fatalf("calls=%d, cause lost: %v", calls, err)
			}
			var failure *StartupError
			if tc.code == "" {
				if errors.As(err, &failure) {
					t.Fatal("unknown outcome misclassified as preparation")
				}
				return
			}
			if !errors.As(err, &failure) || failure.Code != tc.code || failure.Retryable || !failure.UserActionRequired {
				t.Fatalf("unexpected startup state: %#v", err)
			}
		})
	}
}
func TestStartupCancellationStopsPreparationRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	c, _ := New(api.SessionTarget{Kind: "mobile"}, func(context.Context, string, map[string]any, any) error {
		calls++
		cancel()
		return errors.New("setup_in_progress")
	})
	defer c.BindRequest(ctx)()
	if err := c.OpenSession(api.OpenOptions{}); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d, cancellation=%v", calls, err)
	}
	// A request timeout is an unknown open outcome; never retry it as preparation.
	calls = 0
	c, _ = New(api.SessionTarget{Kind: "mobile"}, func(context.Context, string, map[string]any, any) error {
		calls++
		return fmt.Errorf("request: %w", context.DeadlineExceeded)
	})
	if err := c.OpenSession(api.OpenOptions{}); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("calls=%d, timeout=%v", calls, err)
	}
}
