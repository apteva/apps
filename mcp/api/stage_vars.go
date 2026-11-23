package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Stage variables let one immutable configuration serve several stages that
// differ only in target values. Route target fields may reference
// {{stage.name}} and {{stage.vars.<key>}}; the gateway substitutes them only
// for stage-served requests. Legacy (non-stage) requests never substitute.

const (
	maxStageVars        = 64
	maxStageVarValueLen = 2048
	// stagePlaceholderSample stands in for placeholders when a route's target
	// is structurally validated at write time (URL, app name, topic shapes).
	stagePlaceholderSample = "stagevar"
)

var (
	templateCandidateRe = regexp.MustCompile(`\{\{([^{}]*)\}\}`)
	stageVarKeyRe       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)
)

// stagePlaceholder is one parsed {{stage...}} reference. Var is empty for
// {{stage.name}}.
type stagePlaceholder struct {
	Name bool
	Var  string
}

// parseStagePlaceholder reports whether a {{...}} body is a stage reference
// and, if so, whether it is well formed. Non-stage templates are ignored.
func parseStagePlaceholder(body string) (stagePlaceholder, bool, error) {
	inner := strings.TrimSpace(body)
	if inner != "stage" && !strings.HasPrefix(inner, "stage.") {
		return stagePlaceholder{}, false, nil
	}
	if inner == "stage.name" {
		return stagePlaceholder{Name: true}, true, nil
	}
	if key, ok := strings.CutPrefix(inner, "stage.vars."); ok && stageVarKeyRe.MatchString(key) {
		return stagePlaceholder{Var: key}, true, nil
	}
	return stagePlaceholder{}, true, fmt.Errorf("invalid stage placeholder {{%s}}: use {{stage.name}} or {{stage.vars.<key>}}", inner)
}

// stagePlaceholders returns the stage variable keys referenced by s and
// rejects malformed stage references.
func stagePlaceholders(s string, vars map[string]struct{}) error {
	if !strings.Contains(s, "{{") {
		return nil
	}
	for _, m := range templateCandidateRe.FindAllStringSubmatch(s, -1) {
		p, isStage, err := parseStagePlaceholder(m[1])
		if err != nil {
			return err
		}
		if isStage && p.Var != "" {
			vars[p.Var] = struct{}{}
		}
	}
	return nil
}

// substituteStageString replaces stage placeholders in one string. A
// reference to a variable the stage does not define is an error, never an
// empty string. Substituted values are not re-scanned.
func substituteStageString(s, stageName string, vars map[string]string) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	var firstErr error
	out := templateCandidateRe.ReplaceAllStringFunc(s, func(match string) string {
		p, isStage, err := parseStagePlaceholder(match[2 : len(match)-2])
		if !isStage {
			return match
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return match
		}
		if p.Name {
			return stageName
		}
		value, ok := vars[p.Var]
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("missing stage variable %q", p.Var)
			}
			return match
		}
		return value
	})
	return out, firstErr
}

// walkJSONStrings rewrites every string value (not object keys) of a decoded
// JSON document.
func walkJSONStrings(v any, fn func(string) (string, error)) (any, error) {
	switch x := v.(type) {
	case string:
		return fn(x)
	case []any:
		for i := range x {
			out, err := walkJSONStrings(x[i], fn)
			if err != nil {
				return nil, err
			}
			x[i] = out
		}
		return x, nil
	case map[string]any:
		for k := range x {
			out, err := walkJSONStrings(x[k], fn)
			if err != nil {
				return nil, err
			}
			x[k] = out
		}
		return x, nil
	default:
		return v, nil
	}
}

