package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// IntegrationProviderAdapter is the paid-data extension point. An integration
// only needs universe and bars tools returning the normalized DTOs below.
// Vendor credentials remain in Apteva's integration connection, never here.
type IntegrationProviderAdapter struct {
	Slug, VenueName, UniverseTool, BarsTool string
	Client                                  sourceClient
}

func (p *IntegrationProviderAdapter) Name() string  { return p.Slug }
func (p *IntegrationProviderAdapter) Venue() string { return p.VenueName }
func (p *IntegrationProviderAdapter) Universe(_ context.Context, limit int) ([]Instrument, error) {
	raw, ok := p.Client.call(p.Slug, p.UniverseTool, map[string]any{"limit": limit})
	if !ok {
		return nil, fmt.Errorf("integration %s unavailable", p.Slug)
	}
	var env struct {
		Instruments []Instrument `json:"instruments"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Instruments) > 0 {
		return env.Instruments, nil
	}
	var direct []Instrument
	if err := json.Unmarshal(raw, &direct); err != nil {
		return nil, err
	}
	return direct, nil
}
func (p *IntegrationProviderAdapter) Bars(_ context.Context, symbol, interval string, limit int) ([]Candle, error) {
	raw, ok := p.Client.call(p.Slug, p.BarsTool, map[string]any{"symbol": symbol, "interval": interval, "limit": limit})
	if !ok {
		return nil, fmt.Errorf("integration %s unavailable", p.Slug)
	}
	var env struct {
		Bars []Candle `json:"bars"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Bars) > 0 {
		return env.Bars, nil
	}
	var direct []Candle
	if err := json.Unmarshal(raw, &direct); err != nil {
		return nil, err
	}
	return direct, nil
}
