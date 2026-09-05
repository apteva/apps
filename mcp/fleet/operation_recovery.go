package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type activeOperation struct {
	ID        string          `json:"id"`
	Operation string          `json:"operation"`
	Phase     string          `json:"phase"`
	Snapshot  json.RawMessage `json:"snapshot"`
}

func (s *store) activeOperation(id string) (*activeOperation, error) {
	var op activeOperation
	var snapshot string
	err := s.db.QueryRow(`SELECT id,operation,phase,snapshot FROM fleet_active_operations WHERE tenant_id=?`, id).Scan(&op.ID, &op.Operation, &op.Phase, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	op.Snapshot = json.RawMessage(snapshot)
	return &op, err
}
func (a *App) checkpointOperation(id, phase string, snapshot any) error {
	if snapshot == nil {
		_, err := a.store.db.Exec(`UPDATE fleet_active_operations SET phase=? WHERE tenant_id=?`, phase, id)
		return err
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = a.store.db.Exec(`UPDATE fleet_active_operations SET phase=?,snapshot=? WHERE tenant_id=?`, phase, string(b), id)
	return err
}
func (a *App) requireRecovery(id string, err error) {
	_ = a.checkpointOperation(id, "recovery_required", nil)
	_ = a.store.recordEvent(id, "operation_recovery_required", "system", map[string]any{"error": err.Error()})
}

// Recovery never guesses which copy is safe to run. Fence all recorded
// endpoints, preserve their data and leave the authoritative tenant stopped.
// A subsequent Start is an explicit activation of the registry's location.
func (a *App) httpRecoverOperation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	id := strings.Split(strings.TrimPrefix(r.URL.Path, "/tenants/"), "/")[0]
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil || !req.Confirm {
		writeJSONErr(w, 400, fmt.Errorf("confirm=true is required to stop recorded runtimes"))
		return
	}
	a.opMu.Lock()
	if a.operations[id] != "" {
		a.opMu.Unlock()
		writeJSONErr(w, 409, fmt.Errorf("operation is still executing"))
		return
	}
	a.operations[id] = "recovery"
	a.opMu.Unlock()
	defer func() { a.opMu.Lock(); delete(a.operations, id); a.opMu.Unlock() }()
	op, err := a.store.activeOperation(id)
	if err != nil {
		writeJSONErr(w, 500, err)
		return
	}
	current, _, err := a.store.get(id)
	if err != nil {
		writeJSONErr(w, 404, err)
		return
	}
	if op == nil {
		lease, leaseErr := a.store.operationLease(id)
		if leaseErr != nil {
			writeJSONErr(w, 500, leaseErr)
			return
		}
		if lease == nil {
			writeJSONErr(w, 409, fmt.Errorf("no interrupted operation"))
			return
		}
		snapshot, _ := json.Marshal(map[string]any{"source": current})
		op = &activeOperation{Operation: lease.Operation, Phase: lease.Phase, Snapshot: snapshot}
	}
	var state struct {
		Source          *Tenant          `json:"source"`
		Target          *Tenant          `json:"target"`
		PreviousDataDir string           `json:"previous_data_dir"`
		SourceMetadata  *restoreMetadata `json:"source_metadata"`
	}
	if err = json.Unmarshal(op.Snapshot, &state); err != nil {
		writeJSONErr(w, 500, err)
		return
	}
	for _, tn := range []*Tenant{state.Target, state.Source, current} {
		if tn == nil || tn.Kind == KindRemote {
			continue
		}
		if err = a.stopTenantOnHost(globalCtx, tn, portFromTenant(tn)); err != nil {
			a.requireRecovery(id, err)
			writeJSONErr(w, 409, err)
			return
		}
	}
	if state.PreviousDataDir != "" {
		if state.Source == nil || state.SourceMetadata == nil || state.Source.IsHosted() || validateLocalTenantDir(state.Source.Slug, state.Source.ConfigDir) != nil || filepath.Dir(state.PreviousDataDir) != filepath.Dir(state.Source.ConfigDir) || !strings.HasPrefix(state.PreviousDataDir, state.Source.ConfigDir+".prerestore-") {
			writeJSONErr(w, 409, fmt.Errorf("invalid restore recovery metadata; data preserved"))
			return
		}
		info, statErr := os.Lstat(state.PreviousDataDir)
		if statErr == nil && info.IsDir() {
			if _, err = os.Lstat(state.Source.ConfigDir); err == nil {
				failed := state.Source.ConfigDir + ".failedrestore-" + time.Now().UTC().Format("20060102-150405.000000000")
				if err = publishTenantDirectory(state.Source.ConfigDir, failed); err != nil {
					writeJSONErr(w, 409, err)
					return
				}
			} else if !os.IsNotExist(err) {
				writeJSONErr(w, 409, err)
				return
			}
			if err = publishTenantDirectory(state.PreviousDataDir, state.Source.ConfigDir); err != nil {
				writeJSONErr(w, 409, err)
				return
			}
		} else if !os.IsNotExist(statErr) {
			writeJSONErr(w, 409, fmt.Errorf("previous data directory is unavailable"))
			return
		}
		if _, err = os.Stat(state.Source.ConfigDir); err != nil {
			writeJSONErr(w, 409, fmt.Errorf("authoritative data directory missing; recovery remains pending"))
			return
		}
		if err = a.store.restoreMetadata(id, *state.SourceMetadata); err != nil {
			writeJSONErr(w, 500, err)
			return
		}
	}
	if err = a.store.setStatus(id, StatusStopped, "user:recovery"); err != nil {
		writeJSONErr(w, 500, err)
		return
	}
	if err = a.store.clearOperationLease(id); err != nil {
		writeJSONErr(w, 500, err)
		return
	}
	_, err = a.store.db.Exec(`DELETE FROM fleet_active_operations WHERE tenant_id=? AND id=?`, id, op.ID)
	if err != nil {
		writeJSONErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tenant_id": id, "status": StatusStopped, "note": "Recorded runtimes stopped; data preserved. Inspect the recorded locations before starting the authoritative tenant.", "recovery": state})
}
