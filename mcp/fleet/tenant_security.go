package main

// tenant_security.go — incident tooling across Fleet-managed tenants.
//
//   tenant_security_overview  read-only proxy to the tenant's GET /api/security/overview
//   tenant_security_events    read-only proxy to the tenant's GET /api/security/events
//   tenant_contain            rotate the control key, then revoke the target
//                             users' sessions and keys on the tenant
//   tenant_contain_all        tenant_contain on every tenant; complete only
//                             when every tenant is confirmed
//   fleet_incident_report     Fleet's own record of sensitive actions per
//                             tenant (key reveals, password resets, remote
//                             runs, user/key/connection changes) in a window
//
// A tenant running a server without the security API answers 404; that is
// reported as "unsupported" with its version, never as an error.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// tenantSecurityUnsupported reports whether a tenant answer means "this
// server has no security API" (missing route, or the SPA/HTML fallback).
func tenantSecurityUnsupported(err error) bool {
	if err == nil {
		return false
	}
	switch tenantHTTPStatus(err) {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	case 0:
		// A 2xx body that is not JSON (an HTML fallback page).
		return strings.Contains(err.Error(), "invalid character")
	}
	return false
}

func (a *App) unsupportedView(t *Tenant, feature string) map[string]any {
	return map[string]any{
		"tenant_id": t.ID,
		"slug":      t.Slug,
		"status":    "unsupported",
		"version":   t.CurrentVersion,
		"detail":    fmt.Sprintf("tenant apteva-server %s has no %s; upgrade the tenant to use it", firstNonEmptyStr(t.CurrentVersion, "(unknown version)"), feature),
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (a *App) tenantSecurityGET(appCtx *sdk.AppCtx, args map[string]any, path string, query url.Values, feature string) (any, error) {
	id := strings.TrimSpace(getStr(args, "tenant_id"))
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	t, key, err := a.tenantControlAuth(id)
	if err != nil {
		return nil, err
	}
	baseURL, err := a.internalTenantBaseURL(appCtx, t)
	if err != nil {
		return nil, fmt.Errorf("open tenant control channel: %w", err)
	}
	if enc := query.Encode(); enc != "" {
		path += "?" + enc
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out any
	if err := tenantCall(ctx, baseURL, key, http.MethodGet, path, nil, &out); err != nil {
		if tenantSecurityUnsupported(err) {
			return a.unsupportedView(t, feature), nil
		}
		return nil, err
	}
	return map[string]any{"tenant_id": t.ID, "slug": t.Slug, "status": "ok", "version": t.CurrentVersion, "result": out}, nil
}

func (a *App) toolTenantSecurityOverview(appCtx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.tenantSecurityGET(appCtx, args, "/api/security/overview", url.Values{}, "security API")
}

// tenantSecurityEventFilters are passed through to the tenant unchanged.
var tenantSecurityEventFilters = []string{"category", "action", "principal_type", "principal_id", "source_ip", "result", "since", "before_seq", "limit"}

func (a *App) toolTenantSecurityEvents(appCtx *sdk.AppCtx, args map[string]any) (any, error) {
	q := url.Values{}
	for _, k := range tenantSecurityEventFilters {
		switch v := args[k].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				q.Set(k, strings.TrimSpace(v))
			}
		case float64:
			q.Set(k, strconv.FormatInt(int64(v), 10))
		case int:
			q.Set(k, strconv.Itoa(v))
		case int64:
			q.Set(k, strconv.FormatInt(v, 10))
		}
	}
	return a.tenantSecurityGET(appCtx, args, "/api/security/events", q, "security event timeline")
}

// ── containment ────────────────────────────────────────────────────────────

type containTargetResult struct {
	UserID   int64          `json:"user_id,omitempty"`
	AllUsers bool           `json:"all_users,omitempty"`
	Status   string         `json:"status"` // confirmed | partial | refused | unsupported | failed
	Detail   string         `json:"detail,omitempty"`
	Counts   map[string]int `json:"counts,omitempty"`
	Results  any            `json:"results,omitempty"`
}

type tenantContainResult struct {
	TenantID string                `json:"tenant_id"`
	Slug     string                `json:"slug,omitempty"`
	Version  string                `json:"version,omitempty"`
	Status   string                `json:"status"` // confirmed | partial | unsupported | failed
	Detail   string                `json:"detail,omitempty"`
	Rotation *keyRotationResult    `json:"rotation,omitempty"`
	Targets  []containTargetResult `json:"targets,omitempty"`
}

type containOptions struct {
	Reason       string
	UserIDs      []int64
	AllUsers     bool
	DisableUsers bool
	Sessions     bool
	APIKeys      bool
}

func containOptionsFrom(args map[string]any) (containOptions, error) {
	o := containOptions{
		Reason:       strings.TrimSpace(getStr(args, "reason")),
		AllUsers:     boolArg(args, "all_users"),
		DisableUsers: boolArg(args, "disable_users"),
		Sessions:     true,
		APIKeys:      true,
	}
	if o.Reason == "" {
		return o, errors.New("reason is required (it is recorded on the tenant's security trail)")
	}
	if v, ok := args["sessions"].(bool); ok {
		o.Sessions = v
	}
	if v, ok := args["api_keys"].(bool); ok {
		o.APIKeys = v
	}
	if raw, ok := args["user_ids"].([]any); ok {
		for _, v := range raw {
			switch n := v.(type) {
			case float64:
				if n > 0 {
					o.UserIDs = append(o.UserIDs, int64(n))
				}
			case string:
				if id, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil && id > 0 {
					o.UserIDs = append(o.UserIDs, id)
				}
			}
		}
	}
	if o.AllUsers && len(o.UserIDs) > 0 {
		return o, errors.New("pass user_ids or all_users, not both")
	}
	if !o.Sessions && !o.APIKeys && !o.DisableUsers {
		return o, errors.New("nothing to contain: sessions, api_keys and disable_users are all off")
	}
	return o, nil
}

func (a *App) toolTenantContain(appCtx *sdk.AppCtx, args map[string]any) (any, error) {
	id := strings.TrimSpace(getStr(args, "tenant_id"))
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	opts, err := containOptionsFrom(args)
	if err != nil {
		return nil, err
	}
	return a.containTenant(appCtx, id, opts, actorOf(args)), nil
}

// containTenant never returns an error: every outcome is a status, so
// tenant_contain_all can report each tenant.
func (a *App) containTenant(appCtx *sdk.AppCtx, tenantID string, opts containOptions, actor string) *tenantContainResult {
	out := &tenantContainResult{TenantID: tenantID, Status: "failed"}
	finish := func() *tenantContainResult {
		kind := "tenant_contained"
		if out.Status != "confirmed" {
			kind = "tenant_containment_incomplete"
		}
		_ = a.store.recordEvent(tenantID, kind, actor, map[string]any{
			"status": out.Status, "detail": out.Detail, "reason": opts.Reason,
			"targets": out.Targets, "all_users": opts.AllUsers, "user_ids": opts.UserIDs,
		})
		return out
	}
	t, _, err := a.store.get(tenantID)
	if err != nil {
		out.Detail = err.Error()
		return out // unknown tenant: nothing to record against
	}
	out.Slug, out.Version = t.Slug, t.CurrentVersion
	done, err := a.beginTenantOperation(tenantID, "contain tenant")
	if err != nil {
		out.Detail = err.Error()
		return finish()
	}
	defer done()

	// 1. Rotate the control key. This alone voids any revealed key.
	rot, rerr := a.rotateControlKey(appCtx, tenantID, actor, "containment: "+opts.Reason)
	out.Rotation = rot
	rotatedCleanly := rerr == nil
	if rerr != nil && (rot == nil || rot.NewKeyID == 0 || !rot.OldKeyRevoked) {
		out.Detail = "control key rotation failed: " + rerr.Error()
		return finish()
	}

	// 2. Revoke the target users' sessions and keys on the tenant, keeping
	//    Fleet's new control key.
	t, key, err := a.tenantControlAuth(tenantID)
	if err != nil {
		out.Status, out.Detail = "partial", "control key rotated; "+err.Error()
		return finish()
	}
	baseURL, err := a.internalTenantBaseURL(appCtx, t)
	if err != nil {
		out.Status, out.Detail = "partial", "control key rotated; open tenant control channel: "+err.Error()
		return finish()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var me tenantMe
	if err := tenantCall(ctx, baseURL, key, http.MethodGet, "/api/auth/me", nil, &me); err != nil {
		out.Status, out.Detail = "partial", "control key rotated; auth/me: "+err.Error()
		return finish()
	}
	type target struct {
		userID int64
		all    bool
	}
	var targets []target
	switch {
	case opts.AllUsers:
		targets = []target{{all: true}}
	case len(opts.UserIDs) > 0:
		for _, uid := range opts.UserIDs {
			targets = append(targets, target{userID: uid})
		}
	default:
		// The control key's own user: the tenant admin Fleet registered.
		targets = []target{{userID: me.ID}}
	}
	keep := []int64{}
	if rot != nil && rot.NewKeyID > 0 {
		keep = append(keep, rot.NewKeyID)
	}
	allConfirmed := true
	unsupported := false
	for _, tg := range targets {
		req := map[string]any{
			"user_id":              tg.userID,
			"sessions":             opts.Sessions,
			"api_keys":             opts.APIKeys,
			"keep_key_ids":         keep,
			"keep_current_session": false,
			"reason":               "fleet containment: " + opts.Reason,
		}
		if tg.all {
			req["confirm_all_users"] = true
		}
		// Never disable the account Fleet's control key belongs to.
		if opts.DisableUsers && !tg.all && tg.userID != me.ID {
			req["disable_user"] = true
		}
		tr := containTargetResult{UserID: tg.userID, AllUsers: tg.all}
		var resp struct {
			Complete bool           `json:"complete"`
			Counts   map[string]int `json:"counts"`
			Results  any            `json:"results"`
		}
		err := tenantCall(ctx, baseURL, key, http.MethodPost, "/api/security/containment/revoke", req, &resp)
		switch {
		case err == nil:
			tr.Counts, tr.Results = resp.Counts, resp.Results
			if resp.Complete {
				tr.Status = "confirmed"
			} else {
				tr.Status, tr.Detail = "partial", "tenant reported incomplete containment"
			}
		case tenantSecurityUnsupported(err):
			tr.Status, tr.Detail = "unsupported", "tenant server has no containment endpoint"
			unsupported = true
		case tenantHTTPStatus(err) == http.StatusForbidden:
			tr.Status = "refused"
			tr.Detail = "tenant refused containment from an API key; it needs a server that accepts the " + controlKeyScope + " key scope: " + err.Error()
		default:
			tr.Status, tr.Detail = "failed", err.Error()
		}
		if tr.Status != "confirmed" {
			allConfirmed = false
		}
		out.Targets = append(out.Targets, tr)
		if unsupported {
			break
		}
	}
	switch {
	case unsupported:
		out.Status = "unsupported"
		out.Detail = fmt.Sprintf("control key rotated; tenant apteva-server %s has no containment endpoint, so sessions and other keys were not revoked", firstNonEmptyStr(t.CurrentVersion, "(unknown version)"))
	case allConfirmed && rotatedCleanly:
		out.Status = "confirmed"
	default:
		out.Status = "partial"
		if out.Detail == "" {
			out.Detail = "control key rotated; containment on the tenant is incomplete (see targets)"
		}
		if !rotatedCleanly && rerr != nil {
			out.Detail += "; rotation: " + rerr.Error()
		}
	}
	return finish()
}

func (a *App) toolTenantContainAll(appCtx *sdk.AppCtx, args map[string]any) (any, error) {
	opts, err := containOptionsFrom(args)
	if err != nil {
		return nil, err
	}
	if len(opts.UserIDs) > 0 {
		return nil, errors.New("user_ids are per-tenant; use tenant_contain for specific users")
	}
	rows, err := a.store.db.Query(`SELECT id, slug, status FROM fleet_tenants ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	type row struct{ id, slug, status string }
	var tenants []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.slug, &r.status); err != nil {
			rows.Close()
			return nil, err
		}
		tenants = append(tenants, r)
	}
	rows.Close()

	actor := actorOf(args)
	results := make([]*tenantContainResult, len(tenants))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, tn := range tenants {
		if tn.status == StatusSetupPending {
			results[i] = &tenantContainResult{TenantID: tn.id, Slug: tn.slug, Status: "failed",
				Detail: "setup_pending: no control key exists yet; finish setup or delete the tenant"}
			continue
		}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = a.containTenant(appCtx, id, opts, actor)
		}(i, tn.id)
	}
	wg.Wait()

	counts := map[string]int{}
	complete := len(results) > 0
	for _, r := range results {
		counts[r.Status]++
		if r.Status != "confirmed" {
			complete = false
		}
	}
	if len(results) == 0 {
		complete = true
	}
	return map[string]any{
		"complete": complete,
		"counts":   counts,
		"tenants":  results,
		"note":     "complete is true only when every tenant is confirmed; re-run to retry partial or failed tenants (rotation resumes safely).",
	}, nil
}

// ── incident report ────────────────────────────────────────────────────────

// incidentEventKinds are the Fleet events an incident responder needs:
// anything that exposed a credential or changed who can act on a tenant.
var incidentEventKinds = []string{
	"api_key_revealed", "api_key_reveal_denied", "admin_password_reset", "support_login",
	"remote_call", "platform_call", "key_attached", "connected",
	"cloned", "backup_restored", "control_key_rotated",
	"control_key_rotation_failed", "tenant_contained", "tenant_containment_incomplete",
}

// platformReadActions are tenant_platform_call actions that change nothing.
var platformReadActions = map[string]bool{"list": true, "get": true, "marketplace": true, "search": true, "status": true, "read": true, "tools": true, "preflight": true}

func parseIncidentTime(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(v); err == nil {
		return now.Add(-d), nil
	}
	if strings.HasSuffix(v, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil {
			return now.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02"} {
		if ts, err := time.Parse(layout, v); err == nil {
			return ts.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised time %q (use RFC3339, YYYY-MM-DD, or a duration like 24h / 7d)", v)
}

func (a *App) toolIncidentReport(_ *sdk.AppCtx, args map[string]any) (any, error) {
	now := time.Now().UTC()
	since, err := parseIncidentTime(getStr(args, "since"), now)
	if err != nil {
		return nil, err
	}
	if since.IsZero() {
		since = now.Add(-7 * 24 * time.Hour)
	}
	until, err := parseIncidentTime(getStr(args, "until"), now)
	if err != nil {
		return nil, err
	}
	if until.IsZero() {
		until = now
	}
	if !until.After(since) {
		return nil, errors.New("until must be after since")
	}
	const sqliteTime = "2006-01-02 15:04:05"
	q := strings.Builder{}
	q.WriteString(`SELECT e.id, e.tenant_id, COALESCE(t.slug,''), e.kind, COALESCE(e.actor,''), e.payload, e.created_at
		FROM fleet_events e LEFT JOIN fleet_tenants t ON t.id = e.tenant_id
		WHERE e.created_at >= ? AND e.created_at <= ? AND e.kind IN (`)
	params := []any{since.Format(sqliteTime), until.Format(sqliteTime)}
	for i, k := range incidentEventKinds {
		if i > 0 {
			q.WriteString(",")
		}
		q.WriteString("?")
		params = append(params, k)
	}
	q.WriteString(")")
	if id := strings.TrimSpace(getStr(args, "tenant_id")); id != "" {
		q.WriteString(" AND e.tenant_id = ?")
		params = append(params, id)
	}
	if actor := strings.TrimSpace(getStr(args, "actor")); actor != "" {
		q.WriteString(" AND e.actor LIKE ?")
		params = append(params, "%"+actor+"%")
	}
	q.WriteString(" ORDER BY e.created_at, e.id LIMIT 5000")
	rows, err := a.store.db.Query(q.String(), params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type tenantSection struct {
		TenantID string           `json:"tenant_id"`
		Slug     string           `json:"slug,omitempty"`
		Counts   map[string]int   `json:"counts"`
		Events   []map[string]any `json:"events"`
	}
	sections := map[string]*tenantSection{}
	byActor := map[string]int{}
	legacyActors := 0
	total := 0
	includeReads := boolArg(args, "include_reads")
	for rows.Next() {
		var e Event
		var slug, actor string
		var payload *string
		if err := rows.Scan(&e.ID, &e.TenantID, &slug, &e.Kind, &actor, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		var p map[string]any
		if payload != nil {
			_ = json.Unmarshal([]byte(*payload), &p)
		}
		category := e.Kind
		if e.Kind == "platform_call" {
			action, _ := p["action"].(string)
			resource, _ := p["resource"].(string)
			if platformReadActions[action] && !includeReads {
				continue
			}
			category = "platform_" + resource + "_" + action
		}
		s := sections[e.TenantID]
		if s == nil {
			s = &tenantSection{TenantID: e.TenantID, Slug: slug, Counts: map[string]int{}}
			sections[e.TenantID] = s
		}
		s.Counts[category]++
		s.Events = append(s.Events, map[string]any{
			"id": e.ID, "at": e.CreatedAt.UTC().Format(time.RFC3339), "kind": e.Kind,
			"category": category, "actor": actor, "payload": p,
		})
		byActor[actor]++
		if actor == "user" || actor == "" {
			legacyActors++
		}
		total++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*tenantSection, 0, len(sections))
	for _, s := range sections {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	notes := []string{
		"Fleet keeps the newest 1000 events per tenant; older events in the window may have been pruned.",
		"This is Fleet's own record of what it did through each tenant's control key. Use tenant_security_events for the tenant's own security trail.",
	}
	if legacyActors > 0 {
		notes = append(notes, fmt.Sprintf("%d event(s) carry the legacy actor \"user\" (recorded before Fleet 0.12.0) and cannot be attributed to a caller.", legacyActors))
	}
	return map[string]any{
		"since":    since.Format(time.RFC3339),
		"until":    until.Format(time.RFC3339),
		"total":    total,
		"by_actor": byActor,
		"tenants":  out,
		"notes":    notes,
	}, nil
}
