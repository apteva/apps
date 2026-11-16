package main

// control_key.go — tenant_rotate_control_key.
//
// Fleet reaches every tenant with one admin `sk-` key (the "control key").
// If that key leaks (tenant_reveal_api_key, a compromised operator), the
// only fix that works on every tenant version is to replace it on the
// tenant and revoke the old one. Rotation is ordered so that a crash at
// any step leaves Fleet holding a key that works, and re-running the tool
// finishes the job instead of minting yet another key:
//
//  1. mint a new key on the tenant with the current key, and seal it into
//     pending_key_* before anything else (a crash here leaves an extra,
//     unused key that the next run verifies and adopts);
//  2. verify the new key authenticates as the same tenant user;
//  3. swap it in as the live key, remembering the old key's prefix in
//     revoke_key_prefix (one transaction);
//  4. revoke the old key, found by its prefix, using the new key; then
//     confirm the old key is rejected and clear revoke_key_prefix.
//
// Steps 1-3 never revoke anything, so Fleet can always still reach the
// tenant. The new key is minted with the "security.containment" scope so a
// tenant whose server supports containment-scoped keys accepts it for
// POST /api/security/containment/revoke (see tenant_contain); older servers
// store the scope and ignore it. Servers that gate privileged scopes refuse
// to let an unscoped key grant it; rotation then mints an unscoped key, and
// the tenant needs one scoped key from a reauthenticated session (Fleet's
// auto-setup mints it that way) before tenant_contain can confirm.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// controlKeyScope is the key scope tenant servers accept for incident
// containment from an API key (and for nothing else privileged).
const controlKeyScope = "security.containment"

// controlKeyName is the tenant-side label of keys Fleet mints for itself.
const controlKeyName = "fleet-control"

// tenantHTTPError is a non-2xx answer from a tenant, keeping the status so
// callers can tell "endpoint missing" (404) from "refused" (401/403).
type tenantHTTPError struct {
	Status int
	Body   string
}

func (e *tenantHTTPError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 300 {
		body = body[:300] + "…"
	}
	return fmt.Sprintf("tenant returned %d: %s", e.Status, body)
}

func tenantHTTPStatus(err error) int {
	var he *tenantHTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// tenantCall is tenantJSON with a typed status error.
func tenantCall(ctx context.Context, baseURL, apiKey, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, tenantControlMaxBody))
	if resp.StatusCode >= 300 {
		return &tenantHTTPError{Status: resp.StatusCode, Body: string(raw)}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// keyPrefixOf mirrors apteva-server: the stored prefix is the first 11
// characters ("sk-" + 8 hex) of the raw key.
func keyPrefixOf(key string) string {
	key = strings.TrimSpace(key)
	if len(key) < 11 {
		return key
	}
	return key[:11]
}

// ── store ──────────────────────────────────────────────────────────────────

type controlKeyState struct {
	KeyID         int64
	KeyPrefix     string
	PendingEnc    []byte
	PendingID     int64
	PendingPrefix string
	RevokePrefix  string
	RotatedAt     sql.NullTime
}

func (s *store) controlKeyState(id string) (controlKeyState, error) {
	var st controlKeyState
	err := s.db.QueryRow(`SELECT api_key_id, api_key_prefix, pending_key_enc, pending_key_id, pending_key_prefix, revoke_key_prefix, key_rotated_at
		FROM fleet_tenants WHERE id=?`, id).Scan(&st.KeyID, &st.KeyPrefix, &st.PendingEnc, &st.PendingID, &st.PendingPrefix, &st.RevokePrefix, &st.RotatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNotFound
	}
	return st, err
}

func (s *store) savePendingControlKey(id string, enc []byte, keyID int64, prefix string) error {
	_, err := s.db.Exec(`UPDATE fleet_tenants SET pending_key_enc=?, pending_key_id=?, pending_key_prefix=? WHERE id=?`, enc, keyID, prefix, id)
	return err
}

func (s *store) clearPendingControlKey(id string) error {
	_, err := s.db.Exec(`UPDATE fleet_tenants SET pending_key_enc=NULL, pending_key_id=0, pending_key_prefix='' WHERE id=?`, id)
	return err
}

// promotePendingControlKey makes the pending key live and records the
// previous key's prefix for revocation, atomically.
func (s *store) promotePendingControlKey(id, revokePrefix string) error {
	res, err := s.db.Exec(`UPDATE fleet_tenants SET
			api_key_enc=pending_key_enc, api_key_id=pending_key_id, api_key_prefix=pending_key_prefix,
			pending_key_enc=NULL, pending_key_id=0, pending_key_prefix='',
			revoke_key_prefix=?, key_rotated_at=?, updated_at=?
		WHERE id=? AND pending_key_enc IS NOT NULL`, revokePrefix, time.Now().UTC(), time.Now().UTC(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("no pending control key to promote")
	}
	return nil
}

func (s *store) clearRevokePrefix(id string) error {
	_, err := s.db.Exec(`UPDATE fleet_tenants SET revoke_key_prefix='' WHERE id=?`, id)
	return err
}

// ── rotation ───────────────────────────────────────────────────────────────

type keyRotationResult struct {
	TenantID       string   `json:"tenant_id"`
	Slug           string   `json:"slug,omitempty"`
	Status         string   `json:"status"` // rotated | resumed | failed
	NewKeyID       int64    `json:"new_key_id,omitempty"`
	NewKeyPrefix   string   `json:"new_key_prefix,omitempty"`
	OldKeyPrefix   string   `json:"old_key_prefix,omitempty"`
	OldKeyRevoked  bool     `json:"old_key_revoked"`
	OldKeyRejected *bool    `json:"old_key_rejected,omitempty"`
	ContainScope   *bool    `json:"containment_scope_granted,omitempty"` // nil when resuming (unknown)
	Steps          []string `json:"steps"`
	Error          string   `json:"error,omitempty"`
}

type tenantMe struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
}

type tenantKeyRecord struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	KeyPrefix string `json:"key_prefix"`
	RevokedAt string `json:"revoked_at,omitempty"`
}

