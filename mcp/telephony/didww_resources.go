package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) didwwResourceProvider(ctx *sdk.AppCtx) (*numberProvider, error) {
	provider, err := a.numberProviderFor(ctx)
	if err != nil {
		return nil, err
	}
	if provider.Slug != "didww" {
		return nil, errors.New("DIDWW regulatory resources require a DIDWW carrier connection")
	}
	return provider, nil
}

func didwwAddressCountryID(ctx *sdk.AppCtx, connID int64, country string) (string, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if len(country) != 2 {
		return "", errors.New("country must be an ISO alpha-2 code")
	}
	return didwwCountryID(ctx, connID, country)
}

func (a *App) didwwAddressesList(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.didwwResourceProvider(ctx)
	if err != nil {
		return nil, err
	}
	input := map[string]any{"page_size": boundedIntArg(args, "limit", 50, 1, 1000), "include": "country,identities"}
	if country := strings.TrimSpace(strArg(args, "country", "")); country != "" {
		id, err := didwwAddressCountryID(ctx, provider.ConnID, country)
		if err != nil {
			return nil, err
		}
		input["country_id"] = id
	}
	if identityID := strings.TrimSpace(firstNonEmpty(strArg(args, "identity_id", ""), strArg(args, "identity_sid", ""))); identityID != "" {
		input["identity_id"] = identityID
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "list_addresses", input)
	if err != nil {
		return nil, err
	}
	var root struct {
		Data     []map[string]any `json:"data"`
		Included []map[string]any `json:"included"`
		Meta     map[string]any   `json:"meta"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode DIDWW addresses: %w", err)
	}
	countries := map[string]string{}
	for _, item := range root.Included {
		if item["type"] != "countries" {
			continue
		}
		attrs, _ := item["attributes"].(map[string]any)
		countries[stringValue(item["id"])] = stringValue(attrs["iso"])
	}
	addresses := make([]map[string]any, 0, len(root.Data))
	for _, item := range root.Data {
		rels, _ := item["relationships"].(map[string]any)
		countryID := ""
		if rel, ok := rels["country"].(map[string]any); ok {
			if data, ok := rel["data"].(map[string]any); ok {
				countryID = stringValue(data["id"])
			}
		}
		addresses = append(addresses, normalizeDIDWWAddress(item, countries[countryID]))
	}
	return map[string]any{"provider": "didww", "addresses": addresses, "meta": root.Meta}, nil
}

func (a *App) didwwRequirements(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.didwwResourceProvider(ctx)
	if err != nil {
		return nil, err
	}
	input := map[string]any{"page_size": boundedIntArg(args, "limit", 100, 1, 1000), "include": "country,did_group_type,personal_proof_types,business_proof_types,address_proof_types,business_permanent_document,business_onetime_document,personal_permanent_document,personal_onetime_document"}
	if country := strings.TrimSpace(strArg(args, "country", "")); country != "" {
		id, e := didwwAddressCountryID(ctx, provider.ConnID, country)
		if e != nil {
			return nil, e
		}
		input["country_id"] = id
	}
	if typ := strings.TrimSpace(strArg(args, "number_type", "")); typ != "" {
		id, e := didwwGroupTypeID(ctx, provider.ConnID, strings.ToLower(typ))
		if e == nil {
			input["did_group_type_id"] = id
		}
	}
	if id := strings.TrimSpace(strArg(args, "requirement_id", "")); id != "" {
		raw, err := executeCarrierTool(ctx, provider.ConnID, "get_requirement", map[string]any{"id": id, "include": input["include"]})
		if err != nil {
			return nil, err
		}
		var single map[string]any
		if err := json.Unmarshal(raw, &single); err != nil {
			return nil, fmt.Errorf("decode DIDWW requirement: %w", err)
		}
		data, _ := single["data"].(map[string]any)
		return map[string]any{"provider": "didww", "requirements": []map[string]any{data}, "requirement": data, "included": single["included"], "meta": single["meta"]}, nil
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "list_requirements", input)
	if err != nil {
		return nil, err
	}
	var root struct {
		Data     []map[string]any `json:"data"`
		Included []map[string]any `json:"included"`
		Meta     map[string]any   `json:"meta"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode DIDWW requirements: %w", err)
	}
	return map[string]any{"provider": "didww", "requirements": root.Data, "included": root.Included, "meta": root.Meta}, nil
}

