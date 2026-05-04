package main

// db.go — data access. Pure SQL; no business logic, no HTTP, no JWT.
// Functions return domain types or sql.ErrNoRows; callers translate.
//
// Convention: every function that touches user/session/client/etc.
// data takes (db, projectID, …) so the partition key is impossible
// to forget. The caller computed it via resolveProject*.

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ─── Users ───────────────────────────────────────────────────────────

func dbGetUserByID(db *sql.DB, projectID string, id int64) (*User, error) {
	row := db.QueryRow(`
		SELECT id, email, IFNULL(email_verified_at,''), IFNULL(display_name,''), IFNULL(avatar_url,''),
		       status, password_hash IS NOT NULL,
		       IFNULL(last_login_at,''), IFNULL(locked_until,''),
		       IFNULL(created_at,''), IFNULL(updated_at,'')
		FROM users WHERE project_id = ? AND id = ?`,
		projectID, id)
	return scanUser(db, projectID, row)
}

func dbGetUserByEmail(db *sql.DB, projectID, email string) (*User, error) {
	row := db.QueryRow(`
		SELECT id, email, IFNULL(email_verified_at,''), IFNULL(display_name,''), IFNULL(avatar_url,''),
		       status, password_hash IS NOT NULL,
		       IFNULL(last_login_at,''), IFNULL(locked_until,''),
		       IFNULL(created_at,''), IFNULL(updated_at,'')
		FROM users WHERE project_id = ? AND email = ?`,
		projectID, strings.ToLower(strings.TrimSpace(email)))
	return scanUser(db, projectID, row)
}

// scanUser reads a row produced by the queries above (column order is
// fixed). It also computes mfa_enabled with a follow-up query — small
// extra round-trip, kept here so callers don't have to remember it.
func scanUser(db *sql.DB, projectID string, row *sql.Row) (*User, error) {
	var u User
	var hasPw int
	if err := row.Scan(&u.ID, &u.Email, &u.EmailVerifiedAt, &u.DisplayName, &u.AvatarURL,
		&u.Status, &hasPw, &u.LastLoginAt, &u.LockedUntil, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	u.HasPassword = hasPw == 1
	u.ProjectID = projectID
	mfa, err := dbUserHasConfirmedMFA(db, u.ID)
	if err != nil {
		return nil, err
	}
	u.MFAEnabled = mfa
	return &u, nil
}

func dbUserHasConfirmedMFA(db *sql.DB, userID int64) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM mfa_factors WHERE user_id = ? AND confirmed_at IS NOT NULL`,
		userID).Scan(&n)
	return n > 0, err
}

// dbCreateUser inserts a new row and returns the assigned id.
// passwordHash may be empty (OAuth-only signup). emailVerified true
// skips the verification flow (admin-created via MCP).
func dbCreateUser(db *sql.DB, projectID, email, passwordHash, displayName string, emailVerified bool) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	verifiedAt := sql.NullString{}
	if emailVerified {
		verifiedAt = sql.NullString{Valid: true, String: now}
	}
	pw := sql.NullString{}
	if passwordHash != "" {
		pw = sql.NullString{Valid: true, String: passwordHash}
	}
	res, err := db.Exec(`
		INSERT INTO users(project_id, email, password_hash, display_name, email_verified_at, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, 'active', ?, ?)`,
		projectID, strings.ToLower(strings.TrimSpace(email)), pw, displayName, verifiedAt, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// dbGetUserPasswordHash returns the encoded hash for verification.
// Separate from dbGetUserByID because we never want the hash to leak
// into the public User struct.
func dbGetUserPasswordHash(db *sql.DB, projectID string, userID int64) (string, error) {
	var h sql.NullString
	err := db.QueryRow(
		`SELECT password_hash FROM users WHERE project_id = ? AND id = ?`,
		projectID, userID).Scan(&h)
	if err != nil {
		return "", err
	}
	if !h.Valid {
		return "", nil
	}
	return h.String, nil
}

func dbSetUserPassword(db *sql.DB, projectID string, userID int64, passwordHash string) error {
	_, err := db.Exec(
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE project_id = ? AND id = ?`,
		passwordHash, time.Now().UTC().Format(time.RFC3339), projectID, userID)
	return err
}

