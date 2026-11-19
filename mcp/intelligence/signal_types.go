package main

import (
	"context"
	"time"
)

// MarketDataProvider is the stable boundary between signal logic and data
// vendors. Public sources and paid integrations must produce the same bars and
// executable quote shape; the scanner never contains vendor-specific logic.
type MarketDataProvider interface {
	Name() string
	Venue() string
	Universe(context.Context, int) ([]Instrument, error)
	Bars(context.Context, string, string, int) ([]Candle, error)
}

type Instrument struct {
	Symbol         string  `json:"symbol"`
	Canonical      string  `json:"canonical_symbol"`
	AssetClass     string  `json:"asset_class"`
	Bid            float64 `json:"bid"`
	Ask            float64 `json:"ask"`
	Last           float64 `json:"last"`
	QuoteVolume24h float64 `json:"quote_volume_24h"`
	QuoteTime      int64   `json:"quote_time,omitempty"`
}

type Candle struct {
	Time   int64   `json:"time"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
	Closed bool    `json:"closed"`
}

type SignalFeatures struct {
	Price         float64 `json:"price"`
	EMA20         float64 `json:"ema_20"`
	EMA50         float64 `json:"ema_50"`
	RSI14         float64 `json:"rsi_14"`
	ATR14         float64 `json:"atr_14"`
	Return1Bps    float64 `json:"return_1_bps"`
	Return6Bps    float64 `json:"return_6_bps"`
	Return24Bps   float64 `json:"return_24_bps"`
	VolatilityBps float64 `json:"volatility_bps"`
	VolumeZ20     float64 `json:"volume_z_20"`
	PriceZ20      float64 `json:"price_z_20"`
	PriorHigh20   float64 `json:"prior_high_20"`
	PriorLow20    float64 `json:"prior_low_20"`
	TrendStrength float64 `json:"trend_strength"`
}

type SignalCandidate struct {
	Strategy        string
	Direction       string
	Score           float64
	Confidence      float64
	ExpectedMoveBps float64
	Rationale       []string
}

type SignalOpportunity struct {
	ID                int64          `json:"id,omitempty"`
	PublicID          string         `json:"public_id"`
	ProjectID         string         `json:"project_id,omitempty"`
	FeedID            int64          `json:"feed_id"`
	FeedSlug          string         `json:"feed_slug,omitempty"`
	RunID             int64          `json:"run_id,omitempty"`
	Provider          string         `json:"provider"`
	Venue             string         `json:"venue"`
	ProviderSymbol    string         `json:"provider_symbol"`
	CanonicalSymbol   string         `json:"canonical_symbol"`
	AssetClass        string         `json:"asset_class"`
	Strategy          string         `json:"strategy"`
	Direction         string         `json:"direction"`
	Interval          string         `json:"interval"`
	SignalTime        int64          `json:"signal_time"`
	GeneratedAt       time.Time      `json:"generated_at"`
	ValidUntil        time.Time      `json:"valid_until"`
	EntryPrice        float64        `json:"entry_price"`
	BidPrice          float64        `json:"bid_price"`
	AskPrice          float64        `json:"ask_price"`
	StopLoss          float64        `json:"stop_loss"`
	Target1           float64        `json:"target_1"`
	Target2           float64        `json:"target_2"`
	Confidence        float64        `json:"confidence"`
	Score             float64        `json:"score"`
	ExpectedMoveBps   float64        `json:"expected_move_bps"`
	GrossEdgeBps      float64        `json:"gross_edge_bps"`
	CostBps           float64        `json:"cost_bps"`
	NetEdgeBps        float64        `json:"net_edge_bps"`
	SpreadBps         float64        `json:"spread_bps"`
	QuoteVolume24h    float64        `json:"quote_volume_24h"`
	Rationale         []string       `json:"rationale"`
	Features          SignalFeatures `json:"features"`
	Provenance        map[string]any `json:"provenance"`
	Status            string         `json:"status"`
	EvaluationPrice   *float64       `json:"evaluation_price,omitempty"`
	RealizedReturnBps *float64       `json:"realized_return_bps,omitempty"`
	Outcome           string         `json:"outcome,omitempty"`
	EvaluatedAt       *time.Time     `json:"evaluated_at,omitempty"`
}

type SignalFeed struct {
	ID            int64   `json:"id"`
	Slug          string  `json:"slug"`
	Name          string  `json:"name"`
	Description   string  `json:"description"`
	AssetClass    string  `json:"asset_class"`
	Interval      string  `json:"interval"`
	MinConfidence float64 `json:"min_confidence"`
	MinNetEdgeBps float64 `json:"min_net_edge_bps"`
	Visibility    string  `json:"visibility"`
	DelaySeconds  int     `json:"delay_seconds"`
	Status        string  `json:"status"`
}
