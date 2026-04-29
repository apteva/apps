// Email Checker v0.1 — stateless email validation.
//
// Three live signals + three list lookups:
//
//   live:
//     - syntax (net/mail.ParseAddress, RFC 5322)
//     - DNS MX records (3s timeout)
//     - optional SMTP RCPT TO probe (separate tool, slow + unreliable)
//
//   lists:
//     - disposable provider (mailinator etc.)
//     - free provider (gmail/outlook/yahoo etc.)
//     - role account local-part (info@, support@, …)
//
// No DB. Every call resolves fresh. The SMTP probe is gated to its
// own tool because (a) it's the slow one and (b) its result is only
// trustworthy for self-hosted servers — Google/MS accept every RCPT
// and decide at DATA time, so we explicitly mark when the response
// is informative versus not.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const manifestYAML = `schema: apteva-app/v1
name: email-checker
display_name: Email Checker
version: 0.1.0
description: |
  Stateless email validation — syntax, DNS MX, disposable/free/role
  classification, optional SMTP probe.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - net.egress
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: email_check,      description: "Full check — syntax + MX + classification." }
    - { name: email_check_smtp, description: "SMTP RCPT TO probe. Slow, unreliable on Google/MS." }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/email-checker
  port: 8080
  health_check: /health
upgrade_policy: auto-patch
`

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(*sdk.AppCtx) error         { return nil }
func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// REST mirror of the main MCP tool — handy for quick curl
		// checks and for the dashboard if a panel ever lands.
		{Pattern: "/check", Handler: a.httpCheck},
	}
}

func (a *App) httpCheck(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	if email == "" {
		http.Error(w, "?email= required", http.StatusBadRequest)
		return
	}
	writeJSON(w, check(email))
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "email_check",
			Description: "Validate an email. Args: email. Returns {email, valid, reasons[], syntax_ok, domain, mx[], disposable, role, free}.",
			InputSchema: schemaObject(map[string]any{
				"email": map[string]any{"type": "string"},
			}, []string{"email"}),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				email, _ := args["email"].(string)
				if email == "" {
					return nil, errors.New("email required")
				}
				return check(email), nil
			},
		},
		{
			Name: "email_check_smtp",
			Description: "Connect to the domain's MX, do EHLO + MAIL FROM <> + RCPT TO, return the raw status. " +
				"Slow (1–5s). Outbound :25 is blocked on most cloud hosts. Google/Microsoft accept every RCPT — " +
				"`informative=false` flags responses that can't tell mailbox-exists from mailbox-doesn't-exist. " +
				"Args: email, timeout_seconds? (default 5).",
			InputSchema: schemaObject(map[string]any{
				"email":           map[string]any{"type": "string"},
				"timeout_seconds": map[string]any{"type": "integer"},
			}, []string{"email"}),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				email, _ := args["email"].(string)
				if email == "" {
					return nil, errors.New("email required")
				}
				timeout := 5 * time.Second
				if v, ok := args["timeout_seconds"].(float64); ok && v > 0 {
					timeout = time.Duration(v) * time.Second
				}
				return checkSMTP(email, timeout), nil
			},
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Live checks ───────────────────────────────────────────────────

type CheckResult struct {
	Email      string   `json:"email"`
	Valid      bool     `json:"valid"`
	Reasons    []string `json:"reasons"`
	SyntaxOK   bool     `json:"syntax_ok"`
	Domain     string   `json:"domain,omitempty"`
	MX         []string `json:"mx,omitempty"`
	Disposable bool     `json:"disposable"`
	Role       bool     `json:"role"`
	Free       bool     `json:"free"`
}

func check(input string) CheckResult {
	res := CheckResult{Email: strings.TrimSpace(input), Reasons: []string{}}

	// 1. Syntax — net/mail handles RFC 5322 + display-name forms.
	addr, err := mail.ParseAddress(res.Email)
	if err != nil {
		res.Reasons = append(res.Reasons, "bad_syntax")
		return res
	}
	res.SyntaxOK = true
	// Strip the display name, lowercase the addr-spec — DNS is case
	// insensitive, MX lookup is normalized, and our list lookups are
	// keyed off lowercase.
	res.Email = strings.ToLower(addr.Address)
	at := strings.LastIndex(res.Email, "@")
	if at < 0 {
		res.Reasons = append(res.Reasons, "bad_syntax")
		return res
	}
	local := res.Email[:at]
	res.Domain = res.Email[at+1:]

	// 2. DNS MX. 3s timeout is enough for any reasonable resolver
	// and bounded so a dead domain doesn't hang the agent's tool call.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	mxs, _ := net.DefaultResolver.LookupMX(ctx, res.Domain)
	res.MX = make([]string, 0, len(mxs))
	for _, mx := range mxs {
		res.MX = append(res.MX, strings.TrimSuffix(mx.Host, "."))
	}
	if len(res.MX) == 0 {
		res.Reasons = append(res.Reasons, "no_mx_records")
	}

	// 3. List classification — these are inherently reputation/role
	// labels, not DNS-derivable. Bundled lists are best-effort; a
	// disposable provider added yesterday won't be flagged until the
	// next list refresh ships in a release.
	res.Disposable = disposableDomains[res.Domain]
	res.Free = freeDomains[res.Domain]
	res.Role = roleLocalParts[local]
	if res.Disposable {
		res.Reasons = append(res.Reasons, "disposable_domain")
	}
	if res.Role {
		res.Reasons = append(res.Reasons, "role_account")
	}

	// Final verdict. Free + role are signals an agent might want to
	// weight ("don't auto-trust signups from gmail" / "info@ is
	// probably a shared mailbox") but they're not by themselves
	// invalid — leave them out of the disqualifying-reasons set.
	res.Valid = res.SyntaxOK && len(res.MX) > 0 && !res.Disposable
	return res
}

// ─── SMTP probe ─────────────────────────────────────────────────────

type SMTPResult struct {
	Email       string `json:"email"`
	MX          string `json:"mx,omitempty"`
	RcptStatus  string `json:"rcpt_status"`        // ok | reject | tempfail | timeout | blocked | connect_failed | no_mx | bad_syntax | unknown
	Code        int    `json:"code,omitempty"`     // SMTP reply code from RCPT TO
	Response    string `json:"response,omitempty"` // raw SMTP text
	Informative bool   `json:"informative"`        // whether rcpt_status actually tells us mailbox-exists
	Note        string `json:"note,omitempty"`     // human-readable caveat ("Google accepts all RCPTs", etc.)
}

func checkSMTP(input string, timeout time.Duration) SMTPResult {
	res := SMTPResult{Email: strings.TrimSpace(input)}
	addr, err := mail.ParseAddress(res.Email)
	if err != nil {
		res.RcptStatus = "bad_syntax"
		return res
	}
	email := strings.ToLower(addr.Address)
	at := strings.LastIndex(email, "@")
	if at < 0 {
		res.RcptStatus = "bad_syntax"
		return res
	}
	domain := email[at+1:]
	res.Email = email

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	mxs, err := net.DefaultResolver.LookupMX(ctx, domain)
	if err != nil || len(mxs) == 0 {
		res.RcptStatus = "no_mx"
		return res
	}
	mxHost := strings.TrimSuffix(mxs[0].Host, ".")
	res.MX = mxHost
	res.Note, res.Informative = smtpProviderNote(mxHost)

	// Connect on :25 with the user's timeout. Cloud hosts (Hetzner,
	// AWS, GCP) block outbound 25 by default; treat connect failure
	// distinct from timeout so the operator can tell "blocked at the
	// edge" from "server isn't answering."
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(mxHost, "25"))
	if err != nil {
		if strings.Contains(err.Error(), "i/o timeout") {
			res.RcptStatus = "timeout"
		} else {
			res.RcptStatus = "blocked"
		}
		res.Response = err.Error()
		res.Informative = false
		return res
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	c, err := smtp.NewClient(conn, mxHost)
	if err != nil {
		res.RcptStatus = "connect_failed"
		res.Response = err.Error()
		res.Informative = false
		return res
	}
	defer c.Close()

	if err := c.Hello("checker.apteva.local"); err != nil {
		res.RcptStatus = "connect_failed"
		res.Response = err.Error()
		res.Informative = false
		return res
	}
	// Empty MAIL FROM (the standard probe form — RFC 5321 §4.5.5).
	if err := c.Mail(""); err != nil {
		res.RcptStatus = "connect_failed"
		res.Response = err.Error()
		res.Informative = false
		return res
	}
	err = c.Rcpt(email)
	_ = c.Quit()
	if err == nil {
		res.Code = 250
		res.RcptStatus = "ok"
		return res
	}
	// net/smtp wraps SMTP errors in textproto.Error — extract code.
	res.Response = err.Error()
	if code, msg := parseSMTPErr(err); code != 0 {
		res.Code = code
		res.Response = msg
		switch {
		case code >= 500 && code < 600:
			res.RcptStatus = "reject"
		case code >= 400 && code < 500:
			// 4xx is greylisting / temp fail. Not a definitive answer.
			res.RcptStatus = "tempfail"
			res.Informative = false
		default:
			res.RcptStatus = "unknown"
			res.Informative = false
		}
		return res
	}
	res.RcptStatus = "unknown"
	res.Informative = false
	return res
}

// parseSMTPErr extracts the numeric reply code + message from an
// error returned by net/smtp. Returns (0, "") if the error isn't
// an SMTP-level reply (e.g. it's a transport error from the
// underlying conn).
func parseSMTPErr(err error) (int, string) {
	// net/smtp surfaces SMTP errors as textproto.Error which formats
	// as "<code> <message>". Avoiding the import by parsing the
	// formatted string keeps the dependency surface tight.
	s := err.Error()
	if len(s) < 4 || s[3] != ' ' {
		return 0, s
	}
	code := 0
	for i := 0; i < 3; i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, s
		}
		code = code*10 + int(c-'0')
	}
	return code, strings.TrimSpace(s[4:])
}

// smtpProviderNote returns a caveat string + whether the provider's
// RCPT TO replies actually distinguish mailbox-exists from
// mailbox-doesn't-exist. Google + Microsoft famously accept every
// RCPT and decide at DATA time, so a 250 from them tells us nothing
// useful for existence — flag the result non-informative so callers
// (agents) can weight it correctly.
func smtpProviderNote(mxHost string) (note string, informative bool) {
	h := strings.ToLower(mxHost)
	switch {
	case strings.HasSuffix(h, ".google.com") || strings.HasSuffix(h, ".googlemail.com"):
		return "Google Workspace accepts every RCPT and validates at DATA time — 250 doesn't mean the mailbox exists.", false
	case strings.HasSuffix(h, ".outlook.com") || strings.HasSuffix(h, ".protection.outlook.com") || strings.Contains(h, "office365"):
		return "Microsoft 365 accepts every RCPT — 250 doesn't mean the mailbox exists.", false
	case strings.HasSuffix(h, ".yahoodns.net") || strings.HasSuffix(h, ".yahoo.com"):
		return "Yahoo aggressively rate-limits SMTP probes; results are unreliable.", false
	}
	// Self-hosted Postfix / Exim / Exchange typically reply honestly.
	return "", true
}

// ─── helpers ───────────────────────────────────────────────────────

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(body)
}
