package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Instrument is the normalized identity and trading-hours metadata retained
// alongside provider marks. ProviderSymbol preserves the venue spelling while
// Symbol remains the canonical spelling used throughout the trading app.
type Instrument struct {
	Symbol           string  `json:"symbol"`
	ProviderSymbol   string  `json:"provider_symbol,omitempty"`
	Name             string  `json:"name,omitempty"`
	AssetClass       string  `json:"asset_class"`
	Exchange         string  `json:"exchange"`
	ExchangeTimezone string  `json:"exchange_timezone"`
	Calendar         string  `json:"calendar"`
	BaseCurrency     string  `json:"base_currency,omitempty"`
	QuoteCurrency    string  `json:"quote_currency,omitempty"`
	VolumeUnit       string  `json:"volume_unit"`
	TickSize         float64 `json:"tick_size,omitempty"`
	LotSize          float64 `json:"lot_size,omitempty"`
	Active           bool    `json:"active"`
	ExpiresAt        string  `json:"expires_at,omitempty"`
	Source           string  `json:"source"`
	UpdatedAt        string  `json:"updated_at"`
}

const (
	calendarAlwaysOpen = "24X7"
	calendarUSEquity   = "US_EQUITY"
)

func defaultInstrument(symbol, assetClass, source string, now time.Time) *Instrument {
	symbol = canonicalSymbol(symbol)
	assetClass = strings.ToLower(strings.TrimSpace(assetClass))
	i := &Instrument{
		Symbol:         symbol,
		ProviderSymbol: symbol,
		AssetClass:     assetClass,
		Active:         true,
		Source:         source,
		UpdatedAt:      now.UTC().Format(time.RFC3339),
	}
	switch assetClass {
	case "crypto":
		i.Exchange = "BINANCE"
		i.ExchangeTimezone = "UTC"
		i.Calendar = calendarAlwaysOpen
		i.QuoteCurrency = "USD"
		i.BaseCurrency = strings.TrimSuffix(symbol, "-USD")
		i.VolumeUnit = "quote_currency"
	case "polymarket":
		i.Exchange = "POLYMARKET"
		i.ExchangeTimezone = "UTC"
		i.Calendar = calendarAlwaysOpen
		i.QuoteCurrency = "USD"
		i.VolumeUnit = "quote_currency"
	case "equity", "etf":
		i.Exchange = "US"
		i.ExchangeTimezone = "America/New_York"
		i.Calendar = calendarUSEquity
		i.QuoteCurrency = "USD"
		i.VolumeUnit = "shares"
	default:
		i.Exchange = "UNKNOWN"
		i.ExchangeTimezone = "UTC"
		i.Calendar = calendarAlwaysOpen
		i.VolumeUnit = "units"
	}
	return i
}

func normalizeInstrument(i *Instrument, symbol, assetClass, source string, now time.Time) (*Instrument, error) {
	base := defaultInstrument(symbol, assetClass, source, now)
	if i == nil {
		return base, nil
	}
	if strings.TrimSpace(i.Symbol) != "" {
		base.Symbol = canonicalSymbol(i.Symbol)
	}
	if base.Symbol != canonicalSymbol(symbol) {
		return nil, fmt.Errorf("instrument symbol %q does not match mark symbol %q", base.Symbol, symbol)
	}
	if v := strings.TrimSpace(i.ProviderSymbol); v != "" {
		base.ProviderSymbol = v
	}
	if v := strings.TrimSpace(i.Name); v != "" {
		base.Name = v
	}
	if v := strings.ToLower(strings.TrimSpace(i.AssetClass)); v != "" {
		base.AssetClass = v
	}
	if base.AssetClass != strings.ToLower(strings.TrimSpace(assetClass)) {
		return nil, fmt.Errorf("instrument asset class %q does not match mark asset class %q", base.AssetClass, assetClass)
	}
	if v := strings.ToUpper(strings.TrimSpace(i.Exchange)); v != "" {
		base.Exchange = v
	}
	if v := strings.TrimSpace(i.ExchangeTimezone); v != "" {
		if _, err := time.LoadLocation(v); err != nil {
			return nil, fmt.Errorf("invalid exchange timezone %q: %w", v, err)
		}
		base.ExchangeTimezone = v
	}
	if v := strings.ToUpper(strings.TrimSpace(i.Calendar)); v != "" {
		base.Calendar = v
	}
	if v := strings.ToUpper(strings.TrimSpace(i.BaseCurrency)); v != "" {
		base.BaseCurrency = v
	}
	if v := strings.ToUpper(strings.TrimSpace(i.QuoteCurrency)); v != "" {
		base.QuoteCurrency = v
	}
	if v := strings.ToLower(strings.TrimSpace(i.VolumeUnit)); v != "" {
		base.VolumeUnit = v
	}
	if i.TickSize < 0 || i.LotSize < 0 || !finite(i.TickSize) || !finite(i.LotSize) {
		return nil, errors.New("instrument tick_size and lot_size must be finite and non-negative")
	}
	base.TickSize = i.TickSize
	base.LotSize = i.LotSize
	base.Active = i.Active
	if v := strings.TrimSpace(i.ExpiresAt); v != "" {
		at, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return nil, fmt.Errorf("invalid instrument expiry %q: %w", v, err)
		}
		base.ExpiresAt = at.UTC().Format(time.RFC3339)
	}
	if v := strings.TrimSpace(i.Source); v != "" {
		base.Source = v
	}
	base.UpdatedAt = now.UTC().Format(time.RFC3339)
	return base, nil
}

