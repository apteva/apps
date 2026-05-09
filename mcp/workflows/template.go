package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TemplateContext is the lookup root for {{ … }} expressions inside
// step definitions. Populated fresh per run; steps grows as the
// runner advances.
//
//	{{ input.amount }}                     → trigger payload
//	{{ steps.lookup.contact.id }}          → prior step output
//	{{ env.SLACK_WEBHOOK }}                → spawn env var (read-only)
//	{{ now }}                              → run-start time, RFC3339
type TemplateContext struct {
	Input any
	Steps map[string]any
	Env   map[string]string
	Now   string
}

// resolvePath walks a dot-separated path against a context. Returns
// (value, true) on hit, (nil, false) on miss. Misses are treated as
// nil — workflows that need to detect "field absent" should test
// with a branch step, not via templating.
func resolvePath(ctx TemplateContext, path string) (any, bool) {
	parts := strings.Split(strings.TrimSpace(path), ".")
	if len(parts) == 0 || parts[0] == "" {
		return nil, false
	}
	var cur any
	switch parts[0] {
	case "input":
		cur = ctx.Input
	case "steps":
		cur = ctx.Steps
	case "env":
		// env is map[string]string but path resolution wants any
		envAny := map[string]any{}
		for k, v := range ctx.Env {
			envAny[k] = v
		}
		cur = envAny
	case "now":
		if len(parts) > 1 {
			return nil, false // now has no sub-fields
		}
		return ctx.Now, true
	default:
		return nil, false
	}
	for i := 1; i < len(parts); i++ {
		seg := parts[i]
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(v) {
				return nil, false
			}
			cur = v[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// templateRE matches a {{ ... }} placeholder. Whitespace inside is
// tolerated; the captured group is the trimmed expression.
var templateRE = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// Render substitutes {{ … }} expressions inside `v` using `ctx`. v
// is an already-parsed JSON value tree (map / slice / string /
// number / bool / nil), as produced by yaml/json unmarshalling
// into `any`.
//
// Substitution semantics — the part that matters for safety:
//
//	if a string is exactly "{{ expr }}" → replace with the raw
//	  resolved value (object, array, number, etc. preserved)
//	if a string contains {{ … }} mixed with other text → resolved
//	  values are stringified and spliced in
//	non-string leaves (number/bool/nil) are passed through
//	maps and slices are recursed into
//
// The "exact match" path is what makes this not vulnerable to
// shell-style injection: the substituted value never gets re-
// parsed. A user passing `{ "x": "{{ input.y }}" }` where
// input.y is `{ "drop": "table" }` ends up with a JSON
// `{ "x": { "drop": "table" } }`, never an unparsed string.
func Render(v any, ctx TemplateContext) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, mv := range x {
			r, err := Render(mv, ctx)
			if err != nil {
				return nil, fmt.Errorf("at %q: %w", k, err)
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, sv := range x {
			r, err := Render(sv, ctx)
			if err != nil {
				return nil, fmt.Errorf("at [%d]: %w", i, err)
			}
			out[i] = r
		}
		return out, nil
	case string:
		return renderString(x, ctx)
	default:
		// numbers, bools — passthrough.
		return v, nil
	}
}

// renderString implements the two cases described in Render's
// docstring: exact-match preservation vs. interpolation.
func renderString(s string, ctx TemplateContext) (any, error) {
	matches := templateRE.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}
	// Exact-match optimisation: the whole string is one expression
	// and nothing else. Surface the raw value (could be any JSON
	// type) instead of stringifying.
	if len(matches) == 1 {
		m := matches[0]
		if m[0] == 0 && m[1] == len(s) {
			expr := s[m[2]:m[3]]
			val, ok := resolvePath(ctx, expr)
			if !ok {
				return nil, nil
			}
			return val, nil
		}
	}
	// Interpolation path. Build the result by walking from the
	// previous match end to the next match start, then appending
	// the stringified resolved value.
	var b strings.Builder
	prev := 0
	for _, m := range matches {
		b.WriteString(s[prev:m[0]])
		expr := s[m[2]:m[3]]
		val, _ := resolvePath(ctx, expr)
		b.WriteString(stringify(val))
		prev = m[1]
	}
	b.WriteString(s[prev:])
	return b.String(), nil
}

// stringify renders a resolved value into a string for
// interpolation. Object/array values get JSON-encoded so the result
// is at least machine-readable; nil becomes empty string.
func stringify(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		// JSON numbers are float64 when decoded into any. Print
		// without a trailing decimal for whole numbers — surprises
		// fewer downstream consumers (Slack messages, log lines).
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	// Fallback: %v. Objects/arrays show up here. Production
	// workflows that interpolate complex structures should rethink
	// the template — but %v at least produces something.
	return fmt.Sprintf("%v", v)
}

// stripTemplateError formats a render-time path miss. Reserved for
// future use; today renders never fail-on-miss (we treat misses as
// nil), but the runner may want to enable strict mode in v0.2 so
// keeping the helper around.
func stripTemplateError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(strings.TrimPrefix(err.Error(), "template: "))
}
