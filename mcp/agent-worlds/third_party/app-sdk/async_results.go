package sdk

import (
	"fmt"
	"strings"
	"time"
)

// AsyncResultCapabilities is advertised by PlatformInfo. Missing capabilities
// mean an older server; apps must not assume streaming notifications work.
type AsyncResultCapabilities struct {
	Version       int      `json:"version"`
	Modes         []string `json:"modes"`
	Replay        bool     `json:"replay"`
	ThreadCleanup bool     `json:"thread_cleanup"`
}

// ValidateAsyncResultSpec is shared by app manifest validation and dispatch.
// Events may contain patterns. TerminalEvents are exact topics included in
// Events; an input-required/progress event must not be marked terminal.
func ValidateAsyncResultSpec(spec *AsyncResultSpec) error {
	if spec == nil {
		return nil
	}
	if strings.TrimSpace(spec.IDField) == "" {
		return fmt.Errorf("id_field required")
	}
	n := spec.Notify
	if n == nil {
		return nil
	}
	if n.Target != "" && n.Target != "caller" {
		return fmt.Errorf("notify.target must be caller")
	}
	if n.Mode != "" && n.Mode != "once" && n.Mode != "stream" {
		return fmt.Errorf("notify.mode must be once or stream")
	}
	if len(n.Events) == 0 || len(n.Events) > 64 {
		return fmt.Errorf("notify.events must contain 1 to 64 topics")
	}
	events := map[string]bool{}
	for _, event := range n.Events {
		if strings.TrimSpace(event) != event || event == "" || len(event) > 256 || strings.ContainsAny(event, "\r\n\x00") {
			return fmt.Errorf("invalid notify event %q", event)
		}
		if events[event] {
			return fmt.Errorf("duplicate notify event %q", event)
		}
		events[event] = true
	}
	if n.Mode == "stream" && len(n.TerminalEvents) == 0 {
		return fmt.Errorf("stream requires terminal_events")
	}
	if n.Mode != "stream" && len(n.TerminalEvents) != 0 {
		return fmt.Errorf("terminal_events requires mode stream")
	}
	seen := map[string]bool{}
	for _, event := range n.TerminalEvents {
		if !events[event] || seen[event] || strings.ContainsAny(event, "*?") {
			return fmt.Errorf("terminal event %q must be a unique exact topic in events", event)
		}
		seen[event] = true
	}
	if n.ExpiresAfter != "" {
		d, err := time.ParseDuration(n.ExpiresAfter)
		if err != nil || d <= 0 {
			return fmt.Errorf("expires_after must be a positive duration")
		}
	}
	return nil
}
