package main

import "encoding/json"

// This is a message-owned snapshot returned by Messaging, not a current
// template lookup. Keep provider-confirmed and locally captured text distinct.
type TemplateSnapshot struct {
	TemplateID         int64          `json:"template_id,omitempty"`
	Name               string         `json:"name,omitempty"`
	ProviderTemplateID string         `json:"provider_template_id,omitempty"`
	CapturedAt         string         `json:"captured_at,omitempty"`
	ContentSource      string         `json:"content_source"`
	Availability       string         `json:"availability"`
	Variables          map[string]any `json:"variables,omitempty"`
	Subject            string         `json:"subject,omitempty"`
	BodyText           string         `json:"body_text,omitempty"`
	BodyHTML           string         `json:"body_html,omitempty"`
	MissingVariables   []string       `json:"missing_variables,omitempty"`
}

func templateSnapshotFromAny(value any) *TemplateSnapshot {
	if value == nil {
		return nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var s *TemplateSnapshot
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	return s
}

func recordedTemplateSnapshot(activity *Activity) *TemplateSnapshot {
	if activity == nil {
		return nil
	}
	var detail map[string]any
	if json.Unmarshal([]byte(activity.SourceDetail), &detail) != nil {
		return nil
	}
	return templateSnapshotFromAny(detail["template_snapshot"])
}
