package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws/wsutil"
)

func TestBrowserNetworkSignedMetadataAndSafeFallback(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "local-install-token")
	for _, tc := range []struct {
		name, ip, want, source string
		change                 func(*http.Request)
	}{
		{"ipv4", "198.51.100.7", "198.51.100.7", "trusted_proxy", nil},
		{"ipv6", "2001:db8::7", "2001:db8::7", "trusted_proxy", nil},
		{"mapped_ipv4", "::ffff:198.51.100.7", "198.51.100.7", "trusted_proxy", nil},
		{"missing", "", "192.0.2.4", "socket_peer", nil},
		{"forged", "198.51.100.7", "192.0.2.4", "socket_peer", func(r *http.Request) { r.Header.Set(sdk.HeaderClientIPSignature, strings.Repeat("0", 64)) }},
		{"wrong_install", "198.51.100.7", "192.0.2.4", "socket_peer", func(r *http.Request) { sdk.SetClientIPHeaders(r, "198.51.100.7", "other-install") }},
		{"request_changed", "198.51.100.7", "192.0.2.4", "socket_peer", func(r *http.Request) { r.URL.RawQuery = "changed=yes" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://local/softphone/media/call/SECRET?transport=webrtc", nil)
			r.RemoteAddr = "192.0.2.4:1234"
			sdk.SetClientIPHeaders(r, tc.ip, "local-install-token")
			r.Header.Set("X-Forwarded-For", "198.51.100.7")
			r.Header.Set("Authorization", "Bearer SECRET")
			if tc.change != nil {
				tc.change(r)
			}
			address, err := resolveAudioNetworkAddress(r)
			if (err != nil) != (tc.change != nil) {
				t.Fatalf("warning: %v", err)
			}
			config := map[string]string{"audio_telemetry_trusted_proxy_cidrs": "192.0.2.0/24", "audio_telemetry_known_vpn_exits": "198.51.100.7,2001:db8::/64"}
			event := newAudioNetworkContextWithAddress(&callRow{ID: "call", ProjectID: "project"}, phoneTestIdentity("alice"), address, config)
			if event.ClientIP != tc.want || event.SocketPeerIP != "192.0.2.4" || event.AddressSource != tc.source {
				t.Fatalf("%+v", event)
			}
			wantClassification := "unknown"
			if tc.source == "trusted_proxy" {
				wantClassification = "known_vpn_exit"
			}
			if event.Classification != wantClassification {
				t.Fatalf("%+v", event)
			}
			var hasher audioPeerHasher
			hash, epoch, source := hasher.hashAddress(address.Client, address.Source)
			if hash == "" || epoch == "" || source != event.AddressSource {
				t.Fatal(hash, epoch, source)
			}
			raw, _ := json.Marshal(event)
			if strings.Contains(string(raw), "SECRET") {
				t.Fatal("secret retained")
			}
			// Retained diagnostics no longer depend on live handshake headers or expiry.
			r.Header = make(http.Header)
			r.RemoteAddr = "203.0.113.99:9"
			if again := newAudioNetworkContextWithAddress(&callRow{ID: "call", ProjectID: "project"}, phoneTestIdentity("alice"), address, config); again.ClientIP != tc.want {
				t.Fatal("address not frozen")
			}
		})
	}
}

func TestBrowserNetworkSignedReconnectAndForgedMetadataDoNotAffectMedia(t *testing.T) {
	softphoneTestCtx(t)
	t.Setenv("APTEVA_APP_TOKEN", "local-install-token")
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	row := phoneTestCall(t, app, "signed-network-media", "in-progress")
	alice := phoneTestIdentity("alice")
	principal := &phonePrincipal{Identity: alice, Project: row.ProjectID, Destinations: map[string]bool{"sales": true}}
	if err := app.setPhoneOwner(&row, principal, "sales"); err != nil {
		t.Fatal(err)
	}
	// Fixture stands in for the validated Server forwarding, signing the final URI.
	// Also exercises the shared handler's observation-only forged-header fallback.
	server := softphoneTestServer(t, app)
	peer := dialWS(t, server.URL+"/peer/"+row.ID+"/"+row.CallbackSecret)
	defer peer.Close()
	var connectionID string
	hub := app.softphones.hubFor(row.ID)
	deadline := time.Now().Add(3 * time.Second)
	for hub.peerWriter() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	carrier := hub.peerWriter()
	if carrier == nil {
		t.Fatal("carrier did not attach")
	}
	for _, ip := range []string{"198.51.100.7", "2001:db8::8", "forged"} {
		session, err := app.issuePhoneSession(&row, principal)
		if err != nil {
			t.Fatal(err)
		}
		// One proxy per reconnect prevents mutable fixture data racing the handler.
		target, _ := url.Parse(server.URL)
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarder := httputil.NewSingleHostReverseProxy(target)
			director := forwarder.Director
			forwarder.Director = func(r *http.Request) {
				director(r)
				if ip == "forged" {
					r.Header.Set(sdk.HeaderClientIPMetadata, "invalid")
					r.Header.Set(sdk.HeaderClientIPSignature, strings.Repeat("0", 64))
				} else {
					sdk.SetClientIPHeaders(r, ip, "local-install-token")
				}
			}
			forwarder.ServeHTTP(w, r)
		}))
		browser := dialWS(t, proxy.URL+strings.TrimPrefix(session.MediaURL, "/api/apps/telephony/_install/42"))
		readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)
		hub := app.softphones.hubFor(row.ID)
		socket, _ := hub.telemetry.snapshots()
		if socket.ConnectionID == connectionID || socket.ConnectionID == "" || hub.peerWriter() != carrier {
			t.Fatal("carrier or reconnect changed")
		}
		connectionID = socket.ConnectionID
		pcm := bytes.Repeat([]byte{0x34, 0x12}, 480)
		if err = wsutil.WriteClientBinary(browser, pcm); err != nil {
			t.Fatal(err)
		}
		if got := readBinaryWithin(t, peer, 3*time.Second); !bytes.Equal(got, pcm) {
			t.Fatal("microphone changed")
		}
		if err = wsutil.WriteClientBinary(peer, pcm); err != nil {
			t.Fatal(err)
		}
		if got := readBinaryWithin(t, browser, 3*time.Second); !bytes.Equal(got, pcm) {
			t.Fatal("playback changed")
		}
		app.audioNetworks.mu.Lock()
		found := false
		for _, e := range app.audioNetworks.pending {
			if e.ConnectionID == connectionID && (e.Action == "attached" || e.Action == "reconnected") {
				found = true
				wantIP, wantSource := ip, "trusted_proxy"
				if ip == "forged" {
					wantIP, wantSource = "127.0.0.1", "socket_peer"
				}
				if e.ClientIP != wantIP || e.SocketPeerIP != "127.0.0.1" || e.AddressSource != wantSource || e.AdviserIdentity != alice {
					t.Errorf("%+v", e)
				}
			}
		}
		app.audioNetworks.mu.Unlock()
		if !found {
			t.Fatal("missing connection event")
		}
		browser.Close()
		proxy.Close()
	}
	current, err := app.db().findCall(row.ID)
	if err != nil || isTerminalStatus(current.Status) {
		t.Fatal("observations ended carrier call")
	}
}
