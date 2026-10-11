package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Passwords are write-only, encrypted with an install-local persisted key.
// Account defaults use an empty phone_number; a number override takes priority.
type outboundSIPConfig struct {
	Host            string   `json:"host"`
	ResourceID      string   `json:"resource_id"`
	ResourceType    string   `json:"resource_type,omitempty"`
	AuthMode        string   `json:"auth_mode"`
	MediaEncryption string   `json:"media_encryption"`
	Codecs          []string `json:"codecs,omitempty"`
}
type outboundSIPCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type outboundTransportView struct {
	Transport             string             `json:"transport"`
	Scope                 string             `json:"scope"`
	Config                *outboundSIPConfig `json:"config,omitempty"`
	CredentialsConfigured bool               `json:"credentials_configured"`
	VerifiedAt            string             `json:"verified_at,omitempty"`
}

var sipSecretMu sync.Mutex
var twilioTerminationHost = regexp.MustCompile(`^[a-z0-9-]+\.pstn(?:\.[a-z0-9-]+)?\.twilio\.com$`)

func validateOutboundSIPConfig(provider string, cfg *outboundSIPConfig) error {
	cfg.Host = strings.ToLower(strings.TrimSpace(cfg.Host))
	cfg.ResourceID = strings.TrimSpace(cfg.ResourceID)
	if cfg.AuthMode == "" {
		cfg.AuthMode = "digest"
	}
	if cfg.MediaEncryption == "" {
		cfg.MediaEncryption = "srtp_sdes"
	}
	if cfg.AuthMode != "digest" && cfg.AuthMode != "ip" {
		return errors.New("auth_mode must be digest or ip")
	}
	if cfg.MediaEncryption != "srtp_sdes" && cfg.MediaEncryption != "disabled" {
		return errors.New("media_encryption must be srtp_sdes or disabled")
	}
	if !validProviderResourceID(cfg.ResourceID) {
		return errors.New("SIP resource_id is required")
	}
	switch provider {
	case "twilio":
		if !twilioTerminationHost.MatchString(cfg.Host) || !strings.HasPrefix(cfg.ResourceID, "TK") {
			return errors.New("Twilio requires its Elastic SIP trunk ID and pstn.twilio.com termination hostname")
		}
		cfg.ResourceType = "elastic_sip_trunk"
		if len(cfg.Codecs) == 0 {
			cfg.Codecs = []string{"PCMU", "PCMA"}
		}
		for _, c := range cfg.Codecs {
			if c == "G722" {
				return errors.New("Twilio outbound trunk supports PCMU/PCMA")
			}
		}
	case "telnyx":
		if cfg.Host != "sip.telnyx.com" {
			return errors.New("Telnyx outbound host must be sip.telnyx.com")
		}
		if cfg.ResourceType == "" {
			cfg.ResourceType = "credential_connection"
		}
		switch cfg.ResourceType {
		case "credential_connection", "ip_connection", "fqdn_connection":
		default:
			return errors.New("invalid Telnyx SIP resource_type")
		}
		if cfg.ResourceType == "credential_connection" && cfg.AuthMode != "digest" || cfg.ResourceType != "credential_connection" && cfg.AuthMode != "ip" {
			return errors.New("Telnyx authentication must match the SIP connection type")
		}
		if len(cfg.Codecs) == 0 {
			cfg.Codecs = []string{"G722", "PCMU", "PCMA"}
		}
	default:
		return errors.New("outbound transport selection currently supports Telnyx and Twilio")
	}
	return validateLocalMediaSettings(localMediaSettings{Codecs: cfg.Codecs})
}

