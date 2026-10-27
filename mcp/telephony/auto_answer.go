package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type autoAnswerWarningPolicy struct {
	Enabled  bool `json:"enabled"`
	WindowMS int  `json:"window_ms"`
}
type autoAnswerPolicy struct {
	WarningTone          *autoAnswerWarningPolicy `json:"warning_tone,omitempty"`
	Mode                 string                   `json:"mode"`
	DelayMS              int                      `json:"delay_ms"`
	AllowAdviserOverride *bool                    `json:"allow_adviser_override,omitempty"`
}
type autoAnswerEffective struct {
	WarningTone          *autoAnswerWarningPolicy `json:"warning_tone,omitempty"`
	Mode                 string                   `json:"mode"`
	DelayMS              int                      `json:"delay_ms"`
	Source               string                   `json:"source"`
	AllowAdviserOverride bool                     `json:"allow_adviser_override"`
}

func (p autoAnswerPolicy) validate() error {
	if p.WarningTone != nil && p.WarningTone.WindowMS != 0 && (p.WarningTone.WindowMS < 750 || p.WarningTone.WindowMS > 3000) {
		return errors.New("warning window must be between 750 and 3000 ms")
	}
	if p.Mode != "manual" && p.Mode != "automatic" && p.Mode != "inherit" {
		return errors.New("answer mode must be inherit, manual or automatic")
	}
	if p.DelayMS < 0 || p.DelayMS > 10000 {
		return errors.New("automatic answer delay must be between 0 and 10000 ms")
	}
	return nil
}
func (a *App) readAutoAnswerPolicy(project, scope, id string) (autoAnswerPolicy, bool, error) {
	var raw string
	err := a.db().db.QueryRow(`SELECT policy_json FROM telephony_auto_answer_policies WHERE project_id=? AND scope=? AND scope_id=?`, project, scope, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return autoAnswerPolicy{Mode: "inherit"}, false, nil
	}
	var p autoAnswerPolicy
	if err == nil {
		err = json.Unmarshal([]byte(raw), &p)
	}
	if err == nil {
		err = p.validate()
	}
	return p, err == nil, err
}
func (a *App) effectiveAutoAnswer(project, principal, route, dest string) (autoAnswerEffective, error) {
	e := autoAnswerEffective{Mode: "manual", Source: "project", AllowAdviserOverride: true}
	for _, target := range [][2]string{{"project", ""}, {"route", route}, {"destination", dest}, {"adviser", principal}} {
		if target[0] != "project" && target[1] == "" {
			continue
		}
		if target[0] == "adviser" && !e.AllowAdviserOverride {
			continue
		}
		p, have, err := a.readAutoAnswerPolicy(project, target[0], target[1])
		if err != nil {
			return e, err
		}
		if !have {
			continue
		}
		if p.Mode != "inherit" {
			e.Mode, e.DelayMS, e.Source = p.Mode, p.DelayMS, target[0]
			e.WarningTone = p.WarningTone
			if e.WarningTone != nil && e.WarningTone.WindowMS == 0 {
				w := *e.WarningTone
				w.WindowMS = 1000
				e.WarningTone = &w
			}
		}
		if target[0] != "adviser" && p.AllowAdviserOverride != nil {
			e.AllowAdviserOverride = e.AllowAdviserOverride && *p.AllowAdviserOverride
		}
	}
	return e, nil
}

// Platform operators and application advisers each retain their authenticated identity.
func autoAnswerOwner(r *http.Request) string {
	if p := phoneUserFrom(r); p != nil {
		return p.Identity.key()
	}
	if principal, err := sdk.PrincipalFromRequest(r); err == nil && principal != nil && principal.UserID > 0 {
		return "operator:" + strconv.FormatInt(principal.UserID, 10)
	}
	if id, err := strconv.ParseInt(r.Header.Get("X-Apteva-Operator-ID"), 10, 64); err == nil && id > 0 {
		return "operator:" + strconv.FormatInt(id, 10)
	}
	return "operator" // trusted machine/API context without a platform-user header
}
func autoAnswerSessionID(id string) bool {
	return len(id) >= 16 && len(id) <= 128 && !strings.ContainsAny(id, " /\\\t\r\n")
}

