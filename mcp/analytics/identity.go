package main

// Server-side identity and acquisition attribution (v0.18).
//
// The tag sends an anonymous props.visitor_id plus the visitor's first and
// latest acquisition touches (utm_* / touch_* and first_utm_* / first_*).
// Only a backend that has authenticated someone may say who that visitor is:
// it calls analytics_identify, which links the visitor to its user ID,
// backfills the visitor's anonymous tag events, and records aliases (IDs the
// same user has in other apps). Attribution is then derived from the linked
// visitors' tag history, so it works across devices and across domains that
// share a visitor through the tag's linker.

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	maxIdentityValue      = 256
	maxIdentityAliases    = 20
	maxVisitorsPerUser    = 100
	maxAttributionActors  = 5000
	attributionNone       = "(none)"
	attributionNotSet     = "(not set)"
	visitorIDExpr         = "json_extract(props, '$.visitor_id')"
	webVisitorPredicate   = "project_id = ? AND source = 'web' AND " + visitorIDExpr
	defaultAttributionKey = "user_id"
)

var touchFields = []string{"source", "medium", "campaign", "term", "content", "referrer", "landing"}

// Touch is one acquisition touch as the tag recorded it.
type Touch struct {
	Source    string `json:"source,omitempty"`
	Medium    string `json:"medium,omitempty"`
	Campaign  string `json:"campaign,omitempty"`
	Term      string `json:"term,omitempty"`
	Content   string `json:"content,omitempty"`
	Referrer  string `json:"referrer,omitempty"`
	Landing   string `json:"landing,omitempty"`
	TS        int64  `json:"ts,omitempty"`
	VisitorID string `json:"visitor_id,omitempty"`
}

func (t *Touch) field(name string) string {
	switch name {
	case "source":
		return t.Source
	case "medium":
		return t.Medium
	case "campaign":
		return t.Campaign
	case "term":
		return t.Term
	case "content":
		return t.Content
	case "referrer":
		return t.Referrer
	case "landing":
		return t.Landing
	}
	return ""
}

// UserAttribution is a user's first and latest known acquisition touch.
type UserAttribution struct {
	UserID     string   `json:"user_id"`
	Visitors   []string `json:"visitors"`
	FirstTouch *Touch   `json:"first_touch"`
	LastTouch  *Touch   `json:"last_touch"`
}

type IdentifyResult struct {
	UserID         string          `json:"user_id"`
	VisitorID      string          `json:"visitor_id,omitempty"`
	PreviousUserID string          `json:"previous_user_id,omitempty"`
	Backfilled     int64           `json:"backfilled"`
	Aliases        []string        `json:"aliases,omitempty"`
	Attribution    UserAttribution `json:"attribution"`
}

func cleanIdentityValue(name, value string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", fmt.Errorf("%s required", name)
		}
		return "", nil
	}
	if len(value) > maxIdentityValue {
		return "", fmt.Errorf("%s exceeds %d bytes", name, maxIdentityValue)
	}
	return value, nil
}

// linkedUserID returns the user a visitor is linked to, or "".
func linkedUserID(db sqlRunner, projectID, visitorID string) string {
	if projectID == "" || visitorID == "" {
		return ""
	}
	var userID string
	if err := db.QueryRow(`SELECT user_id FROM identity_links WHERE project_id = ? AND visitor_id = ?`, projectID, visitorID).Scan(&userID); err != nil {
		return ""
	}
	return userID
}

