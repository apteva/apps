package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type operationLease struct {
	Operation             string
	Phase                 string
	RequestedVersion      string
	PreviousVersion       string
	PreviousTargetVersion string
	UpdatedAt             time.Time
}

// Only release the stop fence after stopped status has been committed.
func (a *App) completeStop(id string) error {
	if err := a.store.setStatus(id, StatusStopped, "user"); err != nil {
		return fmt.Errorf("persist stopped state; recovery required: %w", err)
	}
	return a.checkpointOperation(id, "completed", nil)
}

func (s *store) operationLease(tenantID string) (*operationLease, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var lease operationLease
	err := s.db.QueryRow(`
		SELECT operation, phase, requested_version, previous_version,
		       previous_target_version, updated_at
		FROM fleet_operation_leases WHERE tenant_id = ?
	`, tenantID).Scan(&lease.Operation, &lease.Phase, &lease.RequestedVersion,
		&lease.PreviousVersion, &lease.PreviousTargetVersion, &lease.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &lease, nil
}

func (s *store) setOperationLease(tenantID, operation, phase, requested, previousVersion, previousTarget string) error {
	_, err := s.db.Exec(`
		INSERT INTO fleet_operation_leases
		    (tenant_id, operation, phase, requested_version, previous_version, previous_target_version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id) DO UPDATE SET
		    operation = excluded.operation,
		    phase = excluded.phase,
		    requested_version = excluded.requested_version,
		    previous_version = excluded.previous_version,
		    previous_target_version = excluded.previous_target_version,
		    updated_at = excluded.updated_at
	`, tenantID, operation, phase, requested, previousVersion, previousTarget, time.Now().UTC())
	return err
}

func (s *store) setOperationLeasePhase(tenantID, phase string) error {
	result, err := s.db.Exec(`UPDATE fleet_operation_leases SET phase = ?, updated_at = ? WHERE tenant_id = ?`,
		phase, time.Now().UTC(), tenantID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("operation lease missing for tenant %s", tenantID)
	}
	return nil
}

func (s *store) clearOperationLease(tenantID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM fleet_operation_leases WHERE tenant_id = ?`, tenantID)
	return err
}

func (a *App) beginTenantOperation(tenantID, operation string) (func(), error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}
	a.opMu.Lock()
	defer a.opMu.Unlock()
	if a.operations == nil {
		a.operations = map[string]string{}
	}
	if active := a.operations[tenantID]; active != "" {
		return nil, fmt.Errorf("tenant already has an operation in progress: %s", active)
	}
	if lease, err := a.store.operationLease(tenantID); err != nil {
		return nil, fmt.Errorf("read tenant operation lease: %w", err)
	} else if lease != nil && operation != "update" {
		return nil, fmt.Errorf("tenant already has a durable operation in progress: %s (%s)", lease.Operation, lease.Phase)
	}
	opID := newID()
	tenant, _, getErr := a.store.get(tenantID)
	if getErr != nil && getErr != ErrNotFound {
		return nil, getErr
	}
	if tenant != nil {
		snapshot, _ := json.Marshal(map[string]any{"source": tenant})
		if _, err := a.store.db.Exec(`INSERT INTO fleet_active_operations(tenant_id,id,operation,snapshot) VALUES(?,?,?,?)`, tenantID, opID, operation, string(snapshot)); err != nil {
			return nil, fmt.Errorf("tenant operation pending; inspect/recover before retrying: %w", err)
		}
	}
	a.operations[tenantID] = operation
	return func() {
		a.opMu.Lock()
		if a.operations[tenantID] == operation {
			_, _ = a.store.db.Exec(`DELETE FROM fleet_active_operations WHERE tenant_id=? AND id=? AND phase!='recovery_required'`, tenantID, opID)
			delete(a.operations, tenantID)
		}
		a.opMu.Unlock()
	}, nil
}

func (a *App) tenantOperation(tenantID string) string {
	a.opMu.Lock()
	active := a.operations[tenantID]
	a.opMu.Unlock()
	if active != "" {
		return active
	}
	if op, err := a.store.activeOperation(tenantID); err != nil {
		return "operation state unavailable"
	} else if op != nil {
		return op.Operation + " (" + op.Phase + ")"
	}
	lease, err := a.store.operationLease(tenantID)
	if err != nil {
		// Fail closed: reconciliation and health-driven respawn must never
		// start a duplicate runtime merely because the lease cannot be read.
		return "durable operation state unavailable"
	}
	if lease == nil {
		return ""
	}
	return lease.Operation + " (" + lease.Phase + ")"
}
