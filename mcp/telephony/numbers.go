package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const numberOfferTTL = 10 * time.Minute

type numberSearchRequest struct {
	Countries  []string
	NumberType string
	Features   []string
	AreaCode   string
	Pattern    string
	Limit      int
}

type numberOffer struct {
	ConfirmationToken  string   `json:"confirmation_token,omitempty"`
	ExpiresAt          string   `json:"expires_at,omitempty"`
	Provider           string   `json:"provider"`
	PhoneNumber        string   `json:"phone_number"`
	Country            string   `json:"country"`
	NumberType         string   `json:"number_type"`
	FriendlyName       string   `json:"friendly_name,omitempty"`
	Locality           string   `json:"locality,omitempty"`
	Region             string   `json:"region,omitempty"`
	Features           []string `json:"features,omitempty"`
	MonthlyPrice       string   `json:"monthly_price,omitempty"`
	UpfrontPrice       string   `json:"upfront_price,omitempty"`
	InboundPrice       string   `json:"inbound_price,omitempty"`
	Currency           string   `json:"currency,omitempty"`
	AddressRequirement string   `json:"address_requirement,omitempty"`
	RequirementsMet    *bool    `json:"requirements_met,omitempty"`
	PurchaseReady      bool     `json:"purchase_ready"`
	PurchaseBlocker    string   `json:"purchase_blocker,omitempty"`
}

type numberCountryResult struct {
	Country  string        `json:"country"`
	Count    int           `json:"count"`
	Offers   []numberOffer `json:"offers"`
	Warnings []string      `json:"warnings,omitempty"`
	Error    string        `json:"error,omitempty"`
}

type numberPurchaseIntent struct {
	Token               string
	ProjectID           string
	Provider            string
	CarrierConnectionID int64
	Country             string
	PhoneNumber         string
	NumberType          string
	MonthlyPrice        string
	UpfrontPrice        string
	InboundPrice        string
	Currency            string
	Status              string
	ResponseJSON        string
	ErrorMessage        string
	ExpiresAt           time.Time
}

type numberProvider struct {
	Slug     string
	ConnID   int64
	Fields   map[string]string
	Search   bool
	Purchase bool
	Types    []string
	Reason   string
}

func (a *App) numberProviderFor(ctx *sdk.AppCtx) (*numberProvider, error) {
	bound := ctx.IntegrationFor("carrier")
	if bound == nil {
		return nil, errors.New("no carrier bound")
	}
	creds, err := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("read carrier credentials: %w", err)
	}
	slug := strings.ToLower(strings.TrimSpace(creds.Slug))
	if slug == "" {
		slug = strings.ToLower(strings.TrimSpace(bound.AppSlug))
	}
	p := &numberProvider{Slug: slug, ConnID: bound.ConnectionID, Fields: creds.Fields}
	switch slug {
	case "twilio":
		p.Search, p.Purchase = true, true
		p.Types = []string{"local", "toll_free"}
	case "telnyx":
		p.Search, p.Purchase = true, true
		p.Types = []string{"local", "mobile", "national", "toll_free"}
	case "vonage":
		p.Search, p.Purchase = true, true
		p.Types = []string{"local", "mobile", "toll_free"}
	case "plivo":
		p.Search, p.Purchase = true, true
		p.Types = []string{"local", "mobile", "national", "toll_free"}
	case "signalwire":
		p.Search = true
		p.Types = []string{"local", "toll_free"}
		p.Reason = "SignalWire inventory is searchable, but purchase is disabled because its inventory API does not return a verifiable price quote"
	default:
		p.Reason = "phone-number inventory is not implemented for this carrier"
	}
	return p, nil
}

