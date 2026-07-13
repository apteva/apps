package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPJSONStatusSetsHeadersBeforeStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	httpJSONStatus(rec, http.StatusCreated, map[string]any{"created": true})
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q", got)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q", got)
	}
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("x-content-type-options = %q", got)
	}
}

func TestDecodeJSONRejectsUnknownAndTrailingData(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":  `{"title":"ok","surprise":true}`,
		"trailing": `{"title":"ok"} {"title":"second"}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/testimonials", strings.NewReader(body))
			rec := httptest.NewRecorder()
			var dst Testimonial
			if err := decodeJSON(rec, req, &dst); err == nil {
				t.Fatal("decode succeeded")
			}
		})
	}
}

func TestIntFromAnyRejectsFractions(t *testing.T) {
	if _, ok := intFromAny(4.5); ok {
		t.Fatal("fractional float accepted as integer")
	}
}
