package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const historicalCSV = "CURRENCY,CURRENCY_DENOM,TIME_PERIOD,OBS_VALUE\nGBP,EUR,2025-12-11,0.8\nGBP,EUR,2025-12-12,0.82\n"

func TestECBHistoricalImportBeforeRollingWindow(t *testing.T) {
	app := testCtx(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.Contains(r.URL.Path, "D.GBP.EUR.SP00.A") || r.URL.Query().Get("startPeriod") != "2025-12-01" || r.URL.Query().Get("format") != "csvdata" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.Write([]byte(historicalCSV))
	}))
	defer server.Close()
	prior := ecbDataURL
	ecbDataURL = server.URL + "/"
	defer func() { ecbDataURL = prior }()
	for i := 0; i < 2; i++ {
		out, err := syncECBHistory(context.Background(), app, "GBP", "EUR", "2025-12-01", "2025-12-12")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 2 || out[0].EffectiveAt != "2025-12-12T15:00:00Z" || out[0].ProviderSlug != ecbProviderSlug {
			t.Fatalf("bad historical response %+v", out)
		}
	}
	var count int
	app.AppDB().QueryRow(`SELECT COUNT(*) FROM rate_observations`).Scan(&count)
	if count != 2 || calls != 2 {
		t.Fatalf("count=%d calls=%d", count, calls)
	}
	quote, err := (&App{}).selectRate(app, baseRequest("GBP", "EUR", "2025-12-14T12:00:00Z"))
	if err != nil || len(quote.Path) != 1 || quote.Path[0].EffectiveDate != "2025-12-12" {
		t.Fatalf("weekend quote=%+v err=%v", quote, err)
	}
}
func TestECBHistoricalBoundsAndAtomicRejection(t *testing.T) {
	app := testCtx(t)
	if _, err := syncECBHistory(context.Background(), app, "GBP", "EUR", "2025-01-01", "2025-12-12"); err == nil {
		t.Fatal("unbounded request accepted")
	}
	for _, raw := range []string{"not,csv\n", strings.Replace(historicalCSV, "GBP,EUR,2025-12-12,0.82", "USD,EUR,2025-12-12,0.82", 1), strings.Replace(historicalCSV, "0.82", "-1", 1)} {
		if _, err := parseECBHistoryCSV([]byte(raw), "fixture", time.Now(), []string{"GBP"}, "2025-12-01", "2025-12-12"); err == nil {
			t.Fatal("invalid provider response accepted", raw)
		}
	}
}
func TestECBHistoricalCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := syncECBHistory(ctx, testCtx(t), "GBP", "EUR", "2025-12-01", "2025-12-12"); err == nil {
		t.Fatal("cancelled fetch succeeded")
	}
}
