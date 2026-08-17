package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

func directSIPTestConfig() sipGatewayConfig {
	return sipGatewayConfig{
		Enabled: true, Transport: "tls", ListenAddress: "0.0.0.0:5061",
		PublicHost: "sip.example.test", PublicIP: netip.MustParseAddr("198.51.100.10"),
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")},
		RTPBindIP:    netip.MustParseAddr("0.0.0.0"), RTPPortMin: 20000, RTPPortMax: 20010,
		SRTPMode: sipSRTPPreferred, MaxSessions: 10,
	}
}

func writeSIPTestCertificate(t *testing.T, directory, host string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateDER})
	if err := os.WriteFile(filepath.Join(directory, "fullchain.pem"), certificatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "privkey.pem"), privatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSIPGatewayConfigIsDisabledByDefault(t *testing.T) {
	t.Setenv("TELEPHONY_SIP_ENABLED", "false")
	cfg, err := loadSIPGatewayConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled {
		t.Fatal("direct SIP unexpectedly enabled")
	}
}

func TestSIPGatewayConfigRejectsUnsafeExposure(t *testing.T) {
	t.Setenv("TELEPHONY_SIP_ENABLED", "true")
	t.Setenv("TELEPHONY_SIP_PUBLIC_IP", "198.51.100.10")
	t.Setenv("TELEPHONY_SIP_PUBLIC_HOST", "sip.example.test")
	t.Setenv("TELEPHONY_SIP_TRANSPORT", "udp")
	t.Setenv("TELEPHONY_SIP_ALLOW_INSECURE_SIGNALING", "false")
	t.Setenv("TELEPHONY_SIP_ALLOWED_CIDRS", "203.0.113.0/24")
	if _, err := loadSIPGatewayConfig(nil); err == nil || !strings.Contains(err.Error(), "sip_allow_insecure_signaling") {
		t.Fatalf("unsafe UDP signaling error=%v", err)
	}

	t.Setenv("TELEPHONY_SIP_TRANSPORT", "tls")
	t.Setenv("TELEPHONY_SIP_TLS_CERT_FILE", "/tmp/cert.pem")
	t.Setenv("TELEPHONY_SIP_TLS_KEY_FILE", "/tmp/key.pem")
	t.Setenv("TELEPHONY_SIP_ALLOWED_CIDRS", "0.0.0.0/0")
	if _, err := loadSIPGatewayConfig(nil); err == nil || !strings.Contains(err.Error(), "entire internet") {
		t.Fatalf("open carrier source policy error=%v", err)
	}
}

