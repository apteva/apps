package main

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const replySuggestionComponent = "reply-suggestions"

// Suggested replies are display-only message metadata. Accepting one creates a
// local draft, not an interaction result, inbox item or agent event.
func normalizeReplySuggestions(components []Component, phase, text string) error {
	count := 0
	for i := range components {
		c := &components[i]
		if c.App != appName || c.Name != replySuggestionComponent {
			continue
		}
		count++
		if count > 1 {
			return errors.New("only one suggested reply is supported per message")
		}
		if phase != "final" || strings.TrimSpace(text) == "" {
			return errors.New("attach a suggested reply to a non-empty final reply")
		}
		if c.Interaction != nil {
			return errors.New("suggested replies cannot have an interaction")
		}
		items, err := genericObjectsArg(c.Props, "items", 1)
		if err != nil || len(items) != 1 {
			return errors.New("reply-suggestions requires exactly one item")
		}
		item := items[0]
		id, _ := item["id"].(string)
		if id == "" {
			id = "next"
		}
		value, _ := item["text"].(string)
		value = strings.TrimSpace(value)
		if !validComponentID(id) || value == "" || utf8.RuneCountInString(value) > 500 {
			return errors.New("suggested reply needs an identifier and 1-500 characters of text")
		}
		c.Props = map[string]any{"items": []map[string]any{{"id": id, "text": value}}}
	}
	if count > 0 {
		for _, c := range components {
			if c.Interaction != nil {
				return errors.New("suggested replies cannot accompany a pending interaction")
			}
		}
	}
	return nil
}