func parseNumberSearchRequest(args map[string]any) (numberSearchRequest, error) {
	request := numberSearchRequest{
		NumberType: strings.ToLower(strings.TrimSpace(strArg(args, "number_type", "local"))),
		AreaCode:   strings.TrimSpace(strArg(args, "area_code", "")),
		Pattern:    strings.TrimSpace(strArg(args, "pattern", "")),
		Limit:      intArg(args, "limit", 10),
	}
	if request.NumberType == "toll-free" || request.NumberType == "tollfree" {
		request.NumberType = "toll_free"
	}
	validTypes := map[string]bool{"any": true, "local": true, "mobile": true, "national": true, "toll_free": true}
	if !validTypes[request.NumberType] {
		return request, errors.New("number_type must be any, local, mobile, national, or toll_free")
	}
	if request.Limit < 1 || request.Limit > 20 {
		return request, errors.New("limit must be between 1 and 20 per country")
	}
	if len(request.AreaCode) > 12 || !digitsOnly(request.AreaCode) {
		return request, errors.New("area_code must contain at most 12 digits")
	}
	if len(request.Pattern) > 32 || !numberPatternSafe(request.Pattern) {
		return request, errors.New("pattern may contain only digits, +, *, #, spaces, parentheses, and hyphens")
	}

	seenCountries := map[string]bool{}
	addCountry := func(country string) error {
		country = strings.ToUpper(strings.TrimSpace(country))
		if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
			return fmt.Errorf("invalid ISO country code %q", country)
		}
		if !seenCountries[country] {
			seenCountries[country] = true
			request.Countries = append(request.Countries, country)
		}
		return nil
	}
	if country := strArg(args, "country", ""); country != "" {
		if err := addCountry(country); err != nil {
			return request, err
		}
	}
	for _, country := range stringListArg(args, "countries") {
		if err := addCountry(country); err != nil {
			return request, err
		}
	}
	if len(request.Countries) == 0 {
		return request, errors.New("country or countries is required")
	}
	if len(request.Countries) > 30 {
		return request, errors.New("at most 30 countries may be searched at once")
	}

	seenFeatures := map[string]bool{}
	for _, feature := range stringListArg(args, "features") {
		feature = strings.ToLower(strings.TrimSpace(feature))
		if feature != "voice" && feature != "sms" && feature != "mms" {
			return request, fmt.Errorf("unsupported feature %q", feature)
		}
		if !seenFeatures[feature] {
			seenFeatures[feature] = true
			request.Features = append(request.Features, feature)
		}
	}
	if len(request.Features) == 0 {
		request.Features = []string{"voice"}
	}
	return request, nil
}

func (a *App) searchNumberInventory(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	request, err := parseNumberSearchRequest(args)
	if err != nil {
		return nil, err
	}
	projectID := currentProject(ctx)
	if projectID == "" {
		return nil, errors.New("project context required for number search")
	}
	provider, err := a.numberProviderFor(ctx)
	if err != nil {
		return nil, err
	}
	base := map[string]any{
		"provider":               provider.Slug,
		"supported":              provider.Search,
		"purchase_supported":     provider.Purchase,
		"supported_number_types": provider.Types,
		"number_type":            request.NumberType,
	}
	if provider.Reason != "" {
		base["note"] = provider.Reason
	}
	if !provider.Search {
		base["reason"] = provider.Reason
		base["countries"] = []numberCountryResult{}
		base["offers"] = []numberOffer{}
		return base, nil
	}
	if request.NumberType != "any" && !containsString(provider.Types, request.NumberType) {
		return nil, fmt.Errorf("provider %s does not support %s searches; supported types: %s",
			provider.Slug, request.NumberType, strings.Join(provider.Types, ", "))
	}

	expiresAt := time.Now().UTC().Add(numberOfferTTL)
	results := make([]numberCountryResult, 0, len(request.Countries))
	allOffers := make([]numberOffer, 0)
	for _, country := range request.Countries {
		offers, warnings, searchErr := a.searchProviderCountry(ctx, provider, request, country)
		result := numberCountryResult{Country: country, Offers: []numberOffer{}, Warnings: warnings}
		if searchErr != nil {
			result.Error = searchErr.Error()
			results = append(results, result)
			continue
		}
		if len(offers) > request.Limit {
			offers = offers[:request.Limit]
		}
		for i := range offers {
			offers[i].Provider = provider.Slug
			offers[i].Country = country
			if !validE164(offers[i].PhoneNumber) {
				offers[i].PurchaseBlocker = "provider returned an invalid phone number"
			} else if offers[i].MonthlyPrice == "" {
				offers[i].PurchaseBlocker = "provider did not return a monthly price"
			} else if offers[i].Currency == "" {
				offers[i].PurchaseBlocker = "provider did not return the quote currency"
			} else if offers[i].RequirementsMet != nil && !*offers[i].RequirementsMet {
				offers[i].PurchaseBlocker = "provider regulatory requirements are not met"
			} else if provider.Purchase {
				token := newSecret()
				intent := numberPurchaseIntent{
					Token: token, ProjectID: projectID, Provider: provider.Slug,
					CarrierConnectionID: provider.ConnID, Country: country,
					PhoneNumber: offers[i].PhoneNumber, NumberType: offers[i].NumberType,
					MonthlyPrice: offers[i].MonthlyPrice, UpfrontPrice: offers[i].UpfrontPrice,
					InboundPrice: offers[i].InboundPrice, Currency: offers[i].Currency,
					Status: "quoted", ExpiresAt: expiresAt,
				}
				if err := dbNumberPurchaseIntentInsert(ctx.AppDB(), intent); err != nil {
					return nil, fmt.Errorf("persist number offer: %w", err)
				}
				offers[i].PurchaseReady = true
				offers[i].ConfirmationToken = token
				offers[i].ExpiresAt = expiresAt.Format(time.RFC3339)
			} else {
				offers[i].PurchaseBlocker = firstNonEmpty(provider.Reason, "provider purchase is not supported")
			}
		}
		result.Offers = offers
		result.Count = len(offers)
		results = append(results, result)
		allOffers = append(allOffers, offers...)
	}
	sort.SliceStable(allOffers, func(i, j int) bool {
		left, leftOK := priceFloat(allOffers[i].MonthlyPrice)
		right, rightOK := priceFloat(allOffers[j].MonthlyPrice)
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK && left != right {
			return left < right
		}
		return allOffers[i].PhoneNumber < allOffers[j].PhoneNumber
	})
	base["countries"] = results
	base["offers"] = allOffers
	base["offer_count"] = len(allOffers)
	base["pricing_note"] = "Prices are provider quotes and may exclude taxes, regulatory fees, media streaming, and AI usage. Purchase tokens expire after 10 minutes."
	return base, nil
}

