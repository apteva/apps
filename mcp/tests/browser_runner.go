package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type browserCheckSession struct {
	id       string
	app      *sdk.AppCtx
	runID    int64
	checkID  int64
	evidence BrowserEvidence
}

func callComputer(ctx context.Context, app *sdk.AppCtx, tool string, args map[string]any, out any) error {
	if app.PlatformAPI() == nil {
		return errors.New("Computer binding is unavailable")
	}
	return sdk.CallAppResultContext(ctx, app.PlatformAPI(), "computer", tool, args, out)
}
func (a *App) browserCapabilities(ctx context.Context, app *sdk.AppCtx) error {
	var capabilities struct {
		Protocol    string   `json:"protocol"`
		Backend     string   `json:"backend"`
		Available   bool     `json:"available"`
		Fresh       bool     `json:"fresh_context"`
		Boundary    bool     `json:"runtime_network_boundary"`
		Screenshots bool     `json:"screenshots"`
		Actions     []string `json:"actions"`
	}
	if err := callComputer(ctx, app, "browser_test_capabilities", map[string]any{}, &capabilities); err != nil {
		return errors.New("a Computer binding with browser test protocol v1 is required")
	}
	if capabilities.Protocol != browserProtocol || capabilities.Backend != "local" || !capabilities.Available || !capabilities.Fresh || !capabilities.Boundary || !capabilities.Screenshots {
		return errors.New("Computer did not report the required test capabilities")
	}
	for _, action := range []string{"navigate", "reload", "observe", "click", "fill", "select", "check", "upload"} {
		found := false
		for _, supported := range capabilities.Actions {
			found = found || supported == action
		}
		if !found {
			return errors.New("Computer lacks a required test action")
		}
	}
	return nil
}
func (a *App) openBrowserCheck(ctx context.Context, check Check, app *appRunContext) (*browserCheckSession, error) {
	if app.release == nil || app.release.config.Browser == nil || len(app.release.targets) == 0 {
		return nil, errors.New("browser check has no isolated targets")
	}
	if err := a.browserCapabilities(ctx, app.ctx); err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	session := &browserCheckSession{id: "bt_" + hex.EncodeToString(nonce[:]), app: app.ctx, runID: app.runID, checkID: check.ID}
	if _, err := app.ctx.AppDB().Exec(`INSERT INTO test_browser_leases(id,project_id,run_id,check_id) VALUES(?,?,?,?)`, session.id, app.ctx.CurrentProject(), app.runID, check.ID); err != nil {
		return nil, err
	}
	app.browser = session // retain the reserved ID even if startup times out
	config := app.release.config.Browser
	options := map[string]any{"width": config.Width, "height": config.Height, "locale": config.Locale, "timezone": config.Timezone,
		"policy": map[string]any{"runtime_id": app.release.runtimeID, "targets": app.release.targets}}
	ttl := check.Definition.TimeoutMS/1000 + 20
	if ttl < 30 {
		ttl = 30
	}
	if ttl > 300 {
		ttl = 300
	}
	for {
		var opened struct {
			ID       string `json:"id"`
			State    string `json:"state"`
			Protocol string `json:"protocol"`
			Engine   string `json:"engine"`
		}
		err := callComputer(ctx, app.ctx, "browser_test_session", map[string]any{"action": "open", "id": session.id, "options": options, "ttl_seconds": ttl}, &opened)
		if err != nil {
			return nil, errors.New("Computer could not open the isolated test browser")
		}
		if opened.State == "busy" {
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
				continue
			}
		}
		if opened.ID != session.id || opened.State != "active" || opened.Protocol != browserProtocol || opened.Engine == "" {
			return nil, errors.New("invalid browser test startup reply")
		}
		session.evidence = BrowserEvidence{SessionID: session.id, Protocol: opened.Protocol, Engine: opened.Engine, Width: config.Width, Height: config.Height, Locale: config.Locale, Timezone: config.Timezone}
		return session, nil
	}
}
func (a *App) executeBrowserStep(ctx context.Context, step Step, values map[string]any, app *appRunContext, result *StepResult) (any, error) {
	if app.browser == nil || app.release == nil || step.Browser == nil {
		return nil, errors.New("browser test session is unavailable")
	}
	action := *step.Browser
	var err error
	action.Path, err = expand(action.Path, values, nil)
	if err != nil {
		return nil, err
	}
	action.Value, err = expand(action.Value, values, nil)
	if err != nil {
		return nil, err
	}
	if action.ValueRef != "" {
		value, ok := app.release.browserValues[action.ValueRef]
		if !ok {
			return nil, errors.New("browser value reference is unavailable")
		}
		action.Value, action.Sensitive = value, true
	}
	command := map[string]any{"action": action.Action, "target": action.Target, "path": action.Path,
		"locator": action.Locator, "value": action.Value, "sensitive": action.Sensitive, "checked": action.Checked}
	if action.UploadRef != "" {
		command["file"] = app.release.config.Browser.Uploads[action.UploadRef]
	}
	invoke := func(command map[string]any) (map[string]any, error) {
		var response struct {
			Observation map[string]any `json:"observation"`
			Error       string         `json:"error"`
			Dispatched  bool           `json:"action_dispatched"`
		}
		if err := callComputer(ctx, app.ctx, "browser_test_step", map[string]any{"id": app.browser.id, "command": command}, &response); err != nil {
			return nil, errors.New("browser test command interrupted")
		}
		result.ActionDispatched = response.Dispatched
		if response.Error != "" {
			return nil, errors.New(response.Error)
		}
		if response.Observation == nil {
			response.Observation = map[string]any{}
		}
		response.Observation["action_dispatched"] = response.Dispatched
		return response.Observation, nil
	}
	// Wait only for target readiness. The mutating action below is dispatched once.
	if action.Action != "observe" && action.Action != "navigate" && action.Action != "reload" {
		observation := map[string]any{"action": "observe", "locator": action.Locator}
		for {
			observed, err := invoke(observation)
			if err != nil {
				return nil, err
			}
			count, _ := observed["count"].(float64)
			if count > 1 {
				return nil, errors.New("browser locator matches multiple elements")
			}
			if count == 1 && observed["visible"] == true && observed["enabled"] == true {
				break
			}
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, errors.New("browser target readiness deadline exceeded")
			case <-timer.C:
			}
		}
	}
	return invoke(command)
}
func (a *App) closeBrowserCheck(ctx context.Context, session *browserCheckSession) (BrowserEvidence, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var closed struct {
		ID          string             `json:"id"`
		State       string             `json:"state"`
		Diagnostics BrowserDiagnostics `json:"diagnostics"`
	}
	err := callComputer(ctx, session.app, "browser_test_session", map[string]any{"action": "close", "id": session.id}, &closed)
	if err != nil || closed.ID != session.id || closed.State != "closed" && closed.State != "interrupted" {
		return session.evidence, errors.New("browser cleanup is pending")
	}
	session.evidence.Diagnostics = closed.Diagnostics
	if _, err = session.app.AppDB().Exec(`UPDATE test_browser_leases SET closed=1 WHERE id=? AND project_id=?`, session.id, session.app.CurrentProject()); err != nil {
		return session.evidence, err
	}
	if closed.State == "interrupted" {
		return session.evidence, errors.New("browser session was interrupted")
	}
	if len(closed.Diagnostics.Errors) > 0 || closed.Diagnostics.Blocked > 0 || closed.Diagnostics.Truncated {
		return session.evidence, errors.New("browser reported page errors, blocked traffic, or incomplete diagnostics")
	}
	return session.evidence, nil
}
func (a *App) captureBrowserFailure(ctx context.Context, session *browserCheckSession, step string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var captured struct {
		Artifact browserArtifact `json:"artifact"`
	}
	if err := callComputer(ctx, session.app, "browser_test_capture", map[string]any{"id": session.id}, &captured); err != nil {
		return 0, err
	}
	return saveBrowserArtifact(session.app, session.runID, session.checkID, step, captured.Artifact)
}
func (a *App) reconcileBrowserCleanup(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().Query(`SELECT l.id,l.run_id,l.check_id FROM test_browser_leases l JOIN test_runs r ON r.id=l.run_id AND r.project_id=l.project_id WHERE l.project_id=? AND l.closed=0 AND r.status IN ('passed','failed') LIMIT 20`, app.CurrentProject())
	if err != nil {
		return err
	}
	var leases []*browserCheckSession
	for rows.Next() {
		s := &browserCheckSession{app: app}
		if err = rows.Scan(&s.id, &s.runID, &s.checkID); err != nil {
			rows.Close()
			return err
		}
		leases = append(leases, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, lease := range leases {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, _ = a.closeBrowserCheck(ctx, lease)
	}
	return nil
}
func safeBrowserError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 256 || strings.ContainsAny(message, "\r\n") {
		return "browser execution failed"
	}
	return fmt.Sprint(message)
}
