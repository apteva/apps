package main

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Registration input validation ────────────────────────────────
//
// Registration is unauthenticated by design (that's the funnel), which
// makes it a messaging amplifier if the address is taken on trust:
// whatever lands in email/phone is later handed to messaging as the
// `to` address for T-24h/T-1h/T-15m plus the we're-live blast. An
// attacker scripting registrations with third-party addresses or
// premium-rate numbers burns the owner's messaging credits and their
// sender reputation.
//
// Two guards live here and both are enforced in the *tool* layer
// (toolRegister) so the MCP path is covered too, not just HTTP:
//   1. strict format validation + normalization, and
//   2. a per-webinar registration budget (registrationLimiter).

const (
	maxEmailLen = 254
	maxLocalLen = 64
	maxNameLen  = 120
)

// NormalizeRegistrationContact validates and normalizes one
// registration's contact pair. Exactly one of email/phone is required;
// both are accepted. Returns user-safe errors — the text is surfaced
// straight to the registration form.
//
// Email is lowercased; phone is stripped of human formatting and must
// end up as E.164 ("+" then 8–15 digits).
func NormalizeRegistrationContact(email, phone string) (string, string, error) {
	email = strings.TrimSpace(email)
	phone = strings.TrimSpace(phone)
	if email == "" && phone == "" {
		return "", "", fmt.Errorf("email or phone required")
	}
	if email != "" {
		norm, err := normalizeEmail(email)
		if err != nil {
			return "", "", err
		}
		email = norm
	}
	if phone != "" {
		norm, err := normalizePhoneE164(phone)
		if err != nil {
			return "", "", err
		}
		phone = norm
	}
	return email, phone, nil
}

// normalizeEmail applies a deliberately strict subset of RFC 5321: one
// unquoted local part, one dotted domain, no whitespace, no control
// characters, no address-list syntax. Anything exotic is rejected
// rather than forwarded to the messaging provider.
func normalizeEmail(raw string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(raw))
	if len(e) > maxEmailLen {
		return "", fmt.Errorf("email is too long")
	}
	if strings.ContainsAny(e, " \t\r\n\"'<>,;:\\()[]") {
		return "", fmt.Errorf("email contains invalid characters")
	}
	at := strings.LastIndex(e, "@")
	if at <= 0 || at == len(e)-1 {
		return "", fmt.Errorf("email must look like name@example.com")
	}
	local, domain := e[:at], e[at+1:]
	if strings.Contains(local, "@") || len(local) > maxLocalLen {
		return "", fmt.Errorf("email must look like name@example.com")
	}
	for _, r := range local {
		if !isEmailLocalRune(r) {
			return "", fmt.Errorf("email contains invalid characters")
		}
	}
	if local[0] == '.' || local[len(local)-1] == '.' || strings.Contains(local, "..") {
		return "", fmt.Errorf("email must look like name@example.com")
	}
	if err := validateEmailDomain(domain); err != nil {
		return "", err
	}
	return e, nil
}

func isEmailLocalRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune(".!#$%&*+-/=?^_`{|}~", r)
}

// validateEmailDomain requires at least two labels, alphanumeric or
// hyphen, no leading/trailing hyphen, and a TLD of >= 2 letters.
func validateEmailDomain(domain string) error {
	if domain == "" || len(domain) > 253 {
		return fmt.Errorf("email domain is invalid")
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return fmt.Errorf("email domain must include a top-level domain")
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("email domain is invalid")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("email domain is invalid")
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return fmt.Errorf("email domain is invalid")
		}
	}
	tld := labels[len(labels)-1]
	if len(tld) < 2 {
		return fmt.Errorf("email domain must include a top-level domain")
	}
	for _, r := range tld {
		if r < 'a' || r > 'z' {
			return fmt.Errorf("email domain must include a top-level domain")
		}
	}
	return nil
}