func (a *App) searchProviderCountry(ctx *sdk.AppCtx, provider *numberProvider, request numberSearchRequest, country string) ([]numberOffer, []string, error) {
	switch provider.Slug {
	case "twilio":
		return searchTwilioNumbers(ctx, provider.ConnID, request, country)
	case "telnyx":
		return searchTelnyxNumbers(ctx, provider.ConnID, request, country)
	case "vonage":
		return searchVonageNumbers(ctx, provider.ConnID, request, country)
	case "plivo":
		return searchPlivoNumbers(ctx, provider.ConnID, request, country)
	case "signalwire":
		return searchSignalWireNumbers(ctx, provider.ConnID, request, country)
	default:
		return nil, nil, fmt.Errorf("number search is unsupported for provider %s", provider.Slug)
	}
}

func searchTwilioNumbers(ctx *sdk.AppCtx, connID int64, request numberSearchRequest, country string) ([]numberOffer, []string, error) {
	types := []string{request.NumberType}
	if request.NumberType == "any" {
		types = []string{"local", "toll_free"}
	}
	monthly, currency, monthlyErr := twilioNumberPricing(ctx, connID, country)
	inbound, inboundCurrency, inboundErr := twilioInboundPricing(ctx, connID, country)
	if currency == "" {
		currency = inboundCurrency
	}
	warnings := []string{}
	if monthlyErr != nil {
		warnings = append(warnings, "monthly pricing unavailable: "+monthlyErr.Error())
	}
	if inboundErr != nil {
		warnings = append(warnings, "inbound pricing unavailable: "+inboundErr.Error())
	}
	offers := []numberOffer{}
	var firstErr error
	for _, numberType := range types {
		tool := "search_available_numbers"
		if numberType == "toll_free" {
			tool = "search_toll_free_numbers"
		}
		input := map[string]any{"Country": country, "PageSize": request.Limit}
		if containsString(request.Features, "voice") {
			input["VoiceEnabled"] = true
		}
		if containsString(request.Features, "sms") {
			input["SmsEnabled"] = true
		}
		if containsString(request.Features, "mms") && numberType == "local" {
			input["MmsEnabled"] = true
		}
		if request.AreaCode != "" && numberType == "local" {
			input["AreaCode"] = request.AreaCode
		}
		if request.Pattern != "" {
			input["Contains"] = request.Pattern
		}
		data, err := executeCarrierTool(ctx, connID, tool, input)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		parsed, err := parseTwilioOffers(data, country, numberType, monthly, inbound, currency)
		if err != nil {
			return nil, warnings, err
		}
		offers = append(offers, parsed...)
	}
	if len(offers) == 0 && firstErr != nil {
		return nil, warnings, firstErr
	}
	return offers, warnings, nil
}

