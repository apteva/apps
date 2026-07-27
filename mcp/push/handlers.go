package main

import (
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

const maxBodyBytes = 64 << 10

type registerRequest struct {
	ProviderToken string `json:"provider_token"`
	Platform      string `json:"platform"`
	InstanceRef   string `json:"instance_ref"`
	UserRef       string `json:"user_ref"`
	AppVersion    string `json:"app_version"`
}

type deliveryRequest struct {
	DeviceID       string `json:"device_id"`
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	Badge          *int   `json:"badge"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (a *App) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleRegister(w http.ResponseWriter, r *http.Request) {
	var input registerRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	input.ProviderToken = strings.TrimSpace(input.ProviderToken)
	input.InstanceRef = strings.TrimSpace(input.InstanceRef)
	if input.Platform == "" {
		input.Platform = "ios"
	}
	if input.Platform != "ios" || len(input.ProviderToken) < 32 || input.InstanceRef == "" {
		writeError(w, http.StatusBadRequest, "provider_token, platform=ios, and instance_ref are required")
		return
	}
	encrypted, err := a.cipher.encrypt(input.ProviderToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not protect device token")
		return
	}
	d, err := a.store.upsertDevice(encrypted, digest(input.ProviderToken), input.UserRef, input.AppVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not register device")
		return
	}
	secret, err := randomValue("push_", 32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create grant")
		return
	}
	g, err := a.store.createGrant(d.ID, input.InstanceRef, digest(secret), time.Now().UTC().Add(365*24*time.Hour))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create grant")
		return
	}
	a.ctx.Emit("device.registered", map[string]any{"device_id": d.ID, "platform": d.Platform})
	writeJSON(w, http.StatusCreated, map[string]any{
		"device":     d,
		"grant":      secret,
		"expires_at": g.ExpiresAt,
	})
}

func (a *App) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	g, ok := a.requireGrant(w, r)
	if !ok {
		return
	}
	if r.PathValue("id") != g.DeviceID {
		writeError(w, http.StatusForbidden, "grant does not belong to this device")
		return
	}
	a.revokeDevice(w, g.DeviceID)
}

func (a *App) handleAdminDeleteDevice(w http.ResponseWriter, r *http.Request) {
	a.revokeDevice(w, r.PathValue("id"))
}

func (a *App) revokeDevice(w http.ResponseWriter, id string) {
	if err := a.store.revokeDevice(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "device not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not revoke device")
		return
	}
	a.ctx.Emit("device.revoked", map[string]any{"device_id": id})
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
}

func (a *App) handleCreateDelivery(w http.ResponseWriter, r *http.Request) {
	g, ok := a.requireGrant(w, r)
	if !ok {
		return
	}
	var input deliveryRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if input.DeviceID != g.DeviceID {
		writeError(w, http.StatusForbidden, "grant does not belong to this device")
		return
	}
	if !validDeliveryType(input.Type) || strings.TrimSpace(input.IdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, "type and idempotency_key are required")
		return
	}
	if input.Badge != nil && (*input.Badge < 0 || *input.Badge > 9999) {
		writeError(w, http.StatusBadRequest, "badge must be between 0 and 9999")
		return
	}
	d, duplicate, err := a.store.createDelivery(g, input.Type, input.ItemID, input.Badge, input.IdempotencyKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create delivery")
		return
	}
	if duplicate {
		writeJSON(w, http.StatusOK, d)
		return
	}
	a.dispatch(w, d)
}

func (a *App) handleTestDevice(w http.ResponseWriter, r *http.Request) {
	g, ok := a.requireGrant(w, r)
	if !ok {
		return
	}
	if r.PathValue("id") != g.DeviceID {
		writeError(w, http.StatusForbidden, "grant does not belong to this device")
		return
	}
	key, err := randomValue("test_", 12)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create test delivery")
		return
	}
	d, _, err := a.store.createDelivery(g, "test", "", nil, key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create test delivery")
		return
	}
	a.dispatch(w, d)
}

func (a *App) handleAdminTestDevice(w http.ResponseWriter, r *http.Request) {
	d, err := a.store.deviceByID(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "device not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load device")
		return
	}
	var g grant
	var revoked sql.NullString
	err = a.store.db.QueryRow(`
		SELECT id, device_id, instance_ref, expires_at, revoked_at
		FROM grants WHERE device_id = ? ORDER BY created_at DESC LIMIT 1`, d.ID).
		Scan(&g.ID, &g.DeviceID, &g.InstanceRef, &g.ExpiresAt, &revoked)
	if err != nil || revoked.Valid {
		writeError(w, http.StatusConflict, "device has no active grant")
		return
	}
	expires, parseErr := time.Parse(time.RFC3339Nano, g.ExpiresAt)
	if parseErr != nil || !expires.After(time.Now().UTC()) {
		writeError(w, http.StatusConflict, "device has no active grant")
		return
	}
	key, err := randomValue("admin_test_", 12)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create test delivery")
		return
	}
	push, _, err := a.store.createDelivery(&g, "test", "", nil, key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create test delivery")
		return
	}
	a.dispatch(w, push)
}

func (a *App) dispatch(w http.ResponseWriter, d *delivery) {
	device, err := a.store.deviceByID(d.DeviceID)
	if err != nil || device.Status != "active" {
		finished, _ := a.store.finishDelivery(d.ID, "failed", "", "device is not active")
		writeJSON(w, http.StatusConflict, finished)
		return
	}
	token, err := a.cipher.decrypt(device.TokenCiphertext)
	if err != nil {
		finished, _ := a.store.finishDelivery(d.ID, "failed", "", "stored device token is unavailable")
		writeJSON(w, http.StatusInternalServerError, finished)
		return
	}
	result, sendErr := a.provider.send(a.ctx, token, d)
	if sendErr != nil {
		providerID := ""
		if result != nil {
			providerID = result.ID
			if result.PermanentFailure {
				a.store.markDeviceInvalid(d.DeviceID)
			}
		}
		finished, _ := a.store.finishDelivery(d.ID, "failed", providerID, sendErr.Error())
		a.ctx.Emit("delivery.failed", map[string]any{"delivery_id": d.ID, "device_id": d.DeviceID})
		writeJSON(w, http.StatusBadGateway, finished)
		return
	}
	finished, err := a.store.finishDelivery(d.ID, "sent", result.ID, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "notification was sent but delivery state could not be saved")
		return
	}
	a.ctx.Emit("delivery.sent", map[string]any{"delivery_id": d.ID, "device_id": d.DeviceID})
	writeJSON(w, http.StatusCreated, finished)
}

func (a *App) handleGetDelivery(w http.ResponseWriter, r *http.Request) {
	g, ok := a.requireGrant(w, r)
	if !ok {
		return
	}
	d, err := a.store.deliveryByID(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "delivery not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load delivery")
		return
	}
	if d.GrantID != g.ID {
		writeError(w, http.StatusForbidden, "grant does not belong to this delivery")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (a *App) handleStats(w http.ResponseWriter, _ *http.Request) {
	stats, err := a.store.stats(a.ctx.IntegrationFor("ios_provider") != nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load push statistics")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (a *App) handleListDevices(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.listDevices(queryLimit(r, 50))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load devices")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": items})
}

func (a *App) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.listDeliveries(queryLimit(r, 50))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load deliveries")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": items})
}

func (a *App) requireGrant(w http.ResponseWriter, r *http.Request) (*grant, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "push grant required")
		return nil, false
	}
	g, err := a.store.authorizeGrant(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired push grant")
		return nil, false
	}
	return g, true
}

func validDeliveryType(value string) bool {
	return value == "approval" || value == "alert" || value == "report" || value == "test"
}

func queryLimit(r *http.Request, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || value < 1 {
		return fallback
	}
	if value > 100 {
		return 100
	}
	return value
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSONStatus(w, status, map[string]any{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	writeJSONStatus(w, status, value)
}

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
