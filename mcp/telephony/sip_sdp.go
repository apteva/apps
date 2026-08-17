package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/pion/sdp/v3"
	"github.com/pion/srtp/v3"
)

const sipSDESCryptoSuite = "AES_CM_128_HMAC_SHA1_80"

type sipMediaOffer struct {
	RemoteAddress netip.Addr
	RemotePort    int
	PayloadType   uint8
	Codec         string
	PacketSamples int
	Secure        bool
	RemoteKey     []byte
}

type sipMediaSecurity struct {
	RemoteContext *srtp.Context
	LocalContext  *srtp.Context
	LocalKey      []byte
}

func parseSIPMediaOffer(body []byte, cfg sipGatewayConfig) (sipMediaOffer, error) {
	if len(body) == 0 || len(body) > 64<<10 {
		return sipMediaOffer{}, errors.New("SIP INVITE must contain an SDP offer smaller than 64 KB")
	}
	var description sdp.SessionDescription
	if err := description.Unmarshal(body); err != nil {
		return sipMediaOffer{}, fmt.Errorf("parse SDP offer: %w", err)
	}
	for _, media := range description.MediaDescriptions {
		if !strings.EqualFold(media.MediaName.Media, "audio") || media.MediaName.Port.Value == 0 {
			continue
		}
		if media.MediaName.Port.Value < 1 || media.MediaName.Port.Value > 65535 {
			return sipMediaOffer{}, errors.New("SDP audio port is invalid")
		}
		protocol := strings.ToUpper(strings.Join(media.MediaName.Protos, "/"))
		secure := protocol == "RTP/SAVP" || protocol == "RTP/SAVPF"
		if !secure && protocol != "RTP/AVP" && protocol != "RTP/AVPF" {
			return sipMediaOffer{}, fmt.Errorf("unsupported SDP media protocol %s", protocol)
		}
		if cfg.SRTPMode == sipSRTPRequired && !secure {
			return sipMediaOffer{}, errors.New("SRTP is required but the carrier offered plain RTP")
		}
		if cfg.SRTPMode == sipSRTPDisabled && secure {
			return sipMediaOffer{}, errors.New("carrier offered SRTP but direct SIP SRTP is disabled")
		}
		for _, attribute := range media.Attributes {
			switch strings.ToLower(attribute.Key) {
			case "inactive", "recvonly":
				return sipMediaOffer{}, errors.New("SDP offer does not allow bidirectional audio")
			}
		}

		address := mediaConnectionAddress(media, &description)
		remoteIP, err := netip.ParseAddr(address)
		if err != nil || !remoteIP.Is4() || remoteIP.IsMulticast() || remoteIP.IsUnspecified() {
			return sipMediaOffer{}, errors.New("SDP media address must be an explicit unicast IPv4 address")
		}
		if !cfg.sourceAllowed(remoteIP.String()) {
			return sipMediaOffer{}, errors.New("SDP media address is outside TELEPHONY_SIP_ALLOWED_CIDRS")
		}
		payloadType, codec, err := negotiatedG711Codec(media)
		if err != nil {
			return sipMediaOffer{}, err
		}
		packetTimeMS, err := sipPacketTimeMS(media.Attributes)
		if err != nil {
			return sipMediaOffer{}, err
		}
		offer := sipMediaOffer{
			RemoteAddress: remoteIP,
			RemotePort:    media.MediaName.Port.Value,
			PayloadType:   payloadType,
			Codec:         codec,
			PacketSamples: packetTimeMS * 8,
			Secure:        secure,
		}
		if secure {
			offer.RemoteKey, err = parseSDESCryptoKey(media.Attributes)
			if err != nil {
				return sipMediaOffer{}, err
			}
		}
		return offer, nil
	}
	return sipMediaOffer{}, errors.New("SDP offer has no active audio media")
}

func sipPacketTimeMS(attributes []sdp.Attribute) (int, error) {
	const defaultPacketTimeMS = 20
	for _, attribute := range attributes {
		if !strings.EqualFold(attribute.Key, "ptime") {
			continue
		}
		value, err := strconv.Atoi(strings.TrimSpace(attribute.Value))
		if err != nil || value < 10 || value > 60 || value%10 != 0 {
			return 0, errors.New("SDP ptime must be 10-60 ms in 10 ms increments")
		}
		return value, nil
	}
	return defaultPacketTimeMS, nil
}

func mediaConnectionAddress(media *sdp.MediaDescription, description *sdp.SessionDescription) string {
	connection := media.ConnectionInformation
	if connection == nil {
		connection = description.ConnectionInformation
	}
	if connection == nil || connection.Address == nil {
		return ""
	}
	return strings.TrimSpace(connection.Address.Address)
}

