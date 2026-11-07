package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type fleetPreflightReport struct {
	Version       string `json:"version"`
	SchemaChanged bool   `json:"schema_changed"`
	RollbackSafe  bool   `json:"rollback_safe"`
	Reported      bool   `json:"-"`
}

// Keep the disposable backup for diagnosis; never copy it over live data.
// Older preflight implementations may omit the report. They can still perform
// forward updates, but cannot authorize an automatic binary rollback.
func (a *App) legacyUpdatePreflight(ctx context.Context, app *sdk.AppCtx, t *Tenant, binary, version string) (fleetPreflightReport, error) {
	var report fleetPreflightReport
	backup := filepath.Join(t.ConfigDir, "fleet-update-preflight", newID(), "apteva.db")
	command := fmt.Sprintf("mkdir -p %s && env APTEVA_HOME=%s DB_PATH=%s DATA_DIR=%s APTEVA_UPDATE_PREFLIGHT=%s %s --preflight >&2 && if [ -f %s ]; then cat %s; else printf '%%s' '{}'; fi",
		sh(filepath.Dir(backup)), sh(t.ConfigDir), sh(filepath.Join(t.ConfigDir, "apteva.db")), sh(t.ConfigDir), sh(backup), sh(binary), sh(backup+".json"), sh(backup+".json"))
	// Local stdout must contain just the report, with diagnostics in a log.
	logPath := filepath.Join(filepath.Dir(backup), "preflight.log")
	command = "(" + command + ") 2>" + sh(logPath)
	// Create the directory before the shell opens the log redirection.
	command = "mkdir -p " + sh(filepath.Dir(backup)) + " && " + command
	out, err := a.tenantCommand(ctx, app, t, command, 3*time.Minute)
	if err != nil {
		return report, fmt.Errorf("database preflight failed (see %s): %w", logPath, err)
	}
	if err := json.Unmarshal(out, &report); err != nil {
		return report, fmt.Errorf("invalid database preflight report: %w", err)
	}
	report.Reported = report.Version != ""
	if report.Reported && report.Version != version {
		return report, fmt.Errorf("preflight reports %s, requested %s", report.Version, version)
	}
	_ = a.store.recordEvent(t.ID, "update_preflight", "tool:tenant_update", map[string]any{"version": version, "backup": backup, "rollback_safe": report.Reported && report.RollbackSafe && !report.SchemaChanged})
	return report, nil
}

func rollbackCompatible(report fleetPreflightReport) bool {
	return report.Reported && report.RollbackSafe && !report.SchemaChanged
}

func (a *App) legacyRollbackAllowed(ctx context.Context, app *sdk.AppCtx, t *Tenant, candidate fleetPreflightReport, previousBinary, previousVersion string) error {
	if !rollbackCompatible(candidate) {
		return errors.New("candidate database compatibility is unconfirmed or migrations changed schema/data; automatic rollback refused")
	}
	// Validate the previous release against the database as it exists NOW,
	// including any migrations performed during the failed startup.
	previous, err := a.legacyUpdatePreflight(ctx, app, t, previousBinary, previousVersion)
	if err != nil {
		return err
	}
	if !rollbackCompatible(previous) {
		return errors.New("previous release cannot safely use the current database; automatic rollback refused")
	}
	if _, present, err := a.hostRequest(ctx, app, t, "JOURNAL", "", nil); present || err != nil {
		return errors.New("a stable host journal exists; legacy rollback refused")
	}
	return nil
}