func (a *App) didwwAddressCreate(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.didwwResourceProvider(ctx)
	if err != nil {
		return nil, err
	}
	city := strings.TrimSpace(firstNonEmpty(strArg(args, "city", ""), strArg(args, "city_name", "")))
	postal := strings.TrimSpace(strArg(args, "postal_code", ""))
	address := strings.TrimSpace(firstNonEmpty(strArg(args, "street", ""), strArg(args, "address", "")))
	country := strings.TrimSpace(strArg(args, "country", ""))
	identityID := strings.TrimSpace(firstNonEmpty(strArg(args, "identity_id", ""), strArg(args, "identity_sid", "")))
	if city == "" || postal == "" || address == "" || country == "" || identityID == "" {
		return nil, errors.New("city, postal_code, street, country, and identity_id are required for a DIDWW address")
	}
	countryID, err := didwwAddressCountryID(ctx, provider.ConnID, country)
	if err != nil {
		return nil, err
	}
	input := map[string]any{"city_name": city, "postal_code": postal, "address": address, "country_id": countryID, "identity_id": identityID}
	if description := strings.TrimSpace(firstNonEmpty(strArg(args, "friendly_name", ""), strArg(args, "description", ""))); description != "" {
		input["description"] = description
	}
	if external := strings.TrimSpace(strArg(args, "external_reference_id", "")); external != "" {
		input["external_reference_id"] = external
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "create_address", input)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode DIDWW address: %w", err)
	}
	data, _ := root["data"].(map[string]any)
	return map[string]any{"provider": "didww", "address": normalizeDIDWWAddress(data, country)}, nil
}

func normalizeDIDWWAddress(item map[string]any, country string) map[string]any {
	attrs, _ := item["attributes"].(map[string]any)
	return map[string]any{
		"sid": stringValue(item["id"]), "address_id": stringValue(item["id"]),
		"friendly_name": stringValue(attrs["description"]), "customer_name": stringValue(attrs["description"]),
		"street": stringValue(attrs["address"]), "city": stringValue(attrs["city_name"]),
		"postal_code": stringValue(attrs["postal_code"]), "iso_country": country,
		"verified": boolValue(attrs["verified"]), "validated": boolValue(attrs["verified"]),
	}
}

func didwwIdentityType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "personal" {
		return "Personal"
	}
	if value == "business" {
		return "Business"
	}
	return ""
}

func normalizeDIDWWIdentity(item map[string]any, country string) map[string]any {
	attrs, _ := item["attributes"].(map[string]any)
	name := strings.TrimSpace(strings.Join([]string{stringValue(attrs["first_name"]), stringValue(attrs["last_name"])}, " "))
	name = firstNonEmpty(stringValue(attrs["company_name"]), name, stringValue(attrs["description"]))
	status := "unverified"
	if boolValue(attrs["verified"]) {
		status = "verified"
	}
	return map[string]any{
		"sid": stringValue(item["id"]), "identity_id": stringValue(item["id"]),
		"friendly_name": name, "identity_type": strings.ToLower(stringValue(attrs["identity_type"])),
		"status": status, "verified": boolValue(attrs["verified"]), "country": country,
		"resource_kind": "identity",
		"company_name":  stringValue(attrs["company_name"]), "first_name": stringValue(attrs["first_name"]),
		"last_name": stringValue(attrs["last_name"]), "contact_email": stringValue(attrs["contact_email"]),
	}
}

func didwwIdentityProfile(identity map[string]any) map[string]any {
	return map[string]any{
		"sid": identity["sid"], "friendly_name": identity["friendly_name"],
		"status": identity["status"], "email": identity["contact_email"],
		"resource_kind": "identity", "identity_id": identity["identity_id"],
	}
}

