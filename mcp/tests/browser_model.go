package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

const browserProtocol = "apteva-browser-test/v1"

type BrowserConfig struct {
	Width     int                    `json:"width"`
	Height    int                    `json:"height"`
	Locale    string                 `json:"locale"`
	Timezone  string                 `json:"timezone"`
	ValueRefs map[string]ToolProbe   `json:"value_refs,omitempty"`
	Uploads   map[string]BrowserFile `json:"uploads,omitempty"`
}
type BrowserFile struct {
	Name   string `json:"name"`
	MIME   string `json:"mime"`
	Base64 string `json:"base64"`
}
type BrowserLocator struct {
	TestID string `json:"test_id,omitempty"`
	Role   string `json:"role,omitempty"`
	Name   string `json:"name,omitempty"`
	Text   string `json:"text,omitempty"`
}
type BrowserAction struct {
	Action    string         `json:"action"`
	Target    string         `json:"target,omitempty"`
	Path      string         `json:"path,omitempty"`
	Locator   BrowserLocator `json:"locator,omitempty"`
	Value     *string        `json:"value,omitempty"`
	ValueRef  string         `json:"value_ref,omitempty"`
	UploadRef string         `json:"upload_ref,omitempty"`
	Sensitive bool           `json:"sensitive,omitempty"`
	Checked   bool           `json:"checked,omitempty"`
}

// UnmarshalJSON keeps omitted and empty values distinct while rejecting null.
func (b *BrowserAction) UnmarshalJSON(data []byte) error {
	type action BrowserAction
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key, raw := range fields {
		if strings.EqualFold(key, "value") && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("browser value must be a string; null is not allowed")
		}
		if strings.EqualFold(key, "value_ref") {
			var ref string
			if err := json.Unmarshal(raw, &ref); err != nil || !browserRef.MatchString(ref) {
				return errors.New("invalid browser value_ref")
			}
		}
	}
	var decoded action
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*b = BrowserAction(decoded)
	return nil
}

type BrowserDiagnostics struct {
	Errors         []string          `json:"errors"`
	Requests       []json.RawMessage `json:"requests"`
	Blocked        int               `json:"blocked"`
	BoundaryDenied int               `json:"boundary_denied"`
	Truncated      bool              `json:"truncated"`
}
type BrowserEvidence struct {
	SessionID   string             `json:"session_id"`
	Protocol    string             `json:"protocol"`
	Engine      string             `json:"engine"`
	Width       int                `json:"width"`
	Height      int                `json:"height"`
	Locale      string             `json:"locale"`
	Timezone    string             `json:"timezone"`
	Diagnostics BrowserDiagnostics `json:"diagnostics"`
}

var browserRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func (c *BrowserConfig) validate() error {
	if c.Width == 0 {
		c.Width = 1280
	}
	if c.Height == 0 {
		c.Height = 720
	}
	if c.Locale == "" {
		c.Locale = "en-US"
	}
	if c.Timezone == "" {
		c.Timezone = "UTC"
	}
	if c.Width < 320 || c.Width > 1920 || c.Height < 240 || c.Height > 1200 || len(c.Locale) > 40 || len(c.Timezone) > 80 ||
		len(c.ValueRefs) > 32 || len(c.Uploads) > 12 {
		return errors.New("invalid browser viewport, environment, values or fixtures")
	}
	for name, probe := range c.ValueRefs {
		if !browserRef.MatchString(name) || !validProbe(probe) {
			return errors.New("invalid browser value reference")
		}
	}
	for name, file := range c.Uploads {
		if !slugPattern.MatchString(name) || file.Name == "" || strings.ContainsAny(file.Name, "/\\") || len(file.Base64) > 128<<10 {
			return errors.New("invalid browser upload fixture")
		}
	}
	return nil
}
func (s *Step) validateBrowser() error {
	if s.Browser == nil {
		return errors.New("browser step requires a browser action")
	}
	b := s.Browser
	switch b.Action {
	case "navigate":
		if !slugPattern.MatchString(b.Target) || validateGatewayPath(strings.SplitN(b.Path, "?", 2)[0]) != nil || b.Path == "" {
			return errors.New("browser navigation requires a named target and relative path")
		}
	case "reload", "observe", "click", "fill", "select", "check", "upload":
	default:
		return errors.New("invalid browser action")
	}
	l := b.Locator
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
	if b.Action != "navigate" && b.Action != "reload" && !(b.Action == "observe" && l == (BrowserLocator{})) {
		if kinds != 1 || l.Name != "" && l.Role == "" || len(encode(l)) > 2048 {
			return errors.New("browser locator requires one test ID, role/name or text")
		}
	}
	if b.ValueRef != "" && (!browserRef.MatchString(b.ValueRef) || b.Value != nil) {
		return errors.New("use a browser value or a value_ref")
	}
	if b.Action == "select" && b.Value == nil && b.ValueRef == "" {
		return errors.New("browser select requires exactly one value or value_ref")
	}
	if b.Action == "upload" && !slugPattern.MatchString(b.UploadRef) {
		return errors.New("browser upload requires upload_ref")
	}
	if s.PollEveryMS > 0 && b.Action != "observe" {
		return errors.New("only browser observations may poll")
	}
	if len(s.Assertions) == 0 {
		if b.Action == "observe" {
			return errors.New("browser observations require explicit assertions")
		}
		s.Assertions = []Assertion{{Path: "/action_dispatched", Op: "equals", Value: true}}
	}
	return nil
}
func hasBrowser(check Check) bool {
	if check.Kind == "browser" {
		return true
	}
	for _, step := range check.Definition.Steps {
		if step.Kind == "browser" {
			return true
		}
	}
	return false
}
