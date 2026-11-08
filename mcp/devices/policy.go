package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

var ErrForbidden = errors.New("policy controller authorization required")

func (a *App) baseLimit(key string, def int) int {
	return configInt(a.ctx, key, def, limitMinimum(key), limitMaximum(key))
}
func (a *App) loadPolicy() error {
	var raw string
	var rev int64
	err := a.ctx.AppDB().QueryRow(`SELECT revision,limits_json FROM device_policy WHERE project_id=?`, a.projectID).Scan(&rev, &raw)
	values := map[string]int{}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &values); err != nil {
			return err
		}
		if err = a.validatePolicy(values); err != nil {
			return fmt.Errorf("stored policy: %w", err)
		}
	}
	a.policyMu.Lock()
	a.policy = values
	a.policyRevision = rev
	a.policyMu.Unlock()
	return nil
}
func (a *App) activeLimits() map[string]int {
	limits := a.baseLimits()
	a.policyMu.RLock()
	defer a.policyMu.RUnlock()
	for key, value := range a.policy {
		limits[key] = value
	}
	return limits
}
func (a *App) validatePolicy(values map[string]int) error {
	allowed := a.baseLimits()
	for key, value := range values {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown policy limit %q", key)
		}
		minimum := limitMinimum(key)
		if value < minimum || value > limitMaximum(key) {
			return fmt.Errorf("%s must be an integer from %d to %d", key, minimum, limitMaximum(key))
		}
	}
	return nil
}
func (a *App) policyWriter(call context.Context) error {
	if err := a.authorizeCaller(call); err != nil {
		return err
	}
	caller := sdk.CallerFrom(call)
	controller := a.ctx.Config().Get("policy_controller_app")
	if controller == "" {
		controller = "saas"
	}
	if caller.AppInstallID <= 0 || caller.AppName != controller {
		return ErrForbidden
	}
	return nil
}
func (a *App) httpPolicyWriter(r *http.Request) error {
	// The platform scrubs and owns this header behind the SDK installation-token
	// gate. Explicitly configured operators are control-plane principals only.
	id := r.Header.Get("X-Apteva-Operator-ID")
	if id == "" {
		return ErrForbidden
	}
	for _, allowed := range strings.Split(a.ctx.Config().Get("policy_admin_user_ids"), ",") {
		if value := strings.TrimSpace(allowed); value != "" && value == id {
			return nil
		}
	}
	return ErrForbidden
}
func (a *App) policySnapshot() (map[string]any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	return a.policySnapshotLocked()
}
func (a *App) policySnapshotLocked() (map[string]any, error) {
	limits := a.activeLimits()
	a.policyMu.RLock()
	revision := a.policyRevision
	a.policyMu.RUnlock()
	resources, err := a.resourceSnapshot(a.ctx.AppDB())
	if err != nil {
		return nil, err
	}
	return map[string]any{"scope_id": a.projectID, "revision": revision, "limits": limits, "resources": resources, "over_limit": overLimits(resources, limits)}, nil
}
func policyInput(args map[string]any) (int64, string, map[string]int, error) {
	request := str(args, "request_id")
	if request == "" || len(request) > 128 {
		return 0, "", nil, errors.New("request_id must contain 1 to 128 characters")
	}
	number, ok := args["expected_revision"]
	if !ok {
		return 0, "", nil, errors.New("expected_revision required")
	}
	raw, _ := json.Marshal(number)
	revision, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || revision < 0 {
		return 0, "", nil, errors.New("expected_revision must be a nonnegative integer")
	}
	patch, ok := args["limits"].(map[string]any)
	if !ok || len(patch) == 0 {
		return 0, "", nil, errors.New("limits must be a nonempty object")
	}
	values := map[string]int{}
	for key, value := range patch {
		raw, _ := json.Marshal(value)
		n, e := strconv.ParseInt(string(raw), 10, 32)
		if e != nil {
			return 0, "", nil, fmt.Errorf("%s must be an integer", key)
		}
		values[key] = int(n)
	}
	return revision, request, values, nil
}
func (a *App) applyPolicy(args map[string]any) (map[string]any, error) {
	revision, request, patch, err := policyInput(args)
	if err != nil {
		return nil, err
	}
	if err = a.validatePolicy(patch); err != nil {
		return nil, err
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	hash := fingerprint(map[string]any{"expected_revision": revision, "limits": patch})
	var oldHash, snapshot string
	err = a.ctx.AppDB().QueryRow(`SELECT fingerprint,snapshot_json FROM device_policy_updates WHERE project_id=? AND request_id=?`, a.projectID, request).Scan(&oldHash, &snapshot)
	if err == nil {
		if oldHash != hash {
			return nil, ErrConflict
		}
		result := jsonObject(snapshot)
		result["replayed"] = true
		a.policyMu.RLock()
		result["current_revision"] = a.policyRevision
		a.policyMu.RUnlock()
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	a.policyMu.RLock()
	current := a.policyRevision
	merged := map[string]int{}
	for key, value := range a.policy {
		merged[key] = value
	}
	a.policyMu.RUnlock()
	if revision != current {
		return nil, fmt.Errorf("%w: current policy revision is %d", ErrConflict, current)
	}
	for key, value := range patch {
		merged[key] = value
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	encoded, _ := json.Marshal(merged)
	next := current + 1
	at := nowText()
	_, err = tx.Exec(`INSERT INTO device_policy(project_id,revision,limits_json,updated_at)VALUES(?,?,?,?) ON CONFLICT(project_id)DO UPDATE SET revision=excluded.revision,limits_json=excluded.limits_json,updated_at=excluded.updated_at`, a.projectID, next, string(encoded), at)
	if err != nil {
		return nil, err
	}
	resources, err := a.resourceSnapshot(tx)
	if err != nil {
		return nil, err
	}
	effective := a.baseLimits()
	for key, value := range merged {
		effective[key] = value
	}
	result := map[string]any{"scope_id": a.projectID, "revision": next, "limits": effective, "resources": resources, "over_limit": overLimits(resources, effective), "updated_at": at}
	if err = a.event(tx, "", "devices.policy.updated", map[string]any{"revision": next, "limits": patch, "request_id": request}); err != nil {
		return nil, err
	}
	resultJSON, _ := json.Marshal(result)
	_, err = tx.Exec(`INSERT INTO device_policy_updates VALUES(?,?,?,?,?,?)`, a.projectID, request, hash, next, string(resultJSON), at)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	a.policyMu.Lock()
	a.policy = merged
	a.policyRevision = next
	a.policyMu.Unlock()
	a.wakeOutbox()
	return result, nil
}
func overLimits(resources map[string]int64, limits map[string]int) []string {
	mapping := map[string]string{"transport_receipts": "max_transport_receipts", "quarantined_messages": "max_ingest_rejections", "max_device_state_keys": "max_state_keys_per_device", "max_device_state_bytes": "max_state_bytes_per_device", "max_device_history_records": "telemetry_max_rows_per_device", "max_device_history_bytes": "max_history_bytes_per_device", "max_device_pending_commands": "max_pending_commands_per_device", "max_device_inflight_commands": "max_concurrent_commands_per_device", "max_device_http_credentials": "max_http_credentials_per_device", "devices": "max_devices", "pending_commands": "max_pending_commands", "inflight_commands": "max_concurrent_commands", "pending_events": "max_pending_events"}
	out := []string{}
	for metric, key := range mapping {
		value := resources[metric]
		if metric == "devices" {
			value += resources["pending_provisions"]
		}
		if value > int64(limits[key]) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}
func (a *App) handlePolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result, err := a.policySnapshot()
		respond(w, result, err)
		return
	}
	if r.Method != http.MethodPut {
		methodNotAllowed(w)
		return
	}
	if err := a.httpPolicyWriter(r); err != nil {
		writeError(w, 403, err)
		return
	}
	var args map[string]any
	if !decodeBody(w, r, &args) {
		return
	}
	result, err := a.applyPolicy(args)
	respond(w, result, err)
}
