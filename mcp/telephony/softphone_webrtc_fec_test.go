package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pion/sdp/v3"
)

func TestRTCFECPreferenceValidationAndIsolation(t *testing.T) {
	if value, err := parseRTCFECPreference(nil); err != nil || value != nil || !(softphoneRTCConfig{}).fecEnabled() {
		t.Fatal("missing preference changed the default", value, err)
	}
	for _, value := range []string{"true", "false"} {
		fec, err := parseRTCFECPreference(url.Values{"webrtc_fec": {value}})
		if err != nil || fec == nil || *fec != (value == "true") {
			t.Fatal(value, fec, err)
		}
		if (softphoneRTCConfig{FEC: fec}).fecEnabled() != *fec || !(softphoneRTCConfig{}).fecEnabled() {
			t.Fatal("preference leaked into another call")
		}
	}
	for _, values := range [][]string{nil, {""}, {"0"}, {"False"}, {"garbage"}, {"false", "true"}, {"false", "false"}} {
		if _, err := parseRTCFECPreference(url.Values{"webrtc_fec": values}); err == nil {
			t.Fatal("accepted malformed preference", values)
		}
	}
}

func TestRTCFECSDPOptOutOnlyChangesOpusFEC(t *testing.T) {
	raw := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 109 112 101\r\na=ice-ufrag:keep\r\na=ice-pwd:unchanged\r\na=rtpmap:109 opus/48000/2\r\na=fmtp:109 minptime=10;useinbandfec=1;stereo=0;maxaveragebitrate=32000;useinbandfec=1\r\na=rtpmap:112 OPUS/48000/2\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-16\r\n"
	changed, err := disableRTCSDPFEC(raw)
	if err != nil {
		t.Fatal(err)
	}
	var parsed sdp.SessionDescription
	if err := parsed.Unmarshal([]byte(changed)); err != nil {
		t.Fatal(err)
	}
	var fmtps []string
	for _, a := range parsed.MediaDescriptions[0].Attributes {
		if a.Key == "fmtp" {
			fmtps = append(fmtps, a.Value)
		}
	}
	if strings.Join(fmtps, "|") != "109 minptime=10;stereo=0;maxaveragebitrate=32000;useinbandfec=0|101 0-16|112 useinbandfec=0" || !strings.Contains(changed, "a=ice-pwd:unchanged") || !strings.Contains(changed, "a=ice-ufrag:keep") {
		t.Fatal("changed unrelated negotiation or left redundant FEC", changed)
	}
	if _, err := disableRTCSDPFEC("invalid SDP"); err == nil {
		t.Fatal("accepted malformed SDP")
	}
}

func TestRTCFECOffUsesPortableCodecAndKeepsAudio(t *testing.T) {
	encoder, err := newRTCOpusEncoderWithFEC(32000, false)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	decoder, err := newRTCOpusDecoderWithFEC(false)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	if _, ok := encoder.(*rtcGoOpusEncoder); !ok {
		t.Fatal("opt-out retained native encoder cost")
	}
	if _, ok := decoder.(*rtcGoOpusDecoder); !ok {
		t.Fatal("opt-out retained native decoder")
	}
	packet, samples := make([]byte, 1275), make([]int16, 2880)
	for i := 0; i < 5; i++ {
		n, err := encoder.EncodeFloat32(make([]float32, 960), packet)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := decoder.decode(packet[:n], samples); err != nil || n != 480 {
			t.Fatal("opt-out stopped audio", n, err)
		}
	}
	if n, err := decoder.recover(nil, samples[:480], false); err != nil || n != 480 {
		t.Fatal("opt-out disabled basic loss concealment", n, err)
	}
}

func TestRTCFECMalformedOptionDoesNotReplaceLiveSocket(t *testing.T) {
	rtcTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	socket := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")
	readSoftphoneEventWithin(t, socket, "ready", 3*time.Second)
	hub := app.softphones.lookup("call-soft-1")
	previous := hub.browserWriter()
	for _, suffix := range []string{"garbage", "false&webrtc_fec=true", ""} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/softphone/media/call-soft-1/peer-secret?transport=webrtc&webrtc_fec="+suffix, nil)
		app.handleSoftphoneMedia(w, r)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_webrtc_fec") || app.softphones.lookup("call-soft-1") != hub || hub.browserWriter() != previous {
			t.Fatal("malformed preference reached media upgrade", w.Code, w.Body.String())
		}
	}
}
