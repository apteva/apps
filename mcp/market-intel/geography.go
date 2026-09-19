package main

import (
	"encoding/json"
	"fmt"
	"math"
)

// EvidenceLocation describes a relationship, not merely a dot. Coordinates
// must come from a source or an explicitly attributed enrichment, never a
// guess based on the source's country. Stored in payload.locations so existing
// evidence databases and arbitrary adapter payloads remain compatible.
type EvidenceLocation struct {
	Latitude     *float64 `json:"lat"`
	Longitude    *float64 `json:"lon"`
	Label        string   `json:"label,omitempty"`
	Country      string   `json:"country,omitempty"`
	Region       string   `json:"region,omitempty"`
	Relationship string   `json:"relationship"`
	Precision    string   `json:"precision"`
	Confidence   *float64 `json:"confidence,omitempty"`
	Provenance   string   `json:"provenance"`
}

func evidenceLocations(payload json.RawMessage) ([]EvidenceLocation, error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(payload, &obj) != nil {
		return nil, nil
	}
	raw, exists := obj["locations"]
	if !exists {
		return nil, nil
	}
	var locations []EvidenceLocation
	if err := json.Unmarshal(raw, &locations); err != nil {
		return nil, fmt.Errorf("locations: %w", err)
	}
	if len(locations) > 100 {
		return nil, fmt.Errorf("at most 100 locations per evidence item")
	}
	for i, loc := range locations {
		if loc.Latitude == nil || loc.Longitude == nil || math.IsNaN(*loc.Latitude) || math.IsNaN(*loc.Longitude) || math.Abs(*loc.Latitude) > 90 || math.Abs(*loc.Longitude) > 180 {
			return nil, fmt.Errorf("location %d requires valid lat/lon", i)
		}
		if loc.Relationship == "" || loc.Precision == "" || loc.Provenance == "" {
			return nil, fmt.Errorf("location %d requires relationship, precision and provenance", i)
		}
		if loc.Confidence != nil && (*loc.Confidence < 0 || *loc.Confidence > 1) {
			return nil, fmt.Errorf("location %d confidence must be between 0 and 1", i)
		}
	}
	return locations, nil
}
