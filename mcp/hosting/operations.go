package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Operation kinds.
const (
	OpProvision  = "provision"
	OpSuspend    = "suspend"
	OpResume     = "resume"
	OpAccessKeys = "access_keys"
	OpRestart    = "restart"
	OpRelease    = "release"
	OpReinstall  = "reinstall"
	OpInspect    = "inspect"
	OpClean      = "clean"
)

const (
	leaseDuration = 3 * time.Minute
	pollInterval  = 30 * time.Second
)

// stepTimeouts bounds how long a step may keep reporting "not yet".
var stepTimeouts = map[string]time.Duration{
	"verify_clean": time.Hour,
	"wait_ready":   2 * time.Hour,
	"wait_access":  15 * time.Minute,
	// A phone that stays offline, or that someone else keeps holding.
	"device_check": 15 * time.Minute,
	"device_open":  15 * time.Minute,
	"device_stop":  15 * time.Minute,
	"device_close": 15 * time.Minute,
}

// errPending means the step has not finished yet and should be polled again.
type errPending struct {
	reason string
	until  time.Time
}

func (e *errPending) Error() string { return "pending: " + e.reason }

func pending(reason string) error { return &errPending{reason: reason} }

func pendingUntil(reason string, until time.Time) error {
	return &errPending{reason: reason, until: until}
}

// newOperationID returns a lowercase UUIDv4. The reset scripts accept only
// 36-character hex/dash operation IDs.
func newOperationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func newLeaseToken() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// kickOperation starts driving an operation in the background. Tests swap it
// for a synchronous call.
var kickOperation func(a *App, ctx *sdk.AppCtx, id string)

func init() {
	kickOperation = func(a *App, ctx *sdk.AppCtx, id string) { go a.drive(ctx, id) }
}

// enqueueOperation inserts a run. Runs for the same tenant or machine are
// executed one at a time in creation order.
func enqueueOperation(db querier, kind, tenantID string, machineID, instanceID int64, steps []string, actorName string) (*Operation, error) {
	if len(steps) == 0 {
		return nil, fmt.Errorf("operation %s has no steps", kind)
	}
	op := &Operation{ID: newOperationID(), Kind: kind, TenantID: tenantID, MachineID: machineID, InstanceID: instanceID, Steps: steps, Status: OpQueued, Actor: actorName}
	if _, err := db.Exec(`INSERT INTO hosting_operations (id, kind, tenant_id, machine_id, instance_id, steps_json, status, actor, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?)`,
		op.ID, kind, nullStr(tenantID), nullInt(machineID), nullInt(instanceID), jsonOrEmpty(steps, "[]"), actorName, sqlTime(now()), sqlTime(now())); err != nil {
		return nil, err
	}
	return dbOperationGet(db, op.ID)
}

// claimOperation takes the lease on a due operation. It fails quietly when
// the run is not due, is leased by someone else, or another run for the same
// tenant/machine holds the active slot.
func claimOperation(db *sql.DB, id string) (*Operation, string, error) {
	token := newLeaseToken()
	t := now()
	res, err := db.Exec(`UPDATE hosting_operations
		SET status='running', lease_token=?, lease_until=?, updated_at=?,
		    step_started_at=COALESCE(step_started_at, ?)
		WHERE id=? AND (
		  (status='queued' AND NOT EXISTS (
		     -- Runs for one tenant or machine execute in the order they were requested.
		     SELECT 1 FROM hosting_operations o2
		     WHERE o2.status IN ('queued','running','waiting') AND o2.rowid < hosting_operations.rowid
		       AND (o2.tenant_id = hosting_operations.tenant_id OR o2.machine_id = hosting_operations.machine_id)))
		  OR (status='waiting' AND (not_before IS NULL OR not_before<=?))
		  OR (status='running' AND (lease_until IS NULL OR lease_until<=?)))`,
		token, sqlTime(t.Add(leaseDuration)), sqlTime(t), sqlTime(t), id, sqlTime(t), sqlTime(t))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, "", nil // another run for this tenant/machine is active
		}
		return nil, "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, "", nil
	}
	op, err := dbOperationGet(db, id)
	return op, token, err
}