func negotiatedG711Codec(media *sdp.MediaDescription) (uint8, string, error) {
	rtpmap := make(map[string]string)
	for _, attribute := range media.Attributes {
		if !strings.EqualFold(attribute.Key, "rtpmap") {
			continue
		}
		parts := strings.Fields(attribute.Value)
		if len(parts) >= 2 {
			rtpmap[parts[0]] = strings.ToUpper(parts[1])
		}
	}
	for _, preferred := range []struct {
		static string
		name   string
	}{
		{static: "0", name: "PCMU"},
		{static: "8", name: "PCMA"},
	} {
		for _, format := range media.MediaName.Formats {
			codec := rtpmap[format]
			if format == preferred.static || strings.HasPrefix(codec, preferred.name+"/8000") {
				value, err := strconv.ParseUint(format, 10, 8)
				if err != nil || value > 127 {
					continue
				}
				return uint8(value), preferred.name, nil
			}
		}
	}
	return 0, "", errors.New("carrier must offer G.711 PCMU or PCMA at 8 kHz")
}

func parseSDESCryptoKey(attributes []sdp.Attribute) ([]byte, error) {
	for _, attribute := range attributes {
		if !strings.EqualFold(attribute.Key, "crypto") {
			continue
		}
		parts := strings.Fields(attribute.Value)
		if len(parts) < 3 || !strings.EqualFold(parts[1], sipSDESCryptoSuite) || !strings.HasPrefix(parts[2], "inline:") {
			continue
		}
		encoded := strings.TrimPrefix(parts[2], "inline:")
		if index := strings.IndexByte(encoded, '|'); index >= 0 {
			encoded = encoded[:index]
		}
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(key) != 30 {
			return nil, errors.New("SDES SRTP key must contain 30 bytes of key and salt material")
		}
		return key, nil
	}
	return nil, errors.New("SRTP offer has no supported SDES AES_CM_128_HMAC_SHA1_80 crypto attribute")
}

func newSIPMediaSecurity(offer sipMediaOffer) (*sipMediaSecurity, error) {
	if !offer.Secure {
		return nil, nil
	}
	localKey := make([]byte, 30)
	if _, err := rand.Read(localKey); err != nil {
		return nil, fmt.Errorf("generate SRTP key: %w", err)
	}
	remoteContext, err := srtp.CreateContext(offer.RemoteKey[:16], offer.RemoteKey[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
	if err != nil {
		return nil, fmt.Errorf("create inbound SRTP context: %w", err)
	}
	localContext, err := srtp.CreateContext(localKey[:16], localKey[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
	if err != nil {
		return nil, fmt.Errorf("create outbound SRTP context: %w", err)
	}
	return &sipMediaSecurity{RemoteContext: remoteContext, LocalContext: localContext, LocalKey: localKey}, nil
}

func buildSIPMediaAnswer(cfg sipGatewayConfig, offer sipMediaOffer, localPort int, security *sipMediaSecurity) ([]byte, error) {
	protocol := []string{"RTP", "AVP"}
	attributes := []sdp.Attribute{
		sdp.NewAttribute("rtpmap", fmt.Sprintf("%d %s/8000", offer.PayloadType, offer.Codec)),
		sdp.NewPropertyAttribute("sendrecv"),
		sdp.NewAttribute("ptime", "20"),
	}
	if offer.Secure {
		if security == nil || len(security.LocalKey) != 30 {
			return nil, errors.New("SRTP answer has no local key")
		}
		protocol = []string{"RTP", "SAVP"}
		attributes = append(attributes, sdp.NewAttribute("crypto",
			"1 "+sipSDESCryptoSuite+" inline:"+base64.StdEncoding.EncodeToString(security.LocalKey)))
	}
	sessionIDBytes := make([]byte, 8)
	if _, err := rand.Read(sessionIDBytes); err != nil {
		return nil, err
	}
	var sessionID uint64
	for _, value := range sessionIDBytes {
		sessionID = sessionID<<8 | uint64(value)
	}
	publicIP := cfg.PublicIP.String()
	description := sdp.SessionDescription{
		Version: 0,
		Origin: sdp.Origin{
			Username: "-", SessionID: sessionID, SessionVersion: 1,
			NetworkType: "IN", AddressType: "IP4", UnicastAddress: publicIP,
		},
		SessionName: sdp.SessionName("Apteva Telephony"),
		ConnectionInformation: &sdp.ConnectionInformation{
			NetworkType: "IN", AddressType: "IP4", Address: &sdp.Address{Address: publicIP},
		},
		TimeDescriptions: []sdp.TimeDescription{{Timing: sdp.Timing{StartTime: 0, StopTime: 0}}},
		MediaDescriptions: []*sdp.MediaDescription{{
			MediaName: sdp.MediaName{
				Media: "audio", Port: sdp.RangedPort{Value: localPort},
				Protos: protocol, Formats: []string{strconv.Itoa(int(offer.PayloadType))},
			},
			Attributes: attributes,
		}},
	}
	return description.Marshal()
}
