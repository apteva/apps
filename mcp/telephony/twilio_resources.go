package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func resourceToolResult(result map[string]any, err error) (any, error) {
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return result, nil
}

func (a *App) toolAddressesList(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return resourceToolResult(a.twilioAddressesList(ctx, args))
}

func (a *App) toolAddressCreate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return resourceToolResult(a.twilioAddressCreate(ctx, args))
}

func (a *App) toolRegulatoryRequirements(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return resourceToolResult(a.twilioRegulatoryRequirements(ctx, args))
}

func (a *App) toolRegulatoryBundlesList(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return resourceToolResult(a.twilioBundlesList(ctx, args))
}

func (a *App) toolRegulatoryBundleCreate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return resourceToolResult(a.twilioBundleCreate(ctx, args))
}

func (a *App) toolRegulatoryBundleGet(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return resourceToolResult(a.twilioBundleGet(ctx, args))
}

func (a *App) toolRegulatoryBundleItemCreate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return resourceToolResult(a.twilioBundleItemCreate(ctx, args))
}

func (a *App) toolRegulatoryBundleEvaluate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return resourceToolResult(a.twilioBundleEvaluate(ctx, args))
}

func (a *App) toolRegulatoryBundleSubmit(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return resourceToolResult(a.twilioBundleSubmit(ctx, args))
}

func validTwilioBundleSID(value string) bool { return validTwilioSID(value, "BU") }

func validTwilioRegulationSID(value string) bool { return validTwilioSID(value, "RN") }

func validTwilioObjectSID(value string) bool {
	return validTwilioSID(value, "IT") || validTwilioSID(value, "RD")
}

func validTwilioSID(value, prefix string) bool {
	if len(value) != 34 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, char := range value[2:] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

func (a *App) twilioNumberProvider(ctx *sdk.AppCtx) (*numberProvider, error) {
	provider, err := a.numberProviderFor(ctx)
	if err != nil {
		return nil, err
	}
	if provider.Slug != "twilio" {
		return nil, fmt.Errorf("this resource workflow currently requires a Twilio carrier binding; bound provider is %s", provider.Slug)
	}
	return provider, nil
}

func twilioResponse(raw json.RawMessage) (map[string]any, error) {
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode Twilio response: %w", err)
	}
	return response, nil
}