func TestSIPGatewayConfigDerivesManagedDefaults(t *testing.T) {
	root := t.TempDir()
	host := "agents.example.test"
	certificateDir := filepath.Join(root, host)
	if err := os.MkdirAll(certificateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSIPTestCertificate(t, certificateDir, host)
	cfg, err := loadSIPGatewayConfigWithOptions(nil, sipConfigOptions{
		ForceEnabled:     true,
		PublicURL:        "https://" + host,
		CertificateRoots: []string{root},
		LookupNetIP: func(context.Context, string, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("198.51.100.10")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.PublicHost != host || cfg.PublicIP.String() != "198.51.100.10" ||
		cfg.TLSCertFile != filepath.Join(certificateDir, "fullchain.pem") ||
		cfg.RTPPortMin != defaultSIPRTPPortMin || cfg.RTPPortMax != defaultSIPRTPPortMax ||
		cfg.MaxSessions != defaultSIPMaxSessions ||
		!cfg.sourceAllowed("54.171.127.193:5061") || !cfg.sourceAllowed("185.246.41.140:20000") {
		t.Fatalf("managed SIP defaults were not resolved: %#v", cfg)
	}
	if _, err := cfg.tlsConfig(); err != nil {
		t.Fatalf("managed SIP certificate was not usable: %v", err)
	}
	cfg.PublicHost = "wrong.example.test"
	if _, err := cfg.tlsConfig(); err == nil || !strings.Contains(err.Error(), "does not cover") {
		t.Fatalf("certificate hostname mismatch error=%v", err)
	}
}

func TestSIPGatewayConfigDiscoversNativeAptevaCertificateCache(t *testing.T) {
	cache := t.TempDir()
	host := "agents.example.test"
	cacheFile := filepath.Join(cache, host)
	if err := os.WriteFile(cacheFile, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APTEVA_ACME_CACHE_DIR", cache)
	cfg, err := loadSIPGatewayConfigWithOptions(nil, sipConfigOptions{
		ForceEnabled: true,
		PublicURL:    "https://" + host,
		LookupNetIP: func(context.Context, string, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("198.51.100.10")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSCertFile != cacheFile || cfg.TLSKeyFile != cacheFile {
		t.Fatalf("native certificate cache was not selected: %#v", cfg)
	}
}

func TestSIPGatewayConfigUsesAppSettingsBeforeEnvironment(t *testing.T) {
	t.Setenv("TELEPHONY_SIP_ENABLED", "false")
	config := sdk.Config{
		"sip_enabled":                  "true",
		"sip_transport":                "udp",
		"sip_allow_insecure_signaling": "true",
		"sip_listen":                   "0.0.0.0:5070",
		"sip_public_host":              "sip.example.test",
		"sip_public_ip":                "198.51.100.10",
		"sip_allowed_cidrs":            "203.0.113.0/24",
		"sip_rtp_bind_ip":              "0.0.0.0",
		"sip_rtp_port_min":             "20000",
		"sip_rtp_port_max":             "20100",
		"sip_srtp":                     "required",
		"sip_max_sessions":             "25",
	}
	cfg, err := loadSIPGatewayConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.Transport != "udp" || cfg.endpointURI() != "sip:sip.example.test:5070;transport=udp" ||
		cfg.RTPPortMin != 20000 || cfg.RTPPortMax != 20100 || cfg.MaxSessions != 25 {
		t.Fatalf("app config was not applied: %#v", cfg)
	}
}

func TestSIPGatewayConfigRejectsOpenCIDRAndInvalidHostname(t *testing.T) {
	if _, err := parseSIPAllowedCIDRs("0.0.0.0/0"); err == nil || !strings.Contains(err.Error(), "entire internet") {
		t.Fatalf("open CIDR error=%v", err)
	}
	config := sdk.Config{
		"sip_enabled": "true", "sip_transport": "udp", "sip_allow_insecure_signaling": "true",
		"sip_listen": "0.0.0.0:5060", "sip_public_host": "https://sip.example.test",
		"sip_public_ip": "198.51.100.10", "sip_allowed_cidrs": "203.0.113.0/24",
	}
	if _, err := loadSIPGatewayConfig(config); err == nil || !strings.Contains(err.Error(), "certificate hostname") {
		t.Fatalf("invalid hostname error=%v", err)
	}
}

func TestSelectingDirectSIPStartsGatewayLazily(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	t.Setenv("TELEPHONY_SIP_TRANSPORT", "udp")
	t.Setenv("TELEPHONY_SIP_ALLOW_INSECURE_SIGNALING", "true")
	t.Setenv("TELEPHONY_SIP_LISTEN", fmt.Sprintf("127.0.0.1:%d", port))
	t.Setenv("TELEPHONY_SIP_PUBLIC_HOST", "sip.example.test")
	t.Setenv("TELEPHONY_SIP_PUBLIC_IP", "198.51.100.10")
	t.Setenv("TELEPHONY_SIP_ALLOWED_CIDRS", "203.0.113.0/24")

	route := routeRow{
		ID: "route-lazy-sip", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 10,
		PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, Secret: "secret",
	}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	if app.directSIPGateway() != nil {
		t.Fatal("direct SIP gateway started before the transport was selected")
	}
	stored, err := app.setRouteTransport(ctx, route.ID, inboundTransportSIPDirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.stopSIPGateway)
	if app.directSIPGateway() == nil || stored.InboundTransport != inboundTransportSIPDirect {
		t.Fatalf("direct SIP was not started lazily: route=%+v gateway=%v", stored, app.directSIPGateway())
	}
}

func TestParseSIPMediaOfferNegotiatesG711AndEnforcesCarrierNetwork(t *testing.T) {
	cfg := directSIPTestConfig()
	offerBody := []byte("v=0\r\n" +
		"o=- 1 1 IN IP4 203.0.113.20\r\n" +
		"s=carrier\r\n" +
		"c=IN IP4 203.0.113.20\r\n" +
		"t=0 0\r\n" +
		"m=audio 4000 RTP/AVP 8 0\r\n" +
		"a=rtpmap:8 PCMA/8000\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=sendrecv\r\n")
	offer, err := parseSIPMediaOffer(offerBody, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if offer.Codec != "PCMU" || offer.PayloadType != 0 || offer.RemotePort != 4000 || offer.PacketSamples != 160 {
		t.Fatalf("unexpected negotiated offer: %#v", offer)
	}

	blocked := strings.ReplaceAll(string(offerBody), "203.0.113.20", "192.0.2.20")
	if _, err := parseSIPMediaOffer([]byte(blocked), cfg); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("blocked media address error=%v", err)
	}

	unsupported := strings.Replace(string(offerBody), "m=audio 4000 RTP/AVP 8 0", "m=audio 4000 RTP/AVP 111", 1)
	if _, err := parseSIPMediaOffer([]byte(unsupported), cfg); err == nil || !strings.Contains(err.Error(), "G.711") {
		t.Fatalf("unsupported codec error=%v", err)
	}
	invalidPacketTime := append(append([]byte(nil), offerBody...), []byte("a=ptime:25\r\n")...)
	if _, err := parseSIPMediaOffer(invalidPacketTime, cfg); err == nil || !strings.Contains(err.Error(), "ptime") {
		t.Fatalf("invalid packet-time error=%v", err)
	}
}

func TestSIPSDESSRTPRoundTripAndAnswer(t *testing.T) {
	cfg := directSIPTestConfig()
	cfg.SRTPMode = sipSRTPRequired
	remoteKey := make([]byte, 30)
	for i := range remoteKey {
		remoteKey[i] = byte(i + 1)
	}
	body := []byte("v=0\r\n" +
		"o=- 1 1 IN IP4 203.0.113.20\r\n" +
		"s=carrier\r\n" +
		"c=IN IP4 203.0.113.20\r\n" +
		"t=0 0\r\n" +
		"m=audio 4000 RTP/SAVP 0\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=crypto:1 " + sipSDESCryptoSuite + " inline:" + base64.StdEncoding.EncodeToString(remoteKey) + "\r\n")
	offer, err := parseSIPMediaOffer(body, cfg)
	if err != nil {
		t.Fatal(err)
	}
	security, err := newSIPMediaSecurity(offer)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := buildSIPMediaAnswer(cfg, offer, 20000, security)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(answer), "RTP/SAVP") || !strings.Contains(string(answer), sipSDESCryptoSuite) {
		t.Fatalf("SRTP answer missing crypto policy:\n%s", answer)
	}

	carrierWriter, err := srtp.CreateContext(remoteKey[:16], remoteKey[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
	if err != nil {
		t.Fatal(err)
	}
	packet := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 1, Timestamp: 160, SSRC: 99},
		Payload: make([]byte, sipRTPPacketSamples),
	}
	raw, _ := packet.Marshal()
	encrypted, err := carrierWriter.EncryptRTP(nil, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	media := &sipRTPMedia{offer: offer, security: security}
	decoded, err := media.decodePacket(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SequenceNumber != packet.SequenceNumber || len(decoded.Payload) != sipRTPPacketSamples {
		t.Fatalf("unexpected decrypted RTP packet: %#v", decoded)
	}
}

func TestRTPJitterBufferReordersAndConcealsLoss(t *testing.T) {
	buffer := newRTPJitterBuffer()
	buffer.push(12, append([]byte{12}, make([]byte, 159)...))
	buffer.push(10, append([]byte{10}, make([]byte, 159)...))
	buffer.push(11, append([]byte{11}, make([]byte, 159)...))
	for _, want := range []byte{10, 11, 12} {
		got, ready := buffer.pop()
		if !ready || len(got) != sipRTPPacketSamples || got[0] != want {
			t.Fatalf("pop=%v ready=%v want=%d", got, ready, want)
		}
	}

	loss := newRTPJitterBuffer()
	loss.push(20, append([]byte{20}, make([]byte, 159)...))
	loss.push(22, append([]byte{22}, make([]byte, 159)...))
	loss.push(23, append([]byte{23}, make([]byte, 159)...))
	if got, ready := loss.pop(); !ready || got[0] != 20 {
		t.Fatalf("initial packet=%v ready=%v", got, ready)
	}
	if got, ready := loss.pop(); !ready || got != nil {
		t.Fatalf("lost packet was not concealed: %v ready=%v", got, ready)
	}
	if got, ready := loss.pop(); !ready || got[0] != 22 {
		t.Fatalf("post-loss packet=%v ready=%v", got, ready)
	}
}

func TestRTPAudioFramerNormalizesCarrierPacketTime(t *testing.T) {
	jitter := newRTPJitterBuffer()
	framer := newRTPAudioFramer(jitter, sipMediaOffer{Codec: "PCMU", PacketSamples: 320})
	jitter.push(1, append(make([]byte, 160), make([]byte, 160)...))
	jitter.push(2, make([]byte, 320))
	first, ready := framer.pop()
	if !ready || len(first) != sipRTPPacketSamples {
		t.Fatalf("first normalized frame len=%d ready=%v", len(first), ready)
	}
	second, ready := framer.pop()
	if !ready || len(second) != sipRTPPacketSamples {
		t.Fatalf("second normalized frame len=%d ready=%v", len(second), ready)
	}
}

func TestSIPRTPPacerRejectsOverflowWithoutPartialEnqueue(t *testing.T) {
	pacer := &sipRTPPacer{
		playback: &sipPlaybackState{},
		queue:    make(chan sipRTPOutboundPacket, 2),
	}
	pacer.queue <- sipRTPOutboundPacket{itemID: "existing"}
	pacer.playback.pending.Store(1)
	if _, err := pacer.enqueue([]sipRTPOutboundPacket{{itemID: "new-1"}, {itemID: "new-2"}}); err == nil {
		t.Fatal("oversized enqueue unexpectedly succeeded")
	}
	if len(pacer.queue) != 1 || pacer.playback.pending.Load() != 1 {
		t.Fatalf("overflow partially mutated queue: len=%d pending=%d", len(pacer.queue), pacer.playback.pending.Load())
	}
}

func TestSIPRTPPacerSendsTwentyMillisecondPacketAndProgress(t *testing.T) {
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress := make(chan twilioPlaybackProgress, 1)
	media := &sipRTPMedia{
		conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr),
		offer: sipMediaOffer{PayloadType: 0, Codec: "PCMU"},
	}
	playback := &sipPlaybackState{}
	pacer := newSIPRTPPacer(ctx, media, playback, func(value twilioPlaybackProgress) error {
		progress <- value
		return nil
	})
	if _, err := pacer.enqueue([]sipRTPOutboundPacket{{
		payload: make([]byte, sipRTPPacketSamples), itemID: "item-1", audioEndMS: 20,
	}}); err != nil {
		t.Fatal(err)
	}
	_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
	raw := make([]byte, 1500)
	n, _, err := receiver.ReadFromUDP(raw)
	if err != nil {
		t.Fatal(err)
	}
	var packet rtp.Packet
	if err := packet.Unmarshal(raw[:n]); err != nil {
		t.Fatal(err)
	}
	if len(packet.Payload) != sipRTPPacketSamples || packet.Timestamp == 0 {
		t.Fatalf("unexpected paced RTP packet: %#v", packet)
	}
	select {
	case got := <-progress:
		if got.ItemID != "item-1" || got.AudioEndMS != 20 {
			t.Fatalf("unexpected progress: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("playback progress was not emitted")
	}
}

func TestDirectSIPRouteLookupRejectsAmbiguousNumbers(t *testing.T) {
	db := testCallsDB(t)
	route := routeRow{
		ID: "sip-route-1", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 10,
		PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, Secret: "secret",
		InboundTransport: inboundTransportSIPDirect,
	}
	if err := db.insertRoute(route); err != nil {
		t.Fatal(err)
	}
	got, err := db.findDirectSIPRouteByNumber(route.PhoneNumber)
	if err != nil || got == nil || got.ID != route.ID {
		t.Fatalf("route=%+v err=%v", got, err)
	}
	route.ID = "sip-route-2"
	route.AgentID = 8
	route.CarrierConnectionID = 11
	if err := db.insertRoute(route); err != nil {
		t.Fatal(err)
	}
	if _, err := db.findDirectSIPRouteByNumber(route.PhoneNumber); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("ambiguous route error=%v", err)
	}
}

func TestSIPNumberExtractionPreservesForwardedDestination(t *testing.T) {
	raw := "INVITE sip:+12025550100@sip.example.test SIP/2.0\r\n" +
		"Via: SIP/2.0/TLS 203.0.113.20:5061;branch=z9hG4bK-test\r\n" +
		"From: <sip:+34648257793@carrier.test>;tag=caller\r\n" +
		"To: <sip:+12025550100@sip.example.test>\r\n" +
		"Call-ID: call-1@carrier.test\r\n" +
		"CSeq: 1 INVITE\r\n" +
		"Contact: <sip:carrier@203.0.113.20>\r\n" +
		"Diversion: <sip:+33123456789@forwarder.test>\r\n" +
		"Content-Length: 0\r\n\r\n"
	message, err := sip.ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	request := message.(*sip.Request)
	candidates := sipCalledNumberCandidates(request)
	if len(candidates) != 2 || candidates[0] != "+12025550100" || candidates[1] != "+33123456789" {
		t.Fatalf("called-number candidates=%#v", candidates)
	}
	if caller := sipCallerNumber(request); caller != "+34648257793" {
		t.Fatalf("caller=%q", caller)
	}
}

func TestSIPGatewayRingsAndCancelsInboundDialog(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	portProbe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	signalingPort := portProbe.LocalAddr().(*net.UDPAddr).Port
	_ = portProbe.Close()

	cfg := directSIPTestConfig()
	cfg.Transport = "udp"
	cfg.ListenAddress = fmt.Sprintf("127.0.0.1:%d", signalingPort)
	cfg.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	gateway, err := newSIPGateway(app, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gateway.Stop)
	app.sip.gateway = gateway

	route := routeRow{
		ID: "route-live-sip", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 10,
		PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, Secret: "secret",
		AnswerMode: answerModeAgent, TimeoutSec: 60, InboundTransport: inboundTransportSIPDirect,
		TransportConfig: `{"provider":"twilio","trunk_id":"TK1"}`,
	}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}

	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: signalingPort})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	localPort := conn.LocalAddr().(*net.UDPAddr).Port
	sdpBody := "v=0\r\n" +
		"o=- 1 1 IN IP4 127.0.0.1\r\n" +
		"s=test\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"t=0 0\r\n" +
		"m=audio 24000 RTP/AVP 0\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n"
	invite := fmt.Sprintf("INVITE sip:%s@127.0.0.1:%d SIP/2.0\r\n", route.PhoneNumber, signalingPort) +
		fmt.Sprintf("Via: SIP/2.0/UDP 127.0.0.1:%d;branch=z9hG4bK-cancel-test;rport\r\n", localPort) +
		"Max-Forwards: 70\r\n" +
		"From: <sip:+34648257793@127.0.0.1>;tag=caller\r\n" +
		fmt.Sprintf("To: <sip:%s@127.0.0.1>\r\n", route.PhoneNumber) +
		"Call-ID: cancel-test@127.0.0.1\r\n" +
		"CSeq: 1 INVITE\r\n" +
		fmt.Sprintf("Contact: <sip:caller@127.0.0.1:%d>\r\n", localPort) +
		"Content-Type: application/sdp\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(sdpBody), sdpBody)
	if _, err := conn.Write([]byte(invite)); err != nil {
		t.Fatal(err)
	}
	readSIPResponseContaining(t, conn, "180 Ringing")

	cancel := fmt.Sprintf("CANCEL sip:%s@127.0.0.1:%d SIP/2.0\r\n", route.PhoneNumber, signalingPort) +
		fmt.Sprintf("Via: SIP/2.0/UDP 127.0.0.1:%d;branch=z9hG4bK-cancel-test;rport\r\n", localPort) +
		"Max-Forwards: 70\r\n" +
		"From: <sip:+34648257793@127.0.0.1>;tag=caller\r\n" +
		fmt.Sprintf("To: <sip:%s@127.0.0.1>\r\n", route.PhoneNumber) +
		"Call-ID: cancel-test@127.0.0.1\r\n" +
		"CSeq: 1 CANCEL\r\n" +
		"Content-Length: 0\r\n\r\n"
	if _, err := conn.Write([]byte(cancel)); err != nil {
		t.Fatal(err)
	}
	readSIPResponseContaining(t, conn, "200 OK")

	deadline := time.Now().Add(2 * time.Second)
	for {
		call, findErr := app.db().findInboundCallByProviderID("cancel-test@127.0.0.1")
		if findErr == nil && call != nil && call.Status == "canceled" && gateway.sessionCount() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("canceled SIP dialog not cleaned up: call=%+v err=%v sessions=%d", call, findErr, gateway.sessionCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readSIPResponseContaining(t *testing.T, conn *net.UDPConn, expected string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	buffer := make([]byte, 4096)
	var observed []string
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, _, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				continue
			}
			t.Fatal(err)
		}
		response := string(buffer[:n])
		observed = append(observed, strings.SplitN(response, "\r\n", 2)[0])
		if strings.Contains(response, expected) {
			return
		}
	}
	t.Fatalf("SIP response %q not observed; got %v", expected, observed)
}

func TestConnectedNumberDirectSIPHealth(t *testing.T) {
	app := &App{}
	app.sip.gateway = &sipGateway{cfg: directSIPTestConfig()}
	route := routeRow{
		ID: "route-direct", AgentID: 7, Enabled: true, AnswerMode: answerModeRealtimeImmediate,
		InboundTransport: inboundTransportSIPDirect, TransportConfig: `{"provider":"twilio","trunk_id":"TK1"}`,
	}
	view := app.connectedNumberView(nil, "twilio", ownedNumber{
		PhoneNumber: "+12025550100", ConnectionID: "TK1",
	}, &route, func(int64) string { return "Reception" })
	if view.Route == nil || !view.Route.TransportConfigured || view.RoutingHealth != "healthy" ||
		view.VoiceWebhookStatus != routingDirectSIP || view.StatusCallbackState != webhookNotApplicable {
		t.Fatalf("unexpected direct SIP health: %#v", view)
	}
}

func TestNumbersTransportEndpointIsProjectScoped(t *testing.T) {
	app, _ := withTelephonyTestContext(t, &answerPlatform{})
	app.sip.gateway = &sipGateway{cfg: directSIPTestConfig()}
	route := routeRow{
		ID: "route-transport", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 10,
		PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, Secret: "secret",
	}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	body := `{"route_id":"route-transport","inbound_transport":"sip_direct","configure":false}`
	request := httptest.NewRequest(http.MethodPost, "/numbers/transport?project_id=project-a", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	app.handleNumbers(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	stored, err := app.db().findRoute(route.ID)
	if err != nil || stored == nil || stored.InboundTransport != inboundTransportSIPDirect {
		t.Fatalf("stored route=%+v err=%v", stored, err)
	}
}

func TestTwilioDirectSIPCarrierConfigurationAndRestore(t *testing.T) {
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
		"list_phone_numbers":               json.RawMessage(`{"incoming_phone_numbers":[{"sid":"PN1","phone_number":"+12025550100","voice_url":"https://old.test/voice","status_callback":"https://old.test/status"}]}`),
		"create_elastic_sip_trunk":         json.RawMessage(`{"sid":"TK1"}`),
		"create_sip_trunk_origination_url": json.RawMessage(`{"sid":"OU1"}`),
		"associate_sip_trunk_phone_number": json.RawMessage(`{"sid":"PN1"}`),
		"delete_elastic_sip_trunk":         json.RawMessage(`{}`),
	}}
	app, ctx := withTelephonyTestContext(t, platform)
	route := routeRow{
		ID: "route-twilio-sip", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 10,
		PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, Secret: "secret",
		InboundTransport: inboundTransportSIPDirect,
	}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	stored, _ := app.db().findRoute(route.ID)
	if err := app.configureTwilioDirectSIP(ctx, stored, directSIPTestConfig()); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 4 ||
		platform.integrationCalls[1].Input["Secure"] != true ||
		platform.integrationCalls[2].Input["SipUrl"] != "sip:sip.example.test;transport=tls" {
		t.Fatalf("unexpected Twilio SIP calls: %#v", platform.integrationCalls)
	}
	stored, _ = app.db().findRoute(route.ID)
	var state directSIPProviderConfig
	if json.Unmarshal([]byte(stored.TransportConfig), &state) != nil || state.TrunkID != "TK1" || state.OriginationURLID != "OU1" {
		t.Fatalf("unexpected saved Twilio SIP state: %+v", state)
	}
	platform.integrationCalls = nil
	if err := app.deconfigureDirectSIPCarrierRoute(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "delete_elastic_sip_trunk" {
		t.Fatalf("Twilio SIP route was not restored: %#v", platform.integrationCalls)
	}
}

func TestTelnyxDirectSIPCarrierConfigurationAndRestore(t *testing.T) {
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
		"list_phone_numbers":     json.RawMessage(`{"data":[{"id":"number-1","phone_number":"+12025550100","connection_id":"old-connection"}]}`),
		"create_fqdn_connection": json.RawMessage(`{"data":{"id":"connection-1"}}`),
		"create_fqdn":            json.RawMessage(`{"data":{"id":"fqdn-1"}}`),
	}}
	app, ctx := withTelephonyTestContext(t, platform)
	cfg := directSIPTestConfig()
	cfg.SRTPMode = sipSRTPPreferred
	route := routeRow{
		ID: "route-telnyx-sip", ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 10,
		PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, Secret: "secret",
		InboundTransport: inboundTransportSIPDirect,
	}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	stored, _ := app.db().findRoute(route.ID)
	if err := app.configureTelnyxDirectSIP(ctx, stored, cfg); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 4 ||
		platform.integrationCalls[1].Tool != "create_fqdn_connection" ||
		platform.integrationCalls[2].Tool != "create_fqdn" ||
		platform.integrationCalls[3].Input["connection_id"] != "connection-1" {
		t.Fatalf("unexpected Telnyx SIP calls: %#v", platform.integrationCalls)
	}
	stored, _ = app.db().findRoute(route.ID)
	platform.integrationCalls = nil
	if err := app.deconfigureDirectSIPCarrierRoute(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 3 ||
		platform.integrationCalls[0].Input["connection_id"] != "old-connection" ||
		platform.integrationCalls[1].Tool != "delete_fqdn" ||
		platform.integrationCalls[2].Tool != "delete_fqdn_connection" {
		t.Fatalf("Telnyx SIP route was not restored: %#v", platform.integrationCalls)
	}
}

func TestDirectSIPCarrierSetupRollsBackPartialResources(t *testing.T) {
	t.Run("twilio association failure", func(t *testing.T) {
		platform := &answerPlatform{
			failTool: "associate_sip_trunk_phone_number",
			integrationResponse: map[string]json.RawMessage{
				"list_phone_numbers":               json.RawMessage(`{"incoming_phone_numbers":[{"sid":"PN1","phone_number":"+12025550100"}]}`),
				"create_elastic_sip_trunk":         json.RawMessage(`{"sid":"TK1"}`),
				"create_sip_trunk_origination_url": json.RawMessage(`{"sid":"OU1"}`),
			},
		}
		app, ctx := withTelephonyTestContext(t, platform)
		route := routeRow{
			ID: "route-twilio-rollback", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 10,
			PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, Secret: "secret",
			InboundTransport: inboundTransportSIPDirect,
		}
		if err := app.db().insertRoute(route); err != nil {
			t.Fatal(err)
		}
		stored, _ := app.db().findRoute(route.ID)
		if err := app.configureTwilioDirectSIP(ctx, stored, directSIPTestConfig()); err == nil {
			t.Fatal("Twilio partial setup unexpectedly succeeded")
		}
		last := platform.integrationCalls[len(platform.integrationCalls)-1]
		if last.Tool != "delete_elastic_sip_trunk" || last.Input["TrunkSid"] != "TK1" {
			t.Fatalf("Twilio partial resources were not rolled back: %#v", platform.integrationCalls)
		}
	})

	t.Run("telnyx assignment failure", func(t *testing.T) {
		platform := &answerPlatform{
			failTool: "update_phone_number",
			integrationResponse: map[string]json.RawMessage{
				"list_phone_numbers":     json.RawMessage(`{"data":[{"id":"number-1","phone_number":"+12025550100","connection_id":"old-connection"}]}`),
				"create_fqdn_connection": json.RawMessage(`{"data":{"id":"connection-1"}}`),
				"create_fqdn":            json.RawMessage(`{"data":{"id":"fqdn-1"}}`),
			},
		}
		app, ctx := withTelephonyTestContext(t, platform)
		route := routeRow{
			ID: "route-telnyx-rollback", ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 10,
			PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, Secret: "secret",
			InboundTransport: inboundTransportSIPDirect,
		}
		if err := app.db().insertRoute(route); err != nil {
			t.Fatal(err)
		}
		stored, _ := app.db().findRoute(route.ID)
		if err := app.configureTelnyxDirectSIP(ctx, stored, directSIPTestConfig()); err == nil {
			t.Fatal("Telnyx partial setup unexpectedly succeeded")
		}
		calls := platform.integrationCalls
		if len(calls) < 2 || calls[len(calls)-2].Tool != "delete_fqdn" || calls[len(calls)-1].Tool != "delete_fqdn_connection" {
			t.Fatalf("Telnyx partial resources were not rolled back: %#v", calls)
		}
	})
}

func TestDirectSIPRecordingPolicyRejectsProviderCloudRecording(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	app.sip.gateway = &sipGateway{cfg: directSIPTestConfig()}
	route := routeRow{
		ID: "route-recording", ProjectID: "project-a", CarrierSlug: "twilio",
		PhoneNumber: "+12025550100", AgentID: 7, Enabled: true,
		RecordingMode: recordingModeAlways,
	}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	if _, err := app.setRouteTransport(ctx, route.ID, inboundTransportSIPDirect, 0); err == nil || !strings.Contains(err.Error(), "recording") {
		t.Fatalf("direct SIP recording-policy error=%v", err)
	}
}

var _ sdk.PlatformClient = (*answerPlatform)(nil)
