package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkflowDef is the parsed-from-source workflow definition. The
// shape mirrors the on-disk YAML/JSON one-to-one — fields that
// don't apply to a step's kind are simply zero-valued.
//
// The whole struct is JSON-tagged in addition to YAML-tagged so
// the same parser handles both. We auto-detect by sniffing the
// first non-whitespace byte: '{' or '[' = JSON, anything else =
// YAML. This matches what most config tooling does.
type WorkflowDef struct {
	Name    string     `yaml:"name" json:"name"`
	Trigger TriggerDef `yaml:"trigger" json:"trigger"`
	Steps   []StepDef  `yaml:"steps" json:"steps"`
}

// TriggerDef captures the workflow's trigger config. Only `kind` is
// required; the other fields are kind-specific and validated at
// parse time.
//
// v0.1 supports kind=http and kind=manual only. event/schedule are
// reserved for v0.2 — parser accepts them so YAML written today
// stays valid after the upgrade.
type TriggerDef struct {
	Kind   string `yaml:"kind" json:"kind"`
	Topic  string `yaml:"topic,omitempty" json:"topic,omitempty"`
	Source string `yaml:"source,omitempty" json:"source,omitempty"` // for kind=event: source app
	Cron   string `yaml:"cron,omitempty" json:"cron,omitempty"`     // for kind=schedule
}

// StepDef is a tagged-union; valid combinations depend on Kind.
// The parser doesn't reject unknown fields — yaml.v3 silently
// ignores them. That's fine for forward-compat; we instead
// validate the *required* fields per kind in Validate().
type StepDef struct {
	ID   string `yaml:"id" json:"id"`
	Kind string `yaml:"kind" json:"kind"` // http | function | app | emit | branch

	// Universal: input gets templated against {input, steps, env, now}
	// at execution time. For http it goes into the request body; for
	// function/app it's the tool args; for emit it's the event data.
	Input any `yaml:"input,omitempty" json:"input,omitempty"`

	// kind=http
	URL    string `yaml:"url,omitempty" json:"url,omitempty"`
	App    string `yaml:"app,omitempty" json:"app,omitempty"`
	Path   string `yaml:"path,omitempty" json:"path,omitempty"`
	Method string `yaml:"method,omitempty" json:"method,omitempty"`

	// kind=function
	FunctionName string `yaml:"name,omitempty" json:"name,omitempty"`

	// kind=app
	// (App reuses the http App field — same target slug.)
	Tool string `yaml:"tool,omitempty" json:"tool,omitempty"`

	// kind=integration — calls an integration connection (Pushover,
	// Slack, Resend, ...) via the platform's ExecuteIntegrationTool.
	// (Tool is reused — same field as kind=app.) ConnectionID is the
	// integer id of an in-project connection; the workflow author
	// copies it from the dashboard's Connections panel.
	ConnectionID int64 `yaml:"connection_id,omitempty" json:"connection_id,omitempty"`

	// kind=emit
	Topic string `yaml:"topic,omitempty" json:"topic,omitempty"`
	Data  any    `yaml:"data,omitempty" json:"data,omitempty"`

	// kind=branch
	When string `yaml:"when,omitempty" json:"when,omitempty"`
	Else *Goto  `yaml:"else,omitempty" json:"else,omitempty"`

	// Common
	Retry   *RetryConfig `yaml:"retry,omitempty" json:"retry,omitempty"`
	OnError *Goto        `yaml:"on_error,omitempty" json:"on_error,omitempty"`
}

// Goto picks a different step to jump to. Used by branch.else and
// step.on_error. Goto.End=true terminates the run as success;
// Goto.Fail=true terminates as failure with the supplied message.
type Goto struct {
	StepID  string `yaml:"goto,omitempty" json:"goto,omitempty"`
	End     bool   `yaml:"end,omitempty" json:"end,omitempty"`
	Fail    bool   `yaml:"fail,omitempty" json:"fail,omitempty"`
	Message string `yaml:"message,omitempty" json:"message,omitempty"`
}

// RetryConfig — per-step retry policy. Same shape as jobs uses for
// dispatcher retries: linear/exponential isn't a knob, the runner
// always uses exponential (delay = backoff_seconds * 2^(attempt-1)).
type RetryConfig struct {
	Max            int `yaml:"max,omitempty" json:"max,omitempty"`
	BackoffSeconds int `yaml:"backoff_seconds,omitempty" json:"backoff_seconds,omitempty"`
}

