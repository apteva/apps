package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// These fields come only from the authenticated gateway. Subject IDs are never
// globally unique: two auth installations may both have a user named "1".
type phoneIdentity struct {
	IssuerApp       string `json:"issuer_app"`
	IssuerInstallID string `json:"issuer_install_id"`
	SubjectType     string `json:"subject_type"`
	SubjectID       string `json:"subject_id"`
	OrganizationID  string `json:"organization_id"`
}

func (p phoneIdentity) key() string { b, _ := json.Marshal(p); return string(b) }
func (p phoneIdentity) valid() bool {
	return p.IssuerApp != "" && p.IssuerInstallID != "" && p.SubjectType == "user" && p.SubjectID != ""
}

type phoneGrant struct {
	Role            string   `json:"role"` // user or supervisor; supervisors still have resource bounds
	Destinations    []string `json:"destinations"`
	OutboundNumbers []string `json:"outbound_numbers"`
}
type phoneUser struct {
	Identity phoneIdentity `json:"identity"`
	Enabled  bool          `json:"enabled"`
	Groups   []string      `json:"groups"`
	phoneGrant
}
type phoneGroup struct {
	ID string `json:"id"`
	phoneGrant
}
type phonePolicy struct {
	Providers []phoneAuthProvider `json:"providers,omitempty"`
	Revision  int64               `json:"revision"`
	Users     []phoneUser         `json:"users"`
	Groups    []phoneGroup        `json:"groups"`
}
type phonePrincipal struct {
	Identity     phoneIdentity
	Project      string
	Revision     int64
	Supervisor   bool
	Destinations map[string]bool
	Numbers      map[string]bool
}
type phonePrincipalKey struct{}

func phoneUserFrom(r *http.Request) *phonePrincipal {
	p, _ := r.Context().Value(phonePrincipalKey{}).(*phonePrincipal)
	return p
}
func phoneHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (a *App) phonePolicy(project string) (phonePolicy, error) {
	var p phonePolicy
	var raw string
	err := a.db().db.QueryRow(`SELECT revision,policy_json FROM telephony_access_policies WHERE project_id=?`, project).Scan(&p.Revision, &raw)
	if err == sql.ErrNoRows {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	revision := p.Revision
	err = json.Unmarshal([]byte(raw), &p)
	p.Revision = revision
	return p, err
}
func (a *App) phonePrincipal(project string, identity phoneIdentity) (*phonePrincipal, error) {
	policy, err := a.phonePolicy(project)
	if err != nil {
		return nil, err
	}
	p := &phonePrincipal{Identity: identity, Project: project, Revision: policy.Revision, Destinations: map[string]bool{}, Numbers: map[string]bool{}}
	add := func(g phoneGrant) {
		p.Supervisor = p.Supervisor || g.Role == "supervisor"
		for _, v := range g.Destinations {
			p.Destinations[v] = true
		}
		for _, v := range g.OutboundNumbers {
			p.Numbers[v] = true
		}
	}
	for _, u := range policy.Users {
		if u.Identity == identity && u.Enabled {
			add(u.phoneGrant)
			for _, id := range u.Groups {
				for _, g := range policy.Groups {
					if g.ID == id {
						add(g.phoneGrant)
					}
				}
			}
			return p, nil
		}
	}
	return nil, errors.New("application user has no Telephony access")
}
func phoneRequestIdentity(r *http.Request) (phoneIdentity, bool) {
	p := phoneIdentity{r.Header.Get("X-Apteva-Issuer-App"), r.Header.Get("X-Apteva-Issuer-Install-ID"), r.Header.Get("X-Apteva-Subject-Type"), r.Header.Get("X-Apteva-Subject-ID"), r.Header.Get("X-Apteva-Organization-ID")}
	return p, p != (phoneIdentity{}) || r.Header.Get("X-Apteva-Scopes") != ""
}
func phoneAction(r *http.Request) string {
	path := r.URL.Path
	if r.Method == "GET" && path == "/calls" {
		return "call.read"
	}
	if r.Method == "GET" && path == "/softphone/access" {
		return "call.read"
	}
	if r.Method != "POST" {
		return ""
	}
	switch {
	case path == "/softphone/place":
		return "call.dial"
	case strings.HasPrefix(path, "/softphone/answer/"):
		return "call.answer"
	case strings.HasPrefix(path, "/softphone/attach/"), strings.HasPrefix(path, "/softphone/renew/"):
		return "call.attach"
	case strings.HasPrefix(path, "/softphone/takeover/"):
		return "call.takeover"
	case strings.HasPrefix(path, "/softphone/release/"):
		return "call.answer"
	case strings.HasPrefix(path, "/calls/") && strings.HasSuffix(path, "/hangup"):
		return "call.hangup"
	}
	return ""
}
func (a *App) applicationUserHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, delegated := phoneRequestIdentity(r)
		if !delegated {
			next(w, r)
			return
		}
		if !identity.valid() || r.Header.Get("X-Apteva-Project-ID") == "" {
			http.Error(w, "incomplete application-user identity", 401)
			return
		}
		project, err := a.panelProject(r)
		if err != nil {
			http.Error(w, "project not allowed", 403)
			return
		}
		action := phoneAction(r)
		var scopes []struct {
			Type    string   `json:"type"`
			App     string   `json:"app"`
			Actions []string `json:"actions"`
		}
		allowed := false
		if json.Unmarshal([]byte(r.Header.Get("X-Apteva-Scopes")), &scopes) == nil && action != "" {
			for _, s := range scopes {
				if s.Type == "app_user" && s.App == "telephony" {
					for _, v := range s.Actions {
						if v == action {
							allowed = true
						}
					}
				}
			}
		}
		if !allowed {
			http.Error(w, "application-user action not allowed", 403)
			return
		}
		p, err := a.phonePrincipal(project, identity)
		if err != nil {
			http.Error(w, "Telephony access denied", 403)
			return
		}
		if action == "call.takeover" && !p.Supervisor {
			http.Error(w, "supervisor permission required", 403)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), phonePrincipalKey{}, p)))
	}
}