func (a *App) didwwIdentitiesList(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.didwwResourceProvider(ctx)
	if err != nil {
		return nil, err
	}
	input := map[string]any{"page_size": boundedIntArg(args, "limit", 50, 1, 1000), "include": "country"}
	if typ := didwwIdentityType(strArg(args, "identity_type", "")); typ != "" {
		input["identity_type"] = typ
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "list_identities", input)
	if err != nil {
		return nil, err
	}
	var root struct {
		Data     []map[string]any `json:"data"`
		Included []map[string]any `json:"included"`
		Meta     map[string]any   `json:"meta"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode DIDWW identities: %w", err)
	}
	countries := map[string]string{}
	for _, item := range root.Included {
		if item["type"] == "countries" {
			attrs, _ := item["attributes"].(map[string]any)
			countries[stringValue(item["id"])] = stringValue(attrs["iso"])
		}
	}
	identities := make([]map[string]any, 0, len(root.Data))
	for _, item := range root.Data {
		rels, _ := item["relationships"].(map[string]any)
		country := ""
		if rel, ok := rels["country"].(map[string]any); ok {
			if d, ok := rel["data"].(map[string]any); ok {
				country = countries[stringValue(d["id"])]
			}
		}
		identities = append(identities, normalizeDIDWWIdentity(item, country))
	}
	return map[string]any{"provider": "didww", "identities": identities, "profiles": identities, "meta": root.Meta}, nil
}

func (a *App) didwwIdentityCreate(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.didwwResourceProvider(ctx)
	if err != nil {
		return nil, err
	}
	typ := didwwIdentityType(strArg(args, "identity_type", ""))
	if typ == "" {
		return nil, errors.New("identity_type must be personal or business")
	}
	country := strings.TrimSpace(strArg(args, "country", ""))
	countryID := strings.TrimSpace(strArg(args, "country_id", ""))
	if countryID == "" {
		if country == "" {
			return nil, errors.New("country or country_id is required")
		}
		countryID, err = didwwAddressCountryID(ctx, provider.ConnID, country)
		if err != nil {
			return nil, err
		}
	}
	attrs := map[string]any{"identity_type": typ}
	for _, key := range []string{"first_name", "last_name", "phone_number", "id_number", "birth_date", "company_name", "company_reg_number", "vat_id", "personal_tax_id", "contact_email", "description", "external_reference_id"} {
		if value := strings.TrimSpace(strArg(args, key, "")); value != "" {
			attrs[key] = value
		}
	}
	input := map[string]any{"identity_type": typ, "country_id": countryID}
	for key, value := range attrs {
		input[key] = value
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "create_identity", input)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode DIDWW identity: %w", err)
	}
	data, _ := root["data"].(map[string]any)
	return map[string]any{"provider": "didww", "identity": normalizeDIDWWIdentity(data, country)}, nil
}

func (a *App) didwwIdentityGet(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.didwwResourceProvider(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(firstNonEmpty(strArg(args, "id", ""), strArg(args, "identity_id", "")))
	if id == "" {
		return nil, errors.New("identity_id is required")
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "get_identity", map[string]any{"id": id, "include": "country,proofs,addresses"})
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode DIDWW identity: %w", err)
	}
	data, _ := root["data"].(map[string]any)
	identity := normalizeDIDWWIdentity(data, "")
	profile := didwwIdentityProfile(identity)
	return map[string]any{"provider": "didww", "identity": identity, "profile": profile, "bundle": profile, "items": root["included"], "raw": root}, nil
}

func didwwVerificationStatus(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func normalizeDIDWWVerification(item map[string]any) map[string]any {
	attrs, _ := item["attributes"].(map[string]any)
	rels, _ := item["relationships"].(map[string]any)
	addressID, identityID := "", ""
	if rel, ok := rels["address"].(map[string]any); ok {
		if d, ok := rel["data"].(map[string]any); ok {
			addressID = stringValue(d["id"])
		}
	}
	if rel, ok := rels["identity"].(map[string]any); ok {
		if d, ok := rel["data"].(map[string]any); ok {
			identityID = stringValue(d["id"])
		}
	}
	return map[string]any{"sid": stringValue(item["id"]), "verification_id": stringValue(item["id"]), "friendly_name": firstNonEmpty(stringValue(attrs["service_description"]), stringValue(attrs["reference"]), stringValue(item["id"])), "status": didwwVerificationStatus(stringValue(attrs["status"])), "resource_kind": "verification", "reject_reasons": stringValue(attrs["reject_reasons"]), "reference": stringValue(attrs["reference"]), "address_id": addressID, "identity_id": identityID, "created_at": stringValue(attrs["created_at"])}
}

func (a *App) didwwAddressVerificationsList(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.didwwResourceProvider(ctx)
	if err != nil {
		return nil, err
	}
	input := map[string]any{"page_size": boundedIntArg(args, "limit", 50, 1, 1000), "include": "address,dids"}
	if status := strings.TrimSpace(strArg(args, "status", "")); status != "" {
		input["status"] = strings.Title(strings.ToLower(status))
	}
	if addressID := strings.TrimSpace(strArg(args, "address_id", "")); addressID != "" {
		input["address_id"] = addressID
	}
	if identityID := strings.TrimSpace(strArg(args, "identity_id", "")); identityID != "" {
		input["identity_id"] = identityID
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "list_address_verifications", input)
	if err != nil {
		return nil, err
	}
	var root struct {
		Data []map[string]any `json:"data"`
		Meta map[string]any   `json:"meta"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode DIDWW address verifications: %w", err)
	}
	values := make([]map[string]any, 0, len(root.Data))
	for _, item := range root.Data {
		values = append(values, normalizeDIDWWVerification(item))
	}
	return map[string]any{"provider": "didww", "verifications": values, "profiles": values, "bundles": values, "meta": root.Meta}, nil
}

