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
	RemoteAddress     netip.Addr
	RemotePort        int
	RemoteRTCPPort    int
	RemoteRTCPAddress netip.Addr
	PayloadType       uint8
	Codec             string
	Direction         string
	PacketSamples     int
	Secure            bool
	RemoteKey         []byte
	DTMFPayloadType   int
	CryptoTag         string
	// Every offered m-line in order; the answer mirrors them (RFC 3264 §6),
	// rejecting all but MediaIndex with port 0.
	MediaLines []sdp.MediaName
	MediaIndex int
}

type sipMediaSecurity struct {
	RemoteContext *srtp.Context
	LocalContext  *srtp.Context
	LocalKey      []byte
	RemoteRTCP    *srtp.Context
	LocalRTCP     *srtp.Context
}

func parseSIPMediaOffer(body []byte, cfg sipGatewayConfig) (sipMediaOffer, error) {
	return parseSIPMediaOfferCodecs(body, cfg, nil)
}
func parseSIPMediaOfferCodecs(body []byte, cfg sipGatewayConfig, codecs []string) (sipMediaOffer, error) {
	return parseSIPMediaOfferOptions(body, cfg, codecs, false)
}
func parseSIPMediaOfferOptions(body []byte, cfg sipGatewayConfig, codecs []string, allowHold bool) (sipMediaOffer, error) {
	if len(body) == 0 || len(body) > 64<<10 {
		return sipMediaOffer{}, errors.New("SIP INVITE must contain an SDP offer smaller than 64 KB")
	}
	var description sdp.SessionDescription
	if err := description.Unmarshal(body); err != nil {
		return sipMediaOffer{}, fmt.Errorf("parse SDP offer: %w", err)
	}
	var firstErr error
	for index, media := range description.MediaDescriptions {
		if !strings.EqualFold(media.MediaName.Media, "audio") || media.MediaName.Port.Value == 0 {
			continue
		}
		// An unsupported audio stream is rejected individually; a later
		// supported one can still be accepted.
		offer, err := parseSIPAudioMedia(media, &description, cfg, codecs, allowHold)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		offer.MediaIndex = index
		for _, line := range description.MediaDescriptions {
			offer.MediaLines = append(offer.MediaLines, line.MediaName)
		}
		return offer, nil
	}
	if firstErr != nil {
		return sipMediaOffer{}, firstErr
	}
	return sipMediaOffer{}, errors.New("SDP offer has no active audio media")
}

