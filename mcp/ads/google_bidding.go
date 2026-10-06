package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Campaign.campaign_bidding_strategy is a protobuf oneof. Every entry below is
// a real field in that oneof — a name outside it fails the whole mutate with
// "Unknown name ... at 'operations[0].create'". In particular there is no
// maximizeClicks field: Maximize Clicks is TargetSpend, which is exactly the
// mistake v0.1.47 shipped.

const (
	bidManualCPC            = "manualCpc"
	bidMaximizeConversions  = "maximizeConversions"
	bidMaximizeConvValue    = "maximizeConversionValue"
	bidTargetSpend          = "targetSpend"
	bidTargetImpressionShr  = "targetImpressionShare"
	defaultImpressionShare  = 650000 // 65%, in micros
	defaultImpressionAnchor = "ANYWHERE_ON_PAGE"
)

// googleObjectiveBidding maps the unified objective onto the Google strategy
// expressing the same goal. Sales optimises for value, leads for conversion
// count, traffic for clicks within budget, awareness for share of impressions.
var googleObjectiveBidding = map[string]string{
	"sales":     bidMaximizeConvValue,
	"leads":     bidMaximizeConversions,
	"traffic":   bidTargetSpend,
	"awareness": bidTargetImpressionShr,
}

// googleBidStrategyNames lets a caller name a Google strategy directly instead
// of inferring one from a Meta-shaped objective. Smart Bidding performs badly
// on an account with no conversion history, so a new advertiser needs to be
// able to start on Manual CPC or Maximize Clicks and graduate later.
var googleBidStrategyNames = map[string]string{
	"manual_cpc":                bidManualCPC,
	"maximize_clicks":           bidTargetSpend, // Google's UI name for TargetSpend
	"target_spend":              bidTargetSpend,
	"maximize_conversions":      bidMaximizeConversions,
	"target_cpa":                bidMaximizeConversions, // modern tCPA is a maximizeConversions target
	"maximize_conversion_value": bidMaximizeConvValue,
	"target_roas":               bidMaximizeConvValue,
	"target_impression_share":   bidTargetImpressionShr,
}

// Meta's bid_strategy vocabulary. Accepted on Meta, meaningless on Google.
var metaBidStrategyNames = map[string]bool{
	"lowest_cost": true, "cost_cap": true, "bid_cap": true,
	"lowest_cost_with_bid_cap": true,
}

// targetImpressionShare is a Search-only strategy; Performance Max accepts only
// the two conversion strategies.
var googleChannelBidding = map[string][]string{
	"SEARCH":          {bidManualCPC, bidMaximizeConversions, bidMaximizeConvValue, bidTargetSpend, bidTargetImpressionShr},
	"PERFORMANCE_MAX": {bidMaximizeConversions, bidMaximizeConvValue},
	"SHOPPING":        {bidManualCPC, bidMaximizeConversions, bidMaximizeConvValue, bidTargetSpend},
	"DISPLAY":         {bidManualCPC, bidMaximizeConversions, bidMaximizeConvValue, bidTargetSpend},
	"VIDEO":           {bidMaximizeConversions, bidMaximizeConvValue, bidTargetSpend},
	"DEMAND_GEN":      {bidMaximizeConversions, bidMaximizeConvValue, bidTargetSpend},
}

