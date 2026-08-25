package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	ecbProviderSlug = "ecb-reference-rates"
	ecbProviderName = "European Central Bank"
	ecbHistoryURL   = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-hist-90d.xml"
	ecbMaxBodyBytes = 2 << 20
)

var ecbClient = &http.Client{Timeout: 12 * time.Second}

type ecbEnvelope struct {
	Cube struct {
		Days []ecbDay `xml:"Cube"`
	} `xml:"Cube"`
}

type ecbDay struct {
	Date  string    `xml:"time,attr"`
	Rates []ecbRate `xml:"Cube"`
}

type ecbRate struct {
	Currency string `xml:"currency,attr"`
	Rate     string `xml:"rate,attr"`
}

func parseECBReferenceRates(raw []byte, observedAt time.Time, payloadHash string) ([]ObservationInput, string, error) {
	var envelope ecbEnvelope
	if err := xml.Unmarshal(raw, &envelope); err != nil {
		return nil, "", fmt.Errorf("decode ECB reference rates: %w", err)
	}
	if len(envelope.Cube.Days) == 0 {
		return nil, "", errors.New("ECB reference-rate feed contained no dated observations")
	}
	brussels, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		return nil, "", err
	}
	inputs := []ObservationInput{}
	latest := ""
	for _, day := range envelope.Cube.Days {
		date, err := time.Parse("2006-01-02", strings.TrimSpace(day.Date))
		if err != nil {
			continue
		}
		if day.Date > latest {
			latest = day.Date
		}
		// ECB publishes at around 16:00 Brussels time. Recording that time
		// prevents an exact intraday as_of from seeing a rate before release.
		effective := time.Date(date.Year(), date.Month(), date.Day(), 16, 0, 0, 0, brussels).UTC()
		for _, item := range day.Rates {
			quote, err := normalizeCode(item.Currency)
			if err != nil || quote == "EUR" {
				continue
			}
			_, canonical, err := parsePositiveDecimal(item.Rate)
			if err != nil {
				continue
			}
			inputs = append(inputs, ObservationInput{
				Base: "EUR", Quote: quote, Rate: canonical, RateKind: "reference",
				EffectiveAt: effective, EffectiveDate: day.Date, Granularity: "day",
				ObservedAt: observedAt.UTC(), ProviderSlug: ecbProviderSlug,
				ProviderRef:  ecbHistoryURL + "#" + day.Date,
				OriginalBase: "EUR", OriginalQuote: quote, PayloadHash: payloadHash,
				AdapterVersion: "ecb-eurofxref-v1",
				QualityFlags:   []string{"official_reference_rate", "information_only"},
			})
		}
	}
	if len(inputs) == 0 {
		return nil, "", errors.New("ECB reference-rate feed contained no usable observations")
	}
	return inputs, latest, nil
}

func syncECBReferenceRates(ctx context.Context, app *sdk.AppCtx, url string, client *http.Client) (int, string, error) {
	if client == nil {
		client = ecbClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Accept", "application/xml, text/xml;q=0.9")
	req.Header.Set("User-Agent", "Apteva-Currencies/0.2 (+https://github.com/apteva/apps)")
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("download ECB reference rates: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("download ECB reference rates: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, ecbMaxBodyBytes+1))
	if err != nil {
		return 0, "", err
	}
	if len(raw) > ecbMaxBodyBytes {
		return 0, "", errors.New("ECB reference-rate response exceeded 2 MiB")
	}
	sum := sha256.Sum256(raw)
	inputs, latest, err := parseECBReferenceRates(raw, time.Now().UTC(), hex.EncodeToString(sum[:]))
	if err != nil {
		return 0, "", err
	}
	created, err := insertECBObservations(ctx, app.AppDB(), scopeKey(app, nil), inputs)
	return created, latest, err
}

func insertECBObservations(ctx context.Context, db *sql.DB, projectID string, inputs []ObservationInput) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO rate_observations
        (project_id,base,quote,rate_text,rate_kind,effective_at,effective_date,granularity,
         observed_at,provider_slug,connection_id,provider_ref,original_base,original_quote,
         payload_hash,adapter_version,quality_flags_json)
        VALUES (?,?,?,?,?,?,?,?,?,?,NULL,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	created := 0
	for _, input := range inputs {
		flags, _ := json.Marshal(input.QualityFlags)
		result, err := stmt.ExecContext(ctx,
			projectID, input.Base, input.Quote, input.Rate, input.RateKind,
			input.EffectiveAt.UTC().Format(time.RFC3339Nano), input.EffectiveDate,
			input.Granularity, input.ObservedAt.UTC().Format(time.RFC3339Nano),
			input.ProviderSlug, input.ProviderRef, input.OriginalBase, input.OriginalQuote,
			input.PayloadHash, input.AdapterVersion, string(flags))
		if err != nil {
			return 0, err
		}
		if affected, err := result.RowsAffected(); err == nil {
			created += int(affected)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return created, nil
}

func ecbRefreshDue(db *sql.DB, projectID string, now time.Time) bool {
	var lastAttempt string
	var failures int
	err := db.QueryRow(`SELECT COALESCE(last_attempt_at,''),failure_count FROM provider_health
        WHERE project_id=? AND connection_id=0 AND provider_slug=?`, projectID, ecbProviderSlug).Scan(&lastAttempt, &failures)
	if err != nil || lastAttempt == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339Nano, lastAttempt)
	if err != nil {
		return true
	}
	interval := 6 * time.Hour
	if failures > 0 {
		interval = 15 * time.Minute
	}
	return now.Sub(last) >= interval
}

func (a *App) refreshECBIfDue(ctx context.Context, app *sdk.AppCtx, force bool) (int, string, error) {
	if !configBool(app, "ecb_bootstrap_enabled", true) {
		return 0, "", nil
	}
	projectID := scopeKey(app, nil)
	if !force && !ecbRefreshDue(app.AppDB(), projectID, time.Now().UTC()) {
		return 0, "", nil
	}
	conn := sdk.PlatformConnection{ID: 0, AppSlug: ecbProviderSlug, Name: ecbProviderName, Status: "public"}
	created, latest, err := syncECBReferenceRates(ctx, app, ecbHistoryURL, ecbClient)
	updateProviderHealth(app.AppDB(), projectID, conn, err == nil, err)
	if err != nil {
		app.Emit("currencies.sync.failed", map[string]any{"provider": ecbProviderSlug, "error": err.Error()})
		return 0, "", err
	}
	app.Emit("currencies.sync.completed", map[string]any{
		"provider": ecbProviderSlug, "observations": created, "latest_effective_date": latest,
	})
	return created, latest, nil
}

func configBool(ctx *sdk.AppCtx, key string, fallback bool) bool {
	if ctx == nil {
		return fallback
	}
	raw := strings.TrimSpace(ctx.Config().Get(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}
