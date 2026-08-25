package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type selectedEdge struct {
	observation RateObservation
	rate        *big.Rat
	inverted    bool
	stale       bool
	warnings    []string
}

func (a *App) selectRate(ctx *sdk.AppCtx, req SelectionRequest) (RateQuote, error) {
	if req.Base == req.Quote {
		q := RateQuote{
			Base: req.Base, Quote: req.Quote, Rate: "1", RateKind: "identity",
			AsOf: req.AsOf.UTC().Format(time.RFC3339Nano), EffectiveAt: req.AsOf.UTC().Format(time.RFC3339Nano),
			EffectiveDate: req.AsOf.UTC().Format("2006-01-02"), Identity: true,
			Path: []RatePathEdge{}, Warnings: []string{},
		}
		q.QuoteID = quoteID(q)
		return q, nil
	}

	if err := trackPair(ctx.AppDB(), req.ProjectID, req.Base, req.Quote); err != nil {
		return RateQuote{}, err
	}
	q, err := a.selectStoredRate(ctx, req)
	if err == nil {
		return q, nil
	}
	if !req.Fetch || !errors.Is(err, errRateMissing) {
		return RateQuote{}, err
	}
	if _, fetchErr := a.fetchPair(ctx, req.ProjectID, req.Base, req.Quote, req.AsOf, ""); fetchErr != nil {
		return RateQuote{}, fmt.Errorf("rate unavailable; provider refresh failed: %w", fetchErr)
	}
	return a.selectStoredRate(ctx, req)
}

var errRateMissing = errors.New("no eligible exchange-rate observation")

func (a *App) selectStoredRate(ctx *sdk.AppCtx, req SelectionRequest) (RateQuote, error) {
	edge, err := selectEdge(ctx, req, req.Base, req.Quote, req.AllowInverse)
	if err == nil {
		q := quoteFromEdges(req, []*selectedEdge{edge})
		q.Rate = ratDecimal(edge.rate)
		if !edge.inverted {
			q.Rate = edge.observation.Rate
		}
		q.RateKind = edge.observation.RateKind
		q.Derived = edge.inverted
		q.QuoteID = quoteID(q)
		return q, nil
	}
	if !req.AllowTriangulation {
		return RateQuote{}, fmt.Errorf("%w for %s/%s at %s", errRateMissing, req.Base, req.Quote, req.AsOf.UTC().Format(time.RFC3339))
	}

	for _, pivot := range pivotCurrencies(ctx) {
		if pivot == req.Base || pivot == req.Quote {
			continue
		}
		left, leftErr := selectEdge(ctx, req, req.Base, pivot, req.AllowInverse)
		if leftErr != nil {
			continue
		}
		right, rightErr := selectEdge(ctx, req, pivot, req.Quote, req.AllowInverse)
		if rightErr != nil {
			continue
		}
		rate := new(big.Rat).Mul(left.rate, right.rate)
		q := quoteFromEdges(req, []*selectedEdge{left, right})
		q.Rate = ratDecimal(rate)
		q.RateKind = "derived"
		q.Derived = true
		q.QuoteID = quoteID(q)
		return q, nil
	}
	return RateQuote{}, fmt.Errorf("%w for %s/%s at %s (direct, inverse, and configured pivots exhausted)",
		errRateMissing, req.Base, req.Quote, req.AsOf.UTC().Format(time.RFC3339))
}

func selectEdge(ctx *sdk.AppCtx, req SelectionRequest, base, quote string, allowInverse bool) (*selectedEdge, error) {
	rows, err := candidateObservations(ctx.AppDB(), req, base, quote)
	if err != nil {
		return nil, err
	}
	if edge := chooseCandidate(ctx, req, rows, false); edge != nil {
		return edge, nil
	}
	if !allowInverse {
		return nil, errRateMissing
	}
	reverse, err := candidateObservations(ctx.AppDB(), req, quote, base)
	if err != nil {
		return nil, err
	}
	edge := chooseCandidate(ctx, req, reverse, true)
	if edge == nil {
		return nil, errRateMissing
	}
	return edge, nil
}

func chooseCandidate(ctx *sdk.AppCtx, req SelectionRequest, rows []RateObservation, inverse bool) *selectedEdge {
	if len(rows) == 0 {
		return nil
	}
	sortCandidates(ctx, rows)
	var staleCandidate *selectedEdge
	for i := range rows {
		r, _, err := parsePositiveDecimal(rows[i].Rate)
		if err != nil {
			continue
		}
		if inverse {
			r.Inv(r)
		}
		effective, err := time.Parse(time.RFC3339Nano, rows[i].EffectiveAt)
		if err != nil {
			continue
		}
		stale := req.MaxAge > 0 && req.AsOf.Sub(effective) > req.MaxAge
		edge := &selectedEdge{observation: rows[i], rate: r, inverted: inverse, stale: stale, warnings: []string{}}
		if inverse {
			edge.warnings = append(edge.warnings, "rate inverted from "+rows[i].Base+"/"+rows[i].Quote)
		}
		if stale {
			edge.warnings = append(edge.warnings, "selected observation exceeds max_age_seconds")
			if staleCandidate == nil {
				staleCandidate = edge
			}
			continue
		}
		edge.warnings = append(edge.warnings, conflictWarnings(rows[i], rows)...)
		return edge
	}
	if req.AllowStale {
		return staleCandidate
	}
	return nil
}

