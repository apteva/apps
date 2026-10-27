package main

import (
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

var orderPlacementMu sync.Mutex

func (a *App) toolVenueProfileUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	venue := strings.ToLower(strings.TrimSpace(strArg(args, "venue_slug")))
	class := strings.ToLower(strings.TrimSpace(strArg(args, "asset_class")))
	symbol := canonicalSymbol(strArg(args, "symbol"))
	if symbol == "" {
		symbol = "*"
	}
	p := defaultVenueProfile(venue, class)
	if existing, err := dbVenueProfile(ctx.AppDB(), venue, class, symbol); err == nil && existing.Symbol == symbol {
		p = *existing
	}
	p.VenueSlug, p.AssetClass, p.Symbol, p.Source = venue, class, symbol, "operator"
	setString := func(key string, target *string) {
		if _, ok := args[key]; ok {
			*target = strArg(args, key)
		}
	}
	setFloat := func(key string, target *float64) {
		if _, ok := args[key]; ok {
			*target = anyFloat(args[key])
		}
	}
	setString("status", &p.Status)
	setString("calendar", &p.Calendar)
	setString("session_policy", &p.SessionPolicy)
	setString("fee_currency", &p.FeeCurrency)
	setString("spread_model", &p.SpreadModel)
	setString("slippage_model", &p.SlippageModel)
	setFloat("maker_fee_bps", &p.MakerFeeBps)
	setFloat("taker_fee_bps", &p.TakerFeeBps)
	setFloat("fallback_spread_bps", &p.FallbackSpreadBps)
	setFloat("slippage_bps", &p.SlippageBps)
	setFloat("min_qty", &p.MinQty)
	setFloat("min_notional", &p.MinNotional)
	setFloat("qty_step", &p.QtyStep)
	setFloat("price_tick", &p.PriceTick)
	setFloat("funding_rate_bps", &p.FundingRateBps)
	if _, ok := args["funding_interval_hours"]; ok {
		p.FundingIntervalHours = int(int64Arg(args, "funding_interval_hours", 0))
	}
	if v, ok := args["supports_post_only"].(bool); ok {
		p.SupportsPostOnly = v
	}
	if v, ok := args["supports_reduce_only"].(bool); ok {
		p.SupportsReduceOnly = v
	}
	if err := dbUpsertVenueProfile(ctx.AppDB(), &p); err != nil {
		return nil, err
	}
	emit("venue.profile.changed", map[string]any{"venue_slug": p.VenueSlug, "asset_class": p.AssetClass, "symbol": p.Symbol, "profile": p})
	return map[string]any{"profile": p}, nil
}
