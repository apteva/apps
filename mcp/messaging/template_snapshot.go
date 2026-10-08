package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// A snapshot is message-owned, never joined to today's mutable template on read.
// Provider request Body stays empty for ContentSid sends.
type TemplateSnapshot struct {
	TemplateID         int64          `json:"template_id,omitempty"`
	Name               string         `json:"name,omitempty"`
	ProviderTemplateID string         `json:"provider_template_id,omitempty"`
	CapturedAt         string         `json:"captured_at"`
	ContentSource      string         `json:"content_source"`
	Availability       string         `json:"availability"`
	Variables          map[string]any `json:"variables,omitempty"`
	Subject            string         `json:"subject,omitempty"`
	OriginalBodyText   string         `json:"original_body_text,omitempty"`
	OriginalBodyHTML   string         `json:"original_body_html,omitempty"`
	BodyText           string         `json:"body_text,omitempty"`
	BodyHTML           string         `json:"body_html,omitempty"`
	MissingVariables   []string       `json:"missing_variables,omitempty"`
}

var snapshotVariable = regexp.MustCompile(`\{\{\s*([^{}\s]+)\s*\}\}`)

func captureTemplateSnapshot(tpl *Template, sid, contentVars, subject, body, html string) *TemplateSnapshot {
	if tpl == nil && sid == "" {
		return nil
	}
	s := &TemplateSnapshot{ProviderTemplateID: sid, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano), ContentSource: "send_time_template", Availability: "unavailable"}
	if contentVars != "" && json.Unmarshal([]byte(contentVars), &s.Variables) != nil {
		return s // do not invent values for malformed provider input
	}
	if tpl == nil {
		return s // direct ContentSid with no project-owned local content
	}
	s.TemplateID, s.Name = tpl.ID, tpl.Name
	s.OriginalBodyText, s.OriginalBodyHTML = tpl.BodyText, tpl.BodyHTML
	s.Subject, s.BodyText, s.BodyHTML = subject, body, html
	if sid == "" {
		s.Availability = "complete" // the exact local-template provider payload
		return s
	}
	missing := map[string]bool{}
	render := func(text string) string {
		return snapshotVariable.ReplaceAllStringFunc(text, func(slot string) string {
			key := snapshotVariable.FindStringSubmatch(slot)[1]
			value, ok := s.Variables[key]
			if !ok || value == nil {
				missing[key] = true
				return slot
			}
			return fmt.Sprint(value) // single pass: a value containing {{x}} stays literal
		})
	}
	s.Subject, s.BodyText, s.BodyHTML = render(tpl.Subject), render(tpl.BodyText), render(tpl.BodyHTML)
	for key := range missing {
		s.MissingVariables = append(s.MissingVariables, key)
	}
	sort.Strings(s.MissingVariables)
	if strings.TrimSpace(s.BodyText+s.BodyHTML) != "" {
		s.Availability = "complete"
		if len(missing) > 0 {
			s.Availability = "incomplete"
		}
	}
	return s
}

func encodeTemplateSnapshot(s *TemplateSnapshot) any {
	if s == nil {
		return nil
	}
	b, _ := json.Marshal(s)
	return string(b)
}
