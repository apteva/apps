package main

import (
	"math"
	"strings"
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
	MinQty           float64 `json:"min_qty,omitempty"`
	MinNotional      float64 `json:"min_notional,omitempty"`
	Active           bool    `json:"active"`
	ExpiresAt        string  `json:"expires_at,omitempty"`
	Source           string  `json:"source"`
	UpdatedAt        string  `json:"updated_at"`
}

const (
	calendarAlwaysOpen = "24X7"
	calendarUSEquity   = "US_EQUITY"
)

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