func conflictWarnings(selected RateObservation, rows []RateObservation) []string {
	selectedRate, _, err := parsePositiveDecimal(selected.Rate)
	if err != nil {
		return nil
	}
	seen := map[string]bool{selected.ProviderSlug: true}
	threshold := new(big.Rat).SetFrac64(5, 1000) // 0.5%
	for _, row := range rows {
		if seen[row.ProviderSlug] {
			continue
		}
		if row.EffectiveDate != selected.EffectiveDate {
			continue
		}
		seen[row.ProviderSlug] = true
		other, _, err := parsePositiveDecimal(row.Rate)
		if err != nil {
			continue
		}
		diff := new(big.Rat).Sub(other, selectedRate)
		if diff.Sign() < 0 {
			diff.Neg(diff)
		}
		diff.Quo(diff, selectedRate)
		if diff.Cmp(threshold) > 0 {
			return []string{"provider conflict exceeds 0.5%: " + selected.ProviderSlug + " vs " + row.ProviderSlug}
		}
	}
	return nil
}

func quoteFromEdges(req SelectionRequest, edges []*selectedEdge) RateQuote {
	q := RateQuote{
		Base: req.Base, Quote: req.Quote, AsOf: req.AsOf.UTC().Format(time.RFC3339Nano),
		Path: []RatePathEdge{}, Warnings: []string{},
	}
	var oldest time.Time
	for _, edge := range edges {
		o := edge.observation
		effective, _ := time.Parse(time.RFC3339Nano, o.EffectiveAt)
		if oldest.IsZero() || effective.Before(oldest) {
			oldest = effective
			q.EffectiveAt = o.EffectiveAt
			q.EffectiveDate = o.EffectiveDate
		}
		base, quote := o.Base, o.Quote
		if edge.inverted {
			base, quote = quote, base
		}
		q.Path = append(q.Path, RatePathEdge{
			RateID: o.ID, Base: base, Quote: quote, Rate: ratDecimal(edge.rate), RateKind: o.RateKind,
			Provider: o.ProviderSlug, ConnectionID: o.ConnectionID, ProviderRef: o.ProviderRef,
			EffectiveAt: o.EffectiveAt, EffectiveDate: o.EffectiveDate, ObservedAt: o.ObservedAt,
			Granularity: o.Granularity, AdapterVersion: o.AdapterVersion, Inverted: edge.inverted,
		})
		q.Stale = q.Stale || edge.stale
		q.Warnings = append(q.Warnings, edge.warnings...)
	}
	return q
}

func quoteID(q RateQuote) string {
	canonical := struct {
		Base, Quote, Rate, RateKind string
		Path                        []RatePathEdge
	}{q.Base, q.Quote, q.Rate, q.RateKind, q.Path}
	b, _ := json.Marshal(canonical)
	sum := sha256.Sum256(b)
	return "fxq_" + hex.EncodeToString(sum[:12])
}

func pivotCurrencies(ctx *sdk.AppCtx) []string {
	v := "EUR,USD"
	if ctx != nil && strings.TrimSpace(ctx.Config().Get("pivot_currencies")) != "" {
		v = ctx.Config().Get("pivot_currencies")
	}
	out := []string{}
	for _, code := range splitCSV(v) {
		if c, err := normalizeCode(code); err == nil {
			out = append(out, c)
		}
	}
	return out
}

func convertWithQuote(db *sql.DB, amount int64, from, to, rounding string, quote RateQuote) (map[string]any, error) {
	fromDef, err := getCurrency(db, from)
	if err != nil {
		return nil, err
	}
	toDef, err := getCurrency(db, to)
	if err != nil {
		return nil, err
	}
	if fromDef.MinorUnits == nil || toDef.MinorUnits == nil {
		return nil, errors.New("minor-unit conversion is unavailable for currencies whose ISO minor unit is N.A.")
	}
	rate, _, err := parsePositiveDecimal(quote.Rate)
	if err != nil {
		return nil, err
	}
	converted, rounded, err := convertMinor(amount, *fromDef.MinorUnits, *toDef.MinorUnits, rate, rounding)
	if err != nil {
		return nil, err
	}
	conversionID := conversionQuoteID(quote.QuoteID, amount, converted, *fromDef.MinorUnits, *toDef.MinorUnits, rounding)
	return map[string]any{
		"amount_minor": amount, "from": from, "converted_amount_minor": converted, "to": to,
		"from_minor_units": *fromDef.MinorUnits, "to_minor_units": *toDef.MinorUnits,
		"rounding": rounding, "rounding_occurred": rounded, "rate_snapshot": quote, "conversion_id": conversionID,
	}, nil
}

func conversionQuoteID(quoteID string, amount, converted int64, fromExponent, toExponent int, rounding string) string {
	b, _ := json.Marshal([]any{quoteID, amount, converted, fromExponent, toExponent, rounding})
	sum := sha256.Sum256(b)
	return "fxc_" + hex.EncodeToString(sum[:12])
}
