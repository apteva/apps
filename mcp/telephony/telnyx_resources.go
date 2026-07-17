package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) telnyxNumberProvider(ctx *sdk.AppCtx) (*numberProvider, error) {
	provider, err := a.numberProviderFor(ctx)
	if err != nil {
		return nil, err
	}
	if provider.Slug != "telnyx" {
		return nil, fmt.Errorf("this resource workflow requires a Telnyx carrier binding; bound provider is %s", provider.Slug)
	}
	return provider, nil
}

func telnyxResponse(raw json.RawMessage) (map[string]any, error) {
	var response any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("decode Telnyx response: %w", err)
	}
	switch value := response.(type) {
	case map[string]any:
		return value, nil
	case []any:
		return map[string]any{"data": value}, nil
	default:
		return nil, errors.New("decode Telnyx response: expected an object or array")
	}
}

func telnyxDataMap(response map[string]any) map[string]any {
	if data, ok := response["data"].(map[string]any); ok {
		return data
	}
	return response
}

func telnyxDataList(response map[string]any) []any {
	if data, ok := response["data"].([]any); ok {
		return data
	}
	return []any{}
}

func validProviderResourceID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, "/?#\r\n\t")
}

func telnyxNumberType(value string) (string, error) {
	value = normalizeNumberType(value)
	switch value {
	case "local", "mobile", "national", "toll_free", "shared_cost":
		return value, nil
	default:
		return "", errors.New("number_type must be local, mobile, national, toll_free, or shared_cost")
	}
}

func normalizeTelnyxAddress(raw map[string]any) map[string]any {
	address := make(map[string]any, len(raw)+10)
	for key, value := range raw {
		address[key] = value
	}
	id := stringValue(raw["id"])
	name := firstNonEmpty(stringValue(raw["business_name"]), strings.TrimSpace(stringValue(raw["first_name"])+" "+stringValue(raw["last_name"])))
	address["sid"], address["address_id"] = id, id
	address["friendly_name"] = stringValue(raw["customer_reference"])
	address["customer_name"] = name
	address["street"] = stringValue(raw["street_address"])
	address["street_secondary"] = stringValue(raw["extended_address"])
	address["city"] = stringValue(raw["locality"])
	address["region"] = stringValue(raw["administrative_area"])
	address["iso_country"] = stringValue(raw["country_code"])
	address["validated"] = raw["validate_address"]
	return address
}

func normalizeTelnyxProfile(raw map[string]any) map[string]any {
	profile := make(map[string]any, len(raw)+8)
	for key, value := range raw {
		profile[key] = value
	}
	id := stringValue(raw["id"])
	profile["sid"], profile["compliance_id"] = id, id
	profile["friendly_name"] = stringValue(raw["customer_reference"])
	profile["iso_country"] = stringValue(raw["country_code"])
	profile["number_type"] = stringValue(raw["phone_number_type"])
	return profile
}

func (a *App) telnyxAddressesList(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.telnyxNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	input := map[string]any{
		"page[size]": boundedIntArg(args, "limit", 50, 1, 250),
	}
	if name := strings.TrimSpace(strArg(args, "customer_name", "")); name != "" {
		input["filter[customer_reference][contains]"] = name
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "list_addresses", input)
	if err != nil {
		return nil, err
	}
	response, err := telnyxResponse(raw)
	if err != nil {
		return nil, err
	}
	addresses := make([]map[string]any, 0)
	country := strings.ToUpper(strings.TrimSpace(strArg(args, "country", "")))
	for _, item := range telnyxDataList(response) {
		entry, _ := item.(map[string]any)
		if country != "" && !strings.EqualFold(stringValue(entry["country_code"]), country) {
			continue
		}
		addresses = append(addresses, normalizeTelnyxAddress(entry))
	}
	return map[string]any{"provider": "telnyx", "addresses": addresses, "meta": response["meta"]}, nil
}