func twilioNumberPricing(ctx *sdk.AppCtx, connID int64, country string) (map[string]string, string, error) {
	data, err := executeCarrierTool(ctx, connID, "get_phone_number_pricing", map[string]any{"Country": country})
	if err != nil {
		return nil, "", err
	}
	var out struct {
		PriceUnit         string `json:"price_unit"`
		PhoneNumberPrices []struct {
			NumberType   string `json:"number_type"`
			CurrentPrice string `json:"current_price"`
			BasePrice    string `json:"base_price"`
		} `json:"phone_number_prices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, "", fmt.Errorf("decode Twilio number pricing: %w", err)
	}
	prices := map[string]string{}
	for _, price := range out.PhoneNumberPrices {
		value := firstNonEmpty(price.CurrentPrice, price.BasePrice)
		prices[normalizeNumberType(price.NumberType)] = value
	}
	return prices, strings.ToUpper(out.PriceUnit), nil
}

func twilioInboundPricing(ctx *sdk.AppCtx, connID int64, country string) (map[string]string, string, error) {
	data, err := executeCarrierTool(ctx, connID, "get_voice_pricing", map[string]any{"Country": country})
	if err != nil {
		return nil, "", err
	}
	var out struct {
		PriceUnit         string `json:"price_unit"`
		InboundCallPrices []struct {
			NumberType   string `json:"number_type"`
			CurrentPrice string `json:"current_price"`
			BasePrice    string `json:"base_price"`
		} `json:"inbound_call_prices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, "", fmt.Errorf("decode Twilio voice pricing: %w", err)
	}
	prices := map[string]string{}
	for _, price := range out.InboundCallPrices {
		prices[normalizeNumberType(price.NumberType)] = firstNonEmpty(price.CurrentPrice, price.BasePrice)
	}
	return prices, strings.ToUpper(out.PriceUnit), nil
}

func parseTwilioOffers(data json.RawMessage, country, numberType string, monthly, inbound map[string]string, currency string) ([]numberOffer, error) {
	var out struct {
		Available []struct {
			FriendlyName       string          `json:"friendly_name"`
			PhoneNumber        string          `json:"phone_number"`
			Locality           string          `json:"locality"`
			Region             string          `json:"region"`
			ISOCountry         string          `json:"iso_country"`
			AddressRequirement string          `json:"address_requirements"`
			Capabilities       map[string]bool `json:"capabilities"`
		} `json:"available_phone_numbers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode Twilio number search: %w", err)
	}
	offers := make([]numberOffer, 0, len(out.Available))
	for _, item := range out.Available {
		features := []string{}
		for key, enabled := range item.Capabilities {
			if enabled {
				features = append(features, strings.ToLower(key))
			}
		}
		sort.Strings(features)
		typeKey := normalizeNumberType(numberType)
		offers = append(offers, numberOffer{
			PhoneNumber: item.PhoneNumber, Country: firstNonEmpty(item.ISOCountry, country),
			NumberType: typeKey, FriendlyName: item.FriendlyName, Locality: item.Locality,
			Region: item.Region, Features: features, MonthlyPrice: monthly[typeKey],
			InboundPrice: inbound[typeKey], Currency: currency,
			AddressRequirement: item.AddressRequirement,
		})
	}
	return offers, nil
}

func searchTelnyxNumbers(ctx *sdk.AppCtx, connID int64, request numberSearchRequest, country string) ([]numberOffer, []string, error) {
	input := map[string]any{
		"filter[country_code]": country,
		"filter[features]":     request.Features,
		"filter[limit]":        request.Limit,
		"page[size]":           request.Limit,
	}
	if request.NumberType != "any" {
		input["filter[phone_number_type]"] = request.NumberType
	}
	if request.AreaCode != "" {
		input["filter[national_destination_code]"] = request.AreaCode
	}
	if request.Pattern != "" {
		input["filter[starts_with]"] = compactPhoneNumber(request.Pattern)
	}
	data, err := executeCarrierTool(ctx, connID, "search_available_phone_numbers", input)
	if err != nil {
		return nil, nil, err
	}
	offers, err := parseTelnyxOffers(data, country, request.NumberType)
	return offers, nil, err
}

func parseTelnyxOffers(data json.RawMessage, country, requestedType string) ([]numberOffer, error) {
	var out struct {
		Data []struct {
			PhoneNumber     string `json:"phone_number"`
			PhoneNumberType string `json:"phone_number_type"`
			Cost            struct {
				Upfront  string `json:"upfront_cost"`
				Monthly  string `json:"monthly_cost"`
				Currency string `json:"currency"`
			} `json:"cost_information"`
			Features []struct {
				Name string `json:"name"`
			} `json:"features"`
			Regions []struct {
				Type string `json:"region_type"`
				Name string `json:"region_name"`
			} `json:"region_information"`
			RequirementsMet *bool `json:"requirements_met"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode Telnyx number search: %w", err)
	}
	offers := make([]numberOffer, 0, len(out.Data))
	for _, item := range out.Data {
		locality, region := "", ""
		for _, info := range item.Regions {
			switch info.Type {
			case "locality", "rate_center":
				if locality == "" {
					locality = info.Name
				}
			case "administrative_area":
				region = info.Name
			}
		}
		features := []string{}
		for _, feature := range item.Features {
			features = append(features, strings.ToLower(feature.Name))
		}
		numberType := normalizeNumberType(firstNonEmpty(item.PhoneNumberType, requestedType))
		if numberType == "any" || numberType == "" {
			numberType = "local"
		}
		offers = append(offers, numberOffer{
			PhoneNumber: item.PhoneNumber, Country: country, NumberType: numberType,
			Locality: locality, Region: region, Features: features,
			MonthlyPrice: item.Cost.Monthly, UpfrontPrice: item.Cost.Upfront,
			Currency: strings.ToUpper(item.Cost.Currency), RequirementsMet: item.RequirementsMet,
		})
	}
	return offers, nil
}