// identify links visitorID (optional) and aliases (optional) to userID and
// backfills the visitor's anonymous tag events. Re-linking a visitor to a
// different user (a shared device) moves future events only; events already
// owned by the previous user stay theirs.
func identify(db sqlDatabase, projectID, userID, visitorID string, aliases []string) (IdentifyResult, error) {
	result := IdentifyResult{UserID: userID, VisitorID: visitorID}
	tx, err := db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	if visitorID != "" {
		var previous string
		err := tx.QueryRow(`SELECT user_id FROM identity_links WHERE project_id = ? AND visitor_id = ?`, projectID, visitorID).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result, err
		}
		if previous != "" && previous != userID {
			result.PreviousUserID = previous
		}
		if _, err := tx.Exec(`INSERT INTO identity_links (project_id, visitor_id, user_id, linked_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(project_id, visitor_id) DO UPDATE SET
			  linked_at = CASE WHEN identity_links.user_id = excluded.user_id THEN identity_links.linked_at ELSE excluded.linked_at END,
			  user_id = excluded.user_id,
			  updated_at = excluded.updated_at`,
			projectID, visitorID, userID, now, now); err != nil {
			return result, err
		}
		res, err := tx.Exec(`UPDATE events SET user_id = ? WHERE `+webVisitorPredicate+` = ? AND (user_id IS NULL OR user_id = '')`,
			userID, projectID, visitorID)
		if err != nil {
			return result, err
		}
		result.Backfilled, _ = res.RowsAffected()
	}
	for _, alias := range aliases {
		if alias == userID {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO identity_aliases (project_id, alias, user_id, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(project_id, alias) DO UPDATE SET user_id = excluded.user_id, updated_at = excluded.updated_at`,
			projectID, alias, userID, now); err != nil {
			return result, err
		}
		result.Aliases = append(result.Aliases, alias)
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	result.Attribution, err = userAttribution(db, projectID, userID)
	return result, err
}

// resolveUser maps an alias to its user; anything else is taken as a user ID.
func resolveUser(db sqlRunner, projectID, actor string) string {
	var userID string
	if err := db.QueryRow(`SELECT user_id FROM identity_aliases WHERE project_id = ? AND alias = ?`, projectID, actor).Scan(&userID); err == nil {
		return userID
	}
	return actor
}

func userAttribution(db sqlRunner, projectID, userID string) (UserAttribution, error) {
	out := UserAttribution{UserID: userID, Visitors: []string{}}
	rows, err := db.Query(`SELECT visitor_id FROM identity_links WHERE project_id = ? AND user_id = ? ORDER BY linked_at, visitor_id LIMIT ?`,
		projectID, userID, maxVisitorsPerUser)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var visitor string
		if err := rows.Scan(&visitor); err != nil {
			rows.Close()
			return out, err
		}
		out.Visitors = append(out.Visitors, visitor)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out.Visitors) == 0 {
		return out, err
	}
	if out.FirstTouch, err = visitorsTouch(db, projectID, out.Visitors, "first_", "first_touch_ts", "ASC"); err != nil {
		return out, err
	}
	out.LastTouch, err = visitorsTouch(db, projectID, out.Visitors, "", "touch_ts", "DESC")
	return out, err
}

// visitorsTouch reads the earliest first touch or the latest touch the tag
// reported for any of the visitors.
func visitorsTouch(db sqlRunner, projectID string, visitors []string, prefix, tsKey, order string) (*Touch, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(visitors)), ",")
	args := []any{projectID}
	for _, v := range visitors {
		args = append(args, v)
	}
	tsExpr := "json_extract(props, '$." + tsKey + "')"
	cols := []string{tsExpr, visitorIDExpr}
	labelPrefix := prefix
	if labelPrefix == "" {
		labelPrefix = "touch_"
	}
	for _, f := range touchFields {
		key := prefix + "utm_" + f
		if f == "referrer" || f == "landing" {
			key = labelPrefix + f
		}
		cols = append(cols, "COALESCE(json_extract(props, '$."+key+"'), '')")
	}
	q := `SELECT ` + strings.Join(cols, ", ") + ` FROM events WHERE ` + webVisitorPredicate + ` IN (` + placeholders + `)
		AND ` + tsExpr + ` IS NOT NULL ORDER BY ` + tsExpr + ` ` + order + `, id ` + order + ` LIMIT 1`
	var t Touch
	var ts sql.NullFloat64
	err := db.QueryRow(q, args...).Scan(&ts, &t.VisitorID, &t.Source, &t.Medium, &t.Campaign, &t.Term, &t.Content, &t.Referrer, &t.Landing)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.TS = int64(ts.Float64)
	return &t, nil
}

type attributionRow struct {
	Value  string `json:"value"`
	Events int64  `json:"events"`
	Users  int    `json:"users"`
}

// attributionReport groups events matching f by the acquisition touch of the
// user behind each event. actorKey names where the actor is ("user_id" or a
// "props.<key>" holding a user ID or an alias such as a billing customer ID),
// so lifecycle events from any app join without that app changing.
func attributionReport(db sqlRunner, f Filter, actorKey, touch, by string, limit int) (map[string]any, error) {
	actorExpr := "user_id"
	if actorKey != defaultAttributionKey {
		expr, ok := propsExtract(actorKey)
		if !ok {
			return nil, fmt.Errorf("actor_key must be %q or \"props.<key>\"", defaultAttributionKey)
		}
		actorExpr = expr
	}
	if touch != "first" && touch != "last" {
		return nil, errors.New("touch must be first or last")
	}
	known := false
	for _, field := range touchFields {
		known = known || field == by
	}
	if !known {
		return nil, fmt.Errorf("by must be one of %s", strings.Join(touchFields, ", "))
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	where, args, err := f.buildWhere()
	if err != nil {
		return nil, err
	}
	if where != "" {
		where += " AND "
	}
	total, err := countEvents(db, f)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT CAST(`+actorExpr+` AS TEXT) AS actor, COUNT(*) FROM events WHERE `+where+
		actorExpr+` IS NOT NULL AND CAST(`+actorExpr+` AS TEXT) != '' GROUP BY actor ORDER BY COUNT(*) DESC LIMIT ?`,
		append(args, maxAttributionActors+1)...)
	if err != nil {
		return nil, err
	}
	type actorCount struct {
		actor string
		n     int64
	}
	var actors []actorCount
	for rows.Next() {
		var a actorCount
		if err := rows.Scan(&a.actor, &a.n); err != nil {
			rows.Close()
			return nil, err
		}
		actors = append(actors, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	truncated := len(actors) > maxAttributionActors
	if truncated {
		actors = actors[:maxAttributionActors]
	}
	buckets := map[string]*attributionRow{}
	users := map[string]map[string]bool{}
	cache := map[string]*Touch{}
	var withActor int64
	for _, a := range actors {
		withActor += a.n
		userID := resolveUser(db, f.ProjectID, a.actor)
		t, seen := cache[userID]
		if !seen {
			attribution, err := userAttribution(db, f.ProjectID, userID)
			if err != nil {
				return nil, err
			}
			t = attribution.FirstTouch
			if touch == "last" {
				t = attribution.LastTouch
			}
			cache[userID] = t
		}
		value := attributionNone
		if t != nil {
			value = t.field(by)
			if value == "" {
				value = attributionNotSet
			}
		}
		if buckets[value] == nil {
			buckets[value] = &attributionRow{Value: value}
			users[value] = map[string]bool{}
		}
		buckets[value].Events += a.n
		users[value][userID] = true
	}
	out := make([]attributionRow, 0, len(buckets))
	for value, row := range buckets {
		row.Users = len(users[value])
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Events != out[j].Events {
			return out[i].Events > out[j].Events
		}
		return out[i].Value < out[j].Value
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return map[string]any{
		"touch": touch, "by": by, "actor_key": actorKey,
		"rows":                 out,
		"events":               total,
		"events_without_actor": total - withActor,
		"actors":               len(actors),
		"truncated":            truncated,
	}, nil
}

// ─── MCP tools ────────────────────────────────────────────────────────

func (a *App) identityTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "analytics_identify",
			Description: "Link an anonymous website visitor to an authenticated user in the current project. Call it from a backend after sign-up or sign-in, never with browser-supplied identity. Args: user_id (required), visitor_id? (the tag's apa_vid cookie or apa.visitorId()), aliases? (other IDs this user has in other apps, e.g. a billing customer ID, max 20). Backfills the visitor's anonymous tag events and returns {user_id, backfilled, previous_user_id?, attribution: {first_touch, last_touch, visitors}}.",
			InputSchema: schemaObject(map[string]any{
				"user_id":    map[string]any{"type": "string"},
				"visitor_id": map[string]any{"type": "string"},
				"aliases":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}, []string{"user_id"}),
			Handler: a.toolIdentify,
		},
		{
			Name:        "analytics_user_attribution",
			Description: "First and latest acquisition touch (utm source/medium/campaign/term/content, referrer, landing page) for a user in the current project, across every visitor linked by analytics_identify. Args: user_id (required; an alias also resolves).",
			InputSchema: schemaObject(map[string]any{
				"user_id": map[string]any{"type": "string"},
			}, []string{"user_id"}),
			Handler: a.toolUserAttribution,
		},
		{
			Name:        "analytics_attribution",
			Description: "Break down events (e.g. sign-ups, trials, payments) by the acquisition of the user behind them. Args: topic (required), app?, since?, until?, where?, actor_key? (\"user_id\" by default, or \"props.<key>\" holding a user ID or alias such as props.customer_id), touch? (first|last, default first), by? (source|medium|campaign|term|content|referrer|landing, default source), limit? (default 20). Returns rows [{value, events, users}]; \"(none)\" means no known touch.",
			InputSchema: schemaObject(map[string]any{
				"topic":     map[string]any{"type": "string"},
				"app":       map[string]any{"type": "string"},
				"since":     map[string]any{"type": "integer"},
				"until":     map[string]any{"type": "integer"},
				"where":     map[string]any{"type": "object"},
				"actor_key": map[string]any{"type": "string"},
				"touch":     map[string]any{"type": "string", "enum": []string{"first", "last"}},
				"by":        map[string]any{"type": "string", "enum": touchFields},
				"limit":     map[string]any{"type": "integer"},
			}, []string{"topic"}),
			Handler: a.toolAttribution,
		},
	}
}

func (a *App) toolIdentify(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	userID, err := cleanIdentityValue("user_id", stringArg(args, "user_id"), true)
	if err != nil {
		return nil, err
	}
	visitorID, err := cleanIdentityValue("visitor_id", stringArg(args, "visitor_id"), false)
	if err != nil {
		return nil, err
	}
	var aliases []string
	if raw, ok := args["aliases"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return nil, errors.New("aliases must be an array of strings")
		}
		if len(list) > maxIdentityAliases {
			return nil, fmt.Errorf("at most %d aliases", maxIdentityAliases)
		}
		for _, item := range list {
			text, ok := item.(string)
			if !ok {
				return nil, errors.New("aliases must be an array of strings")
			}
			alias, err := cleanIdentityValue("alias", text, true)
			if err != nil {
				return nil, err
			}
			aliases = append(aliases, alias)
		}
	}
	return identify(toolWriter(ctx), projectID, userID, visitorID, aliases)
}

func (a *App) toolUserAttribution(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	actor, err := cleanIdentityValue("user_id", stringArg(args, "user_id"), true)
	if err != nil {
		return nil, err
	}
	db := toolReader(ctx)
	return userAttribution(db, projectID, resolveUser(db, projectID, actor))
}

func (a *App) toolAttribution(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if strings.TrimSpace(stringArg(args, "topic")) == "" {
		return nil, errors.New("topic required")
	}
	f, err := scopedFilter(ctx, args)
	if err != nil {
		return nil, err
	}
	actorKey := stringArg(args, "actor_key")
	if actorKey == "" {
		actorKey = defaultAttributionKey
	}
	touch := stringArg(args, "touch")
	if touch == "" {
		touch = "first"
	}
	by := stringArg(args, "by")
	if by == "" {
		by = "source"
	}
	return attributionReport(toolReader(ctx), f, actorKey, touch, by, intArg(args, "limit"))
}
