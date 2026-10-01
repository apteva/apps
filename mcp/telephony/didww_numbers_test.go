package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func didwwSearchPlatform(available bool) *answerPlatform {
	meta := "false"
	if available {
		meta = "true"
	}
	return &answerPlatform{
		bindings:    map[string]any{"carrier": int64(19)},
		credentials: &sdk.ConnectionCredentials{Slug: "didww", Fields: map[string]string{}},
		integrationResponse: map[string]json.RawMessage{
			"list_countries":       json.RawMessage(`{"data":[{"id":"country-fr","type":"countries","attributes":{"iso":"FR","name":"France"}}]}`),
			"list_did_group_types": json.RawMessage(`{"data":[{"id":"type-local","type":"did_group_types","attributes":{"name":"Local"}}]}`),
			"list_did_groups":      json.RawMessage(`{"data":[{"id":"group-paris","type":"did_groups","attributes":{"area_name":"Paris","features":["voice_in","voice_out"]},"relationships":{"stock_keeping_units":{"data":[{"type":"stock_keeping_units","id":"sku-1"}]},"address_requirement":{"data":{"type":"address_requirements","id":"req-fr"}}},"meta":{"available_dids_enabled":` + meta + `,"total_count":12}}],"included":[{"id":"sku-1","type":"stock_keeping_units","attributes":{"setup_price":"0","monthly_price":"1.5","channels_included_count":0}}]}`),
			"list_available_dids":  json.RawMessage(`{"data":[{"id":"available-1","type":"available_dids","attributes":{"number":"33150000001"}}]}`),
		},
	}
}

func TestDIDWWSearchExactInventoryCreatesVoiceOffer(t *testing.T) {
	platform := didwwSearchPlatform(true)
	app, ctx := withTelephonyTestContext(t, platform)
	result, err := app.searchNumberInventory(ctx, map[string]any{"country": "FR", "number_type": "local", "features": []any{"voice"}, "limit": 5})
	if err != nil {
		t.Fatal(err)
	}
	offers := result["offers"].([]numberOffer)
	if len(offers) != 1 || offers[0].PhoneNumber != "+33150000001" || !offers[0].PurchaseReady || offers[0].InventoryMode != "available_did" {
		t.Fatalf("unexpected DIDWW exact offers: %+v", offers)
	}
	if offers[0].ProviderResourceID != "available-1" || offers[0].ProviderGroupID != "group-paris" || offers[0].ProviderSKUID != "sku-1" {
		t.Fatalf("DIDWW resource IDs were not retained: %+v", offers[0])
	}
}

func TestDIDWWGroupSearchSupportsAccountsWithoutIndividualInventory(t *testing.T) {
	platform := didwwSearchPlatform(false)
	app, ctx := withTelephonyTestContext(t, platform)
	result, err := app.searchNumberInventory(ctx, map[string]any{"country": "FR", "number_type": "local", "features": []any{"voice"}, "limit": 5})
	if err != nil {
		t.Fatal(err)
	}
	offers := result["offers"].([]numberOffer)
	if len(offers) != 1 || offers[0].PhoneNumber != "" || offers[0].ProviderGroupID != "group-paris" || !offers[0].PurchaseReady {
		t.Fatalf("unexpected DIDWW group quote: %+v", offers)
	}
	if offers[0].FriendlyName == "" || offers[0].PurchaseBlocker != "" {
		t.Fatalf("group quote was not actionable: %+v", offers[0])
	}
	if offers[0].ConfirmationToken == "" {
		t.Fatal("group quote did not receive a confirmation token")
	}
}

func TestDIDWWDocumentedRequirementRelationshipBlocksUntilVerification(t *testing.T) {
	platform := didwwSearchPlatform(false)
	platform.integrationResponse["list_did_groups"] = json.RawMessage(`{"data":[{"id":"group-paris","type":"did_groups","attributes":{"area_name":"Paris","features":["voice_in"]},"relationships":{"stock_keeping_units":{"data":[{"type":"stock_keeping_units","id":"sku-1"}]},"requirement":{"data":{"type":"requirements","id":"requirement-fr"}}},"meta":{"needs_registration":true,"available_dids_enabled":false,"total_count":12}}],"included":[{"id":"sku-1","type":"stock_keeping_units","attributes":{"setup_price":"0","monthly_price":"1.5"}}]}`)
	app, ctx := withTelephonyTestContext(t, platform)
	result, err := app.searchNumberInventory(ctx, map[string]any{"country": "FR", "number_type": "local"})
	if err != nil {
		t.Fatal(err)
	}
	offer := result["offers"].([]numberOffer)[0]
	if !offer.ComplianceRequired || offer.AddressRequirement != "DIDWW registration requirement: requirement-fr" || !offer.PurchaseReady {
		t.Fatalf("documented DIDWW registration was not retained: %+v", offer)
	}
}