// Legacy MCP tools are operator/agent APIs. App-user HTTP scopes must not allow
// bypassing ownership via tools/call, even if a token has overly broad MCP scopes.
func operatorPhoneTool(next func(context.Context, *sdk.AppCtx, map[string]any) (any, error)) func(context.Context, *sdk.AppCtx, map[string]any) (any, error) {
	return func(c context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
		if p := sdk.CallerFrom(c); p != nil && (p.SubjectID != "" || p.SubjectType != "") {
			return nil, errors.New("application users must use the authorized softphone API")
		}
		return next(c, ctx, args)
	}
}
func (a *App) phoneOwner(call string) (string, string, error) {
	var owner, dest string
	err := a.db().db.QueryRow(`SELECT principal,destination_id FROM telephony_call_owners WHERE call_id=?`, call).Scan(&owner, &dest)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return owner, dest, err
}
func (a *App) setPhoneOwner(row *callRow, p *phonePrincipal, dest string) error {
	_, err := a.db().db.Exec(`INSERT INTO telephony_call_owners(call_id,project_id,principal,destination_id) VALUES(?,?,?,?) ON CONFLICT(call_id) DO UPDATE SET principal=excluded.principal,destination_id=excluded.destination_id`, row.ID, row.ProjectID, p.Identity.key(), dest)
	return err
}
func (a *App) phoneCallAllowed(p *phonePrincipal, row *callRow, shared bool) bool {
	if p == nil {
		return true
	}
	if row.ProjectID != p.Project {
		return false
	}
	owner, dest, err := a.phoneOwner(row.ID)
	if err != nil {
		return false
	}
	if owner == p.Identity.key() {
		if row.Direction == "outbound" {
			return p.Numbers[row.FromNumber]
		}
		return p.Destinations[dest]
	}
	if shared && p.Supervisor {
		if row.Direction == "outbound" {
			return p.Numbers[row.FromNumber]
		}
		return p.Destinations[row.RoutingDestinationID] || p.Destinations[dest]
	}
	return false
}
func (a *App) phoneOfferDestination(p *phonePrincipal, row *callRow, requested string) string {
	if row.Status != "pending" || row.Direction != "inbound" {
		return ""
	}
	offers, err := a.db().activeRingOffers(row.ID, row.ProjectID)
	if err != nil {
		return ""
	}
	for _, offer := range offers {
		if offer.Kind == "browser" && p.Destinations[offer.DestinationID] && (requested == "" || requested == offer.DestinationID) {
			return offer.DestinationID
		}
	}
	if len(offers) == 0 && row.PeerKind == peerKindHuman && p.Destinations[row.RoutingDestinationID] && (requested == "" || requested == row.RoutingDestinationID) {
		return row.RoutingDestinationID
	}
	return ""
}
func (a *App) filterPhoneCalls(r *http.Request, rows []callRow) []callRow {
	p := phoneUserFrom(r)
	if p == nil {
		return rows
	}
	out := make([]callRow, 0, len(rows))
	for _, row := range rows {
		if !a.phoneCallAllowed(p, &row, true) && a.phoneOfferDestination(p, &row, "") == "" {
			continue
		}
		offers := make([]ringOffer, 0)
		for _, offer := range row.RingOffers {
			if offer.Kind == "browser" && p.Destinations[offer.DestinationID] {
				offers = append(offers, offer)
			}
		}
		row.RingOffers = offers
		out = append(out, row)
	}
	return out
}

