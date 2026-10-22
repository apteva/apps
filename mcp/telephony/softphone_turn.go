package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/pion/stun/v4"
	"github.com/pion/webrtc/v4"
)

// Policies are frozen per attachment. Only credentials rotate during a call.
// The empty principal is the project default; an identity is never a bare user ID.
type phoneTURNPolicy struct {
	Source             string `json:"source"`
	Transport          string `json:"transport"`
	ICEPolicy          string `json:"ice_transport_policy"`
	TLSPort            int    `json:"tls_port,omitempty"`
	TTLSeconds         int    `json:"ttl_seconds"`
	AllowUserOverrides bool   `json:"allow_user_overrides,omitempty"`
}

func (p phoneTURNPolicy) resolved() phoneTURNPolicy {
	if p.Source == "" {
		p.Source = "existing"
	}
	if p.Transport == "" {
		p.Transport = "all"
	}
	if p.ICEPolicy == "" {
		p.ICEPolicy = "all"
	}
	if p.TTLSeconds == 0 {
		p.TTLSeconds = 3600
	}
	return p
}
func (p phoneTURNPolicy) validate() error {
	p = p.resolved()
	if (p.Source != "existing" && p.Source != "app") || (p.Transport != "all" && p.Transport != "udp" && p.Transport != "tcp" && p.Transport != "tls") || (p.ICEPolicy != "all" && p.ICEPolicy != "relay") || p.TTLSeconds < 60 || p.TTLSeconds > 86400 || p.TLSPort < 0 || p.TLSPort > 65535 || (p.TLSPort != 0 && p.Transport != "tls") {
		return errors.New("invalid TURN policy")
	}
	return nil
}
func (a *App) readTURNPolicy(project, principal string) (phoneTURNPolicy, bool, error) {
	var raw string
	err := a.db().db.QueryRow(`SELECT policy_json FROM telephony_turn_policies WHERE project_id=? AND principal=?`, project, principal).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return (phoneTURNPolicy{}).resolved(), false, nil
	}
	if err != nil {
		return phoneTURNPolicy{}, false, err
	}
	var p phoneTURNPolicy
	err = json.Unmarshal([]byte(raw), &p)
	if err == nil {
		err = p.validate()
	}
	return p.resolved(), true, err
}
func (a *App) resolvedTURNPolicy(project string, identity phoneIdentity, q url.Values) (phoneTURNPolicy, error) {
	base, _, err := a.readTURNPolicy(project, "")
	if err != nil {
		return base, err
	}
	p := base
	if identity.valid() {
		user, have, e := a.readTURNPolicy(project, identity.key())
		if e != nil {
			return p, e
		}
		if have {
			p = user
		}
	}
	overridden := q.Has("turn_transport") || q.Has("ice_policy") || q.Has("turn_tls_port")
	if overridden && identity.valid() && !base.AllowUserOverrides {
		return p, errors.New("TURN session overrides are disabled")
	}
	for _, k := range []string{"turn_transport", "ice_policy", "turn_tls_port"} {
		if len(q[k]) > 1 {
			return p, errors.New("duplicate TURN option")
		}
	}
	if q.Has("turn_transport") {
		p.Transport = q.Get("turn_transport")
		if p.Transport != "tls" {
			p.TLSPort = 0
		}
	}
	if q.Has("ice_policy") {
		p.ICEPolicy = q.Get("ice_policy")
	}
	if q.Has("turn_tls_port") {
		p.TLSPort, err = strconv.Atoi(q.Get("turn_tls_port"))
		if err != nil || p.TLSPort == 0 {
			return p, errors.New("invalid TURN TLS port")
		}
	}
	if err = p.validate(); err != nil {
		return p, err
	}
	return p, nil
}
func (a *App) turnPolicies(ctx *sdk.AppCtx, args map[string]any, self *phonePrincipal) (map[string]any, error) {
	project := ctx.CurrentProject()
	if project == "" {
		return nil, errors.New("project required")
	}
	var identity phoneIdentity
	if self != nil {
		identity = self.Identity
		if args["identity"] != nil {
			return nil, errors.New("user identity comes from authentication")
		}
	} else if args["identity"] != nil {
		raw, _ := json.Marshal(args["identity"])
		if json.Unmarshal(raw, &identity) != nil || !identity.valid() {
			return nil, errors.New("invalid adviser identity")
		}
		if _, err := a.phonePrincipal(project, identity); err != nil {
			return nil, errors.New("adviser is not authorized in this project")
		}
	}
	principal := ""
	if identity.valid() {
		principal = identity.key()
	}
	base, _, err := a.readTURNPolicy(project, "")
	if err != nil {
		return nil, err
	}
	if args["policy"] != nil || boolArg(args, "inherit", false) {
		if self != nil && !base.AllowUserOverrides {
			return nil, errors.New("user TURN overrides are disabled")
		}
		if boolArg(args, "inherit", false) {
			if principal == "" {
				return nil, errors.New("project defaults cannot inherit")
			}
			_, err = a.db().db.Exec(`DELETE FROM telephony_turn_policies WHERE project_id=? AND principal=?`, project, principal)
		} else {
			raw, e := json.Marshal(args["policy"])
			if e != nil {
				return nil, errors.New("invalid TURN policy")
			}
			var p phoneTURNPolicy
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.DisallowUnknownFields()
			if e = decoder.Decode(&p); e != nil {
				return nil, errors.New("invalid TURN policy")
			}
			if e = p.validate(); e != nil {
				return nil, e
			}
			if principal != "" && p.AllowUserOverrides {
				return nil, errors.New("only project defaults control user overrides")
			}
			if p.Source == "app" && ctx.IntegrationFor("turn") == nil {
				return nil, errors.New("bind the optional TURN app integration first")
			}
			raw, _ = json.Marshal(p.resolved())
			_, err = a.db().db.Exec(`INSERT INTO telephony_turn_policies(project_id,principal,policy_json,updated_at) VALUES(?,?,?,?) ON CONFLICT(project_id,principal) DO UPDATE SET policy_json=excluded.policy_json,updated_at=excluded.updated_at`, project, principal, string(raw), ringTime(time.Now()))
		}
		if err != nil {
			return nil, err
		}
	}
	selected, have, err := a.readTURNPolicy(project, principal)
	if err != nil {
		return nil, err
	}
	base, _, err = a.readTURNPolicy(project, "")
	if err != nil {
		return nil, err
	}
	effective := selected
	if !have && principal != "" {
		effective = base
	}
	bound := ctx.IntegrationFor("turn") != nil
	result := map[string]any{"policy": selected, "effective": effective, "override": have && principal != "", "allow_user_overrides": base.AllowUserOverrides, "turn_bound": bound, "scope": map[bool]string{true: "adviser", false: "project"}[principal != ""]}
	if self == nil {
		policy, e := a.phonePolicy(project)
		if e != nil {
			return nil, e
		}
		identities := []phoneIdentity{}
		for _, u := range policy.Users {
			if u.Enabled {
				identities = append(identities, u.Identity)
			}
		}
		result["advisers"] = identities
	}
	return result, nil
}
func (a *App) handleSelfTURNPolicy(w http.ResponseWriter, r *http.Request, project string) {
	var args map[string]any
	if r.Method == http.MethodPost {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		if d.Decode(&args) != nil {
			http.Error(w, "invalid TURN settings", 400)
			return
		}
	}
	result, err := a.turnPolicies(globalCtx.WithProject(project), args, phoneUserFrom(r))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, result)
}