func dbMarkLoginSuccess(db *sql.DB, projectID string, userID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`UPDATE users SET last_login_at = ?, failed_login_count = 0, locked_until = NULL, updated_at = ? WHERE project_id = ? AND id = ?`,
		now, now, projectID, userID)
	return err
}

// dbMarkLoginFailure increments the streak. The caller decides when
// to lock — passing the policy in keeps the SQL layer policy-free.
func dbMarkLoginFailure(db *sql.DB, projectID string, userID int64, lockUntil time.Time) error {
	var until sql.NullString
	if !lockUntil.IsZero() {
		until = sql.NullString{Valid: true, String: lockUntil.UTC().Format(time.RFC3339)}
	}
	_, err := db.Exec(
		`UPDATE users
		   SET failed_login_count = failed_login_count + 1,
		       locked_until = COALESCE(?, locked_until),
		       updated_at = ?
		 WHERE project_id = ? AND id = ?`,
		until, time.Now().UTC().Format(time.RFC3339), projectID, userID)
	return err
}

func dbSetUserStatus(db *sql.DB, projectID string, userID int64, status string) error {
	_, err := db.Exec(
		`UPDATE users SET status = ?, updated_at = ? WHERE project_id = ? AND id = ?`,
		status, time.Now().UTC().Format(time.RFC3339), projectID, userID)
	return err
}

// dbSearchUsers — q is substring on email + display_name; status and
// createdAfter are optional filters. limit clamped 1..200 by caller.
func dbSearchUsers(db *sql.DB, projectID, q, status, createdAfter string, limit int) ([]User, error) {
	args := []any{projectID}
	conds := []string{"project_id = ?"}
	if q != "" {
		conds = append(conds, "(email LIKE ? OR display_name LIKE ?)")
		like := "%" + strings.ToLower(q) + "%"
		args = append(args, like, like)
	}
	if status != "" {
		conds = append(conds, "status = ?")
		args = append(args, status)
	}
	if createdAfter != "" {
		conds = append(conds, "created_at > ?")
		args = append(args, createdAfter)
	}
	args = append(args, limit)
	q1 := "SELECT id, email, IFNULL(email_verified_at,''), IFNULL(display_name,''), IFNULL(avatar_url,''), status, password_hash IS NOT NULL, IFNULL(last_login_at,''), IFNULL(locked_until,''), IFNULL(created_at,''), IFNULL(updated_at,'') FROM users WHERE " + strings.Join(conds, " AND ") + " ORDER BY created_at DESC LIMIT ?"
	rows, err := db.Query(q1, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var hasPw int
		if err := rows.Scan(&u.ID, &u.Email, &u.EmailVerifiedAt, &u.DisplayName, &u.AvatarURL,
			&u.Status, &hasPw, &u.LastLoginAt, &u.LockedUntil, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.HasPassword = hasPw == 1
		u.ProjectID = projectID
		// MFA flag — one extra query per row, fine for v0.1 page sizes.
		// If this becomes a hot path, replace with a LEFT JOIN to a
		// "any confirmed factor" subquery.
		mfa, err := dbUserHasConfirmedMFA(db, u.ID)
		if err != nil {
			return nil, err
		}
		u.MFAEnabled = mfa
		out = append(out, u)
	}
	return out, rows.Err()
}

// ─── Sessions ────────────────────────────────────────────────────────

func dbCreateSession(db *sql.DB, projectID string, userID int64, clientID, refreshHash, ua, ip string, expiresAt time.Time) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO sessions(project_id, user_id, client_id, refresh_token_hash, user_agent, ip, expires_at)
		 VALUES(?,?,?,?,?,?,?)`,
		projectID, userID, clientID, refreshHash, ua, ip, expiresAt.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// dbFindActiveSessionByRefresh looks up an unrevoked, unexpired
// session by the (already-hashed) refresh token. Returns nil if not
// found — the caller treats nil as "invalid refresh token" without
// distinguishing reasons (defends against probing).
func dbFindActiveSessionByRefresh(db *sql.DB, projectID, refreshHash string) (*Session, error) {
	row := db.QueryRow(`
		SELECT id, user_id, client_id, IFNULL(user_agent,''), IFNULL(ip,''),
		       IFNULL(created_at,''), IFNULL(last_seen_at,''), IFNULL(expires_at,''), IFNULL(revoked_at,'')
		FROM sessions
		WHERE project_id = ? AND refresh_token_hash = ?
		  AND revoked_at IS NULL
		  AND expires_at > ?`,
		projectID, refreshHash, time.Now().UTC().Format(time.RFC3339))
	var s Session
	if err := row.Scan(&s.ID, &s.UserID, &s.ClientID, &s.UserAgent, &s.IP,
		&s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.RevokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func dbRevokeSession(db *sql.DB, projectID string, id int64) error {
	_, err := db.Exec(
		`UPDATE sessions SET revoked_at = ? WHERE project_id = ? AND id = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), projectID, id)
	return err
}