func (a *App) handleAutoAnswer(w http.ResponseWriter, r *http.Request, project string) {
	w.Header().Set("Cache-Control", "private, no-store")
	var args struct {
		Scope            string            `json:"scope"`
		ScopeID          string            `json:"scope_id"`
		Identity         *phoneIdentity    `json:"identity,omitempty"`
		Policy           *autoAnswerPolicy `json:"policy,omitempty"`
		Inherit          bool              `json:"inherit,omitempty"`
		SessionID        string            `json:"session_id"`
		Available        bool              `json:"available"`
		WarningSupported bool              `json:"warning_tone_supported"`
		Takeover         bool              `json:"takeover"`
		CallID           string            `json:"call_id"`
		DestinationID    string            `json:"destination_id"`
		Cancel           bool              `json:"cancel"`
	}
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "GET or POST required", 405)
		return
	}
	if r.Method == "POST" {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&args) != nil {
			http.Error(w, "invalid auto-answer request", 400)
			return
		}
	}
	p := phoneUserFrom(r)
	principal := autoAnswerOwner(r)
	a.autoAnswerMu.Lock()
	defer a.autoAnswerMu.Unlock()
	switch r.URL.Path {
	case "/softphone/auto-answer-policy":
		scope, id := args.Scope, args.ScopeID
		if scope == "" {
			scope = "adviser"
		}
		if p != nil && (scope != "adviser" || args.Identity != nil || id != "") {
			http.Error(w, "adviser identity comes from authentication", 403)
			return
		}
		if scope == "adviser" {
			id = principal
			if args.Identity != nil {
				if !args.Identity.valid() {
					http.Error(w, "complete identity required", 400)
					return
				}
				if _, err := a.phonePrincipal(project, *args.Identity); err != nil {
					http.Error(w, "adviser unavailable", 403)
					return
				}
				id = args.Identity.key()
			}
		} else if scope == "project" {
			id = ""
		} else if scope == "route" {
			row, err := a.db().findRoute(id)
			if err != nil || row == nil || row.ProjectID != project {
				http.Error(w, "route not found", 404)
				return
			}
		} else if scope == "destination" {
			row, err := a.findRoutingDestination(project, id)
			if err != nil || row == nil || row.Kind != "browser" {
				http.Error(w, "browser destination not found", 404)
				return
			}
		} else {
			http.Error(w, "invalid policy scope", 400)
			return
		}
		effective, err := a.effectiveAutoAnswer(project, id, "", "")
		if err != nil {
			http.Error(w, "policy unavailable", 503)
			return
		}
		if (args.Policy != nil || args.Inherit) && p != nil && !effective.AllowAdviserOverride {
			http.Error(w, "adviser overrides are disabled", 403)
			return
		}
		if args.Policy != nil || args.Inherit {
			if args.Inherit {
				if scope == "project" {
					http.Error(w, "project cannot inherit", 400)
					return
				}
				_, err = a.db().db.Exec(`DELETE FROM telephony_auto_answer_policies WHERE project_id=? AND scope=? AND scope_id=?`, project, scope, id)
			} else {
				if err = args.Policy.validate(); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				if scope == "adviser" && args.Policy.AllowAdviserOverride != nil {
					http.Error(w, "only administrator defaults control overrides", 400)
					return
				}
				if scope == "project" && args.Policy.Mode == "inherit" {
					http.Error(w, "project cannot inherit", 400)
					return
				}
				raw, _ := json.Marshal(args.Policy)
				_, err = a.db().db.Exec(`INSERT INTO telephony_auto_answer_policies VALUES(?,?,?,?,?) ON CONFLICT(project_id,scope,scope_id) DO UPDATE SET policy_json=excluded.policy_json,updated_at=excluded.updated_at`, project, scope, id, string(raw), ringTime(time.Now()))
			}
			if err != nil {
				http.Error(w, "policy write unavailable", 503)
				return
			}
		}
		effective, err = a.effectiveAutoAnswer(project, func() string {
			if scope == "adviser" {
				return id
			}
			return ""
		}(), func() string {
			if scope == "route" {
				return id
			}
			return ""
		}(), func() string {
			if scope == "destination" {
				return id
			}
			return ""
		}())
		if err != nil {
			http.Error(w, "policy unavailable", 503)
			return
		}
		policy, configured, err := a.readAutoAnswerPolicy(project, scope, id)
		if err != nil {
			http.Error(w, "policy unavailable", 503)
			return
		}
		result := map[string]any{"scope": scope, "scope_id": id, "policy": policy, "configured": configured, "effective": effective, "admin": p == nil}
		if p == nil {
			access, err := a.phonePolicy(project)
			if err != nil {
				http.Error(w, "advisers unavailable", 503)
				return
			}
			var advisers []phoneIdentity
			for _, u := range access.Users {
				if u.Enabled {
					advisers = append(advisers, u.Identity)
				}
			}
			result["advisers"] = advisers
			type target struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			var routes, dests []target
			rows, err := a.db().db.Query(`SELECT id,phone_number FROM inbound_routes WHERE project_id=? ORDER BY phone_number`, project)
			if err != nil {
				http.Error(w, "routes unavailable", 503)
				return
			}
			for rows.Next() {
				var v target
				if err = rows.Scan(&v.ID, &v.Name); err != nil {
					break
				}
				routes = append(routes, v)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				http.Error(w, "routes unavailable", 503)
				return
			}
			rows, err = a.db().db.Query(`SELECT id,name FROM routing_destinations WHERE project_id=? AND kind='browser' ORDER BY name`, project)
			if err != nil {
				http.Error(w, "destinations unavailable", 503)
				return
			}
			for rows.Next() {
				var v target
				if err = rows.Scan(&v.ID, &v.Name); err != nil {
					break
				}
				dests = append(dests, v)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				http.Error(w, "destinations unavailable", 503)
				return
			}
			result["routes"], result["destinations"] = routes, dests
		}
		writeJSON(w, result)
	case "/softphone/auto-answer-session":
		if r.Method != "POST" || !autoAnswerSessionID(args.SessionID) {
			http.Error(w, "valid browser session required", 400)
			return
		}
		now := time.Now().UnixMilli()
		if !args.Available {
			_, err := a.db().db.Exec(`DELETE FROM telephony_auto_answer_sessions WHERE project_id=? AND principal=? AND session_id=?`, project, principal, args.SessionID)
			if err != nil {
				http.Error(w, "session unavailable", 503)
				return
			}
			_, err = a.db().db.Exec(`DELETE FROM telephony_auto_answer_reservations WHERE project_id=? AND principal=? AND session_id=? AND call_id IN (SELECT id FROM calls WHERE status='pending')`, project, principal, args.SessionID)
			if err != nil {
				http.Error(w, "reservation unavailable", 503)
				return
			}
			writeJSON(w, map[string]any{"designated": false})
			return
		}
		// Expiry is indexed and small. Heartbeats never scan historical calls.
		_, err := a.db().db.Exec(`DELETE FROM telephony_auto_answer_sessions WHERE expires_at<=?`, now)
		if err != nil {
			http.Error(w, "session unavailable", 503)
			return
		}
		res, err := a.db().db.Exec(`INSERT INTO telephony_auto_answer_sessions(project_id,principal,session_id,expires_at,warning_supported) VALUES(?,?,?,?,?) ON CONFLICT(project_id,principal) DO UPDATE SET session_id=excluded.session_id,expires_at=excluded.expires_at,warning_supported=excluded.warning_supported WHERE telephony_auto_answer_sessions.session_id=excluded.session_id OR ?`, project, principal, args.SessionID, now+30000, args.WarningSupported, args.Takeover)
		if err != nil {
			http.Error(w, "session unavailable", 503)
			return
		}
		n, err := res.RowsAffected()
		if err != nil {
			http.Error(w, "session unavailable", 503)
			return
		}
		writeJSON(w, map[string]any{"designated": n == 1, "lease_ms": 30000})
	case "/softphone/auto-answer-offer":
		if r.Method != "POST" || !autoAnswerSessionID(args.SessionID) || args.CallID == "" {
			http.Error(w, "session and call required", 400)
			return
		}
		if args.Cancel {
			_, err := a.db().db.Exec(`DELETE FROM telephony_auto_answer_reservations WHERE project_id=? AND principal=? AND session_id=? AND call_id=?`, project, principal, args.SessionID, args.CallID)
			if err != nil {
				http.Error(w, "reservation unavailable", 503)
				return
			}
			writeJSON(w, map[string]any{"canceled": true})
			return
		}
		row, err := a.db().findCall(args.CallID)
		if err != nil {
			http.Error(w, "call unavailable", 503)
			return
		}
		if row == nil || row.ProjectID != project {
			http.Error(w, "call not found", 404)
			return
		}
		dest := args.DestinationID
		if p != nil {
			dest = a.phoneOfferDestination(p, row, dest)
			if dest == "" {
				writePhoneOfferExpired(w)
				return
			}
		}
		if row.Status != "pending" || row.Direction != "inbound" {
			http.Error(w, "call no longer offered", 409)
			return
		}
		offers, err := a.db().activeRingOffers(row.ID, project)
		if err != nil {
			http.Error(w, "offers unavailable", 503)
			return
		}
		if row.PeerKind != peerKindHuman && !ringHasBrowser(offers) {
			http.Error(w, "browser offer required", 409)
			return
		}
		if dest == "" {
			for _, o := range offers {
				if o.Kind == "browser" {
					dest = o.DestinationID
					break
				}
			}
		}
		effective, err := a.effectiveAutoAnswer(project, principal, row.RouteID, dest)
		if err != nil {
			http.Error(w, "policy unavailable", 503)
			return
		}
		if effective.Mode != "automatic" {
			writeJSON(w, map[string]any{"eligible": false, "effective": effective})
			return
		}
		var selected *ringOffer
		for i := range offers {
			if offers[i].Kind == "browser" && offers[i].DestinationID == dest {
				selected = &offers[i]
				break
			}
		}
		if selected == nil {
			writePhoneOfferExpired(w)
			return
		}
		if effective.WarningTone != nil && effective.WarningTone.Enabled {
			var supported bool
			err = a.db().db.QueryRow(`SELECT warning_supported FROM telephony_auto_answer_sessions WHERE project_id=? AND principal=? AND session_id=? AND expires_at>?`, project, principal, args.SessionID, time.Now().UnixMilli()).Scan(&supported)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				writeJSONStatus(w, 503, map[string]any{"code": "auto_answer_session_unavailable"})
				return
			}
			if err != nil || !supported {
				writeJSONStatus(w, 409, map[string]any{"code": "auto_answer_warning_unsupported"})
				return
			}
		}
		if err = a.reserveAutoAnswer(project, principal, args.SessionID, row.ID, dest, effective.DelayMS); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		log.Printf("auto_answer reserved at=%s call=%s project=%s principal_hash=%s source=%s delay_ms=%d", ringTime(time.Now()), row.ID, project, phoneHash(principal)[:12], effective.Source, effective.DelayMS)
		writeJSON(w, map[string]any{"eligible": true, "call_id": row.ID, "destination_id": dest, "offer_id": selected.ID, "offer_ttl_ms": autoAnswerOfferTTL(selected.ExpiresAt), "effective": effective})
	default:
		http.NotFound(w, r)
	}
}
func autoAnswerOfferTTL(expires string) int64 {
	at, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return 0
	}
	return max(0, time.Until(at).Milliseconds())
}

