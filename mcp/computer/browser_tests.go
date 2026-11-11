package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/computer/internal/browser/testbrowser"
)

type testBrowserManager struct {
	mu       sync.Mutex
	sessions map[string]*testBrowserLease
}
type testBrowserLease struct {
	browser *testbrowser.Browser
	project string
	owner   int64
	profile string
	timer   *time.Timer
}

var testSessionID = regexp.MustCompile(`^bt_[a-f0-9]{32}$`)

func testBrowserOwner(call context.Context, app *sdk.AppCtx) (int64, string, error) {
	caller := sdk.CallerFrom(call)
	if caller == nil || caller.AppName != "tests" || caller.AppInstallID < 1 || caller.ProjectID == "" ||
		(app.CurrentProject() != "" && app.CurrentProject() != caller.ProjectID) {
		return 0, "", errors.New("browser test tools require a project-scoped call from Tests")
	}
	return caller.AppInstallID, caller.ProjectID, nil
}
func (a *App) browserTestTools() []sdk.Tool {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"id":          map[string]any{"type": "string"},
		"action":      map[string]any{"type": "string"},
		"options":     map[string]any{"type": "object"},
		"command":     map[string]any{"type": "object"},
		"ttl_seconds": map[string]any{"type": "integer"},
	}}
	wrap := func(name, description string, handler func(context.Context, *sdk.AppCtx, map[string]any) (any, error)) sdk.Tool {
		return sdk.Tool{Name: name, Description: description, InputSchema: schema, HandlerCtx: func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
			if _, _, err := testBrowserOwner(ctx, app); err != nil {
				return nil, err
			}
			return handler(ctx, app, args)
		}}
	}
	return []sdk.Tool{
		wrap("browser_test_capabilities", "Read the versioned local browser testing contract.", func(context.Context, *sdk.AppCtx, map[string]any) (any, error) {
			return map[string]any{"protocol": testbrowser.Protocol, "backend": "local", "available": testbrowser.Available(), "fresh_context": true, "runtime_network_boundary": true,
				"network_schemes": []string{"http"}, "recording": false, "screenshots": true, "concurrency": 1,
				"actions": []string{"navigate", "reload", "observe", "click", "fill", "select", "check", "upload"}}, nil
		}),
		wrap("browser_test_session", "Open, inspect or close an ephemeral, runtime-restricted headless Chromium session.", a.browserTestSession),
		wrap("browser_test_step", "Execute one deterministic browser action or return structured element observations.", a.browserTestStep),
		wrap("browser_test_capture", "Capture a masked failure screenshot for immediate storage by Tests.", a.browserTestCapture),
	}
}
func decodeBrowserArgs(args map[string]any, out any) error {
	raw, err := json.Marshal(args)
	if err != nil || len(raw) > 256<<10 {
		return errors.New("browser test arguments exceed the size limit")
	}
	return json.Unmarshal(raw, out)
}
func (a *App) browserTestSession(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	var in struct {
		ID      string              `json:"id"`
		Action  string              `json:"action"`
		Options testbrowser.Options `json:"options"`
		TTL     int                 `json:"ttl_seconds"`
	}
	if err := decodeBrowserArgs(args, &in); err != nil {
		return nil, err
	}
	if !testSessionID.MatchString(in.ID) {
		return nil, errors.New("a reserved bt_ session ID is required")
	}
	owner, project, err := testBrowserOwner(ctx, app)
	if err != nil {
		return nil, err
	}
	if in.Action == "open" {
		if in.TTL < 10 || in.TTL > 300 {
			return nil, errors.New("test TTL must be 10–300 seconds")
		}
		if err = in.Options.Policy.Validate(); err != nil {
			return nil, err
		}
		root := testProfileRoot()
		if err = os.MkdirAll(root, 0700); err != nil {
			return nil, errors.New("test profile directory is unavailable")
		}
		profile, err := os.MkdirTemp(root, "profile-")
		if err != nil {
			return nil, err
		}
		in.Options.Profile = profile
		expires := time.Now().Add(time.Duration(in.TTL) * time.Second)
		// The partial unique index reserves one test slot across projects and calls.
		_, err = app.AppDB().Exec(`INSERT INTO computer_test_sessions(id,project_id,owner_install_id,state,profile,expires_at,created_at)
   VALUES(?,?,?,'starting',?,?,?)`, in.ID, project, owner, profile, expires.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			_ = os.RemoveAll(profile)
			var activeID string
			if e := app.AppDB().QueryRow(`SELECT id FROM computer_test_sessions WHERE state IN ('starting','active') LIMIT 1`).Scan(&activeID); e == nil && activeID != in.ID {
				return map[string]any{"id": in.ID, "state": "busy"}, nil
			}
			return nil, errors.New("browser test session could not be reserved")
		}
		browser, err := testbrowser.New(ctx, in.Options)
		if err != nil {
			_, _ = app.AppDB().Exec(`UPDATE computer_test_sessions SET state='closed' WHERE id=?`, in.ID)
			_ = os.RemoveAll(profile)
			return nil, err
		}
		_, err = app.AppDB().Exec(`UPDATE computer_test_sessions SET state='active',pid=?,engine=? WHERE id=? AND state='starting'`, browser.PID(), browser.Engine(), in.ID)
		if err != nil {
			browser.Close()
			_, _ = app.AppDB().Exec(`UPDATE computer_test_sessions SET state='closed' WHERE id=?`, in.ID)
			_ = os.RemoveAll(profile)
			return nil, err
		}
		lease := &testBrowserLease{browser: browser, project: project, owner: owner, profile: profile}
		a.browserTests.mu.Lock()
		a.browserTests.sessions[in.ID] = lease
		lease.timer = time.AfterFunc(time.Until(expires), func() { _, _ = a.closeBrowserTest(app, in.ID, owner, project) })
		a.browserTests.mu.Unlock()
		if ctx.Err() != nil {
			_, _ = a.closeBrowserTest(app, in.ID, owner, project)
			return nil, ctx.Err()
		}
		return map[string]any{"id": in.ID, "state": "active", "protocol": testbrowser.Protocol, "engine": browser.Engine(),
			"width": in.Options.Width, "height": in.Options.Height, "locale": in.Options.Locale, "timezone": in.Options.Timezone}, nil
	}
	if in.Action == "close" {
		return a.closeBrowserTest(app, in.ID, owner, project)
	}
	if in.Action != "status" {
		return nil, errors.New("browser test session action must be open, status or close")
	}
	lease, err := a.getBrowserTest(app, in.ID, owner, project)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": in.ID, "state": "active", "diagnostics": lease.browser.Diagnostics()}, nil
}
func (a *App) getBrowserTest(app *sdk.AppCtx, id string, owner int64, project string) (*testBrowserLease, error) {
	a.browserTests.mu.Lock()
	lease := a.browserTests.sessions[id]
	a.browserTests.mu.Unlock()
	if lease == nil || lease.owner != owner || lease.project != project {
		return nil, errors.New("browser test session is unavailable")
	}
	return lease, nil
}
func (a *App) closeBrowserTest(app *sdk.AppCtx, id string, owner int64, project string) (any, error) {
	a.browserTests.mu.Lock()
	lease := a.browserTests.sessions[id]
	if lease != nil && (lease.owner != owner || lease.project != project) {
		a.browserTests.mu.Unlock()
		return nil, errors.New("browser test session is unavailable")
	}
	if lease != nil {
		delete(a.browserTests.sessions, id)
		lease.timer.Stop()
	}
	a.browserTests.mu.Unlock()
	if lease == nil {
		var state, raw string
		if err := app.AppDB().QueryRow(`SELECT state,diagnostics FROM computer_test_sessions WHERE id=? AND project_id=? AND owner_install_id=?`, id, project, owner).Scan(&state, &raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return map[string]any{"id": id, "state": "closed", "diagnostics": testbrowser.Diagnostics{}}, nil
			}
			return nil, err
		}
		if state == "starting" || state == "active" {
			return nil, errors.New("browser startup or cleanup is pending")
		}
		var diagnostics testbrowser.Diagnostics
		if err := json.Unmarshal([]byte(raw), &diagnostics); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "state": state, "diagnostics": diagnostics}, nil
	}
	diagnostics := lease.browser.Diagnostics()
	lease.browser.Close()
	_ = os.RemoveAll(lease.profile)
	raw, _ := json.Marshal(diagnostics)
	if _, err := app.AppDB().Exec(`UPDATE computer_test_sessions SET state='closed',diagnostics=? WHERE id=? AND project_id=? AND owner_install_id=?`, string(raw), id, project, owner); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "state": "closed", "diagnostics": diagnostics}, nil
}
func (a *App) browserTestStep(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	var in struct {
		ID      string              `json:"id"`
		Command testbrowser.Command `json:"command"`
	}
	if err := decodeBrowserArgs(args, &in); err != nil {
		return nil, err
	}
	owner, project, err := testBrowserOwner(ctx, app)
	if err != nil {
		return nil, err
	}
	lease, err := a.getBrowserTest(app, in.ID, owner, project)
	if err != nil {
		return nil, err
	}
	result, err := lease.browser.Do(ctx, in.Command)
	if err != nil {
		result.Error = err.Error()
		return result, nil // action_dispatched distinguishes uncertain writes from preflight errors
	}
	return result, nil
}
func (a *App) browserTestCapture(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := args["id"].(string)
	owner, project, err := testBrowserOwner(ctx, app)
	if err != nil {
		return nil, err
	}
	lease, err := a.getBrowserTest(app, id, owner, project)
	if err != nil {
		return nil, err
	}
	artifact, err := lease.browser.Screenshot(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifact": artifact}, nil
}
func testProfileRoot() string {
	if db := os.Getenv("DB_PATH"); db != "" && db != ":memory:" {
		return filepath.Join(filepath.Dir(db), "browser-test-profiles")
	}
	return filepath.Join(os.TempDir(), "apteva-browser-tests", strconv.Itoa(os.Getuid()))
}
func (a *App) recoverBrowserTests(app *sdk.AppCtx) error {
	a.browserTests = &testBrowserManager{sessions: map[string]*testBrowserLease{}}
	rows, err := app.AppDB().Query(`SELECT id,profile FROM computer_test_sessions WHERE state IN ('starting','active')`)
	if err != nil {
		return err
	}
	type orphan struct{ id, profile string }
	var items []orphan
	for rows.Next() {
		var item orphan
		if err = rows.Scan(&item.id, &item.profile); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		if err = stopOrphanBrowser(item.profile); err != nil {
			return err
		}
		if _, err = app.AppDB().Exec(`UPDATE computer_test_sessions SET state='interrupted' WHERE id=?`, item.id); err != nil {
			return err
		}
	}
	return nil
}
func stopOrphanBrowser(profile string) error {
	root, err := filepath.Abs(testProfileRoot())
	if err != nil {
		return err
	}
	path, err := filepath.Abs(profile)
	if err != nil || filepath.Dir(path) != root || !strings.HasPrefix(filepath.Base(path), "profile-") {
		return errors.New("invalid orphan browser profile")
	}
	// Match the unique profile argument before signaling any process.
	processes, err := exec.Command("ps", "-axo", "pid=,args=").Output()
	if err != nil {
		return errors.New("cannot inspect orphan browser processes")
	}
	marker := "--user-data-dir=" + path
	for _, line := range strings.Split(string(processes), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, arg := range fields[1:] {
			if arg == marker {
				pid, err := strconv.Atoi(fields[0])
				if err != nil || pid < 2 {
					return errors.New("invalid orphan browser PID")
				}
				process, err := os.FindProcess(pid)
				if err == nil {
					_ = process.Kill()
				}
			}
		}
	}
	return os.RemoveAll(path)
}
func (a *App) closeAllBrowserTests(app *sdk.AppCtx) {
	if a.browserTests == nil {
		return
	}
	a.browserTests.mu.Lock()
	ids := make(map[string]*testBrowserLease, len(a.browserTests.sessions))
	for id, lease := range a.browserTests.sessions {
		ids[id] = lease
	}
	a.browserTests.mu.Unlock()
	for id, lease := range ids {
		if _, err := a.closeBrowserTest(app, id, lease.owner, lease.project); err != nil {
			app.Logger().Warn("browser test cleanup failed", "error", fmt.Sprint(err))
		}
	}
}
