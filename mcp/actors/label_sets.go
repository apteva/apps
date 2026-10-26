package main

import (
	"errors"
	"fmt"
	"strings"
)

// Lists stay typed through whole-value templates; labels are exact names,
// never CSS selectors or substrings. Bound fan-out before opening a browser.
func actorListTemplate(value any) bool {
	text, ok := value.(string)
	return ok && strings.HasPrefix(text, "{{") && strings.HasSuffix(text, "}}") && strings.Count(text, "{{") == 1
}

func actorLabelList(value any) ([]string, error) {
	var labels []string
	switch list := value.(type) {
	case []string:
		labels = append([]string{}, list...)
	case []any:
		labels = make([]string, len(list))
		for i, item := range list {
			label, ok := item.(string)
			if !ok {
				return nil, errors.New("labels must be a string array")
			}
			labels[i] = label
		}
	default:
		return nil, errors.New("labels must be a string array")
	}
	if len(labels) > 50 {
		return nil, errors.New("labels exceeds the 50 label limit")
	}
	seen := map[string]bool{}
	for _, label := range labels {
		if label == "" || strings.TrimSpace(label) != label || strings.ContainsAny(label, "\r\n") || len(label) > 500 {
			return nil, errors.New("labels must be nonempty single-line names without surrounding whitespace (at most 500 bytes)")
		}
		if seen[label] {
			return nil, fmt.Errorf("duplicate label %q", label)
		}
		seen[label] = true
	}
	return labels, nil
}

// Aggregated DOM text uses newline separators. Sets ignore ordering but reject
// duplicate, missing and extra labels; prices/names remain caller inputs.
func actorAssertLabelSet(actual, expected any) error {
	want, err := actorLabelList(expected)
	if err != nil {
		return err
	}
	if text, ok := actual.(string); ok {
		if text == "" {
			actual = []string{}
		} else {
			actual = strings.Split(text, "\n")
		}
	}
	got, err := actorLabelList(actual)
	if err != nil {
		return fmt.Errorf("invalid extracted label set: %w", err)
	}
	if len(got) != len(want) {
		return fmt.Errorf("selected labels %q do not match requested labels %q", got, want)
	}
	seen := map[string]bool{}
	for _, label := range got {
		seen[label] = true
	}
	for _, label := range want {
		if !seen[label] {
			return fmt.Errorf("requested label %q is missing from %q", label, got)
		}
	}
	return nil
}