func (a *App) didwwAddressVerificationGet(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.didwwResourceProvider(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(firstNonEmpty(strArg(args, "id", ""), strArg(args, "compliance_id", ""), strArg(args, "verification_id", "")))
	if id == "" {
		return nil, errors.New("verification_id is required")
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "get_address_verification", map[string]any{"id": id, "include": "address,dids"})
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode DIDWW address verification: %w", err)
	}
	data, _ := root["data"].(map[string]any)
	verification := normalizeDIDWWVerification(data)
	return map[string]any{"provider": "didww", "verification": verification, "profile": verification, "bundle": verification, "items": root["included"], "raw": root}, nil
}

func (a *App) didwwAddressVerificationCreate(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.didwwResourceProvider(ctx)
	if err != nil {
		return nil, err
	}
	addressID := strings.TrimSpace(strArg(args, "address_id", ""))
	if addressID == "" {
		return nil, errors.New("address_id is required")
	}
	didIDs := stringListArg(args, "did_ids")
	if didID := strings.TrimSpace(strArg(args, "did_id", "")); didID != "" {
		didIDs = append(didIDs, didID)
	}
	if len(didIDs) == 0 {
		return nil, errors.New("did_id or did_ids is required")
	}
	input := map[string]any{"address_id": addressID, "did_ids": didIDs}
	if ids := stringListArg(args, "onetime_file_ids"); len(ids) > 0 {
		input["onetime_file_ids"] = ids
	}
	for _, key := range []string{"service_description", "callback_url", "callback_method", "external_reference_id"} {
		if value := strings.TrimSpace(strArg(args, key, "")); value != "" {
			input[key] = value
		}
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "create_address_verification", input)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode DIDWW address verification: %w", err)
	}
	data, _ := root["data"].(map[string]any)
	verification := normalizeDIDWWVerification(data)
	return map[string]any{"provider": "didww", "verification": verification, "profile": verification, "bundle": verification}, nil
}