// validKinds and stepNameRE constrain what the parser accepts.
var validKinds = map[string]bool{
	"http": true, "function": true, "app": true, "integration": true, "emit": true, "branch": true,
}

var validTriggerKinds = map[string]bool{
	"http": true, "manual": true, "event": true, "schedule": true,
}

var stepIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ParseDefinition decodes raw bytes into a WorkflowDef. Auto-detects
// JSON vs. YAML by the first non-space byte.
func ParseDefinition(b []byte) (*WorkflowDef, error) {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" {
		return nil, errors.New("workflow source is empty")
	}
	def := &WorkflowDef{}
	first := trimmed[0]
	if first == '{' || first == '[' {
		if err := json.Unmarshal([]byte(trimmed), def); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
	} else {
		if err := yaml.Unmarshal([]byte(trimmed), def); err != nil {
			return nil, fmt.Errorf("parse yaml: %w", err)
		}
	}
	if err := def.Validate(); err != nil {
		return nil, err
	}
	return def, nil
}

// Validate runs structural checks: trigger kind known, step ids
// unique + slug-shaped, branch.goto / on_error.goto refer to
// declared steps. Field-level checks per kind happen here too.
func (d *WorkflowDef) Validate() error {
	if d.Name == "" {
		return errors.New("workflow.name required")
	}
	if d.Trigger.Kind == "" {
		d.Trigger.Kind = "manual"
	}
	if !validTriggerKinds[d.Trigger.Kind] {
		return fmt.Errorf("trigger.kind %q must be http|manual|event|schedule", d.Trigger.Kind)
	}
	if len(d.Steps) == 0 {
		return errors.New("workflow has no steps")
	}

	seen := map[string]bool{}
	for i := range d.Steps {
		s := &d.Steps[i]
		if !stepIDRE.MatchString(s.ID) {
			return fmt.Errorf("step %d: id %q must match [a-z0-9][a-z0-9_-]{0,62}", i, s.ID)
		}
		if seen[s.ID] {
			return fmt.Errorf("step %q: duplicate id", s.ID)
		}
		seen[s.ID] = true
		if !validKinds[s.Kind] {
			return fmt.Errorf("step %q: kind %q must be http|function|app|emit|branch", s.ID, s.Kind)
		}
		if err := validateKind(s); err != nil {
			return fmt.Errorf("step %q: %w", s.ID, err)
		}
	}
	// Goto targets must exist.
	for i := range d.Steps {
		s := &d.Steps[i]
		for _, g := range []*Goto{s.Else, s.OnError} {
			if g == nil || g.StepID == "" {
				continue
			}
			if !seen[g.StepID] {
				return fmt.Errorf("step %q: goto target %q not declared", s.ID, g.StepID)
			}
		}
	}
	return nil
}

// validateKind enforces kind-specific required fields. Done here
// rather than at execution time so a malformed workflow fails fast
// at create/update.
func validateKind(s *StepDef) error {
	switch s.Kind {
	case "http":
		// Either a fully qualified URL or {app, path}. Mirrors jobs'
		// resolveTargetURL — we want the same ergonomics.
		if s.URL == "" && (s.App == "" || s.Path == "") {
			return errors.New("http step needs url or {app, path}")
		}
	case "function":
		if s.FunctionName == "" {
			return errors.New("function step needs name")
		}
	case "app":
		if s.App == "" || s.Tool == "" {
			return errors.New("app step needs app and tool")
		}
	case "integration":
		if s.ConnectionID <= 0 {
			return errors.New("integration step needs connection_id")
		}
		if s.Tool == "" {
			return errors.New("integration step needs tool")
		}
	case "emit":
		if s.Topic == "" {
			return errors.New("emit step needs topic")
		}
	case "branch":
		if s.When == "" {
			return errors.New("branch step needs when")
		}
	}
	return nil
}

// stepIndex returns the position of step `id` within d.Steps, or -1
// if it doesn't exist. Used by the runner to jump on goto/on_error.
func (d *WorkflowDef) stepIndex(id string) int {
	for i := range d.Steps {
		if d.Steps[i].ID == id {
			return i
		}
	}
	return -1
}
