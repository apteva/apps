package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// Pins both content-encoding paths supported by files_upload:
//
//   1. plain base64 string (legacy: direct HTTP/MCP callers)
//   2. {"_binary": true, "base64": "...", "mimeType": "...", "size": N}
//      envelope (new: apteva-core's MCP proxy substitutes this when
//      the agent passes a blobref://<id> file handle)
//
// Without case 2 the agent cannot route a screenshot from
// computer_use to files_upload — the envelope JSON would fail base64
// decode and the upload would error.

func TestDecodeUploadPayload_PlainBase64(t *testing.T) {
	want := []byte("hello world")
	got, err := decodeUploadPayload(base64.StdEncoding.EncodeToString(want))
	if err != nil {
		t.Fatalf("plain base64 should decode: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDecodeUploadPayload_BinaryEnvelope(t *testing.T) {
	want := []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3} // JPEG magic + payload
	envelope, _ := json.Marshal(map[string]any{
		"_binary":  true,
		"base64":   base64.StdEncoding.EncodeToString(want),
		"mimeType": "image/jpeg",
		"size":     len(want),
	})
	got, err := decodeUploadPayload(string(envelope))
	if err != nil {
		t.Fatalf("envelope should decode: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDecodeUploadPayload_BinaryEnvelopeWithLeadingWhitespace(t *testing.T) {
	want := []byte("with whitespace")
	envelope, _ := json.Marshal(map[string]any{
		"_binary": true,
		"base64":  base64.StdEncoding.EncodeToString(want),
	})
	got, err := decodeUploadPayload("\n  " + string(envelope) + "\n")
	if err != nil {
		t.Fatalf("envelope with whitespace should decode: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDecodeUploadPayload_RejectsInvalidBase64(t *testing.T) {
	_, err := decodeUploadPayload("not!!base64!!")
	if err == nil {
		t.Fatal("expected error on invalid base64")
	}
}

func TestDecodeUploadPayload_RejectsBadEnvelopeBase64(t *testing.T) {
	bad := `{"_binary": true, "base64": "not!!base64!!"}`
	_, err := decodeUploadPayload(bad)
	if err == nil {
		t.Fatal("expected error on invalid base64 inside envelope")
	}
	if !strings.Contains(err.Error(), "envelope") {
		t.Errorf("error should mention envelope, got: %v", err)
	}
}
