package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// prepareTarget resolves credentials and existing-mode targets, and confirms
// the live candidate still matches the one the run was queued for.
func (a *App) prepareTarget(ctx context.Context, run *appRunContext, r *Run) error {
	snap := run.snapshot
	run.credentials = map[string]string{}
	suite := &Suite{Credentials: snap.Credentials}
	for _, check := range r.Checks {
		for _, ref := range credentialReferences(check) {
			if _, done := run.credentials[ref]; done {
				continue
			}
			value, err := credentialValue(run.ctx, suite, ref)
			if err != nil {
				return err
			}
			run.credentials[ref] = value
			run.addSecret(value)
		}
	}
	if snap.Target == nil || snap.Target.Mode != "existing" {
		return nil
	}
	run.targets = map[string]*url.URL{}
	for name, target := range snap.Target.Targets {
		u, err := url.Parse(target.BaseURL)
		if err != nil {
			return fmt.Errorf("invalid target %q", name)
		}
		run.targets[name] = u
	}
	if r.Release != nil && snap.Candidate != nil {
		live, err := resolveCandidate(ctx, run.ctx, snap.Candidate)
		if err != nil {
			return err
		}
		if live.Digest != r.Release.CandidateDigest {
			return fmt.Errorf("the live build changed after the run was queued (build %d, digest %s, expected %s)",
				live.BuildID, short(live.Digest), short(r.Release.CandidateDigest))
		}
	}
	return nil
}

// observeExistingCandidate re-reads the live build after the checks ran.
func (a *App) observeExistingCandidate(ctx context.Context, run *appRunContext, record *ReleaseRecord) string {
	if run.snapshot.Candidate == nil {
		return "existing-mode verdicts need target.candidate"
	}
	live, err := resolveCandidate(ctx, run.ctx, run.snapshot.Candidate)
	if err != nil {
		record.Inconclusive = true
		return "inconclusive: " + err.Error()
	}
	record.ObservedDigest = live.Digest
	if live.Digest != record.CandidateDigest {
		record.Inconclusive = true
		return fmt.Sprintf("inconclusive: Deploy deployment %d changed from build %d to build %d during the run",
			run.snapshot.Candidate.DeploymentID, record.BuildID, live.BuildID)
	}
	return ""
}

// observeSourceCandidate re-reads the build after its source was tested. A
// source-mode verdict needs a code check that tested the candidate's source.
func (a *App) observeSourceCandidate(ctx context.Context, run *appRunContext, record *ReleaseRecord) string {
	if record.SourceDigest == "" {
		record.Inconclusive = true
		return "inconclusive: no code check tested the candidate's source"
	}
	var out deployCandidate
	err := sdk.CallAppResultContext(ctx, run.ctx.PlatformAPI(), "deploy", "deploy_candidate_digest",
		map[string]any{"build_id": record.BuildID, "_project_id": run.ctx.CurrentProject()}, &out)
	if err != nil {
		record.Inconclusive = true
		return "inconclusive: Deploy could not confirm build " + fmt.Sprint(record.BuildID) + ": " + deployReason(err)
	}
	record.ObservedDigest = out.Digest
	if out.Digest != record.CandidateDigest {
		record.Inconclusive = true
		return fmt.Sprintf("inconclusive: build %d now has digest %s, the run was queued for %s", record.BuildID, short(out.Digest), short(record.CandidateDigest))
	}
	if out.SourceDigest != "" && out.SourceDigest != record.SourceDigest {
		record.Inconclusive = true
		return fmt.Sprintf("inconclusive: Deploy records source %s for build %d, the run tested %s", out.SourceDigest, record.BuildID, record.SourceDigest)
	}
	return ""
}

// orderChecks runs the session's setup check first.
func orderChecks(checks []Check, session *SessionConfig) []Check {
	if session == nil {
		return checks
	}
	ordered := make([]Check, 0, len(checks))
	for _, c := range checks {
		if c.Name == session.SetupCheck {
			ordered = append(ordered, c)
		}
	}
	for _, c := range checks {
		if c.Name != session.SetupCheck {
			ordered = append(ordered, c)
		}
	}
	return ordered
}

// completeSession records what the setup check produced. It returns the
// reason dependent checks are blocked, or "" when the session is ready.
func (r *appRunContext) completeSession(session *SessionConfig, result Result) string {
	if result.Status != "passed" {
		return fmt.Sprintf("blocked: session setup check %q did not pass", session.SetupCheck)
	}
	switch session.Kind {
	case "bearer":
		token, _ := r.captures[session.Capture].(string)
		if token == "" || strings.ContainsAny(token, "\r\n") || len(token) > 8192 {
			return fmt.Sprintf("blocked: session setup check %q did not capture a usable %q", session.SetupCheck, session.Capture)
		}
		r.addSecret(token)
		r.sessionToken = session.Prefix + token
	case "browser":
		if r.sessionContext == "" {
			return fmt.Sprintf("blocked: session setup check %q did not leave a signed-in browser", session.SetupCheck)
		}
	}
	return ""
}

// applyAuth adds header_refs credentials and the suite's bearer session.
func (r *appRunContext) applyAuth(req *http.Request, refs map[string]HeaderRef) error {
	for name, ref := range refs {
		value := r.credentials[ref.Ref]
		if value == "" {
			return fmt.Errorf("credential %s is unavailable", ref.Ref)
		}
		req.Header.Set(name, ref.Prefix+value)
	}
	if r.sessionToken != "" && !r.sessionOff && !r.setupCheck && r.snapshot.Session != nil {
		if header := r.snapshot.Session.Header; req.Header.Get(header) == "" {
			req.Header.Set(header, r.sessionToken)
		}
	}
	return nil
}

func scrubText(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return text
}

func scrubAny(v any, secrets []string) any {
	switch value := v.(type) {
	case string:
		return scrubText(value, secrets)
	case map[string]any:
		for k, item := range value {
			value[k] = scrubAny(item, secrets)
		}
		return value
	case []any:
		for i, item := range value {
			value[i] = scrubAny(item, secrets)
		}
		return value
	default:
		return v
	}
}

// scrubResult removes every resolved credential and session token from a
// result before it is stored.
func scrubResult(result Result, secrets []string) Result {
	if len(secrets) == 0 {
		return result
	}
	result.Error = scrubText(result.Error, secrets)
	if raw, err := json.Marshal(result.Output); err == nil {
		var generic any
		if json.Unmarshal(raw, &generic) == nil {
			result.Output = scrubAny(generic, secrets)
		} else {
			result.Output = nil
		}
	}
	raw, err := json.Marshal(result.Assertions)
	if err == nil {
		var generic []any
		if json.Unmarshal(raw, &generic) == nil {
			cleaned, _ := json.Marshal(scrubAny(generic, secrets))
			var assertions []AssertionResult
			if json.Unmarshal(cleaned, &assertions) == nil {
				result.Assertions = assertions
			}
		}
	}
	return result
}

// checkBrowserBinding confirms the Computer binding a suite's browser checks need.
func (a *App) checkBrowserBinding(ctx context.Context, app *sdk.AppCtx, v Suite) error {
	if v.Target != nil && v.Target.Mode == "existing" {
		return a.webBinding(ctx, app)
	}
	if err := a.browserCapabilities(ctx, app); err != nil {
		return err
	}
	return nil
}

var errNoTarget = errors.New("browser check has no targets")