func dbRevokeAllUserSessions(db *sql.DB, projectID string, userID int64) (int64, error) {
	res, err := db.Exec(
		`UPDATE sessions SET revoked_at = ? WHERE project_id = ? AND user_id = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), projectID, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func dbListUserSessions(db *sql.DB, projectID string, userID int64) ([]Session, error) {
	rows, err := db.Query(`
		SELECT id, user_id, client_id, IFNULL(user_agent,''), IFNULL(ip,''),
		       IFNULL(created_at,''), IFNULL(last_seen_at,''), IFNULL(expires_at,''), IFNULL(revoked_at,'')
		FROM sessions
		WHERE project_id = ? AND user_id = ?
		ORDER BY created_at DESC`,
		projectID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.UserID, &s.ClientID, &s.UserAgent, &s.IP,
			&s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ─── Clients ─────────────────────────────────────────────────────────

func dbCreateClient(db *sql.DB, projectID string, c Client, secretHash string) (int64, error) {
	redirects, _ := json.Marshal(c.RedirectURIs)
	origins, _ := json.Marshal(c.AllowedOrigins)
	grants, _ := json.Marshal(c.AllowedGrantTypes)
	requirePKCE := 0
	if c.RequirePKCE {
		requirePKCE = 1
	}
	requireMFA := 0
	if c.RequireMFA {
		requireMFA = 1
	}
	rotation := 1
	if !c.RefreshRotation {
		rotation = 0
	}
	res, err := db.Exec(`
		INSERT INTO clients(project_id, client_id, client_secret_hash, name, type,
			redirect_uris, allowed_origins, allowed_grant_types, token_endpoint_auth_method,
			require_pkce, require_mfa, jwt_audience,
			access_token_ttl_seconds, refresh_token_ttl_seconds, refresh_rotation)
		VALUES(?,?,?,?,?, ?,?,?,?, ?,?,?, ?,?,?)`,
		projectID, c.ClientID, nullStr(secretHash), c.Name, c.Type,
		string(redirects), string(origins), string(grants), c.TokenEndpointAuthMethod,
		requirePKCE, requireMFA, nullStr(c.JWTAudience),
		nullInt(c.AccessTokenTTLSeconds), nullInt(c.RefreshTokenTTLSeconds), rotation)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbGetClientByClientID(db *sql.DB, projectID, clientID string) (*Client, error) {
	row := db.QueryRow(`
		SELECT id, client_id, name, type,
		       redirect_uris, allowed_origins, allowed_grant_types, token_endpoint_auth_method,
		       require_pkce, require_mfa, IFNULL(jwt_audience,''),
		       IFNULL(access_token_ttl_seconds, 0), IFNULL(refresh_token_ttl_seconds, 0), refresh_rotation,
		       IFNULL(disabled_at,''), IFNULL(created_at,'')
		FROM clients WHERE project_id = ? AND client_id = ?`,
		projectID, clientID)
	return scanClient(row)
}

func scanClient(row *sql.Row) (*Client, error) {
	var c Client
	var redirects, origins, grants string
	var requirePKCE, requireMFA, rotation int
	if err := row.Scan(&c.ID, &c.ClientID, &c.Name, &c.Type,
		&redirects, &origins, &grants, &c.TokenEndpointAuthMethod,
		&requirePKCE, &requireMFA, &c.JWTAudience,
		&c.AccessTokenTTLSeconds, &c.RefreshTokenTTLSeconds, &rotation,
		&c.DisabledAt, &c.CreatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(redirects), &c.RedirectURIs)
	_ = json.Unmarshal([]byte(origins), &c.AllowedOrigins)
	_ = json.Unmarshal([]byte(grants), &c.AllowedGrantTypes)
	c.RequirePKCE = requirePKCE == 1
	c.RequireMFA = requireMFA == 1
	c.RefreshRotation = rotation == 1
	return &c, nil
}

// dbVerifyClientSecret returns true when the presented secret matches
// the stored hash. Public clients (null secret) always return false —
// they shouldn't be authenticating with a secret in the first place.
func dbVerifyClientSecret(db *sql.DB, projectID, clientID, secret string) (bool, error) {
	var h sql.NullString
	if err := db.QueryRow(
		`SELECT client_secret_hash FROM clients WHERE project_id = ? AND client_id = ? AND disabled_at IS NULL`,
		projectID, clientID).Scan(&h); err != nil {
		return false, err
	}
	if !h.Valid {
		return false, nil
	}
	return h.String == hashToken(secret), nil
}

func dbListClients(db *sql.DB, projectID string, includeDisabled bool) ([]Client, error) {
	q := `SELECT id, client_id, name, type, redirect_uris, allowed_origins, allowed_grant_types, token_endpoint_auth_method, require_pkce, require_mfa, IFNULL(jwt_audience,''), IFNULL(access_token_ttl_seconds,0), IFNULL(refresh_token_ttl_seconds,0), refresh_rotation, IFNULL(disabled_at,''), IFNULL(created_at,'') FROM clients WHERE project_id = ?`
	if !includeDisabled {
		q += " AND disabled_at IS NULL"
	}
	q += " ORDER BY created_at DESC"
	rows, err := db.Query(q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Client
	for rows.Next() {
		var c Client
		var redirects, origins, grants string
		var requirePKCE, requireMFA, rotation int
		if err := rows.Scan(&c.ID, &c.ClientID, &c.Name, &c.Type,
			&redirects, &origins, &grants, &c.TokenEndpointAuthMethod,
			&requirePKCE, &requireMFA, &c.JWTAudience,
			&c.AccessTokenTTLSeconds, &c.RefreshTokenTTLSeconds, &rotation,
			&c.DisabledAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(redirects), &c.RedirectURIs)
		_ = json.Unmarshal([]byte(origins), &c.AllowedOrigins)
		_ = json.Unmarshal([]byte(grants), &c.AllowedGrantTypes)
		c.RequirePKCE = requirePKCE == 1
		c.RequireMFA = requireMFA == 1
		c.RefreshRotation = rotation == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func dbUpdateClientSecret(db *sql.DB, projectID, clientID, newHash string) error {
	res, err := db.Exec(
		`UPDATE clients SET client_secret_hash = ? WHERE project_id = ? AND client_id = ?`,
		newHash, projectID, clientID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("client not found")
	}
	return nil
}

func dbDisableClient(db *sql.DB, projectID, clientID string) error {
	_, err := db.Exec(
		`UPDATE clients SET disabled_at = ? WHERE project_id = ? AND client_id = ? AND disabled_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), projectID, clientID)
	return err
}