// drive runs an operation's steps until it finishes, waits, fails, or loses
// its lease.
func (a *App) drive(ctx *sdk.AppCtx, id string) {
	if ctx == nil {
		ctx = globalCtx
	}
	if ctx == nil {
		return
	}
	db := ctx.AppDB()
	op, token, err := claimOperation(db, id)
	if err != nil {
		ctx.Logger().Warn("hosting operation claim failed", "operation_id", id, "err", err)
		return
	}
	if op == nil {
		return
	}
	if op.Step == 0 && len(op.Log) == 0 && op.MachineID > 0 {
		// The machine shows what is happening to it once its run starts.
		if state := machineStateDuring(op.Kind); state != "" {
			_, _ = db.Exec(`UPDATE hosting_machines SET state=?, state_reason=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND (state!='quarantined' OR ?='clean')`, state, op.Kind+" "+op.ID, op.MachineID, op.Kind)
		}
	}
	for {
		if op.Step >= len(op.Steps) {
			a.finishOperation(ctx, op, token)
			return
		}
		step := op.Steps[op.Step]
		stop := keepLease(db, op.ID, token)
		runErr := a.runStep(ctx, op, step)
		stop()
		var p *errPending
		switch {
		case runErr == nil:
			if !advanceOperation(db, op, token, step) {
				return // fenced
			}
			op, err = dbOperationGet(db, op.ID)
			if err != nil || op == nil || op.Status != OpRunning || op.LeaseToken != token {
				return
			}
		case errors.As(runErr, &p):
			if started, ok := parseSQLTime(op.StepStartedAt); ok {
				if limit, has := stepTimeouts[step]; has && now().Sub(started) > limit {
					a.failOperation(ctx, op, token, step, fmt.Errorf("%s did not finish within %s (%s)", step, limit, p.reason))
					return
				}
			}
			next := now().Add(pollInterval)
			if !p.until.IsZero() && p.until.After(next) {
				next = p.until
			}
			waitOperation(db, op, token, step, p.reason, next)
			return
		default:
			a.failOperation(ctx, op, token, step, runErr)
			return
		}
	}
}

// leaseRenewEvery is how often a running step extends its lease, so a step
// slower than leaseDuration (creating an instance, pulling an image, erasing
// a Mac) is never claimed and run a second time by the worker.
var leaseRenewEvery = leaseDuration / 3

func keepLease(db *sql.DB, id, token string) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(leaseRenewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_, _ = db.Exec(`UPDATE hosting_operations SET lease_until=? WHERE id=? AND lease_token=? AND status='running'`,
					sqlTime(now().Add(leaseDuration)), id, token)
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
	}
}

func appendLog(op *Operation, step, result, detail string) string {
	if len(detail) > 600 {
		detail = detail[:600] + "…"
	}
	logs := append(op.Log, OperationLog{At: sqlTime(now()), Step: step, Result: result, Detail: detail})
	if len(logs) > 200 {
		logs = logs[len(logs)-200:]
	}
	op.Log = logs
	return jsonOrEmpty(logs, "[]")
}

