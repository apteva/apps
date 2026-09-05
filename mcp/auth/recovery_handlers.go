package main

import (
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type recoveryRequest struct {
	Email            string `json:"email"`
	Password         string `json:"password"`
	Token            string `json:"token"`
	ClientID         string `json:"client_id"`
	OrganizationSlug string `json:"organization_slug"`
	ContinueURL      string `json:"continue_url"`
}

func (a *App) handlePasswordResetRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx, pid, body, client, org, ok := recoveryContext(w, r)
	if !ok {
		return
	}
	if err := validateRecoveryContinueURL(client, body.ContinueURL); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := enqueueRecovery(ctx, pid, org, client, strings.ToLower(strings.TrimSpace(body.Email)), "reset_password", body.ContinueURL); err != nil {
		httpErr(w, 503, "recovery unavailable")
		return
	}
	// Always return the same response so this endpoint cannot enumerate users.
	httpStatus(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *App) handlePasswordResetConfirm(w http.ResponseWriter, r *http.Request) {
	a.confirmRecovery(w, r, "reset_password")
}
func (a *App) handleEmailVerify(w http.ResponseWriter, r *http.Request) {
	a.confirmRecovery(w, r, "verify_email")
}
func (a *App) confirmRecovery(w http.ResponseWriter, r *http.Request, kind string) {
	if r.Method != "POST" {
		httpErr(w, 405, "POST only")
		return
	}
	ctx, pid, body, client, org, ok := recoveryContext(w, r)
	if !ok {
		return
	}
	var hash string
	if kind == "reset_password" {
		if err := checkPasswordPolicy(ctx, org, body.Password); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		var err error
		hash, err = hashPassword(body.Password)
		if err != nil {
			httpErr(w, 503, "password_service_busy")
			return
		}
	}
	tx, err := beginAuthTx(ctx.AppDB(), pid, org.ID)
	if err != nil {
		httpErr(w, 500, "recovery_failed")
		return
	}
	defer tx.Rollback()
	uid, _, err := consumeVerificationTokenTx(tx, pid, hashToken(body.Token), org.ID, kind, client.ClientID)
	if err != nil {
		httpErr(w, 400, "link is invalid or expired")
		return
	}
	user, err := dbGetUserByID(tx, pid, org.ID, uid)
	currentOrg, orgErr := dbGetOrgByID(tx, pid, org.ID)
	currentClient, clientErr := dbGetClientByClientID(tx, pid, client.ClientID)
	if err != nil || orgErr != nil || clientErr != nil || user.Status != "active" || currentOrg.Status != "active" || currentClient.DisabledAt != "" {
		httpErr(w, 400, "link is invalid or expired")
		return
	}
	if kind == "reset_password" {
		if err = dbSetUserPassword(tx, pid, org.ID, uid, hash); err == nil {
			_, err = revokeUserState(tx, pid, org.ID, uid)
		}
		if err == nil {
			_, err = tx.Exec(`UPDATE users SET failed_login_count=0,locked_until=NULL WHERE project_id=? AND organization_id=? AND id=?`, pid, org.ID, uid)
		}
	}
	// Possession of either emailed credential proves ownership of the mailbox.
	if err == nil {
		err = dbMarkEmailVerified(tx, pid, org.ID, uid)
	}
	if err != nil {
		httpErr(w, 500, "recovery_failed")
		return
	}
	dbAudit(tx, pid, org.ID, &uid, client.ClientID, kind+"_completed", r.RemoteAddr, r.UserAgent(), nil)
	if err = tx.Commit(); err != nil {
		httpErr(w, 500, "recovery_failed")
		return
	}
	httpJSON(w, map[string]any{"ok": true, "login_required": true})
}

func (a *App) handleEmailVerificationResend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx, pid, body, client, org, ok := recoveryContext(w, r)
	if !ok {
		return
	}
	if err := validateRecoveryContinueURL(client, body.ContinueURL); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := enqueueRecovery(ctx, pid, org, client, strings.ToLower(strings.TrimSpace(body.Email)), "verify_email", body.ContinueURL); err != nil {
		httpErr(w, 503, "recovery unavailable")
		return
	}
	httpStatus(w, http.StatusAccepted, map[string]any{"ok": true})
}

func recoveryContext(w http.ResponseWriter, r *http.Request) (*sdk.AppCtx, string, recoveryRequest, *Client, *Organization, bool) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return nil, "", recoveryRequest{}, nil, nil, false
	}
	var body recoveryRequest
	if err := decodeRequest(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return nil, "", body, nil, nil, false
	}
	client, err := requireClient(ctx, pid, body.ClientID)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return nil, "", body, nil, nil, false
	}
	if err := requireRecoveryOrigin(ctx, client, r); err != nil {
		httpErr(w, http.StatusForbidden, "origin_not_allowed")
		return nil, "", body, nil, nil, false
	}
	org, err := resolveOrgForRequest(ctx, pid, client, body.OrganizationSlug)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return nil, "", body, nil, nil, false
	}
	if err := consumeRate(ctx.AppDB(), pid+":recovery:"+client.ClientID+":"+strings.ToLower(strings.TrimSpace(body.Email)), 20, time.Hour); err != nil {
		httpErr(w, 429, "rate_limited")
		return nil, "", body, nil, nil, false
	}
	return ctx, pid, body, client, org, true
}

func mintPublicAuthResponse(ctx *sdk.AppCtx, pid string, org *Organization, user *User, client *Client, r *http.Request) (map[string]any, error) {
	tokens, err := mintSession(ctx, pid, org, user, client, r)
	if err != nil {
		return nil, err
	}
	resp := map[string]any{
		"user": user, "authorization": tokens.authorization, "access_token": tokens.access,
		"refresh_token": tokens.refresh, "expires_in": tokens.expiresIn, "token_type": "Bearer",
	}
	delegated, err := mintAptevaDelegatedToken(pid, org, user, client)
	if err != nil {
		ctx.Logger().Warn("delegated user token mint failed", "err", err)
	} else if delegated != nil {
		resp["apteva_access_token"] = delegated.AccessToken
		resp["apteva_expires_in"] = delegated.ExpiresIn
	}
	return resp, nil
}
