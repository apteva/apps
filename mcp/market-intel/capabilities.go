package main

import (
	"encoding/json"
	"time"
)

// Capability is the provider-neutral contract understood by the intelligence
// layer. Connectors may expose different tool names and payloads; the gateway
// selects them by capability and preserves the original payload.
type Capability string

const (
	CapabilityOdds        Capability = "odds"
	CapabilityEvents      Capability = "events"
	CapabilityTimeseries  Capability = "timeseries"
	CapabilityQuotes      Capability = "quotes"
	CapabilityOrderbook   Capability = "orderbook"
	CapabilityPositions   Capability = "positions"
	CapabilityTrades      Capability = "trades"
	CapabilitySentiment   Capability = "sentiment"
	CapabilityPredictions Capability = "predictions"
	CapabilityDocuments   Capability = "documents"
)

// EvidenceEnvelope is the source-neutral boundary object. Payload stays raw
// so no connector data is lost; normalized consumers use the temporal and
// provenance fields when available.
type EvidenceEnvelope struct {
	Locations  []EvidenceLocation `json:"locations,omitempty"`
	Source     string             `json:"source"`
	Connector  string             `json:"connector"`
	Kind       string             `json:"kind"`
	Entity     string             `json:"entity,omitempty"`
	ObservedAt time.Time          `json:"observed_at"`
	EventTime  *time.Time         `json:"event_time,omitempty"`
	ValidFrom  *time.Time         `json:"valid_from,omitempty"`
	ValidTo    *time.Time         `json:"valid_to,omitempty"`
	Payload    json.RawMessage    `json:"payload"`
	Provenance map[string]any     `json:"provenance,omitempty"`
}

func capabilitiesForQuery(q queryType) []Capability {
	switch q {
	case qOdds:
		return []Capability{CapabilityOdds}
	case qH2H, qRanking, qForm:
		return []Capability{CapabilityEvents}
	case qIndicator, qCryptoPrice:
		return []Capability{CapabilityTimeseries}
	case qMarketPrice:
		return []Capability{CapabilityQuotes}
	case qNews:
		return []Capability{CapabilityEvents, CapabilityDocuments, CapabilitySentiment}
	default:
		return nil
	}
}

func sourceCapabilities() map[string][]string {
	out := map[string]map[string]bool{}
	for _, byQuery := range registry {
		for q, specs := range byQuery {
			for _, s := range specs {
				if out[s.slug] == nil {
					out[s.slug] = map[string]bool{}
				}
				for _, c := range capabilitiesForQuery(q) {
					out[s.slug][string(c)] = true
				}
			}
		}
	}
	rows := map[string][]string{}
	for slug, caps := range out {
		for cap := range caps {
			rows[slug] = append(rows[slug], cap)
		}
		// Stable output for APIs and tests.
		for i := 0; i < len(rows[slug]); i++ {
			for j := i + 1; j < len(rows[slug]); j++ {
				if rows[slug][j] < rows[slug][i] {
					rows[slug][i], rows[slug][j] = rows[slug][j], rows[slug][i]
				}
			}
		}
	}
	return rows
}