func parseSIPAudioMedia(media *sdp.MediaDescription, description *sdp.SessionDescription, cfg sipGatewayConfig, codecs []string, allowHold bool) (sipMediaOffer, error) {
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
	direction := "sendrecv"
	for _, attributes := range [][]sdp.Attribute{description.Attributes, media.Attributes} {
		for _, attribute := range attributes {
			switch strings.ToLower(attribute.Key) {
			case "sendrecv", "sendonly", "recvonly", "inactive":
				direction = strings.ToLower(attribute.Key)
			}
		}
	}
	if direction != "sendrecv" && !allowHold {
		return sipMediaOffer{}, errors.New("SDP offer does not allow bidirectional audio")
	}

	address := mediaConnectionAddress(media, description)
	remoteIP, err := netip.ParseAddr(address)
	// RFC 2543 hold (c=0.0.0.0) in a re-offer: the peer cannot receive, so it
	// is at most sending. RFC 3264 §8.4 asks answerers to accept it as hold.
	if err == nil && allowHold && remoteIP.Is4() && remoteIP.IsUnspecified() {
		switch direction {
		case "sendrecv", "sendonly":
			direction = "sendonly"
		default:
			direction = "inactive"
		}
	} else if err != nil || !remoteIP.Is4() || remoteIP.IsMulticast() || remoteIP.IsUnspecified() {
		return sipMediaOffer{}, errors.New("SDP media address must be an explicit unicast IPv4 address")
	} else if !cfg.sourceAllowed(remoteIP.String()) {
		return sipMediaOffer{}, errors.New("SDP media address is outside TELEPHONY_SIP_ALLOWED_CIDRS")
	}
	payloadType, codec, err := negotiateSIPCodec(media, codecs)
	if err != nil {
		return sipMediaOffer{}, err
	}
	packetTimeMS, err := sipPacketTimeMS(media.Attributes)
	if err != nil {
		return sipMediaOffer{}, err
	}
	offer := sipMediaOffer{
		RemoteAddress:   remoteIP,
		RemotePort:      media.MediaName.Port.Value,
		PayloadType:     payloadType,
		Codec:           codec,
		Direction:       direction,
		PacketSamples:   packetTimeMS * 8,
		DTMFPayloadType: negotiatedSIPTelephoneEvent(media),
		Secure:          secure,
	}
	for _, attribute := range media.Attributes {
		if attribute.Key == "rtcp-mux-only" {
			return sipMediaOffer{}, errors.New("SIP RTCP mux-only is not supported; offer RTP+1")
		}
		if attribute.Key == "rtcp" {
			fields := strings.Fields(attribute.Value)
			if len(fields) != 1 && len(fields) != 4 {
				return sipMediaOffer{}, errors.New("invalid SDP RTCP address")
			}
			port, e := strconv.Atoi(fields[0])
			if e != nil || port < 1 || port > 65535 {
				return sipMediaOffer{}, errors.New("invalid SDP RTCP port")
			}
			offer.RemoteRTCPPort = port
			if len(fields) == 4 {
				ip, e := netip.ParseAddr(fields[3])
				if e != nil || fields[1] != "IN" || fields[2] != "IP4" || !ip.Is4() || ip.IsMulticast() || ip.IsUnspecified() || !cfg.sourceAllowed(ip.String()) {
					return sipMediaOffer{}, errors.New("SDP RTCP address is not allowed")
				}
				offer.RemoteRTCPAddress = ip
			}
		}
	}
	if secure {
		offer.CryptoTag, offer.RemoteKey, err = parseSDESCrypto(media.Attributes)
		if err != nil {
			return sipMediaOffer{}, err
		}
	}
	return offer, nil
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
	return negotiateSIPCodec(media, nil)
}
func negotiateSIPCodec(media *sdp.MediaDescription, codecs []string) (uint8, string, error) {
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
	if len(codecs) == 0 {
		codecs = []string{"G722", "PCMU", "PCMA"}
	}
	staticTypes := map[string]string{"G722": "9", "PCMU": "0", "PCMA": "8"}
	for _, name := range codecs {
		preferred := struct{ static, name string }{staticTypes[name], name}
		for _, format := range media.MediaName.Formats {
			codec := rtpmap[format]
			if (format == preferred.static && (codec == "" || codec == preferred.name+"/8000" || codec == preferred.name+"/8000/1")) || ((codec == preferred.name+"/8000" || codec == preferred.name+"/8000/1") && format != "0" && format != "8" && format != "9") {
				value, err := strconv.ParseUint(format, 10, 8)
				if err != nil || value > 127 {
					continue
				}
				return uint8(value), preferred.name, nil
			}
		}
	}
	return 0, "", errors.New("carrier must offer G722 or G.711 PCMU/PCMA (8 kHz RTP clock)")
}

func parseSDESCrypto(attributes []sdp.Attribute) (string, []byte, error) {
	for _, attribute := range attributes {
		if !strings.EqualFold(attribute.Key, "crypto") {
			continue
		}
		parts := strings.Fields(attribute.Value)
		if len(parts) != 3 || !strings.EqualFold(parts[1], sipSDESCryptoSuite) || !strings.HasPrefix(parts[2], "inline:") {
			continue
		}
		tag, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil || tag == 0 || tag > 999999999 || strconv.FormatUint(tag, 10) != parts[0] {
			continue
		}
		encoded := strings.TrimPrefix(parts[2], "inline:")
		// Lifetime, MKI and session parameters need explicit cryptographic
		// support. Do not silently negotiate them and then ignore them.
		if strings.ContainsAny(encoded, "|;") {
			continue
		}
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(key) != 30 {
			return "", nil, errors.New("SDES SRTP key must contain 30 bytes of key and salt material")
		}
		return parts[0], key, nil
	}
	return "", nil, errors.New("SRTP offer has no supported SDES AES_CM_128_HMAC_SHA1_80 crypto attribute")
}

