package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const maxScenarioSteps = 24

type Step struct {
	Browser     *BrowserAction       `json:"browser,omitempty"`
	ID          string               `json:"id"`
	Kind        string               `json:"kind,omitempty"`
	Path        string               `json:"path,omitempty"`
	Method      string               `json:"method,omitempty"`
	Headers     map[string]string    `json:"headers,omitempty"`
	JSON        any                  `json:"json,omitempty"`
	Identity    string               `json:"identity,omitempty"`
	Target      string               `json:"target,omitempty"`
	HeaderRefs  map[string]HeaderRef `json:"header_refs,omitempty"`
	BudgetMS    int                  `json:"budget_ms,omitempty"`
	Function    string               `json:"function,omitempty"`
	Event       any                  `json:"event,omitempty"`
	App         string               `json:"app,omitempty"`
	Tool        string               `json:"tool,omitempty"`
	Input       map[string]any       `json:"input,omitempty"`
	TimeoutMS   int                  `json:"timeout_ms,omitempty"`
	PollEveryMS int                  `json:"poll_every_ms,omitempty"`
	Assertions  []Assertion          `json:"assertions,omitempty"`
	Schema      json.RawMessage      `json:"response_schema,omitempty"`
	Capture     map[string]string    `json:"capture,omitempty"`
	Evidence    []string             `json:"evidence,omitempty"`
	Parallel    []Step               `json:"parallel,omitempty"`
}

type StepResult struct {
	ID                string            `json:"id"`
	Kind              string            `json:"kind,omitempty"`
	Status            string            `json:"status"`
	Error             string            `json:"error,omitempty"`
	DurationMS        int64             `json:"duration_ms"`
	Attempts          int               `json:"attempts,omitempty"`
	Method            string            `json:"method,omitempty"`
	Path              string            `json:"path,omitempty"`
	OperationID       string            `json:"operation_id,omitempty"`
	HTTPStatus        int               `json:"http_status,omitempty"`
	Correlation       string            `json:"correlation_id,omitempty"`
	Assertions        []AssertionResult `json:"assertions,omitempty"`
	Evidence          map[string]any    `json:"evidence,omitempty"`
	Parallel          []StepResult      `json:"parallel,omitempty"`
	ArtifactID        int64             `json:"artifact_id,omitempty"`
	Diagnostics       any               `json:"diagnostics,omitempty"`
	ActionDispatched  bool              `json:"action_dispatched,omitempty"`
	coverageQualified bool
}

func validateSteps(steps []Step) error {
	if len(steps) == 0 || len(steps) > maxScenarioSteps {
		return fmt.Errorf("provide 1–%d scenario steps", maxScenarioSteps)
	}
	ids, names := map[string]bool{}, map[string]bool{}
	leaves := 0
	for i := range steps {
		s := &steps[i]
		if len(s.Parallel) == 0 {
			if err := validateStep(s, ids, names); err != nil {
				return err
			}
			leaves++
			continue
		}
		if !slugPattern.MatchString(s.ID) || ids[s.ID] || s.Kind != "" || len(s.Parallel) < 2 || len(s.Parallel) > 4 {
			return errors.New("parallel groups require a unique id and 2–4 leaf steps")
		}
		ids[s.ID] = true
		for j := range s.Parallel {
			if s.Parallel[j].Kind == "browser" {
				return errors.New("browser steps must execute sequentially")
			}
			if err := validateStep(&s.Parallel[j], ids, names); err != nil {
				return err
			}
			leaves++
		}
	}
	if leaves > maxScenarioSteps {
		return fmt.Errorf("maximum %d leaf steps per scenario", maxScenarioSteps)
	}
	return nil
}

