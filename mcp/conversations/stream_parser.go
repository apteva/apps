package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf16"
)

// flatStringParser scans each byte once. Incomplete escapes stay at pos until
// the next chunk arrives; nested objects cannot masquerade as top-level fields.
type flatStringParser struct {
	pos, depth               int
	inString, isKey, escaped bool
	key                      string
	token                    strings.Builder
	text                     strings.Builder
	destination              string
	destinationSeen          bool
	textComplete             bool
	expectingValue           bool
}

func (p *flatStringParser) consume(raw string) {
	for p.pos < len(raw) {
		c := raw[p.pos]
		if !p.inString {
			switch c {
			case '{', '[':
				p.depth++
			case '}', ']':
				p.depth--
			case ':':
				if p.depth == 1 {
					p.expectingValue = true
				}
			case ',':
				if p.depth == 1 {
					p.expectingValue = false
				}
			case '"':
				p.inString = true
				p.isKey = p.depth == 1 && !p.expectingValue
				p.token.Reset()
			}
			p.pos++
			continue
		}
		if c == '"' {
			p.inString = false
			if p.isKey {
				p.key = p.token.String()
			} else if p.depth == 1 {
				if p.key == "conversation_id" {
					p.destination = p.token.String()
					p.destinationSeen = true
				}
				if p.key == "text" {
					p.textComplete = true
				}
			}
			p.pos++
			continue
		}
		value := ""
		if c == '\\' {
			if p.pos+1 >= len(raw) {
				return
			}
			count := 2
			if raw[p.pos+1] == 'u' {
				count = 6
				if p.pos+count > len(raw) {
					return
				}
				v, err := strconv.ParseUint(raw[p.pos+2:p.pos+6], 16, 16)
				if err != nil {
					return
				}
				if utf16.IsSurrogate(rune(v)) && v >= 0xD800 && v <= 0xDBFF {
					count = 12
					if p.pos+count > len(raw) {
						return
					}
				}
			}
			if err := json.Unmarshal([]byte(`"`+raw[p.pos:p.pos+count]+`"`), &value); err != nil {
				return
			}
			p.pos += count
		} else {
			value = raw[p.pos : p.pos+1]
			p.pos++
		}
		if p.isKey || (p.depth == 1 && p.key == "conversation_id") {
			p.token.WriteString(value)
		}
		if !p.isKey && p.depth == 1 && p.key == "text" {
			p.text.WriteString(value)
		}
	}
}
