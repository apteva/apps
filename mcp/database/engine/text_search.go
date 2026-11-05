package engine

import (
	"bytes"
	"unicode/utf8"
)

// like_contains_ascii searches several text fields as one string: nulls read
// as "", fields are joined by one space, and the text must match the LIKE
// pattern "%" + value + "%" with SQLite's default semantics (% and _ are
// wildcards, _ is one UTF-8 character, ASCII letters fold case, no escape).
// Every adapter evaluates it natively; it never selects an index, so other
// conjuncts still choose the access path.
const opLikeContainsASCII = "like_contains_ascii"

// contains_ascii is a literal substring test on one text field:
// instr(lower(coalesce(field,”)), lower(value)) > 0. Null reads as "", ASCII
// letters fold case (other letters compare exactly) and %, _ and \ are ordinary
// characters. An empty value matches every record. It never selects an index.
const opContainsASCII = "contains_ascii"

const (
	maxSearchFields = 16
	maxSearchValue  = 1024
)

func searchPattern(value string) string { return "%" + value + "%" }

// searchAll reports whether the predicate matches every record, so adapters
// can drop it and keep index-covered counts.
func searchAll(f *Filter) bool { return f.Value.(string) == "" }

func matchesSearch(r Record, f *Filter) bool {
	if searchAll(f) {
		return true
	}
	var stack [256]byte
	text := stack[:0]
	for i, name := range f.Fields {
		if i > 0 {
			text = append(text, ' ')
		}
		if v, ok := r[name].(string); ok {
			text = asciiLower(text, v)
		}
	}
	pattern := asciiLower(nil, f.Value.(string))
	if bytes.IndexAny(pattern, "%_") < 0 {
		return bytes.Contains(text, pattern)
	}
	return likeMatch(asciiLower(nil, searchPattern(f.Value.(string))), text)
}

func matchesContains(v any, f *Filter) bool {
	if searchAll(f) {
		return true
	}
	text, _ := v.(string)
	var stack [256]byte
	return bytes.Contains(asciiLower(stack[:0], text), asciiLower(nil, f.Value.(string)))
}

func validateContains(fld Field, f *Filter) error {
	if fld.Type != "text" {
		return Invalid("contains_ascii requires a text field; %s is %s", fld.Name, fld.Type)
	}
	s, ok := f.Value.(string)
	if !ok {
		return Invalid("contains_ascii requires a string value")
	}
	if len(s) > maxSearchValue {
		return Invalid("contains_ascii value is limited to 1024 bytes")
	}
	return nil
}

func asciiLower(dst []byte, src string) []byte {
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		dst = append(dst, c)
	}
	return dst
}

// likeMatch matches lowered text against a lowered LIKE pattern. A % retries
// from one character past its previous attempt, so each % costs linear time.
func likeMatch(p, s []byte) bool {
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(s) {
		if pi < len(p) && p[pi] == '%' {
			star, mark = pi, si
			pi++
			continue
		}
		if pi < len(p) && p[pi] == '_' {
			_, n := utf8.DecodeRune(s[si:])
			pi, si = pi+1, si+n
			continue
		}
		if pi < len(p) && p[pi] == s[si] {
			pi, si = pi+1, si+1
			continue
		}
		if star < 0 {
			return false
		}
		_, n := utf8.DecodeRune(s[mark:])
		mark += n
		pi, si = star+1, mark
	}
	for pi < len(p) && p[pi] == '%' {
		pi++
	}
	return pi == len(p)
}

func validateSearch(c Collection, f *Filter) error {
	if f.Op != opLikeContainsASCII || f.Fields == nil {
		return Invalid("fields requires op like_contains_ascii, and like_contains_ascii requires fields")
	}
	if len(f.Fields) == 0 || len(f.Fields) > maxSearchFields {
		return Invalid("like_contains_ascii requires 1–16 fields")
	}
	for _, name := range f.Fields {
		fld, e := c.field(name)
		if e != nil {
			return e
		}
		if fld.Type != "text" {
			return Invalid("like_contains_ascii requires text fields; %s is %s", name, fld.Type)
		}
	}
	s, ok := f.Value.(string)
	if !ok {
		return Invalid("like_contains_ascii requires a string value")
	}
	if len(s) > maxSearchValue {
		return Invalid("like_contains_ascii value is limited to 1024 bytes")
	}
	return nil
}
