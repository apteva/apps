package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var ErrDeviceAuth = errors.New("invalid or unavailable device write credential")

type HTTPDeviceCredential struct {
	ID        string  `json:"credential_id"`
	DeviceID  string  `json:"device_id"`
	Label     string  `json:"label"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt string  `json:"expires_at"`
	RevokedAt *string `json:"revoked_at,omitempty"`
}

func (a *App) issueHTTPCredential(args map[string]any) (map[string]any, error) {
	id, label, key := str(args, "device_id"), str(args, "label"), str(args, "idempotency_key")
	if key == "" || len(key) > 128 {
		return nil, errors.New("idempotency_key must contain 1 to 128 characters")
	}
	if len(label) > 128 {
		return nil, errors.New("label exceeds 128 characters")
	}
	ttl := 0
	// No fractional TTL or implicit conversion of malformed JSON numbers.
	if value, ok := args["expires_in_seconds"]; ok {
		raw, _ := json.Marshal(value)
		n, e := strconv.Atoi(string(raw))
		if e != nil {
			return nil, errors.New("expires_in_seconds must be an integer")
		}
		ttl = n
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	d, err := getDevice(a.ctx.AppDB(), a.projectID, id, false)
	if err != nil {
		return nil, err
	}
	if !d.Enabled {
		return nil, ErrDeviceAuth
	}
	hash := fingerprint(map[string]any{"label": label, "expires_in_seconds": ttl})
	var existing, oldHash string
	err = a.ctx.AppDB().QueryRow(`SELECT id,fingerprint FROM device_http_credentials WHERE project_id=? AND device_id=? AND idempotency_key=?`, a.projectID, id, key).Scan(&existing, &oldHash)
	if err == nil {
		if hash != oldHash {
			return nil, ErrConflict
		}
		return a.httpCredentialResult(existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if ttl == 0 {
		ttl = 86400 * 30
		if ttl > a.limit("max_http_credential_ttl_seconds", 86400*365) {
			ttl = a.limit("max_http_credential_ttl_seconds", 86400*365)
		}
	}
	if ttl < 60 || ttl > a.limit("max_http_credential_ttl_seconds", 86400*365) {
		return nil, errors.New("expires_in_seconds exceeds policy or is below 60 seconds")
	}
	var pending int
	if err = a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_operations WHERE project_id=? AND device_id=? AND status='pending' AND kind IN('rotate','disable','delete')`, a.projectID, id).Scan(&pending); err != nil {
		return nil, err
	}
	if pending > 0 {
		return nil, ErrDeviceAuth
	}
	var count int
	if err = a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_http_credentials WHERE project_id=? AND device_id=? AND revoked_at IS NULL AND expires_at>?`, a.projectID, id, nowText()).Scan(&count); err != nil {
		return nil, err
	}
	if count >= a.limit("max_http_credentials_per_device", 5) {
		return nil, ErrLimit
	}
	cid := newID("hic_")
	secret, err := generateSecret()
	if err != nil {
		return nil, err
	}
	token := "dvi_" + cid + "." + secret
	digest := sha256.Sum256([]byte(token))
	cipherText, err := a.cryptSecret("http:"+cid, token, true)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	created := formatTime(now)
	expires := formatTime(now.Add(time.Duration(ttl) * time.Second))
	recovery := formatTime(now.Add(time.Duration(a.limit("secret_recovery_hours", 24)) * time.Hour))
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO device_http_credentials(id,project_id,device_id,label,secret_hash,secret_cipher,created_at,expires_at,recovery_until,idempotency_key,fingerprint)VALUES(?,?,?,?,?,?,?,?,?,?,?)`, cid, a.projectID, id, label, digest[:], cipherText, created, expires, recovery, key, hash)
	if err != nil {
		return nil, err
	}
	if err = a.event(tx, id, "devices.credential.created", map[string]any{"credential_id": cid, "transport": "http", "expires_at": expires}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	a.wakeOutbox()
	return a.httpCredentialResult(cid)
}
func (a *App) httpCredentialResult(id string) (map[string]any, error) {
	var c HTTPDeviceCredential
	var encrypted, recovery string
	err := a.ctx.AppDB().QueryRow(`SELECT id,device_id,label,created_at,expires_at,revoked_at,secret_cipher,recovery_until FROM device_http_credentials WHERE project_id=? AND id=?`, a.projectID, id).Scan(&c.ID, &c.DeviceID, &c.Label, &c.CreatedAt, &c.ExpiresAt, &c.RevokedAt, &encrypted, &recovery)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"credential": c, "header": "X-Device-Token", "ingest_path": "/ingest/" + c.DeviceID + "/{state|telemetry}", "recovery_until": recovery}
	if c.RevokedAt != nil || c.ExpiresAt <= nowText() {
		result["unavailable"] = true
		return result, nil
	}
	if encrypted == "" || recovery <= nowText() {
		result["secret_recovery_expired"] = true
		return result, nil
	}
	token, err := a.cryptSecret("http:"+id, encrypted, false)
	if err != nil {
		return nil, err
	}
	result["token"] = token
	return result, nil
}
func (a *App) listHTTPCredentials(id string) ([]HTTPDeviceCredential, error) {
	if _, err := getDevice(a.ctx.AppDB(), a.projectID, id, false); err != nil {
		return nil, err
	}
	rows, err := a.ctx.AppDB().Query(`SELECT id,device_id,label,created_at,expires_at,revoked_at FROM device_http_credentials WHERE project_id=? AND device_id=? ORDER BY created_at,id`, a.projectID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HTTPDeviceCredential{}
	for rows.Next() {
		var c HTTPDeviceCredential
		if err = rows.Scan(&c.ID, &c.DeviceID, &c.Label, &c.CreatedAt, &c.ExpiresAt, &c.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (a *App) revokeHTTPCredential(device, id string) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	if _, err := getDevice(a.ctx.AppDB(), a.projectID, device, false); err != nil {
		return nil, err
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var revoked *string
	if err = tx.QueryRow(`SELECT revoked_at FROM device_http_credentials WHERE project_id=? AND device_id=? AND id=?`, a.projectID, device, id).Scan(&revoked); err != nil {
		return nil, fmt.Errorf("%w: credential", ErrNotFound)
	}
	if revoked != nil {
		return map[string]any{"credential_id": id, "revoked": true}, nil
	}
	if _, err = tx.Exec(`UPDATE device_http_credentials SET revoked_at=?,secret_cipher='' WHERE project_id=? AND id=?`, nowText(), a.projectID, id); err != nil {
		return nil, err
	}
	if err = a.event(tx, device, "devices.credential.revoked", map[string]any{"credential_id": id, "transport": "http"}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	a.wakeOutbox()
	return map[string]any{"credential_id": id, "revoked": true}, nil
}
func (a *App) authenticateDeviceWrite(token, device string) (Device, error) {
	tokenID, _, ok := strings.Cut(strings.TrimPrefix(token, "dvi_"), ".")
	if !ok || !strings.HasPrefix(token, "dvi_hic_") || len(token) > 256 {
		return Device{}, ErrDeviceAuth
	}
	var stored []byte
	var expires string
	var revoked *string
	var id string
	err := a.ctx.AppDB().QueryRow(`SELECT device_id,secret_hash,expires_at,revoked_at FROM device_http_credentials WHERE project_id=? AND id=?`, a.projectID, tokenID).Scan(&id, &stored, &expires, &revoked)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Device{}, err
	}
	digest := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(digest[:], stored) != 1 || err != nil || id != device || revoked != nil || expires <= nowText() {
		return Device{}, ErrDeviceAuth
	}
	d, err := getDevice(a.ctx.AppDB(), a.projectID, id, false)
	if errors.Is(err, ErrNotFound) {
		return Device{}, ErrDeviceAuth
	}
	if err != nil {
		return d, err
	}
	if !d.Enabled {
		return Device{}, ErrDeviceAuth
	}
	var pending int
	if err = a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_operations WHERE project_id=? AND device_id=? AND status='pending' AND kind IN('rotate','disable','delete')`, a.projectID, id).Scan(&pending); err != nil {
		return Device{}, err
	}
	if pending > 0 {
		return Device{}, ErrDeviceAuth
	}
	return d, nil
}
func (a *App) handleDeviceIngest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/ingest/"), "/")
	if len(parts) != 2 || !validDeviceID(parts[0]) || (parts[1] != "state" && parts[1] != "telemetry") {
		writeError(w, 404, ErrNotFound)
		return
	}
	device, kind := parts[0], parts[1]
	token := r.Header.Get("X-Device-Token")
	if token == "" && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer dvi_") {
		token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if _, err := a.authenticateDeviceWrite(token, device); err != nil {
		deviceAuthError(w, err)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 131072))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, 413, errors.New("device payload exceeds 128 KiB"))
		} else {
			writeError(w, 400, errors.New("unable to read submission"))
		}
		return
	}
	var envelope map[string]any
	if err = decodeJSON(string(raw), &envelope); err != nil || envelope == nil {
		writeError(w, 400, errors.New("body must be a device data envelope"))
		return
	}
	// An authenticated device cannot override either ownership dimension.
	if scope := str(envelope, "scope_id"); scope != "" && scope != a.projectID {
		writeError(w, 403, ErrDeviceAuth)
		return
	}
	if id := str(envelope, "device_id"); id != "" && id != device {
		writeError(w, 403, ErrDeviceAuth)
		return
	}
	a.mutationMu.Lock()
	d, err := a.authenticateDeviceWrite(token, device)
	duplicate := false
	if err == nil {
		var n int
		err = a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_messages WHERE project_id=? AND device_id=? AND message_id=?`, a.projectID, device, str(envelope, "message_id")).Scan(&n)
		duplicate = n > 0
	}
	if err == nil {
		err = a.ingestWithProtocolLocked(d, kind, string(raw), nowText(), "apteva.devices/v2")
	}
	var received string
	if err == nil {
		err = a.ctx.AppDB().QueryRow(`SELECT received_at FROM device_messages WHERE project_id=? AND device_id=? AND message_id=?`, a.projectID, device, str(envelope, "message_id")).Scan(&received)
	}
	a.mutationMu.Unlock()
	if err != nil {
		if errors.Is(err, ErrDeviceAuth) {
			deviceAuthError(w, err)
		} else {
			respond(w, nil, err)
		}
		return
	}
	a.wakeOutbox()
	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"accepted": true, "message_id": str(envelope, "message_id"), "duplicate": duplicate, "device_id": device, "scope_id": a.projectID, "received_at": received})
}
func deviceAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrDeviceAuth) {
		writeError(w, 401, ErrDeviceAuth)
	} else {
		writeError(w, 503, errors.New("device authentication unavailable"))
	}
}
func (a *App) handleHTTPCredentials(w http.ResponseWriter, r *http.Request, device, action string) {
	if action == "ingest-credentials" {
		if r.Method == http.MethodGet {
			result, err := a.listHTTPCredentials(device)
			respond(w, result, err)
			return
		}
		if r.Method == http.MethodPost {
			var args map[string]any
			if !decodeBody(w, r, &args) {
				return
			}
			args["device_id"] = device
			if !idempotencyHeader(w, r, args) {
				return
			}
			result, err := a.issueHTTPCredential(args)
			respond(w, result, err)
			return
		}
		methodNotAllowed(w)
		return
	}
	id := strings.TrimPrefix(action, "ingest-credentials/")
	if id == action || strings.Contains(id, "/") || r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	result, err := a.revokeHTTPCredential(device, id)
	respond(w, result, err)
}
