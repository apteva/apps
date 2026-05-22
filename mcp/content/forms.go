// core/form block — submission storage, template substitution, and
// cross-app action execution.
//
// A form block stores its config inline in the post's body_blocks
// JSON (fields, actions, success). When a visitor submits, the
// handler:
//
//   1. Resolves the post + block from the URL's block_id (block IDs
//      are unique within a project; a LIKE prefilter narrows the
//      candidate posts before we parse JSON to find the actual block).
//   2. Validates the payload (required fields, honeypot, rate limit).
//   3. Runs each action — { app, tool, args } — by calling the
//      bound app's MCP tool via ctx.PlatformAPI().CallAppResult.
//      args may template {{ field_name }} and {{ steps.N.path }};
//      outputs feed the chain so step N+1 can use step N's id, etc.
//   4. Records the submission (payload + per-action results) into
//      form_submissions regardless of action outcomes.
//   5. Emits the project-scoped 'form.submitted' event so panels and
//      other apps can react live.
//
// Action semantics:
//   on_failure: abort    — first failing action stops the chain;
//                          status="partial"
//   on_failure: continue — every action runs; status="ok" iff all
//                          succeeded, "partial" otherwise

package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ── DB layer ─────────────────────────────────────────────────────

type FormSubmission struct {
	ID        int64          `json:"id"`
	ProjectID string         `json:"project_id,omitempty"`
	SiteID    int64          `json:"site_id"`
	PostID    int64          `json:"post_id"`
	BlockID   string         `json:"block_id"`
	Payload   map[string]any `json:"payload"`
	IPHash    string         `json:"ip_hash,omitempty"`
	UserAgent string         `json:"user_agent,omitempty"`
	Status    string         `json:"status"`
	Results   []ActionResult `json:"results"`
	Error     string         `json:"error,omitempty"`
	CreatedAt int64          `json:"created_at"`
}