func TestDIDWWOrderInputUsesExactOrGroupResource(t *testing.T) {
	exact := didwwNumberOrderInput(&numberPurchaseIntent{Token: "t1", ProviderResourceID: "available-1", ProviderGroupID: "group-paris", ProviderSKUID: "sku-1", InventoryMode: "available_did"})
	attrs := exact["items"].([]map[string]any)[0]["attributes"].(map[string]any)
	if attrs["available_did_id"] != "available-1" || attrs["did_group_id"] != nil {
		t.Fatalf("exact DIDWW order resource mismatch: %#v", exact)
	}
	group := didwwNumberOrderInput(&numberPurchaseIntent{Token: "t2", ProviderGroupID: "group-paris", ProviderSKUID: "sku-1", InventoryMode: "did_group"})
	attrs = group["items"].([]map[string]any)[0]["attributes"].(map[string]any)
	if attrs["qty"] != 1 || attrs["available_did_id"] != nil || group["allow_back_ordering"] != false {
		t.Fatalf("group DIDWW order resource mismatch: %#v", group)
	}
}

func TestDIDWWCompletedOrderReturnsAllocatedNumber(t *testing.T) {
	raw := json.RawMessage(`{"data":{"id":"order-1","attributes":{"status":"Completed"}},"included":[{"type":"dids","id":"did-1","attributes":{"number":"33142345678"}}]}`)
	if got := didwwNumberFromOrder(raw); got != "+33142345678" {
		t.Fatalf("allocated DID was not normalized: %q", got)
	}
}