// normalizeMark is the single live-data quality gate. It gives timestamps
// explicit semantics: provider event time when supplied, otherwise receipt
// time. All persisted timestamps are RFC3339 UTC.
func normalizeMark(source string, mark *Mark, receivedAt time.Time) (*Mark, error) {
	if mark == nil {
		return nil, errors.New("nil market mark")
	}
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	receivedAt = receivedAt.UTC()
	mark.Symbol = canonicalSymbol(mark.Symbol)
	if mark.Symbol == "" {
		return nil, errors.New("market mark symbol is required")
	}
	mark.AssetClass = strings.ToLower(strings.TrimSpace(mark.AssetClass))
	if mark.AssetClass == "" {
		mark.AssetClass = inferAssetClass(mark.Symbol)
	}
	if !finite(mark.Price) || mark.Price <= 0 {
		return nil, fmt.Errorf("invalid price for %s", mark.Symbol)
	}
	if mark.AssetClass == "polymarket" && mark.Price > 1 {
		return nil, fmt.Errorf("invalid YES probability for %s", mark.Symbol)
	}
	for label, value := range map[string]*float64{
		"no_price": mark.NoPrice, "prev_close": mark.PrevClose, "volume_24h": mark.Volume24h,
	} {
		if value == nil {
			continue
		}
		if !finite(*value) || *value < 0 {
			return nil, fmt.Errorf("invalid %s for %s", label, mark.Symbol)
		}
	}
	if mark.NoPrice != nil && *mark.NoPrice > 1 {
		return nil, fmt.Errorf("invalid NO probability for %s", mark.Symbol)
	}

	mark.Source = strings.TrimSpace(source)
	mark.ReceivedAt = receivedAt.Format(time.RFC3339Nano)
	if strings.TrimSpace(mark.MarkedAt) == "" {
		mark.MarkedAt = receivedAt.Format(time.RFC3339Nano)
		mark.TimestampKind = "received"
	} else {
		at, err := time.Parse(time.RFC3339Nano, mark.MarkedAt)
		if err != nil {
			return nil, fmt.Errorf("invalid market timestamp for %s: %w", mark.Symbol, err)
		}
		at = at.UTC()
		if at.After(receivedAt.Add(time.Minute)) {
			return nil, fmt.Errorf("market timestamp for %s is more than one minute in the future", mark.Symbol)
		}
		mark.MarkedAt = at.Format(time.RFC3339Nano)
		if strings.TrimSpace(mark.TimestampKind) == "" {
			mark.TimestampKind = "exchange"
		}
	}
	if mark.Volume24h != nil && strings.TrimSpace(mark.VolumeUnit) == "" {
		switch mark.AssetClass {
		case "equity", "etf":
			mark.VolumeUnit = "shares"
		default:
			mark.VolumeUnit = "quote_currency"
		}
	}
	instrument, err := normalizeInstrument(mark.Instrument, mark.Symbol, mark.AssetClass, source, receivedAt)
	if err != nil {
		return nil, err
	}
	if mark.VolumeUnit != "" {
		instrument.VolumeUnit = mark.VolumeUnit
	} else {
		mark.VolumeUnit = instrument.VolumeUnit
	}
	mark.Instrument = instrument
	return mark, nil
}

// normalizeBars validates provider OHLCV at the ingestion boundary, fills
// absent OHLC values from close, removes duplicate timestamps, and sorts the
// result. It rejects contradictory candles instead of silently storing them.
func normalizeBars(symbol, source string, bars []Bar) ([]Bar, error) {
	byTime := make(map[int64]Bar, len(bars))
	polymarket := inferAssetClass(symbol) == "polymarket"
	for _, bar := range bars {
		if bar.T <= 0 {
			return nil, fmt.Errorf("%s history for %s contains an invalid timestamp", source, symbol)
		}
		if polymarket {
			if !finite(bar.Yes) || bar.Yes <= 0 || bar.Yes >= 1 {
				return nil, fmt.Errorf("%s history for %s contains an invalid probability at %d", source, symbol, bar.T)
			}
			byTime[bar.T] = bar
			continue
		}
		if !finite(bar.C) || bar.C <= 0 {
			return nil, fmt.Errorf("%s history for %s contains an invalid close at %d", source, symbol, bar.T)
		}
		if bar.O == 0 {
			bar.O = bar.C
		}
		if bar.H == 0 {
			bar.H = math.Max(bar.O, bar.C)
		}
		if bar.L == 0 {
			bar.L = math.Min(bar.O, bar.C)
		}
		if !finite(bar.O) || !finite(bar.H) || !finite(bar.L) || !finite(bar.V) ||
			bar.O <= 0 || bar.H <= 0 || bar.L <= 0 || bar.V < 0 {
			return nil, fmt.Errorf("%s history for %s contains non-finite or non-positive OHLCV at %d", source, symbol, bar.T)
		}
		if bar.H < math.Max(bar.O, bar.C) || bar.L > math.Min(bar.O, bar.C) || bar.L > bar.H {
			return nil, fmt.Errorf("%s history for %s contains an inconsistent candle at %d", source, symbol, bar.T)
		}
		byTime[bar.T] = bar
	}
	out := make([]Bar, 0, len(byTime))
	for _, bar := range byTime {
		out = append(out, bar)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out, nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func historicalVolumeUnit(source, assetClass string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "binance-public":
		return "base_asset"
	case "yahoo-finance", "alpaca-market-data":
		return "shares"
	}
	if strings.EqualFold(assetClass, "polymarket") {
		return "contracts"
	}
	return "units"
}