func searchVonageNumbers(ctx *sdk.AppCtx, connID int64, request numberSearchRequest, country string) ([]numberOffer, []string, error) {
	input := map[string]any{"country": country, "size": request.Limit}
	if request.NumberType != "any" {
		mapping := map[string]string{"local": "landline", "mobile": "mobile-lvn", "toll_free": "landline-toll-free"}
		input["type"] = mapping[request.NumberType]
	}
	features := []string{}
	for _, feature := range request.Features {
		if feature == "voice" || feature == "sms" {
			features = append(features, strings.ToUpper(feature))
		}
	}
	if len(features) > 0 {
		input["features"] = strings.Join(features, ",")
	}
	if request.Pattern != "" {
		input["pattern"] = compactPhoneNumber(request.Pattern)
		input["search_pattern"] = 1
	} else if request.AreaCode != "" {
		input["pattern"] = request.AreaCode
		input["search_pattern"] = 0
	}
	data, err := executeCarrierTool(ctx, connID, "numbers_search", input)
	if err != nil {
		return nil, nil, err
	}
	offers, err := parseVonageOffers(data, country, request.NumberType)
	return offers, nil, err
}

func parseVonageOffers(data json.RawMessage, country, requestedType string) ([]numberOffer, error) {
	var out struct {
		Numbers []map[string]any `json:"numbers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode Vonage number search: %w", err)
	}
	offers := make([]numberOffer, 0, len(out.Numbers))
	for _, item := range out.Numbers {
		phone := stringValue(item["msisdn"])
		if phone != "" && !strings.HasPrefix(phone, "+") {
			phone = "+" + phone
		}
		features := anyStringList(item["features"])
		for i := range features {
			features[i] = strings.ToLower(features[i])
		}
		numberType := normalizeNumberType(firstNonEmpty(stringValue(item["type"]), requestedType))
		if numberType == "landline" {
			numberType = "local"
		}
		if numberType == "mobile_lvn" {
			numberType = "mobile"
		}
		monthly := firstNonEmpty(stringValue(item["monthly_cost"]), stringValue(item["cost"]))
		offers = append(offers, numberOffer{
			PhoneNumber: phone, Country: firstNonEmpty(stringValue(item["country"]), country),
			NumberType: numberType, Features: features, MonthlyPrice: monthly,
			UpfrontPrice: stringValue(item["setup_cost"]), Currency: strings.ToUpper(stringValue(item["currency"])),
		})
	}
	return offers, nil
}

type plivoPrice struct {
	Monthly  string
	Upfront  string
	Inbound  string
	Currency string
}

func searchPlivoNumbers(ctx *sdk.AppCtx, connID int64, request numberSearchRequest, country string) ([]numberOffer, []string, error) {
	input := map[string]any{
		"country_iso": country,
		"services":    strings.Join(request.Features, ","),
		"limit":       request.Limit,
	}
	if request.NumberType != "any" {
		numberType := request.NumberType
		if numberType == "toll_free" {
			numberType = "tollfree"
		}
		input["type"] = numberType
	}
	if request.Pattern != "" {
		input["pattern"] = compactPhoneNumber(request.Pattern)
	}
	if request.AreaCode != "" {
		input["pattern"] = request.AreaCode
	}
	data, err := executeCarrierTool(ctx, connID, "search_phone_numbers", input)
	if err != nil {
		return nil, nil, err
	}
	prices, priceErr := plivoNumberPricing(ctx, connID, country, request.Features)
	warnings := []string{}
	if priceErr != nil {
		warnings = append(warnings, "pricing unavailable: "+priceErr.Error())
	}
	offers, err := parsePlivoOffers(data, country, request.NumberType, prices)
	return offers, warnings, err
}

func plivoNumberPricing(ctx *sdk.AppCtx, connID int64, country string, requiredFeatures []string) (map[string]plivoPrice, error) {
	data, err := executeCarrierTool(ctx, connID, "get_number_pricing", map[string]any{"country_iso": country})
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode Plivo pricing: %w", err)
	}
	currency := strings.ToUpper(firstNonEmpty(stringValue(raw["currency"]), stringValue(raw["price_unit"])))
	phoneNumbers, _ := raw["phone_numbers"].(map[string]any)
	voice, _ := raw["voice"].(map[string]any)
	inboundRoot, _ := voice["inbound"].(map[string]any)
	prices := map[string]plivoPrice{}
	for rawType, value := range phoneNumbers {
		typeKey := normalizeNumberType(rawType)
		entry, _ := value.(map[string]any)
		price := plivoPrice{Monthly: stringValue(entry["rate"]), Currency: currency}
		if rates, ok := entry["rates"].([]any); ok {
			for _, rateValue := range rates {
				rate, _ := rateValue.(map[string]any)
				capabilities := anyStringList(rate["capabilities"])
				matches := true
				for _, required := range requiredFeatures {
					if !containsString(capabilities, required) {
						matches = false
						break
					}
				}
				if matches {
					price.Monthly = firstNonEmpty(stringValue(rate["rental_rate"]), price.Monthly)
					price.Upfront = stringValue(rate["setup_rate"])
					break
				}
			}
		}
		if inbound, ok := inboundRoot[rawType].(map[string]any); ok {
			price.Inbound = stringValue(inbound["rate"])
		} else if inbound, ok := inboundRoot[typeKey].(map[string]any); ok {
			price.Inbound = stringValue(inbound["rate"])
		}
		prices[typeKey] = price
	}
	return prices, nil
}

func parsePlivoOffers(data json.RawMessage, country, requestedType string, prices map[string]plivoPrice) ([]numberOffer, error) {
	var out struct {
		Objects []struct {
			Number      string `json:"number"`
			Type        string `json:"type"`
			Region      string `json:"region"`
			City        string `json:"city"`
			Restriction string `json:"restriction"`
			Monthly     string `json:"monthly_rental_rate"`
			Setup       string `json:"setup_rate"`
			Voice       bool   `json:"voice_enabled"`
			SMS         bool   `json:"sms_enabled"`
			MMS         bool   `json:"mms_enabled"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode Plivo number search: %w", err)
	}
	offers := make([]numberOffer, 0, len(out.Objects))
	for _, item := range out.Objects {
		phone := item.Number
		if phone != "" && !strings.HasPrefix(phone, "+") {
			phone = "+" + phone
		}
		numberType := normalizeNumberType(firstNonEmpty(item.Type, requestedType))
		if numberType == "any" || numberType == "" {
			numberType = "local"
		}
		features := []string{}
		if item.Voice {
			features = append(features, "voice")
		}
		if item.SMS {
			features = append(features, "sms")
		}
		if item.MMS {
			features = append(features, "mms")
		}
		price := prices[numberType]
		offers = append(offers, numberOffer{
			PhoneNumber: phone, Country: country, NumberType: numberType,
			Locality: item.City, Region: item.Region, Features: features,
			MonthlyPrice: firstNonEmpty(item.Monthly, price.Monthly),
			UpfrontPrice: firstNonEmpty(item.Setup, price.Upfront),
			InboundPrice: price.Inbound, Currency: price.Currency,
			AddressRequirement: item.Restriction,
		})
	}
	return offers, nil
}

