package main

// identities.go — guest and external-identity accounts (v0.10.0).
//
// Games and other player-facing apps need "play before signup": an
// account keyed by a device id or a studio-issued custom id, upgradable
// to a full email account later, plus Steam / Game Center / Play Games
// style identities that the calling app has already verified.
//
// Auth does not verify provider assertions itself. The caller (a
// sibling app reached through the app-to-app bridge) proves possession
// of the device or validates the provider ticket, then hands auth the
// stable (provider, provider_user_id) pair. Auth owns the user row, the
// session, and the tokens — exactly as it does for email logins.
//
// Storage: identities live in oauth_identities (one row per provider
// subject, unique per org). Guest users carry kind='guest' and a
// synthetic, non-routable email under guest.invalid; scanUser blanks
// that placeholder so it never leaks into JSON or JWT claims.

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	userKindAccount = "account"
	userKindGuest   = "guest"

	guestEmailDomain     = "guest.invalid"
	identityCreatedEvent = "identity_created"
)

var providerRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

// Identity — one external subject bound to a user. provider is a
// lowercase slug chosen by the calling app (device, custom, steam,
// apple, google_play, …); provider_user_id is the provider's stable
// subject for that person, hashed by the caller when it is sensitive.
type Identity struct {
	ID             int64  `json:"id"`
	OrganizationID int64  `json:"organization_id"`
	UserID         int64  `json:"user_id"`
	Provider       string `json:"provider"`
	ProviderUserID string `json:"provider_user_id"`
	CreatedAt      string `json:"created_at,omitempty"`
	LastUsedAt     string `json:"last_used_at,omitempty"`
}

func userKindOrDefault(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return userKindAccount
	}
	return kind
}

// normalizeUserKind runs after every users row scan: defaults the
// kind and hides the guest placeholder email from callers.
func normalizeUserKind(u *User) {
	if u == nil {
		return
	}
	u.Kind = userKindOrDefault(u.Kind)
	if isGuestEmail(u.Email) {
		u.Email = ""
	}
}

func isGuestEmail(email string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(email)), "@"+guestEmailDomain)
}

func newGuestEmail() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "guest-" + hex.EncodeToString(b) + "@" + guestEmailDomain, nil
}

func validateProviderPair(provider, subject string) (string, string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	subject = strings.TrimSpace(subject)
	if !providerRe.MatchString(provider) {
		return "", "", errors.New("provider must be 2-32 lowercase letters, digits, or underscores")
	}
	if subject == "" || len(subject) > 256 {
		return "", "", errors.New("provider_user_id required (max 256 chars)")
	}
	return provider, subject, nil
}

// ─── DB access ───────────────────────────────────────────────────────

