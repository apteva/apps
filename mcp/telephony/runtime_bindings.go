package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"time"
)

const runtimeBindingsPath = "/_runtime/bindings"

type runtimeBindingRequest struct {
	Phase    string         `json:"phase"`
	ChangeID string         `json:"change_id"`
	Previous map[string]any `json:"previous"`
	Desired  map[string]any `json:"desired"`
}

func runtimeCarrierIDs(value any) []int64 {
	if m, ok := value.(map[string]any); ok {
		list, _ := m["ids"].([]any)
		ids := []int64{}
		for _, v := range list {
			ids = append(ids, runtimeCarrierIDs(v)...)
		}
		return ids
	}
	var id int64
	switch n := value.(type) {
	case float64:
		if n == float64(int64(n)) {
			id = int64(n)
		}
	case int64:
		id = n
	case int:
		id = int64(n)
	}
	if id > 0 {
		return []int64{id}
	}
	return nil
}
func hasRuntimeID(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// A signed, platform-only protocol. Browser/app-user routes cannot prepare a
// drain. SDK bearer auth is supplemented with a body signature so a gateway
// which forwards an operator request using the app token cannot invoke it.
func (a *App) handleRuntimeBindings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	token := os.Getenv("APTEVA_APP_TOKEN")
	signature, e := hex.DecodeString(r.Header.Get("X-Apteva-Runtime-Signature"))
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write(raw)
	if token == "" || e != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		http.Error(w, "runtime signature required", 403)
		return
	}
	var request runtimeBindingRequest
	if json.Unmarshal(raw, &request) != nil || request.ChangeID == "" || len(request.ChangeID) > 128 || request.Previous == nil || request.Desired == nil {
		http.Error(w, "invalid change", 400)
		return
	}
	if err := validateRuntimeBindingChange(request); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	result, err := a.applyRuntimeBindingRequest(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, result)
}

func (a *App) applyRuntimeBindingRequest(req runtimeBindingRequest) (map[string]any, error) {
	if err := validateRuntimeBindingChange(req); err != nil {
		return nil, err
	}

	previous := runtimeCarrierIDs(req.Previous["carrier"])
	desired := runtimeCarrierIDs(req.Desired["carrier"])
	// Canonical request binds an idempotent phase transition to its exact inputs.
	identity := req
	identity.Phase = ""
	canonical, _ := json.Marshal(identity)
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	tx, err := a.db().db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var change, stored, status string
	err = tx.QueryRow(`SELECT change_id,request_json,status FROM runtime_binding_updates WHERE id=1`).Scan(&change, &stored, &status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if change == req.ChangeID && stored != string(canonical) {
		return nil, errors.New("change payload mismatch")
	}
	if change != req.ChangeID && status == "prepared" {
		return nil, errors.New("another binding change is draining")
	}
	if req.Phase == "commit" && change != req.ChangeID {
		return nil, errors.New("change was not prepared")
	}
	if req.Phase == "prepare" && (change != req.ChangeID || status != "committed") {
		for _, id := range previous {
			if !hasRuntimeID(desired, id) {
				if _, err = tx.Exec(`INSERT INTO carrier_binding_drains(connection_id,change_id,removed) VALUES(?,?,0) ON CONFLICT(connection_id) DO UPDATE SET change_id=excluded.change_id,removed=0`, id, req.ChangeID); err != nil {
					return nil, err
				}
			}
		}
		_, err = tx.Exec(`INSERT INTO runtime_binding_updates(id,change_id,request_json,status) VALUES(1,?,?,'prepared') ON CONFLICT(id) DO UPDATE SET change_id=excluded.change_id,request_json=excluded.request_json,status='prepared'`, req.ChangeID, string(canonical))
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	work, err := a.bindingDrainWork(req.ChangeID)
	if err != nil {
		return nil, err
	}
	if req.Phase == "commit" {
		if work > 0 {
			return nil, errors.New("carrier work is still draining")
		}
		tx, err = a.db().db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		for _, id := range desired {
			if _, err = tx.Exec(`DELETE FROM carrier_binding_drains WHERE connection_id=?`, id); err != nil {
				return nil, err
			}
		}
		if _, err = tx.Exec(`UPDATE carrier_binding_drains SET removed=1 WHERE change_id=?`, req.ChangeID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(`UPDATE runtime_binding_updates SET status='committed' WHERE id=1`); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	}
	ids := []int64{}
	for _, id := range previous {
		if !hasRuntimeID(desired, id) {
			ids = append(ids, id)
		}
	}
	return map[string]any{"ready": work == 0, "retained_work": work, "draining_connections": ids, "change_id": req.ChangeID}, nil
}

// Keep broker authorization until live legs, provider effects and recording
// imports finish. The quiet period retains late terminal/recording callbacks.
func (a *App) bindingDrainWork(change string) (int, error) {
	var count int
	err := a.db().db.QueryRow(`SELECT
 (SELECT COUNT(*) FROM calls c JOIN carrier_binding_drains d ON d.connection_id=c.carrier_connection_id WHERE d.change_id=? AND
 (c.status NOT IN ('completed','failed','no-answer','busy','canceled') OR c.media_active=1)) +
 (SELECT COUNT(*) FROM calls c JOIN carrier_binding_drains d ON d.connection_id=c.carrier_connection_id WHERE d.change_id=? AND c.ended_at>=?) +
 (SELECT COUNT(*) FROM routing_effects e JOIN calls c ON c.id=e.call_id JOIN carrier_binding_drains d ON d.connection_id=c.carrier_connection_id WHERE d.change_id=? AND e.status='pending') +
 (SELECT COUNT(*) FROM carrier_activations x JOIN calls c ON c.id=x.call_id JOIN carrier_binding_drains d ON d.connection_id=c.carrier_connection_id WHERE d.change_id=? AND x.status IN ('pending','waiting')) +
 (SELECT COUNT(*) FROM recordings r JOIN carrier_binding_drains d ON d.connection_id=r.carrier_connection_id WHERE d.change_id=? AND r.deleted_at='' AND r.storage_status IN ('pending','importing','failed')) +
 (SELECT COUNT(*) FROM call_legs l JOIN calls c ON c.id=l.id JOIN carrier_binding_drains d ON d.connection_id=c.carrier_connection_id WHERE d.change_id=? AND l.status NOT IN ('finished','canceled','failed'))`, change, change, time.Now().UTC().Add(-60*time.Second).Format(time.RFC3339), change, change, change, change).Scan(&count)
	return count, err
}

func validateRuntimeBindingChange(req runtimeBindingRequest) error {
	if req.Phase != "prepare" && req.Phase != "commit" {
		return errors.New("unsupported phase")
	}
	for k, v := range req.Previous {
		if k != "carrier" && !reflect.DeepEqual(v, req.Desired[k]) {
			return errors.New("this binding role does not support live updates: " + k)
		}
	}
	for k, v := range req.Desired {
		if k != "carrier" && !reflect.DeepEqual(v, req.Previous[k]) {
			return errors.New("this binding role does not support live updates: " + k)
		}
	}
	return nil
}
