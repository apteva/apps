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
	Type         string              `json:"type"`                   // "click", "double_click", "type", "key", "scroll", "screenshot", "navigate", "back", "reload", "wait", "select_option", "set_checked", "set_temporal", "set_text"
	X            int                 `json:"x,omitempty"`            // click/scroll coordinate
	Y            int                 `json:"y,omitempty"`            // click/scroll coordinate
	Selector     string              `json:"selector,omitempty"`     // CSS selector for click and DOM-targeted form actions
	Files        []string            `json:"files,omitempty"`        // local or provider-session file paths for upload_file
	Text         string              `json:"text,omitempty"`         // for "type" action
	Value        string              `json:"value,omitempty"`        // for "select_option": option value; for "set_temporal": full field value
	Checked      bool                `json:"checked,omitempty"`      // for "set_checked": desired checkbox/switch/radio state
	Texts        []string            `json:"texts,omitempty"`        // for "select_option": option display texts
	Values       []string            `json:"values,omitempty"`       // for "select_option": option values
	Mode         string              `json:"mode,omitempty"`         // for "select_option": replace, add, remove, toggle; for "set_text": replace, append
	NewlineMode  string              `json:"newline_mode,omitempty"` // for "set_text": preserve, compact
	Key          string              `json:"key,omitempty"`          // for "key" action (e.g. "Enter", "Escape")
	Direction    string              `json:"direction,omitempty"`    // for "scroll": "up", "down", "left", "right"
	Amount       int                 `json:"amount,omitempty"`       // for "scroll": CSS pixels; defaults to 300
	URL          string              `json:"url,omitempty"`          // for "navigate"
	Duration     int                 `json:"duration,omitempty"`     // for "wait" (milliseconds)
	Presentation PresentationOptions `json:"-"`
	// Label: Set-of-Mark target. When non-zero, click/double_click
	// resolve the target via the label→bbox map populated by the
	// most recent screenshot. Takes precedence over X/Y when set.
	// Implementations that don't support SoM fall back to X/Y.
	Label int `json:"label,omitempty"`
}

// PresentationOptions makes browser actions legible in live views and hosted
// session recordings. The zero value preserves the existing fast automation
// behavior. Computer attaches the normalized session preset to every action so
// concrete backends do not need to know about MCP/session arguments.
type PresentationOptions struct {
	Mode              string `json:"mode,omitempty"`
	ShowCursor        bool   `json:"show_cursor,omitempty"`
	TypingDelayMS     int    `json:"typing_delay_ms,omitempty"`
	PointerDurationMS int    `json:"pointer_duration_ms,omitempty"`
	ClickEffectMS     int    `json:"click_effect_ms,omitempty"`
	PostActionDelayMS int    `json:"post_action_delay_ms,omitempty"`
}