func twilioAttributes(args map[string]any) (string, error) {
	value, ok := args["attributes"]
	if !ok || value == nil {
		return "", errors.New("attributes required; use the exact fields returned by telephony_regulatory_requirements")
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		var decoded map[string]any
		if text == "" || json.Unmarshal([]byte(text), &decoded) != nil {
			return "", errors.New("attributes must be a JSON object")
		}
		return text, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || encoded[0] != '{' {
		return "", errors.New("attributes must be an object")
	}
	return string(encoded), nil
}

func twilioNumberType(value string) (string, error) {
	value = normalizeNumberType(value)
	if value == "toll_free" {
		return "toll-free", nil
	}
	switch value {
	case "local", "mobile", "national":
		return value, nil
	default:
		return "", errors.New("number_type must be local, mobile, national, or toll_free")
	}
}

func (a *App) twilioAddressesList(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.twilioNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	input := map[string]any{"PageSize": boundedIntArg(args, "limit", 50, 1, 1000)}
	if country := strings.ToUpper(strings.TrimSpace(strArg(args, "country", ""))); country != "" {
		if len(country) != 2 {
			return nil, errors.New("country must be an ISO alpha-2 code")
		}
		input["IsoCountry"] = country
	}
	if name := strings.TrimSpace(strArg(args, "customer_name", "")); name != "" {
		input["CustomerName"] = name
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "list_addresses", input)
	if err != nil {
		return nil, err
	}
	response, err := twilioResponse(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": "twilio", "addresses": response["addresses"], "meta": response["meta"]}, nil
}

func (a *App) twilioAddressCreate(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.twilioNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	required := map[string]string{
		"customer_name": "CustomerName", "street": "Street", "city": "City",
		"region": "Region", "postal_code": "PostalCode", "country": "IsoCountry",
	}
	input := map[string]any{"AutoCorrectAddress": boolArg(args, "auto_correct", true)}
	for local, remote := range required {
		value := strings.TrimSpace(strArg(args, local, ""))
		if value == "" {
			return nil, fmt.Errorf("%s required", local)
		}
		input[remote] = value
	}
	country := strings.ToUpper(stringValue(input["IsoCountry"]))
	if len(country) != 2 {
		return nil, errors.New("country must be an ISO alpha-2 code")
	}
	input["IsoCountry"] = country
	if value := strings.TrimSpace(strArg(args, "street_secondary", "")); value != "" {
		input["StreetSecondary"] = value
	}
	if value := strings.TrimSpace(strArg(args, "friendly_name", "")); value != "" {
		input["FriendlyName"] = value
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "create_address", input)
	if err != nil {
		return nil, err
	}
	address, err := twilioResponse(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": "twilio", "address": address}, nil
}

func (a *App) twilioRegulatoryRequirements(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.twilioNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	country := strings.ToUpper(strings.TrimSpace(strArg(args, "country", "")))
	if len(country) != 2 {
		return nil, errors.New("country must be an ISO alpha-2 code")
	}
	numberType, err := twilioNumberType(strArg(args, "number_type", ""))
	if err != nil {
		return nil, err
	}
	endUserType := strings.ToLower(strings.TrimSpace(strArg(args, "end_user_type", "")))
	if endUserType != "individual" && endUserType != "business" {
		return nil, errors.New("end_user_type must be individual or business")
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "list_regulations", map[string]any{
		"IsoCountry": country, "NumberType": numberType, "EndUserType": endUserType,
		"IncludeConstraints": true, "PageSize": 50,
	})
	if err != nil {
		return nil, err
	}
	response, err := twilioResponse(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"provider": "twilio", "country": country, "number_type": numberType,
		"end_user_type": endUserType, "regulations": response["results"], "meta": response["meta"],
	}, nil
}

func (a *App) twilioBundlesList(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.twilioNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	input := map[string]any{"PageSize": boundedIntArg(args, "limit", 50, 1, 1000)}
	for local, remote := range map[string]string{
		"status": "Status", "country": "IsoCountry", "end_user_type": "EndUserType", "friendly_name": "FriendlyName",
	} {
		if value := strings.TrimSpace(strArg(args, local, "")); value != "" {
			if local == "country" {
				value = strings.ToUpper(value)
			}
			input[remote] = value
		}
	}
	if value := strings.TrimSpace(strArg(args, "number_type", "")); value != "" {
		numberType, err := twilioNumberType(value)
		if err != nil {
			return nil, err
		}
		input["NumberType"] = numberType
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "list_regulatory_bundles", input)
	if err != nil {
		return nil, err
	}
	response, err := twilioResponse(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": "twilio", "bundles": response["results"], "meta": response["meta"]}, nil
}

func (a *App) twilioBundleCreate(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.twilioNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	friendlyName := strings.TrimSpace(strArg(args, "friendly_name", ""))
	email := strings.TrimSpace(strArg(args, "email", ""))
	if friendlyName == "" || email == "" {
		return nil, errors.New("friendly_name and email are required")
	}
	parsedEmail, err := mail.ParseAddress(email)
	if err != nil || !strings.EqualFold(parsedEmail.Address, email) {
		return nil, errors.New("email must be a valid plain email address")
	}
	input := map[string]any{"FriendlyName": friendlyName, "Email": email}
	if callback := strings.TrimSpace(strArg(args, "status_callback", "")); callback != "" {
		input["StatusCallback"] = callback
	}
	regulationSID := strings.TrimSpace(strArg(args, "regulation_sid", ""))
	if regulationSID != "" {
		if !validTwilioRegulationSID(regulationSID) {
			return nil, errors.New("regulation_sid must be a Twilio Regulation SID beginning with RN")
		}
		input["RegulationSid"] = regulationSID
	} else {
		country := strings.ToUpper(strings.TrimSpace(strArg(args, "country", "")))
		if len(country) != 2 {
			return nil, errors.New("country must be an ISO alpha-2 code when regulation_sid is omitted")
		}
		numberType, err := twilioNumberType(strArg(args, "number_type", ""))
		if err != nil {
			return nil, err
		}
		endUserType := strings.ToLower(strings.TrimSpace(strArg(args, "end_user_type", "")))
		if endUserType != "individual" && endUserType != "business" {
			return nil, errors.New("end_user_type must be individual or business")
		}
		input["IsoCountry"], input["NumberType"], input["EndUserType"] = country, numberType, endUserType
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "create_regulatory_bundle", input)
	if err != nil {
		return nil, err
	}
	bundle, err := twilioResponse(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": "twilio", "bundle": bundle}, nil
}

func (a *App) twilioBundleGet(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.twilioNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	bundleSID := strings.TrimSpace(strArg(args, "bundle_sid", ""))
	if !validTwilioBundleSID(bundleSID) {
		return nil, errors.New("bundle_sid must be a Twilio Regulatory Bundle SID beginning with BU")
	}
	bundleRaw, err := executeCarrierTool(ctx, provider.ConnID, "get_regulatory_bundle", map[string]any{"BundleSid": bundleSID})
	if err != nil {
		return nil, err
	}
	bundle, err := twilioResponse(bundleRaw)
	if err != nil {
		return nil, err
	}
	itemsRaw, err := executeCarrierTool(ctx, provider.ConnID, "list_bundle_items", map[string]any{"BundleSid": bundleSID, "PageSize": 100})
	if err != nil {
		return nil, err
	}
	items, err := twilioResponse(itemsRaw)
	if err != nil {
		return nil, err
	}
	var regulation any
	if regulationSID := stringValue(bundle["regulation_sid"]); validTwilioRegulationSID(regulationSID) {
		regulationRaw, callErr := executeCarrierTool(ctx, provider.ConnID, "get_regulation", map[string]any{"RegulationSid": regulationSID, "IncludeConstraints": true})
		if callErr != nil {
			return nil, callErr
		}
		regulation, err = twilioResponse(regulationRaw)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"provider": "twilio", "bundle": bundle, "regulation": regulation, "items": items["results"]}, nil
}

func (a *App) twilioBundleItemCreate(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.twilioNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	bundleSID := strings.TrimSpace(strArg(args, "bundle_sid", ""))
	if !validTwilioBundleSID(bundleSID) {
		return nil, errors.New("bundle_sid must be a Twilio Regulatory Bundle SID beginning with BU")
	}
	kind := strings.ToLower(strings.TrimSpace(strArg(args, "kind", "")))
	friendlyName := strings.TrimSpace(strArg(args, "friendly_name", ""))
	itemType := strings.ToLower(strings.TrimSpace(strArg(args, "type", "")))
	if friendlyName == "" || itemType == "" {
		return nil, errors.New("friendly_name and type are required")
	}
	attributes, err := twilioAttributes(args)
	if err != nil {
		return nil, err
	}
	var itemRaw json.RawMessage
	switch kind {
	case "end_user":
		if itemType != "individual" && itemType != "business" {
			return nil, errors.New("end-user type must be individual or business")
		}
		itemRaw, err = executeCarrierTool(ctx, provider.ConnID, "create_regulatory_end_user", map[string]any{
			"FriendlyName": friendlyName, "Type": itemType, "Attributes": attributes,
		})
	case "document":
		input := map[string]any{"FriendlyName": friendlyName, "Type": itemType, "Attributes": attributes}
		if file := strArg(args, "file", ""); file != "" {
			if len(file) > 7<<20 {
				return nil, errors.New("file exceeds the Twilio 5 MB decoded upload limit")
			}
			input["File"] = file
			input["File_filename"] = strings.TrimSpace(strArg(args, "file_name", "document"))
			itemRaw, err = executeCarrierTool(ctx, provider.ConnID, "upload_regulatory_document", input)
		} else {
			itemRaw, err = executeCarrierTool(ctx, provider.ConnID, "create_regulatory_document", input)
		}
	default:
		return nil, errors.New("kind must be end_user or document")
	}
	if err != nil {
		return nil, err
	}
	item, err := twilioResponse(itemRaw)
	if err != nil {
		return nil, err
	}
	objectSID := stringValue(item["sid"])
	if !validTwilioObjectSID(objectSID) {
		return nil, errors.New("Twilio created the resource but returned no usable item SID; inspect the provider account before retrying")
	}
	assignmentRaw, err := executeCarrierTool(ctx, provider.ConnID, "assign_bundle_item", map[string]any{"BundleSid": bundleSID, "ObjectSid": objectSID})
	if err != nil {
		return nil, fmt.Errorf("created %s but failed to assign it to bundle %s: %w", objectSID, bundleSID, err)
	}
	assignment, err := twilioResponse(assignmentRaw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": "twilio", "bundle_sid": bundleSID, "item": item, "assignment": assignment}, nil
}

func (a *App) twilioBundleEvaluate(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.twilioNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	bundleSID := strings.TrimSpace(strArg(args, "bundle_sid", ""))
	if !validTwilioBundleSID(bundleSID) {
		return nil, errors.New("bundle_sid must be a Twilio Regulatory Bundle SID beginning with BU")
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, "evaluate_regulatory_bundle", map[string]any{"BundleSid": bundleSID})
	if err != nil {
		return nil, err
	}
	evaluation, err := twilioResponse(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": "twilio", "bundle_sid": bundleSID, "evaluation": evaluation}, nil
}

func (a *App) twilioBundleSubmit(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.twilioNumberProvider(ctx)
	if err != nil {
		return nil, err
	}
	evaluated, err := a.twilioBundleEvaluate(ctx, args)
	if err != nil {
		return nil, err
	}
	evaluation, _ := evaluated["evaluation"].(map[string]any)
	if !strings.EqualFold(stringValue(evaluation["status"]), "compliant") {
		return map[string]any{
			"provider": "twilio", "submitted": false,
			"reason":     "bundle evaluation is not compliant; resolve the returned failures before submission",
			"evaluation": evaluation,
		}, nil
	}
	bundleSID := strings.TrimSpace(strArg(args, "bundle_sid", ""))
	raw, err := executeCarrierTool(ctx, provider.ConnID, "update_regulatory_bundle", map[string]any{
		"BundleSid": bundleSID, "Status": "pending-review",
	})
	if err != nil {
		return nil, err
	}
	bundle, err := twilioResponse(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": "twilio", "submitted": true, "bundle": bundle, "evaluation": evaluation}, nil
}

func validateTwilioPurchaseResources(ctx *sdk.AppCtx, intent *numberPurchaseIntent, addressSID, bundleSID string) error {
	if addressSID != "" {
		raw, err := executeCarrierTool(ctx, intent.CarrierConnectionID, "get_address", map[string]any{"AddressSid": addressSID})
		if err != nil {
			return fmt.Errorf("validate address_sid: %w", err)
		}
		address, err := twilioResponse(raw)
		if err != nil {
			return err
		}
		if !strings.EqualFold(stringValue(address["sid"]), addressSID) {
			return errors.New("Twilio did not return the selected address")
		}
		if strings.EqualFold(intent.AddressRequirement, "local") && !strings.EqualFold(stringValue(address["iso_country"]), intent.Country) {
			return fmt.Errorf("address_sid must be in %s for this local address requirement", intent.Country)
		}
	}
	if bundleSID == "" {
		return nil
	}
	bundleRaw, err := executeCarrierTool(ctx, intent.CarrierConnectionID, "get_regulatory_bundle", map[string]any{"BundleSid": bundleSID})
	if err != nil {
		return fmt.Errorf("validate bundle_sid: %w", err)
	}
	bundle, err := twilioResponse(bundleRaw)
	if err != nil {
		return err
	}
	status := strings.ToLower(stringValue(bundle["status"]))
	if status != "twilio-approved" {
		return fmt.Errorf("bundle_sid is not approved (status: %s)", firstNonEmpty(status, "unknown"))
	}
	regulationSID := stringValue(bundle["regulation_sid"])
	if !validTwilioRegulationSID(regulationSID) {
		return errors.New("approved bundle has no valid regulation SID")
	}
	regulationRaw, err := executeCarrierTool(ctx, intent.CarrierConnectionID, "get_regulation", map[string]any{
		"RegulationSid": regulationSID, "IncludeConstraints": false,
	})
	if err != nil {
		return fmt.Errorf("validate bundle regulation: %w", err)
	}
	regulation, err := twilioResponse(regulationRaw)
	if err != nil {
		return err
	}
	if !strings.EqualFold(stringValue(regulation["iso_country"]), intent.Country) {
		return fmt.Errorf("bundle_sid is for %s, not %s", firstNonEmpty(stringValue(regulation["iso_country"]), "an unknown country"), intent.Country)
	}
	return nil
}

func boundedIntArg(args map[string]any, key string, fallback, min, max int) int {
	value := intArg(args, key, fallback)
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func boolArg(args map[string]any, key string, fallback bool) bool {
	value, ok := args[key]
	if !ok {
		return fallback
	}
	if typed, ok := value.(bool); ok {
		return typed
	}
	return fallback
}