func searchSignalWireNumbers(ctx *sdk.AppCtx, connID int64, request numberSearchRequest, country string) ([]numberOffer, []string, error) {
	types := []string{request.NumberType}
	if request.NumberType == "any" {
		types = []string{"local", "toll_free"}
	}
	offers := []numberOffer{}
	var firstErr error
	for _, numberType := range types {
		tool := "search_available_numbers"
		if numberType == "toll_free" {
			tool = "search_toll_free_numbers"
		}
		input := map[string]any{"Country": country, "PageSize": request.Limit}
		if containsString(request.Features, "voice") {
			input["VoiceEnabled"] = true
		}
		if containsString(request.Features, "sms") {
			input["SmsEnabled"] = true
		}
		if containsString(request.Features, "mms") && numberType == "local" {
			input["MmsEnabled"] = true
		}
		if request.AreaCode != "" && numberType == "local" {
			input["AreaCode"] = request.AreaCode
		}
		if request.Pattern != "" {
			input["Contains"] = request.Pattern
		}
		data, err := executeCarrierTool(ctx, connID, tool, input)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		parsed, err := parseTwilioOffers(data, country, numberType, nil, nil, "")
		if err != nil {
			return nil, nil, err
		}
		offers = append(offers, parsed...)
	}
	if len(offers) == 0 && firstErr != nil {
		return nil, nil, firstErr
	}
	return offers, []string{"SignalWire search results do not include a verifiable price; purchase is disabled"}, nil
}

