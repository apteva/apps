package main

import (
	"encoding/json"
	"unicode/utf8"
)

// Count encoding/json's default wire representation without allocating and
// sorting a second copy of a whole batch/result just to enforce a byte budget.
// Stop as soon as the budget is exceeded; input values are still type-validated
// by the mutation handlers before any transaction can commit.
func jsonSize(value any, limit int64) (int64, error) {
	var used int64
	var visit func(any, int) error
	visit = func(value any, depth int) error {
		if depth > 128 {
			return errf("JSON nesting exceeds 128 levels")
		}
		if limit > 0 && used > limit {
			return nil
		}
		switch v := value.(type) {
		case nil:
			used += 4
		case int:
			used += integerSize(int64(v))
		case int64:
			used += integerSize(v)
		case string:
			used += jsonStringSize(v)
		case bool:
			if v {
				used += 4
			} else {
				used += 5
			}
		case map[string]any:
			if v == nil {
				used += 4
				break
			}
			used += 2
			first := true
			for key, item := range v {
				if !first {
					used++
				}
				first = false
				used += jsonStringSize(key) + 1
				if err := visit(item, depth+1); err != nil {
					return err
				}
				if limit > 0 && used > limit {
					break
				}
			}
		case []any:
			if v == nil {
				used += 4
				break
			}
			used += 2
			for i, item := range v {
				if i > 0 {
					used++
				}
				if err := visit(item, depth+1); err != nil {
					return err
				}
				if limit > 0 && used > limit {
					break
				}
			}
		default:
			encoded, err := json.Marshal(v)
			if err != nil {
				return err
			}
			used += int64(len(encoded))
		}
		return nil
	}
	err := visit(value, 0)
	return used, err
}
func jsonStringSize(s string) int64 {
	n := int64(2)
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"' || c == '\\' || c == '\b' || c == '\f' || c == '\n' || c == '\r' || c == '\t':
			n += 2
			i++
		case c < 0x20 || c == '<' || c == '>' || c == '&':
			n += 6
			i++
		case c < utf8.RuneSelf:
			n++
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 || r == '\u2028' || r == '\u2029' {
				n += 6
			} else {
				n += int64(size)
			}
			i += size
		}
	}
	return n
}

func integerSize(v int64) int64 {
	n := int64(1)
	u := uint64(v)
	if v < 0 {
		n++
		u = uint64(-(v + 1)) + 1
	}
	for u >= 10 {
		n++
		u /= 10
	}
	return n
}
