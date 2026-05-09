package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// EvalCondition evaluates a branch.when expression. Three forms:
//
//   "input.x"                   — single token: truthiness check.
//   "input.x == 5"              — three tokens: comparison.
//   "steps.lookup.found != true" — same, with a literal RHS.
//
// Operands are either dot-paths (resolved against ctx) or JSON
// literals (strings in single OR double quotes, numbers, true,
// false, null). The dot-path heuristic: if the trimmed token starts
// with "input.", "steps.", "env.", or equals "now", it's a path.
// Otherwise it's parsed as JSON.
//
// Deliberately tiny. Anything more complex is "make it a function
// step." Keeping the surface small means workflows stay auditable
// — there's no Turing-complete eval running inside the dispatcher.
func EvalCondition(when string, ctx TemplateContext) (bool, error) {
	trimmed := strings.TrimSpace(when)
	if trimmed == "" {
		return false, fmt.Errorf("empty condition")
	}
	tokens := splitConditionTokens(trimmed)
	switch len(tokens) {
	case 1:
		v, _ := resolveOperand(tokens[0], ctx)
		return truthy(v), nil
	case 3:
		lhs, _ := resolveOperand(tokens[0], ctx)
		rhs, _ := resolveOperand(tokens[2], ctx)
		return compareOp(tokens[1], lhs, rhs)
	default:
		return false, fmt.Errorf("expected 1 or 3 tokens, got %d (%q)", len(tokens), when)
	}
}

// splitConditionTokens splits the expression on whitespace, but
// respects single/double-quoted runs. Lets the user write
//
//	"input.kind == 'invoice paid'"
//
// without losing the space inside the quoted literal.
func splitConditionTokens(s string) []string {
	tokens := []string{}
	var cur strings.Builder
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote != 0 {
			cur.WriteByte(c)
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inQuote = c
			cur.WriteByte(c)
		case ' ', '\t':
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

// resolveOperand turns a token into a value: a dot-path resolves
// via the context; otherwise we try to parse as JSON. Single quotes
// are normalised to double quotes for the JSON parser.
func resolveOperand(tok string, ctx TemplateContext) (any, bool) {
	tok = strings.TrimSpace(tok)
	if isPathLike(tok) {
		return resolvePath(ctx, tok)
	}
	// Single-quoted strings. JSON doesn't allow them; rewrite.
	if len(tok) >= 2 && tok[0] == '\'' && tok[len(tok)-1] == '\'' {
		tok = `"` + tok[1:len(tok)-1] + `"`
	}
	var v any
	if err := json.Unmarshal([]byte(tok), &v); err != nil {
		// Not JSON. Treat as bare string — the user probably meant
		// `input.kind == invoice` for a string value. Forgiving
		// path; if you want strictness write quotes.
		return tok, true
	}
	return v, true
}

// isPathLike heuristic — see EvalCondition's doc. Cheap to call.
func isPathLike(tok string) bool {
	if tok == "now" {
		return true
	}
	for _, prefix := range []string{"input.", "input", "steps.", "steps", "env.", "env"} {
		if tok == prefix || strings.HasPrefix(tok, prefix+".") {
			return true
		}
	}
	return false
}

// truthy mirrors JS-ish truthiness: nil/false/0/""/[]/{} → false,
// everything else → true. Keeping it close to what people expect
// from `if (x)` in any modern scripting language.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// compareOp implements the six comparison operators against two
// already-resolved values. Same-type comparisons only — mismatched
// types return false (no implicit coercion).
func compareOp(op string, lhs, rhs any) (bool, error) {
	switch op {
	case "==":
		return jsonEqual(lhs, rhs), nil
	case "!=":
		return !jsonEqual(lhs, rhs), nil
	case ">", "<", ">=", "<=":
		return numericCompare(op, lhs, rhs)
	default:
		return false, fmt.Errorf("unknown operator %q", op)
	}
}

// jsonEqual compares values using the relaxed-equality rules JSON
// users expect — numbers are compared as float64, strings as
// strings, bools as bools, nils equal nils. Objects/arrays compare
// only via deep value equality.
func jsonEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == b
	}
	la, aIsNum := numeric(a)
	lb, bIsNum := numeric(b)
	if aIsNum && bIsNum {
		return la == lb
	}
	// Same-type fallthrough.
	return fmt.Sprintf("%T:%v", a, a) == fmt.Sprintf("%T:%v", b, b)
}

func numericCompare(op string, lhs, rhs any) (bool, error) {
	la, ok := numeric(lhs)
	if !ok {
		return false, fmt.Errorf("operator %q needs numeric LHS, got %T", op, lhs)
	}
	rb, ok := numeric(rhs)
	if !ok {
		return false, fmt.Errorf("operator %q needs numeric RHS, got %T", op, rhs)
	}
	switch op {
	case ">":
		return la > rb, nil
	case "<":
		return la < rb, nil
	case ">=":
		return la >= rb, nil
	case "<=":
		return la <= rb, nil
	}
	return false, fmt.Errorf("unreachable: op %q", op)
}

// numeric coerces JSON-decoded numbers (float64) and Go ints/int64
// into float64 for comparison.
func numeric(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}