func dbFindIdentity(db *sql.DB, projectID string, orgID int64, provider, subject string) (*Identity, error) {
	row := db.QueryRow(`
		SELECT id, IFNULL(organization_id,0), user_id, provider, provider_user_id,
		       IFNULL(created_at,''), IFNULL(last_used_at,'')
		FROM oauth_identities
		WHERE project_id = ? AND organization_id = ? AND provider = ? AND provider_user_id = ?`,
		projectID, orgID, provider, subject)
	var i Identity
	if err := row.Scan(&i.ID, &i.OrganizationID, &i.UserID, &i.Provider, &i.ProviderUserID,
		&i.CreatedAt, &i.LastUsedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &i, nil
}

func dbInsertIdentity(db *sql.DB, projectID string, orgID, userID int64, provider, subject, rawProfile string) (int64, error) {
	res, err := db.Exec(`
		INSERT INTO oauth_identities(project_id, organization_id, user_id, provider, provider_user_id, raw_profile, last_used_at)
		VALUES(?,?,?,?,?,?,?)`,
		projectID, orgID, userID, provider, subject, nullStr(rawProfile), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbTouchIdentity(db *sql.DB, id int64) error {
	_, err := db.Exec(`UPDATE oauth_identities SET last_used_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func dbListIdentities(db *sql.DB, projectID string, orgID, userID int64) ([]Identity, error) {
	rows, err := db.Query(`
		SELECT id, IFNULL(organization_id,0), user_id, provider, provider_user_id,
		       IFNULL(created_at,''), IFNULL(last_used_at,'')
		FROM oauth_identities
		WHERE project_id = ? AND organization_id = ? AND user_id = ?
		ORDER BY created_at ASC, id ASC`,
		projectID, orgID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Identity{}
	for rows.Next() {
		var i Identity
		if err := rows.Scan(&i.ID, &i.OrganizationID, &i.UserID, &i.Provider, &i.ProviderUserID,
			&i.CreatedAt, &i.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// dbDeleteIdentities removes one identity (provider + subject) or every
// identity of a provider (subject == "") for a user.
func dbDeleteIdentities(db *sql.DB, projectID string, orgID, userID int64, provider, subject string) (int64, error) {
	q := `DELETE FROM oauth_identities WHERE project_id = ? AND organization_id = ? AND user_id = ? AND provider = ?`
	args := []any{projectID, orgID, userID, provider}
	if subject != "" {
		q += ` AND provider_user_id = ?`
		args = append(args, subject)
	}
	res, err := db.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func dbCreateGuestUser(db *sql.DB, projectID string, orgID int64, email, displayName, metadataJSON string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if strings.TrimSpace(metadataJSON) == "" {
		metadataJSON = "{}"
	}
	res, err := db.Exec(`
		INSERT INTO users(project_id, organization_id, email, password_hash, display_name, email_verified_at, metadata_json, status, kind, created_at, updated_at)
		VALUES(?, ?, ?, NULL, ?, NULL, ?, 'active', 'guest', ?, ?)`,
		projectID, orgID, strings.ToLower(strings.TrimSpace(email)), displayName, metadataJSON, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// dbCountClientEvents — audit rows for one client + event since a
// point in time. Backs the per-client creation rate limit.
func dbCountClientEvents(db *sql.DB, projectID, clientID, event string, since time.Time) (int, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE project_id = ? AND client_id = ? AND event = ? AND occurred_at > ?`,
		projectID, clientID, event, since.UTC().Format(time.RFC3339)).Scan(&n)
	return n, err
}

// dbUpgradeGuest turns a guest row into an account row in one statement.
// The WHERE kind = 'guest' guard makes a double upgrade a no-op error
// instead of a silent overwrite of a real account's credentials.
func dbUpgradeGuest(db *sql.DB, projectID string, orgID, userID int64, email, passwordHash string, emailVerified bool) error {
	now := time.Now().UTC().Format(time.RFC3339)
	verifiedAt := sql.NullString{}
	if emailVerified {
		verifiedAt = sql.NullString{Valid: true, String: now}
	}
	res, err := db.Exec(`
		UPDATE users
		   SET email = ?, password_hash = ?, kind = 'account', email_verified_at = ?, updated_at = ?
		 WHERE project_id = ? AND organization_id = ? AND id = ? AND kind = 'guest'`,
		strings.ToLower(strings.TrimSpace(email)), passwordHash, verifiedAt, now, projectID, orgID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("user is not a guest")
	}
	return nil
}

// ─── Tools ───────────────────────────────────────────────────────────

func (a *App) identityTools() []sdk.Tool {
	orgSelector := map[string]any{
		"organization_id":   map[string]any{"type": "integer"},
		"organization_slug": map[string]any{"type": "string"},
	}
	merge := func(base map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range orgSelector {
			out[k] = v
		}
		for k, v := range base {
			out[k] = v
		}
		return out
	}
	return []sdk.Tool{
		{
			Name:        "auth_public_login_identity",
			Description: "APP-TO-APP ONLY. Log a player or visitor in by an external identity the calling app has already verified (device possession, Steam ticket, Apple identity token) and mint a session. Auth trusts the (provider, provider_user_id) pair. Unknown identities create a guest user (kind=guest, no email, no password) unless create_if_missing=false. Creation is rate-limited per client by identity_signups_per_client_per_hour. Args: client_id, organization_slug (multi-org clients), provider (lowercase slug), provider_user_id, display_name, create_if_missing (default true), metadata (object, applied on create), ip, user_agent. Returns {user, created, access_token, refresh_token, expires_in, authorization, client_id, organization_slug}.",
			InputSchema: schemaObject(map[string]any{
				"client_id":         map[string]any{"type": "string"},
				"organization_slug": map[string]any{"type": "string"},
				"provider":          map[string]any{"type": "string"},
				"provider_user_id":  map[string]any{"type": "string"},
				"display_name":      map[string]any{"type": "string"},
				"create_if_missing": map[string]any{"type": "boolean"},
				"metadata":          map[string]any{"type": "object"},
				"ip":                map[string]any{"type": "string"},
				"user_agent":        map[string]any{"type": "string"},
			}, []string{"provider", "provider_user_id"}),
			Exposure: sdk.ToolExposureAppOnly,
			Handler:  a.toolPublicLoginIdentity,
		},
		{
			Name:        "auth_identities_link",
			Description: "Bind an external identity to an existing user. Requires organization_id/slug + user_id + provider + provider_user_id. The caller must have verified the assertion. Idempotent when the pair is already bound to the same user; errors when it belongs to another user. Optional raw_profile (object) is stored for reference.",
			InputSchema: schemaObject(merge(map[string]any{
				"user_id":          map[string]any{"type": "integer"},
				"provider":         map[string]any{"type": "string"},
				"provider_user_id": map[string]any{"type": "string"},
				"raw_profile":      map[string]any{"type": "object"},
			}), []string{"user_id", "provider", "provider_user_id"}),
			Handler: a.toolIdentitiesLink,
		},
		{
			Name:        "auth_identities_unlink",
			Description: "Remove an external identity from a user. Requires organization_id/slug + user_id + provider; provider_user_id narrows to one subject, otherwise every identity of that provider is removed. Refuses to strand a guest (no password, last identity) unless force=true.",
			InputSchema: schemaObject(merge(map[string]any{
				"user_id":          map[string]any{"type": "integer"},
				"provider":         map[string]any{"type": "string"},
				"provider_user_id": map[string]any{"type": "string"},
				"force":            map[string]any{"type": "boolean"},
			}), []string{"user_id", "provider"}),
			Handler: a.toolIdentitiesUnlink,
		},
		{
			Name:        "auth_identities_list",
			Description: "List a user's external identities (user_id), or resolve one identity to its user (provider + provider_user_id). Requires organization_id/slug. Read-only: never mints a session.",
			InputSchema: schemaObject(merge(map[string]any{
				"user_id":          map[string]any{"type": "integer"},
				"provider":         map[string]any{"type": "string"},
				"provider_user_id": map[string]any{"type": "string"},
			}), nil),
			Handler: a.toolIdentitiesList,
		},
		{
			Name:        "auth_guest_upgrade",
			Description: "Convert a guest user into a full account by setting an email and password. Requires organization_id/slug + user_id + email + password; optional display_name. Validates the password policy and email uniqueness, keeps every linked identity, and sends a verification email when email_verification_required is true. Returns {user, verification_required}.",
			InputSchema: schemaObject(merge(map[string]any{
				"user_id":      map[string]any{"type": "integer"},
				"email":        map[string]any{"type": "string"},
				"password":     map[string]any{"type": "string"},
				"display_name": map[string]any{"type": "string"},
			}), []string{"user_id", "email", "password"}),
			Handler: a.toolGuestUpgrade,
		},
		{
			Name:        "auth_jwks_get",
			Description: "Return the organization's public signing keys (JWKS) plus the issuer string, so sibling apps can verify auth-issued EdDSA JWTs locally without an HTTP round trip per request. Args: organization_id/slug (defaults to the default org).",
			InputSchema: schemaObject(orgSelector, nil),
			Handler:     a.toolJWKSGet,
		},
	}
}

func (a *App) toolPublicLoginIdentity(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	provider, subject, err := validateProviderPair(stringArg(args, "provider", ""), stringArg(args, "provider_user_id", ""))
	if err != nil {
		return nil, err
	}
	client, err := requireClient(ctx, pid, stringArg(args, "client_id", ""))
	if err != nil {
		return nil, err
	}
	org, err := resolveOrgForRequest(ctx, pid, client, stringArg(args, "organization_slug", ""))
	if err != nil {
		return nil, err
	}
	ip := stringArg(args, "ip", "")
	ua := stringArg(args, "user_agent", "mcp:auth_public_login_identity")
	db := ctx.AppDB()

	ident, err := dbFindIdentity(db, pid, org.ID, provider, subject)
	if err != nil {
		return nil, err
	}
	var user *User
	created := false
	if ident != nil {
		user, err = dbGetUserByID(db, pid, org.ID, ident.UserID)
		if err != nil {
			return nil, fmt.Errorf("identity user: %w", err)
		}
		_ = dbTouchIdentity(db, ident.ID)
	} else {
		if !boolArg(args, "create_if_missing", true) {
			dbAudit(db, pid, org.ID, nil, client.ClientID, "login_failed", ip, ua,
				map[string]any{"reason": "identity_unknown", "provider": provider})
			return nil, errors.New("identity_not_found")
		}
		if limit := cfgInt(ctx, "identity_signups_per_client_per_hour", 600); limit > 0 {
			n, err := dbCountClientEvents(db, pid, client.ClientID, identityCreatedEvent, time.Now().Add(-time.Hour))
			if err != nil {
				return nil, err
			}
			if n >= limit {
				dbAudit(db, pid, org.ID, nil, client.ClientID, "identity_rate_limited", ip, ua,
					map[string]any{"provider": provider, "limit": limit})
				return nil, fmt.Errorf("rate_limited: client %s created %d identities in the last hour (limit %d)", client.ClientID, n, limit)
			}
		}
		email, err := newGuestEmail()
		if err != nil {
			return nil, err
		}
		metadataJSON, err := metadataArg(args)
		if err != nil {
			return nil, err
		}
		uid, err := dbCreateGuestUser(db, pid, org.ID, email, stringArg(args, "display_name", ""), metadataJSON)
		if err != nil {
			return nil, err
		}
		if _, err := dbInsertIdentity(db, pid, org.ID, uid, provider, subject, ""); err != nil {
			return nil, err
		}
		user, err = dbGetUserByID(db, pid, org.ID, uid)
		if err != nil {
			return nil, err
		}
		created = true
		dbAudit(db, pid, org.ID, &uid, client.ClientID, identityCreatedEvent, ip, ua,
			map[string]any{"provider": provider})
	}

	if user.Status != "active" {
		dbAudit(db, pid, org.ID, &user.ID, client.ClientID, "login_failed", ip, ua,
			map[string]any{"reason": "status:" + user.Status, "provider": provider})
		return nil, errors.New("user_inactive")
	}
	if userLocked(user) {
		dbAudit(db, pid, org.ID, &user.ID, client.ClientID, "login_locked", ip, ua, nil)
		return nil, errors.New("account_locked")
	}
	if err := dbMarkLoginSuccess(db, pid, org.ID, user.ID); err != nil {
		return nil, err
	}
	// Synthetic request so mintSession can stamp UA / IP on the
	// sessions row; orgBaseURL prefers config + PlatformInfo over the
	// request host, so the empty Host is harmless.
	r, _ := http.NewRequest("POST", "/login", nil)
	r.RemoteAddr = ip
	r.Header.Set("User-Agent", ua)
	tokens, err := mintSession(ctx, pid, org, user, client, r)
	if err != nil {
		return nil, err
	}
	aptevaToken, err := mintAptevaDelegatedToken(pid, org, user, client)
	if err != nil {
		ctx.Logger().Warn("delegated user token mint failed", "err", err)
		aptevaToken = nil
	}
	dbAudit(db, pid, org.ID, &user.ID, client.ClientID, "identity_login", ip, ua,
		map[string]any{"provider": provider, "created": created})
	if refreshed, err := dbGetUserByID(db, pid, org.ID, user.ID); err == nil {
		user = refreshed
	}
	out := map[string]any{
		"user":              user,
		"created":           created,
		"provider":          provider,
		"authorization":     tokens.authorization,
		"access_token":      tokens.access,
		"refresh_token":     tokens.refresh,
		"expires_in":        tokens.expiresIn,
		"token_type":        "Bearer",
		"client_id":         client.ClientID,
		"organization_slug": org.Slug,
	}
	if aptevaToken != nil {
		out["apteva_access_token"] = aptevaToken.AccessToken
		out["apteva_expires_in"] = aptevaToken.ExpiresIn
	}
	return out, nil
}

func (a *App) toolIdentitiesLink(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	uid, ok := intReq(args, "user_id")
	if !ok {
		return nil, errors.New("user_id required")
	}
	provider, subject, err := validateProviderPair(stringArg(args, "provider", ""), stringArg(args, "provider_user_id", ""))
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if _, err := dbGetUserByID(db, pid, org.ID, uid); err != nil {
		return nil, errors.New("unknown user_id")
	}
	if existing, err := dbFindIdentity(db, pid, org.ID, provider, subject); err != nil {
		return nil, err
	} else if existing != nil {
		if existing.UserID == uid {
			return map[string]any{"identity": existing, "linked": false}, nil
		}
		return nil, errors.New("identity_already_linked: this provider subject belongs to another user")
	}
	rawProfile := ""
	if v, ok := args["raw_profile"]; ok && v != nil {
		if b, err := json.Marshal(v); err == nil {
			rawProfile = string(b)
		}
	}
	id, err := dbInsertIdentity(db, pid, org.ID, uid, provider, subject, rawProfile)
	if err != nil {
		return nil, err
	}
	dbAudit(db, pid, org.ID, &uid, "", "identity_linked", "", "agent",
		map[string]any{"provider": provider})
	ident, err := dbFindIdentity(db, pid, org.ID, provider, subject)
	if err != nil || ident == nil {
		ident = &Identity{ID: id, OrganizationID: org.ID, UserID: uid, Provider: provider, ProviderUserID: subject}
	}
	return map[string]any{"identity": ident, "linked": true}, nil
}

func (a *App) toolIdentitiesUnlink(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	uid, ok := intReq(args, "user_id")
	if !ok {
		return nil, errors.New("user_id required")
	}
	provider := strings.ToLower(strings.TrimSpace(stringArg(args, "provider", "")))
	if !providerRe.MatchString(provider) {
		return nil, errors.New("provider required")
	}
	subject := strings.TrimSpace(stringArg(args, "provider_user_id", ""))
	db := ctx.AppDB()
	user, err := dbGetUserByID(db, pid, org.ID, uid)
	if err != nil {
		return nil, errors.New("unknown user_id")
	}
	if user.Kind == userKindGuest && !user.HasPassword && !boolArg(args, "force", false) {
		all, err := dbListIdentities(db, pid, org.ID, uid)
		if err != nil {
			return nil, err
		}
		remaining := 0
		for _, i := range all {
			if i.Provider == provider && (subject == "" || i.ProviderUserID == subject) {
				continue
			}
			remaining++
		}
		if remaining == 0 {
			return nil, errors.New("unlinking would leave this guest unreachable; upgrade the account first or pass force=true")
		}
	}
	n, err := dbDeleteIdentities(db, pid, org.ID, uid, provider, subject)
	if err != nil {
		return nil, err
	}
	dbAudit(db, pid, org.ID, &uid, "", "identity_unlinked", "", "agent",
		map[string]any{"provider": provider, "count": n})
	return map[string]any{"removed": n}, nil
}

func (a *App) toolIdentitiesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if uid, ok := intReq(args, "user_id"); ok {
		list, err := dbListIdentities(db, pid, org.ID, uid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"identities": list, "count": len(list)}, nil
	}
	provider, subject, err := validateProviderPair(stringArg(args, "provider", ""), stringArg(args, "provider_user_id", ""))
	if err != nil {
		return nil, errors.New("user_id, or provider + provider_user_id, required")
	}
	ident, err := dbFindIdentity(db, pid, org.ID, provider, subject)
	if err != nil {
		return nil, err
	}
	if ident == nil {
		return map[string]any{"identities": []Identity{}, "count": 0}, nil
	}
	return map[string]any{"identities": []Identity{*ident}, "count": 1}, nil
}

func (a *App) toolGuestUpgrade(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	uid, ok := intReq(args, "user_id")
	if !ok {
		return nil, errors.New("user_id required")
	}
	email := strings.ToLower(strings.TrimSpace(stringArg(args, "email", "")))
	password := stringArg(args, "password", "")
	if email == "" || password == "" {
		return nil, errors.New("email and password required")
	}
	if !strings.Contains(email, "@") || isGuestEmail(email) {
		return nil, errors.New("email must be a real address")
	}
	db := ctx.AppDB()
	user, err := dbGetUserByID(db, pid, org.ID, uid)
	if err != nil {
		return nil, errors.New("unknown user_id")
	}
	if user.Kind != userKindGuest {
		return nil, errors.New("user is not a guest")
	}
	if existing, err := dbGetUserByEmail(db, pid, org.ID, email); err == nil && existing != nil {
		return nil, errors.New("email already registered")
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if reason := validatePassword(password,
		cfgInt(ctx, "password_min_length", 8),
		cfgInt(ctx, "password_classes_required", 0)); reason != "" {
		return nil, errors.New(reason)
	}
	pwHash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	verificationRequired := cfgBool(ctx, "email_verification_required", true)
	if err := dbUpgradeGuest(db, pid, org.ID, uid, email, pwHash, !verificationRequired); err != nil {
		return nil, err
	}
	if v, ok := args["display_name"].(string); ok && strings.TrimSpace(v) != "" {
		_ = dbUpdateUserProfile(db, pid, org.ID, uid, &v, nil, nil)
	}
	if verificationRequired {
		if err := issueVerifyEmailToken(ctx, pid, org, uid, email); err != nil {
			ctx.Logger().Warn("verify-email send failed", "err", err)
		}
	}
	dbAudit(db, pid, org.ID, &uid, "", "guest_upgraded", "", "agent",
		map[string]any{"email": email, "verification_required": verificationRequired})
	updated, err := dbGetUserByID(db, pid, org.ID, uid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"user": updated, "verification_required": verificationRequired}, nil
}

func (a *App) toolJWKSGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgsOptional(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if org == nil {
		org, err = dbGetOrgBySlug(ctx.AppDB(), pid, "default")
		if err != nil {
			return nil, errors.New("default organization missing")
		}
	}
	keys, err := dbAllSigningKeys(ctx.AppDB(), pid, org.ID)
	if err != nil {
		return nil, err
	}
	out := make([]jwk, 0, len(keys))
	for kid, pub := range keys {
		out = append(out, jwkFromEd25519(kid, pub))
	}
	return map[string]any{
		"organization_slug": org.Slug,
		"issuer":            orgBaseURL(ctx, nil, org),
		"keys":              out,
	}, nil
}