// Browser grants are distinct from the durable carrier peer secret. One current
// grant per call prevents stale tabs and explicit takeovers replaying old URLs.
const phoneLeaseSeconds = 60

func (a *App) issuePhoneSession(row *callRow, p *phonePrincipal) (*softphoneSession, error) {
	principal := ""
	revision := int64(0)
	if p != nil {
		if !a.phoneCallAllowed(p, row, false) {
			return nil, errors.New("call not owned by user")
		}
		fresh, err := a.phonePrincipal(row.ProjectID, p.Identity)
		if err != nil || fresh.Revision != p.Revision {
			return nil, errors.New("access changed; retry")
		}
		principal = p.Identity.key()
		revision = p.Revision
	}
	token := newSecret()
	_, err := a.db().db.Exec(`INSERT INTO telephony_media_sessions(call_id,project_id,token_hash,principal,policy_revision,expires_at) VALUES(?,?,?,?,?,?) ON CONFLICT(call_id) DO UPDATE SET token_hash=excluded.token_hash,principal=excluded.principal,policy_revision=excluded.policy_revision,expires_at=excluded.expires_at`, row.ID, row.ProjectID, phoneHash(token), principal, revision, time.Now().Unix()+phoneLeaseSeconds)
	if err != nil {
		return nil, err
	}
	return &softphoneSession{CallID: row.ID, MediaURL: a.softphoneMediaURL(row.ID, token), SessionToken: token, To: row.ToNumber, From: row.FromNumber, LeaseSeconds: phoneLeaseSeconds}, nil
}
func (a *App) validPhoneMedia(row *callRow, token string) bool {
	var hash, principal string
	var revision, expires int64
	err := a.db().db.QueryRow(`SELECT token_hash,principal,policy_revision,expires_at FROM telephony_media_sessions WHERE call_id=? AND project_id=?`, row.ID, row.ProjectID).Scan(&hash, &principal, &revision, &expires)
	if err == sql.ErrNoRows {
		owner, _, e := a.phoneOwner(row.ID)
		return e == nil && owner == "" && row.PeerToken != "" && secureEqual(token, row.PeerToken)
	}
	if err != nil || expires <= time.Now().Unix() || !secureEqual(phoneHash(token), hash) {
		return false
	}
	if principal == "" {
		return true
	}
	var identity phoneIdentity
	if json.Unmarshal([]byte(principal), &identity) != nil {
		return false
	}
	p, err := a.phonePrincipal(row.ProjectID, identity)
	return err == nil && p.Revision == revision && a.phoneCallAllowed(p, row, false)
}
func (a *App) handlePhoneSession(w http.ResponseWriter, r *http.Request, project, action, id string) {
	unlock := a.softphones.lockClaim(id)
	defer unlock()
	row, err := a.db().findCall(id)
	if err != nil || row == nil || row.ProjectID != project {
		http.Error(w, "call not found", 404)
		return
	}
	if isTerminalStatus(row.Status) || row.PeerKind != peerKindHuman || row.PeerToken == "" || row.Status == "pending" {
		http.Error(w, "call has no attachable human session", 409)
		return
	}
	p := phoneUserFrom(r)
	if action == "takeover" {
		if p != nil {
			if !p.Supervisor || !a.phoneCallAllowed(p, row, true) {
				http.Error(w, "takeover denied", 403)
				return
			}
			_, dest, e := a.phoneOwner(id)
			if e != nil {
				http.Error(w, "ownership unavailable", 500)
				return
			}
			if dest == "" {
				dest = row.RoutingDestinationID
			}
			if e = a.setPhoneOwner(row, p, dest); e != nil {
				http.Error(w, "ownership unavailable", 500)
				return
			}
		}
	} else if !a.phoneCallAllowed(p, row, false) {
		http.Error(w, "call not found", 404)
		return
	}
	if action == "renew" {
		var body struct {
			SessionToken string `json:"session_token"`
		}
		if decodeJSONBody(r, &body) != nil || !a.validPhoneMedia(row, body.SessionToken) {
			http.Error(w, "session expired or replaced", 403)
			return
		}
		// Renew only your own lease, not an administrative bearer grant.
		principal := ""
		if p != nil {
			principal = p.Identity.key()
		}
		res, e := a.db().db.Exec(`UPDATE telephony_media_sessions SET expires_at=? WHERE call_id=? AND token_hash=? AND principal=?`, time.Now().Unix()+phoneLeaseSeconds, id, phoneHash(body.SessionToken), principal)
		if e != nil {
			http.Error(w, "session unavailable", 500)
			return
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			http.Error(w, "session not owned", 403)
			return
		}
		writeJSON(w, map[string]any{"lease_seconds": phoneLeaseSeconds})
		return
	}
	session, err := a.issuePhoneSession(row, p)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	writeJSON(w, session)
}