func validateStep(s *Step, ids, names map[string]bool) error {
	if len(s.Parallel) > 0 || !slugPattern.MatchString(s.ID) || ids[s.ID] {
		return fmt.Errorf("step %q needs a unique id and cannot contain nested groups", s.ID)
	}
	ids[s.ID] = true
	if s.TimeoutMS == 0 {
		s.TimeoutMS = 10000
	}
	if s.BudgetMS < 0 || s.BudgetMS > s.TimeoutMS {
		return fmt.Errorf("budget_ms must be between 0 and timeout_ms in %s", s.ID)
	}
	if err := validateHeaderRefs(s.HeaderRefs); err != nil {
		return fmt.Errorf("step %s: %w", s.ID, err)
	}
	if (s.Target != "" || len(s.HeaderRefs) > 0) && s.Kind != "http" {
		return fmt.Errorf("target and header_refs apply to HTTP steps only (%s)", s.ID)
	}
	if s.Target != "" && !slugPattern.MatchString(s.Target) {
		return fmt.Errorf("invalid target in %s", s.ID)
	}
	if s.TimeoutMS < 100 || s.TimeoutMS > 30000 || s.PollEveryMS < 0 ||
		s.PollEveryMS > s.TimeoutMS || s.PollEveryMS > 0 && s.PollEveryMS < 100 {
		return fmt.Errorf("invalid timing for step %s", s.ID)
	}
	switch s.Kind {
	case "browser":
		if err := s.validateBrowser(); err != nil {
			return err
		}
	case "http":
		if s.Method == "" {
			s.Method = http.MethodGet
		}
		s.Method = strings.ToUpper(s.Method)
		switch s.Method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		default:
			return fmt.Errorf("invalid HTTP method in %s", s.ID)
		}
		if s.Path == "" || (s.PollEveryMS > 0 && s.Method != "GET" && s.Method != "HEAD") {
			return fmt.Errorf("HTTP step %s needs a path; polling requires GET or HEAD", s.ID)
		}
		for k, v := range s.Headers {
			switch strings.ToLower(k) {
			case "host", "authorization", "proxy-authorization", "x-api-key", "cookie", "connection", "transfer-encoding":
				return fmt.Errorf("reserved header in %s; use an identity for credentials", s.ID)
			}
			if strings.ContainsAny(k+v, "\r\n") {
				return fmt.Errorf("invalid header in %s", s.ID)
			}
		}
	case "function":
		if strings.TrimSpace(s.Function) == "" || s.PollEveryMS > 0 {
			return fmt.Errorf("function step %s requires a function name and cannot poll", s.ID)
		}
	case "app_tool":
		if !slugPattern.MatchString(s.App) || !slugPattern.MatchString(s.Tool) {
			return fmt.Errorf("app-tool step %s requires an app and a tool", s.ID)
		}
	default:
		return fmt.Errorf("invalid kind in step %s", s.ID)
	}
	if err := validateAssertions(s.Assertions); err != nil {
		return fmt.Errorf("step %s: %w", s.ID, err)
	}
	if len(s.Schema) > 0 {
		if s.Kind != "http" {
			return errors.New("response_schema requires an HTTP step")
		}
		if _, err := compileSchema(s.Schema); err != nil {
			return fmt.Errorf("invalid response schema in %s: %w", s.ID, err)
		}
	}
	if len(s.Capture) > 16 || len(s.Evidence) > 16 {
		return fmt.Errorf("too many capture or evidence paths in %s", s.ID)
	}
	for name, path := range s.Capture {
		if !slugPattern.MatchString(name) || names[name] || !strings.HasPrefix(path, "/") {
			return fmt.Errorf("invalid or duplicate capture %q in %s", name, s.ID)
		}
		names[name] = true
	}
	for _, path := range s.Evidence {
		if !strings.HasPrefix(path, "/") {
			return fmt.Errorf("evidence must use a JSON pointer in %s", s.ID)
		}
	}
	return nil
}

