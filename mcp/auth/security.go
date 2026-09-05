package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// DBTX lets state transitions use the same readers within their transaction.
type DBTX interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func beginAuthTx(db *sql.DB, pid string, oid int64) (*sql.Tx, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	// Reserve SQLite's writer before reading state. This prevents deferred
	// read-to-write upgrades racing each other, even with multiple processes.
	res, err := tx.Exec(`UPDATE organizations SET status=status WHERE project_id=? AND id=?`, pid, oid)
	if err == nil {
		n, e := res.RowsAffected()
		if e != nil {
			err = e
		} else if n != 1 {
			err = errors.New("unknown organization")
		}
	}
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

type authPolicy struct {
	MinLength, Classes, AccessSeconds, RefreshDays, LockThreshold, LockMinutes int
	Verify                                                                     bool
}
type policySpec struct {
	defaultValue int
	min, max     int
}

var policyInts = map[string]policySpec{
	"password_min_length": {8, 8, 256}, "password_classes_required": {0, 0, 4},
	"jwt_access_ttl_seconds": {900, 60, 86400}, "jwt_refresh_ttl_days": {30, 1, 90},
	"lockout_threshold": {5, 0, 100}, "lockout_initial_minutes": {15, 1, 1440},
}

func effectivePolicy(ctx *sdk.AppCtx, org *Organization) (authPolicy, error) {
	values := map[string]any{}
	for name, s := range policyInts {
		values[name] = s.defaultValue
		if raw := strings.TrimSpace(cfgStr(ctx, name, "")); raw != "" {
			n, e := strconv.Atoi(raw)
			if e != nil {
				return authPolicy{}, fmt.Errorf("invalid policy %s", name)
			}
			values[name] = n
		}
	}
	verify := true
	if raw := strings.TrimSpace(cfgStr(ctx, "email_verification_required", "")); raw != "" {
		b, e := strconv.ParseBool(raw)
		if e != nil {
			return authPolicy{}, errors.New("invalid email_verification_required")
		}
		verify = b
	}
	if org != nil && strings.TrimSpace(org.PolicyOverrides) != "" {
		var overrides map[string]any
		d := json.NewDecoder(strings.NewReader(org.PolicyOverrides))
		d.UseNumber()
		if err := d.Decode(&overrides); err != nil || overrides == nil {
			return authPolicy{}, errors.New("policy_overrides must be a JSON object")
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return authPolicy{}, errors.New("policy_overrides must contain exactly one object")
		}
		for k, v := range overrides {
			if k == "email_verification_required" {
				b, ok := v.(bool)
				if !ok {
					return authPolicy{}, errors.New("email_verification_required must be boolean")
				}
				verify = b
				continue
			}
			if _, ok := policyInts[k]; !ok {
				return authPolicy{}, fmt.Errorf("unsupported policy %s", k)
			}
			n, ok := strictID(v, false)
			if !ok {
				return authPolicy{}, fmt.Errorf("policy %s must be an integer", k)
			}
			values[k] = int(n)
		}
	}
	for k, s := range policyInts {
		n := values[k].(int)
		if n < s.min || n > s.max {
			return authPolicy{}, fmt.Errorf("policy %s must be between %d and %d", k, s.min, s.max)
		}
	}
	return authPolicy{values["password_min_length"].(int), values["password_classes_required"].(int), values["jwt_access_ttl_seconds"].(int), values["jwt_refresh_ttl_days"].(int), values["lockout_threshold"].(int), values["lockout_initial_minutes"].(int), verify}, nil
}
func validatePolicyJSON(raw *string) error {
	if raw == nil || *raw == "" {
		return nil
	}
	_, err := effectivePolicy(nil, &Organization{PolicyOverrides: *raw})
	return err
}
func checkPasswordPolicy(ctx *sdk.AppCtx, org *Organization, pw string) error {
	p, e := effectivePolicy(ctx, org)
	if e != nil {
		return e
	}
	if why := validatePassword(pw, p.MinLength, p.Classes); why != "" {
		return errors.New(why)
	}
	return nil
}

func sessionEligibility(ctx *sdk.AppCtx, org *Organization, u *User, c *Client) error {
	if org == nil || org.Status != "active" {
		return errors.New("organization_inactive")
	}
	if u == nil || u.Status != "active" {
		return errors.New("user_inactive")
	}
	if c == nil || c.DisabledAt != "" || c.Type == "m2m" || hasGrant(c, "client_credentials") {
		return errors.New("client_not_allowed")
	}
	if c.OrganizationID > 0 && c.OrganizationID != org.ID {
		return errors.New("client_not_allowed")
	}
	if u.OrganizationID != org.ID {
		return errors.New("user_inactive")
	}
	if c.RequireMFA || u.MFAEnabled {
		return errors.New("mfa_required: MFA authentication is not implemented")
	}
	if userLocked(u) {
		return errors.New("account_locked")
	}
	p, err := effectivePolicy(ctx, org)
	if err != nil {
		return err
	}
	if p.Verify && u.Kind != userKindGuest && u.EmailVerifiedAt == "" {
		return errors.New("email_unverified")
	}
	return nil
}

func strictID(v any, positive bool) (int64, bool) {
	var n int64
	switch x := v.(type) {
	case json.Number:
		var err error
		n, err = x.Int64()
		if err != nil {
			return 0, false
		}
	case int64:
		n = x
	case int:
		n = int64(x)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || x >= float64(math.MaxInt64) || x < float64(math.MinInt64) || math.Trunc(x) != x {
			return 0, false
		}
		n = int64(x)
	default:
		return 0, false
	}
	return n, (!positive || n > 0)
}
func strictStrings(args map[string]any, key string) ([]string, error) {
	v, exists := args[key]
	if !exists {
		return nil, nil
	}
	var out []string
	switch x := v.(type) {
	case []string:
		out = append([]string{}, x...)
	case []any:
		out = []string{}
		for _, a := range x {
			s, ok := a.(string)
			if !ok {
				return nil, fmt.Errorf("%s must contain strings", key)
			}
			out = append(out, s)
		}
	default:
		return nil, fmt.Errorf("%s must be an array", key)
	}
	if len(out) > 100 {
		return nil, fmt.Errorf("%s allows at most 100 entries", key)
	}
	return out, nil
}
func validateEmail(raw string) error {
	a, e := mail.ParseAddress(raw)
	if e != nil || a.Address != raw || len(raw) > 254 || isGuestEmail(raw) {
		return errors.New("valid email address required")
	}
	return nil
}
func decodeRequest(w http.ResponseWriter, r *http.Request, out any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("exactly one JSON object required")
	}
	return nil
}