// ActionResult is one entry in form_submissions.results — what each
// (app, tool, args) call returned. Output is the unwrapped MCP tool
// result; Error is set iff the call returned a non-nil err.
type ActionResult struct {
	App    string `json:"app"`
	Tool   string `json:"tool"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Output any    `json:"output,omitempty"`
}

func dbInsertFormSubmission(db *sql.DB, s FormSubmission) (int64, error) {
	payloadJSON, _ := json.Marshal(s.Payload)
	resultsJSON, _ := json.Marshal(s.Results)
	res, err := db.Exec(`
        INSERT INTO form_submissions
        (project_id, site_id, post_id, block_id, payload, ip_hash, user_agent, status, results, error, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, s.ProjectID, s.SiteID, s.PostID, s.BlockID, string(payloadJSON),
		s.IPHash, s.UserAgent, s.Status, string(resultsJSON), s.Error, s.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbGetFormSubmission(db *sql.DB, projectID string, id int64) (*FormSubmission, error) {
	row := db.QueryRow(`
        SELECT id, project_id, site_id, post_id, block_id, payload, ip_hash, user_agent, status, results, error, created_at
        FROM form_submissions WHERE project_id=? AND id=?
    `, projectID, id)
	return scanFormSubmission(row)
}

func dbListFormSubmissions(db *sql.DB, projectID, blockID string, limit int) ([]FormSubmission, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	q := `SELECT id, project_id, site_id, post_id, block_id, payload, ip_hash, user_agent, status, results, error, created_at
          FROM form_submissions WHERE project_id=?`
	args := []any{projectID}
	if blockID != "" {
		q += ` AND block_id=?`
		args = append(args, blockID)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FormSubmission
	for rows.Next() {
		s, err := scanFormSubmission(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func scanFormSubmission(row rowScanner) (*FormSubmission, error) {
	var s FormSubmission
	var payload, results, ipHash, ua, errStr sql.NullString
	var siteID sql.NullInt64
	if err := row.Scan(&s.ID, &s.ProjectID, &siteID, &s.PostID, &s.BlockID,
		&payload, &ipHash, &ua, &s.Status, &results, &errStr, &s.CreatedAt); err != nil {
		return nil, err
	}
	if siteID.Valid {
		s.SiteID = siteID.Int64
	}
	if payload.Valid && payload.String != "" {
		_ = json.Unmarshal([]byte(payload.String), &s.Payload)
	}
	if results.Valid && results.String != "" {
		_ = json.Unmarshal([]byte(results.String), &s.Results)
	}
	s.IPHash = ipHash.String
	s.UserAgent = ua.String
	s.Error = errStr.String
	return &s, nil
}

// ── form-block discovery ─────────────────────────────────────────

// dbFindFormBlock locates a core/form block by its unique block id.
// Block IDs are 8-hex-char strings (b_<hex>); the LIKE prefilter
// narrows candidate posts, then we parse each one's body_blocks to
// find the exact block (and confirm the type). Returns (post, block,
// nil) on hit; (nil, nil, sql.ErrNoRows) when not found.
//
// Scoped to non-deleted, non-archived posts so a deleted form can't
// be re-submitted.
func dbFindFormBlock(db *sql.DB, projectID, blockID string) (*Post, *Block, error) {
	if blockID == "" {
		return nil, nil, errors.New("block id required")
	}
	needle := `"id":"` + blockID + `"`
	rows, err := db.Query(`
        SELECT id, project_id, site_id, kind, slug, status, body_blocks
        FROM posts
        WHERE project_id = ? AND deleted_at IS NULL AND status = 'published'
          AND body_blocks LIKE ?
    `, projectID, "%"+needle+"%")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p Post
		var siteID sql.NullInt64
		var body string
		if err := rows.Scan(&p.ID, &p.ProjectID, &siteID, &p.Kind, &p.Slug, &p.Status, &body); err != nil {
			return nil, nil, err
		}
		if siteID.Valid {
			p.SiteID = siteID.Int64
		}
		doc, err := parseDocument(body)
		if err != nil {
			continue
		}
		if b := findBlockByID(doc.Blocks, blockID); b != nil {
			p.BodyBlocks = doc
			return &p, b, nil
		}
	}
	return nil, nil, sql.ErrNoRows
}

func findBlockByID(bs []Block, id string) *Block {
	for i := range bs {
		if bs[i].ID == id {
			return &bs[i]
		}
		if hit := findBlockByID(bs[i].Inner, id); hit != nil {
			return hit
		}
	}
	return nil
}

// ── template substitution ────────────────────────────────────────

var tplRe = regexp.MustCompile(`\{\{\s*([\w\.]+)\s*\}\}`)

// substituteTemplates walks v (string, map, slice) and replaces every
// `{{ path }}` occurrence inside string values. If a string is
// EXACTLY one template token (`"{{ steps.0.user_id }}"`), the resolved
// value's native type is preserved (so an integer user_id stays an
// integer instead of being stringified). Otherwise the substitution
// is text-only ("{{ first_name }} <{{ email }}>" → "Ada <ada@x.com>").
func substituteTemplates(v any, ctx map[string]any) any {
	switch x := v.(type) {
	case string:
		// Single-token fast path — preserve the resolved value's type.
		if m := tplRe.FindStringSubmatch(x); len(m) == 2 && m[0] == strings.TrimSpace(x) {
			return resolvePath(ctx, m[1])
		}
		return tplRe.ReplaceAllStringFunc(x, func(match string) string {
			sub := tplRe.FindStringSubmatch(match)
			if len(sub) < 2 {
				return match
			}
			return fmt.Sprintf("%v", resolvePath(ctx, sub[1]))
		})
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = substituteTemplates(vv, ctx)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = substituteTemplates(vv, ctx)
		}
		return out
	default:
		return v
	}
}

func resolvePath(ctx map[string]any, p string) any {
	parts := strings.Split(p, ".")
	var cur any = ctx
	for _, k := range parts {
		switch c := cur.(type) {
		case map[string]any:
			cur = c[k]
		case []any:
			idx, err := strconv.Atoi(k)
			if err != nil || idx < 0 || idx >= len(c) {
				return ""
			}
			cur = c[idx]
		default:
			return ""
		}
	}
	if cur == nil {
		return ""
	}
	return cur
}

// ── action runner ────────────────────────────────────────────────

// runFormActions executes the action chain for one submission.
// Returns (results, status, err) where status is "ok" (all
// succeeded), "partial" (continue mode + some failed), or "partial"
// (abort mode + first failure stopped the chain). err is the first
// action error in abort mode; nil otherwise (even if results contains
// failures, in continue mode).
func runFormActions(ctx *sdk.AppCtx, pid string, actions []any, payload map[string]any, onFailure string) ([]ActionResult, string, error) {
	results := make([]ActionResult, 0, len(actions))
	chainCtx := map[string]any{}
	for k, v := range payload {
		chainCtx[k] = v
	}
	chainCtx["steps"] = []any{}

	var firstErr error
	allOK := true
	for _, a := range actions {
		act, ok := a.(map[string]any)
		if !ok {
			continue
		}
		appName, _ := act["app"].(string)
		tool, _ := act["tool"].(string)
		if appName == "" || tool == "" {
			allOK = false
			results = append(results, ActionResult{App: appName, Tool: tool, OK: false, Error: "app and tool required"})
			continue
		}
		rawArgs, _ := act["args"].(map[string]any)
		substituted, _ := substituteTemplates(rawArgs, chainCtx).(map[string]any)
		if substituted == nil {
			substituted = map[string]any{}
		}
		// Cross-app calls to project-scoped sibling apps need the
		// originating project_id when content itself is global-scoped
		// or when the sibling enforces project resolution from args.
		if _, has := substituted["_project_id"]; !has {
			substituted["_project_id"] = pid
		}

		var out any
		callErr := ctx.PlatformAPI().CallAppResult(appName, tool, substituted, &out)
		res := ActionResult{App: appName, Tool: tool, OK: callErr == nil}
		if callErr != nil {
			res.Error = callErr.Error()
			allOK = false
			if firstErr == nil {
				firstErr = callErr
			}
		} else {
			res.Output = out
		}
		results = append(results, res)

		steps, _ := chainCtx["steps"].([]any)
		steps = append(steps, out)
		chainCtx["steps"] = steps

		if callErr != nil && onFailure != "continue" {
			return results, "partial", callErr
		}
	}
	if !allOK {
		return results, "partial", nil
	}
	return results, "ok", nil
}

// ── rate limiter ─────────────────────────────────────────────────

// Per-IP-hash sliding window. 5 submissions per 60s. In-memory,
// per-sidecar — sufficient for v1 (form-submit is rare; abusing a
// single form across multiple sidecars defeats this but defeats most
// per-instance limits anyway). The map is bounded by occasional
// pruning of expired entries.
const (
	formRateLimitWindow = 60 * time.Second
	formRateLimitMax    = 5
)

var (
	formRateMu  sync.Mutex
	formRateLog = map[string][]int64{}
)

func formRateLimitOK(ipHash string) bool {
	if ipHash == "" {
		return true
	}
	now := time.Now().Unix()
	cutoff := now - int64(formRateLimitWindow.Seconds())
	formRateMu.Lock()
	defer formRateMu.Unlock()
	hits := formRateLog[ipHash]
	kept := hits[:0]
	for _, t := range hits {
		if t > cutoff {
			kept = append(kept, t)
		}
	}
	if len(kept) >= formRateLimitMax {
		formRateLog[ipHash] = kept
		return false
	}
	kept = append(kept, now)
	formRateLog[ipHash] = kept
	// Occasional pruning: every ~256 inserts, drop entries whose
	// entire log is expired. Cheap and keeps the map bounded.
	if len(formRateLog)&0xff == 0 {
		for k, v := range formRateLog {
			fresh := false
			for _, t := range v {
				if t > cutoff {
					fresh = true
					break
				}
			}
			if !fresh {
				delete(formRateLog, k)
			}
		}
	}
	return true
}

// ── validation ───────────────────────────────────────────────────

// validateFormPayload enforces the block's `fields` declaration:
// every field with required=true must have a non-empty submitted
// value. Type coercion / format checking (email shape, etc) is left
// to the action target — pushing all validation into one place means
// CRM's own email-format check is what blocks "bad" addresses, not a
// duplicate regex here.
func validateFormPayload(fields []any, payload map[string]any) error {
	for _, f := range fields {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		name, _ := fm["name"].(string)
		if name == "" {
			continue
		}
		required, _ := fm["required"].(bool)
		if !required {
			continue
		}
		v, present := payload[name]
		if !present {
			return fmt.Errorf("field %q is required", name)
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			return fmt.Errorf("field %q is required", name)
		}
	}
	return nil
}