// normalizePhoneE164 accepts human formatting (spaces, dashes, dots,
// parentheses, a leading "00" international prefix) and emits strict
// E.164. Anything that isn't a plausible dialable international number
// is rejected — SMS toll fraud is the failure mode we're guarding.
func normalizePhoneE164(raw string) (string, error) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && b.Len() == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')' || r == ' ':
			// human formatting — drop
		default:
			return "", fmt.Errorf("phone must be in international format, e.g. +15551234567")
		}
	}
	p := b.String()
	if strings.HasPrefix(p, "00") {
		p = "+" + p[2:]
	}
	if !strings.HasPrefix(p, "+") {
		return "", fmt.Errorf("phone must start with + and a country code, e.g. +15551234567")
	}
	digits := p[1:]
	if len(digits) < 8 || len(digits) > 15 {
		return "", fmt.Errorf("phone must be 8–15 digits after the country code")
	}
	if digits[0] == '0' {
		return "", fmt.Errorf("phone must start with + and a country code, e.g. +15551234567")
	}
	return p, nil
}

// sanitizeDisplayName trims control characters and caps length. Display
// names are echoed into chat and the CRM contact record.
func sanitizeDisplayName(raw string) string {
	out := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(raw))
	if len(out) > maxNameLen {
		out = out[:maxNameLen]
	}
	return out
}

// ─── CTA URL allowlist ────────────────────────────────────────────

// validateCTAURL restricts offer CTAs to http/https with a real host.
// The live room opens the value with window.open(), so a `javascript:`
// or `data:` CTA is script execution in every attendee's browser — and
// offers are agent-scriptable through webinars_post_offer /
// webinars_define_offer, so a prompt-injected agent would be enough.
func validateCTAURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("cta_url required")
	}
	if strings.ContainsAny(trimmed, "\r\n\t") {
		return "", fmt.Errorf("cta_url must not contain control characters")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("cta_url is not a valid URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("cta_url must be an http:// or https:// URL")
	}
	if u.Host == "" {
		return "", fmt.Errorf("cta_url must include a host")
	}
	return trimmed, nil
}

// ─── Per-webinar registration budget ──────────────────────────────

// errRegistrationRateLimited is the sentinel toolRegister returns once a
// webinar exceeds its per-minute registration budget. HTTP callers map
// it to 429; MCP callers see it as a plain tool error.
var errRegistrationRateLimited = simpleErr("too many registrations for this webinar right now — try again in a minute")

// defaultRegistrationsPerMinute is the per-webinar budget when
// `registration_rate_limit_per_minute` isn't configured. Generous for a
// real launch (a 60-per-minute funnel is a very good day) and still
// three orders of magnitude below what a script can push.
const defaultRegistrationsPerMinute = 60

// registrationLimiter is a fixed-window counter keyed by webinar. In
// memory on purpose: the point is to shed an abusive burst cheaply,
// before it reaches the single SQLite writer or the messaging provider.
// A sidecar restart resets the window, which is acceptable — the budget
// is an abuse guard, not an accounting ledger.
type registrationLimiter struct {
	mu      sync.Mutex
	windows map[int64]*registrationWindow
}

type registrationWindow struct {
	start time.Time
	count int
}

func newRegistrationLimiter() *registrationLimiter {
	return &registrationLimiter{windows: map[int64]*registrationWindow{}}
}

// allow consumes one token for webinarID. limit <= 0 disables the guard.
func (l *registrationLimiter) allow(webinarID int64, limit int, now time.Time) bool {
	if l == nil || limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.windows[webinarID]
	if !ok || now.Sub(w.start) >= time.Minute {
		l.windows[webinarID] = &registrationWindow{start: now, count: 1}
		l.gcLocked(now)
		return true
	}
	if w.count >= limit {
		return false
	}
	w.count++
	return true
}

// gcLocked drops windows that rolled over, so a long-lived sidecar
// doesn't accumulate one entry per webinar ever registered against.
func (l *registrationLimiter) gcLocked(now time.Time) {
	if len(l.windows) < 512 {
		return
	}
	for id, w := range l.windows {
		if now.Sub(w.start) >= time.Minute {
			delete(l.windows, id)
		}
	}
}

// registrationsPerMinute reads the configured budget.
func (a *App) registrationsPerMinute(ctx *sdk.AppCtx) int {
	raw := strings.TrimSpace(ctx.Config().Get("registration_rate_limit_per_minute"))
	if raw == "" {
		return defaultRegistrationsPerMinute
	}
	n := intArg(map[string]any{"v": raw}, "v", defaultRegistrationsPerMinute)
	if n < 0 {
		return 0
	}
	return n
}
