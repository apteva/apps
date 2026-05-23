package main

// Image-resolution unit tests (v0.2). The data: + http branches are
// pure / app-free; the storage branch is exercised only for its
// no-platform error path (a full storage round-trip lives in the
// integration test).

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodeDataURI_PNG(t *testing.T) {
	raw := []byte{0x89, 0x50, 0x4e, 0x47}
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
	data, ext, err := decodeDataURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, raw) {
		t.Errorf("data = %v, want %v", data, raw)
	}
	if ext != "png" {
		t.Errorf("ext = %q, want png", ext)
	}
}

func TestDecodeDataURI_RequiresBase64(t *testing.T) {
	if _, _, err := decodeDataURI("data:image/png,notbase64"); err == nil {
		t.Error("expected error for non-base64 data URI")
	}
	if _, _, err := decodeDataURI("not-a-data-uri"); err == nil {
		t.Error("expected error for malformed data URI")
	}
}

func TestResolveImageSrc_RejectsHTTP(t *testing.T) {
	for _, src := range []string{"http://x/y.png", "https://x/y.png", "HTTPS://X/Y.PNG"} {
		_, _, err := resolveImageSrc(nil, src)
		if err == nil {
			t.Errorf("%s: expected http(s) rejection", src)
			continue
		}
		if !strings.Contains(err.Error(), "disabled") {
			t.Errorf("%s: unexpected error %v", src, err)
		}
	}
}

func TestResolveImageSrc_DataURI(t *testing.T) {
	uri := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("x"))
	_, ext, err := resolveImageSrc(nil, uri)
	if err != nil {
		t.Fatal(err)
	}
	if ext != "jpg" {
		t.Errorf("ext = %q, want jpg", ext)
	}
}

// storage:<id> (and bare numeric) need a platform client; with none,
// resolution surfaces a clear error so imageRow can fall back to a
// placeholder.
func TestResolveImageSrc_StorageNoPlatform(t *testing.T) {
	for _, src := range []string{"storage:5", "5"} {
		if _, _, err := resolveImageSrc(nil, src); err == nil {
			t.Errorf("%s: expected error when platform client is nil", src)
		}
	}
}

func TestResolveImageSrc_Unsupported(t *testing.T) {
	if _, _, err := resolveImageSrc(nil, "ftp://nope"); err == nil {
		t.Error("expected error for unsupported scheme")
	}
}

func TestExtFromContentType(t *testing.T) {
	for ct, want := range map[string]string{
		"image/png":  "png",
		"image/jpeg": "jpg",
		"image/jpg":  "jpg",
		"image/gif":  "",
		"":           "",
	} {
		if got := extFromContentType(ct); got != want {
			t.Errorf("extFromContentType(%q) = %q, want %q", ct, got, want)
		}
	}
}