func floatArg(args map[string]any, key string) float64 {
	switch typed := args[key].(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

// Google Ads serializes int64 fields as JSON strings, unlike the X/Reddit
// adapters which send numeric micros.
func googleMicros(cents int) string { return strconv.Itoa(cents * 10000) }

// googleBiddingField resolves the strategy for one campaign: an explicit
// Google bid_strategy wins, otherwise the objective decides.
func googleBiddingField(args map[string]any, channelType string) (string, error) {
	requested := strings.TrimSpace(strings.ToLower(stringArgAny(args, "bid_strategy")))
	objective := strings.TrimSpace(strings.ToLower(stringArgAny(args, "objective")))

	field := ""
	switch {
	case requested == "":
	case googleBidStrategyNames[requested] != "":
		field = googleBidStrategyNames[requested]
	case metaBidStrategyNames[requested]:
		return "", fmt.Errorf(
			"bid_strategy=%s is Meta vocabulary and has no Google equivalent; use manual_cpc, maximize_clicks, "+
				"target_spend, maximize_conversions, target_cpa, maximize_conversion_value, target_roas or "+
				"target_impression_share, or omit bid_strategy and let objective choose", requested)
	default:
		return "", fmt.Errorf("unknown bid_strategy: %s", requested)
	}

	if field == "" {
		mapped, ok := googleObjectiveBidding[objective]
		if !ok {
			return "", fmt.Errorf(
				"objective=%s has no Google Ads bidding strategy; use sales, leads, traffic or awareness, "+
					"or set bid_strategy explicitly", objective)
		}
		field = mapped
	}

	allowed, known := googleChannelBidding[channelType]
	if !known {
		return field, nil
	}
	for _, candidate := range allowed {
		if candidate == field {
			return field, nil
		}
	}
	return "", fmt.Errorf("bidding strategy %s is not valid for a %s campaign; supported here: %s",
		field, channelType, strings.Join(allowed, ", "))
}

// googleBiddingPayload builds the strategy body. targetImpressionShare has
// required subfields, so it is populated rather than sent empty.
func googleBiddingPayload(field string, args map[string]any) map[string]any {
	payload := map[string]any{}
	ceiling := intArg(args, "cpc_bid_ceiling_cents", 0)
	switch field {
	case bidMaximizeConvValue:
		if roas := floatArg(args, "target_roas"); roas > 0 {
			payload["targetRoas"] = roas
		}
	case bidMaximizeConversions:
		if cents := intArg(args, "target_cpa_cents", 0); cents > 0 {
			payload["targetCpaMicros"] = googleMicros(cents)
		}
	case bidTargetSpend:
		if ceiling > 0 {
			payload["cpcBidCeilingMicros"] = googleMicros(ceiling)
		}
	case bidTargetImpressionShr:
		location := strings.TrimSpace(strings.ToUpper(stringArgAny(args, "impression_share_location")))
		if location == "" {
			location = defaultImpressionAnchor
		}
		payload["location"] = location
		share := defaultImpressionShare
		if percent := floatArg(args, "impression_share_percent"); percent > 0 && percent <= 100 {
			share = int(percent * 10000)
		}
		payload["locationFractionMicros"] = strconv.Itoa(share)
		if ceiling > 0 {
			payload["cpcBidCeilingMicros"] = googleMicros(ceiling)
		}
	case bidManualCPC:
		payload["enhancedCpcEnabled"] = false
	}
	return payload
}

// EU political advertising declaration. Required on campaign create since
// Google Ads API v23; omitting it fails with fieldError REQUIRED.
func googleEUPoliticalDeclaration(args map[string]any) string {
	if boolArgDefault(args, "contains_eu_political_advertising", false) {
		return "CONTAINS_EU_POLITICAL_ADVERTISING"
	}
	return "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING"
}

// Google's protobuf JSON parser accepts either the proto field name or its
// json_name, but rejects a payload that carries both for one field. A caller
// setting a field through platform_options in snake_case would otherwise
// collide with the camelCase value set here, so collapse to one spelling and
// let the caller's explicit value win.
func collapseGoogleFieldSpellings(campaign map[string]any) {
	pairs := map[string]string{
		"containsEuPoliticalAdvertising": "contains_eu_political_advertising",
		"advertisingChannelType":         "advertising_channel_type",
		"biddingStrategy":                "bidding_strategy",
		"startDate":                      "start_date",
		"endDate":                        "end_date",
	}
	for camel, snake := range pairs {
		value, present := campaign[snake]
		if !present {
			continue
		}
		campaign[camel] = value
		delete(campaign, snake)
	}
}
