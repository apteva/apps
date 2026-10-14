package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const carrierVerificationLifetime = 24 * time.Hour
const previouslyVerified = "previously_verified"

// A current authenticated SDK binding is required before any lookup. Hashing
// fields prevents another account/API-key revision from inheriting evidence.
func carrierCredentialHash(creds *sdk.ConnectionCredentials) string {
	raw, _ := json.Marshal(creds.Fields)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

type carrierHTTPError struct {
	status  int
	message string
}

func (e *carrierHTTPError) Error() string { return e.message }

func temporaryCarrierVerificationError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var status int
	var carrier *carrierHTTPError
	var sip *sipSetupHTTPError
	if errors.As(err, &carrier) {
		status = carrier.status
	}
	if errors.As(err, &sip) {
		status = sip.status
	}
	switch status {
	case 408, 429, 500, 502, 503, 504:
		return true
	}
	var network net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout())
}

func (a *App) saveCarrierVerification(project string, connection int64, creds *sdk.ConnectionCredentials, kind, key string, value any, at time.Time) error {
	if numbers, ok := value.([]ownedNumber); ok {
		// Verification needs identity and capabilities, not old webhook URLs
		// (which may carry query credentials) or callback configuration.
		evidence := make([]ownedNumber, len(numbers))
		for i, n := range numbers {
			evidence[i] = ownedNumber{ProviderNumberID: n.ProviderNumberID, PhoneNumber: n.PhoneNumber,
				FriendlyName: n.FriendlyName, Capabilities: n.Capabilities, CarrierStatus: n.CarrierStatus,
				ApplicationID: n.ApplicationID, ConnectionID: n.ConnectionID}
		}
		value = evidence
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > 2<<20 {
		return errors.New("carrier verification evidence exceeds bound")
	}
	result, err := a.db().db.Exec(`INSERT INTO carrier_verification_evidence(project_id,connection_id,provider,credential_hash,kind,resource_key,value_json,verified_at_ms,observed_at_ns)
 VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,connection_id,provider,credential_hash,kind,resource_key)
 DO UPDATE SET value_json=excluded.value_json,verified_at_ms=excluded.verified_at_ms,observed_at_ns=excluded.observed_at_ns
 WHERE excluded.observed_at_ns>=observed_at_ns AND (value_json<>excluded.value_json OR observed_at_ns<excluded.observed_at_ns)`,
		project, connection, creds.Slug, carrierCredentialHash(creds), kind, key, string(raw), at.UnixMilli(), at.UnixNano())
	if err == nil {
		if changed, _ := result.RowsAffected(); changed > 0 {
			// Indexed, bounded maintenance only when evidence changes. No
			// polling job, historical scan, or audio-path work.
			_, err = a.db().db.Exec(`DELETE FROM carrier_verification_evidence WHERE rowid IN
                (SELECT rowid FROM carrier_verification_evidence WHERE verified_at_ms<? LIMIT 64)`, time.Now().Add(-carrierVerificationLifetime).UnixMilli())
		}
	}
	return err
}

func (a *App) carrierVerification(project string, connection int64, creds *sdk.ConnectionCredentials, kind, key string, out any) (time.Time, error) {
	var raw string
	var ms int64
	err := a.db().db.QueryRow(`SELECT value_json,verified_at_ms FROM carrier_verification_evidence
 WHERE project_id=? AND connection_id=? AND provider=? AND credential_hash=? AND kind=? AND resource_key=?`, project, connection, creds.Slug, carrierCredentialHash(creds), kind, key).Scan(&raw, &ms)
	if err != nil {
		return time.Time{}, err
	}
	at := time.UnixMilli(ms).UTC()
	if at.After(time.Now().Add(time.Minute)) || time.Since(at) > carrierVerificationLifetime {
		return time.Time{}, errors.New("carrier verification evidence expired")
	}
	return at, json.Unmarshal([]byte(raw), out)
}

func (a *App) forgetCarrierVerification(project string, connection int64, creds *sdk.ConnectionCredentials, kind, key string) error {
	// A timestamped tombstone also rejects a successful older in-flight read.
	return a.saveCarrierVerification(project, connection, creds, kind, key, nil, time.Now())
}

func sipVerificationKey(cfg outboundSIPConfig) string {
	raw, _ := json.Marshal(cfg)
	return "sip:" + fmt.Sprintf("%x", sha256.Sum256(raw))
}
func programmableVerificationKey(provider, application string) string {
	return "programmable:" + provider + ":" + application
}

func voiceOwnedNumber(n ownedNumber) bool {
	if !validE164(n.PhoneNumber) || (len(n.Capabilities) > 0 && !containsString(n.Capabilities, "voice")) {
		return false
	}
	switch strings.ToLower(n.CarrierStatus) {
	case "not_found", "inactive", "disabled", "released", "deleted", "suspended", "blocked":
		return false
	}
	return true
}

// Configuration alone is not ownership proof: manual routes cannot introduce
// arbitrary caller IDs while the carrier is down. It must accompany a recent,
// successful full inventory under the same credentials.
func (a *App) configuredOutboundNumber(project string, connection int64, creds *sdk.ConnectionCredentials, number string) bool {
	routes, err := a.db().listRoutesForProjectConnection(project, connection)
	if err != nil {
		return false
	}
	for _, r := range currentRoutesByNumber(routes) {
		if r.Enabled && r.CarrierSlug == creds.Slug && compactPhoneNumber(r.PhoneNumber) == compactPhoneNumber(number) {
			return true
		}
	}
	if validE164(creds.Fields["phone_number"]) && compactPhoneNumber(creds.Fields["phone_number"]) == compactPhoneNumber(number) {
		return true
	}
	var count int
	return a.db().db.QueryRow(`SELECT COUNT(*) FROM outbound_transports WHERE project_id=? AND connection_id=? AND provider=? AND phone_number=?`, project, connection, creds.Slug, number).Scan(&count) == nil && count > 0
}

func (a *App) cachedOwnedNumbers(project string, connection int64, creds *sdk.ConnectionCredentials) ([]ownedNumber, time.Time, error) {
	var numbers []ownedNumber
	at, err := a.carrierVerification(project, connection, creds, "numbers", "", &numbers)
	if err != nil {
		return nil, at, err
	}
	eligible := []ownedNumber{}
	for _, n := range numbers {
		if voiceOwnedNumber(n) && a.configuredOutboundNumber(project, connection, creds, n.PhoneNumber) {
			eligible = append(eligible, n)
		}
	}
	return eligible, at, nil
}

func (a *App) outboundOwnedNumbers(ctx *sdk.AppCtx, project string, bound *sdk.BoundIntegration, creds *sdk.ConnectionCredentials) ([]ownedNumber, error) {
	provider := &numberProvider{Slug: creds.Slug, ConnID: bound.ConnectionID, Fields: creds.Fields}
	started := time.Now()
	numbers, err := listOwnedCarrierNumbers(ctx, provider)
	if err == nil {
		if e := a.saveCarrierVerification(project, bound.ConnectionID, creds, "numbers", "", numbers, started); e != nil {
			return nil, fmt.Errorf("save number verification: %w", e)
		}
		return numbers, nil
	}
	if !temporaryCarrierVerificationError(err) {
		if !errors.Is(err, context.Canceled) {
			_ = a.forgetCarrierVerification(project, bound.ConnectionID, creds, "numbers", "")
		}
		return nil, err
	}
	cached, _, cacheErr := a.cachedOwnedNumbers(project, bound.ConnectionID, creds)
	if cacheErr != nil {
		return nil, err
	}
	return cached, nil
}

type outboundVerificationView struct {
	Status     string `json:"status"`
	VerifiedAt string `json:"verified_at"`
	ExpiresAt  string `json:"expires_at"`
}

func (a *App) cachedOutboundReadiness(project string, connection int64, creds *sdk.ConnectionCredentials, key string) (outboundReadinessView, time.Time, error) {
	var r outboundReadinessView
	at, err := a.carrierVerification(project, connection, creds, "ready", key, &r)
	if err == nil && r.Status != outboundReady {
		err = errors.New("prior configuration was not outbound ready")
	}
	return r, at, err
}

func (a *App) verifySIPWithEvidence(request context.Context, ctx *sdk.AppCtx, project string, connection int64, creds *sdk.ConnectionCredentials, cfg outboundSIPConfig) error {
	key := sipVerificationKey(cfg)
	started := time.Now()
	err := verifyOutboundSIPProvider(request, creds, cfg)
	if err == nil {
		return a.saveCarrierVerification(project, connection, creds, "ready", key, carrierOutboundReady(), started)
	}
	if !temporaryCarrierVerificationError(err) {
		if !errors.Is(err, context.Canceled) {
			_ = a.forgetCarrierVerification(project, connection, creds, "ready", key)
		}
		return err
	}
	_, _, priorErr := a.cachedOutboundReadiness(project, connection, creds, key)
	if priorErr != nil {
		return err
	}
	return nil
}

func (a *App) ensureOutboundReadyWithEvidence(ctx *sdk.AppCtx, project string, connection int64, creds *sdk.ConnectionCredentials, application string) error {
	key := programmableVerificationKey(creds.Slug, application)
	started := time.Now()
	view, err := a.outboundReadiness(ctx, creds.Slug, connection, application)
	if err != nil {
		if !temporaryCarrierVerificationError(err) {
			_ = a.forgetCarrierVerification(project, connection, creds, "ready", key)
			return err
		}
		prior, _, e := a.cachedOutboundReadiness(project, connection, creds, key)
		if e != nil {
			return err
		}
		// A fresh profile list can revoke readiness even if the application
		// lookup subsequently times out. Never revive a known-disabled profile.
		if view.profilesVerified {
			found := false
			for _, p := range view.Profiles {
				if p.ID == prior.ProfileID && p.Enabled {
					found = true
				}
			}
			if !found {
				_ = a.forgetCarrierVerification(project, connection, creds, "ready", key)
				return errors.New("previous outbound profile is missing or disabled")
			}
		}
		return nil
	}
	if view.Status == outboundAutoConfigurable {
		_, err = a.applyOutboundProfile(ctx, creds.Slug, connection, application, view.RecommendedProfileID)
		if err != nil {
			return err
		}
		view.ProfileID = view.RecommendedProfileID
		view.Status = outboundReady
	}
	if view.Status != outboundReady {
		if !errors.Is(err, context.Canceled) {
			_ = a.forgetCarrierVerification(project, connection, creds, "ready", key)
		}
		return errors.New(firstNonEmpty(view.Message, "outbound configuration is unavailable"))
	}
	return a.saveCarrierVerification(project, connection, creds, "ready", key, view, started)
}

func (a *App) cachedConnectedNumbers(ctx *sdk.AppCtx, project string, p *numberProvider) ([]connectedNumberView, error) {
	creds := &sdk.ConnectionCredentials{Slug: p.Slug, Fields: p.Fields}
	owned, at, err := a.cachedOwnedNumbers(project, p.ConnID, creds)
	if err != nil {
		return nil, err
	}
	routes, err := a.db().listRoutesForProjectConnection(project, p.ConnID)
	if err != nil {
		return nil, err
	}
	byPhone := map[string]*routeRow{}
	for _, r := range currentRoutesByNumber(routes) {
		copy := r
		byPhone[compactPhoneNumber(r.PhoneNumber)] = &copy
	}
	out := []connectedNumberView{}
	for _, n := range owned {
		proofAt := at
		route := byPhone[compactPhoneNumber(n.PhoneNumber)]
		v, _, _, e := a.outboundTransport(project, p.ConnID, n.PhoneNumber)
		if e != nil {
			return nil, e
		}
		key := programmableVerificationKey(p.Slug, "")
		if p.Slug == "telnyx" {
			key = programmableVerificationKey(p.Slug, firstNonEmpty(p.Fields["connection_id"], telnyxRouteApplicationID(route, n.ConnectionID)))
		}
		if v.Transport == inboundTransportSIPDirect && v.Config != nil {
			key = sipVerificationKey(*v.Config)
		}
		ready, readyAt, readyErr := a.cachedOutboundReadiness(project, p.ConnID, creds, key)
		view := connectedNumberView{PhoneNumber: n.PhoneNumber, Provider: p.Slug, ProviderNumberID: n.ProviderNumberID, Capabilities: append([]string{}, n.Capabilities...), CarrierStatus: n.CarrierStatus,
			VerifiedAt: at.Format(time.RFC3339Nano), RouteStatus: "not_configured", RoutingHealth: "unverified", VoiceWebhookStatus: webhookUnknown, StatusCallbackState: webhookUnknown,
			OutboundTransport: v, Outbound: outboundReadinessView{Status: outboundConfigError, Profiles: []outboundProfileOption{}, Message: "Carrier inventory unavailable; no recent verified outbound configuration"}}
		if route != nil {
			view.RouteStatus = "disabled"
			if route.Enabled {
				view.RouteStatus = "enabled"
			}
			view.Route = &connectedRouteView{ID: route.ID, Enabled: route.Enabled, AgentID: route.AgentID, Transport: route.InboundTransport, AnswerMode: route.AnswerMode, RecordingMode: route.RecordingMode}
		}
		if readyErr == nil {
			view.Outbound = ready
			view.Outbound.Status = outboundReady
			view.Outbound.Message = "Carrier inventory temporarily unavailable; using previously verified outbound configuration"
			view.OutboundFallbackAllowed = true
			if v.Transport == inboundTransportSIPDirect && a.directSIPGateway() == nil {
				view.OutboundFallbackAllowed = false
				view.Outbound.Status = outboundConfigError
				view.Outbound.Message = "SIP gateway unavailable"
			}
			if readyAt.Before(proofAt) {
				proofAt = readyAt
			}
		}
		view.VerifiedAt = proofAt.Format(time.RFC3339Nano)
		view.OutboundVerification = &outboundVerificationView{Status: previouslyVerified, VerifiedAt: proofAt.Format(time.RFC3339Nano), ExpiresAt: proofAt.Add(carrierVerificationLifetime).Format(time.RFC3339Nano)}
		out = append(out, view)
	}
	return out, nil
}

func (a *App) markOutboundFallback(project string, p *numberProvider, n *connectedNumberView, key string, ownershipAt time.Time) {
	creds := &sdk.ConnectionCredentials{Slug: p.Slug, Fields: p.Fields}
	_, at, err := a.cachedOutboundReadiness(project, p.ConnID, creds, key)
	if err != nil || !a.configuredOutboundNumber(project, p.ConnID, creds, n.PhoneNumber) || !voiceOwnedNumber(ownedNumber{PhoneNumber: n.PhoneNumber, Capabilities: n.Capabilities, CarrierStatus: n.CarrierStatus}) {
		return
	}
	if !ownershipAt.IsZero() && ownershipAt.Before(at) {
		at = ownershipAt
	}
	n.OutboundFallbackAllowed = true
	if n.OutboundTransport.Transport == inboundTransportSIPDirect && a.directSIPGateway() == nil {
		n.OutboundFallbackAllowed = false
	}
	n.OutboundVerification = &outboundVerificationView{Status: previouslyVerified, VerifiedAt: at.Format(time.RFC3339Nano), ExpiresAt: at.Add(carrierVerificationLifetime).Format(time.RFC3339Nano)}
}

// A saved SIP resource may remain ready to place calls while its separate
// verification status records that the REST check is temporarily unavailable.
func (a *App) inventorySIPReadiness(ctx *sdk.AppCtx, project string, p *numberProvider, cfg outboundSIPConfig, fresh bool) (outboundReadinessView, error) {
	creds := &sdk.ConnectionCredentials{Slug: p.Slug, Fields: p.Fields}
	key := sipVerificationKey(cfg)
	prior, at, priorErr := a.cachedOutboundReadiness(project, p.ConnID, creds, key)
	if !fresh && priorErr == nil && time.Since(at) < inventoryCacheTTL {
		return prior, nil
	}
	request := p.inventoryContext
	if request == nil {
		request = context.Background()
	}
	started := time.Now()
	err := verifyOutboundSIPProvider(request, creds, cfg)
	if err == nil {
		r := carrierOutboundReady()
		err = a.saveCarrierVerification(project, p.ConnID, creds, "ready", key, r, started)
		return r, err
	}
	if !temporaryCarrierVerificationError(err) {
		if !errors.Is(err, context.Canceled) {
			_ = a.forgetCarrierVerification(project, p.ConnID, creds, "ready", key)
		}
		return outboundReadinessView{}, err
	}
	if priorErr == nil {
		prior.Status = previouslyVerified
		return prior, err
	}
	return outboundReadinessView{}, err
}
