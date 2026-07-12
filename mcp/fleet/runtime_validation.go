package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const cloneQuarantineMinAptevaVersion = "0.25.4"

func supportsCloneRuntimeRecovery(version string) bool {
	parse := func(raw string) ([3]int, bool) {
		var out [3]int
		parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(raw), "v"), ".")
		if len(parts) < 3 {
			return out, false
		}
		for i := 0; i < 3; i++ {
			digits := parts[i]
			if cut := strings.IndexAny(digits, "-+"); cut >= 0 {
				digits = digits[:cut]
			}
			n, err := strconv.Atoi(digits)
			if err != nil || n < 0 {
				return out, false
			}
			out[i] = n
		}
		return out, true
	}
	got, ok := parse(version)
	if !ok {
		return false
	}
	want, _ := parse(cloneQuarantineMinAptevaVersion)
	for i := range got {
		if got[i] != want[i] {
			return got[i] > want[i]
		}
	}
	return true
}

type tenantAppRuntime struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
	BinPath string `json:"bin_path"`
	Port    int    `json:"port"`
}

const remoteRuntimeInventoryScript = `
import json, sqlite3, sys
db = sqlite3.connect(sys.argv[1])
rows = db.execute("""SELECT i.id, a.name, COALESCE(i.version,''), i.status,
COALESCE(i.local_bin_path,''), COALESCE(i.local_port,0)
FROM app_installs i JOIN apps a ON a.id=i.app_id
WHERE i.status='running' AND COALESCE(i.local_bin_path,'') != '' ORDER BY i.id""").fetchall()
print(json.dumps([{"id":r[0],"name":r[1],"version":r[2],"status":r[3],"bin_path":r[4],"port":r[5]} for r in rows]))
`

func (a *App) tenantAppRuntimes(ctx *sdk.AppCtx, host fleetHost, dir string) ([]tenantAppRuntime, error) {
	dbPath := filepath.Join(dir, "apteva.db")
	if host.IsLocal() {
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		rows, err := db.Query(`SELECT i.id, a.name, COALESCE(i.version,''), i.status,
			COALESCE(i.local_bin_path,''), COALESCE(i.local_port,0)
			FROM app_installs i JOIN apps a ON a.id=i.app_id
			WHERE i.status='running' AND COALESCE(i.local_bin_path,'') != '' ORDER BY i.id`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []tenantAppRuntime
		for rows.Next() {
			var r tenantAppRuntime
			if err := rows.Scan(&r.ID, &r.Name, &r.Version, &r.Status, &r.BinPath, &r.Port); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}
	out, code, err := instanceRunCommand(ctx, host.InstanceID,
		fmt.Sprintf("python3 -c %s %s", sh(remoteRuntimeInventoryScript), sh(dbPath)), 30)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("read hosted app runtimes: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	var runtimes []tenantAppRuntime
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &runtimes); err != nil {
		return nil, fmt.Errorf("decode hosted app runtimes: %w", err)
	}
	return runtimes, nil
}

func (a *App) waitForQuarantinedRuntimes(ctx *sdk.AppCtx, host fleetHost, dir string, required []tenantAppRuntime, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		runtimes, err := a.tenantAppRuntimes(ctx, host, dir)
		if err == nil {
			byID := make(map[int64]tenantAppRuntime, len(runtimes))
			for _, runtime := range runtimes {
				byID[runtime.ID] = runtime
			}
			var missing []string
			for _, want := range required {
				got, ok := byID[want.ID]
				if !ok || got.Name != want.Name || got.Version != want.Version {
					missing = append(missing, fmt.Sprintf("%s@%s", want.Name, want.Version))
				} else if !runtimePathWithinTenant(dir, got.BinPath) {
					missing = append(missing, fmt.Sprintf("%s@%s has non-target runtime path", want.Name, want.Version))
				}
			}
			binaryMissing, checkErr := a.missingRuntimeBinaries(ctx, host, runtimes)
			missing = append(missing, binaryMissing...)
			if checkErr == nil && len(missing) == 0 {
				return nil
			}
			if checkErr != nil {
				err = checkErr
			} else {
				err = fmt.Errorf("runtimes not ready: %s", strings.Join(missing, ", "))
			}
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(2 * time.Second)
	}
}

func runtimePathWithinTenant(tenantDir, binPath string) bool {
	appsRoot := filepath.Clean(filepath.Join(tenantDir, "apps"))
	cleanPath := filepath.Clean(binPath)
	rel, err := filepath.Rel(appsRoot, cleanPath)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (a *App) missingRuntimeBinaries(ctx *sdk.AppCtx, host fleetHost, runtimes []tenantAppRuntime) ([]string, error) {
	if host.IsLocal() {
		var missing []string
		for _, r := range runtimes {
			if info, err := os.Stat(r.BinPath); err != nil || info.IsDir() {
				missing = append(missing, fmt.Sprintf("%s@%s", r.Name, r.Version))
			}
		}
		return missing, nil
	}
	rowsJSON, _ := json.Marshal(runtimes)
	script := `import json, os, sys
rows=json.loads(sys.argv[1])
print(json.dumps([r["name"]+"@"+r["version"] for r in rows if not os.path.isfile(r["bin_path"])]))`
	out, code, err := instanceRunCommand(ctx, host.InstanceID,
		fmt.Sprintf("python3 -c %s %s", sh(script), sh(string(rowsJSON))), 30)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("check hosted runtime binaries: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	var missing []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &missing); err != nil {
		return nil, err
	}
	return missing, nil
}

func (a *App) requireTargetAppsHealthy(ctx *sdk.AppCtx, host fleetHost, dir string, required []tenantAppRuntime) error {
	if len(required) == 0 {
		return nil
	}
	current, err := a.tenantAppRuntimes(ctx, host, dir)
	if err != nil {
		return err
	}
	byID := make(map[int64]tenantAppRuntime, len(current))
	for _, r := range current {
		byID[r.ID] = r
	}
	var failures []string
	for _, want := range required {
		got, ok := byID[want.ID]
		if !ok || got.Name != want.Name || got.Version != want.Version {
			failures = append(failures, fmt.Sprintf("%s@%s missing", want.Name, want.Version))
			continue
		}
		if !runtimePathWithinTenant(dir, got.BinPath) {
			failures = append(failures, got.Name+" has non-target runtime path")
			continue
		}
		if got.Port <= 0 {
			failures = append(failures, got.Name+" has no runtime port")
			continue
		}
		if err := a.probeAppRuntime(ctx, host, got.Port); err != nil {
			failures = append(failures, fmt.Sprintf("%s unhealthy: %v", got.Name, err))
		}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (a *App) waitForTargetAppsHealthy(ctx *sdk.AppCtx, host fleetHost, dir string, required []tenantAppRuntime, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		lastErr = a.requireTargetAppsHealthy(ctx, host, dir, required)
		if lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(2 * time.Second)
	}
}

func (a *App) probeAppRuntime(ctx *sdk.AppCtx, host fleetHost, port int) error {
	if host.IsLocal() {
		probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health", port), nil)
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	}
	script := fmt.Sprintf(`import sys, urllib.request
r=urllib.request.urlopen("http://127.0.0.1:%d/health", timeout=3)
sys.exit(0 if r.status == 200 else 1)`, port)
	out, code, err := instanceRunCommand(ctx, host.InstanceID, fmt.Sprintf("python3 -c %s", sh(script)), 10)
	if err != nil || code != 0 {
		return fmt.Errorf("remote health probe: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	return nil
}
