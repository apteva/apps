package main

import (
	"os"
	"strings"
	"testing"
)

func TestValidateCookieProfile(t *testing.T) {
	cookies := ".youtube.com\tTRUE\t/\tTRUE\t1893456000\tSID\tsecret\n"
	if err := validateCookieProfile("youtube", "cookies_netscape", profilePayload{CookiesNetscape: cookies}); err != nil {
		t.Fatal(err)
	}
	if err := validateCookieProfile("youtube", "cookies_netscape", profilePayload{CookiesNetscape: "bad"}); err == nil {
		t.Fatal("expected invalid cookie format to fail")
	}
}

func TestValidateCookieProfileSupportsPatreon(t *testing.T) {
	cookies := ".patreon.com\tTRUE\t/\tTRUE\t1893456000\tsession_id\tsecret\n"
	if err := validateCookieProfile("patreon", "cookies_netscape", profilePayload{CookiesNetscape: cookies}); err != nil {
		t.Fatal(err)
	}
	if err := validateCookieProfile("patreon", "cookies_netscape", profilePayload{CookiesNetscape: ".youtube.com\tTRUE\t/\tTRUE\t1893456000\tSID\tsecret\n"}); err == nil {
		t.Fatal("expected Patreon profile without Patreon cookies to fail")
	}
}

func TestValidateCookieProfileAcceptsHttpOnlyRows(t *testing.T) {
	cookies := "#HttpOnly_.patreon.com\tTRUE\t/\tTRUE\t1893456000\tsession_id\tsecret\n"
	if err := validateCookieProfile("patreon", "cookies_netscape", profilePayload{CookiesNetscape: cookies}); err != nil {
		t.Fatal(err)
	}
}

func TestEncryptDecryptPayloadRoundTrip(t *testing.T) {
	t.Setenv("MEDIA_DOWNLOADER_SECRET", "test-secret")
	payload := profilePayload{CookiesNetscape: ".youtube.com\tTRUE\t/\tTRUE\t1893456000\tSID\tsecret\n"}
	encoded, err := encryptPayload(nil, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, encryptedPrefix) {
		t.Fatalf("expected encrypted payload, got %q", encoded)
	}
	got, err := decryptPayload(nil, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.CookiesNetscape != payload.CookiesNetscape {
		t.Fatalf("round trip mismatch")
	}
}

func TestEncryptRequiresSecret(t *testing.T) {
	t.Setenv("MEDIA_DOWNLOADER_SECRET", "")
	t.Setenv("APTEVA_SECRET", "")
	os.Unsetenv("MEDIA_DOWNLOADER_SECRET")
	os.Unsetenv("APTEVA_SECRET")
	if _, err := encryptPayload(nil, profilePayload{CookiesNetscape: "x"}); err == nil {
		t.Fatal("expected missing secret error")
	}
}