func (a *App) purchaseNumber(ctx *sdk.AppCtx, token string) (map[string]any, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("confirmation_token required; call telephony_numbers_search and obtain explicit user confirmation first")
	}
	projectID := currentProject(ctx)
	if projectID == "" {
		return nil, errors.New("project context required for number purchase")
	}
	intent, err := dbNumberPurchaseIntentGet(ctx.AppDB(), projectID, token)
	if err != nil {
		return nil, err
	}
	if intent == nil {
		return nil, errors.New("confirmation token not found")
	}
	if intent.Status == "succeeded" {
		return numberPurchaseResult(intent, true), nil
	}
	if intent.Status != "quoted" {
		return nil, fmt.Errorf("number purchase intent is %s; search again before retrying", intent.Status)
	}
	if time.Now().UTC().After(intent.ExpiresAt) {
		_ = dbNumberPurchaseIntentStatus(ctx.AppDB(), token, "expired", nil, "confirmation token expired")
		return nil, errors.New("confirmation token expired; search for the number again")
	}
	provider, err := a.numberProviderFor(ctx)
	if err != nil {
		return nil, err
	}
	if provider.ConnID != intent.CarrierConnectionID || provider.Slug != intent.Provider {
		return nil, errors.New("the bound carrier changed after this quote; search again")
	}
	claimed, err := dbNumberPurchaseIntentClaim(ctx.AppDB(), projectID, token)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, errors.New("number purchase is already in progress; do not retry automatically")
	}

	var raw json.RawMessage
	switch intent.Provider {
	case "twilio":
		raw, err = executeCarrierTool(ctx, intent.CarrierConnectionID, "buy_phone_number", map[string]any{
			"PhoneNumber":  intent.PhoneNumber,
			"FriendlyName": "Apteva Telephony",
		})
	case "telnyx":
		input := map[string]any{"phone_numbers": []map[string]any{{"phone_number": intent.PhoneNumber}}}
		if connectionID := strings.TrimSpace(provider.Fields["connection_id"]); connectionID != "" {
			input["connection_id"] = connectionID
		}
		raw, err = executeCarrierTool(ctx, intent.CarrierConnectionID, "create_number_order", input)
	case "vonage":
		raw, err = executeCarrierTool(ctx, intent.CarrierConnectionID, "number_buy", map[string]any{
			"country": intent.Country,
			"msisdn":  compactPhoneNumber(intent.PhoneNumber),
		})
	case "plivo":
		raw, err = executeCarrierTool(ctx, intent.CarrierConnectionID, "buy_phone_number", map[string]any{
			"number": compactPhoneNumber(intent.PhoneNumber),
		})
	default:
		err = fmt.Errorf("number purchase is unsupported for provider %s", intent.Provider)
	}
	if err != nil {
		_ = dbNumberPurchaseIntentStatus(ctx.AppDB(), token, "failed", nil, err.Error())
		return nil, fmt.Errorf("provider purchase failed; do not retry automatically: %w", err)
	}
	if intent.Provider == "vonage" {
		var body map[string]any
		if json.Unmarshal(raw, &body) == nil {
			code := stringValue(body["error-code"])
			if code != "" && code != "0" && code != "200" {
				message := firstNonEmpty(stringValue(body["error-code-label"]), "Vonage rejected the purchase")
				_ = dbNumberPurchaseIntentStatus(ctx.AppDB(), token, "failed", raw, message)
				return nil, errors.New(message)
			}
		}
	}
	if err := dbNumberPurchaseIntentStatus(ctx.AppDB(), token, "succeeded", raw, ""); err != nil {
		return nil, fmt.Errorf("number purchased but local confirmation persistence failed; do not retry: %w", err)
	}
	intent.Status = "succeeded"
	intent.ResponseJSON = string(raw)
	return numberPurchaseResult(intent, false), nil
}

func numberPurchaseResult(intent *numberPurchaseIntent, replay bool) map[string]any {
	var response any
	if intent.ResponseJSON != "" {
		if err := json.Unmarshal([]byte(intent.ResponseJSON), &response); err != nil {
			response = intent.ResponseJSON
		}
	}
	return map[string]any{
		"purchased": true, "idempotent_replay": replay,
		"provider": intent.Provider, "phone_number": intent.PhoneNumber,
		"country": intent.Country, "number_type": intent.NumberType,
		"monthly_price": intent.MonthlyPrice, "upfront_price": intent.UpfrontPrice,
		"inbound_price": intent.InboundPrice, "currency": intent.Currency,
		"provider_response": response,
		"next":              "Create an inbound route for this number, then configure the carrier webhook where that provider is supported.",
	}
}

