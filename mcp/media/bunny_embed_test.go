package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPositiveJSONInteger(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int64
		ok   bool
	}{
		{name: "positive", raw: "215489", want: 215489, ok: true},
		{name: "zero", raw: "0"},
		{name: "negative", raw: "-1"},
		{name: "fraction", raw: "1.5"},
		{name: "string", raw: `"215489"`},
		{name: "null", raw: "null"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := positiveJSONInteger(json.RawMessage(tc.raw))
			if got != tc.want || ok != tc.ok {
				t.Fatalf("positiveJSONInteger(%q) = (%d, %v), want (%d, %v)", tc.raw, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestVerifyBunnyEmbedRemote(t *testing.T) {
	const (
		libraryID = int64(215489)
		videoGUID = "989c7743-9372-48de-aca2-ceea4bee3071"
	)

	t.Run("valid player", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("cdn-videolibraryid", fmt.Sprint(libraryID))
			fmt.Fprintf(w, `<html><meta property="og:video:url" content="https://cdn.example/%s/play.mp4"></html>`, videoGUID)
		}))
		defer server.Close()

		got := verifyBunnyEmbedRemote(context.Background(), server.URL, libraryID, videoGUID)
		if !got.Verified || got.Reason != "" || got.HTTPStatus != http.StatusOK {
			t.Fatalf("unexpected verification result: %+v", got)
		}
	})

	t.Run("soft 404", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `<html><title>404</title></html>`)
		}))
		defer server.Close()

		got := verifyBunnyEmbedRemote(context.Background(), server.URL, libraryID, videoGUID)
		if got.Verified || got.Reason != "remote_soft_404" {
			t.Fatalf("unexpected verification result: %+v", got)
		}
	})

	t.Run("library mismatch", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("cdn-videolibraryid", "999")
			fmt.Fprintf(w, `<meta property="og:video:url" content="https://cdn.example/%s/play.mp4">`, videoGUID)
		}))
		defer server.Close()

		got := verifyBunnyEmbedRemote(context.Background(), server.URL, libraryID, videoGUID)
		if got.Verified || got.Reason != "remote_library_id_mismatch" {
			t.Fatalf("unexpected verification result: %+v", got)
		}
	})
}