// Fixed windows are bounded in storage and enforced atomically by SQLite.
func consumeRate(db DBTX, key string, limit int, period time.Duration) error {
	if limit <= 0 {
		return nil
	}
	secs := int64(period / time.Second)
	now := time.Now().Unix()
	window := now / secs
	key = hashToken(key) + ":" + strconv.FormatInt(window, 10)
	res, err := db.Exec(`INSERT INTO auth_rate_limits(key,count,expires_at) VALUES(?,1,?) ON CONFLICT(key) DO UPDATE SET count=count+1 WHERE count<?`, key, (window+1)*secs, limit)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("rate_limited")
	}
	return nil
}
func publicGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
		if r.Method != "GET" && r.Method != "HEAD" {
			ctx := getAppCtx(r)
			if ctx != nil {
				ip, _, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					ip = r.RemoteAddr
				}
				if err := consumeRate(ctx.AppDB(), envProject()+":request:"+ip, 300, time.Minute); err != nil {
					w.Header().Set("Retry-After", "60")
					httpErr(w, 429, "rate_limited")
					return
				}
			}
		}
		next(w, r)
	}
}

func authenticateClient(ctx *sdk.AppCtx, pid string, c *Client, r *http.Request, secret string) error {
	if c.Type == "m2m" {
		return errors.New("client_not_allowed")
	}
	if hasGrant(c, "client_credentials") {
		return errors.New("client_not_allowed")
	}
	if c.Type != "web" {
		return nil
	}
	if r != nil {
		if id, s, ok := r.BasicAuth(); ok {
			if id != c.ClientID {
				return errors.New("invalid_client")
			}
			secret = s
		}
	}
	ok, err := dbVerifyClientSecret(ctx.AppDB(), pid, c.ClientID, secret)
	if err != nil || !ok {
		return errors.New("invalid_client")
	}
	return nil
}

func markLoginFailure(ctx *sdk.AppCtx, pid string, org *Organization, u *User, p authPolicy) error {
	return inAuthTx(ctx.AppDB(), pid, org.ID, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT failed_login_count FROM users WHERE project_id=? AND organization_id=? AND id=?`, pid, org.ID, u.ID).Scan(&n); err != nil {
			return err
		}
		var until time.Time
		if p.LockThreshold > 0 && n+1 >= p.LockThreshold {
			steps := (n+1)/p.LockThreshold - 1
			if steps > 6 {
				steps = 6
			}
			minutes := p.LockMinutes * (1 << steps)
			if minutes > 1440 {
				minutes = 1440
			}
			until = time.Now().Add(time.Duration(minutes) * time.Minute)
		}
		return dbMarkLoginFailure(tx, pid, org.ID, u.ID, until)
	})
}

// The built-in recovery page is same-origin and submits only a mailbox token.
func requireRecoveryOrigin(ctx *sdk.AppCtx, c *Client, r *http.Request) error {
	origin := r.Header.Get("Origin")
	base, err := url.Parse(platformBaseURL(ctx, r))
	if err == nil && base.Host != "" && origin == base.Scheme+"://"+base.Host {
		return nil
	}
	return requireAllowedOrigin(c, origin)
}
