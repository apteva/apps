package main

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var ecbDataURL = "https://data-api.ecb.europa.eu/service/data/EXR/"

// Bounded historical ECB windows complement the rolling 90-day feed. This
// fetches observations, not converted amounts, so consumers retain their own
// accounting/rounding policy.
func syncECBHistory(ctx context.Context, app *sdk.AppCtx, base, quote, from, to string) ([]RateObservation, error) {
	start, e := time.Parse("2006-01-02", from)
	if e != nil {
		return nil, errors.New("from must be YYYY-MM-DD")
	}
	end, e := time.Parse("2006-01-02", to)
	if e != nil || end.Before(start) || end.Sub(start) > 31*24*time.Hour {
		return nil, errors.New("historical ECB window must be ordered and at most 31 days")
	}
	if start.Before(time.Date(1999, 1, 4, 0, 0, 0, 0, time.UTC)) || end.After(time.Now().UTC().Add(24*time.Hour)) {
		return nil, errors.New("ECB dates must be between 1999-01-04 and today")
	}
	base, e = normalizeCode(base)
	if e != nil {
		return nil, e
	}
	quote, e = normalizeCode(quote)
	if e != nil {
		return nil, e
	}
	if base == quote {
		return nil, errors.New("identity pairs need no reference rates")
	}
	codes := []string{}
	for _, c := range []string{base, quote} {
		if c != "EUR" {
			codes = append(codes, c)
		}
	}
	query := url.Values{"startPeriod": {from}, "endPeriod": {to}, "format": {"csvdata"}}
	endpoint := ecbDataURL + "D." + strings.Join(codes, "+") + ".EUR.SP00.A?" + query.Encode()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Accept", "text/csv")
	resp, e := ecbClient.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ECB historical rates HTTP %d", resp.StatusCode)
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, ecbMaxBodyBytes+1))
	if e != nil {
		return nil, e
	}
	if len(raw) > ecbMaxBodyBytes {
		return nil, errors.New("ECB historical response exceeds 2 MiB")
	}
	inputs, e := parseECBHistoryCSV(raw, endpoint, time.Now().UTC(), codes, from, to)
	if e != nil {
		return nil, e
	}
	project := scopeKey(app, nil)
	if project == "" {
		return nil, errors.New("project context required")
	}
	if _, e = insertECBObservations(ctx, app.AppDB(), project, inputs); e != nil {
		return nil, e
	}
	out := []RateObservation{}
	for _, c := range codes {
		rows, err := historyObservations(app.AppDB(), project, "EUR", c, start.Format(time.RFC3339), end.AddDate(0, 0, 1).Format(time.RFC3339), []string{ecbProviderSlug}, []string{"reference"}, 1000)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}
func parseECBHistoryCSV(raw []byte, source string, observed time.Time, codes []string, from, to string) ([]ObservationInput, error) {
	reader := csv.NewReader(strings.NewReader(string(raw)))
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	cols := map[string]int{}
	for i, h := range header {
		cols[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i
	}
	for _, key := range []string{"CURRENCY", "CURRENCY_DENOM", "TIME_PERIOD", "OBS_VALUE"} {
		if _, ok := cols[key]; !ok {
			return nil, fmt.Errorf("ECB CSV missing %s", key)
		}
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		return nil, err
	}
	out := []ObservationInput{}
	allowed := map[string]bool{}
	for _, c := range codes {
		allowed[c] = true
	}
	for {
		row, e := reader.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		c := row[cols["CURRENCY"]]
		day := row[cols["TIME_PERIOD"]]
		if !allowed[c] || row[cols["CURRENCY_DENOM"]] != "EUR" || day < from || day > to {
			return nil, errors.New("ECB historical response contains an unexpected pair/date")
		}
		date, e := time.Parse("2006-01-02", day)
		if e != nil {
			return nil, e
		}
		_, rate, e := parsePositiveDecimal(row[cols["OBS_VALUE"]])
		if e != nil {
			return nil, e
		}
		effective := time.Date(date.Year(), date.Month(), date.Day(), 16, 0, 0, 0, loc)
		out = append(out, ObservationInput{Base: "EUR", Quote: c, Rate: rate, RateKind: "reference", EffectiveAt: effective, EffectiveDate: day, Granularity: "day", ObservedAt: observed, ProviderSlug: ecbProviderSlug, ProviderRef: source + "#" + day, OriginalBase: "EUR", OriginalQuote: c, PayloadHash: hash, AdapterVersion: "ecb-exr-csv-v1", QualityFlags: []string{"official_reference_rate", "information_only"}})
		if len(out) > 1000 {
			return nil, errors.New("ECB response contains too many observations")
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no ECB historical observations in requested window")
	}
	return out, nil
}