func (a *App) reserveAutoAnswer(project, principal, session, call, dest string, delay int) error {
	now := time.Now().UnixMilli()
	tx, err := a.db().db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Acquire the SQLite write lock before checking sessions, other calls or reservations.
	if _, err = tx.Exec(`DELETE FROM telephony_auto_answer_reservations WHERE expires_at<=? AND EXISTS (SELECT 1 FROM calls c WHERE c.id=telephony_auto_answer_reservations.call_id AND (c.status='pending' OR c.status IN ('completed','failed','busy','no-answer','canceled')))`, now); err != nil {
		return err
	}
	var valid int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM telephony_auto_answer_sessions WHERE project_id=? AND principal=? AND session_id=? AND expires_at>?`, project, principal, session, now).Scan(&valid); err != nil {
		return err
	}
	if valid != 1 {
		return errors.New("this browser is not available for automatic answering")
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM (SELECT o.call_id FROM telephony_call_owners o JOIN calls c ON c.id=o.call_id WHERE o.project_id=? AND o.principal=? AND o.call_id<>? AND c.status NOT IN ('completed','failed','busy','no-answer','canceled') UNION SELECT r.call_id FROM telephony_auto_answer_reservations r JOIN calls c ON c.id=r.call_id WHERE r.project_id=? AND r.principal=? AND r.call_id<>? AND c.status NOT IN ('completed','failed','busy','no-answer','canceled'))`, project, principal, call, project, principal, call).Scan(&valid); err != nil {
		return err
	}
	if valid > 0 {
		return errors.New("adviser is already occupied")
	}
	// Terminal reservations for this principal are cheap indexed lookups.
	if _, err = tx.Exec(`DELETE FROM telephony_auto_answer_reservations WHERE project_id=? AND principal=? AND EXISTS (SELECT 1 FROM calls c WHERE c.id=telephony_auto_answer_reservations.call_id AND c.status IN ('completed','failed','busy','no-answer','canceled'))`, project, principal); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO telephony_auto_answer_reservations VALUES(?,?,?,?,?,?) ON CONFLICT(project_id,principal) DO UPDATE SET session_id=excluded.session_id,destination_id=excluded.destination_id,expires_at=excluded.expires_at WHERE telephony_auto_answer_reservations.call_id=excluded.call_id`, project, principal, session, call, dest, now+int64(delay)+15000)
	if err != nil {
		return errors.New("call reserved by another adviser")
	}
	var actual string
	if err = tx.QueryRow(`SELECT call_id FROM telephony_auto_answer_reservations WHERE project_id=? AND principal=?`, project, principal).Scan(&actual); err != nil {
		return err
	}
	if actual != call {
		return errors.New("adviser already has another offer")
	}
	return tx.Commit()
}
func (a *App) validateAutoAnswer(r *http.Request, row *callRow, session, dest string, warningCompleted bool) error {
	principal := autoAnswerOwner(r)
	var expiry int64
	var storedDest string
	var warningSupported bool
	err := a.db().db.QueryRow(`SELECT r.expires_at,r.destination_id,s.warning_supported FROM telephony_auto_answer_reservations r JOIN telephony_auto_answer_sessions s ON s.project_id=r.project_id AND s.principal=r.principal AND s.session_id=r.session_id WHERE r.project_id=? AND r.principal=? AND r.session_id=? AND r.call_id=? AND s.expires_at>?`, row.ProjectID, principal, session, row.ID, time.Now().UnixMilli()).Scan(&expiry, &storedDest, &warningSupported)
	if err != nil || expiry <= time.Now().UnixMilli() || storedDest != dest {
		return errors.New("automatic answer reservation expired")
	}
	e, err := a.effectiveAutoAnswer(row.ProjectID, principal, row.RouteID, dest)
	if err != nil {
		return err
	}
	if e.WarningTone != nil && e.WarningTone.Enabled && (!warningSupported || !warningCompleted) {
		return errors.New("auto_answer_warning_required")
	}
	if e.Mode != "automatic" {
		return errors.New("automatic answering is now disabled")
	}
	log.Printf("auto_answer attempt at=%s call=%s project=%s principal_hash=%s source=%s delay_ms=%d", ringTime(time.Now()), row.ID, row.ProjectID, phoneHash(principal)[:12], e.Source, e.DelayMS)
	// Existing owner/capacity checks run immediately afterward in the normal answer path.
	return nil
}
