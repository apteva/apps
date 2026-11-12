package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/apteva/apps/mcp/computer/internal/browser/testbrowser"
)

// Exercise the public browser_test_step handler, including JSON command
// decoding, its selection adapter, and the shared selector in real Chromium.
func TestBrowserTestStepSelectEmptyValue(t *testing.T) {
	if os.Getenv("RUN_BROWSER_TESTS") != "1" {
		t.Skip("set RUN_BROWSER_TESTS=1 for the real Computer adapter")
	}
	prefix := "/api/environment-app-public/rt_selection/api/apps/ui/_install/1/ui"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<body>
<select aria-label="Ringtone" id="ringtone"><option value="other">Other</option><option value="">Use default</option><option value="bell" selected>Bell</option></select>
<select aria-label="No empty option"><option value="bell" selected>Bell</option></select>
<select aria-label="Disabled option"><option value="" disabled>Use default</option><option value="bell" selected>Bell</option></select>
<select aria-label="Duplicate value"><option value="">First</option><option value="">Second</option><option value="bell" selected>Bell</option></select>
<select aria-label="Reconciled" onchange="setTimeout(()=>this.value='bell',0)"><option value="">Use default</option><option value="bell" selected>Bell</option></select>
<select aria-label="Duplicate locator"><option value="">Default</option><option value="bell" selected>Bell</option></select>
<select aria-label="Duplicate locator"><option value="">Default</option><option value="bell" selected>Bell</option></select>
<input data-testid="text" value="initial"><input data-testid="password" type="password">
<output data-testid="events">0/0</output>
<script>let inputs=0,changes=0;const el=document.querySelector('#ringtone');function show(){document.querySelector('[data-testid=events]').textContent=inputs+'/'+changes}el.addEventListener('input',()=>{inputs++;show()});el.addEventListener('change',()=>{changes++;show()});</script>
</body>`)
	}))
	defer server.Close()
	appCtx := tk.NewAppCtx(t, "apteva.yaml").WithProject("selection")
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "computer.db"))
	app := &App{}
	if err := app.recoverBrowserTests(appCtx); err != nil {
		t.Fatal(err)
	}
	callCtx := sdk.WithCaller(context.Background(), &sdk.Caller{AppName: "tests", AppInstallID: 1, ProjectID: "selection"})
	tools := map[string]sdk.Tool{}
	for _, tool := range app.browserTestTools() {
		tools[tool.Name] = tool
	}
	id := "bt_0123456789abcdef0123456789abcdef"
	_, err := tools["browser_test_session"].HandlerCtx(callCtx, appCtx, map[string]any{"id": id, "action": "open", "ttl_seconds": 120, "options": map[string]any{"width": 1000, "height": 700, "policy": map[string]any{"runtime_id": "rt_selection", "targets": map[string]string{"frontend": server.URL + prefix}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := tools["browser_test_session"].HandlerCtx(callCtx, appCtx, map[string]any{"id": id, "action": "close"}); err != nil {
			t.Error(err)
		}
	}()
	step := func(command map[string]any) (testbrowser.Result, error) {
		out, err := tools["browser_test_step"].HandlerCtx(callCtx, appCtx, map[string]any{"id": id, "command": command})
		if err != nil {
			return testbrowser.Result{}, err
		}
		return out.(testbrowser.Result), nil
	}
	must := func(command map[string]any) testbrowser.Result {
		t.Helper()
		result, err := step(command)
		if err != nil || result.Error != "" {
			t.Fatalf("command %v failed: %v %s", command["action"], err, result.Error)
		}
		return result
	}
	locator := func(name string) map[string]any { return map[string]any{"role": "combobox", "name": name} }
	observe := func(name string) testbrowser.Result {
		return must(map[string]any{"action": "observe", "locator": locator(name)})
	}
	must(map[string]any{"action": "navigate", "target": "frontend", "path": "/"})
	if value := observe("Ringtone").Observation.Value; value == nil || *value != "bell" {
		t.Fatal("fixture did not start on Bell")
	}
	must(map[string]any{"action": "select", "locator": locator("Ringtone"), "value": ""})
	result := observe("Ringtone")
	if result.Observation.Value == nil || *result.Observation.Value != "" {
		t.Fatalf("empty option not selected: %+v", result)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), `"value":""`) {
		t.Fatalf("empty observation omitted: %s", encoded)
	}
	events := must(map[string]any{"action": "observe", "locator": map[string]any{"test_id": "events"}})
	if events.Observation.Text != "1/1" {
		t.Fatalf("missing input/change events: %+v", events)
	}
	must(map[string]any{"action": "select", "locator": locator("Ringtone"), "value": "bell"})
	for _, name := range []string{"No empty option", "Disabled option", "Duplicate value", "Duplicate locator", "Reconciled"} {
		t.Run(name, func(t *testing.T) {
			result, err := step(map[string]any{"action": "select", "locator": locator(name), "value": ""})
			if err == nil && result.Error == "" {
				t.Fatal("invalid/reverted selection reported success")
			}
			if name != "Duplicate locator" {
				if value := observe(name).Observation.Value; value == nil || *value != "bell" {
					t.Fatal("failed selection silently picked another option", value)
				}
			}
		})
	}
	for _, field := range []map[string]any{{}, {"value": nil}, {"value": 1}, {"value": false}, {"value": "Bell"}} {
		t.Run(fmt.Sprint(field), func(t *testing.T) {
			command := map[string]any{"action": "select", "locator": locator("Ringtone")}
			for key, value := range field {
				command[key] = value
			}
			result, err := step(command)
			if err == nil && result.Error == "" {
				t.Fatal("invalid selection accepted")
			}
			if value, exists := field["value"]; (!exists || value == nil) && result.Dispatched {
				t.Fatal("missing/null selection dispatched")
			}
			if value := observe("Ringtone").Observation.Value; value == nil || *value != "bell" {
				t.Fatal("failed selection changed current value")
			}
		})
	}
	// Both explicit empty text and the legacy omitted fill value still clear.
	for _, field := range []map[string]any{{"value": ""}, {}} {
		must(map[string]any{"action": "fill", "locator": map[string]any{"test_id": "text"}, "value": "filled"})
		command := map[string]any{"action": "fill", "locator": map[string]any{"test_id": "text"}}
		for key, value := range field {
			command[key] = value
		}
		must(command)
		value := must(map[string]any{"action": "observe", "locator": map[string]any{"test_id": "text"}}).Observation.Value
		if value == nil || *value != "" {
			t.Fatal("text field did not clear")
		}
	}
	must(map[string]any{"action": "fill", "locator": map[string]any{"test_id": "password"}, "value": "synthetic-secret", "sensitive": true})
	if value := must(map[string]any{"action": "observe", "locator": map[string]any{"test_id": "password"}}).Observation.Value; value != nil {
		t.Fatal("password value leaked")
	}
}
