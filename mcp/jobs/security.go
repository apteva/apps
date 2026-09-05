package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func validIdentifier(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Execution payloads stay in the database. Every external Job serialization
// uses a deliberately small projection, including create/get and MCP results.
func publicJob(j *Job) *Job {
	if j == nil {
		return nil
	}
	out := *j
	out.Target = map[string]any{}
	for _, key := range []string{"kind", "app", "tool", "method", "instance_id", "timeout_seconds", "timeout_ms"} {
		if v, ok := j.Target[key]; ok {
			out.Target[key] = v
		}
	}
	if raw := strKey(j.Target, "url"); raw != "" {
		if u, err := url.Parse(raw); err == nil {
			u.User = nil
			u.RawQuery = ""
			u.Fragment = ""
			out.Target["url"] = u.Scheme + "://" + u.Host + "/[redacted]"
		}
	}
	for _, key := range []string{"headers", "body", "input", "message"} {
		if _, ok := j.Target[key]; ok {
			out.Target[key] = "[redacted]"
		}
	}
	out.IdempotencyKey = ""
	out.LastError = safeDispatchErrorString(j.LastError, j.Target)
	return &out
}
func (j Job) MarshalJSON() ([]byte, error) {
	type wire Job
	return json.Marshal((*wire)(publicJob(&j)))
}
func safeDispatchError(err error, target map[string]any) string {
	if err == nil {
		return ""
	}
	return safeDispatchErrorString(err.Error(), target)
}
func safeDispatchErrorString(s string, target map[string]any) string {
	// A destination can echo arbitrary credentials. Keep diagnostics categorical
	// instead of publishing remote bodies, URLs, or arbitrary remote error text.
	if s == "" {
		return ""
	}
	for _, safe := range []string{"Target deadline exceeded", "Delivery interrupted; outcome may be unknown", "Target is outside the job project", "Job owner authorization is no longer valid", "Target delivery failed; check the target configuration and run HTTP status"} {
		if s == safe {
			return s
		}
	}

	switch {
	case strings.HasPrefix(s, "HTTP target returned status "):
		if code, err := strconv.Atoi(strings.TrimPrefix(s, "HTTP target returned status ")); err == nil && code >= 100 && code <= 599 {
			return fmt.Sprintf("HTTP target returned status %d", code)
		}
		return "Target delivery failed"
	case strings.Contains(s, "exceeds 1 MiB"):
		return "Target response exceeded the 1 MiB limit"
	case strings.Contains(s, "connection refused"):
		return "Target connection refused"
	case strings.Contains(s, "no such host"):
		return "Target hostname could not be resolved"
	case strings.Contains(s, "context deadline exceeded"):
		return "Target deadline exceeded"
	case strings.Contains(s, "interrupted"):
		return "Delivery interrupted; outcome may be unknown"
	case strings.HasPrefix(s, "non-2xx: "):
		if code, err := strconv.Atoi(strings.TrimPrefix(s, "non-2xx: ")); err == nil && code >= 100 && code <= 599 {
			return fmt.Sprintf("HTTP target returned status %d", code)
		}
		return "Target delivery failed"
	case strings.Contains(s, "outside the job project"):
		return "Target is outside the job project"
	case strings.Contains(s, "authorization"):
		return "Job owner authorization is no longer valid"
	default:
		return "Target delivery failed; check the target configuration and run HTTP status"
	}
}
func (a *App) toolScheduleTrusted(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller := sdk.CallerFrom(callCtx)
	if caller == nil {
		// Direct CLI callers on a fixed project install retain compatibility. A
		// global install requires a bound identity; HTTP remains the admin surface.
		if pid := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); pid != "" {
			clean := map[string]any{}
			for k, v := range args {
				if !strings.HasPrefix(k, "_") {
					clean[k] = v
				}
			}
			delete(clean, "owner_instance")
			delete(clean, "owner_app")
			clean["_project_id"] = pid
			clean["_request_context"] = callCtx
			return a.toolSchedule(app, clean)
		}
		return nil, errors.New("authenticated caller context required for global scheduling")
	}
	if pid := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); pid != "" && caller.ProjectID != pid {
		return nil, errors.New("caller project does not match installation")
	}
	if caller.SubjectID != "" {
		return nil, errors.New("delegated users must use the authorized Jobs HTTP surface")
	}

	clean := make(map[string]any, len(args))
	for k, v := range args {
		if !strings.HasPrefix(k, "_") {
			clean[k] = v
		}
	}
	clean["_project_id"] = caller.ProjectID
	clean["_request_context"] = callCtx
	delete(clean, "owner_instance")
	delete(clean, "owner_app")
	if caller.AgentID > 0 {
		clean["_instance_id"] = caller.AgentID
		clean["owner_instance"] = caller.AgentID
	}
	if caller.AppName != "" {
		clean["owner_app"] = caller.AppName
	}
	if !caller.Allows("jobs.schedule", "") {
		return nil, errors.New("job scheduling authorization denied")
	}
	return a.toolSchedule(app.WithProject(caller.ProjectID), clean)
}
func authorizeDispatch(ctx context.Context, app *sdk.AppCtx, j *Job) error {
	if j.OwnerInstance == nil {
		return nil
	}
	owner, err := getInstanceContext(ctx, app, *j.OwnerInstance)
	if err != nil || owner == nil || owner.ProjectID != j.ProjectID {
		return errors.New("job owner authorization is no longer valid")
	}
	grants, err := getGrantsContext(ctx, app, *j.OwnerInstance)
	if err != nil || grants == nil {
		return errors.New("job owner authorization could not be verified")
	}
	caller := &sdk.Caller{AgentID: *j.OwnerInstance, Grants: grants.Grants, DefaultEffect: grants.DefaultEffect}
	if !caller.Allows("jobs.schedule", "") {
		return errors.New("job owner authorization revoked")
	}
	return nil
}