func validateScenarioBinding(c Check, suite Suite) error {
	release, target := suite.Release, suite.Target
	if c.Kind == "code" {
		return validateCodeBinding(c, suite)
	}
	if target != nil && target.Mode == "source" {
		return errors.New("source-mode suites run only code checks")
	}
	if c.Definition.Session != "" && c.Definition.Session != "none" {
		return errors.New(`check session may only be "none"`)
	}
	if c.Definition.Session == "none" && suite.Session == nil {
		return errors.New(`session "none" requires a suite session`)
	}
	for _, ref := range credentialReferences(c) {
		alias, _, _ := strings.Cut(strings.TrimPrefix(ref, "credentials."), ".")
		if _, ok := suite.Credentials[alias]; !ok {
			return fmt.Errorf("credential alias %q is not declared on the suite", alias)
		}
	}
	if c.Kind == "http" && c.Definition.Target != "" {
		if target == nil || target.Mode != "existing" {
			return errors.New("HTTP checks with a target need an existing-mode suite target; use scenario steps in isolated mode")
		}
		if _, ok := target.Targets[c.Definition.Target]; !ok {
			return fmt.Errorf("unknown target %q", c.Definition.Target)
		}
	}
	isolated := release != nil || target != nil && target.Mode == "isolated"
	if c.Kind != "scenario" && c.Kind != "browser" {
		if isolated {
			return errors.New("release suites require bound scenario checks")
		}
		return nil
	}
	for _, s := range c.Definition.Steps {
		group := s.Parallel
		if len(group) == 0 {
			group = []Step{s}
		}
		for _, step := range group {
			if target != nil {
				if err := validateTargetStep(step, target); err != nil {
					return err
				}
				continue
			}
			if step.Target != "" || len(step.HeaderRefs) > 0 {
				return fmt.Errorf("step %s uses a target or header_refs, which need a suite target", step.ID)
			}
			if release == nil {
				if step.Kind == "browser" {
					return errors.New("browser steps require a suite target or an isolated release environment")
				}
				if step.Kind == "app_tool" || (step.Identity != "" && step.Identity != "anonymous") {
					return errors.New("app-tool steps and named identities require a release environment")
				}
				if step.Kind == "http" {
					u, err := url.Parse(step.Path)
					if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
						return errors.New("standalone HTTP steps require an absolute HTTP(S) URL")
					}
				}
				continue
			}
			if step.Kind == "browser" {
				if step.Browser.Locator.Selector != "" {
					return fmt.Errorf("step %s: CSS selectors are only supported in existing mode", step.ID)
				}
				if release.Browser == nil {
					return errors.New("release browser configuration is required")
				}
				if ref := step.Browser.ValueRef; ref != "" && !strings.HasPrefix(ref, "credentials.") {
					if _, ok := release.Browser.ValueRefs[ref]; !ok {
						return errors.New("unknown browser value_ref")
					}
				}
				if ref := step.Browser.UploadRef; ref != "" {
					if _, ok := release.Browser.Uploads[ref]; !ok {
						return errors.New("unknown browser upload_ref")
					}
				}
			}
			if step.Kind == "app_tool" && !release.allowsRead(step.App, step.Tool) {
				return fmt.Errorf("step %s is outside the release read-tool allowlist", step.ID)
			}
			if step.Identity != "" && step.Identity != "anonymous" {
				if _, ok := release.Identities[step.Identity]; !ok || step.Kind != "http" {
					return fmt.Errorf("unknown or misplaced identity in step %s", step.ID)
				}
			}
			if step.Kind == "http" && !wholeVariable(step.Path) {
				if err := validateGatewayPath(strings.SplitN(step.Path, "?", 2)[0]); err != nil || step.Path == "" {
					return fmt.Errorf("step %s needs a path relative to the bound Gateway", step.ID)
				}
			}
		}
	}
	return nil
}

