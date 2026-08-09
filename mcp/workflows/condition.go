package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// EvalCondition evaluates a branch.when expression. Atomic forms:
//
//	"input.x"                   — single token: truthiness check.
//	"input.x == 5"              — three tokens: comparison.
//	"steps.lookup.found != true" — same, with a literal RHS.
//
// Atoms can be joined with `and` and `or`; `and` binds more tightly
// and both operators short-circuit.
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
	atoms, ops, err := parseConditionExpression(tokens)
	if err != nil {
		return false, fmt.Errorf("%w (%q)", err, when)
	}
	for _, atom := range atoms {
		if err := validateConditionAtom(atom, false); err != nil {
			return false, err
		}
	}
	return evalConditionExpression(atoms, ops, ctx)
}

// ValidateTriggerCondition applies a strict grammar to event trigger
// predicates. Branch conditions intentionally accept forgiving bare-string
// operands for backwards compatibility, but trigger predicates guard whether
// a workflow runs at all and must fail closed on typos.
func ValidateTriggerCondition(when string) error {
	trimmed := strings.TrimSpace(when)
	if trimmed == "" {
		return fmt.Errorf("empty condition")
	}
	tokens := splitConditionTokens(trimmed)
	atoms, _, err := parseConditionExpression(tokens)
	if err != nil {
		return fmt.Errorf("%w (%q)", err, when)
	}
	for i, atom := range atoms {
		if err := validateConditionAtom(atom, true); err != nil {
			return fmt.Errorf("condition %d: %w", i+1, err)
		}
	}
	return nil
}

// EvalTriggerCondition validates before delegating to the shared evaluator.
// Keeping validation here as well as at workflow create/update time protects
// imported or legacy database rows from accidentally matching every event.
func EvalTriggerCondition(when string, ctx TemplateContext) (bool, error) {
	if err := ValidateTriggerCondition(when); err != nil {
		return false, err
	}
	return EvalCondition(when, ctx)
}

func parseConditionExpression(tokens []string) ([][]string, []string, error) {
	if len(tokens) == 0 {
		return nil, nil, fmt.Errorf("empty condition")
	}
	atoms := make([][]string, 0, 1)
	ops := make([]string, 0, 1)
	for i := 0; i < len(tokens); {
		size := 1
		if i+1 < len(tokens) && tokens[i+1] != "and" && tokens[i+1] != "or" {
			if i+2 >= len(tokens) {
				return nil, nil, fmt.Errorf("comparison is missing a right operand")
			}
			size = 3
		}
		atoms = append(atoms, tokens[i:i+size])
		i += size
		if i == len(tokens) {
			break
		}
		if tokens[i] != "and" && tokens[i] != "or" {
			return nil, nil, fmt.Errorf("expected and/or, got %q", tokens[i])
		}
		ops = append(ops, tokens[i])
		i++
		if i == len(tokens) {
			return nil, nil, fmt.Errorf("%s is missing a following condition", ops[len(ops)-1])
		}
	}
	return atoms, ops, nil
}

func validateConditionAtom(atom []string, strict bool) error {
	switch len(atom) {
	case 1:
		if !strict || isPathLike(atom[0]) {
			return nil
		}
		value, err := parseStrictConditionLiteral(atom[0])
		if err != nil {
			return err
		}
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("single-token trigger condition must be a path or boolean literal")
		}
		return nil
	case 3:
		switch atom[1] {
		case "==", "!=", ">", "<", ">=", "<=":
		default:
			return fmt.Errorf("unknown operator %q", atom[1])
		}
		if !strict {
			return nil
		}
		if err := validateStrictConditionOperand(atom[0]); err != nil {
			return fmt.Errorf("left operand: %w", err)
		}
		if err := validateStrictConditionOperand(atom[2]); err != nil {
			return fmt.Errorf("right operand: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("expected a truthiness check or comparison")
	}
}

func evalConditionExpression(atoms [][]string, ops []string, ctx TemplateContext) (bool, error) {
	for start := 0; start < len(atoms); {
		end := start
		for end < len(ops) && ops[end] == "and" {
			end++
		}
		groupMatched := true
		for i := start; i <= end; i++ {
			if !groupMatched {
				continue
			}
			matched, err := evalConditionAtom(atoms[i], ctx)
			if err != nil {
				return false, err
			}
			groupMatched = matched
		}
		if groupMatched {
			return true, nil
		}
		start = end + 1
	}
	return false, nil
}

func evalConditionAtom(atom []string, ctx TemplateContext) (bool, error) {
	if len(atom) == 1 {
		value, _ := resolveOperand(atom[0], ctx)
		return truthy(value), nil
	}
	lhs, _ := resolveOperand(atom[0], ctx)
	rhs, _ := resolveOperand(atom[2], ctx)
	return compareOp(atom[1], lhs, rhs)
}

func validateStrictConditionOperand(tok string) error {
	if isPathLike(tok) {
		return nil
	}
	_, err := parseStrictConditionLiteral(tok)
	return err
}

func parseStrictConditionLiteral(tok string) (any, error) {
	tok = strings.TrimSpace(tok)
	if len(tok) >= 2 && tok[0] == '\'' && tok[len(tok)-1] == '\'' {
		tok = `"` + tok[1:len(tok)-1] + `"`
	}
	var value any
	if err := json.Unmarshal([]byte(tok), &value); err != nil {
		return nil, fmt.Errorf("%q must be a path or quoted/JSON literal", tok)
	}
	return value, nil
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
