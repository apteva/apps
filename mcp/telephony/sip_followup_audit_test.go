package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

func sipAuditSecureOffer(t *testing.T) (sipMediaOffer, []byte) {
	t.Helper()
	key := bytes.Repeat([]byte{0x31}, 30)
	body := "v=0\r\no=- 1 1 IN IP4 203.0.113.20\r\ns=test\r\nc=IN IP4 203.0.113.20\r\nt=0 0\r\nm=audio 4000 RTP/SAVP 0\r\na=crypto:7 " + sipSDESCryptoSuite + " inline:" + base64.StdEncoding.EncodeToString(key) + "\r\n"
	offer, err := parseSIPMediaOffer([]byte(body), directSIPTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	return offer, key
}
func TestSIPAuditAnswerEchoesSelectedCryptoTag(t *testing.T) {
	offer, _ := sipAuditSecureOffer(t)
	security, err := newSIPMediaSecurity(offer)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := buildSIPMediaAnswer(directSIPTestConfig(), offer, 20000, security)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(answer), "a=crypto:7 ") {
		t.Fatal("offer selected crypto tag 7 but answer did not echo 7")
	}
}
func TestSIPAuditRejectsEncryptedPacketReplay(t *testing.T) {
	offer, key := sipAuditSecureOffer(t)
	security, err := newSIPMediaSecurity(offer)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := srtp.CreateContext(key[:16], key[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
	if err != nil {
		t.Fatal(err)
	}
	packet := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 1, Timestamp: 160, SSRC: 99}, Payload: bytes.Repeat([]byte{0xff}, 160)}
	raw, err := packet.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	raw, err = sender.EncryptRTP(nil, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	media := sipRTPMedia{offer: offer, security: security}
	if _, err = media.decodePacket(raw); err != nil {
		t.Fatal(err)
	}
	if _, err = media.decodePacket(raw); err == nil {
		t.Fatal("identical authenticated SRTP packet accepted twice")
	}
}
func TestSIPAuditShortBurstEventuallyPlays(t *testing.T) {
	buffer := newRTPJitterBuffer()
	buffer.push(1, bytes.Repeat([]byte{0xff}, 160))
	buffer.push(2, bytes.Repeat([]byte{0xff}, 160))
	for tick := 0; tick < 250; tick++ {
		if data, ready := buffer.pop(); ready && len(data) > 0 {
			return
		}
	}
	t.Fatal("40 ms burst remains buffered across 250 playback ticks")
}
func TestSIPAuditRejectsIncorrectDynamicCodecRate(t *testing.T) {
	body := "v=0\r\no=- 1 1 IN IP4 203.0.113.20\r\ns=test\r\nc=IN IP4 203.0.113.20\r\nt=0 0\r\nm=audio 4000 RTP/AVP 96\r\na=rtpmap:96 PCMU/80000\r\n"
	if _, err := parseSIPMediaOffer([]byte(body), directSIPTestConfig()); err == nil {
		t.Fatal("accepted PCMU/80000 as an 8000 Hz codec")
	}
}
func TestSIPAuditRTPTimestampAccountsForSilence(t *testing.T) {
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
	media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: "PCMU"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pacer := newSIPRTPPacer(ctx, media, &sipPlaybackState{}, nil)
	read := func() uint32 {
		t.Helper()
		receiver.SetReadDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 2048)
		n, _, err := receiver.ReadFromUDP(buf)
		if err != nil {
			t.Fatal(err)
		}
		var p rtp.Packet
		if err = p.Unmarshal(buf[:n]); err != nil {
			t.Fatal(err)
		}
		return p.Timestamp
	}
	frame := []sipRTPOutboundPacket{{payload: bytes.Repeat([]byte{0xff}, 160)}}
	if _, err = pacer.enqueue(frame); err != nil {
		t.Fatal(err)
	}
	first := read()
	time.Sleep(140 * time.Millisecond)
	if _, err = pacer.enqueue(frame); err != nil {
		t.Fatal(err)
	}
	second := read()
	if second-first < 800 {
		t.Fatalf("timestamp advanced only %d samples after >=140 ms silence; expected elapsed 8 kHz clock", second-first)
	}
}
