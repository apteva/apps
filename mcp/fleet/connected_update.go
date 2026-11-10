package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const connectedUpdatePath = "/api/fleet-platform-update"
const connectedSubmitWindow = 15 * time.Minute
const connectedSubmitLease = 90 * time.Second

var connectedUpdateKey = regexp.MustCompile(`^fup_[a-f0-9]{64}$`)
var connectedHTTP = &http.Client{
	Timeout: 60 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type connectedUpdate struct {
	grant       []byte
	actor       string
	requestedAt int64
	RequestID   string `json:"request_id"`
	Version     string `json:"version"`
	AgentPolicy string `json:"agent_policy"`
	JobID       string `json:"job_id,omitempty"`
	Phase       string `json:"phase"`
	Notice      string `json:"notice,omitempty"`
	Done        bool   `json:"done"`
}

type connectedRemoteJob struct {
	ID         string    `json:"id"`
	State      string    `json:"state"`
	Target     string    `json:"target_version"`
	Previous   string    `json:"previous_version"`
	Reconciled string    `json:"reconciled_version"`
	Updated    time.Time `json:"updated_at"`
	Request    struct {
		ID     string `json:"request_id"`
		Policy string `json:"agent_policy"`
	} `json:"request"`
}

type connectedRemoteUpdate struct {
	Supported bool                `json:"supported"`
	Version   string              `json:"available_version,omitempty"`
	Reason    string              `json:"reason,omitempty"`
	Code      string              `json:"code,omitempty"`
	Job       *connectedRemoteJob `json:"job,omitempty"`
}

func (s *store) connectedUpdate(id string) (*connectedUpdate, error) {
	var op connectedUpdate
	err := s.db.QueryRow(`SELECT grant_enc, actor, requested_at, request_id, version,
		agent_policy, job_id, phase, notice, done FROM fleet_connected_updates WHERE tenant_id=?`,
		id).Scan(&op.grant, &op.actor, &op.requestedAt, &op.RequestID, &op.Version,
		&op.AgentPolicy, &op.JobID, &op.Phase, &op.Notice, &op.Done)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &op, err
}

func connectedUpdateURL(t *Tenant) (string, error) {
	base, err := url.Parse(t.BaseURL)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil ||
		base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.Opaque != "" {
		return "", errors.New("Fleet updates require the connected tenant's valid HTTPS URL")
	}
	return strings.TrimRight(base.String(), "/"), nil
}

func connectedRequest(ctx context.Context, method, target, token string, body []byte, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("invalid connected tenant update request")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := connectedHTTP.Do(req)
	if err != nil {
		return 0, errors.New("cannot reach the tenant over verified HTTPS")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusConflict:
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
		if readErr != nil || len(data) > 64<<10 || json.Unmarshal(data, out) != nil {
			return resp.StatusCode, errors.New("tenant returned an invalid update response")
		}
		return resp.StatusCode, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return resp.StatusCode, errors.New("update access was revoked or the credential is invalid")
	case http.StatusNotFound:
		return resp.StatusCode, errors.New("the tenant does not support Fleet updates; update it locally first")
	default:
		return resp.StatusCode, fmt.Errorf("tenant updater returned HTTP %d", resp.StatusCode)
	}
}

func connectedAvailability(ctx context.Context, base, token string) (connectedRemoteUpdate, error) {
	var remote connectedRemoteUpdate
	status, err := connectedRequest(ctx, http.MethodGet, base+connectedUpdatePath, token, nil, &remote)
	if err == nil && status != http.StatusOK {
		err = errors.New("could not check update support")
	}
	return remote, err
}

func (a *App) httpConnectedUpdate(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Cache-Control", "no-store")
	// The parent proxy scrubs client-supplied identity headers and sets
	// Operator-ID for private operator sessions/keys, not agents or app calls.
	operator := r.Header.Get("X-Apteva-Operator-ID")
	uid, _ := strconv.ParseInt(operator, 10, 64)
	if uid <= 0 || operator != r.Header.Get("X-User-ID") || r.Header.Get("X-Apteva-Subject-Type") != "" {
		writeJSONErr(w, http.StatusForbidden, errors.New("an authenticated Fleet operator is required"))
		return
	}
	t, _, err := a.store.get(id)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, errors.New("tenant not found"))
		return
	}
	if t.Kind != KindRemote || t.InstanceID != 0 {
		writeJSONErr(w, http.StatusBadRequest, errors.New("this updater is for connected remote tenants"))
		return
	}
	op, err := a.store.connectedUpdate(id)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, errors.New("cannot read Fleet update state"))
		return
	}
	base, urlErr := connectedUpdateURL(t)
	if r.Method == http.MethodGet {
		view := map[string]any{"configured": op != nil, "current_version": t.CurrentVersion}
		if op != nil && op.RequestID != "" {
			view["operation"] = op
			view["retryable"] = !op.Done && op.JobID == "" && time.Since(time.Unix(op.requestedAt, 0)) >= connectedSubmitWindow
		}
		if urlErr != nil {
			view["reason"] = urlErr.Error()
		} else if op != nil && op.Done {
			// Polling job progress should only read Fleet's persisted state.
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			key, keyErr := a.keys.open(op.grant)
			if keyErr != nil {
				view["reason"] = "Cannot open the update credential. Replace it in Fleet."
			} else if remote, err := connectedAvailability(ctx, base, string(key)); err != nil {
				view["reason"] = err.Error()
			} else {
				view["supported"], view["available_version"], view["reason"] = remote.Supported, remote.Version, remote.Reason
			}
		}
		writeJSON(w, http.StatusOK, view)
		return
	}
	if urlErr != nil {
		writeJSONErr(w, http.StatusBadRequest, urlErr)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	switch r.Method {
	case http.MethodPut:
		var req struct {
			Credential string `json:"credential"`
		}
		if decoder.Decode(&req) != nil || !connectedUpdateKey.MatchString(req.Credential) {
			writeJSONErr(w, http.StatusBadRequest, errors.New("enter a valid Fleet update credential"))
			return
		}
		if _, err := connectedAvailability(r.Context(), base, req.Credential); err != nil {
			writeJSONErr(w, http.StatusBadGateway, err)
			return
		}
		sealed, err := a.keys.seal([]byte(req.Credential))
		if err == nil {
			_, err = a.store.db.Exec(`INSERT INTO fleet_connected_updates (tenant_id,grant_enc)
				VALUES (?,?) ON CONFLICT(tenant_id) DO UPDATE SET grant_enc=excluded.grant_enc`, id, sealed)
		}
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, errors.New("could not save the update credential"))
			return
		}
		_ = a.store.recordEvent(id, "connected_update_access_saved", "user:"+operator, nil)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost:
		var req struct {
			Version string `json:"version"`
			Policy  string `json:"agent_policy"`
			Confirm bool   `json:"confirm"`
		}
		if decoder.Decode(&req) != nil || !req.Confirm || req.Version == "" ||
			(req.Policy != "preserve" && req.Policy != "rolling" && req.Policy != "restart") {
			writeJSONErr(w, http.StatusBadRequest, errors.New("confirm the exact version and agent policy"))
			return
		}
		if op == nil || !op.Done || t.Status != StatusActive {
			writeJSONErr(w, http.StatusConflict, errors.New("an active tenant with update access and no pending update is required"))
			return
		}
		key, err := a.keys.open(op.grant)
		if err != nil {
			writeJSONErr(w, http.StatusConflict, errors.New("replace the update credential before updating"))
			return
		}
		remote, err := connectedAvailability(r.Context(), base, string(key))
		if err != nil {
			writeJSONErr(w, http.StatusBadGateway, err)
			return
		}
		if !remote.Supported || remote.Version != req.Version {
			writeJSONErr(w, http.StatusConflict, errors.New("the requested release is unavailable; check updates again"))
			return
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, errors.New("cannot generate update request ID"))
			return
		}
		requestID := hex.EncodeToString(random[:])
		if err := a.queueConnectedUpdate(id, requestID, req.Version, req.Policy, "user:"+operator); err != nil {
			writeJSONErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"request_id": requestID})
	case http.MethodPatch:
		var req struct {
			RequestID string `json:"request_id"`
			Confirm   bool   `json:"confirm"`
		}
		if decoder.Decode(&req) != nil || !req.Confirm || req.RequestID == "" {
			writeJSONErr(w, http.StatusBadRequest, errors.New("confirm the request to retry"))
			return
		}
		result, err := a.store.db.Exec(`UPDATE fleet_connected_updates SET requested_at=?, notice=''
			WHERE tenant_id=? AND request_id=? AND job_id='' AND done=0 AND requested_at<=?`,
			time.Now().Unix(), id, req.RequestID, time.Now().Add(-connectedSubmitWindow).Unix())
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, errors.New("cannot retry the request"))
			return
		}
		if n, _ := result.RowsAffected(); n != 1 {
			writeJSONErr(w, http.StatusConflict, errors.New("this request cannot be retried"))
			return
		}
		_ = a.store.recordEvent(id, "connected_update_retried", "user:"+operator, map[string]string{"request_id": req.RequestID})
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, POST, PATCH")
		writeJSONErr(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (a *App) queueConnectedUpdate(id, requestID, version, policy, actor string) error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE fleet_connected_updates SET request_id=?, version=?, agent_policy=?,
		actor=?, job_id='', phase='queued', notice='', requested_at=?, attempted_at=0, done=0 WHERE tenant_id=? AND done=1
		AND EXISTS (SELECT 1 FROM fleet_tenants WHERE id=? AND status='active')`,
		requestID, version, policy, actor, time.Now().Unix(), id, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("another update is already pending")
	}
	data, _ := json.Marshal(map[string]string{"request_id": requestID, "target_version": version, "agent_policy": policy})
	if _, err := tx.Exec(`INSERT INTO fleet_events (tenant_id,kind,actor,payload) VALUES (?,'connected_update_requested',?,?)`,
		id, actor, string(data)); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) runConnectedUpdates(ctx context.Context, _ *sdk.AppCtx) error {
	if !a.connectedUpdateMu.TryLock() {
		return nil
	}
	defer a.connectedUpdateMu.Unlock()
	rows, err := a.store.db.QueryContext(ctx, `SELECT tenant_id FROM fleet_connected_updates WHERE done=0 ORDER BY requested_at`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.reconcileConnectedUpdate(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) reconcileConnectedUpdate(ctx context.Context, id string) error {
	op, err := a.store.connectedUpdate(id)
	if err != nil || op == nil || op.Done {
		return err
	}
	t, _, err := a.store.get(id)
	if err != nil {
		return err
	}
	notice := func(message string) error {
		_, err := a.store.db.Exec(`UPDATE fleet_connected_updates SET notice=?
			WHERE tenant_id=? AND request_id=? AND done=0 AND notice<>?`, message, id, op.RequestID, message)
		return err
	}
	base, err := connectedUpdateURL(t)
	if err != nil {
		return notice(err.Error())
	}
	key, err := a.keys.open(op.grant)
	if err != nil {
		return notice("Cannot open the update credential. Replace it in Fleet.")
	}
	var remote connectedRemoteUpdate
	status, err := connectedRequest(ctx, http.MethodGet, base+connectedUpdatePath+"?request_id="+url.QueryEscape(op.RequestID), string(key), nil, &remote)
	if err != nil || status != http.StatusOK {
		if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound {
			return notice(err.Error())
		}
		return notice("Reconnecting to the tenant to verify update status.")
	}
	if remote.Job == nil {
		if op.JobID != "" {
			return notice("The tenant could not identify the accepted job. Inspect the updater on the host.")
		}
		if time.Since(time.Unix(op.requestedAt, 0)) >= connectedSubmitWindow {
			return notice("Submission unverified. Retry the same request or run apteva update fleet-grant reconcile " + op.RequestID + " on the client server.")
		}
		first := op.Phase == "queued"
		now := time.Now()
		result, err := a.store.db.Exec(`UPDATE fleet_connected_updates SET phase='submitting', attempted_at=?
			WHERE tenant_id=? AND request_id=? AND phase=? AND job_id='' AND done=0 AND attempted_at<=?`,
			now.Unix(), id, op.RequestID, op.Phase, now.Add(-connectedSubmitLease).Unix())
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return nil
		}
		body, _ := json.Marshal(map[string]string{"request_id": op.RequestID, "version": op.Version, "agent_policy": op.AgentPolicy})
		status, err := connectedRequest(ctx, http.MethodPost, base+connectedUpdatePath, string(key), body, &remote)
		if status == http.StatusConflict && err == nil && remote.Code == "not_submitted" {
			if first {
				return a.finishConnectedUpdate(id, op, "rejected", "", remote.Reason)
			}
			return notice("Submission unverified. " + remote.Reason)
		}
		if err != nil || status != http.StatusAccepted || remote.Job == nil {
			return notice("Reconnecting to confirm whether the tenant accepted the update.")
		}
	}
	job := remote.Job
	if job.ID == "" || (op.JobID != "" && job.ID != op.JobID) || job.Request.ID != op.RequestID {
		return notice("The tenant returned a different update job. Inspect the updater on the host.")
	}
	if job.State == "canceled" && op.JobID == "" {
		op.JobID = job.ID
		return a.finishConnectedUpdate(id, op, "canceled", "", "The host operator closed the request before an update was accepted.")
	}
	if job.Target != op.Version || job.Request.Policy != op.AgentPolicy {
		return notice("The tenant returned a different update job. Inspect the updater on the host.")
	}
	if _, err := a.store.db.Exec(`UPDATE fleet_connected_updates SET job_id=?, phase=?, notice=''
		WHERE tenant_id=? AND request_id=? AND done=0 AND (job_id<>? OR phase<>?)`,
		job.ID, job.State, id, op.RequestID, job.ID, job.State); err != nil {
		return err
	}
	switch job.State {
	case "recovery_required":
		return notice("Recover Apteva on the client server, then run apteva update fleet-grant reconcile " + op.RequestID + ".")
	case "succeeded", "rolled_back", "failed", "reconciled":
		expected := job.Previous
		if job.State == "succeeded" {
			expected = op.Version
		}
		if job.State == "reconciled" {
			if job.Reconciled != op.Version && job.Reconciled != job.Previous {
				return notice("The recovery result did not identify the requested or previous release.")
			}
			expected = job.Reconciled
		}
		var health struct {
			OK  bool   `json:"ok"`
			CLI string `json:"cli"`
		}
		status, err := connectedRequest(ctx, http.MethodGet, base+"/api/health", "", nil, &health)
		if expected == "" || err != nil || status != http.StatusOK || !health.OK || strings.TrimPrefix(health.CLI, "v") != expected {
			return notice("Waiting to verify the exact healthy Apteva version.")
		}
		op.JobID = job.ID
		return a.finishConnectedUpdate(id, op, job.State, expected, "")
	default:
		if time.Since(job.Updated) > 2*time.Minute {
			return notice("The updater stopped reporting progress. Inspect platform-update.log, then run apteva update fleet-grant reconcile " + op.RequestID + " on the client server.")
		}
		return notice("")
	}
}

func (a *App) finishConnectedUpdate(id string, op *connectedUpdate, phase, version, notice string) error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE fleet_connected_updates SET phase=?, notice=?, job_id=?, done=1
		WHERE tenant_id=? AND request_id=? AND done=0`, phase, notice, op.JobID, id, op.RequestID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil
	}
	if version != "" {
		if _, err := tx.Exec(`UPDATE fleet_tenants SET current_version=?, last_seen_at=?, updated_at=? WHERE id=?`,
			version, time.Now().UTC(), time.Now().UTC(), id); err != nil {
			return err
		}
	}
	payload, _ := json.Marshal(map[string]string{"request_id": op.RequestID, "job_id": op.JobID,
		"target_version": op.Version, "verified_version": version, "outcome": phase})
	if _, err := tx.Exec(`INSERT INTO fleet_events (tenant_id,kind,actor,payload) VALUES (?,'connected_update_finished',?,?)`,
		id, op.actor, string(payload)); err != nil {
		return err
	}
	return tx.Commit()
}