func outboundSIPAEAD(ctx *sdk.AppCtx, create bool) (cipher.AEAD, error) {
	root := ctx.DataDir()
	if root == "" {
		return nil, errors.New("persistent app data directory required for SIP credentials")
	}
	sipSecretMu.Lock()
	defer sipSecretMu.Unlock()
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "outbound-sip.key")
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && create {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		_, err = f.Write(key)
		if err == nil {
			err = f.Sync()
		}
		e = f.Close()
		if err == nil {
			err = e
		}
		if err != nil {
			_ = os.Remove(path)
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid SIP credential key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func sealOutboundSIPCredentials(ctx *sdk.AppCtx, project string, connection int64, number string, c outboundSIPCredentials) ([]byte, error) {
	aead, err := outboundSIPAEAD(ctx, true)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(c)
	aad := []byte(fmt.Sprintf("%s:%d:%s", project, connection, number))
	return aead.Seal(nonce, nonce, raw, aad), nil
}
func openOutboundSIPCredentials(ctx *sdk.AppCtx, project string, connection int64, number string, data []byte) (outboundSIPCredentials, error) {
	var c outboundSIPCredentials
	aead, err := outboundSIPAEAD(ctx, false)
	if err != nil {
		return c, err
	}
	if len(data) < aead.NonceSize() {
		return c, errors.New("SIP credentials unavailable")
	}
	raw, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(fmt.Sprintf("%s:%d:%s", project, connection, number)))
	if err != nil {
		return c, errors.New("SIP credentials unavailable")
	}
	err = json.Unmarshal(raw, &c)
	return c, err
}
func (a *App) outboundTransport(project string, connection int64, number string) (outboundTransportView, []byte, string, error) {
	v := outboundTransportView{Transport: inboundTransportProgrammable, Scope: "default"}
	var raw, scope, provider string
	var encrypted []byte
	err := a.db().db.QueryRow(`SELECT transport,config_json,credentials_cipher,verified_at,phone_number,provider FROM outbound_transports WHERE project_id=? AND connection_id=? AND phone_number IN ('',?) ORDER BY CASE WHEN phone_number=? THEN 0 ELSE 1 END LIMIT 1`, project, connection, number, number).Scan(&v.Transport, &raw, &encrypted, &v.VerifiedAt, &scope, &provider)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil, "", nil
	}
	if err != nil {
		return v, nil, "", err
	}
	v.Scope = "account"
	if scope != "" {
		v.Scope = "number"
	}
	if v.Transport == inboundTransportSIPDirect {
		var cfg outboundSIPConfig
		if err = json.Unmarshal([]byte(raw), &cfg); err != nil {
			return v, nil, scope, err
		}
		v.Config = &cfg
		v.CredentialsConfigured = len(encrypted) > 0
	}
	return v, encrypted, scope, nil
}

// Provider reads use only the authorized binding's credentials, fixed HTTPS
// hosts, a single bounded deadline, no redirects, and sanitized errors.
func sipProviderGET(request context.Context, creds *sdk.ConnectionCredentials, path string) (map[string]any, error) {
	return sipProviderRequest(request, creds, "", "GET", path, nil)
}
func verifyOutboundSIPProvider(request context.Context, creds *sdk.ConnectionCredentials, cfg outboundSIPConfig) error {
	if creds.Slug == "twilio" {
		response, err := sipProviderGET(request, creds, "/Trunks/"+url.PathEscape(cfg.ResourceID))
		if err != nil {
			return err
		}
		host := strings.ToLower(stringValue(response["domain_name"]))
		// Regional termination hosts identify the same trunk domain.
		root := strings.Split(cfg.Host, ".pstn")[0] + ".pstn.twilio.com"
		if host != root || cfg.MediaEncryption == "srtp_sdes" && !boolValue(response["secure"]) {
			return errors.New("Twilio trunk termination domain or secure-media setting does not match")
		}
		if recording, ok := response["recording"].(map[string]any); ok && stringValue(recording["mode"]) != "" && stringValue(recording["mode"]) != "do-not-record" {
			return errors.New("disable Twilio trunk recording when using local SIP recording")
		}
		return nil
	}
	path := "/" + cfg.ResourceType + "s/" + url.PathEscape(cfg.ResourceID)
	response, err := sipProviderGET(request, creds, path)
	if err != nil {
		return err
	}
	data := telnyxDataMap(response)
	if !boolValue(data["active"]) {
		return errors.New("Telnyx SIP connection is inactive")
	}
	outbound, _ := data["outbound"].(map[string]any)
	profile := stringValue(outbound["outbound_voice_profile_id"])
	if !validProviderResourceID(profile) {
		return errors.New("Telnyx SIP connection needs an outbound voice profile")
	}
	response, err = sipProviderGET(request, creds, "/outbound_voice_profiles/"+url.PathEscape(profile))
	if err != nil {
		return err
	}
	if !boolValue(telnyxDataMap(response)["enabled"]) {
		return errors.New("Telnyx outbound voice profile is disabled")
	}
	return nil
}

