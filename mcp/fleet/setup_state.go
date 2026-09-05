package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func (a *App) setupComplete(id string) bool {
	var complete bool
	return a.store.db.QueryRow(`SELECT setup_complete FROM fleet_tenant_state WHERE tenant_id=?`, id).Scan(&complete) == nil && complete
}

func (a *App) provisionTenant(ctx context.Context, t *Tenant, base, token, email string) (*autoSetupResult, error) {
	var enc []byte
	if err := a.store.db.QueryRow(`SELECT setup_password_enc FROM fleet_tenant_state WHERE tenant_id=?`, t.ID).Scan(&enc); err != nil {
		return nil, err
	}
	password := ""
	if len(enc) > 0 {
		raw, err := a.keys.open(enc)
		if err != nil {
			return nil, err
		}
		password = string(raw)
	} else {
		password = randomPassword()
		var err error
		enc, err = a.keys.seal([]byte(password))
		if err != nil {
			return nil, err
		}
		if _, err = a.store.db.Exec(`UPDATE fleet_tenant_state SET setup_password_enc=?,setup_phase='register' WHERE tenant_id=?`, enc, t.ID); err != nil {
			return nil, err
		}
	}
	result, err := a.autoSetupTenant(ctx, base, token, email, password)
	if err != nil {
		_, _ = a.store.db.Exec(`UPDATE fleet_tenant_state SET setup_phase='recovery_required' WHERE tenant_id=?`, t.ID)
	}
	return result, err
}

// Registration may have succeeded before a timeout or controller restart.
// A successful login with our persisted password makes retry idempotent.
func canResumeSetup(ctx context.Context, base, email, password string) bool {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/api/auth/login", strings.NewReader(string(body)))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

func (a *App) httpSetupRecovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	id := strings.Split(strings.TrimPrefix(r.URL.Path, "/tenants/"), "/")[0]
	done, err := a.beginTenantOperation(id, "setup recovery")
	if err != nil {
		writeJSONErr(w, 409, err)
		return
	}
	defer done()
	t, _, err := a.store.get(id)
	if err != nil {
		writeJSONErr(w, 404, err)
		return
	}
	if a.setupComplete(id) {
		writeJSONErr(w, 409, fmt.Errorf("setup already complete; use attach-key to replace credentials"))
		return
	}
	var enc []byte
	enc, err = a.store.getSetupToken(id)
	if err != nil {
		writeJSONErr(w, 400, err)
		return
	}
	var raw []byte
	if len(enc) > 0 {
		raw, err = a.keys.open(enc)
		if err != nil {
			writeJSONErr(w, 400, err)
			return
		}
	}
	base, err := a.internalTenantBaseURL(globalCtx, t)
	if err != nil {
		writeJSONErr(w, 400, err)
		return
	}
	result, err := a.provisionTenant(r.Context(), t, base, string(raw), t.OwnerEmail)
	if err != nil {
		writeJSONErr(w, 400, err)
		return
	}
	key, err := a.keys.seal([]byte(result.APIKey))
	if err == nil {
		err = a.store.attachAPIKey(id, key)
	}
	if err != nil {
		writeJSONErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tenant_id": id, "admin_password": result.Password, "api_key": result.APIKey, "status": StatusActive})
}

func (a *App) persistSetupToken(id, token string) error {
	enc, err := a.keys.seal([]byte(token))
	if err != nil {
		return err
	}
	_, err = a.store.db.Exec(`UPDATE fleet_tenants SET setup_token_enc=?,status='setup_pending' WHERE id=?`, enc, id)
	return err
}
