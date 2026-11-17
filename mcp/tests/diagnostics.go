package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// DiagnosticsConfig holds per-check rules for page problems that Computer
// records in existing-mode browser sessions (Computer 0.14.0 or newer).
//
// Defaults: any page exception fails the check; a request to a declared
// target's origin (or a fail_origins entry) that returns 400 or more, or fails
// without a response, fails the check. Other requests and console errors are
// recorded only.
type DiagnosticsConfig struct {
	Off           bool              `json:"-"`
	FailOn        []string          `json:"fail_on,omitempty"`
	Allow         []DiagnosticAllow `json:"allow,omitempty"`
	ConsoleErrors string            `json:"console_errors,omitempty"`
	FailOrigins   []string          `json:"fail_origins,omitempty"`
}

// DiagnosticAllow exempts matching requests from the failure rules.
type DiagnosticAllow struct {
	PathPrefix string `json:"path_prefix"`
	Status     int    `json:"status,omitempty"`
	Origin     string `json:"origin,omitempty"`
}

func (d *DiagnosticsConfig) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil || text != "off" {
			return errors.New(`diagnostics must be "off" or an object`)
		}
		*d = DiagnosticsConfig{Off: true}
		return nil
	}
	type plain DiagnosticsConfig
	var v plain
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return err
	}
	*d = DiagnosticsConfig(v)
	return nil
}

func (d DiagnosticsConfig) MarshalJSON() ([]byte, error) {
	if d.Off {
		return []byte(`"off"`), nil
	}
	type plain DiagnosticsConfig
	return json.Marshal(plain(d))
}

func (d *DiagnosticsConfig) validate() error {
	if d == nil || d.Off {
		return nil
	}
	for _, rule := range d.FailOn {
		if rule != "exceptions" && rule != "target_requests" {
			return fmt.Errorf("diagnostics.fail_on accepts exceptions and target_requests, not %q", rule)
		}
	}
	if d.ConsoleErrors != "" && d.ConsoleErrors != "record" && d.ConsoleErrors != "fail" {
		return errors.New(`diagnostics.console_errors must be "record" or "fail"`)
	}
	if len(d.Allow) > 50 || len(d.FailOrigins) > 20 {
		return errors.New("diagnostics accepts at most 50 allow entries and 20 fail_origins")
	}
	for _, allow := range d.Allow {
		if !strings.HasPrefix(allow.PathPrefix, "/") || len(allow.PathPrefix) > 512 || allow.Status < 0 || allow.Status > 599 {
			return errors.New("diagnostics.allow entries need a path_prefix starting with / and an optional status")
		}
		if allow.Origin != "" {
			if _, ok := httpOrigin(allow.Origin); !ok {
				return fmt.Errorf("diagnostics.allow origin %q is not an http(s) origin", allow.Origin)
			}
		}
	}
	for _, origin := range d.FailOrigins {
		if _, ok := httpOrigin(origin); !ok {
			return fmt.Errorf("diagnostics.fail_origins entry %q is not an http(s) origin", origin)
		}
	}
	return nil
}

func httpOrigin(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), true
}

// pageRequest is one entry from Computer's browser_diagnostics.
type pageRequest struct {
	Method string `json:"method,omitempty"`
	Origin string `json:"origin"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	Error  string `json:"error,omitempty"`
	Type   string `json:"type,omitempty"`
}

// diagnosticRules is a check's resolved rule set.
type diagnosticRules struct {
	exceptions, requests, console, truncated bool
	origins                                  map[string]bool
	allow                                    []DiagnosticAllow
}

func resolveDiagnosticRules(config *DiagnosticsConfig, targets map[string]*url.URL) (diagnosticRules, bool) {
	if config != nil && config.Off {
		return diagnosticRules{}, false
	}
	rules := diagnosticRules{exceptions: true, requests: true, origins: map[string]bool{}}
	for _, target := range targets {
		if origin, ok := httpOrigin(target.String()); ok {
			rules.origins[origin] = true
		}
	}
	if config == nil {
		return rules, true
	}
	if config.FailOn != nil {
		// An explicit fail_on also makes truncated diagnostics a failure,
		// since a dropped entry could hide a listed problem.
		rules.exceptions, rules.requests, rules.truncated = false, false, true
		for _, rule := range config.FailOn {
			rules.exceptions = rules.exceptions || rule == "exceptions"
			rules.requests = rules.requests || rule == "target_requests"
		}
	}
	rules.console = config.ConsoleErrors == "fail"
	for _, origin := range config.FailOrigins {
		if o, ok := httpOrigin(origin); ok {
			rules.origins[o] = true
		}
	}
	rules.allow = config.Allow
	return rules, true
}

// failedRequest reports whether a request breaks the rules. Requests the
// browser aborted itself (for example on navigation) are recorded only.
func (r diagnosticRules) failedRequest(req pageRequest) bool {
	if !r.requests || !r.origins[strings.ToLower(req.Origin)] {
		return false
	}
	if req.Status < 400 && (req.Status != 0 || req.Error == "" || strings.Contains(req.Error, "ERR_ABORTED")) {
		return false
	}
	for _, allow := range r.allow {
		if strings.HasPrefix(req.Path, allow.PathPrefix) && (allow.Status == 0 || allow.Status == req.Status) &&
			(allow.Origin == "" || strings.EqualFold(strings.TrimSuffix(allow.Origin, "/"), req.Origin)) {
			return false
		}
	}
	return true
}

func describeRequest(req pageRequest) string {
	method := req.Method
	if method == "" {
		method = "request"
	}
	if req.Status == 0 {
		return fmt.Sprintf("%s %s%s failed: %s", method, req.Origin, req.Path, firstLine(req.Error))
	}
	return fmt.Sprintf("%s %s%s returned %d", method, req.Origin, req.Path, req.Status)
}