func (a *App) toolNumbersSearch(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	result, err := a.searchNumberInventory(ctx, args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return result, nil
}

func (a *App) toolNumbersPurchase(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	result, err := a.purchaseNumber(ctx, strArg(args, "confirmation_token", ""))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return result, nil
}

func (a *App) handleNumbers(w http.ResponseWriter, r *http.Request) {
	projectID, err := a.panelProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body map[string]any
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	if err := decoder.Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	ctx := globalCtx.WithProject(projectID)
	path := strings.TrimSuffix(r.URL.Path, "/")
	var result map[string]any
	switch path {
	case "/numbers/search":
		result, err = a.searchNumberInventory(ctx, body)
	case "/numbers/purchase":
		result, err = a.purchaseNumber(ctx, strArg(body, "confirmation_token", ""))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func dbNumberPurchaseIntentInsert(db *sql.DB, intent numberPurchaseIntent) error {
	_, err := db.Exec(`INSERT INTO number_purchase_intents
        (token, project_id, provider_slug, carrier_connection_id, country, phone_number,
         number_type, monthly_price, upfront_price, inbound_price, currency, status, expires_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'quoted', ?)`,
		intent.Token, intent.ProjectID, intent.Provider, intent.CarrierConnectionID,
		intent.Country, intent.PhoneNumber, intent.NumberType, intent.MonthlyPrice,
		intent.UpfrontPrice, intent.InboundPrice, intent.Currency, intent.ExpiresAt.Format(time.RFC3339))
	return err
}

func dbNumberPurchaseIntentGet(db *sql.DB, projectID, token string) (*numberPurchaseIntent, error) {
	var intent numberPurchaseIntent
	var expires string
	err := db.QueryRow(`SELECT token, project_id, provider_slug, carrier_connection_id, country,
        phone_number, number_type, monthly_price, upfront_price, inbound_price, currency,
        status, response_json, error_message, expires_at
        FROM number_purchase_intents WHERE project_id = ? AND token = ?`, projectID, token).Scan(
		&intent.Token, &intent.ProjectID, &intent.Provider, &intent.CarrierConnectionID,
		&intent.Country, &intent.PhoneNumber, &intent.NumberType, &intent.MonthlyPrice,
		&intent.UpfrontPrice, &intent.InboundPrice, &intent.Currency, &intent.Status,
		&intent.ResponseJSON, &intent.ErrorMessage, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	intent.ExpiresAt, err = time.Parse(time.RFC3339, expires)
	if err != nil {
		return nil, fmt.Errorf("invalid number purchase expiry: %w", err)
	}
	return &intent, nil
}

func dbNumberPurchaseIntentClaim(db *sql.DB, projectID, token string) (bool, error) {
	result, err := db.Exec(`UPDATE number_purchase_intents
        SET status = 'purchasing', updated_at = CURRENT_TIMESTAMP
        WHERE project_id = ? AND token = ? AND status = 'quoted'`, projectID, token)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func dbNumberPurchaseIntentStatus(db *sql.DB, token, status string, response json.RawMessage, errorMessage string) error {
	_, err := db.Exec(`UPDATE number_purchase_intents
        SET status = ?, response_json = ?, error_message = ?, updated_at = CURRENT_TIMESTAMP
        WHERE token = ?`, status, string(response), errorMessage, token)
	return err
}

func normalizeNumberType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("-", "_", " ", "_").Replace(value)
	switch value {
	case "tollfree", "toll_free_number", "landline_toll_free":
		return "toll_free"
	case "landline":
		return "local"
	case "fixed":
		return "local"
	case "mobile_lvn":
		return "mobile"
	default:
		return value
	}
}

func stringListArg(args map[string]any, key string) []string {
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	switch list := value.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		if strings.TrimSpace(list) == "" {
			return nil
		}
		return strings.Split(list, ",")
	default:
		return nil
	}
}

func anyStringList(value any) []string {
	switch list := value.(type) {
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if text := stringValue(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	case []string:
		return list
	case string:
		if list == "" {
			return nil
		}
		return strings.Split(list, ",")
	default:
		return nil
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return ""
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(wanted)) {
			return true
		}
	}
	return false
}

func digitsOnly(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func numberPatternSafe(value string) bool {
	for _, char := range value {
		if (char >= '0' && char <= '9') || strings.ContainsRune("+*# ()-", char) {
			continue
		}
		return false
	}
	return true
}

func compactPhoneNumber(value string) string {
	var out strings.Builder
	for _, char := range value {
		if char >= '0' && char <= '9' {
			out.WriteRune(char)
		}
	}
	return out.String()
}

func priceFloat(value string) (float64, bool) {
	price, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return price, err == nil
}
