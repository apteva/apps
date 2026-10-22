package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"github.com/pion/sdp/v3"
	"strings"
)

type sipOutboundCarrier struct {
	app                        *App
	provider                   string
	fields                     map[string]string
	timeoutSec, maxDurationSec int
}

func (c *sipOutboundCarrier) Slug() string { return c.provider }
func (c *sipOutboundCarrier) ControlFeatures() carrierControlFeatures {
	return carrierControlFeatures{HoldMusic: true, RecordingPause: true}
}
func (c *sipOutboundCarrier) Place(_ *sdk.AppCtx, req carrierPlaceRequest) (*carrierPlaceResult, error) {
	if _, err := genericOutboundSIPSettings(c.provider, c.fields); err != nil {
		return nil, err
	}
	if !validE164(req.To) || !validE164(req.From) || req.CallID == "" {
		return nil, errors.New("outbound SIP requires E.164 caller ID, destination and call ID")
	}
	if req.MachineDetection != "" && req.MachineDetection != machineDetectionOff {
		return nil, errors.New("direct SIP does not support provider answering machine detection; select off or programmable transport")
	}
	c.timeoutSec, c.maxDurationSec = req.TimeoutSec, req.MaxDurationSec
	return &carrierPlaceResult{CarrierSID: req.CallID}, nil
}
func (c *sipOutboundCarrier) StartPlacedCall(ctx *sdk.AppCtx, row *callRow) error {
	settings, err := genericOutboundSIPSettings(c.provider, c.fields)
	if err != nil {
		return err
	}
	if err = c.app.ensureSIPGateway(ctx); err != nil {
		return fmt.Errorf("prepare outbound SIP gateway: %w", err)
	}
	g := c.app.directSIPGateway()
	if g == nil {
		return errors.New("outbound SIP gateway unavailable")
	}
	return g.startSIPOutbound(row, settings, c.timeoutSec, c.maxDurationSec)
}
func (c *sipOutboundCarrier) Hangup(_ *sdk.AppCtx, row *callRow) error {
	return hangupOutboundSIP(c.app, row)
}
func hangupOutboundSIP(a *App, row *callRow) error {
	if row == nil {
		return errors.New("outbound SIP call missing")
	}
	g := a.directSIPGateway()
	if g == nil {
		return errors.New("outbound SIP gateway unavailable")
	}
	s := g.outboundSessionByCall(row.ID)
	if s == nil {
		g.mu.RLock()
		cancel := g.outboundPending[row.ID]
		g.mu.RUnlock()
		if cancel != nil {
			cancel()
			return nil
		}
		return errors.New("outbound SIP dialog is no longer active")
	}
	s.finish("local", nil)
	return nil
}
func genericOutboundSIPSettings(provider string, fields map[string]string) (didwwSIPSettings, error) {
	cfg := outboundSIPConfig{Host: fields["sip_host"], ResourceID: fields["sip_resource_id"], AuthMode: fields["sip_auth_mode"], MediaEncryption: fields["sip_media_encryption"]}
	if provider == "telnyx" {
		cfg.ResourceType = fields["sip_resource_type"]
	}
	if fields["sip_codecs"] != "" {
		if err := json.Unmarshal([]byte(fields["sip_codecs"]), &cfg.Codecs); err != nil {
			return didwwSIPSettings{}, errors.New("invalid outbound SIP codecs")
		}
	}
	if err := validateOutboundSIPConfig(provider, &cfg); err != nil {
		return didwwSIPSettings{}, err
	}
	secret := outboundSIPCredentials{Username: fields["sip_username"], Password: fields["sip_password"]}
	if cfg.AuthMode == "digest" && !validSIPDigestCredentials(secret) {
		return didwwSIPSettings{}, errors.New("SIP digest credentials required")
	}
	settings := didwwSIPSettings{host: cfg.Host, port: 5061, username: secret.Username, password: secret.Password, secure: cfg.MediaEncryption == "srtp_sdes", codecs: cfg.Codecs, scheme: "sip", e164: true}
	if provider == "telnyx" && cfg.AuthMode == "digest" {
		settings.anchorUsername = secret.Username
	}
	return settings, nil
}
func buildOutboundSIPOffer(cfg sipGatewayConfig, settings didwwSIPSettings, port int, key []byte) ([]byte, error) {
	codecs := settings.codecs
	if len(codecs) == 0 {
		codecs = []string{"PCMU"}
	}
	payload := map[string]uint8{"G722": 9, "PCMU": 0, "PCMA": 8}
	body, err := buildSIPMediaAnswer(cfg, sipMediaOffer{Secure: settings.secure, PayloadType: payload[codecs[0]], Codec: codecs[0], CryptoTag: "1", DTMFPayloadType: 101}, port, &sipMediaSecurity{LocalKey: key})
	if err != nil {
		return nil, err
	}
	var desc sdp.SessionDescription
	if err = desc.Unmarshal(body); err != nil {
		return nil, err
	}
	media := desc.MediaDescriptions[0]
	formats := []string{}
	attrs := []sdp.Attribute{}
	for _, attr := range media.Attributes {
		if attr.Key == "rtpmap" && !strings.HasPrefix(attr.Value, "101 ") {
			continue
		}
		attrs = append(attrs, attr)
	}
	for _, codec := range codecs {
		pt := fmt.Sprintf("%d", payload[codec])
		formats = append(formats, pt)
		attrs = append(attrs, sdp.NewAttribute("rtpmap", pt+" "+codec+"/8000"))
	}
	media.MediaName.Formats = append(formats, "101")
	media.Attributes = attrs
	return desc.Marshal()
}
func rowUsesOutboundSIP(row *callRow) bool {
	return row != nil && row.Direction == "outbound" && (row.IngressPath == inboundTransportSIPDirect || row.CarrierSlug == "didww")
}