// validateTargetStep checks one step of a suite that declares a target.
func validateTargetStep(step Step, target *TargetConfig) error {
	if step.Identity != "" && step.Identity != "anonymous" {
		return fmt.Errorf("step %s: named identities belong to hand-written release suites; use header_refs or a bearer session", step.ID)
	}
	switch step.Kind {
	case "browser":
		b := step.Browser
		if target.Mode == "existing" {
			switch {
			case b.Locator.Role != "":
				return fmt.Errorf("step %s: existing mode locates elements by test_id or selector; role locators need the isolated test browser", step.ID)
			case b.Locator.Text != "" && b.Action != "observe":
				return fmt.Errorf("step %s: existing mode clicks and fills by test_id or selector; text locators only work with observe", step.ID)
			}
			for _, assertion := range step.Assertions {
				switch {
				case isReadPath(assertion.Path):
					if b.Action != "observe" || b.Locator.Selector == "" && b.Locator.TestID == "" {
						return fmt.Errorf("step %s: %s needs an observe step with a test_id or selector locator", step.ID, assertion.Path)
					}
				case assertion.Path == "/visible", assertion.Path == "/path", assertion.Path == "/url", assertion.Path == "/target",
					assertion.Path == "/action_dispatched", assertion.Path == "/checked", assertion.Path == "/current_value",
					assertion.Path == "/text_value", assertion.Path == "/verified":
				default:
					return fmt.Errorf("step %s: existing-mode browser observations expose /visible, /count, /text, /value, /attributes/<name>, /path, /url, /target and /action_dispatched, not %s", step.ID, assertion.Path)
				}
			}
		} else if b.Locator.Selector != "" {
			return fmt.Errorf("step %s: CSS selectors are only supported in existing mode", step.ID)
		}
		if b.Action == "navigate" {
			if _, ok := target.Targets[b.Target]; !ok {
				return fmt.Errorf("step %s navigates to unknown target %q", step.ID, b.Target)
			}
		}
		if ref := b.ValueRef; ref != "" && !strings.HasPrefix(ref, "credentials.") {
			if _, ok := browserValueRef(target, ref); !ok {
				return fmt.Errorf("step %s uses unknown value_ref %q", step.ID, ref)
			}
		}
		if ref := b.UploadRef; ref != "" {
			if target.Browser == nil {
				return fmt.Errorf("step %s uses unknown upload_ref %q", step.ID, ref)
			}
			if _, ok := target.Browser.Uploads[ref]; !ok {
				return fmt.Errorf("step %s uses unknown upload_ref %q", step.ID, ref)
			}
		}
	case "http":
		if step.Target == "" {
			if target.Mode == "isolated" {
				return fmt.Errorf("step %s needs a target in isolated mode", step.ID)
			}
			if len(step.HeaderRefs) > 0 {
				return fmt.Errorf("step %s: header_refs need a named target", step.ID)
			}
			u, err := url.Parse(step.Path)
			if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
				return fmt.Errorf("step %s needs a target or an absolute HTTP(S) URL", step.ID)
			}
			return nil
		}
		if _, ok := target.Targets[step.Target]; !ok {
			return fmt.Errorf("step %s uses unknown target %q", step.ID, step.Target)
		}
		if !wholeVariable(step.Path) {
			if err := validateGatewayPath(strings.SplitN(step.Path, "?", 2)[0]); err != nil || step.Path == "" {
				return fmt.Errorf("step %s needs a path relative to its target", step.ID)
			}
		}
	case "app_tool":
		if target.Mode != "isolated" || !target.allowsRead(step.App, step.Tool) {
			return fmt.Errorf("step %s is outside the environment read-tool allowlist", step.ID)
		}
	}
	return nil
}

var variables = regexp.MustCompile(`\{\{([a-zA-Z0-9][a-zA-Z0-9_-]{0,127})\}\}`)

func wholeVariable(value string) bool {
	match := variables.FindString(value)
	return match != "" && match == value
}

func browserValueRef(target *TargetConfig, ref string) (ToolProbe, bool) {
	if target.Browser == nil {
		return ToolProbe{}, false
	}
	probe, ok := target.Browser.ValueRefs[ref]
	return probe, ok
}

// isReadPath reports whether an assertion path needs Computer's read_element
// (Computer 0.14.0 or newer).
func isReadPath(path string) bool {
	switch path {
	case "/count", "/text", "/value", "/redacted":
		return true
	}
	return strings.HasPrefix(path, "/attributes/") && len(path) > len("/attributes/")
}

// usesReadPaths reports whether a check asserts element text, value, count
// or attributes in a browser step.
func usesReadPaths(check Check) bool {
	var steps []Step
	steps = append(steps, check.Definition.Steps...)
	for i := 0; i < len(steps); i++ {
		steps = append(steps, steps[i].Parallel...)
		if steps[i].Kind != "browser" {
			continue
		}
		for _, assertion := range steps[i].Assertions {
			if isReadPath(assertion.Path) {
				return true
			}
		}
	}
	return false
}
