// Package testbrowser implements the local, ephemeral browser test protocol.
package testbrowser

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

const Protocol = "apteva-browser-test/v1"
const MaxArtifact = 768 << 10

type Policy struct {
	RuntimeID string            `json:"runtime_id"`
	Targets   map[string]string `json:"targets"`
}
type Options struct {
	Policy   Policy `json:"policy"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Locale   string `json:"locale"`
	Timezone string `json:"timezone"`
	Profile  string `json:"-"`
}
type Locator struct {
	TestID string `json:"test_id,omitempty"`
	Role   string `json:"role,omitempty"`
	Name   string `json:"name,omitempty"`
	Text   string `json:"text,omitempty"`
}
type Command struct {
	Action    string       `json:"action"`
	Target    string       `json:"target,omitempty"`
	Path      string       `json:"path,omitempty"`
	Locator   Locator      `json:"locator,omitempty"`
	Value     string       `json:"value,omitempty"`
	Sensitive bool         `json:"sensitive,omitempty"`
	Checked   bool         `json:"checked,omitempty"`
	File      *FixtureFile `json:"file,omitempty"`
}
type FixtureFile struct {
	Name   string `json:"name"`
	MIME   string `json:"mime"`
	Base64 string `json:"base64"`
}
type Observation struct {
	Count   int     `json:"count"`
	Visible bool    `json:"visible"`
	Enabled bool    `json:"enabled"`
	Text    string  `json:"text"`
	Value   *string `json:"value,omitempty"`
	Checked *bool   `json:"checked,omitempty"`
	Target  string  `json:"target"`
	Path    string  `json:"path"`
}
type Artifact struct {
	MIME   string `json:"mime"`
	SHA256 string `json:"sha256"`
	Base64 string `json:"base64"`
}
type Diagnostics struct {
	Errors         []string  `json:"errors"`
	Requests       []Request `json:"requests"`
	Blocked        int       `json:"blocked"`
	BoundaryDenied int       `json:"boundary_denied"`
	Truncated      bool      `json:"truncated"`
}
type Request struct {
	Method string `json:"method"`
	Origin string `json:"origin"`
	Target string `json:"target,omitempty"`
	Status int    `json:"status"`
}
type Result struct {
	Observation Observation `json:"observation"`
	Error       string      `json:"error,omitempty"`
	Dispatched  bool        `json:"action_dispatched"`
}

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func (p Policy) Validate() error {
	if !namePattern.MatchString(p.RuntimeID) || len(p.Targets) == 0 || len(p.Targets) > 12 {
		return errors.New("browser policy requires a runtime and 1–12 named targets")
	}
	for name, raw := range p.Targets {
		u, err := url.Parse(raw)
		if err != nil || !namePattern.MatchString(name) || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() == "" || u.RawPath != "" {
			return errors.New("test targets must be runtime-scoped HTTP endpoints")
		}
		ip := net.ParseIP(u.Hostname())
		prefix := "/api/environment-app-public/" + p.RuntimeID + "/api/apps/"
		parts := strings.Split(strings.TrimPrefix(u.Path, prefix), "/")
		if ip == nil || !ip.IsLoopback() || !strings.HasPrefix(u.Path, prefix) || len(parts) < 4 ||
			!namePattern.MatchString(parts[0]) || parts[1] != "_install" || parts[2] == "" || strings.Trim(parts[2], "0123456789") != "" ||
			!safePath(u.Path) {
			return errors.New("test target does not address a public route in the selected runtime")
		}
		if parts[3] == "" || parts[3] == "mcp" || parts[3] == "tools" || parts[3] == "_apteva" {
			return errors.New("test target must have an explicit public application route prefix")
		}
	}
	return nil
}
func safePath(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "%\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}
func (p Policy) Match(u *url.URL) string {
	if u == nil || u.Scheme != "http" || u.User != nil || !safePath(u.Path) {
		return ""
	}
	escaped := strings.ToLower(u.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return ""
	}
	for name, raw := range p.Targets {
		base, _ := url.Parse(raw)
		prefix := strings.TrimSuffix(base.Path, "/")
		if u.Scheme == base.Scheme && u.Host == base.Host && (u.Path == prefix || strings.HasPrefix(u.Path, prefix+"/")) {
			return name
		}
	}
	return ""
}
func (p Policy) URL(target, path string) (string, error) {
	raw, ok := p.Targets[target]
	if !ok || !safePath(strings.SplitN(path, "?", 2)[0]) {
		return "", errors.New("invalid target or relative navigation path")
	}
	base, _ := url.Parse(raw)
	relative, err := url.Parse(path)
	if err != nil || relative.IsAbs() || relative.Host != "" || relative.Fragment != "" {
		return "", errors.New("invalid relative navigation")
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + relative.Path
	base.RawQuery = relative.RawQuery
	if p.Match(base) != target {
		return "", errors.New("navigation is outside the browser policy")
	}
	return base.String(), nil
}
func (c Command) Validate() error {
	switch c.Action {
	case "navigate", "reload", "observe", "click", "fill", "select", "check", "upload":
	default:
		return errors.New("unsupported browser test action")
	}
	if c.Action == "navigate" {
		return nil
	}
	if c.Action == "reload" {
		return nil
	}
	l := c.Locator
	kinds := 0
	if l.TestID != "" {
		kinds++
	}
	if l.Role != "" {
		kinds++
	}
	if l.Text != "" {
		kinds++
	}
	if c.Action == "observe" && kinds == 0 && l.Name == "" {
		return nil
	}
	if kinds != 1 || l.Name != "" && l.Role == "" || len(l.TestID+l.Role+l.Name+l.Text) > 1024 {
		return errors.New("a browser target requires exactly one test ID, role/name, or text locator")
	}
	if c.Action == "upload" && (c.File == nil || c.File.Name == "" || len(c.File.Base64) > 128<<10 || strings.ContainsAny(c.File.Name, "/\\")) {
		return errors.New("upload requires a named fixture of at most 96 KiB")
	}
	return nil
}
func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("invalid protocol JSON: %v", err))
	}
	return string(b)
}