func (a *App) validatePhonePolicy(project string, p phonePolicy) error {
	if len(p.Users) > 10000 || len(p.Groups) > 1000 {
		return errors.New("policy too large")
	}
	validateGrant := func(g phoneGrant) error {
		if g.Role != "user" && g.Role != "supervisor" {
			return errors.New("role must be user or supervisor")
		}
		for _, id := range g.Destinations {
			d, e := a.findRoutingDestination(project, id)
			if e != nil || d == nil || d.Kind != "browser" || !d.Enabled {
				return errors.New("grant requires an enabled browser destination in this project")
			}
		}
		for _, n := range g.OutboundNumbers {
			if !validE164(n) {
				return errors.New("outbound number must be E.164")
			}
		}
		return nil
	}
	providers := map[string]bool{}
	for _, provider := range p.Providers {
		if !validPhoneProvider(provider) || providers[provider.ID] {
			return errors.New("invalid or duplicate online identity provider")
		}
		providers[provider.ID] = true
	}
	groups := map[string]bool{}
	users := map[string]bool{}
	for _, g := range p.Groups {
		if g.ID == "" || groups[g.ID] {
			return errors.New("group IDs must be unique and nonempty")
		}
		groups[g.ID] = true
		if e := validateGrant(g.phoneGrant); e != nil {
			return e
		}
	}
	for _, u := range p.Users {
		if !u.Identity.valid() || users[u.Identity.key()] {
			return errors.New("users need unique complete verified identities")
		}
		users[u.Identity.key()] = true
		if e := validateGrant(u.phoneGrant); e != nil {
			return e
		}
		for _, g := range u.Groups {
			if !groups[g] {
				return errors.New("unknown access group")
			}
		}
	}
	return nil
}

