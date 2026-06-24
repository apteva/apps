package browserbase

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestActionTimeoutsAreBounded(t *testing.T) {
	cases := map[string]time.Duration{
		"click":        clickActionTimeout,
		"double_click": clickActionTimeout,
		"type":         textActionTimeout,
		"key":          keyActionTimeout,
		"scroll":       scrollActionTimeout,
		"wait":         waitActionTimeout,
		"navigate":     navigateActionTimeout,
	}
	for action, want := range cases {
		if got := actionTimeout(action); got != want {
			t.Fatalf("actionTimeout(%q): want %s, got %s", action, want, got)
		}
	}
}

func TestSleepWithContextReportsActionTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	err := sleepWithContext(ctx, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "action_timeout") {
		t.Fatalf("sleepWithContext timeout: want action_timeout, got %v", err)
	}
}