func newSIPMediaSecurity(offer sipMediaOffer) (*sipMediaSecurity, error) {
	if !offer.Secure {
		return nil, nil
	}
	localKey := make([]byte, 30)
	if _, err := rand.Read(localKey); err != nil {
		return nil, fmt.Errorf("generate SRTP key: %w", err)
	}
	return newSIPMediaSecurityWithLocalKey(offer, localKey)
}

func newSIPMediaSecurityWithLocalKey(offer sipMediaOffer, localKey []byte) (*sipMediaSecurity, error) {
	if !offer.Secure {
		return nil, nil
	}
	if len(localKey) != 30 {
		return nil, errors.New("invalid local SRTP key length")
	}
	if len(offer.RemoteKey) != 30 {
		return nil, errors.New("invalid remote SRTP key length")
	}
	remoteContext, err := srtp.CreateContext(offer.RemoteKey[:16], offer.RemoteKey[16:], srtp.ProtectionProfileAes128CmHmacSha1_80, srtp.SRTPReplayProtection(128))
	if err != nil {
		return nil, fmt.Errorf("create inbound SRTP context: %w", err)
	}
	localContext, err := srtp.CreateContext(localKey[:16], localKey[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
	if err != nil {
		return nil, fmt.Errorf("create outbound SRTP context: %w", err)
	}
	remoteRTCP, err := srtp.CreateContext(offer.RemoteKey[:16], offer.RemoteKey[16:], srtp.ProtectionProfileAes128CmHmacSha1_80, srtp.SRTCPReplayProtection(128))
	if err != nil {
		return nil, err
	}
	localRTCP, err := srtp.CreateContext(localKey[:16], localKey[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
	if err != nil {
		return nil, err
	}
	return &sipMediaSecurity{RemoteContext: remoteContext, LocalContext: localContext, LocalKey: localKey, RemoteRTCP: remoteRTCP, LocalRTCP: localRTCP}, nil
}

func buildSIPMediaAnswer(cfg sipGatewayConfig, offer sipMediaOffer, localPort int, security *sipMediaSecurity) ([]byte, error) {
	protocol := []string{"RTP", "AVP"}
	attributes := []sdp.Attribute{
		sdp.NewAttribute("rtpmap", fmt.Sprintf("%d %s/8000", offer.PayloadType, offer.Codec)),
		sdp.NewPropertyAttribute(sipAnswerDirection(offer.Direction)),
		sdp.NewAttribute("ptime", "20"),
		sdp.NewAttribute("rtcp", fmt.Sprintf("%d IN IP4 %s", localPort+1, cfg.PublicIP.String())),
	}
	if offer.DTMFPayloadType > 0 {
		attributes = append(attributes, sdp.NewAttribute("rtpmap", fmt.Sprintf("%d telephone-event/8000", offer.DTMFPayloadType)), sdp.NewAttribute("fmtp", fmt.Sprintf("%d 0-16", offer.DTMFPayloadType)))
	}
	if offer.Secure {
		if security == nil || len(security.LocalKey) != 30 {
			return nil, errors.New("SRTP answer has no local key")
		}
		protocol = []string{"RTP", "SAVP"}
		attributes = append(attributes, sdp.NewAttribute("crypto",
			offer.CryptoTag+" "+sipSDESCryptoSuite+" inline:"+base64.StdEncoding.EncodeToString(security.LocalKey)))
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
	}
	accepted := &sdp.MediaDescription{
		MediaName: sdp.MediaName{
			Media: "audio", Port: sdp.RangedPort{Value: localPort},
			Protos: protocol, Formats: sipAnswerFormats(offer),
		},
		Attributes: attributes,
	}
	if offer.MediaIndex < 0 || offer.MediaIndex >= len(offer.MediaLines) {
		description.MediaDescriptions = []*sdp.MediaDescription{accepted}
		return description.Marshal()
	}
	// RFC 3264 §6: one answer m-line per offered m-line, in order; streams
	// not accepted keep their media and transport with port zero.
	for index, line := range offer.MediaLines {
		if index == offer.MediaIndex {
			description.MediaDescriptions = append(description.MediaDescriptions, accepted)
			continue
		}
		formats := line.Formats
		if len(formats) > 1 {
			formats = formats[:1]
		}
		if len(formats) == 0 {
			formats = []string{"0"}
		}
		description.MediaDescriptions = append(description.MediaDescriptions, &sdp.MediaDescription{
			MediaName: sdp.MediaName{Media: line.Media, Port: sdp.RangedPort{Value: 0}, Protos: line.Protos, Formats: formats},
		})
	}
	return description.Marshal()
}

// sipSDPWithDirection replaces the media direction attribute of a local SDP.
func sipSDPWithDirection(body []byte, direction string) []byte {
	lines := strings.Split(string(body), "\r\n")
	for i, line := range lines {
		switch line {
		case "a=sendrecv", "a=sendonly", "a=recvonly", "a=inactive":
			lines[i] = "a=" + direction
			return []byte(strings.Join(lines, "\r\n"))
		}
	}
	return body
}

// sipSDPWithCryptoTag echoes the offer's SDES tag in a local answer (RFC 4568 §5.1.2).
func sipSDPWithCryptoTag(body []byte, tag string) []byte {
	lines := strings.Split(string(body), "\r\n")
	for i, line := range lines {
		if value, ok := strings.CutPrefix(line, "a=crypto:"); ok {
			if _, rest, found := strings.Cut(value, " "); found && tag != "" {
				lines[i] = "a=crypto:" + tag + " " + rest
				return []byte(strings.Join(lines, "\r\n"))
			}
		}
	}
	return body
}

// nextSIPLocalSDP keeps one SDP session across a dialog (RFC 3264 §8): the
// candidate reuses the previous o= session-id, and the version increments only
// when anything other than the o= line changed.
func nextSIPLocalSDP(previous, candidate []byte) []byte {
	previousOrigin, previousRest := splitSIPSDPOrigin(previous)
	candidateOrigin, candidateRest := splitSIPSDPOrigin(candidate)
	if len(previousOrigin) != 6 || len(candidateOrigin) != 6 {
		return candidate
	}
	version, err := strconv.ParseUint(previousOrigin[2], 10, 64)
	if err != nil {
		return candidate
	}
	if candidateRest != previousRest {
		version++
	}
	candidateOrigin[1], candidateOrigin[2] = previousOrigin[1], strconv.FormatUint(version, 10)
	return []byte(strings.Replace(string(candidate), sipSDPOriginLine(candidate), "o="+strings.Join(candidateOrigin, " "), 1))
}

func sipSDPOriginLine(body []byte) string {
	for _, line := range strings.Split(string(body), "\r\n") {
		if strings.HasPrefix(line, "o=") {
			return line
		}
	}
	return ""
}

func splitSIPSDPOrigin(body []byte) ([]string, string) {
	origin := sipSDPOriginLine(body)
	if origin == "" {
		return nil, string(body)
	}
	return strings.Fields(strings.TrimPrefix(origin, "o=")), strings.Replace(string(body), origin, "", 1)
}

// RFC 4733 telephone events are negotiated independently of the speech codec.
func negotiatedSIPTelephoneEvent(media *sdp.MediaDescription) int {
	for _, attr := range media.Attributes {
		fields := strings.Fields(attr.Value)
		if attr.Key != "rtpmap" || len(fields) != 2 || !strings.EqualFold(fields[1], "telephone-event/8000") {
			continue
		}
		pt, err := strconv.Atoi(fields[0])
		if err != nil || pt < 96 || pt > 127 {
			continue
		}
		for _, f := range media.MediaName.Formats {
			if f == fields[0] {
				return pt
			}
		}
	}
	return -1
}
func sipAnswerFormats(offer sipMediaOffer) []string {
	formats := []string{strconv.Itoa(int(offer.PayloadType))}
	if offer.DTMFPayloadType > 0 {
		formats = append(formats, strconv.Itoa(offer.DTMFPayloadType))
	}
	return formats
}

func sipAnswerDirection(direction string) string {
	switch direction {
	case "sendonly":
		return "recvonly"
	case "recvonly":
		return "sendonly"
	case "inactive":
		return "inactive"
	default:
		return "sendrecv"
	}
}
func sipRemoteMediaMode(direction string) int32 {
	switch direction {
	case "sendonly":
		return 1
	case "recvonly":
		return 2
	case "inactive":
		return 3
	default:
		return 0
	}
}
