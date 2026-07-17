package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseNumberSearchRequestComparison(t *testing.T) {
	request, err := parseNumberSearchRequest(map[string]any{
		"country":     "ee",
		"countries":   []any{"AT", "ee"},
		"number_type": "toll-free",
		"features":    []any{"voice", "sms", "voice"},
		"limit":       float64(5),
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.NumberType != "toll_free" || request.Limit != 5 {
		t.Fatalf("unexpected normalized request: %#v", request)
	}
	if len(request.Countries) != 2 || request.Countries[0] != "EE" || request.Countries[1] != "AT" {
		t.Fatalf("unexpected countries: %#v", request.Countries)
	}
	if len(request.Features) != 2 || request.Features[0] != "voice" || request.Features[1] != "sms" {
		t.Fatalf("unexpected features: %#v", request.Features)
	}
}

func TestParseTwilioOffersNormalizesPricing(t *testing.T) {
	raw := json.RawMessage(`{
      "available_phone_numbers": [{
        "friendly_name": "+372 669 2354",
        "phone_number": "+3726692354",
        "locality": "Tallinn",
        "region": "Harju",
        "iso_country": "EE",
        "address_requirements": "any",
        "capabilities": {"voice": true, "SMS": false, "MMS": false}
      }]
    }`)
	offers, err := parseTwilioOffers(raw, "EE", "local",
		map[string]string{"local": "1.00"}, map[string]string{"local": "0.0115"}, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 {
		t.Fatalf("offers=%d", len(offers))
	}
	offer := offers[0]
	if offer.PhoneNumber != "+3726692354" || offer.MonthlyPrice != "1.00" || offer.InboundPrice != "0.0115" {
		t.Fatalf("unexpected offer: %#v", offer)
	}
	if offer.AddressRequirement != "any" || len(offer.Features) != 1 || offer.Features[0] != "voice" {
		t.Fatalf("unexpected normalized metadata: %#v", offer)
	}
}

func TestParseTwilioOffersMapsLocalInventoryToNationalPricing(t *testing.T) {
	raw := json.RawMessage(`{
      "available_phone_numbers": [{
        "friendly_name": "+420 910 000 001",
        "phone_number": "+420910000001",
        "iso_country": "CZ",
        "address_requirements": "any",
        "capabilities": {"voice": true}
      }]
    }`)
	offers, err := parseTwilioOffers(raw, "CZ", "local",
		map[string]string{"national": "1.50", "mobile": "12.00", "toll_free": "35.00"},
		map[string]string{"national": "0.01070", "toll_free": "0.3173"}, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 {
		t.Fatalf("offers=%d", len(offers))
	}
	offer := offers[0]
	if offer.NumberType != "national" || offer.MonthlyPrice != "1.50" || offer.InboundPrice != "0.01070" {
		t.Fatalf("unexpected national offer: %#v", offer)
	}
}

func TestParseTelnyxOffersPreservesCostAndRequirements(t *testing.T) {
	raw := json.RawMessage(`{
      "data": [{
        "phone_number": "+3725550100",
        "phone_number_type": "mobile",
        "cost_information": {"upfront_cost": "2.00", "monthly_cost": "3.00", "currency": "USD"},
        "features": [{"name": "voice"}, {"name": "sms"}],
        "region_information": [{"region_type": "locality", "region_name": "Tallinn"}],
        "requirements_met": false
      }]
    }`)
	offers, err := parseTelnyxOffers(raw, "EE", "mobile")
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].MonthlyPrice != "3.00" || offers[0].UpfrontPrice != "2.00" {
		t.Fatalf("unexpected offers: %#v", offers)
	}
	if offers[0].RequirementsMet == nil || *offers[0].RequirementsMet {
		t.Fatalf("requirements flag lost: %#v", offers[0])
	}
}

func TestParseVonageOffersNormalizesMSISDN(t *testing.T) {
	raw := json.RawMessage(`{
      "numbers": [{
        "country": "AT",
        "msisdn": "4312345678",
        "type": "landline",
        "cost": "1.25",
        "setup_cost": 0,
        "currency": "EUR",
        "features": ["VOICE"]
      }]
    }`)
	offers, err := parseVonageOffers(raw, "AT", "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].PhoneNumber != "+4312345678" || offers[0].NumberType != "local" {
		t.Fatalf("unexpected offers: %#v", offers)
	}
	if offers[0].MonthlyPrice != "1.25" || offers[0].UpfrontPrice != "0" || offers[0].Currency != "EUR" {
		t.Fatalf("unexpected pricing: %#v", offers[0])
	}
}

func TestParsePlivoOffersUsesQuotedPricing(t *testing.T) {
	raw := json.RawMessage(`{
      "objects": [{
        "number": "3725550100",
        "type": "fixed",
        "region": "Harju",
        "city": "Tallinn",
        "restriction": "local_address",
        "voice_enabled": true,
        "sms_enabled": true
      }]
    }`)
	offers, err := parsePlivoOffers(raw, "EE", "local", map[string]plivoPrice{
		"local": {Monthly: "1.50", Upfront: "0.25", Inbound: "0.01", Currency: "EUR"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 {
		t.Fatalf("offers=%d", len(offers))
	}
	offer := offers[0]
	if offer.PhoneNumber != "+3725550100" || offer.NumberType != "local" {
		t.Fatalf("unexpected normalized offer: %#v", offer)
	}
	if offer.MonthlyPrice != "1.50" || offer.UpfrontPrice != "0.25" || offer.InboundPrice != "0.01" || offer.Currency != "EUR" {
		t.Fatalf("unexpected pricing: %#v", offer)
	}
	if len(offer.Features) != 2 || offer.Features[0] != "voice" || offer.Features[1] != "sms" {
		t.Fatalf("unexpected features: %#v", offer.Features)
	}
}

func TestNormalizeNumberTypeAliases(t *testing.T) {
	for input, want := range map[string]string{
		"fixed": "local", "landline": "local", "tollfree": "toll_free",
		"landline-toll-free": "toll_free", "mobile-lvn": "mobile",
	} {
		if got := normalizeNumberType(input); got != want {
			t.Errorf("normalizeNumberType(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestValidE164ForPurchaseQuotes(t *testing.T) {
	for value, want := range map[string]bool{
		"+3725550100":  true,
		"+12025550100": true,
		"3725550100":   false,
		"+01234567":    false,
		"+123":         false,
		"+1202ABC0100": false,
	} {
		if got := validE164(value); got != want {
			t.Errorf("validE164(%q)=%v, want %v", value, got, want)
		}
	}
}

func TestNumberPurchaseIntentClaimIsSingleUse(t *testing.T) {
	db := testCallsDB(t).db
	intent := numberPurchaseIntent{
		Token: "quote-token", ProjectID: "project-a", Provider: "twilio",
		CarrierConnectionID: 9, Country: "EE", PhoneNumber: "+3726692354",
		NumberType: "local", MonthlyPrice: "1.00", InboundPrice: "0.0115",
		Currency: "USD", AddressRequirement: "any", ExpiresAt: time.Now().UTC().Add(time.Minute),
		ComplianceRequired: true,
	}
	if err := dbNumberPurchaseIntentInsert(db, intent); err != nil {
		t.Fatal(err)
	}
	claimed, err := dbNumberPurchaseIntentClaim(db, "project-a", intent.Token, "AD0123456789abcdef0123456789abcdef", "BU0123456789abcdef0123456789abcdef")
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	claimed, err = dbNumberPurchaseIntentClaim(db, "project-a", intent.Token, "", "")
	if err != nil || claimed {
		t.Fatalf("second claim = %v, %v", claimed, err)
	}
	response := json.RawMessage(`{"sid":"PN123"}`)
	if err := dbNumberPurchaseIntentStatus(db, intent.Token, "succeeded", response, ""); err != nil {
		t.Fatal(err)
	}
	stored, err := dbNumberPurchaseIntentGet(db, "project-a", intent.Token)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.Status != "succeeded" || stored.ResponseJSON != string(response) || stored.AddressRequirement != "any" {
		t.Fatalf("unexpected stored intent: %#v", stored)
	}
	if !stored.ComplianceRequired {
		t.Fatal("compliance requirement flag was not persisted")
	}
	if stored.SelectedAddressSID != "AD0123456789abcdef0123456789abcdef" || stored.SelectedBundleSID != "BU0123456789abcdef0123456789abcdef" {
		t.Fatalf("selected compliance resources were not persisted: %#v", stored)
	}
}

func TestTwilioAddressRequirementValidation(t *testing.T) {
	if requiresNumberAddress("none") || requiresNumberAddress("") || !requiresNumberAddress("any") || !requiresNumberAddress("local") {
		t.Fatal("unexpected address requirement normalization")
	}
	for value, want := range map[string]bool{
		"AD0123456789abcdef0123456789abcdef": true,
		"PN0123456789abcdef0123456789abcdef": false,
		"ADnot-a-valid-sid":                  false,
	} {
		if got := validTwilioAddressSID(value); got != want {
			t.Errorf("validTwilioAddressSID(%q)=%v, want %v", value, got, want)
		}
	}
	for value, want := range map[string]bool{
		"BU0123456789abcdef0123456789abcdef": true,
		"AD0123456789abcdef0123456789abcdef": false,
		"BUnot-a-valid-sid":                  false,
	} {
		if got := validTwilioBundleSID(value); got != want {
			t.Errorf("validTwilioBundleSID(%q)=%v, want %v", value, got, want)
		}
	}
}

func TestManifestAndRuntimeExposeNumberTools(t *testing.T) {
	manifest := (&App{}).Manifest()
	manifestTools := map[string]bool{}
	for _, tool := range manifest.Provides.MCPTools {
		manifestTools[tool.Name] = true
	}
	runtimeTools := map[string]bool{}
	for _, tool := range (&App{}).MCPTools() {
		runtimeTools[tool.Name] = true
	}
	for _, name := range []string{
		"telephony_numbers_search", "telephony_numbers_purchase", "telephony_addresses_list",
		"telephony_address_create", "telephony_regulatory_requirements", "telephony_regulatory_bundles_list",
		"telephony_regulatory_bundle_create", "telephony_regulatory_bundle_get",
		"telephony_regulatory_bundle_item_create", "telephony_regulatory_bundle_evaluate",
		"telephony_regulatory_bundle_submit",
		"telephony_compliance_profiles_list", "telephony_compliance_profile_create",
		"telephony_compliance_profile_get", "telephony_compliance_requirement_set",
		"telephony_compliance_profile_evaluate", "telephony_compliance_profile_submit",
	} {
		if !manifestTools[name] || !runtimeTools[name] {
			t.Fatalf("number tool %s missing: manifest=%v runtime=%v", name, manifestTools[name], runtimeTools[name])
		}
	}
}

func TestTelnyxComplianceProfileEvaluation(t *testing.T) {
	profile := map[string]any{
		"status": "unapproved",
		"regulatory_requirements": []any{
			map[string]any{"requirement_id": "address", "field_type": "address_id", "field_value": ""},
			map[string]any{"requirement_id": "name", "field_type": "textual", "field_value": "Apteva"},
		},
	}
	evaluation := telnyxProfileEvaluation(profile)
	if evaluation["status"] != "incomplete" || evaluation["usable_for_order"] != false {
		t.Fatalf("unexpected incomplete evaluation: %#v", evaluation)
	}
	profile["regulatory_requirements"].([]any)[0].(map[string]any)["field_value"] = "address-123"
	evaluation = telnyxProfileEvaluation(profile)
	if evaluation["status"] != "compliant" || evaluation["usable_for_order"] != true {
		t.Fatalf("unexpected fulfilled evaluation: %#v", evaluation)
	}
	profile["status"] = "no-longer-eligible"
	if evaluation := telnyxProfileEvaluation(profile); evaluation["status"] != "incomplete" {
		t.Fatalf("ineligible profile was accepted: %#v", evaluation)
	}
}

func TestTelnyxNumberOrderUsesRequirementGroup(t *testing.T) {
	input := telnyxNumberOrderInput(
		&numberPurchaseIntent{PhoneNumber: "+3725550100"},
		&numberProvider{Fields: map[string]string{"connection_id": "connection-123"}},
		"requirement-group-123",
	)
	phoneNumbers := input["phone_numbers"].([]map[string]any)
	if phoneNumbers[0]["requirement_group_id"] != "requirement-group-123" || input["connection_id"] != "connection-123" {
		t.Fatalf("unexpected Telnyx order input: %#v", input)
	}
}

func TestProviderResourceAliases(t *testing.T) {
	args := resourceArgs(map[string]any{"address_id": "address-1", "compliance_id": "profile-1"})
	if args["address_sid"] != "address-1" || args["bundle_sid"] != "profile-1" {
		t.Fatalf("generic aliases were not mapped: %#v", args)
	}
	address := normalizeTelnyxAddress(map[string]any{"id": "address-1", "business_name": "Apteva", "country_code": "ES"})
	if address["sid"] != "address-1" || address["address_id"] != "address-1" {
		t.Fatalf("address aliases missing: %#v", address)
	}
}