func TestDIDWWGroupPurchasePersistsIdempotentOrder(t *testing.T) {
	platform := &answerPlatform{
		bindings:            map[string]any{"carrier": int64(19)},
		credentials:         &sdk.ConnectionCredentials{Slug: "didww", Fields: map[string]string{}},
		integrationResponse: map[string]json.RawMessage{"create_order": json.RawMessage(`{"data":{"id":"order-1","type":"orders","attributes":{"status":"completed"}}}`)},
	}
	app, ctx := withTelephonyTestContext(t, platform)
	intent := numberPurchaseIntent{Token: "didww-token", ProjectID: "project-a", Provider: "didww", CarrierConnectionID: 19, Country: "FR", NumberType: "local", MonthlyPrice: "1.5", Currency: "USD", AddressRequirement: "requirement-fr", ComplianceRequired: true, ProviderGroupID: "group-paris", ProviderSKUID: "sku-1", InventoryMode: "did_group", ExpiresAt: time.Now().UTC().Add(time.Minute)}
	if err := dbNumberPurchaseIntentInsert(ctx.AppDB(), intent); err != nil {
		t.Fatal(err)
	}
	result, err := app.purchaseNumber(ctx, intent.Token, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if result["purchased"] != true || result["registration_required"] != true || result["provider_group_id"] != "group-paris" || len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "create_order" {
		t.Fatalf("unexpected DIDWW purchase: %#v calls=%#v", result, platform.integrationCalls)
	}
	if got := platform.integrationCalls[0].Input["external_reference_id"]; got != "telephony-didww-token" {
		t.Fatalf("missing idempotency reference: %v", got)
	}
}

func TestDIDWWPendingOrderIsReconciledWithoutDuplicatePurchase(t *testing.T) {
	platform := &answerPlatform{
		bindings:    map[string]any{"carrier": int64(19)},
		credentials: &sdk.ConnectionCredentials{Slug: "didww", Fields: map[string]string{}},
		integrationResponses: map[string][]json.RawMessage{
			"list_countries": {json.RawMessage(`{"data":[{"id":"country-fr","type":"countries","attributes":{"iso":"FR"}}]}`)},
			"create_order":   {json.RawMessage(`{"data":{"id":"order-2","type":"orders","attributes":{"status":"pending"}}}`)},
			"get_order":      {json.RawMessage(`{"data":{"id":"order-2","type":"orders","attributes":{"status":"completed"}}}`)},
		},
	}
	app, ctx := withTelephonyTestContext(t, platform)
	intent := numberPurchaseIntent{Token: "didww-pending", ProjectID: "project-a", Provider: "didww", CarrierConnectionID: 19, Country: "FR", NumberType: "local", MonthlyPrice: "1.5", Currency: "USD", ProviderGroupID: "group-paris", ProviderSKUID: "sku-1", InventoryMode: "did_group", ExpiresAt: time.Now().UTC().Add(time.Minute)}
	if err := dbNumberPurchaseIntentInsert(ctx.AppDB(), intent); err != nil {
		t.Fatal(err)
	}
	first, err := app.purchaseNumber(ctx, intent.Token, "", "")
	if err != nil || first["order_pending"] != true {
		t.Fatalf("initial order: %#v %v", first, err)
	}
	second, err := app.purchaseNumber(ctx, intent.Token, "", "")
	if err != nil || second["purchased"] != true {
		t.Fatalf("reconciled order: %#v %v", second, err)
	}
	if len(platform.integrationCalls) != 2 || platform.integrationCalls[0].Tool != "create_order" || platform.integrationCalls[1].Tool != "get_order" {
		t.Fatalf("DIDWW order was duplicated: %#v", platform.integrationCalls)
	}
}

func TestDIDWWAddressResourcesUseJSONAPIIds(t *testing.T) {
	platform := &answerPlatform{
		bindings:    map[string]any{"carrier": int64(19)},
		credentials: &sdk.ConnectionCredentials{Slug: "didww", Fields: map[string]string{}},
		integrationResponse: map[string]json.RawMessage{
			"list_countries": json.RawMessage(`{"data":[{"id":"country-fr","type":"countries","attributes":{"iso":"FR"}}]}`),
			"list_addresses": json.RawMessage(`{"data":[{"id":"address-1","type":"addresses","attributes":{"address":"1 Rue de Paris","city_name":"Paris","postal_code":"75001","description":"Flexylead"},"relationships":{"country":{"data":{"type":"countries","id":"country-fr"}}}}],"included":[{"id":"country-fr","type":"countries","attributes":{"iso":"FR"}}]}`),
			"create_address": json.RawMessage(`{"data":{"id":"address-2","type":"addresses","attributes":{"address":"2 Rue de Lyon","city_name":"Paris","postal_code":"75002","description":"Second"}}}`),
		},
	}
	app, ctx := withTelephonyTestContext(t, platform)
	list, err := app.addressesList(ctx, map[string]any{"country": "FR", "limit": 10})
	if err != nil {
		t.Fatal(err)
	}
	addresses := list["addresses"].([]map[string]any)
	if len(addresses) != 1 || addresses[0]["sid"] != "address-1" || addresses[0]["iso_country"] != "FR" {
		t.Fatalf("unexpected DIDWW addresses: %#v", addresses)
	}
	created, err := app.addressCreate(ctx, map[string]any{"country": "FR", "street": "2 Rue de Lyon", "city": "Paris", "postal_code": "75002", "identity_id": "identity-1", "friendly_name": "Second"})
	if err != nil {
		t.Fatal(err)
	}
	if created["provider"] != "didww" || created["address"].(map[string]any)["address_id"] != "address-2" {
		t.Fatalf("unexpected DIDWW created address: %#v", created)
	}
}

func TestDIDWWRegistrationResourcesNormalizeAndGatePurchase(t *testing.T) {
	platform := &answerPlatform{
		bindings:    map[string]any{"carrier": int64(19)},
		credentials: &sdk.ConnectionCredentials{Slug: "didww", Fields: map[string]string{}},
		integrationResponses: map[string][]json.RawMessage{
			"list_countries":             {json.RawMessage(`{"data":[{"id":"country-fr","type":"countries","attributes":{"iso":"FR"}}]}`)},
			"list_identities":            {json.RawMessage(`{"data":[{"id":"identity-1","type":"identities","attributes":{"identity_type":"Business","company_name":"Flexylead","verified":false}}]}`)},
			"create_identity":            {json.RawMessage(`{"data":{"id":"identity-2","type":"identities","attributes":{"identity_type":"Business","company_name":"Flexylead","verified":false}}}`)},
			"list_address_verifications": {json.RawMessage(`{"data":[{"id":"verification-1","type":"address_verifications","attributes":{"status":"Approved","reference":"ABC-1"}}]}`)},
			"get_address_verification":   {json.RawMessage(`{"data":{"id":"verification-1","type":"address_verifications","attributes":{"status":"Approved"},"relationships":{"address":{"data":{"id":"address-1","type":"addresses"}}}}}`)},
		},
	}
	app, ctx := withTelephonyTestContext(t, platform)
	identities, err := app.didwwIdentitiesList(ctx, map[string]any{"limit": 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := identities["identities"].([]map[string]any)[0]["identity_id"]; got != "identity-1" {
		t.Fatalf("identity normalization: %#v", identities)
	}
	created, err := app.didwwIdentityCreate(ctx, map[string]any{"identity_type": "business", "country": "FR", "company_name": "Flexylead"})
	if err != nil || created["identity"].(map[string]any)["identity_id"] != "identity-2" {
		t.Fatalf("identity create: %#v %v", created, err)
	}
	verifications, err := app.didwwAddressVerificationsList(ctx, map[string]any{"status": "approved"})
	if err != nil || verifications["profiles"].([]map[string]any)[0]["status"] != "approved" {
		t.Fatalf("verification normalization: %#v %v", verifications, err)
	}
	platform.integrationResponse = map[string]json.RawMessage{}
	platform.integrationResponse["list_countries"] = json.RawMessage(`{"data":[{"id":"country-fr","type":"countries","attributes":{"iso":"FR"}}]}`)
	platform.integrationResponse["list_identities"] = json.RawMessage(`{"data":[{"id":"identity-1","type":"identities","attributes":{"identity_type":"Business","company_name":"Flexylead","verified":false}}]}`)
	platform.integrationResponse["list_address_verifications"] = json.RawMessage(`{"data":[{"id":"verification-1","type":"address_verifications","attributes":{"status":"Approved","reference":"ABC-1"}}]}`)
	platform.integrationResponse["create_identity"] = json.RawMessage(`{"data":{"id":"identity-2","type":"identities","attributes":{"identity_type":"Business","company_name":"Flexylead","verified":false}}}`)
	profiles, err := app.complianceProfilesList(ctx, map[string]any{"limit": 10})
	if err != nil {
		t.Fatalf("combined DIDWW profile list: %v", err)
	}
	profileList := profiles["profiles"].([]map[string]any)
	if len(profileList) != 2 || profileList[0]["resource_kind"] != "verification" || profileList[1]["resource_kind"] != "identity" {
		t.Fatalf("combined DIDWW profile list: %#v", profileList)
	}
	createdProfile, err := app.complianceProfileCreate(ctx, map[string]any{"country": "FR", "end_user_type": "business", "friendly_name": "Flexylead", "email": "ops@example.test"})
	if err != nil || createdProfile["profile"].(map[string]any)["resource_kind"] != "identity" {
		t.Fatalf("business profile create: %#v %v", createdProfile, err)
	}
	intent := &numberPurchaseIntent{Provider: "didww", ComplianceRequired: true, AddressRequirement: "req-1", CarrierConnectionID: 19}
	if err := validateDIDWWPurchaseResources(ctx, intent, "", ""); err != nil {
		t.Fatalf("documented order-first flow was rejected: %v", err)
	}
	if err := validateDIDWWPurchaseResources(ctx, intent, "address-1", "verification-1"); err == nil {
		t.Fatal("pre-order compliance fields were accepted")
	}
}

func TestDIDWWRequirementLookupUsesSingleResourceEndpoint(t *testing.T) {
	platform := &answerPlatform{
		bindings:    map[string]any{"carrier": int64(19)},
		credentials: &sdk.ConnectionCredentials{Slug: "didww", Fields: map[string]string{}},
		integrationResponse: map[string]json.RawMessage{
			"get_requirement": json.RawMessage(`{"data":{"id":"requirement-fr","type":"requirements","attributes":{"name":"French local"}}}`),
		},
	}
	app, ctx := withTelephonyTestContext(t, platform)
	result, err := app.didwwRequirements(ctx, map[string]any{"requirement_id": "requirement-fr"})
	if err != nil {
		t.Fatal(err)
	}
	if got := result["requirement"].(map[string]any)["id"]; got != "requirement-fr" {
		t.Fatalf("unexpected requirement: %#v", result)
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "get_requirement" {
		t.Fatalf("requirement lookup did not use get_requirement: %#v", platform.integrationCalls)
	}
}

func TestDIDWWRequirementsIncludeAgreementTemplates(t *testing.T) {
	platform := &answerPlatform{
		bindings:    map[string]any{"carrier": int64(19)},
		credentials: &sdk.ConnectionCredentials{Slug: "didww", Fields: map[string]string{}},
		integrationResponse: map[string]json.RawMessage{
			"list_requirements": json.RawMessage(`{"data":[],"included":[{"type":"supporting_document_templates","id":"agreement-fr","attributes":{"name":"France Tripartite Agreement"}}]}`),
		},
	}
	app, ctx := withTelephonyTestContext(t, platform)
	result, err := app.didwwRequirements(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	included := result["included"].([]map[string]any)
	if len(included) != 1 || included[0]["id"] != "agreement-fr" {
		t.Fatalf("agreement metadata lost: %#v", result)
	}
	include := platform.integrationCalls[0].Input["include"].(string)
	for _, relationship := range []string{"business_permanent_document", "business_onetime_document", "personal_permanent_document", "personal_onetime_document"} {
		if !strings.Contains(include, relationship) {
			t.Fatalf("missing agreement relationship %s", relationship)
		}
	}
}