func (p PresentationOptions) Enabled() bool {
	return p.Mode == "demo"
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

// ExtractOptions controls browser-DOM extraction from the currently active
// page. This is intentionally narrower than arbitrary JavaScript evaluation:
// callers can request structured page data without gaining script execution.
type ExtractOptions struct {
	Formats     []string `json:"formats,omitempty"`
	MaxChars    int      `json:"max_chars,omitempty"`
	Readability bool     `json:"readability"`
	WaitMS      int      `json:"wait_ms,omitempty"`
}

// ExtractLink is a normalized anchor from the rendered page.
type ExtractLink struct {
	URL  string `json:"url"`
	Text string `json:"text,omitempty"`
}

// ExtractRect is a rendered DOM rectangle in CSS pixels.
type ExtractRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// ExtractRegion is a query-rankable rendered DOM block with text and geometry.
// Rect uses document CSS pixels so callers can scroll to Y and crop viewport
// screenshots deterministically.
type ExtractRegion struct {
	ID              string      `json:"id"`
	Tag             string      `json:"tag,omitempty"`
	Role            string      `json:"role,omitempty"`
	Selector        string      `json:"selector,omitempty"`
	Heading         string      `json:"heading,omitempty"`
	Text            string      `json:"text,omitempty"`
	Rect            ExtractRect `json:"rect"`
	ViewportRect    ExtractRect `json:"viewport_rect"`
	CoordinateFrame string      `json:"coordinate_frame"`
	Visible         bool        `json:"visible"`
	LinkCount       int         `json:"link_count,omitempty"`
	ImageCount      int         `json:"image_count,omitempty"`
}

// ExtractResult is structured content read from the live rendered DOM.
type ExtractResult struct {
	URL               string          `json:"url"`
	Title             string          `json:"title,omitempty"`
	Description       string          `json:"description,omitempty"`
	Text              string          `json:"text,omitempty"`
	Markdown          string          `json:"markdown,omitempty"`
	HTML              string          `json:"html,omitempty"`
	Links             []ExtractLink   `json:"links,omitempty"`
	Images            []string        `json:"images,omitempty"`
	Regions           []ExtractRegion `json:"regions,omitempty"`
	Metadata          map[string]any  `json:"metadata,omitempty"`
	StructuredData    map[string]any  `json:"structured_data,omitempty"`
	Rendered          bool            `json:"rendered"`
	ExtractionBackend string          `json:"extraction_backend"`
}

// SetOfMarkTarget is one visible interactive target from the latest
// Set-of-Mark screenshot. Coordinates are viewport CSS pixels and label is the
// value accepted by click/double_click label=N.
type SetOfMarkTarget struct {
	Label int    `json:"label"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	W     int    `json:"w"`
	H     int    `json:"h"`
	Tag   string `json:"tag"`
	Role  string `json:"role,omitempty"`
	Text  string `json:"text,omitempty"`
	Type  string `json:"type,omitempty"`
}

// DOMExtractor is implemented by browser backends that can read structured
// content from the active page's live DOM.
type DOMExtractor interface {
	ExtractDOM(opts ExtractOptions) (ExtractResult, error)
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

// SetOfMarkReporter is implemented by backends that can expose the target
// map generated by the latest annotated screenshot. MCP callers must opt in to
// returning this payload because it can be large.
type SetOfMarkReporter interface {
	LastSetOfMark() []SetOfMarkTarget
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
	// proxy exit (e.g. "US"). Honored by browser-engine and
	// browserbase; ignored by steel.
	ProxyCountry string

	// ExternalProxy is a short-lived, server-resolved upstream proxy.
	// Credentials must never be logged, persisted, or returned to tool
	// callers. When set it takes precedence over managed Proxy settings.
	// Browserbase, Steel, and local Chrome honor it; Browser Engine and
	// service backends reject it until their APIs gain an upstream hook.
	ExternalProxy *ExternalProxy

	// Environment contains optional browser-visible identity and emulation
	// settings for a newly-created session. The zero value is intentionally a
	// no-op so existing agent calls preserve the backend's current defaults.
	// Backends must reject applying it while attaching to an existing provider
	// session because that would mutate an already-established browser identity.
	Environment EnvironmentOptions
}

// EnvironmentOptions is a provider-neutral, opt-in browser environment.
// Pointer scalar fields distinguish an explicitly requested false/zero value
// from an omitted setting. Backends apply these settings before the first
// requested URL is loaded and reapply them when switching targets.
type EnvironmentOptions struct {
	UserAgent         string              `json:"user_agent,omitempty"`
	Locale            string              `json:"locale,omitempty"`
	Languages         []string            `json:"languages,omitempty"`
	Timezone          string              `json:"timezone,omitempty"`
	Geolocation       *GeolocationOptions `json:"geolocation,omitempty"`
	DeviceScaleFactor *float64            `json:"device_scale_factor,omitempty"`
	Mobile            *bool               `json:"mobile,omitempty"`
	Touch             *bool               `json:"touch,omitempty"`
	MaxTouchPoints    *int                `json:"max_touch_points,omitempty"`
}

// IsEmpty reports whether applying the environment would be a no-op.
func (o EnvironmentOptions) IsEmpty() bool {
	return o.UserAgent == "" && o.Locale == "" && len(o.Languages) == 0 &&
		o.Timezone == "" && o.Geolocation == nil && o.DeviceScaleFactor == nil &&
		o.Mobile == nil && o.Touch == nil && o.MaxTouchPoints == nil
}

type GeolocationOptions struct {
	Latitude   *float64 `json:"latitude"`
	Longitude  *float64 `json:"longitude"`
	Accuracy   *float64 `json:"accuracy,omitempty"`
	Permission string   `json:"permission,omitempty"`
}

// ExternalProxy contains the minimum provider-neutral connection material
// needed by a browser backend. Server must be scheme://host:port and must not
// contain credentials; Username and Password are passed separately so errors
// and safe session metadata never need to render a credential-bearing URL.
type ExternalProxy struct {
	Server   string
	Username string
	Password string
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
