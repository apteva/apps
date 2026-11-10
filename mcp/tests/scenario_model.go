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
	ID          string            `json:"id"`
	Kind        string            `json:"kind,omitempty"`
	Path        string            `json:"path,omitempty"`
	Method      string            `json:"method,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	JSON        any               `json:"json,omitempty"`
	Identity    string            `json:"identity,omitempty"`
	Function    string            `json:"function,omitempty"`
	Event       any               `json:"event,omitempty"`
	App         string            `json:"app,omitempty"`
	Tool        string            `json:"tool,omitempty"`
	Input       map[string]any    `json:"input,omitempty"`
	TimeoutMS   int               `json:"timeout_ms,omitempty"`
	PollEveryMS int               `json:"poll_every_ms,omitempty"`
	Assertions  []Assertion       `json:"assertions,omitempty"`
	Schema      json.RawMessage   `json:"response_schema,omitempty"`
	Capture     map[string]string `json:"capture,omitempty"`
	Evidence    []string          `json:"evidence,omitempty"`
	Parallel    []Step            `json:"parallel,omitempty"`
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
	if s.TimeoutMS < 100 || s.TimeoutMS > 30000 || s.PollEveryMS < 0 ||
		s.PollEveryMS > s.TimeoutMS || s.PollEveryMS > 0 && s.PollEveryMS < 100 {
		return fmt.Errorf("invalid timing for step %s", s.ID)
	}
	switch s.Kind {
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

func validateScenarioBinding(c Check, release *ReleaseConfig) error {
	if c.Kind != "scenario" {
		if release != nil {
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
			if release == nil {
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

var variables = regexp.MustCompile(`\{\{([a-zA-Z0-9][a-zA-Z0-9_-]{0,127})\}\}`)

func wholeVariable(value string) bool {
	match := variables.FindString(value)
	return match != "" && match == value
}
