// Package computer defines the Computer interface for screen-based environments.
// This package contains only the interface and types — no implementations.
// Implementations live under this app in internal/browser subpackages.
package api

import (
	"fmt"
	"strings"
)

// Action represents a normalized computer use action.
type Action struct {
	Type      string   `json:"type"`                // "click", "double_click", "type", "key", "scroll", "screenshot", "navigate", "wait", "select_option", "set_checked", "set_temporal"
	X         int      `json:"x,omitempty"`         // click/scroll coordinate
	Y         int      `json:"y,omitempty"`         // click/scroll coordinate
	Selector  string   `json:"selector,omitempty"`  // CSS selector for DOM-targeted actions like upload_file
	Files     []string `json:"files,omitempty"`     // local or provider-session file paths for upload_file
	Text      string   `json:"text,omitempty"`      // for "type" action
	Value     string   `json:"value,omitempty"`     // for "select_option": option value; for "set_temporal": full field value
	Checked   bool     `json:"checked,omitempty"`   // for "set_checked": desired checkbox/switch/radio state
	Texts     []string `json:"texts,omitempty"`     // for "select_option": option display texts
	Values    []string `json:"values,omitempty"`    // for "select_option": option values
	Mode      string   `json:"mode,omitempty"`      // for "select_option": replace, add, remove, toggle
	Key       string   `json:"key,omitempty"`       // for "key" action (e.g. "Enter", "Escape")
	Direction string   `json:"direction,omitempty"` // for "scroll": "up", "down", "left", "right"
	Amount    int      `json:"amount,omitempty"`    // for "scroll": CSS pixels; defaults to 300
	URL       string   `json:"url,omitempty"`       // for "navigate"
	Duration  int      `json:"duration,omitempty"`  // for "wait" (milliseconds)
	// Label: Set-of-Mark target. When non-zero, click/double_click
	// resolve the target via the label→bbox map populated by the
	// most recent screenshot. Takes precedence over X/Y when set.
	// Implementations that don't support SoM fall back to X/Y.
	Label int `json:"label,omitempty"`
}

// ScrollDelta converts a scroll action into CDP wheel deltas. Amount is
// intentionally CSS pixels, not wheel ticks: MCP callers commonly pass values
// like 80, 240, or 500 expecting viewport-distance scrolling.
func ScrollDelta(direction string, amount int) (float64, float64, error) {
	if amount <= 0 {
		amount = 300
	}
	var dx, dy float64
	switch strings.ToLower(direction) {
	case "up":
		dy = float64(-amount)
	case "down":
		dy = float64(amount)
	case "left":
		dx = float64(-amount)
	case "right":
		dx = float64(amount)
	default:
		return 0, 0, fmt.Errorf("unknown scroll direction %q (want up/down/left/right)", direction)
	}
	return dx, dy, nil
}

// DisplaySize holds screen dimensions.
type DisplaySize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// ScreenshotOptions controls optional post-processing for a screenshot.
type ScreenshotOptions struct {
	// Annotate composites Set-of-Mark labels onto the returned pixels and
	// refreshes the label map used by click(label=N). Clean capture/export
	// callers should set this false.
	Annotate bool
}

// ScreenshotRecoveryInfo describes a provider-specific screenshot fallback.
// Empty means the screenshot came from the normal capture path.
type ScreenshotRecoveryInfo struct {
	Recovered     bool   `json:"recovered"`
	Strategy      string `json:"strategy"`
	PreviousTabID string `json:"previous_tab_id,omitempty"`
	ActiveTabID   string `json:"active_tab_id,omitempty"`
	URL           string `json:"url,omitempty"`
	Cause         string `json:"cause,omitempty"`
}

// Context binds a session to a persistent state bundle (cookies,
// localStorage, IndexedDB, ServiceWorkers, Cache) that survives across
// sessions. Per-provider mapping:
//
//   - Browserbase   → browserSettings.context = {id, persist}
//   - Browser Engine → context = {id, persist}
//   - Steel         → profileId / persistProfile (Steel calls these
//     "profiles" but the lifecycle and intent match)
//
// IDs are provider-scoped: a Browserbase context id will not resolve on
// Steel, and vice versa. Concurrent attaches to the same context on the
// same provider are unsafe (Chrome can't share a user-data-dir); each
// backend serializes or 409s. Local / service backends ignore Context.
type Context struct {
	// ID is the provider-issued identifier returned at context-create time.
	ID string `json:"id"`
	// Persist controls whether changes (new cookies, storage writes) are
	// saved back to the context at session close. Default true mirrors
	// Browserbase's default; set false for one-shot read-only attaches.
	Persist bool `json:"persist"`
}

