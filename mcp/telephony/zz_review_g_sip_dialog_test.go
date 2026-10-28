package main

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Local hangup while ringing must always produce the 487 final response.
func TestReviewGInboundLocalHangupWhileRingingSends487(t *testing.T) {
	for i := 0; i < 5; i++ {
		t.Run(fmt.Sprint(i), reviewGHangupWhileRinging)
	}
}

func reviewGHangupWhileRinging(t *testing.T) {
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
	route := routeRow{ID: "route-ringing-hangup", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 10,
		PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, Secret: "secret",
		AnswerMode: answerModeAgent, TimeoutSec: 60, InboundTransport: inboundTransportSIPDirect,
		TransportConfig: `{"provider":"twilio","trunk_id":"TK1"}`}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: signalingPort})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	localPort := conn.LocalAddr().(*net.UDPAddr).Port
	sdpBody := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=test\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 24000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"
	invite := fmt.Sprintf("INVITE sip:%s@127.0.0.1:%d SIP/2.0\r\n", route.PhoneNumber, signalingPort) +
		fmt.Sprintf("Via: SIP/2.0/UDP 127.0.0.1:%d;branch=z9hG4bK-hangup-test;rport\r\n", localPort) +
		"Max-Forwards: 70\r\n" +
		"From: <sip:+34648257793@127.0.0.1>;tag=caller\r\n" +
		fmt.Sprintf("To: <sip:%s@127.0.0.1>\r\n", route.PhoneNumber) +
		"Call-ID: hangup-test@127.0.0.1\r\n" +
		"CSeq: 1 INVITE\r\n" +
		fmt.Sprintf("Contact: <sip:caller@127.0.0.1:%d>\r\n", localPort) +
		"Content-Type: application/sdp\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(sdpBody), sdpBody)
	if _, err := conn.Write([]byte(invite)); err != nil {
		t.Fatal(err)
	}
	readSIPResponseContaining(t, conn, "180 Ringing")
	call, err := app.db().findInboundCallByProviderID("hangup-test@127.0.0.1")
	if err != nil || call == nil {
		t.Fatalf("pending call: %v", err)
	}
	go func() { _ = gateway.Hangup(call) }()
	readSIPResponseContaining(t, conn, "487 Request Terminated")
}

func TestReviewGSDPAnswerMirrorsEveryOfferedMediaLine(t *testing.T) {
	cfg := directSIPTestConfig()
	body := "v=0\r\no=- 1 1 IN IP4 203.0.113.10\r\ns=-\r\nc=IN IP4 203.0.113.10\r\nt=0 0\r\n" +
		"m=video 30000 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n" +
		"m=audio 30002 UDP/TLS/RTP/SAVPF 0\r\na=rtpmap:0 PCMU/8000\r\n" +
		"m=audio 30004 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"
	offer, err := parseSIPMediaOffer([]byte(body), cfg)
	if err != nil {
		t.Fatalf("supported later audio stream rejected: %v", err)
	}
	if offer.RemotePort != 30004 || offer.MediaIndex != 2 {
		t.Fatalf("selected stream=%+v", offer)
	}
	answer, err := buildSIPMediaAnswer(cfg, offer, 20000, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(answer), "\r\n") {
		if strings.HasPrefix(line, "m=") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 3 || lines[0] != "m=video 0 RTP/AVP 96" || lines[1] != "m=audio 0 UDP/TLS/RTP/SAVPF 0" || !strings.HasPrefix(lines[2], "m=audio 20000 RTP/AVP 0") {
		t.Fatalf("answer m-lines=%q", lines)
	}
}

func TestReviewGSDPAcceptsRFC2543HoldInReoffer(t *testing.T) {
	cfg := directSIPTestConfig()
	established, err := parseSIPMediaOffer([]byte("v=0\r\no=- 1 1 IN IP4 203.0.113.10\r\ns=-\r\nc=IN IP4 203.0.113.10\r\nt=0 0\r\nm=audio 30000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := parseSIPMediaOfferOptions([]byte("v=0\r\no=- 1 2 IN IP4 203.0.113.10\r\ns=-\r\nc=IN IP4 0.0.0.0\r\nt=0 0\r\nm=audio 30000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"), cfg, []string{"PCMU"}, true)
	if err != nil {
		t.Fatalf("c=0.0.0.0 hold rejected: %v", err)
	}
	if hold.Direction != "sendonly" || !sameSIPMedia(hold, established) || sipAnswerDirection(hold.Direction) != "recvonly" {
		t.Fatalf("hold offer=%+v", hold)
	}
	if _, err := parseSIPMediaOffer([]byte("v=0\r\no=- 1 1 IN IP4 203.0.113.10\r\ns=-\r\nc=IN IP4 0.0.0.0\r\nt=0 0\r\nm=audio 30000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"), cfg); err == nil {
		t.Fatal("initial offer without a media address accepted")
	}
}

func TestReviewGLocalSDPKeepsSessionAndVersionsChanges(t *testing.T) {
	cfg := directSIPTestConfig()
	offer := sipMediaOffer{PayloadType: 0, Codec: "PCMU", Direction: "sendrecv", Secure: true, CryptoTag: "1"}
	security := &sipMediaSecurity{LocalKey: make([]byte, 30)}
	first, err := buildSIPMediaAnswer(cfg, offer, 20000, security)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, _ := buildSIPMediaAnswer(cfg, offer, 20000, security)
	origin := func(body []byte) []string { return strings.Fields(strings.TrimPrefix(sipSDPOriginLine(body), "o=")) }
	same := nextSIPLocalSDP(first, rebuilt)
	if origin(same)[1] != origin(first)[1] || origin(same)[2] != origin(first)[2] || string(same) != string(first) {
		t.Fatalf("unchanged refresh SDP changed session: %q vs %q", origin(same), origin(first))
	}
	held := nextSIPLocalSDP(same, sipSDPWithDirection(same, "recvonly"))
	if origin(held)[1] != origin(first)[1] || origin(held)[2] != "2" || !strings.Contains(string(held), "a=recvonly\r\n") {
		t.Fatalf("direction change not versioned: %q", held)
	}
	tagged := nextSIPLocalSDP(held, sipSDPWithCryptoTag(held, "7"))
	if !strings.Contains(string(tagged), "a=crypto:7 "+sipSDESCryptoSuite) || origin(tagged)[2] != "3" {
		t.Fatalf("crypto tag not echoed: %q", tagged)
	}
}

type reviewGServerTx struct {
	sip.ServerTransaction
	responses []*sip.Response
}

func (tx *reviewGServerTx) Respond(res *sip.Response) error {
	tx.responses = append(tx.responses, res)
	return nil
}

func reviewGOutboundEarlySession(t *testing.T) (*sipGateway, *outboundSIPSession) {
	t.Helper()
	invite := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "+33123456789", Host: "127.0.0.1"})
	from := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "+33123456780", Host: "127.0.0.1"}, Params: sip.NewParams()}
	from.Params.Add("tag", "ourtag")
	invite.AppendHeader(from)
	invite.AppendHeader(sip.NewHeader("Call-ID", "outbound-early@test"))
	cfg := directSIPTestConfig()
	cfg.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	g := &sipGateway{cfg: cfg, outboundDialogs: &sipgo.DialogClientCache{}, outboundByProviderCall: map[string]*outboundSIPSession{}}
	s := &outboundSIPSession{gateway: g, providerCallID: "outbound-early@test",
		dialog: &sipgo.DialogClientSession{Dialog: sipgo.Dialog{InviteRequest: invite}}}
	g.outboundByProviderCall[s.providerCallID] = s
	return g, s
}