func advanceOperation(db *sql.DB, op *Operation, token, step string) bool {
	t := now()
	res, err := db.Exec(`UPDATE hosting_operations SET step=step+1, step_started_at=?, lease_until=?, error='', log_json=?, updated_at=?
		WHERE id=? AND lease_token=? AND status='running'`,
		sqlTime(t), sqlTime(t.Add(leaseDuration)), appendLog(op, step, "ok", ""), sqlTime(t), op.ID, token)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

func waitOperation(db *sql.DB, op *Operation, token, step, reason string, next time.Time) {
	logJSON := jsonOrEmpty(op.Log, "[]")
	// Log a waiting step once, not on every poll.
	if len(op.Log) == 0 || op.Log[len(op.Log)-1].Step != step || op.Log[len(op.Log)-1].Result != "waiting" {
		logJSON = appendLog(op, step, "waiting", reason)
	}
	_, _ = db.Exec(`UPDATE hosting_operations SET status='waiting', not_before=?, lease_token='', lease_until=NULL, log_json=?, error=?, updated_at=?
		WHERE id=? AND lease_token=? AND status='running'`,
		sqlTime(next), logJSON, "", sqlTime(now()), op.ID, token)
}

// failOperation stops the run for operator review. A pool machine involved
// in a failed run is quarantined; retrying needs hosting_operation_retry with
// reviewed=true.
func (a *App) failOperation(ctx *sdk.AppCtx, op *Operation, token, step string, cause error) {
	db := ctx.AppDB()
	msg := fmt.Sprintf("%s failed at %s: %v", op.Kind, step, cause)
	res, err := db.Exec(`UPDATE hosting_operations SET status='failed', error=?, lease_token='', lease_until=NULL, log_json=?, finished_at=?, updated_at=?
		WHERE id=? AND lease_token=? AND status='running'`,
		msg, appendLog(op, step, "failed", cause.Error()), sqlTime(now()), sqlTime(now()), op.ID, token)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return
	}
	if op.MachineID > 0 {
		_ = dbMachineSetState(db, op.MachineID, MachineQuarantined, msg)
		if m, _ := dbMachineGet(db, op.MachineID); m != nil {
			emitMachineQuarantined(ctx, m, msg)
		}
	}
	if op.TenantID != "" {
		_ = dbTenantSetError(db, op.TenantID, msg)
		if op.Kind == OpProvision || op.Kind == OpReinstall {
			_ = dbTenantSetStatus(db, op.TenantID, TenantFailed, msg)
		}
		_ = recordEvent(db, op.TenantID, "operation.failed", "hosting", map[string]any{"operation_id": op.ID, "kind": op.Kind, "step": step, "error": cause.Error()})
	}
	ctx.Logger().Warn("hosting operation failed", "operation_id", op.ID, "kind", op.Kind, "step", step, "err", cause)
	ctx.Emit("hosting.operation.failed", map[string]any{"operation_id": op.ID, "kind": op.Kind, "tenant_id": op.TenantID, "machine_id": op.MachineID, "step": step, "error": msg})
}

