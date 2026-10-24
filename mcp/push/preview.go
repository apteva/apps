package main

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Preview is provided only after the originating server authorizes content
// disclosure. It is deliberately not part of the relay database or event log.
type pushPreview struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func previewText(value string, maximum int) string {
	value = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)), " ")
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum-3]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value) + "…"
}

func normalizePreview(input *pushPreview) (*pushPreview, error) {
	if input == nil {
		return nil, nil
	}
	if !utf8.ValidString(input.Title) || !utf8.ValidString(input.Body) {
		return nil, errors.New("invalid text")
	}
	p := &pushPreview{Title: previewText(input.Title, 240), Body: previewText(input.Body, 1024)}
	if p.Title == "" {
		return nil, errors.New("title required")
	}
	// JSON escaping can expand ASCII text sixfold. Bound the serialized copy,
	// not just rune counts, leaving headroom for APNs routing and badge fields.
	for {
		encoded, _ := json.Marshal(p)
		if len(encoded) <= 2400 {
			return p, nil
		}
		if p.Body == "" {
			return nil, errors.New("preview too large")
		}
		runes := []rune(p.Body)
		p.Body = string(runes[:len(runes)-1])
	}
}

func deliveryCopy(d *delivery) (string, string) {
	if d.Preview != nil {
		return d.Preview.Title, d.Preview.Body
	}
	return notificationCopy(d.Type)
}
