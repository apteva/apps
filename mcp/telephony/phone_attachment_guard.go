package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

var errPhoneSessionChanged = errors.New("media session generation changed")

// Separate from observational metadata. Supported clients opt into conditional
// replacement; legacy clients keep their existing explicitly authorized behavior.
type phoneAttachmentGuard struct {
	ID       string `json:"id"`
	Expected string `json:"expected_generation"`
}

func (g *phoneAttachmentGuard) valid() bool {
	return g.ID != "" && safeAudioDiagnosticID(g.ID) == g.ID && (g.Expected == "" || safeAudioDiagnosticID(g.Expected) == g.Expected)
}

type phoneAttachmentEntry struct {
	session  softphoneSession
	expected string
	until    time.Time
}
type phoneAttachmentCache struct {
	mu      sync.Mutex
	entries map[string]phoneAttachmentEntry
}

// Caller holds the per-call claim lock. This cache contains credentials only in
// bounded process memory, never logs or persistence. A retry must still own the
// call and its cached generation must still be current; no cached response extends a live lease. Expired leases require fresh
// authorization and a conditional successor.
func (a *App) issueGuardedPhoneSession(ctx context.Context, row *callRow, p *phonePrincipal, d audioSessionCorrelation, g *phoneAttachmentGuard) (*softphoneSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if g == nil {
		return a.issuePhoneSession(row, p, d)
	}
	if !g.valid() {
		return nil, errPhoneSessionChanged
	}
	principal := ""
	if p != nil {
		fresh, err := a.phonePrincipal(row.ProjectID, p.Identity)
		if err != nil {
			return nil, err
		}
		if !a.phoneCallAllowed(fresh, row, false) {
			return nil, errPhoneAccessDenied
		}
		principal = p.Identity.key()
	}
	key := row.ProjectID + "\x00" + row.ID + "\x00" + principal + "\x00" + g.ID
	c := &a.phoneAttachments
	now := time.Now()
	c.mu.Lock()
	cached, ok := c.entries[key]
	c.mu.Unlock()
	if ok && now.Before(cached.until) {
		if cached.expected != g.Expected {
			return nil, errPhoneSessionChanged
		}
		var hash, owner, raw string
		var expires int64
		err := a.db().db.QueryRowContext(ctx, `SELECT token_hash,principal,expires_at,diagnostic_json FROM telephony_media_sessions WHERE call_id=? AND project_id=?`, row.ID, row.ProjectID).Scan(&hash, &owner, &expires, &raw)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, errPhoneSessionChanged
			}
			return nil, err
		}
		var current audioSessionCorrelation
		_ = json.Unmarshal([]byte(raw), &current)
		if owner != principal || current.SessionID != cached.session.SessionGeneration || !secureEqual(hash, phoneHash(cached.session.SessionToken)) {
			return nil, errPhoneSessionChanged
		}
		if expires <= now.Unix() {
			// The cached lease cannot be reused. Fresh HTTP authorization above
			// permits a conditional successor only while this attempt's expired
			// generation remains current. It never displaces another session.
			next := *g
			next.Expected = current.SessionID
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			v, err := a.issuePhoneSessionConditional(row, p, &next, d)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			c.entries[key] = phoneAttachmentEntry{*v, g.Expected, now.Add(5 * time.Minute)}
			c.mu.Unlock()
			return v, nil
		}
		v := cached.session
		v.LeaseSeconds = int(expires - now.Unix())
		return &v, nil
	}
	// Check cancellation again after permission checks. The short SQL issuance
	// then completes and fills the retry cache even if the response is lost.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A missing row cannot satisfy a nonempty expected generation (INSERT would
	// otherwise bypass ON CONFLICT's condition).
	var exists bool
	if err := a.db().db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM telephony_media_sessions WHERE call_id=? AND project_id=?)`, row.ID, row.ProjectID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists && g.Expected != "" {
		return nil, errPhoneSessionChanged
	}
	v, err := a.issuePhoneSessionConditional(row, p, g, d)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]phoneAttachmentEntry{}
	}
	for k, e := range c.entries {
		if !now.Before(e.until) {
			delete(c.entries, k)
		}
	}
	if len(c.entries) >= 512 {
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = phoneAttachmentEntry{*v, g.Expected, now.Add(5 * time.Minute)}
	return v, nil
}