// Computer is the interface for screen-based environments.
type Computer interface {
	// Execute performs an action and returns a screenshot.
	Execute(action Action) (screenshot []byte, err error)

	// Screenshot takes a screenshot without performing any action.
	Screenshot() ([]byte, error)

	// DisplaySize returns the screen dimensions.
	DisplaySize() DisplaySize

	// Close terminates the session and releases resources.
	Close() error
}

// ActionOnlyExecutor is implemented by backends that can dispatch an action
// without immediately capturing a screenshot. This is useful for providers
// where post-action capture can be flaky on some SPA transitions, while
// preserving Computer.Execute's historical action+screenshot contract.
type ActionOnlyExecutor interface {
	ExecuteAction(action Action) error
}

// TabInfo describes one browser page target inside a provider session.
type TabInfo struct {
	ID       string `json:"tab_id"`
	URL      string `json:"url"`
	Title    string `json:"title,omitempty"`
	Active   bool   `json:"active"`
	OpenerID string `json:"opener_tab_id,omitempty"`
}

// TabController is implemented by CDP-backed providers that can expose and
// switch between multiple page targets within one browser session.
type TabController interface {
	ListTabs() ([]TabInfo, error)
	ActiveTabID() string
	SwitchTab(tabID string) error
	CloseTab(tabID string) error
}

// ScreenshotWithOptions is implemented by backends that support clean
// screenshots without Set-of-Mark annotation on a per-call basis.
type ScreenshotWithOptions interface {
	ScreenshotWithOptions(options ScreenshotOptions) ([]byte, error)
}

// ScreenshotRecoveryReporter is implemented by backends that can report
// whether the most recent screenshot used a provider-specific fallback.
type ScreenshotRecoveryReporter interface {
	LastScreenshotRecovery() *ScreenshotRecoveryInfo
}

// OpenOptions describes a session-open intent: which url to land on,
// which persistent context (if any) to bind, and whether to attach to
// an existing session id instead of creating a new one. The agent owns
// these decisions — they're tool-call arguments, not factory config.
type OpenOptions struct {
	// URL to navigate to after the session is established. Optional;
	// when empty the session is opened but no navigation is issued
	// (useful for resume to a session that's already on a page).
	URL string

	// ContextID binds the new session to a persistent context. Mutually
	// exclusive with SessionID. Provider-scoped — see Context.
	ContextID string
	// CreateContext asks a backend that can materialize a context/profile
	// during session creation to do so and expose the provider id via
	// ContextInfo after OpenSession returns. Steel uses this for first-run
	// profile creation; Browserbase contexts are created through its
	// explicit REST API before OpenSession.
	CreateContext bool
	// Persist controls whether changes are saved back to the context
	// at session close. Defaults true (matches Browserbase default).
	Persist bool

	// SessionID, when set, attaches to an existing session instead of
	// creating a new one. Mutually exclusive with ContextID. Provider
	// requirements vary: Browserbase needs the session to have been
	// created with KeepAlive=true; Browser Engine accepts both live
	// and snapshot-saved sessions; Steel and local backends reject it.
	SessionID string

	// Timeout sets the new session's max lifetime in seconds. Ignored
	// for SessionID attaches (the timeout was set at original create).
	// Zero leaves the provider's server-side default in place.
	Timeout int

	// Proxy, when non-nil, decides whether the new session routes
	// egress through the backend's managed residential proxy. nil
	// leaves the harness/backend default; &true forces on; &false
	// forces off. Honored by browser-engine, browserbase, steel;
	// ignored by local. Set by the agent via the browser_session
	// open tool — the agent owns the policy decision.
	Proxy *bool

	// ProxyCountry is an ISO-2 country code for the residential
	// proxy exit (e.g. "US"). Honored by browser-engine; ignored by
	// browserbase + steel (they need a custom proxy list for that).
	ProxyCountry string
}

// SessionOpener is implemented by Computers that own session lifecycle.
// One method covers create-with-context, attach-by-id, and re-bind to
// a different context — all by varying OpenOptions. Implementations
// MUST tear down the current session (if different) before establishing
// the new one. Local / service backends implement this as a thin nav.
type SessionOpener interface {
	OpenSession(opts OpenOptions) error
}

// SessionInfo is implemented by backends that can report provider
// session metadata.
type SessionInfo interface {
	SessionType() string
	SessionID() string
	CurrentURL() string
}

// ContextInfo is implemented by backends currently bound to a
// persistent context/profile.
type ContextInfo interface {
	ContextID() string
}

// Resumable is the older attach-by-session-id interface. New backends
// should implement SessionOpener instead.
type Resumable interface {
	Resume(sessionID string) error
}

// Timeoutable is implemented by backends whose active session lease can
// be extended after creation.
type Timeoutable interface {
	ExtendTimeout(seconds int) error
}