// finishOperation applies the run's outcome to the tenant and machine.
func (a *App) finishOperation(ctx *sdk.AppCtx, op *Operation, token string) {
	db := ctx.AppDB()
	tx, err := db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE hosting_operations SET status='succeeded', lease_token='', lease_until=NULL, finished_at=?, updated_at=?
		WHERE id=? AND lease_token=? AND status='running'`, sqlTime(now()), sqlTime(now()), op.ID, token)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return
	}
	var tenant *Tenant
	if op.TenantID != "" {
		if tenant, err = dbTenantGet(tx, op.TenantID); err != nil {
			return
		}
	}
	var machine *Machine
	if op.MachineID > 0 {
		if machine, err = dbMachineGet(tx, op.MachineID); err != nil {
			return
		}
	}
	event := ""
	switch op.Kind {
	case OpProvision, OpReinstall, OpResume:
		if tenant != nil {
			_ = dbTenantSetStatus(tx, tenant.ID, TenantActive, "")
			_ = dbTenantMergeMetadata(tx, tenant.ID, map[string]any{"access_granted": len(tenant.AccessKeys) > 0})
		}
		if machine != nil {
			_ = dbMachineSetState(tx, machine.ID, MachineAssigned, "")
		}
		if tenant != nil && tenant.ReservationID != "" {
			_, _ = tx.Exec(`UPDATE hosting_reservations SET status='confirmed', tenant_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status IN ('held','expired')`, tenant.ID, tenant.ReservationID)
		}
		event = map[string]string{OpProvision: "hosting.tenant.active", OpReinstall: "hosting.tenant.reinstalled", OpResume: "hosting.tenant.resumed"}[op.Kind]
	case OpSuspend:
		if tenant != nil {
			_ = dbTenantSetStatus(tx, tenant.ID, TenantSuspended, "")
			_ = dbTenantMergeMetadata(tx, tenant.ID, map[string]any{"access_granted": false})
		}
		if machine != nil {
			_ = dbMachineSetState(tx, machine.ID, MachineSuspended, "")
		}
		event = "hosting.tenant.suspended"
	case OpAccessKeys:
		if tenant != nil {
			_ = dbTenantMergeMetadata(tx, tenant.ID, map[string]any{"access_granted": len(tenant.AccessKeys) > 0})
		}
		event = "hosting.tenant.access_updated"
	case OpRestart:
		event = "hosting.tenant.restarted"
	case OpRelease:
		if tenant != nil {
			_ = dbTenantSetStatus(tx, tenant.ID, TenantReleased, "")
			_ = dbTenantMergeMetadata(tx, tenant.ID, map[string]any{"access_granted": false})
		}
		if machine != nil {
			_, _ = tx.Exec(`UPDATE hosting_machines SET tenant_id=NULL, updated_at=CURRENT_TIMESTAMP WHERE id=?`, machine.ID)
			verified := containsStep(op.Steps, "verify_clean") || containsStep(op.Steps, "device_verify_clean")
			if verified && machine.Validated {
				_ = dbMachineSetState(tx, machine.ID, MachineAvailable, "cleaned after "+op.TenantID)
			} else if verified {
				_ = dbMachineSetState(tx, machine.ID, MachineQuarantined, "cleaned, but the machine is not validated")
			} else {
				reason := "released; this pool has no automatic reset, clean it and run inspect"
				if eraseRefusedReason(machine) != "" {
					reason = "released; tunnel Macs are not erased automatically (it would remove the tunnel), clean it by hand and run inspect"
				}
				_ = dbMachineSetState(tx, machine.ID, MachineQuarantined, reason)
			}
		}
		event = "hosting.tenant.released"
	case OpInspect, OpClean:
		if machine != nil {
			_, _ = tx.Exec(`UPDATE hosting_machines SET online=1, last_seen_at=CURRENT_TIMESTAMP, script_hashes_json=CASE WHEN ? != '' THEN ? ELSE script_hashes_json END WHERE id=?`,
				hashesIfInstalled(op), hashesIfInstalled(op), machine.ID)
			if machine.TenantID != "" {
				// Never put a machine with a tenant back on sale.
				_ = dbMachineSetState(tx, machine.ID, machine.State, machine.StateReason)
			} else if machine.Validated {
				_ = dbMachineSetState(tx, machine.ID, MachineAvailable, op.Kind+" passed")
			} else {
				_ = dbMachineSetState(tx, machine.ID, MachineQuarantined, op.Kind+" passed; mark the machine validated to sell it")
			}
		}
		event = "hosting.machine." + op.Kind + "ed"
		if op.Kind == OpClean {
			event = "hosting.machine.cleaned"
		}
	}
	if op.TenantID != "" {
		_ = recordEvent(tx, op.TenantID, "operation.succeeded", "hosting", map[string]any{"operation_id": op.ID, "kind": op.Kind})
	}
	if err := tx.Commit(); err != nil {
		return
	}
	if event != "" {
		payload := map[string]any{"operation_id": op.ID, "tenant_id": op.TenantID, "machine_id": op.MachineID, "external_ref": tenantRef(tenant)}
		if machine.managed() {
			payload["managed_by"], payload["device_ref"] = machine.ManagedBy, machine.DeviceRef
		}
		ctx.Emit(event, payload)
	}
	// A freed machine may be waited on by a paid tenant.
	if op.Kind == OpRelease || op.Kind == OpInspect || op.Kind == OpClean {
		a.assignWaitingTenants(ctx)
	}
	// Start the next queued run for the same tenant or machine, if any.
	a.kickNext(ctx, op)
}

func hashesIfInstalled(op *Operation) string {
	if containsStep(op.Steps, "install_reset_scripts") {
		return jsonOrEmpty(shippedResetHashes(), "{}")
	}
	return ""
}

func tenantRef(t *Tenant) string {
	if t == nil {
		return ""
	}
	return t.ExternalRef
}

func containsStep(steps []string, want string) bool {
	for _, s := range steps {
		if s == want {
			return true
		}
	}
	return false
}

func (a *App) kickNext(ctx *sdk.AppCtx, op *Operation) {
	rows, err := ctx.AppDB().Query(`SELECT id FROM hosting_operations WHERE status='queued' AND ((tenant_id IS NOT NULL AND tenant_id=?) OR (machine_id IS NOT NULL AND machine_id=?)) ORDER BY created_at, rowid LIMIT 1`,
		op.TenantID, op.MachineID)
	if err != nil {
		return
	}
	var next string
	if rows.Next() {
		_ = rows.Scan(&next)
	}
	rows.Close()
	if next != "" {
		kickOperation(a, ctx, next)
	}
}

// retryOperation re-queues a failed run after operator review.
func (a *App) retryOperation(ctx *sdk.AppCtx, id string, reviewed bool, actorName string) (*Operation, error) {
	if !reviewed {
		return nil, errors.New("retry needs reviewed=true: check the machine and the run log first")
	}
	db := ctx.AppDB()
	op, err := dbOperationGet(db, id)
	if err != nil {
		return nil, err
	}
	if op == nil {
		return nil, fmt.Errorf("operation %s not found", id)
	}
	if op.Status != OpFailed {
		return nil, fmt.Errorf("operation %s is %s; only failed runs can be retried", id, op.Status)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE hosting_operations SET status='queued', attempts=attempts+1, error='', not_before=NULL, step_started_at=NULL, finished_at=NULL,
		log_json=?, actor=?, updated_at=? WHERE id=? AND status='failed'`, appendLog(op, op.Current, "retry", "reviewed by "+actorName), actorName, sqlTime(now()), id); err != nil {
		return nil, err
	}
	if op.MachineID > 0 {
		// Lift the failure quarantine to the state this run expects.
		state := machineStateDuring(op.Kind)
		if state == "" {
			state = MachineQuarantined
			if t, _ := dbTenantGet(tx, op.TenantID); t != nil {
				state = MachineAssigned
				if t.Status == TenantSuspended || t.Status == TenantResuming {
					state = MachineSuspended
				}
			}
		}
		if err := dbMachineSetState(tx, op.MachineID, state, "retrying "+op.Kind); err != nil {
			return nil, err
		}
	}
	if op.TenantID != "" && (op.Kind == OpProvision || op.Kind == OpReinstall) {
		if err := dbTenantSetStatus(tx, op.TenantID, TenantProvisioning, ""); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	kickOperation(a, ctx, id)
	return dbOperationGet(db, id)
}

// machineStateDuring is the pool machine state while a run of this kind is
// in flight.
func machineStateDuring(kind string) string {
	switch kind {
	case OpProvision:
		return MachinePreparing
	case OpRelease, OpReinstall, OpClean:
		return MachineCleaning
	default:
		return "" // suspend, resume, access keys, restart, inspect: state unchanged
	}
}

// fenceMachineOperations stops every unfinished run on a machine. A worker
// still holding a lease will fail its next fenced update and stop.
func fenceMachineOperations(db querier, machineID int64, reason string) error {
	_, err := db.Exec(`UPDATE hosting_operations SET status='failed', error=?, lease_token='', lease_until=NULL, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE machine_id=? AND status IN ('queued','running','waiting')`, reason, machineID)
	return err
}

// dueOperationIDs lists runs the worker should pick up.
func dueOperationIDs(db *sql.DB, limit int) ([]string, error) {
	t := sqlTime(now())
	rows, err := db.Query(`SELECT id FROM hosting_operations
		WHERE status='queued'
		   OR (status='waiting' AND (not_before IS NULL OR not_before<=?))
		   OR (status='running' AND (lease_until IS NULL OR lease_until<=?))
		ORDER BY created_at, rowid LIMIT ?`, t, t, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