// Administrative policy writes use optimistic revision checking, so concurrent
// changes cannot silently restore removed membership. No application-user route
// maps to this endpoint, including supervisor sessions.
func (a *App) handlePhoneAccess(w http.ResponseWriter, r *http.Request) {
	if _, delegated := phoneRequestIdentity(r); delegated {
		http.Error(w, "operator credentials required", 403)
		return
	}
	project, err := a.panelProject(r)
	if err != nil {
		http.Error(w, "project not allowed", 403)
		return
	}
	if r.URL.Path == "/access/policy" {
		if r.Method == "GET" {
			p, e := a.phonePolicy(project)
			if e != nil {
				http.Error(w, "policy unavailable", 500)
				return
			}
			writeJSON(w, p)
			return
		}
		if r.Method != "PUT" {
			http.Error(w, "method not allowed", 405)
			return
		}
		var p phonePolicy
		if decodeJSONBody(r, &p) != nil {
			http.Error(w, "invalid policy", 400)
			return
		}
		if e := a.validatePhonePolicy(project, p); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		raw, _ := json.Marshal(p)
		var res sql.Result
		var e error
		if p.Revision == 0 {
			res, e = a.db().db.Exec(`INSERT INTO telephony_access_policies(project_id,revision,policy_json) VALUES(?,1,?) ON CONFLICT(project_id) DO NOTHING`, project, string(raw))
		} else {
			res, e = a.db().db.Exec(`UPDATE telephony_access_policies SET revision=revision+1,policy_json=? WHERE project_id=? AND revision=?`, string(raw), project, p.Revision)
		}
		if e != nil {
			http.Error(w, "policy unavailable", 500)
			return
		}
		n, e := res.RowsAffected()
		if e != nil {
			http.Error(w, "policy unavailable", 500)
			return
		}
		if n != 1 {
			http.Error(w, "policy revision changed", 409)
			return
		}
		p.Revision++
		writeJSON(w, p)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/access/assign/") && r.Method == "POST" {
		id := strings.TrimPrefix(r.URL.Path, "/access/assign/")
		unlock := a.softphones.lockClaim(id)
		defer unlock()
		var identity phoneIdentity
		if decodeJSONBody(r, &identity) != nil || !identity.valid() {
			http.Error(w, "complete assignee identity required", 400)
			return
		}
		row, e := a.db().findCall(id)
		if e != nil || row == nil || row.ProjectID != project {
			http.Error(w, "call not found", 404)
			return
		}
		if isTerminalStatus(row.Status) || row.PeerKind != peerKindHuman {
			http.Error(w, "active human call required", 409)
			return
		}
		// Assignment is for a backend-prepared leg. It cannot displace a live operator.
		owner, _, e := a.phoneOwner(id)
		if e != nil || owner != "" {
			http.Error(w, "call already assigned", 409)
			return
		}
		if hub := a.softphones.hubFor(id); hub.browserWriter() != nil {
			http.Error(w, "call already has browser audio", 409)
			return
		}
		p, e := a.phonePrincipal(project, identity)
		if e != nil || (row.Direction == "outbound" && !p.Numbers[row.FromNumber]) || (row.Direction == "inbound" && !p.Destinations[row.RoutingDestinationID]) {
			http.Error(w, "assignee lacks call permissions", 403)
			return
		}
		if e = a.setPhoneOwner(row, p, row.RoutingDestinationID); e != nil {
			http.Error(w, "assignment unavailable", 500)
			return
		}
		_, e = a.db().db.Exec(`DELETE FROM telephony_media_sessions WHERE call_id=?`, id)
		if e != nil {
			http.Error(w, "session invalidation failed", 500)
			return
		}
		writeJSON(w, map[string]any{"call_id": id, "assigned": true})
		return
	}
	http.NotFound(w, r)
}

// Apply visibility before the result limit. A busy project cannot crowd an
// authorized incoming call out of the user's first page with other users' calls.
func (a *App) recentPhoneCalls(r *http.Request, project string, limit int) ([]callRow, error) {
	if phoneUserFrom(r) == nil {
		return a.db().recent(project, limit)
	}
	out := []callRow{}
	for offset := 0; len(out) < limit; offset += 200 {
		rows, err := a.db().listWhere(`project_id=? AND ingress_path<>'ring_group' ORDER BY placed_at DESC,id DESC LIMIT 200 OFFSET `+fmt.Sprint(offset), project)
		if err != nil {
			return nil, err
		}
		out = append(out, a.filterPhoneCalls(r, rows)...)
		if len(rows) < 200 {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