type rtcTURNCredentials struct {
	ICEServers []webrtc.ICEServer `json:"ice_servers"`
	ExpiresAt  int64              `json:"expires_at"`
	TTLSeconds int                `json:"ttl_seconds"`
}

// Shared across installations/attachments in this process; never blocks audio.
var rtcTURNRequests = make(chan struct{}, 4)

type rtcTURNProvider struct {
	policy phoneTURNPolicy
	fetch  func(context.Context) (rtcTURNCredentials, error)
}

func (p *rtcTURNProvider) issue(ctx context.Context) (rtcTURNCredentials, error) {
	if p == nil {
		return rtcTURNCredentials{}, errors.New("managed TURN unavailable")
	}
	req, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if req.Err() != nil {
		return rtcTURNCredentials{}, errors.New("TURN credential request canceled")
	}
	select {
	case rtcTURNRequests <- struct{}{}:
		defer func() { <-rtcTURNRequests }()
	case <-req.Done():
		return rtcTURNCredentials{}, errors.New("TURN credential request canceled")
	}
	v, err := p.fetch(req)
	if err != nil {
		return rtcTURNCredentials{}, errors.New("TURN credential service unavailable")
	}
	if v.ExpiresAt <= time.Now().Unix()+15 || v.ExpiresAt > time.Now().Unix()+86405 || v.TTLSeconds < 60 || v.TTLSeconds > 86400 || v.ExpiresAt > time.Now().Unix()+int64(v.TTLSeconds)+5 || len(v.ICEServers) == 0 || len(v.ICEServers) > 8 {
		return rtcTURNCredentials{}, errors.New("invalid temporary TURN credentials")
	}
	urls := 0
	for _, s := range v.ICEServers {
		password, ok := s.Credential.(string)
		if s.Username == "" || len(s.Username) > 512 || !ok || password == "" || len(password) > 1024 || len(s.URLs) == 0 {
			return rtcTURNCredentials{}, errors.New("invalid temporary TURN credentials")
		}
		for _, u := range s.URLs {
			urls++
			parsed, parseErr := stun.ParseURI(u)
			if parseErr != nil || parsed.Host == "" {
				return rtcTURNCredentials{}, errors.New("invalid TURN endpoint")
			}
			if len(u) > 1024 || strings.ContainsAny(u, "\r\n@#") || (!strings.HasPrefix(u, "turn:") && !strings.HasPrefix(u, "turns:")) {
				return rtcTURNCredentials{}, errors.New("invalid TURN endpoint")
			}
		}
	}
	if urls > 16 {
		return rtcTURNCredentials{}, errors.New("too many TURN endpoints")
	}
	v.ICEServers = selectTURNTransport(v.ICEServers, p.policy.resolved().Transport, p.policy.TLSPort)
	if !hasTURN(v.ICEServers) {
		return rtcTURNCredentials{}, errors.New("requested TURN transport is unavailable")
	}
	return v, nil
}