func (a *App) telnyxAddressCreate(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.telnyxNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	country := strings.ToUpper(strings.TrimSpace(strArg(args, "country", "")))
	if len(country) != 2 {
		return nil, errors.New("country must be an ISO alpha-2 code")
	}
	input := map[string]any{
		"street_address":   strings.TrimSpace(strArg(args, "street", "")),
		"locality":         strings.TrimSpace(strArg(args, "city", "")),
		"country_code":     country,
		"address_book":     true,
		"validate_address": boolArg(args, "auto_correct", true),
	}
	if input["street_address"] == "" || input["locality"] == "" {
		return nil, errors.New("street and city are required")
	}
	businessName := strings.TrimSpace(firstNonEmpty(strArg(args, "business_name", ""), strArg(args, "customer_name", "")))
	firstName := strings.TrimSpace(strArg(args, "first_name", ""))
	lastName := strings.TrimSpace(strArg(args, "last_name", ""))
	if businessName == "" && (firstName == "" || lastName == "") {
		return nil, errors.New("business_name/customer_name or first_name and last_name are required")
	}
	if businessName != "" {
		input["business_name"] = businessName
	} else {
		input["first_name"], input["last_name"] = firstName, lastName
	}
	for local, remote := range map[string]string{
		"friendly_name": "customer_reference", "phone_number": "phone_number",
		"street_secondary": "extended_address", "region": "administrative_area",
		"postal_code": "postal_code", "neighborhood": "neighborhood", "borough": "borough",
	} {
		if value := strings.TrimSpace(strArg(args, local, "")); value != "" {
			input[remote] = value
		}
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "create_address", input)
	if err != nil {
		return nil, err
	}
	response, err := telnyxResponse(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": "telnyx", "address": normalizeTelnyxAddress(telnyxDataMap(response))}, nil
}

func (a *App) telnyxRegulatoryRequirements(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.telnyxNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	country := strings.ToUpper(strings.TrimSpace(strArg(args, "country", "")))
	if len(country) != 2 {
		return nil, errors.New("country must be an ISO alpha-2 code")
	}
	numberType, err := telnyxNumberType(strArg(args, "number_type", ""))
	if err != nil {
		return nil, err
	}
	input := map[string]any{
		"filter[country_code]":      country,
		"filter[phone_number_type]": numberType,
		"filter[action]":            "ordering",
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "list_regulatory_requirements", input)
	if err != nil {
		return nil, err
	}
	response, err := telnyxResponse(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"provider": "telnyx", "country": country, "number_type": numberType,
		"action": "ordering", "requirements": response["data"], "regulations": response["data"], "meta": response["meta"],
	}, nil
}

func (a *App) telnyxProfilesList(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.telnyxNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	input := map[string]any{"page[size]": boundedIntArg(args, "limit", 50, 1, 250)}
	for local, remote := range map[string]string{
		"country": "filter[country_code]", "number_type": "filter[phone_number_type]",
		"status": "filter[status]", "friendly_name": "filter[customer_reference]",
	} {
		if value := strings.TrimSpace(strArg(args, local, "")); value != "" {
			if local == "country" {
				value = strings.ToUpper(value)
			}
			input[remote] = value
		}
	}
	input["filter[action]"] = "ordering"
	raw, err := executeCarrierTool(ctx, provider.ConnID, "list_requirement_groups", input)
	if err != nil {
		return nil, err
	}
	response, err := telnyxResponse(raw)
	if err != nil {
		return nil, err
	}
	profiles := make([]map[string]any, 0)
	for _, item := range telnyxDataList(response) {
		entry, _ := item.(map[string]any)
		profiles = append(profiles, normalizeTelnyxProfile(entry))
	}
	return map[string]any{"provider": "telnyx", "profiles": profiles, "bundles": profiles, "meta": response["meta"]}, nil
}

func (a *App) telnyxProfileCreate(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.telnyxNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	country := strings.ToUpper(strings.TrimSpace(strArg(args, "country", "")))
	if len(country) != 2 {
		return nil, errors.New("country must be an ISO alpha-2 code")
	}
	numberType, err := telnyxNumberType(strArg(args, "number_type", ""))
	if err != nil {
		return nil, err
	}
	input := map[string]any{"country_code": country, "phone_number_type": numberType, "action": "ordering"}
	if name := strings.TrimSpace(strArg(args, "friendly_name", "")); name != "" {
		input["customer_reference"] = name
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "create_requirement_group", input)
	if err != nil {
		return nil, err
	}
	response, err := telnyxResponse(raw)
	if err != nil {
		return nil, err
	}
	profile := normalizeTelnyxProfile(telnyxDataMap(response))
	return map[string]any{"provider": "telnyx", "profile": profile, "bundle": profile}, nil
}

func telnyxProfileID(args map[string]any) (string, error) {
	id := strings.TrimSpace(firstNonEmpty(strArg(args, "compliance_id", ""), strArg(args, "bundle_sid", "")))
	if !validProviderResourceID(id) {
		return "", errors.New("compliance_id must be a valid provider compliance profile ID")
	}
	return id, nil
}

func (a *App) telnyxProfile(ctx *sdk.AppCtx, provider *numberProvider, id string) (map[string]any, error) {
	raw, err := executeCarrierTool(ctx, provider.ConnID, "get_requirement_group", map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	response, err := telnyxResponse(raw)
	if err != nil {
		return nil, err
	}
	profile := telnyxDataMap(response)
	if stringValue(profile["id"]) != id {
		return nil, errors.New("Telnyx did not return the selected compliance profile")
	}
	return profile, nil
}

func (a *App) telnyxProfileGet(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.telnyxNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	id, err := telnyxProfileID(args)
	if err != nil {
		return nil, err
	}
	rawProfile, err := a.telnyxProfile(ctx, provider, id)
	if err != nil {
		return nil, err
	}
	profile := normalizeTelnyxProfile(rawProfile)
	return map[string]any{
		"provider": "telnyx", "profile": profile, "bundle": profile,
		"requirements": rawProfile["regulatory_requirements"], "items": rawProfile["regulatory_requirements"],
	}, nil
}

func telnyxDocumentBase64(value string) (string, error) {
	value = strings.TrimSpace(value)
	if marker := strings.Index(value, ";base64,"); marker >= 0 {
		value = value[marker+8:]
	}
	if value == "" {
		return "", errors.New("file is empty")
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", errors.New("file must be base64 encoded or a base64 data URL")
	}
	if len(decoded) > 20<<20 {
		return "", errors.New("file exceeds the Telnyx 20 MB upload limit")
	}
	return value, nil
}

func (a *App) telnyxRequirementSet(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.telnyxNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	id, err := telnyxProfileID(args)
	if err != nil {
		return nil, err
	}
	requirementID := strings.TrimSpace(strArg(args, "requirement_id", ""))
	if !validProviderResourceID(requirementID) {
		return nil, errors.New("requirement_id is required")
	}
	fieldValue := strings.TrimSpace(strArg(args, "field_value", ""))
	var document map[string]any
	if file := strArg(args, "file", ""); file != "" {
		encoded, decodeErr := telnyxDocumentBase64(file)
		if decodeErr != nil {
			return nil, decodeErr
		}
		filename := strings.TrimSpace(strArg(args, "file_name", ""))
		if filename == "" || len(filename) > 512 || strings.ContainsAny(filename, "\r\n") {
			return nil, errors.New("file_name is required and must be at most 512 characters")
		}
		uploadRaw, uploadErr := executeCarrierTool(ctx, provider.ConnID, "upload_document", map[string]any{
			"file": encoded, "filename": filename, "customer_reference": id,
		})
		if uploadErr != nil {
			return nil, uploadErr
		}
		uploadResponse, decodeErr := telnyxResponse(uploadRaw)
		if decodeErr != nil {
			return nil, decodeErr
		}
		document = telnyxDataMap(uploadResponse)
		fieldValue = stringValue(document["id"])
	}
	if fieldValue == "" {
		return nil, errors.New("field_value or file is required")
	}
	profile, err := a.telnyxProfile(ctx, provider, id)
	if err != nil {
		return nil, err
	}
	values := make([]map[string]any, 0)
	found := false
	if existing, ok := profile["regulatory_requirements"].([]any); ok {
		for _, item := range existing {
			entry, _ := item.(map[string]any)
			entryID := firstNonEmpty(stringValue(entry["requirement_id"]), stringValue(entry["id"]))
			value := stringValue(entry["field_value"])
			if entryID == requirementID {
				value, found = fieldValue, true
			}
			if entryID != "" {
				values = append(values, map[string]any{"requirement_id": entryID, "field_value": value})
			}
		}
	}
	if !found {
		return nil, errors.New("requirement_id does not belong to this compliance profile")
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "update_requirement_group", map[string]any{
		"id": id, "regulatory_requirements": values,
	})
	if err != nil {
		return nil, err
	}
	response, err := telnyxResponse(raw)
	if err != nil {
		return nil, err
	}
	updated := normalizeTelnyxProfile(telnyxDataMap(response))
	return map[string]any{
		"provider": "telnyx", "compliance_id": id, "bundle_sid": id,
		"requirement_id": requirementID, "field_value": fieldValue, "document": document,
		"profile": updated, "bundle": updated,
	}, nil
}

func telnyxProfileEvaluation(profile map[string]any) map[string]any {
	missing := make([]map[string]any, 0)
	if requirements, ok := profile["regulatory_requirements"].([]any); ok {
		for _, item := range requirements {
			entry, _ := item.(map[string]any)
			if strings.TrimSpace(stringValue(entry["field_value"])) == "" {
				missing = append(missing, map[string]any{
					"requirement_id": firstNonEmpty(stringValue(entry["requirement_id"]), stringValue(entry["id"])),
					"field_type":     entry["field_type"],
				})
			}
		}
	}
	status := strings.ToLower(stringValue(profile["status"]))
	usable := status != "no-longer-eligible"
	compliant := len(missing) == 0 && usable
	result := "incomplete"
	if compliant {
		result = "compliant"
	}
	return map[string]any{"status": result, "provider_status": status, "usable_for_order": compliant, "missing": missing}
}

func (a *App) telnyxProfileEvaluate(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.telnyxNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	id, err := telnyxProfileID(args)
	if err != nil {
		return nil, err
	}
	profile, err := a.telnyxProfile(ctx, provider, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"provider": "telnyx", "compliance_id": id, "bundle_sid": id,
		"evaluation": telnyxProfileEvaluation(profile), "profile": normalizeTelnyxProfile(profile),
	}, nil
}

func (a *App) telnyxProfileSubmit(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.telnyxNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	id, err := telnyxProfileID(args)
	if err != nil {
		return nil, err
	}
	profile, err := a.telnyxProfile(ctx, provider, id)
	if err != nil {
		return nil, err
	}
	evaluation := telnyxProfileEvaluation(profile)
	if evaluation["status"] != "compliant" {
		return map[string]any{
			"provider": "telnyx", "submitted": false, "compliance_id": id,
			"reason": "compliance profile is incomplete", "evaluation": evaluation,
		}, nil
	}
	_, err = executeCarrierTool(ctx, provider.ConnID, "submit_requirement_group", map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	submittedProfile, err := a.telnyxProfile(ctx, provider, id)
	if err != nil {
		return nil, err
	}
	updated := normalizeTelnyxProfile(submittedProfile)
	return map[string]any{
		"provider": "telnyx", "submitted": true, "compliance_id": id,
		"profile": updated, "bundle": updated, "evaluation": evaluation,
	}, nil
}

func validateTelnyxPurchaseProfile(ctx *sdk.AppCtx, intent *numberPurchaseIntent, complianceID string) error {
	if complianceID == "" {
		if intent.ComplianceRequired {
			return errors.New("compliance_id required because Telnyx reported unmet regulatory requirements for this number")
		}
		return nil
	}
	if !validProviderResourceID(complianceID) {
		return errors.New("compliance_id must be a valid Telnyx requirement group ID")
	}
	raw, err := executeCarrierTool(ctx, intent.CarrierConnectionID, "get_requirement_group", map[string]any{"id": complianceID})
	if err != nil {
		return fmt.Errorf("validate compliance_id: %w", err)
	}
	response, err := telnyxResponse(raw)
	if err != nil {
		return err
	}
	profile := telnyxDataMap(response)
	if stringValue(profile["id"]) != complianceID {
		return errors.New("Telnyx did not return the selected compliance profile")
	}
	if !strings.EqualFold(stringValue(profile["country_code"]), intent.Country) {
		return fmt.Errorf("compliance_id is for %s, not %s", firstNonEmpty(stringValue(profile["country_code"]), "an unknown country"), intent.Country)
	}
	if normalizeNumberType(stringValue(profile["phone_number_type"])) != normalizeNumberType(intent.NumberType) {
		return fmt.Errorf("compliance_id is for %s numbers, not %s", firstNonEmpty(stringValue(profile["phone_number_type"]), "an unknown type"), intent.NumberType)
	}
	if !strings.EqualFold(stringValue(profile["action"]), "ordering") {
		return errors.New("compliance_id is not an ordering requirement group")
	}
	evaluation := telnyxProfileEvaluation(profile)
	if evaluation["status"] != "compliant" {
		return errors.New("compliance_id is incomplete or no longer eligible")
	}
	return nil
}