// substituteJSONStrings applies fn to string values in a JSON object text.
// Values are substituted after decoding so variable content can never break
// the JSON structure.
func substituteJSONStrings(raw string, fn func(string) (string, error)) (string, error) {
	if !strings.Contains(raw, "{{") {
		return raw, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var doc any
	if err := decoder.Decode(&doc); err != nil {
		return "", errors.New("events must be valid JSON")
	}
	doc, err := walkJSONStrings(doc, fn)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

// routeStageVarRefs collects the variable keys a route's target fields
// reference, rejecting malformed stage placeholders.
func routeStageVarRefs(targetRef, targetPath, eventsJSON string, into map[string]struct{}) error {
	for _, field := range []struct{ name, value string }{{"target_ref", targetRef}, {"target_path", targetPath}} {
		if err := stagePlaceholders(field.value, into); err != nil {
			return fmt.Errorf("%s: %w", field.name, err)
		}
	}
	if strings.Contains(eventsJSON, "{{") {
		_, err := substituteJSONStrings(eventsJSON, func(s string) (string, error) {
			return s, stagePlaceholders(s, into)
		})
		if err != nil {
			return fmt.Errorf("events: %w", err)
		}
	}
	return nil
}

// sampleStageValue replaces well-formed stage placeholders with a neutral
// token so write-time structural validation (URLs, app names, topics) can
// run on templated targets. Malformed placeholders are left as is.
func sampleStageValue(s string) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	return templateCandidateRe.ReplaceAllStringFunc(s, func(match string) string {
		if _, isStage, err := parseStagePlaceholder(match[2 : len(match)-2]); isStage && err == nil {
			return stagePlaceholderSample
		}
		return match
	})
}

// validateTemplatedTarget validates a route target that may contain stage
// placeholders. An HTTP target whose scheme and host come from a stage
// variable (it starts with a placeholder) cannot be checked until the stage
// resolves it; resolveStageRoute re-validates every resolved target.
func validateTemplatedTarget(kind, ref string) error {
	trimmed := strings.TrimSpace(ref)
	if kind == "http" && strings.HasPrefix(trimmed, "{{") {
		if loc := templateCandidateRe.FindStringSubmatchIndex(trimmed); loc != nil && loc[0] == 0 {
			if _, isStage, err := parseStagePlaceholder(trimmed[loc[2]:loc[3]]); isStage && err == nil {
				return nil
			}
		}
	}
	return validateTarget(kind, sampleStageValue(ref))
}

func sampleStageJSON(raw string) string {
	out, err := substituteJSONStrings(raw, func(s string) (string, error) { return sampleStageValue(s), nil })
	if err != nil {
		return raw
	}
	return out
}

// normalizeStageVars validates a stage's vars argument (a JSON object of
// string values) and returns canonical JSON.
func normalizeStageVars(v any) (map[string]string, string, error) {
	vars := map[string]string{}
	switch x := v.(type) {
	case nil:
	case map[string]any:
		for key, value := range x {
			s, ok := value.(string)
			if !ok {
				return nil, "", fmt.Errorf("stage var %q must be a string", key)
			}
			vars[key] = s
		}
	case map[string]string:
		for key, value := range x {
			vars[key] = value
		}
	case string:
		if strings.TrimSpace(x) != "" {
			if err := json.Unmarshal([]byte(x), &vars); err != nil {
				return nil, "", errors.New("vars must be a JSON object of string values")
			}
		}
	default:
		return nil, "", errors.New("vars must be an object of string values")
	}
	if len(vars) > maxStageVars {
		return nil, "", fmt.Errorf("a stage supports at most %d vars", maxStageVars)
	}
	for key, value := range vars {
		if !stageVarKeyRe.MatchString(key) {
			return nil, "", fmt.Errorf("invalid stage var name %q: use letters, digits, _ or -, starting with a letter or _", key)
		}
		if len(value) > maxStageVarValueLen {
			return nil, "", fmt.Errorf("stage var %q exceeds %d bytes", key, maxStageVarValueLen)
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, "", fmt.Errorf("stage var %q must not contain line breaks or NUL", key)
		}
		if strings.Contains(value, "{{") {
			return nil, "", fmt.Errorf("stage var %q must not contain placeholders", key)
		}
	}
	raw, err := json.Marshal(vars)
	if err != nil {
		return nil, "", err
	}
	return vars, string(raw), nil
}

func parseStageVarsJSON(raw string) map[string]string {
	vars := map[string]string{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &vars)
	}
	return vars
}

// missingStageVars reports referenced keys the stage does not define.
func missingStageVars(required map[string]struct{}, vars map[string]string) []string {
	var missing []string
	for key := range required {
		if _, ok := vars[key]; !ok {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

func errMissingStageVars(stageName string, missing []string) error {
	return fmt.Errorf("stage %q is missing vars required by its configuration: %s", stageName, strings.Join(missing, ", "))
}

// resolveStageRoute returns the route with stage placeholders substituted for
// a stage-served request, re-validating the resolved target. Non-stage
// requests return the route unchanged. The cached route is never mutated.
func resolveStageRoute(api *API, route *APIRoute) (*APIRoute, error) {
	if api.StageID == 0 || route == nil {
		return route, nil
	}
	if !strings.Contains(route.TargetRef, "{{") && !strings.Contains(route.TargetPath, "{{") && !strings.Contains(route.EventsJSON, "{{") {
		return route, nil
	}
	vars := parseStageVarsJSON(api.StageVarsJSON)
	sub := func(s string) (string, error) { return substituteStageString(s, api.StageName, vars) }
	resolved := *route
	var err error
	if resolved.TargetRef, err = sub(route.TargetRef); err != nil {
		return nil, stageConfigFailure(err)
	}
	if resolved.TargetPath, err = sub(route.TargetPath); err != nil {
		return nil, stageConfigFailure(err)
	}
	if resolved.EventsJSON, err = substituteJSONStrings(route.EventsJSON, sub); err != nil {
		return nil, stageConfigFailure(err)
	}
	resolved.TargetRef = strings.TrimSpace(resolved.TargetRef)
	if err := validateTarget(resolved.TargetKind, resolved.TargetRef); err != nil {
		return nil, stageConfigFailure(fmt.Errorf("resolved target_ref invalid: %w", err))
	}
	if err := validateRouteTargetPath(resolved.TargetPath); err != nil {
		return nil, stageConfigFailure(fmt.Errorf("resolved target_path invalid: %w", err))
	}
	if resolved.TargetKind == "app_events" {
		if _, err := parseAppEventsConfig(resolved.EventsJSON); err != nil {
			return nil, stageConfigFailure(fmt.Errorf("resolved events invalid: %w", err))
		}
	}
	return &resolved, nil
}

func stageConfigFailure(err error) error {
	return &gatewayFailure{"stage_config_error", "Stage configuration error: " + err.Error(), 500, err}
}
