package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/apteva/apps/mcp/instances/internal/monitor"
)

func TestMonitoringRESTAndMCPShareSnapshot(t *testing.T) {
	t.Setenv("APTEVA_DATA_DIR", t.TempDir())
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old; stopMonitoring(ctx) }()
	if err := ensureLocalInstance(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	m, err := monitoringFor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	batch := monitor.Batch{Version: monitor.CollectorVersion, Latest: &Metrics{Timestamp: now.UTC().Format(time.RFC3339Nano), CPU: CPUMetrics{TotalPct: 42}}}
	if err := m.store.Ingest(0, batch); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/instances/0/metrics", nil)
	w := httptest.NewRecorder()
	(&App{}).handleInstanceItem(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response map[string]any
	json.Unmarshal(w.Body.Bytes(), &response)
	tool, err := (&App{}).toolMetrics(ctx, map[string]any{"id": 0})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(tool)
	var toolResponse map[string]any
	json.Unmarshal(encoded, &toolResponse)
	if response["metrics"].(map[string]any)["cpu"].(map[string]any)["total_pct"] != float64(42) || toolResponse["metrics"].(map[string]any)["cpu"].(map[string]any)["total_pct"] != float64(42) {
		t.Fatal("REST/MCP mismatch")
	}
	req = httptest.NewRequest(http.MethodPost, "/api/instances/0/monitoring", strings.NewReader(`{"enabled":false}`))
	w = httptest.NewRecorder()
	(&App{}).handleInstanceItem(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	status, _ := m.store.Status(0)
	if status.Enabled {
		t.Fatal("disable not persisted")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/instances/0/metrics/history?max_points=999999", nil)
	w = httptest.NewRecorder()
	(&App{}).handleInstanceItem(w, req)
	if w.Code != 400 {
		t.Fatal("history limit bypass")
	}
}
func TestCollectorServiceScripts(t *testing.T) {
	linux, err := collectorServiceScript("linux", "/home/user space", "user")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(linux, `ExecStart="/home/user space/.local/share/apteva-instances-monitor/collector"`) || !strings.Contains(linux, "User=user") || !strings.Contains(linux, "enable apteva-instances-monitor.service") {
		t.Fatal("invalid systemd service", linux)
	}
	mac, err := collectorServiceScript("darwin", "/Users/a&b", "user")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mac, "a&amp;b") || !strings.Contains(mac, "launchctl bootstrap system") {
		t.Fatal("invalid launch daemon")
	}
	if _, err := collectorServiceScript("linux", "/home/user\nExecStart=bad", "user"); err == nil {
		t.Fatal("invalid path accepted")
	}
}
func TestRemoteCollectorVersionAvoidsDownload(t *testing.T) {
	previous := monitoringRunSSH
	defer func() { monitoringRunSSH = previous }()
	calls := []string{}
	monitoringRunSSH = func(_ *Instance, cmd string, _ time.Duration) (string, int, error) {
		calls = append(calls, cmd)
		if strings.Contains(cmd, "uname -m") {
			return "Linux\naarch64\n/home/user\nuser\n" + monitor.CollectorVersion + "\n", 0, nil
		}
		return "", 0, nil
	}
	if err := ensureRemoteCollector(t.Context(), &Instance{ID: 7}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.Contains(calls[1], "systemctl start") {
		t.Fatal(calls)
	}
}

func TestCollectorReleaseContract(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Version != monitor.CollectorVersion || manifest.Runtime.Source.Ref != "instances/v"+monitor.CollectorVersion {
		t.Fatal("release/collector/source version drift")
	}
	var sums map[string]string
	if err := json.Unmarshal(collectorChecksums, &sums); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"instances-collector-linux-amd64", "instances-collector-linux-arm64", "instances-collector-darwin-amd64", "instances-collector-darwin-arm64"} {
		if len(sums[name]) != 64 {
			t.Fatalf("missing checksum for %s", name)
		}
	}
}
