package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const maxChecks = 50
const maxPayload = 256 << 10

type Suite struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Environment string `json:"environment"`
	Archived    bool   `json:"archived"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// Definition is versioned by copying the whole check into the queued run.
// JSON pointers address the normalized runner output (e.g. /response/total).
type Definition struct {
	Function   string            `json:"function,omitempty"`
	Event      any               `json:"event,omitempty"`
	URL        string            `json:"url,omitempty"`
	Method     string            `json:"method,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
	TimeoutMS  int               `json:"timeout_ms"`
	Assertions []Assertion       `json:"assertions"`
}
type Assertion struct {
	Path  string `json:"path"`
	Op    string `json:"op"`
	Value any    `json:"value,omitempty"`
}
type Check struct {
	ID         int64      `json:"id"`
	SuiteID    int64      `json:"suite_id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Enabled    bool       `json:"enabled"`
	Definition Definition `json:"definition"`
}
type Run struct {
	ID          int64    `json:"id"`
	SuiteID     int64    `json:"suite_id"`
	SuiteName   string   `json:"suite_name"`
	Environment string   `json:"environment"`
	Status      string   `json:"status"`
	Trigger     string   `json:"trigger"`
	Passed      int      `json:"passed"`
	Failed      int      `json:"failed"`
	Error       string   `json:"error"`
	CreatedAt   string   `json:"created_at"`
	StartedAt   string   `json:"started_at"`
	FinishedAt  string   `json:"finished_at"`
	Checks      []Check  `json:"checks,omitempty"`
	Results     []Result `json:"results,omitempty"`
}
type AssertionResult struct {
	Assertion
	Passed bool `json:"passed"`
	Actual any  `json:"actual"`
	Exists bool `json:"exists"`
}
type Result struct {
	CheckID    int64             `json:"check_id"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Status     string            `json:"status"`
	DurationMS int64             `json:"duration_ms"`
	Output     any               `json:"output"`
	Assertions []AssertionResult `json:"assertions"`
	Error      string            `json:"error"`
}

func now() string         { return time.Now().UTC().Format(time.RFC3339Nano) }
func encode(v any) string { b, _ := json.Marshal(v); return string(b) }
func decodeArgs(args map[string]any, dst any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	if len(b) > maxPayload {
		return errors.New("definition exceeds 256 KiB")
	}
	return json.Unmarshal(b, dst)
}
func validateCheck(c *Check) error {
	c.Name = strings.TrimSpace(c.Name)
	if c.SuiteID <= 0 || c.Name == "" || len(c.Name) > 200 {
		return errors.New("suite_id and name (1–200 characters) required")
	}
	d := &c.Definition
	if d.TimeoutMS == 0 {
		d.TimeoutMS = 10000
	}
	if d.TimeoutMS < 100 || d.TimeoutMS > 30000 {
		return errors.New("timeout_ms must be 100–30000")
	}
	switch c.Kind {
	case "function":
		if strings.TrimSpace(d.Function) == "" {
			return errors.New("definition.function required")
		}
	case "http":
		u, err := url.Parse(d.URL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return errors.New("definition.url must be an HTTP(S) URL without embedded credentials")
		}
		if d.Method == "" {
			d.Method = "GET"
		}
		d.Method = strings.ToUpper(d.Method)
		switch d.Method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		default:
			return errors.New("unsupported HTTP method")
		}
		for k, v := range d.Headers {
			if strings.ContainsAny(k+v, "\r\n") || strings.EqualFold(k, "Host") {
				return errors.New("invalid HTTP header")
			}
		}
	default:
		return errors.New("kind must be function or http")
	}
	if len(d.Assertions) == 0 || len(d.Assertions) > 50 {
		return errors.New("provide 1–50 assertions")
	}
	for _, a := range d.Assertions {
		if a.Path != "" && !strings.HasPrefix(a.Path, "/") {
			return errors.New("assertion path must be a JSON pointer, e.g. /response/total")
		}
		for i := 0; i < len(a.Path); i++ {
			if a.Path[i] == '~' {
				if i+1 == len(a.Path) || (a.Path[i+1] != '0' && a.Path[i+1] != '1') {
					return errors.New("invalid JSON pointer escape")
				}
				i++
			}
		}
		switch a.Op {
		case "equals", "not_equals", "exists":
		case "contains":
			if _, ok := a.Value.(string); !ok {
				return errors.New("contains expects a string value")
			}
		case "lt", "lte", "gt", "gte":
			if _, ok := a.Value.(float64); !ok {
				return errors.New("numeric comparison expects a number")
			}
		default:
			return fmt.Errorf("unsupported assertion operator %q", a.Op)
		}
	}
	return nil
}
