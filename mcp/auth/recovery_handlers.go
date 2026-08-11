package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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
	user, err := dbGetUserByEmail(ctx.AppDB(), pid, org.ID, strings.ToLower(strings.TrimSpace(body.Email)))
	if err == nil && user != nil && user.Status == "active" {
		if sendErr := issueResetTokenForClient(ctx, pid, org, user.ID, user.Email, recoveryLinkOptions{
			ClientID: client.ClientID, ContinueURL: body.ContinueURL,
		}); sendErr != nil {
			ctx.Logger().Warn("password reset email failed", "err", sendErr)
		}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		ctx.Logger().Warn("password reset lookup failed", "err", err)
	}
	// Always return the same response so this endpoint cannot enumerate users.
	httpStatus(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *App) handlePasswordResetConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx, pid, body, client, org, ok := recoveryContext(w, r)
	if !ok {
		return
	}
	if reason := validatePassword(body.Password, cfgInt(ctx, "password_min_length", 8), cfgInt(ctx, "password_classes_required", 0)); reason != "" {
		httpErr(w, http.StatusBadRequest, reason)
		return
	}
	uid, _, err := dbConsumeVerificationToken(ctx.AppDB(), pid, hashToken(strings.TrimSpace(body.Token)), org.ID, "reset_password", client.ClientID)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "reset link is invalid or expired")
		return
	}
	if _, code, err := a.setUserPassword(ctx, pid, org.ID, uid, body.Password, true, "password_reset_completed", client.ClientID, r.RemoteAddr, r.UserAgent()); err != nil {
		httpErr(w, code, err.Error())
		return
	}
	user, err := dbGetUserByID(ctx.AppDB(), pid, org.ID, uid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp, err := mintPublicAuthResponse(ctx, pid, org, user, client, r)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, resp)
}

func (a *App) handleEmailVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx, pid, body, client, org, ok := recoveryContext(w, r)
	if !ok {
		return
	}
	uid, _, err := dbConsumeVerificationToken(ctx.AppDB(), pid, hashToken(strings.TrimSpace(body.Token)), org.ID, "verify_email", client.ClientID)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "verification link is invalid or expired")
		return
	}
	if err := dbMarkEmailVerified(ctx.AppDB(), pid, org.ID, uid); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := dbGetUserByID(ctx.AppDB(), pid, org.ID, uid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp, err := mintPublicAuthResponse(ctx, pid, org, user, client, r)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &uid, client.ClientID, "email_verified", r.RemoteAddr, r.UserAgent(), nil)
	httpJSON(w, resp)
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
	user, err := dbGetUserByEmail(ctx.AppDB(), pid, org.ID, strings.ToLower(strings.TrimSpace(body.Email)))
	if err == nil && user != nil && user.Status == "active" && user.EmailVerifiedAt == "" {
		if sendErr := issueVerifyEmailTokenForClient(ctx, pid, org, user.ID, user.Email, recoveryLinkOptions{
			ClientID: client.ClientID, ContinueURL: body.ContinueURL,
		}); sendErr != nil {
			ctx.Logger().Warn("verification email resend failed", "err", sendErr)
		}
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return nil, "", body, nil, nil, false
	}
	client, err := requireClient(ctx, pid, body.ClientID)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return nil, "", body, nil, nil, false
	}
	if err := requireAllowedOrigin(client, r.Header.Get("Origin")); err != nil {
		httpErr(w, http.StatusForbidden, "origin_not_allowed")
		return nil, "", body, nil, nil, false
	}
	org, err := resolveOrgForRequest(ctx, pid, client, body.OrganizationSlug)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
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
	delegated, err := mintAptevaDelegatedToken(ctx, pid, org, user)
	if err != nil {
		ctx.Logger().Warn("delegated user token mint failed", "err", err)
	} else if delegated != nil {
		resp["apteva_access_token"] = delegated.AccessToken
		resp["apteva_expires_in"] = delegated.ExpiresIn
	}
	return resp, nil
}