// Caller has validated this exact media token. Revalidate it on every renewal;
// supervisor policies come from their listener session, never the adviser's row.
func (a *App) configureManagedTURN(c *softphoneRTCConfig, row *callRow, token string, identity phoneIdentity, listener bool, q url.Values) error {
	policy, err := a.resolvedTURNPolicy(row.ProjectID, identity, q)
	if err != nil {
		return err
	}
	c.ICEPolicy = policy.ICEPolicy
	if policy.Source != "app" {
		c.ClientICEServers = selectTURNTransport(c.ICEServers, policy.Transport, policy.TLSPort)
		if c.ClientICEServers == nil {
			c.ClientICEServers = []webrtc.ICEServer{}
		}
		if policy.ICEPolicy == "relay" && !hasTURN(c.ClientICEServers) {
			return errors.New("relay policy requires a TURN endpoint")
		}
		return nil
	}
	ctx := globalCtx.WithProject(row.ProjectID)

	session := newAudioSessionID()
	c.TURN = &rtcTURNProvider{policy: policy, fetch: func(request context.Context) (rtcTURNCredentials, error) {
		if request.Err() != nil {
			return rtcTURNCredentials{}, request.Err()
		}
		current, err := a.db().findCall(row.ID)
		if err != nil || current == nil || isTerminalStatus(current.Status) {
			return rtcTURNCredentials{}, errors.New("call unavailable")
		}
		reason := ""
		if listener {
			reason, _ = a.listenerSessionCheck(current, token)
		} else {
			reason, _, _ = a.phoneMediaCheckDetails(current, token)
		}
		if reason != "" {
			return rtcTURNCredentials{}, errors.New("media authorization unavailable")
		}
		if ctx.IntegrationFor("turn") == nil {
			return rtcTURNCredentials{}, errors.New("TURN integration unavailable")
		}
		args := map[string]any{"session_id": session, "ttl_seconds": policy.TTLSeconds, "transport": policy.Transport}
		if policy.TLSPort != 0 {
			args["tls_port"] = policy.TLSPort
		}
		var out rtcTURNCredentials
		err = sdk.CallAppResultContext(request, ctx.PlatformAPI(), "turn", "turn_credentials", args, &out)
		return out, err
	}}
	// Managed credentials belong only to browser peers. The server retains STUN
	// and its public candidate, avoiding needless allocations or double relaying.
	c.ICEServers = selectSTUN(c.ICEServers)
	return nil
}
func selectSTUN(servers []webrtc.ICEServer) []webrtc.ICEServer {
	var result []webrtc.ICEServer
	for _, s := range servers {
		v := s
		v.URLs = nil
		for _, u := range s.URLs {
			if strings.HasPrefix(u, "stun:") || strings.HasPrefix(u, "stuns:") {
				v.URLs = append(v.URLs, u)
			}
		}
		if len(v.URLs) > 0 {
			result = append(result, v)
		}
	}
	return result
}
func hasTURN(servers []webrtc.ICEServer) bool {
	for _, s := range servers {
		for _, u := range s.URLs {
			if strings.HasPrefix(u, "turn:") || strings.HasPrefix(u, "turns:") {
				return true
			}
		}
	}
	return false
}
func selectTURNTransport(servers []webrtc.ICEServer, transport string, port int) []webrtc.ICEServer {
	if transport == "all" {
		return servers
	}
	var result []webrtc.ICEServer
	for _, s := range servers {
		v := s
		v.URLs = nil
		for _, u := range s.URLs {
			parsed, e := stun.ParseURI(u)
			if e != nil {
				continue
			}
			scheme := ""
			if parsed.Scheme == stun.SchemeTypeTURNS && parsed.Proto == stun.ProtoTypeTCP {
				scheme = "tls"
			} else if parsed.Scheme == stun.SchemeTypeTURN {
				if parsed.Proto == stun.ProtoTypeUDP {
					scheme = "udp"
				}
				if parsed.Proto == stun.ProtoTypeTCP {
					scheme = "tcp"
				}
			}
			if scheme != transport || (port != 0 && int(parsed.Port) != port) {
				continue
			}
			v.URLs = append(v.URLs, u)
		}
		if len(v.URLs) > 0 {
			result = append(result, v)
		}
	}
	return result
}