// ─── Signing keys ────────────────────────────────────────────────────

// dbActiveSigningKey returns the most recently created non-retired key
// for the project — the one used to sign new tokens. Verification uses
// dbAllSigningKeys instead so retired-but-not-deleted keys still
// validate during the drain window.
func dbActiveSigningKey(db *sql.DB, projectID string) (kid string, priv ed25519.PrivateKey, err error) {
	var privPEM string
	err = db.QueryRow(`
		SELECT kid, private_pem FROM signing_keys
		WHERE project_id = ? AND retired_at IS NULL
		ORDER BY created_at DESC LIMIT 1`,
		projectID).Scan(&kid, &privPEM)
	if err != nil {
		return "", nil, err
	}
	priv, err = parseEd25519Private(privPEM)
	return
}

// dbAllSigningKeys returns every (kid → public key) for the project,
// retired and active alike. JWKS publishes all of them; verifiers
// pick by kid.
func dbAllSigningKeys(db *sql.DB, projectID string) (map[string]ed25519.PublicKey, error) {
	rows, err := db.Query(`SELECT kid, public_pem FROM signing_keys WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ed25519.PublicKey{}
	for rows.Next() {
		var kid, pubPEM string
		if err := rows.Scan(&kid, &pubPEM); err != nil {
			return nil, err
		}
		pub, err := parseEd25519Public(pubPEM)
		if err != nil {
			return nil, fmt.Errorf("kid %s: %w", kid, err)
		}
		out[kid] = pub
	}
	return out, rows.Err()
}

// ─── Verification tokens ─────────────────────────────────────────────

func dbInsertVerificationToken(db *sql.DB, projectID string, userID int64, kind, tokenHash, meta string, expiresAt time.Time) error {
	_, err := db.Exec(`
		INSERT INTO verification_tokens(project_id, user_id, token_hash, kind, meta, expires_at)
		VALUES(?,?,?,?,?,?)`,
		projectID, userID, tokenHash, kind, nullStr(meta), expiresAt.UTC().Format(time.RFC3339))
	return err
}

// dbConsumeVerificationToken looks up by hash, rejects if expired or
// already used, marks used. Returns (user_id, kind, meta) on success.
// All in one query so the consume is atomic from the caller's view.
func dbConsumeVerificationToken(db *sql.DB, projectID, tokenHash string) (userID int64, kind, meta string, err error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, "", "", err
	}
	defer tx.Rollback()
	var expiresAt, used sql.NullString
	var metaNS sql.NullString
	err = tx.QueryRow(`
		SELECT user_id, kind, meta, expires_at, used_at
		FROM verification_tokens
		WHERE project_id = ? AND token_hash = ?`,
		projectID, tokenHash).Scan(&userID, &kind, &metaNS, &expiresAt, &used)
	if err != nil {
		return 0, "", "", err
	}
	if used.Valid && used.String != "" {
		return 0, "", "", errors.New("token already used")
	}
	if expiresAt.Valid {
		if t, perr := time.Parse(time.RFC3339, expiresAt.String); perr == nil && t.Before(time.Now()) {
			return 0, "", "", errors.New("token expired")
		}
	}
	if _, err := tx.Exec(
		`UPDATE verification_tokens SET used_at = ? WHERE project_id = ? AND token_hash = ?`,
		time.Now().UTC().Format(time.RFC3339), projectID, tokenHash); err != nil {
		return 0, "", "", err
	}
	if err := tx.Commit(); err != nil {
		return 0, "", "", err
	}
	if metaNS.Valid {
		meta = metaNS.String
	}
	return userID, kind, meta, nil
}

// dbMarkEmailVerified is the side-effect of consuming a verify_email
// token. Idempotent: writing the same value twice is fine.
func dbMarkEmailVerified(db *sql.DB, projectID string, userID int64) error {
	_, err := db.Exec(
		`UPDATE users SET email_verified_at = COALESCE(email_verified_at, ?), updated_at = ? WHERE project_id = ? AND id = ?`,
		time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), projectID, userID)
	return err
}

// ─── Audit log ───────────────────────────────────────────────────────

func dbAudit(db *sql.DB, projectID string, userID *int64, clientID, event, ip, ua string, metadata map[string]any) {
	var meta sql.NullString
	if len(metadata) > 0 {
		if b, err := json.Marshal(metadata); err == nil {
			meta = sql.NullString{Valid: true, String: string(b)}
		}
	}
	var uid sql.NullInt64
	if userID != nil {
		uid = sql.NullInt64{Valid: true, Int64: *userID}
	}
	// Best-effort — auditing must never break the request path.
	_, _ = db.Exec(`
		INSERT INTO audit_log(project_id, user_id, client_id, event, ip, user_agent, metadata)
		VALUES(?,?,?,?,?,?,?)`,
		projectID, uid, nullStr(clientID), event, nullStr(ip), nullStr(ua), meta)
}

func dbAuditSearch(db *sql.DB, projectID string, userID int64, event, since, until string, limit int) ([]AuditEvent, error) {
	args := []any{projectID}
	conds := []string{"project_id = ?"}
	if userID > 0 {
		conds = append(conds, "user_id = ?")
		args = append(args, userID)
	}
	if event != "" {
		conds = append(conds, "event = ?")
		args = append(args, event)
	}
	if since != "" {
		conds = append(conds, "occurred_at >= ?")
		args = append(args, since)
	}
	if until != "" {
		conds = append(conds, "occurred_at <= ?")
		args = append(args, until)
	}
	args = append(args, limit)
	q := "SELECT id, user_id, IFNULL(client_id,''), event, IFNULL(ip,''), IFNULL(user_agent,''), IFNULL(metadata,''), occurred_at FROM audit_log WHERE " + strings.Join(conds, " AND ") + " ORDER BY occurred_at DESC LIMIT ?"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var a AuditEvent
		var uid sql.NullInt64
		if err := rows.Scan(&a.ID, &uid, &a.ClientID, &a.Event, &a.IP, &a.UserAgent, &a.Metadata, &a.OccurredAt); err != nil {
			return nil, err
		}
		if uid.Valid {
			v := uid.Int64
			a.UserID = &v
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ─── Stats ───────────────────────────────────────────────────────────

type Stats struct {
	Active     int `json:"active"`
	Disabled   int `json:"disabled"`
	Locked     int `json:"locked"`
	Signups7d  int `json:"signups_7d"`
	Logins24h  int `json:"logins_24h"`
}

func dbStats(db *sql.DB, projectID string) (Stats, error) {
	var s Stats
	weekAgo := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	dayAgo := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	q := func(sql_ string, args ...any) int {
		var n int
		_ = db.QueryRow(sql_, args...).Scan(&n)
		return n
	}
	s.Active = q(`SELECT COUNT(*) FROM users WHERE project_id = ? AND status = 'active'`, projectID)
	s.Disabled = q(`SELECT COUNT(*) FROM users WHERE project_id = ? AND status = 'disabled'`, projectID)
	s.Locked = q(`SELECT COUNT(*) FROM users WHERE project_id = ? AND locked_until IS NOT NULL AND locked_until > ?`, projectID, now)
	s.Signups7d = q(`SELECT COUNT(*) FROM users WHERE project_id = ? AND created_at > ?`, projectID, weekAgo)
	s.Logins24h = q(`SELECT COUNT(*) FROM audit_log WHERE project_id = ? AND event = 'login' AND occurred_at > ?`, projectID, dayAgo)
	return s, nil
}

// ─── tiny helpers ────────────────────────────────────────────────────

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) any {
	if n <= 0 {
		return nil
	}
	return n
}