func reviewGEarlyRequest(method sip.RequestMethod, seq uint32, body string, headers ...sip.Header) *sip.Request {
	req := sip.NewRequest(method, sip.Uri{Scheme: "sip", User: "apteva", Host: "127.0.0.1"})
	to := &sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: "+33123456780", Host: "127.0.0.1"}, Params: sip.NewParams()}
	to.Params.Add("tag", "ourtag")
	req.AppendHeader(to)
	from := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "+33123456789", Host: "127.0.0.1"}, Params: sip.NewParams()}
	from.Params.Add("tag", "theirtag")
	req.AppendHeader(from)
	req.AppendHeader(sip.NewHeader("Call-ID", "outbound-early@test"))
	req.AppendHeader(&sip.CSeqHeader{SeqNo: seq, MethodName: method})
	for _, header := range headers {
		req.AppendHeader(header)
	}
	if body != "" {
		req.SetBody([]byte(body))
	}
	req.SetSource("127.0.0.1:5061")
	return req
}

func TestReviewGOutboundEarlyDialogRefreshAndInfoAvoid481(t *testing.T) {
	g, _ := reviewGOutboundEarlySession(t)
	cases := []struct {
		name   string
		req    *sip.Request
		info   bool
		status int
	}{
		{"timer update", reviewGEarlyRequest(sip.UPDATE, 10, "", sip.NewHeader("Session-Expires", "1800;refresher=uac"), sip.NewHeader("Supported", "timer")), false, 200},
		{"update offer", reviewGEarlyRequest(sip.UPDATE, 11, "v=0\r\n", sip.NewHeader("Content-Type", "application/sdp")), false, 500},
		{"reinvite", reviewGEarlyRequest(sip.INVITE, 12, ""), false, 491},
		{"info", reviewGEarlyRequest(sip.INFO, 13, "Signal=5\r\nDuration=160\r\n", sip.NewHeader("Content-Type", "application/dtmf-relay")), true, 200},
	}
	for _, tc := range cases {
		tx := &reviewGServerTx{}
		if tc.info {
			g.handleInfo(tc.req, tx)
		} else {
			g.handleRefresh(tc.req, tx)
		}
		if len(tx.responses) != 1 || tx.responses[0].StatusCode != tc.status {
			t.Fatalf("%s: responses=%v", tc.name, tx.responses)
		}
		if tc.status == 500 && tx.responses[0].GetHeader("Retry-After") == nil {
			t.Fatalf("%s: 500 without Retry-After", tc.name)
		}
	}
	// A request for another dialog still gets 481.
	foreign := reviewGEarlyRequest(sip.UPDATE, 20, "")
	foreign.To().Params = sip.NewParams()
	foreign.To().Params.Add("tag", "other")
	tx := &reviewGServerTx{}
	g.handleRefresh(foreign, tx)
	if len(tx.responses) != 1 || tx.responses[0].StatusCode != 481 {
		t.Fatalf("foreign early request=%v", tx.responses)
	}
}
