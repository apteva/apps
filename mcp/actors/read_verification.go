package main

import (
	"errors"
	"fmt"
	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
	"time"
)

// Declarative verification runs against the complete rendered DOM before any
// candidates are emitted and after pagination. It is independent of viewport.
type actorReadVerification struct {
	InactiveFields []string                  `json:"inactive_fields,omitempty"`
	Fields         map[string]actorField     `json:"fields"`
	Assertions     map[string]actorAssertion `json:"assertions"`
}
type actorReadVerificationEvidence struct {
	Verified        bool           `json:"verified"`
	Values          map[string]any `json:"applied_values"`
	InactiveValues  map[string]any `json:"inactive_values,omitempty"`
	URL             string         `json:"result_url"`
	Source          string         `json:"source"`
	RequiresVisible bool           `json:"requires_visible_controls"`
	At              string         `json:"checked_at"`
	Error           string         `json:"error,omitempty"`
}

func validateReadVerification(v *actorReadVerification) error {
	if v == nil {
		return nil
	}
	if len(v.Fields) == 0 || len(v.Fields) > maxActorFields || len(v.Assertions) == 0 {
		return errors.New("read verification requires bounded fields and assertions")
	}
	seen := map[string]bool{}
	for _, name := range v.InactiveFields {
		if _, ok := v.Fields[name]; !ok || seen[name] {
			return fmt.Errorf("invalid inactive verification field %q", name)
		}
		seen[name] = true
		if _, ok := v.Assertions[name]; ok {
			return fmt.Errorf("inactive verification field %q cannot assert applied state", name)
		}
	}
	for name := range v.Assertions {
		if _, ok := v.Fields[name]; !ok {
			return fmt.Errorf("verification assertion %q has no extracted field", name)
		}
	}
	// Reuse the existing site-neutral extraction/assertion validator.
	return validateActorDefinition(actorDefinition{SchemaVersion: 1, ReadOnly: true, AllowedHosts: []string{"example.com"}, Steps: []actorStep{{Action: "extract", Items: "body", Fields: v.Fields}, {Action: "assert_values", Assertions: v.Assertions}}})
}
func (e *actorExecution) verifyReadPage(v actorReadView, root *html.Node) error {
	if v.Verification == nil {
		return nil
	}
	evidence := &actorReadVerificationEvidence{URL: e.currentURL, Source: "complete_rendered_dom", At: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, coverage := range e.coverage.Views {
		if coverage.Name == v.Name {
			coverage.Verification = evidence
		}
	}
	// Every configured field is a unique scalar or an explicit many collection.
	// Ambiguous selectors cannot silently verify whichever element comes first.
	for name, f := range v.Verification.Fields {
		if f.Selector != "" && !f.Many && !f.All {
			sel, err := cascadia.Compile(f.Selector)
			if err != nil {
				return err
			}
			count := len(cascadia.QueryAll(root, sel))
			if count > 1 {
				return fmt.Errorf("verification field %s is ambiguous (%d nodes)", name, count)
			}
		}
	}
	values, err := extractNodeItem(root, v.Verification.Fields, e.currentURL)
	evidence.Values = values
	if err != nil {
		evidence.Error = err.Error()
		return fmt.Errorf("read filter verification: %w", err)
	}
	previous := e.lastValues
	e.lastValues = values
	err = e.assertValues(actorStep{Assertions: v.Verification.Assertions})
	e.lastValues = previous
	if err != nil {
		evidence.Error = err.Error()
		return fmt.Errorf("read filter verification: %w", err)
	}
	if len(v.Verification.InactiveFields) > 0 {
		evidence.InactiveValues = map[string]any{}
		for _, name := range v.Verification.InactiveFields {
			evidence.InactiveValues[name] = values[name]
			delete(evidence.Values, name)
		}
	}
	evidence.Verified = true
	return nil
}
