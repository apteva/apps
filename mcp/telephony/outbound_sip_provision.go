package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Only configuration requests acquire this gate. Dialing, media and cleanup
// never do. Entries disappear when the last waiter leaves.
var outboundSetupGates = struct {
	sync.Mutex
	entries map[string]*outboundSetupGate
}{entries: make(map[string]*outboundSetupGate)}

type outboundSetupGate struct {
	token chan struct{}
	refs  int
}

func lockOutboundSetup(ctx context.Context, key string) (func(), error) {
	outboundSetupGates.Lock()
	g := outboundSetupGates.entries[key]
	if g == nil {
		g = &outboundSetupGate{token: make(chan struct{}, 1)}
		g.token <- struct{}{}
		outboundSetupGates.entries[key] = g
	}
	g.refs++
	outboundSetupGates.Unlock()
	releaseRef := func() {
		outboundSetupGates.Lock()
		defer outboundSetupGates.Unlock()
		g.refs--
		if g.refs == 0 {
			delete(outboundSetupGates.entries, key)
		}
	}
	if err := ctx.Err(); err != nil {
		releaseRef()
		return nil, err
	}
	select {
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	case <-g.token:
	}
	if err := ctx.Err(); err != nil {
		g.token <- struct{}{}
		releaseRef()
		return nil, err
	}
	return func() { g.token <- struct{}{}; releaseRef() }, nil
}

type outboundSIPProfilePolicy struct {
	Destinations    []string `json:"destinations"`
	DailySpendLimit string   `json:"daily_spend_limit"`
}
type outboundSIPSetup struct {
	PendingStep      string                    `json:"pending_step,omitempty"`
	Name             string                    `json:"name"`
	ProfileID        string                    `json:"profile_id,omitempty"`
	Policy           *outboundSIPProfilePolicy `json:"profile_policy,omitempty"`
	ResourceID       string                    `json:"resource_id,omitempty"`
	CredentialListID string                    `json:"credential_list_id,omitempty"`
	CredentialID     string                    `json:"credential_id,omitempty"`
}

var sipSpendPattern = regexp.MustCompile(`^[0-9]{1,6}(\.[0-9]{1,2})?$`)

func validateSIPProfilePolicy(p *outboundSIPProfilePolicy) error {
	if len(p.Destinations) == 0 || len(p.Destinations) > 250 {
		return errors.New("choose allowed destination country codes for the outbound profile")
	}
	seen := map[string]bool{}
	countries := []string{}
	for _, c := range p.Destinations {
		c = strings.ToUpper(strings.TrimSpace(c))
		if len(c) != 2 || c[0] < 'A' || c[0] > 'Z' || c[1] < 'A' || c[1] > 'Z' {
			return errors.New("destinations must contain two-letter country codes")
		}
		if !seen[c] {
			seen[c] = true
			countries = append(countries, c)
		}
	}
	p.Destinations = countries
	n, e := strconv.ParseFloat(p.DailySpendLimit, 64)
	if e != nil || !sipSpendPattern.MatchString(p.DailySpendLimit) || n <= 0 {
		return errors.New("daily_spend_limit must be a positive USD amount with at most two decimals")
	}
	return nil
}
func newManagedSIPCredentials() (outboundSIPCredentials, error) {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		return outboundSIPCredentials{}, e
	}
	return outboundSIPCredentials{Username: "apteva" + hex.EncodeToString(b[:8]), Password: "Aa9!" + hex.EncodeToString(b[8:])}, nil
}

