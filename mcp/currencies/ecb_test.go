package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const ecbFixture = `<?xml version="1.0" encoding="UTF-8"?>
<gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01" xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref">
  <Cube>
    <Cube time="2026-08-24">
      <Cube currency="USD" rate="1.2"/>
      <Cube currency="JPY" rate="180"/>
    </Cube>
    <Cube time="2026-08-25">
      <Cube currency="USD" rate="1.25"/>
      <Cube currency="JPY" rate="187.5"/>
    </Cube>
  </Cube>
</gesmes:Envelope>`

func TestParseECBReferenceRates(t *testing.T) {
	observedAt := time.Date(2026, 8, 25, 16, 5, 0, 0, time.UTC)
	inputs, latest, err := parseECBReferenceRates([]byte(ecbFixture), observedAt, "fixture-hash")
	if err != nil {
		t.Fatal(err)
	}
	if latest != "2026-08-25" || len(inputs) != 4 {
		t.Fatalf("latest=%q observations=%d", latest, len(inputs))
	}
	got := inputs[2]
	if got.Base != "EUR" || got.Quote != "USD" || got.Rate != "1.25" {
		t.Fatalf("observation=%+v", got)
	}
	if want := "2026-08-25T14:00:00Z"; got.EffectiveAt.Format(time.RFC3339) != want {
		t.Fatalf("effective_at=%s, want %s", got.EffectiveAt.Format(time.RFC3339), want)
	}
	if got.ProviderSlug != ecbProviderSlug || got.AdapterVersion != "ecb-eurofxref-v1" || len(got.QualityFlags) != 2 {
		t.Fatalf("provenance=%+v", got)
	}
}

func TestSyncECBReferenceRatesIsIdempotentAndSupportsCrossRates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(ecbFixture))
	}))
	defer server.Close()

	ctx := testCtx(t)
	created, latest, err := syncECBReferenceRates(context.Background(), ctx, server.URL, server.Client())
	if err != nil || created != 4 || latest != "2026-08-25" {
		t.Fatalf("first sync created=%d latest=%q err=%v", created, latest, err)
	}
	created, latest, err = syncECBReferenceRates(context.Background(), ctx, server.URL, server.Client())
	if err != nil || created != 0 || latest != "2026-08-25" {
		t.Fatalf("second sync created=%d latest=%q err=%v", created, latest, err)
	}

	req := baseRequest("USD", "JPY", "2026-08-25T23:00:00Z")
	quote, err := (&App{}).selectRate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if quote.Rate != "150" || !quote.Derived || len(quote.Path) != 2 {
		t.Fatalf("cross quote=%+v", quote)
	}
	for _, edge := range quote.Path {
		if edge.Provider != ecbProviderSlug {
			t.Fatalf("unexpected cross-rate provenance: %+v", edge)
		}
	}
}

func TestSyncECBReferenceRatesRejectsUpstreamFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx := testCtx(t)
	if _, _, err := syncECBReferenceRates(context.Background(), ctx, server.URL, server.Client()); err == nil {
		t.Fatal("expected an upstream HTTP error")
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM rate_observations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stored %d observations after failed sync", count)
	}
}
