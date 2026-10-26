package main

import (
	"errors"
	"strings"
)

func validateTrimLineEdges(field actorField) error {
	if field.TrimLineEdges && (!field.PreserveLineBreaks || field.Attribute != "" || (field.Type != "" && field.Type != "text") || field.OwnText) {
		return errors.New("trim_line_edges requires preserve_line_breaks on a text field without attributes or own_text")
	}
	return nil
}

// Remove renderer indentation without changing words, inline spacing or paragraphs.
func trimActorLineEdges(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = strings.Trim(lines[i], " \t\r\u00a0")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}