// Fixed-host API boundary. Error bodies can contain credentials and are never
// surfaced. A caller deadline covers the whole setup, including pagination.
func sipProviderRequest(ctx context.Context, creds *sdk.ConnectionCredentials, api, method, path string, body any) (map[string]any, error) {
	host := "https://api.telnyx.com/v2"
	if creds.Slug == "twilio" {
		host = "https://trunking.twilio.com/v1"
		if api == "account" {
			if !validProviderResourceID(creds.Fields["account_sid"]) {
				return nil, errors.New("Twilio account SID unavailable")
			}
			host = "https://api.twilio.com/2010-04-01/Accounts/" + url.PathEscape(creds.Fields["account_sid"])
		}
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, errors.New("invalid SIP provider path")
	}
	var reader io.Reader
	contentType := "application/json"
	if body != nil {
		if form, ok := body.(url.Values); ok {
			reader = strings.NewReader(form.Encode())
			contentType = "application/x-www-form-urlencoded"
		} else {
			raw, e := json.Marshal(body)
			if e != nil {
				return nil, e
			}
			reader = strings.NewReader(string(raw))
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, host+path, reader)
	if e != nil {
		return nil, errors.New("invalid SIP provider request")
	}
	if creds.Slug == "twilio" {
		if creds.Fields["account_sid"] == "" || creds.Fields["auth_token"] == "" {
			return nil, errors.New("Twilio API credentials unavailable")
		}
		req.SetBasicAuth(creds.Fields["account_sid"], creds.Fields["auth_token"])
	} else {
		// Apteva's Telnyx integration stores its bearer key as token. Retain
		// api_key for older credentials, without modifying the shared fields.
		token := firstNonEmpty(creds.Fields["token"], creds.Fields["api_key"])
		if token == "" {
			return nil, errors.New("Telnyx API credentials unavailable")
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	client := http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("SIP provider request failed; retry setup to reconcile saved progress")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &sipSetupHTTPError{status: res.StatusCode}
	}
	var result map[string]any
	if e = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&result); e != nil {
		return nil, errors.New("invalid SIP provider response; retry setup to reconcile saved progress")
	}
	return result, nil
}

type sipSetupHTTPError struct{ status int }

func (e *sipSetupHTTPError) Error() string {
	return fmt.Sprintf("SIP provider request failed: HTTP %d", e.status)
}

// Discover only exact managed names. Never follow provider-returned arbitrary
// URLs, nor adopt a resource based on a partial name. Bound all list work.
func sipProviderList(ctx context.Context, creds *sdk.ConnectionCredentials, api, path, key string) ([]map[string]any, error) {
	items := []map[string]any{}
	for page := 0; page < 20; page++ {
		query := url.Values{}
		if creds.Slug == "telnyx" {
			query.Set("page[number]", strconv.Itoa(page+1))
			query.Set("page[size]", "100")
		} else {
			query.Set("Page", strconv.Itoa(page))
			query.Set("PageSize", "100")
		}
		result, e := sipProviderRequest(ctx, creds, api, "GET", path+"?"+query.Encode(), nil)
		if e != nil {
			return nil, e
		}
		values, ok := result[key].([]any)
		if !ok {
			return nil, errors.New("invalid SIP provider list response")
		}
		for _, v := range values {
			if m, ok := v.(map[string]any); ok {
				items = append(items, m)
			}
		}
		more := len(values) >= 100
		if creds.Slug == "telnyx" {
			if meta, ok := result["meta"].(map[string]any); ok && intValue(meta["total_pages"]) > 0 {
				more = page+1 < intValue(meta["total_pages"])
			}
		} else {
			if api == "account" {
				more = stringValue(result["next_page_uri"]) != ""
			} else if meta, ok := result["meta"].(map[string]any); ok {
				more = stringValue(meta["next_page_url"]) != ""
			}
		}
		if !more {
			return items, nil
		}
		if len(values) == 0 {
			return nil, errors.New("SIP provider listing made no progress")
		}
	}
	return nil, errors.New("SIP provider listing exceeded its bounded page limit")
}
func uniqueManagedResource(items []map[string]any, field, name, idField string) (string, error) {
	found := ""
	for _, m := range items {
		if stringValue(m[field]) == name {
			id := stringValue(m[idField])
			if !validProviderResourceID(id) || found != "" {
				return "", errors.New("managed SIP resource identity is ambiguous")
			}
			found = id
		}
	}
	return found, nil
}
func sipSetupResourceID(result map[string]any, provider string) (string, error) {
	id := stringValue(telnyxDataMap(result)["id"])
	if provider == "twilio" {
		id = stringValue(result["sid"])
	}
	if !validProviderResourceID(id) {
		return "", errors.New("SIP provider did not return a resource ID; retry to reconcile setup")
	}
	return id, nil
}
func (a *App) saveOutboundSIPSetup(project string, connection int64, number, provider string, s outboundSIPSetup, encrypted []byte) error {
	raw, e := json.Marshal(s)
	if e != nil {
		return e
	}
	_, e = a.db().db.Exec(`INSERT INTO outbound_sip_setup(project_id,connection_id,phone_number,provider,state_json,credentials_cipher) VALUES(?,?,?,?,?,?) ON CONFLICT(project_id,connection_id,phone_number) DO UPDATE SET state_json=excluded.state_json,credentials_cipher=excluded.credentials_cipher`, project, connection, number, provider, string(raw), encrypted)
	return e
}
func sipProfiles(items []map[string]any) []outboundProfileOption {
	profiles := []outboundProfileOption{}
	for _, m := range items {
		id := stringValue(m["id"])
		if !validProviderResourceID(id) {
			continue
		}
		p := outboundProfileOption{ID: id, Name: firstNonEmpty(stringValue(m["name"]), id), Enabled: boolValue(m["enabled"]), DailySpendLimitEnabled: boolValue(m["daily_spend_limit_enabled"]), DailySpendLimit: stringValue(m["daily_spend_limit"])}
		for _, d := range anyList(m["whitelisted_destinations"]) {
			p.Destinations = append(p.Destinations, stringValue(d))
		}
		profiles = append(profiles, p)
	}
	return profiles
}
func automaticSIPProfiles(ctx context.Context, creds *sdk.ConnectionCredentials) ([]outboundProfileOption, error) {
	if creds.Slug != "telnyx" {
		return []outboundProfileOption{}, nil
	}
	items, e := sipProviderList(ctx, creds, "", "/outbound_voice_profiles", "data")
	if e != nil {
		return nil, e
	}
	return sipProfiles(items), nil
}

func (a *App) provisionOutboundSIP(request context.Context, ctx *sdk.AppCtx, creds *sdk.ConnectionCredentials, connection int64, number string, args map[string]any) (outboundSIPConfig, []byte, error) {
	empty := outboundSIPConfig{}
	// Validate gateway and certificate before any carrier mutation, without
	// taking listener ownership or interrupting a running gateway.
	gateway, e := a.resolveSIPGatewayConfig(ctx, true)
	if e != nil {
		return empty, nil, fmt.Errorf("configure the SIP gateway first: %w", e)
	}
	if gateway.Transport != "tls" || gateway.SRTPMode == sipSRTPDisabled {
		return empty, nil, errors.New("automatic outbound SIP requires TLS signaling and SRTP support")
	}
	if _, e = gateway.tlsConfig(); e != nil {
		return empty, nil, fmt.Errorf("SIP gateway certificate: %w", e)
	}
	project := currentProject(ctx)
	requested := strings.TrimSpace(strArg(args, "outbound_profile_id", ""))
	if requested != "" && !validProviderResourceID(requested) {
		return empty, nil, errors.New("invalid outbound profile ID")
	}
	var policy *outboundSIPProfilePolicy
	if args["profile_policy"] != nil {
		raw, _ := json.Marshal(args["profile_policy"])
		policy = &outboundSIPProfilePolicy{}
		if json.Unmarshal(raw, policy) != nil {
			return empty, nil, errors.New("invalid outbound profile policy")
		}
		if e = validateSIPProfilePolicy(policy); e != nil {
			return empty, nil, e
		}
		if requested != "" {
			return empty, nil, errors.New("choose an existing profile or a new profile policy")
		}
	}
	if creds.Slug != "telnyx" && (requested != "" || policy != nil) {
		return empty, nil, errors.New("outbound profiles apply only to Telnyx")
	}
	// Reuse a previously verified selection when no new policy was requested.
	v, encrypted, scope, e := a.outboundTransport(project, connection, number)
	if e != nil {
		return empty, nil, e
	}
	if v.Transport == inboundTransportSIPDirect && v.Config != nil && requested == "" && policy == nil {
		if e = validateOutboundSIPConfig(creds.Slug, v.Config); e != nil {
			return empty, nil, e
		}
		if e = verifyOutboundSIPProvider(request, creds, *v.Config); e != nil {
			return empty, nil, e
		}
		if v.Config.AuthMode == "digest" {
			secret, err := openOutboundSIPCredentials(ctx, project, connection, scope, encrypted)
			if err != nil {
				return empty, nil, err
			}
			encrypted, e = sealOutboundSIPCredentials(ctx, project, connection, number, secret)
			if e != nil {
				return empty, nil, e
			}
		}
		return *v.Config, encrypted, nil
	}
	var s outboundSIPSetup
	var savedProvider, raw string
	e = a.db().db.QueryRow(`SELECT provider,state_json,credentials_cipher FROM outbound_sip_setup WHERE project_id=? AND connection_id=? AND phone_number=?`, project, connection, number).Scan(&savedProvider, &raw, &encrypted)
	if e == nil {
		if savedProvider != creds.Slug {
			return empty, nil, errors.New("carrier identity changed; select an existing SIP resource explicitly")
		}
		if e = json.Unmarshal([]byte(raw), &s); e != nil {
			return empty, nil, e
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return empty, nil, e
	} else {
		hash := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%d:%s:%s", a.installID, project, connection, number, creds.Slug)))
		s = outboundSIPSetup{Name: "apteva-out-" + hex.EncodeToString(hash[:12]), Policy: policy}
		secret, err := newManagedSIPCredentials()
		if err != nil {
			return empty, nil, err
		}
		encrypted, e = sealOutboundSIPCredentials(ctx, project, connection, number, secret)
		if e != nil {
			return empty, nil, e
		}
	}
	secret, e := openOutboundSIPCredentials(ctx, project, connection, number, encrypted)
	if e != nil {
		return empty, nil, errors.New("managed SIP credentials unavailable; restore the persisted credential key")
	}
	if policy != nil {
		if s.Policy != nil {
			x, _ := json.Marshal(s.Policy)
			y, _ := json.Marshal(policy)
			if string(x) != string(y) {
				return empty, nil, errors.New("saved setup has a different profile policy; resume it or use an existing resource")
			}
		} else if s.ProfileID != "" {
			return empty, nil, errors.New("saved setup already uses an outbound profile")
		} else {
			s.Policy = policy
		}
	}
	if s.ProfileID != "" && requested != "" && s.ProfileID != requested {
		if s.ResourceID == "" && s.PendingStep == "" && s.Policy == nil {
			s.ProfileID = requested
		} else {
			return empty, nil, errors.New("saved SIP setup uses another profile; use advanced settings to select another resource")
		}
	}
	save := func() error { return a.saveOutboundSIPSetup(project, connection, number, creds.Slug, s, encrypted) }
	// Journal intent before POST. If its outcome is uncertain, subsequent reads
	// must find that resource before another create is allowed (including eventual
	// consistency delays). Explicit provider validation/auth rejections are safe
	// to retry after the operator fixes their input or permissions.
	create := func(step, api, path string, body any) (map[string]any, error) {
		if s.PendingStep != "" {
			return nil, errors.New("previous carrier setup result is still uncertain; wait and retry to reconcile it")
		}
		if err := request.Err(); err != nil {
			return nil, err
		}
		s.PendingStep = step
		if err := save(); err != nil {
			return nil, err
		}
		result, err := sipProviderRequest(request, creds, api, "POST", path, body)
		var rejection *sipSetupHTTPError
		if errors.As(err, &rejection) && (rejection.status == 400 || rejection.status == 401 || rejection.status == 403 || rejection.status == 404 || rejection.status == 422) {
			s.PendingStep = ""
			if e := save(); e != nil {
				return nil, e
			}
		}
		return result, err
	}
	reconciled := func(step string) error {
		if s.PendingStep == step {
			s.PendingStep = ""
		}
		return save()
	}

	if creds.Slug == "telnyx" {
		profiles, e := automaticSIPProfiles(request, creds)
		if e != nil {
			return empty, nil, e
		}
		if s.ProfileID == "" {
			s.ProfileID = requested
			if s.ProfileID == "" && s.Policy == nil {
				s.ProfileID = soleEnabledOutboundProfileID(profiles)
			}
			if s.ProfileID == "" && s.Policy == nil {
				return empty, nil, errors.New("choose an enabled outbound profile, or define allowed countries and a daily spending limit")
			}
			if s.ProfileID == "" {
				for _, p := range profiles {
					if p.Name == s.Name {
						if s.ProfileID != "" {
							return empty, nil, errors.New("managed outbound profile is ambiguous")
						}
						s.ProfileID = p.ID
					}
				}
			}
		}
		if s.ProfileID != "" {
			valid := false
			for _, p := range profiles {
				if p.ID == s.ProfileID {
					valid = p.Enabled
					if s.Policy != nil {
						expected := map[string]bool{}
						for _, c := range s.Policy.Destinations {
							expected[c] = true
						}
						for _, c := range p.Destinations {
							if !expected[c] {
								valid = false
							}
							delete(expected, c)
						}
						want, _ := strconv.ParseFloat(s.Policy.DailySpendLimit, 64)
						actual, err := strconv.ParseFloat(p.DailySpendLimit, 64)
						if p.Name != s.Name || len(expected) != 0 || !p.DailySpendLimitEnabled || err != nil || actual != want {
							valid = false
						}
					}
				}
			}
			if !valid {
				return empty, nil, errors.New("selected outbound profile is missing, disabled, or no longer matches the requested calling policy")
			}
		}
		if s.ProfileID != "" && s.PendingStep == "profile" {
			if e = reconciled("profile"); e != nil {
				return empty, nil, e
			}
		}
		// Persist generated credentials and intended policy before any mutation.
		if e = save(); e != nil {
			return empty, nil, e
		}
		if s.ProfileID == "" {
			result, err := create("profile", "", "/outbound_voice_profiles", map[string]any{"name": s.Name, "enabled": true, "traffic_type": "conversational", "service_plan": "global", "whitelisted_destinations": s.Policy.Destinations, "daily_spend_limit_enabled": true, "daily_spend_limit": s.Policy.DailySpendLimit})
			if err != nil {
				return empty, nil, err
			}
			s.ProfileID, e = sipSetupResourceID(result, creds.Slug)
			if e != nil {
				return empty, nil, e
			}
			if e = reconciled("profile"); e != nil {
				return empty, nil, e
			}
		}
		if s.ResourceID == "" {
			items, err := sipProviderList(request, creds, "", "/credential_connections", "data")
			if err != nil {
				return empty, nil, err
			}
			s.ResourceID, e = uniqueManagedResource(items, "connection_name", s.Name, "id")
			if e != nil {
				return empty, nil, e
			}
			if s.ResourceID == "" {
				codecs, _ := telnyxProvisioningCodecs([]string{"G722", "PCMU", "PCMA"})
				result, err := create("connection", "", "/credential_connections", map[string]any{"connection_name": s.Name, "user_name": secret.Username, "password": secret.Password, "active": true, "dtmf_type": "RFC 2833", "inbound": map[string]any{"codecs": codecs}, "outbound": map[string]any{"outbound_voice_profile_id": s.ProfileID}, "noise_suppression": "disabled", "jitter_buffer": map[string]any{"enable_jitter_buffer": false}})
				if err != nil {
					return empty, nil, err
				}
				s.ResourceID, e = sipSetupResourceID(result, creds.Slug)
				if e != nil {
					return empty, nil, e
				}
			}
			if e = reconciled("connection"); e != nil {
				return empty, nil, e
			}
		}
		cfg := outboundSIPConfig{Host: "sip.telnyx.com", ResourceID: s.ResourceID, ResourceType: "credential_connection", AuthMode: "digest", MediaEncryption: "srtp_sdes", Codecs: []string{"G722", "PCMU", "PCMA"}}
		result, err := sipProviderGET(request, creds, "/credential_connections/"+url.PathEscape(s.ResourceID))
		if err != nil {
			return empty, nil, err
		}
		m := telnyxDataMap(result)
		outbound, _ := m["outbound"].(map[string]any)
		// Credential connections do not expose transport_protocol. Outbound TLS
		// is enforced by our SIP client (transport=tls, port 5061), independently
		// of inbound transport settings. Verify the documented identity/profile.
		if stringValue(m["connection_name"]) != s.Name || stringValue(m["user_name"]) != secret.Username || stringValue(outbound["outbound_voice_profile_id"]) != s.ProfileID {
			return empty, nil, errors.New("managed Telnyx SIP connection settings changed; resolve them before enabling SIP")
		}
		if e = verifyOutboundSIPProvider(request, creds, cfg); e != nil {
			return empty, nil, e
		}
		return cfg, encrypted, nil
	}
	if e = save(); e != nil {
		return empty, nil, e
	}
	if s.ResourceID == "" {
		items, err := sipProviderList(request, creds, "", "/Trunks", "trunks")
		if err != nil {
			return empty, nil, err
		}
		s.ResourceID, e = uniqueManagedResource(items, "friendly_name", s.Name, "sid")
		if e != nil {
			return empty, nil, e
		}
		if s.ResourceID == "" {
			result, err := create("trunk", "", "/Trunks", url.Values{"FriendlyName": {s.Name}, "DomainName": {s.Name + ".pstn.twilio.com"}, "Secure": {"true"}})
			if err != nil {
				return empty, nil, err
			}
			s.ResourceID, e = sipSetupResourceID(result, creds.Slug)
			if e != nil {
				return empty, nil, e
			}
		}
		if e = reconciled("trunk"); e != nil {
			return empty, nil, e
		}
	}
	if s.CredentialListID == "" {
		items, err := sipProviderList(request, creds, "account", "/SIP/CredentialLists.json", "credential_lists")
		if err != nil {
			return empty, nil, err
		}
		s.CredentialListID, e = uniqueManagedResource(items, "friendly_name", s.Name, "sid")
		if e != nil {
			return empty, nil, e
		}
		if s.CredentialListID == "" {
			result, err := create("credential_list", "account", "/SIP/CredentialLists.json", url.Values{"FriendlyName": {s.Name}})
			if err != nil {
				return empty, nil, err
			}
			s.CredentialListID, e = sipSetupResourceID(result, creds.Slug)
			if e != nil {
				return empty, nil, e
			}
		}
		if e = reconciled("credential_list"); e != nil {
			return empty, nil, e
		}
	}
	credentialPath := "/SIP/CredentialLists/" + url.PathEscape(s.CredentialListID) + "/Credentials.json"
	items, e := sipProviderList(request, creds, "account", credentialPath, "credentials")
	if e != nil {
		return empty, nil, e
	}
	id, e := uniqueManagedResource(items, "username", secret.Username, "sid")
	if e != nil {
		return empty, nil, e
	}
	if id == "" {
		if s.CredentialID != "" {
			return empty, nil, errors.New("managed SIP credential was removed; restore it before enabling SIP")
		}
		result, err := create("credential", "account", credentialPath, url.Values{"Username": {secret.Username}, "Password": {secret.Password}})
		if err != nil {
			return empty, nil, err
		}
		id, e = sipSetupResourceID(result, creds.Slug)
		if e != nil {
			return empty, nil, e
		}
	}
	s.CredentialID = id
	if e = reconciled("credential"); e != nil {
		return empty, nil, e
	}
	mappingPath := "/Trunks/" + url.PathEscape(s.ResourceID) + "/CredentialLists"
	items, e = sipProviderList(request, creds, "", mappingPath, "credential_lists")
	if e != nil {
		return empty, nil, e
	}
	attached := false
	for _, m := range items {
		if stringValue(m["sid"]) == s.CredentialListID {
			attached = true
		}
	}
	if !attached {
		if _, e = create("mapping", "", mappingPath, url.Values{"CredentialListSid": {s.CredentialListID}}); e != nil {
			return empty, nil, e
		}
		items, e = sipProviderList(request, creds, "", mappingPath, "credential_lists")
		if e != nil {
			return empty, nil, e
		}
		for _, m := range items {
			if stringValue(m["sid"]) == s.CredentialListID {
				attached = true
			}
		}
		if !attached {
			return empty, nil, errors.New("SIP authentication attachment is not yet verified; retry to reconcile setup")
		}
	}
	if e = reconciled("mapping"); e != nil {
		return empty, nil, e
	}

	// A managed trunk must not gain an IP ACL: Twilio enforces both schemes.
	items, e = sipProviderList(request, creds, "", "/Trunks/"+url.PathEscape(s.ResourceID)+"/IpAccessControlLists", "ip_access_control_lists")
	if e != nil {
		return empty, nil, e
	}
	if len(items) > 0 {
		return empty, nil, errors.New("managed Twilio trunk has an additional IP restriction; resolve it before enabling SIP")
	}
	cfg := outboundSIPConfig{Host: s.Name + ".pstn.twilio.com", ResourceID: s.ResourceID, ResourceType: "elastic_sip_trunk", AuthMode: "digest", MediaEncryption: "srtp_sdes", Codecs: []string{"PCMU", "PCMA"}}
	if e = verifyOutboundSIPProvider(request, creds, cfg); e != nil {
		return empty, nil, e
	}
	return cfg, encrypted, nil
}
