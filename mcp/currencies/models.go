package main

import "time"

type CurrencyDefinition struct {
	Code        string `json:"code"`
	NumericCode string `json:"numeric_code,omitempty"`
	Name        string `json:"name"`
	MinorUnits  *int   `json:"minor_units"`
	Kind        string `json:"kind"`
	Active      bool   `json:"active"`
	DataVersion string `json:"data_version"`
}

type RateObservation struct {
	ID               int64    `json:"rate_id"`
	ProjectID        string   `json:"-"`
	Base             string   `json:"base"`
	Quote            string   `json:"quote"`
	Rate             string   `json:"rate"`
	RateKind         string   `json:"rate_kind"`
	EffectiveAt      string   `json:"effective_at"`
	EffectiveDate    string   `json:"effective_date"`
	Granularity      string   `json:"granularity"`
	ObservedAt       string   `json:"observed_at"`
	IngestedAt       string   `json:"ingested_at"`
	ProviderSlug     string   `json:"provider"`
	ConnectionID     *int64   `json:"connection_id,omitempty"`
	ProviderRef      string   `json:"provider_ref,omitempty"`
	OriginalBase     string   `json:"original_base"`
	OriginalQuote    string   `json:"original_quote"`
	PayloadHash      string   `json:"payload_hash,omitempty"`
	AdapterVersion   string   `json:"adapter_version"`
	QualityFlags     []string `json:"quality_flags"`
	SupersedesRateID *int64   `json:"supersedes_rate_id,omitempty"`
}

type ObservationInput struct {
	Base           string
	Quote          string
	Rate           string
	RateKind       string
	EffectiveAt    time.Time
	EffectiveDate  string
	Granularity    string
	ObservedAt     time.Time
	ProviderSlug   string
	ConnectionID   int64
	ProviderRef    string
	OriginalBase   string
	OriginalQuote  string
	PayloadHash    string
	AdapterVersion string
	QualityFlags   []string
}

type RatePathEdge struct {
	RateID         int64  `json:"rate_id"`
	Base           string `json:"base"`
	Quote          string `json:"quote"`
	Rate           string `json:"rate"`
	RateKind       string `json:"rate_kind"`
	Provider       string `json:"provider"`
	ConnectionID   *int64 `json:"connection_id,omitempty"`
	ProviderRef    string `json:"provider_ref,omitempty"`
	EffectiveAt    string `json:"effective_at"`
	EffectiveDate  string `json:"effective_date"`
	ObservedAt     string `json:"observed_at"`
	Granularity    string `json:"granularity"`
	AdapterVersion string `json:"adapter_version"`
	Inverted       bool   `json:"inverted"`
}

type RateQuote struct {
	QuoteID       string         `json:"quote_id"`
	Base          string         `json:"base"`
	Quote         string         `json:"quote"`
	Rate          string         `json:"rate"`
	RateKind      string         `json:"rate_kind"`
	AsOf          string         `json:"as_of"`
	EffectiveAt   string         `json:"effective_at"`
	EffectiveDate string         `json:"effective_date"`
	Derived       bool           `json:"derived"`
	Identity      bool           `json:"identity"`
	Stale         bool           `json:"stale"`
	Path          []RatePathEdge `json:"path"`
	Warnings      []string       `json:"warnings"`
}

type SelectionRequest struct {
	ProjectID          string
	Base               string
	Quote              string
	AsOf               time.Time
	Selection          string
	RateKinds          []string
	Providers          []string
	MaxAge             time.Duration
	AllowInverse       bool
	AllowTriangulation bool
	AllowStale         bool
	Fetch              bool
}

type ProviderStatus struct {
	ConnectionID  int64  `json:"connection_id"`
	Provider      string `json:"provider"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	Priority      int    `json:"priority"`
	Enabled       bool   `json:"enabled"`
	LastAttemptAt string `json:"last_attempt_at,omitempty"`
	LastSuccessAt string `json:"last_success_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	FailureCount  int    `json:"failure_count"`
}
