package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

type Device struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Platform     string   `json:"platform"`
	UDID         string   `json:"udid"`
	Physical     bool     `json:"physical"`
	Driver       string   `json:"driver"`
	Browser      string   `json:"browser,omitempty"`
	OS           string   `json:"os,omitempty"`
	State        string   `json:"state"`
	Capabilities []string `json:"capabilities"`
	Hint         string   `json:"hint,omitempty"`
}

type Session struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	DeviceID   string `json:"device_id"`
	Device     Device `json:"device"`
	Mode       string `json:"mode"`
	Status     string `json:"status"`
	ProviderID string `json:"-"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
	Error      string `json:"error,omitempty"`
}

type Observation struct {
	Image  string `json:"image_base64"`
	MIME   string `json:"mime_type"`
	Source string `json:"source,omitempty"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type Step struct {
	Action string         `json:"action"`
	Label  string         `json:"label,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

type StepResult struct {
	Index      int    `json:"index"`
	Action     string `json:"action"`
	Label      string `json:"label,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
}

type TestRun struct {
	ID          string       `json:"id"`
	ProjectID   string       `json:"project_id"`
	DeviceID    string       `json:"device_id"`
	Name        string       `json:"name"`
	BuildRef    string       `json:"build_ref,omitempty"`
	Status      string       `json:"status"`
	FailureKind string       `json:"failure_kind,omitempty"`
	SessionID   string       `json:"session_id,omitempty"`
	CreatedAt   int64        `json:"created_at"`
	FinishedAt  *int64       `json:"finished_at,omitempty"`
	Error       string       `json:"error,omitempty"`
	Results     []StepResult `json:"results"`
}

type Backend interface {
	Open(context.Context, Device, string) (string, error)
	Close(context.Context, Session) error
	Observe(context.Context, Session, bool) (Observation, error)
	Action(context.Context, Session, string, map[string]any) (any, error)
	App(context.Context, Session, string, map[string]any) (any, error)
}

type Executor interface {
	Execute(context.Context, string, string, map[string]any) (any, error)
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"error"`
}

func (e *apiError) Error() string        { return e.Message }
func problem(code, message string) error { return &apiError{code, message} }
func errorCode(err error) string {
	var e *apiError
	if errors.As(err, &e) {
		return e.Code
	}
	return "infrastructure_error"
}
func id(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
func str(m map[string]any, k string) string   { s, _ := m[k].(string); return s }
func boolean(m map[string]any, k string) bool { b, _ := m[k].(bool); return b }
func integer(m map[string]any, k string, def int) int {
	v, ok := number(m[k])
	if !ok {
		return def
	}
	return int(v)
}
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case int:
		return float64(n), true
	case json.Number:
		f, e := n.Float64()
		return f, e == nil
	}
	return 0, false
}
func coord(m map[string]any, k string) (float64, error) {
	n, ok := number(m[k])
	if !ok || n < 0 || n > 1 {
		return 0, problem("invalid_argument", k+" must be between 0 and 1")
	}
	return n, nil
}
func decodeMap(v any, target any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, target)
}
func jsonText(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return string(b)
}
func requireString(m map[string]any, k string) (string, error) {
	v := str(m, k)
	if strings.TrimSpace(v) == "" {
		return "", problem("invalid_argument", k+" is required")
	}
	if len(v) > 8192 {
		return "", fmt.Errorf("%s is too long", k)
	}
	return v, nil
}