func (a *App) toolRotateControlKey(appCtx *sdk.AppCtx, args map[string]any) (any, error) {
	id := strings.TrimSpace(getStr(args, "tenant_id"))
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	done, err := a.beginTenantOperation(id, "rotate control key")
	if err != nil {
		return nil, err
	}
	defer done()
	res, err := a.rotateControlKey(appCtx, id, actorOf(args), strings.TrimSpace(getStr(args, "reason")))
	if err != nil {
		return res, err
	}
	return res, nil
}

// rotateControlKey runs (or resumes) a rotation. The caller must hold the
// tenant operation lock. The returned result is non-nil even on error so
// callers can report how far it got.
func (a *App) rotateControlKey(appCtx *sdk.AppCtx, tenantID, actor, reason string) (*keyRotationResult, error) {
	res := &keyRotationResult{TenantID: tenantID, Status: "failed", Steps: []string{}}
	fail := func(err error) (*keyRotationResult, error) {
		res.Error = err.Error()
		_ = a.store.recordEvent(tenantID, "control_key_rotation_failed", actor, map[string]any{"error": res.Error, "steps": res.Steps, "reason": reason})
		return res, err
	}
	t, enc, err := a.store.get(tenantID)
	if err != nil {
		return res, err
	}
	res.Slug = t.Slug
	if t.Status == StatusSetupPending {
		return fail(errors.New("tenant is setup_pending; it has no control key yet"))
	}
	baseURL, err := a.internalTenantBaseURL(appCtx, t)
	if err != nil {
		return fail(fmt.Errorf("open tenant control channel: %w", err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	raw, err := a.keys.open(enc)
	if err != nil {
		return fail(fmt.Errorf("decrypt control key: %w", err))
	}
	current := string(raw)
	st, err := a.store.controlKeyState(tenantID)
	if err != nil {
		return fail(err)
	}

	// Resume A: a key was minted but never swapped in.
	if len(st.PendingEnc) > 0 {
		pending, perr := a.keys.open(st.PendingEnc)
		var me tenantMe
		if perr == nil {
			perr = tenantCall(ctx, baseURL, string(pending), http.MethodGet, "/api/auth/me", nil, &me)
		}
		if perr == nil && me.ID > 0 {
			if err := a.store.promotePendingControlKey(tenantID, keyPrefixOf(current)); err != nil {
				return fail(fmt.Errorf("adopt pending key: %w", err))
			}
			res.Steps = append(res.Steps, "adopted key minted by an interrupted rotation")
			res.Status = "resumed"
			current = string(pending)
			res.NewKeyID, res.NewKeyPrefix = st.PendingID, st.PendingPrefix
		} else {
			_ = a.store.clearPendingControlKey(tenantID)
			res.Steps = append(res.Steps, "discarded unusable key from an interrupted rotation")
		}
		if st, err = a.store.controlKeyState(tenantID); err != nil {
			return fail(err)
		}
	}

	// Resume B: the swap happened but the old key was never revoked.
	if st.RevokePrefix != "" {
		res.OldKeyPrefix = st.RevokePrefix
		if res.NewKeyID == 0 {
			res.NewKeyID, res.NewKeyPrefix = st.KeyID, st.KeyPrefix
		}
		if err := a.revokeKeyByPrefix(ctx, baseURL, current, st.RevokePrefix, st.KeyID); err != nil {
			return fail(fmt.Errorf("revoke previous control key %s: %w", st.RevokePrefix, err))
		}
		_ = a.store.clearRevokePrefix(tenantID)
		res.OldKeyRevoked = true
		res.Steps = append(res.Steps, "revoked previous key "+st.RevokePrefix)
		res.Status = "resumed"
		_ = a.store.recordEvent(tenantID, "control_key_rotated", actor, map[string]any{
			"resumed": true, "new_key_id": res.NewKeyID, "new_key_prefix": res.NewKeyPrefix,
			"old_key_prefix": res.OldKeyPrefix, "reason": reason,
		})
		return res, nil
	}
	if res.Status == "resumed" {
		// Adopted a pending key but there was nothing recorded to revoke —
		// cannot happen (promote always records the prefix) but stay safe.
		return res, nil
	}

	// Fresh rotation.
	var me tenantMe
	if err := tenantCall(ctx, baseURL, current, http.MethodGet, "/api/auth/me", nil, &me); err != nil {
		if s := tenantHTTPStatus(err); s == http.StatusUnauthorized || s == http.StatusForbidden {
			return fail(fmt.Errorf("the tenant rejected Fleet's control key (%d); attach a new admin key with tenant_attach_key", s))
		}
		return fail(fmt.Errorf("auth/me: %w", err))
	}
	if me.ID == 0 {
		return fail(errors.New("auth/me returned no user id"))
	}
	res.OldKeyPrefix = keyPrefixOf(current)

	var minted struct {
		ID     int64  `json:"id"`
		Key    string `json:"key"`
		Prefix string `json:"prefix"`
	}
	body := map[string]any{
		"name":   controlKeyName + " " + time.Now().UTC().Format("2006-01-02T15:04Z"),
		"scopes": []string{controlKeyScope},
	}
	granted := true
	res.ContainScope = &granted
	if err := tenantCall(ctx, baseURL, current, http.MethodPost, "/api/auth/keys", body, &minted); err != nil {
		if tenantHTTPStatus(err) != http.StatusForbidden {
			return fail(fmt.Errorf("mint new key: %w", err))
		}
		// Servers that gate privileged scopes only let a key that already
		// carries the scope (or a reauthenticated session) grant it. An
		// unscoped control key still rotates; it just cannot contain.
		delete(body, "scopes")
		granted = false
		res.Steps = append(res.Steps, "tenant refused the "+controlKeyScope+" scope for this key; minting an unscoped key")
		if err := tenantCall(ctx, baseURL, current, http.MethodPost, "/api/auth/keys", body, &minted); err != nil {
			return fail(fmt.Errorf("mint new key: %w", err))
		}
	}
	if !strings.HasPrefix(minted.Key, "sk-") || minted.ID == 0 {
		return fail(errors.New("tenant did not return a private key with an id"))
	}
	if minted.Prefix == "" {
		minted.Prefix = keyPrefixOf(minted.Key)
	}
	sealed, err := a.keys.seal([]byte(minted.Key))
	if err != nil {
		return fail(fmt.Errorf("seal new key: %w", err))
	}
	if err := a.store.savePendingControlKey(tenantID, sealed, minted.ID, minted.Prefix); err != nil {
		return fail(fmt.Errorf("store new key: %w", err))
	}
	res.Steps = append(res.Steps, "minted key "+minted.Prefix)

	var meNew tenantMe
	if err := tenantCall(ctx, baseURL, minted.Key, http.MethodGet, "/api/auth/me", nil, &meNew); err != nil || meNew.ID != me.ID {
		// The new key is unusable: delete it with the old one and keep the old.
		_ = tenantCall(ctx, baseURL, current, http.MethodDelete, "/api/auth/keys/"+strconv.FormatInt(minted.ID, 10), nil, nil)
		_ = a.store.clearPendingControlKey(tenantID)
		if err == nil {
			err = fmt.Errorf("new key authenticated as user %d, expected %d", meNew.ID, me.ID)
		}
		return fail(fmt.Errorf("verify new key: %w", err))
	}
	res.Steps = append(res.Steps, "verified new key")

	if err := a.store.promotePendingControlKey(tenantID, res.OldKeyPrefix); err != nil {
		return fail(fmt.Errorf("swap in new key: %w", err))
	}
	res.NewKeyID, res.NewKeyPrefix = minted.ID, minted.Prefix
	res.Steps = append(res.Steps, "swapped in new key")

	if err := a.revokeKeyByPrefix(ctx, baseURL, minted.Key, res.OldKeyPrefix, minted.ID); err != nil {
		// Fleet holds the new key; a re-run finishes the revocation.
		return fail(fmt.Errorf("new key is live but revoking the old key failed (re-run tenant_rotate_control_key to finish): %w", err))
	}
	_ = a.store.clearRevokePrefix(tenantID)
	res.OldKeyRevoked = true
	res.Steps = append(res.Steps, "revoked old key "+res.OldKeyPrefix)

	oldErr := tenantCall(ctx, baseURL, current, http.MethodGet, "/api/auth/me", nil, nil)
	rejected := tenantHTTPStatus(oldErr) == http.StatusUnauthorized || tenantHTTPStatus(oldErr) == http.StatusForbidden
	res.OldKeyRejected = &rejected
	res.Status = "rotated"
	_ = a.store.recordEvent(tenantID, "control_key_rotated", actor, map[string]any{
		"new_key_id": res.NewKeyID, "new_key_prefix": res.NewKeyPrefix,
		"old_key_prefix": res.OldKeyPrefix, "old_key_rejected": rejected, "reason": reason,
		"containment_scope_granted": granted,
	})
	if !rejected {
		return res, fmt.Errorf("old key %s still authenticates after revocation", res.OldKeyPrefix)
	}
	return res, nil
}

// revokeKeyByPrefix deletes the tenant key with the given prefix (other
// than keepID), then confirms it no longer appears among active keys.
// Finding none counts as already revoked.
func (a *App) revokeKeyByPrefix(ctx context.Context, baseURL, apiKey, prefix string, keepID int64) error {
	find := func() ([]tenantKeyRecord, error) {
		var keys []tenantKeyRecord
		if err := tenantCall(ctx, baseURL, apiKey, http.MethodGet, "/api/auth/keys", nil, &keys); err != nil {
			return nil, err
		}
		var out []tenantKeyRecord
		for _, k := range keys {
			if k.KeyPrefix == prefix && k.ID != keepID && k.RevokedAt == "" {
				out = append(out, k)
			}
		}
		return out, nil
	}
	matches, err := find()
	if err != nil {
		return fmt.Errorf("list keys: %w", err)
	}
	if len(matches) > 1 {
		return fmt.Errorf("%d active keys share prefix %s; refusing to guess which to revoke", len(matches), prefix)
	}
	for _, k := range matches {
		if err := tenantCall(ctx, baseURL, apiKey, http.MethodDelete, "/api/auth/keys/"+strconv.FormatInt(k.ID, 10), nil, nil); err != nil {
			return fmt.Errorf("delete key %d: %w", k.ID, err)
		}
	}
	left, err := find()
	if err != nil {
		return fmt.Errorf("re-list keys: %w", err)
	}
	if len(left) > 0 {
		return fmt.Errorf("key %s is still active after delete", prefix)
	}
	return nil
}