func (a *App) configureOutboundTransport(ctx *sdk.AppCtx, args map[string]any, callers ...context.Context) (map[string]any, error) {
	parent := context.Background()
	if len(callers) > 0 && callers[0] != nil {
		parent = callers[0]
	}
	budget := 8 * time.Second
	if boolArg(args, "provision", false) {
		budget = 30 * time.Second
	}
	request, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	project := currentProject(ctx)
	connection := intArg(args, "carrier_connection_id", 0)
	number := strings.TrimSpace(strArg(args, "phone_number", ""))
	if project == "" || connection <= 0 {
		return nil, errors.New("project and carrier_connection_id required")
	}
	var bound *sdk.BoundIntegration
	for _, b := range ctx.IntegrationsFor("carrier") {
		if b != nil && b.ConnectionID == int64(connection) {
			bound = b
			break
		}
	}
	if bound == nil {
		return nil, errors.New("carrier account is not authorized")
	}
	creds, err := ctx.PlatformAPI().GetConnectionCredentials(int64(connection))
	if err != nil || creds == nil {
		return nil, errors.New("carrier credentials unavailable")
	}
	normalized := *creds
	normalized.Slug = strings.ToLower(firstNonEmpty(creds.Slug, bound.AppSlug))
	creds = &normalized
	provider := creds.Slug
	if provider != "telnyx" && provider != "twilio" {
		return nil, errors.New("outbound transport selection supports Telnyx and Twilio")
	}
	if number != "" && !validE164(number) {
		return nil, errors.New("phone_number must be E.164")
	}
	if number != "" && !a.outboundNumberBelongsToBinding(ctx, project, bound, creds, number, request) {
		return nil, errors.New("caller ID is not owned by this carrier account")
	}
	transport := strArg(args, "transport", "")
	if transport != "" || boolArg(args, "inherit", false) {
		unlock, e := lockOutboundSetup(request, fmt.Sprintf("%s:%d:%s:%d", ctx.DataDir(), a.installID, project, connection))
		if e != nil {
			return nil, e
		}
		defer unlock()
	}
	if boolArg(args, "inherit", false) {
		_, err = a.db().db.ExecContext(request, `DELETE FROM outbound_transports WHERE project_id=? AND connection_id=? AND phone_number=?`, project, connection, number)
		a.inventoryReads.invalidate()
		return map[string]any{"ok": err == nil}, err
	}
	if transport == "" {
		v, _, _, e := a.outboundTransport(project, int64(connection), number)
		if e != nil {
			return nil, e
		}
		result := map[string]any{"ok": true, "outbound_transport": v}
		if boolArg(args, "setup_options", false) {
			profiles, err := automaticSIPProfiles(request, creds)
			options := map[string]any{"profiles": profiles, "automatic_supported": true}
			if err != nil {
				options["profiles"] = []outboundProfileOption{}
				options["warning"] = err.Error()
			}
			result["setup_options"] = options
		}
		return result, nil
	}
	if transport != inboundTransportSIPDirect && transport != inboundTransportProgrammable {
		return nil, errors.New("invalid outbound transport")
	}
	cfg := outboundSIPConfig{}
	var encrypted []byte
	verified := ""
	if transport == inboundTransportSIPDirect {
		if boolArg(args, "provision", false) {
			if args["config"] != nil || strArg(args, "username", "") != "" || strArg(args, "password", "") != "" {
				return nil, errors.New("automatic setup manages SIP settings and credentials; use advanced setup for an existing resource")
			}
			cfg, encrypted, err = a.provisionOutboundSIP(request, ctx, creds, int64(connection), number, args)
			if err != nil {
				return nil, err
			}
		} else {
			raw, _ := json.Marshal(args["config"])
			if err = json.Unmarshal(raw, &cfg); err != nil {
				return nil, errors.New("invalid outbound SIP config")
			}
			if err = validateOutboundSIPConfig(provider, &cfg); err != nil {
				return nil, err
			}
			// Omitted credentials retain only this exact scope's encrypted secret.
			var old []byte
			_ = a.db().db.QueryRow(`SELECT credentials_cipher FROM outbound_transports WHERE project_id=? AND connection_id=? AND phone_number=?`, project, connection, number).Scan(&old)
			credentials := outboundSIPCredentials{Username: strArg(args, "username", ""), Password: strArg(args, "password", "")}
			if cfg.AuthMode == "digest" {
				if credentials.Username == "" && credentials.Password == "" {
					encrypted = old
				} else {
					if !validSIPDigestCredentials(credentials) {
						return nil, errors.New("SIP digest username and password required")
					}
					encrypted, err = sealOutboundSIPCredentials(ctx, project, int64(connection), number, credentials)
					if err != nil {
						return nil, err
					}
				}
				if len(encrypted) == 0 {
					return nil, errors.New("SIP digest credentials required")
				}
				if _, err = openOutboundSIPCredentials(ctx, project, int64(connection), number, encrypted); err != nil {
					return nil, errors.New("saved SIP credentials unavailable; restore credential key or supply credentials")
				}
			}
			if err = verifyOutboundSIPProvider(request, creds, cfg); err != nil {
				return nil, err
			}
		}
		verified = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if err = request.Err(); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(cfg)
	_, err = a.db().db.ExecContext(request, `INSERT INTO outbound_transports(project_id,connection_id,phone_number,provider,transport,config_json,credentials_cipher,verified_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(project_id,connection_id,phone_number) DO UPDATE SET provider=excluded.provider,transport=excluded.transport,config_json=excluded.config_json,credentials_cipher=excluded.credentials_cipher,verified_at=excluded.verified_at`, project, connection, number, provider, transport, string(raw), encrypted, verified)
	a.inventoryReads.invalidate()
	if err != nil {
		return nil, err
	}
	v, _, _, err := a.outboundTransport(project, int64(connection), number)
	return map[string]any{"ok": err == nil, "outbound_transport": v, "applies_to": "new_calls"}, err
}
func validSIPDigestCredentials(c outboundSIPCredentials) bool {
	return c.Username != "" && c.Password != "" && len(c.Username) <= 128 && len(c.Password) <= 256 && !strings.ContainsAny(c.Username, "\r\n\x00") && !strings.ContainsAny(c.Password, "\r\n\x00")
}
func (a *App) resolveOutboundSIPCredentials(ctx *sdk.AppCtx, project string, bound *sdk.BoundIntegration, creds *sdk.ConnectionCredentials, from string) (*sdk.ConnectionCredentials, bool, error) {
	normalized := *creds
	normalized.Slug = strings.ToLower(firstNonEmpty(creds.Slug, bound.AppSlug))
	creds = &normalized
	provider := creds.Slug
	if provider != "telnyx" && provider != "twilio" {
		return creds, false, nil
	}
	v, encrypted, scope, err := a.outboundTransport(project, bound.ConnectionID, from)
	if err != nil {
		return nil, false, err
	}
	if v.Transport != inboundTransportSIPDirect {
		return creds, false, nil
	}
	if v.Config == nil {
		return nil, true, errors.New("SIP config missing")
	}
	cfg := *v.Config
	if err = validateOutboundSIPConfig(provider, &cfg); err != nil {
		return nil, true, err
	}
	var secret outboundSIPCredentials
	if cfg.AuthMode == "digest" {
		secret, err = openOutboundSIPCredentials(ctx, project, bound.ConnectionID, scope, encrypted)
		if err != nil {
			return nil, true, err
		}
		if !validSIPDigestCredentials(secret) {
			return nil, true, errors.New("SIP credentials invalid")
		}
	}
	request, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err = verifyOutboundSIPProvider(request, creds, cfg); err != nil {
		return nil, true, err
	}
	resolved := *creds
	resolved.Slug = provider
	resolved.Fields = make(map[string]string, len(creds.Fields)+8)
	for k, v := range creds.Fields {
		resolved.Fields[k] = v
	}
	resolved.Fields["outbound_transport"] = inboundTransportSIPDirect
	resolved.Fields["sip_host"] = cfg.Host
	resolved.Fields["sip_username"] = secret.Username
	resolved.Fields["sip_password"] = secret.Password
	resolved.Fields["sip_auth_mode"] = cfg.AuthMode
	resolved.Fields["sip_media_encryption"] = cfg.MediaEncryption
	raw, _ := json.Marshal(cfg.Codecs)
	resolved.Fields["sip_codecs"] = string(raw)
	resolved.Fields["sip_resource_id"] = cfg.ResourceID
	resolved.Fields["sip_resource_type"] = cfg.ResourceType
	return &resolved, true, nil
}
func (a *App) toolOutboundTransport(caller context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if callerAgentID(caller) != 0 {
		return mcpError("outbound transport settings require a platform principal"), nil
	}
	result, err := a.configureOutboundTransport(ctx, args, caller)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return result, nil
}
