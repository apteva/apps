package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const maxSessionTokenBytes = 16000

var errSessionUnavailable = errors.New("auth_service_unavailable")

type sessionRejection struct {
	code   string
	detail string
}

func (e sessionRejection) Error() string { return e.code }

type verifiedSession struct {
	org           *Organization
	user          *User
	expiresAt     time.Time
	authorization AuthorizationContext
}

func authenticatedBearerSession(w http.ResponseWriter, r *http.Request, app *sdk.AppCtx, pid string) (*verifiedSession, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		httpErr(w, http.StatusUnauthorized, "missing_bearer")
		return nil, false
	}
	session, err := verifySession(r.Context(), app, pid, token)
	if err == nil {
		return session, true
	}
	var rejection sessionRejection
	if errors.As(err, &rejection) {
		if rejection.detail != "" {
			httpStatus(w, http.StatusUnauthorized, map[string]string{"error": rejection.code, "detail": rejection.detail})
		} else {
			httpErr(w, http.StatusUnauthorized, rejection.code)
		}
	} else {
		httpErr(w, http.StatusInternalServerError, errSessionUnavailable.Error())
	}
	return nil, false
}

func verifySession(ctx context.Context, app *sdk.AppCtx, pid, token string) (*verifiedSession, error) {
	invalid := sessionRejection{code: "invalid_token"}
	if len(token) > maxSessionTokenBytes {
		return nil, invalid
	}
	// The unverified org claim selects the key set within the trusted project.
	slug, err := peekJWTOrg(token)
	if err != nil {
		return nil, sessionRejection{code: invalid.code, detail: err.Error()}
	}
	if slug == "" {
		slug = "default" // Legacy tokens without an org claim.
	}

	base := platformBaseURL(app, nil)
	db := app.AppReadDB()
	if base == "" || db == nil {
		return nil, errSessionUnavailable
	}
	// The session, authorization version, and permissions must come from
	// one snapshot, including when a role change races this verification.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	org, err := dbGetOrgBySlug(tx, pid, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sessionRejection{code: invalid.code, detail: "unknown org"}
	}
	if err != nil {
		return nil, err
	}
	keys, err := dbAllSigningKeys(tx, pid, org.ID)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 && org.Status == "active" {
		return nil, errSessionUnavailable
	}
	claims, err := jwtVerify(token, func(kid string) (ed25519.PublicKey, bool) {
		key, ok := keys[kid]
		return key, ok
	})
	if err != nil {
		return nil, sessionRejection{code: invalid.code, detail: err.Error()}
	}
	sub, _ := claims["sub"].(string)
	uid, ok := parseUint(sub)
	if !ok {
		return nil, invalid
	}
	user, err := dbGetUserByID(tx, pid, org.ID, uid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, invalid
	}
	if err != nil {
		return nil, err
	}
	if user.Status != "active" {
		return nil, sessionRejection{code: "user_inactive"}
	}
	if org.Status != "active" {
		return nil, sessionRejection{code: "organization_inactive"}
	}
	cid, _ := claims["azp"].(string)
	sid, _ := claims["sid"].(string)
	if sid == "" || claims["token_use"] != "access" || claims["iss"] != orgIssuerURL(base, org.Slug) {
		return nil, invalid
	}
	client, err := dbGetClientByClientID(tx, pid, cid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, invalid
	}
	if err != nil {
		return nil, err
	}
	audience := client.JWTAudience
	if audience == "" {
		audience = cid
	}
	if claims["aud"] != audience {
		return nil, invalid
	}
	if err := sessionEligibility(app, org, user, client); err != nil {
		var rejection sessionRejection
		if errors.As(err, &rejection) {
			return nil, invalid
		}
		return nil, err
	}
	var active int
	err = tx.QueryRow(`SELECT COUNT(*) FROM auth_session_families WHERE id=? AND project_id=? AND organization_id=? AND user_id=? AND client_id=? AND revoked_at IS NULL AND expires_at>?`, sid, pid, org.ID, uid, cid, rfc3339(time.Now())).Scan(&active)
	if err != nil {
		return nil, err
	}
	if active != 1 {
		return nil, sessionRejection{code: "session_revoked"}
	}
	version, ok := jwtInt64Claim(claims, "authorization_version")
	if !ok || version != user.AuthorizationVersion {
		return nil, sessionRejection{code: "stale_authorization", detail: "refresh the session to receive current roles and permissions"}
	}
	authorization, err := dbAuthorizationContext(tx, pid, org, uid)
	if err != nil {
		return nil, err
	}
	exp, _ := jwtInt64Claim(claims, "exp") // Required and checked by jwtVerify.
	return &verifiedSession{org: org, user: user, expiresAt: time.Unix(exp, 0), authorization: authorization}, nil
}

func (a *App) toolVerifySession(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller := sdk.CallerFrom(ctx)
	if caller == nil || caller.AppInstallID <= 0 || caller.AppName == "" || caller.ProjectID == "" {
		return nil, errors.New("bound app identity and project scope required")
	}
	pid := caller.ProjectID
	if pinned := envProject(); pinned != "" && pinned != pid {
		return nil, errors.New("project does not match install")
	}
	for _, key := range []string{"project_id", "_project_id"} {
		if value, exists := args[key]; exists && value != pid {
			return nil, errors.New("project does not match authenticated scope")
		}
	}

	rejected := map[string]any{"authenticated": false}
	credential, _ := args["credential"].(map[string]any)
	token, _ := credential["token"].(string)
	if credential["type"] != "bearer" || token == "" {
		return rejected, nil
	}
	session, err := verifySession(ctx, app, pid, token)
	if err != nil {
		var rejection sessionRejection
		if errors.As(err, &rejection) {
			return rejected, nil
		}
		return nil, errSessionUnavailable
	}
	return map[string]any{
		"authenticated": true,
		"expires_at":    rfc3339(session.expiresAt),
		"principal": map[string]any{
			"issuer":     "apteva:auth:" + session.org.Slug,
			"subject":    uintToStr(session.user.ID),
			"project_id": pid,
			"tenant_id":  session.org.Slug,
			"claims":     session.authorization,
		},
	}, nil
}