func (a *App) scopedTool(handler func(*sdk.AppCtx, map[string]any) (any, error)) func(context.Context, *sdk.AppCtx, map[string]any) (any, error) {
	return func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
		clean := make(map[string]any, len(args)+1)
		for k, v := range args {
			clean[k] = v
		}
		if caller := sdk.CallerFrom(ctx); caller != nil {
			if pid := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); pid != "" && pid != caller.ProjectID {
				return nil, errors.New("caller project does not match installation")
			}
			clean["_project_id"] = caller.ProjectID
			app = app.WithProject(caller.ProjectID)
		}
		clean["_request_context"] = ctx
		for _, key := range []string{"id", "before", "owner_instance"} {
			if err := boundedInteger(clean, key, 1, 9007199254740991); err != nil {
				return nil, err
			}
		}
		if err := boundedInteger(clean, "limit", 1, 500); err != nil {
			return nil, err
		}
		return handler(app, clean)
	}
}

func checkExecutionLease(ctx context.Context, app *sdk.AppCtx, j *Job) error {
	if j.LeaseToken == "" {
		return nil
	}
	var valid int
	if err := app.AppDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE id=? AND project_id=? AND status='running' AND lease_token=? AND lease_until>=?`, j.ID, j.ProjectID, j.LeaseToken, time.Now().UTC().Format(time.RFC3339)).Scan(&valid); err != nil {
		return err
	}
	if valid != 1 {
		return errors.New("delivery interrupted after lease loss")
	}
	return nil
}
func (r JobRun) MarshalJSON() ([]byte, error) {
	type wire JobRun
	r.IdempotencyKey = ""
	r.Error = safeDispatchErrorString(r.Error, nil)
	return json.Marshal(wire(r))
}

func reservedInputKey(key string) bool {
	switch key {
	case "_project_id", "_instance_id", "_agent_id", "_thread_id", "_job", "_install_id", "_app_install_id", "_caller_agent_id", "_caller_instance_id", "_caller_app", "_caller_install_id", "_subject_id", "_subject_type", "_organization_id":
		return true
	}
	return false
}

func validToolName(name string) bool {
	if strings.TrimSpace(name) == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
