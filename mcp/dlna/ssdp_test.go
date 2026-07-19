package main

import "testing"

func TestParseHeadersIsCaseInsensitiveAndTolerant(t *testing.T) {
	headers := parseHeaders("M-SEARCH * HTTP/1.1\r\nHost: 239.255.255.250:1900\r\nst: ssdp:all\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\n\r\n")
	if headers["ST"] != "ssdp:all" || headers["MAN"] != `"ssdp:discover"` || headers["MX"] != "2" {
		t.Fatalf("unexpected headers: %#v", headers)
	}
}

func TestSSDPAdvertisesRequiredTargets(t *testing.T) {
	server := newSSDPServer("test-uuid", 8200, "192.168.1.10", func() string { return "Test" }, nil)
	targets := server.allTargets()
	if len(targets) != 5 {
		t.Fatalf("targets=%d, want 5", len(targets))
	}
	seen := map[string]bool{}
	for _, target := range targets {
		seen[target[0]] = true
	}
	for _, required := range []string{"upnp:rootdevice", "urn:schemas-upnp-org:device:MediaServer:1", "urn:schemas-upnp-org:service:ContentDirectory:1", "urn:schemas-upnp-org:service:ConnectionManager:1", "uuid:test-uuid"} {
		if !seen[required] {
			t.Errorf("missing SSDP target %q", required)
		}
	}
}
