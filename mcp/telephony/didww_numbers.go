package main

import (
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// DIDWW's v3 inventory is JSON:API. Some accounts expose individual
// available_dids while others expose only did_groups; the latter can still be
// ordered by group and SKU without inventing a phone number.
func searchDIDWWNumbers(ctx *sdk.AppCtx, provider *numberProvider, request numberSearchRequest, country string) ([]numberOffer, []string, error) {
	countryID, err := didwwCountryID(ctx, provider.ConnID, country)
	if err != nil {
		return nil, nil, err
	}
	types := []string{request.NumberType}
	if request.NumberType == "any" {
		types = []string{"local", "mobile", "national", "toll_free"}
	}
	offers := make([]numberOffer, 0)
	warnings := make([]string, 0)
	for _, numberType := range types {
		groupTypeID, typeErr := didwwGroupTypeID(ctx, provider.ConnID, numberType)
		if typeErr != nil {
			warnings = append(warnings, fmt.Sprintf("DIDWW %s type lookup: %v", numberType, typeErr))
			continue
		}
		input := map[string]any{
			"country_id": countryID, "include": "stock_keeping_units,requirement",
			"page_size": 100,
		}
		if groupTypeID != "" {
			input["did_group_type_id"] = groupTypeID
		}
		if len(request.Features) > 0 {
			input["features"] = strings.Join(request.Features, ",")
		}
		raw, searchErr := executeCarrierTool(ctx, provider.ConnID, "list_did_groups", input)
		if searchErr != nil {
			return nil, warnings, fmt.Errorf("DIDWW group search for %s: %w", country, searchErr)
		}
		groups, included, parseErr := didwwGroups(raw)
		if parseErr != nil {
			return nil, warnings, parseErr
		}
		for _, group := range groups {
			sku := didwwCheapestSKU(group, included)
			if sku.ID == "" || sku.MonthlyPrice == "" {
				warnings = append(warnings, fmt.Sprintf("DIDWW %s group %s has no orderable voice SKU", group.AreaName, group.ID))
				continue
			}
			groupOffers, groupWarnings := didwwGroupOffers(ctx, provider, request, country, numberType, group, sku)
			offers = append(offers, groupOffers...)
			warnings = append(warnings, groupWarnings...)
		}
	}
	if request.AreaCode != "" || request.Pattern != "" {
		warnings = append(warnings, "DIDWW group inventory cannot filter an exact area code or pattern until individual available_dids access is enabled")
	}
	return offers, didwwUniqueStrings(warnings), nil
}

type didwwGroup struct {
	ID                   string
	AreaName             string
	Prefix               string
	Features             []string
	AvailableDIDsEnabled bool
	TotalCount           int
	SKUIDs               []string
	AddressRequirementID string
	NeedsRegistration    bool
}

type didwwSKU struct {
	ID               string
	SetupPrice       string
	MonthlyPrice     string
	ChannelsIncluded int
}

func didwwCountryID(ctx *sdk.AppCtx, connID int64, country string) (string, error) {
	raw, err := executeCarrierTool(ctx, connID, "list_countries", map[string]any{"page_size": 1000})
	if err != nil {
		return "", fmt.Errorf("list DIDWW countries: %w", err)
	}
	var root struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", fmt.Errorf("decode DIDWW countries: %w", err)
	}
	for _, item := range root.Data {
		attrs, _ := item["attributes"].(map[string]any)
		if strings.EqualFold(stringValue(attrs["iso"]), country) {
			if id := stringValue(item["id"]); id != "" {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("DIDWW does not list country %s", strings.ToUpper(country))
}

func didwwGroupTypeID(ctx *sdk.AppCtx, connID int64, numberType string) (string, error) {
	want := map[string]string{"local": "local", "mobile": "mobile", "national": "national", "toll_free": "toll-free"}[numberType]
	raw, err := executeCarrierTool(ctx, connID, "list_did_group_types", map[string]any{"page_size": 100})
	if err != nil {
		return "", fmt.Errorf("list DIDWW group types: %w", err)
	}
	var root struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", fmt.Errorf("decode DIDWW group types: %w", err)
	}
	for _, item := range root.Data {
		attrs, _ := item["attributes"].(map[string]any)
		// DIDWW has used both hyphenated and spaced labels across API
		// revisions (for example, "toll-free" and "toll free"). Compare a
		// normalized token so a provider label change cannot silently make a
		// supported number type disappear from search.
		name := strings.ToLower(strings.TrimSpace(stringValue(attrs["name"])))
		name = strings.NewReplacer("-", "", "_", "", " ", "").Replace(name)
		normalizedWant := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(want))
		if name == normalizedWant {
			return stringValue(item["id"]), nil
		}
	}
	return "", fmt.Errorf("DIDWW has no %s group type", numberType)
}

func didwwGroups(raw json.RawMessage) ([]didwwGroup, map[string]didwwSKU, error) {
	var root struct {
		Data     []map[string]any `json:"data"`
		Included []map[string]any `json:"included"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, nil, fmt.Errorf("decode DIDWW groups: %w", err)
	}
	included := make(map[string]didwwSKU)
	for _, item := range root.Included {
		if item["type"] != "stock_keeping_units" {
			continue
		}
		attrs, _ := item["attributes"].(map[string]any)
		included[stringValue(item["id"])] = didwwSKU{
			ID: stringValue(item["id"]), SetupPrice: stringValue(attrs["setup_price"]),
			MonthlyPrice: stringValue(attrs["monthly_price"]), ChannelsIncluded: intValue(attrs["channels_included_count"]),
		}
	}
	groups := make([]didwwGroup, 0, len(root.Data))
	for _, item := range root.Data {
		attrs, _ := item["attributes"].(map[string]any)
		meta, _ := item["meta"].(map[string]any)
		rels, _ := item["relationships"].(map[string]any)
		group := didwwGroup{ID: stringValue(item["id"]), AreaName: stringValue(attrs["area_name"]), Prefix: stringValue(attrs["prefix"]), TotalCount: intValue(meta["total_count"]), AvailableDIDsEnabled: boolValue(meta["available_dids_enabled"]), NeedsRegistration: boolValue(meta["needs_registration"]), Features: didwwStringList(attrs["features"])}
		if rel, ok := rels["stock_keeping_units"].(map[string]any); ok {
			if values, ok := rel["data"].([]any); ok {
				for _, value := range values {
					entry, _ := value.(map[string]any)
					if id := stringValue(entry["id"]); id != "" {
						group.SKUIDs = append(group.SKUIDs, id)
					}
				}
			}
		}
		if rel, ok := rels["requirement"].(map[string]any); ok {
			if data, ok := rel["data"].(map[string]any); ok {
				group.AddressRequirementID = stringValue(data["id"])
			}
		}
		if group.AddressRequirementID == "" {
			if rel, ok := rels["address_requirement"].(map[string]any); ok {
				if data, ok := rel["data"].(map[string]any); ok {
					group.AddressRequirementID = stringValue(data["id"])
				}
			}
		}
		if group.ID != "" {
			groups = append(groups, group)
		}
	}
	return groups, included, nil
}

func didwwCheapestSKU(group didwwGroup, included map[string]didwwSKU) didwwSKU {
	var selected didwwSKU
	for _, id := range group.SKUIDs {
		sku := included[id]
		if sku.ID == "" || sku.MonthlyPrice == "" {
			continue
		}
		if selected.ID == "" {
			selected = sku
			continue
		}
		left, lok := priceFloat(sku.MonthlyPrice)
		right, rok := priceFloat(selected.MonthlyPrice)
		if lok && (!rok || left < right) {
			selected = sku
		}
	}
	return selected
}

func didwwGroupOffers(ctx *sdk.AppCtx, provider *numberProvider, request numberSearchRequest, country, numberType string, group didwwGroup, sku didwwSKU) ([]numberOffer, []string) {
	warnings := []string{}
	offers := make([]numberOffer, 0, request.Limit)
	currency := firstNonEmpty(provider.Fields["currency"], provider.Fields["pricing_currency"], "USD")
	addressRequirement := group.AddressRequirementID
	if addressRequirement != "" {
		addressRequirement = "DIDWW registration requirement: " + addressRequirement
	}
	base := numberOffer{Country: country, NumberType: numberType, FriendlyName: fmt.Sprintf("DIDWW %s %s number", firstNonEmpty(group.AreaName, "France"), numberType), Locality: group.AreaName, Features: []string{"voice"}, MonthlyPrice: sku.MonthlyPrice, UpfrontPrice: sku.SetupPrice, Currency: currency, AddressRequirement: addressRequirement, ComplianceRequired: addressRequirement != "", ProviderGroupID: group.ID, ProviderSKUID: sku.ID, InventoryMode: "did_group"}
	if group.NeedsRegistration && group.AddressRequirementID == "" {
		base.PurchaseBlocker = "DIDWW reported registration required but returned no requirement ID"
	}
	if group.AvailableDIDsEnabled {
		raw, err := executeCarrierTool(ctx, provider.ConnID, "list_available_dids", map[string]any{"did_group_id": group.ID, "features": strings.Join(request.Features, ","), "include": "did_group"})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("DIDWW individual inventory for %s unavailable: %v; showing group order", group.AreaName, err))
		} else {
			var root struct {
				Data []map[string]any `json:"data"`
			}
			if json.Unmarshal(raw, &root) == nil {
				for _, item := range root.Data {
					attrs, _ := item["attributes"].(map[string]any)
					number := normalizedOwnedPhone(firstNonEmpty(stringValue(attrs["number"]), stringValue(attrs["phone_number"])))
					if !validE164(number) || !didwwNumberMatches(number, request) {
						continue
					}
					offer := base
					offer.PhoneNumber, offer.FriendlyName = number, number
					offer.ProviderResourceID, offer.InventoryMode = stringValue(item["id"]), "available_did"
					offers = append(offers, offer)
					if len(offers) >= request.Limit {
						return offers, warnings
					}
				}
				if len(root.Data) == 0 {
					warnings = append(warnings, fmt.Sprintf("DIDWW reported no individual numbers for %s", group.AreaName))
				}
			}
		}
	}
	if len(offers) == 0 && group.TotalCount > 0 {
		warnings = append(warnings, fmt.Sprintf("DIDWW individual inventory is not exposed; ordering will select a number from %s coverage", group.AreaName))
		offers = append(offers, base)
	}
	return offers, warnings
}

func didwwNumberMatches(number string, request numberSearchRequest) bool {
	compact := compactPhoneNumber(number)
	if request.AreaCode != "" && !strings.Contains(compact, strings.TrimLeft(request.AreaCode, "0")) {
		return false
	}
	if request.Pattern != "" {
		pattern := strings.NewReplacer("+", "", " ", "", "-", "", "(", "", ")", "").Replace(request.Pattern)
		if !strings.Contains(compact, strings.Trim(pattern, "*#")) {
			return false
		}
	}
	return true
}

func didwwUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func didwwStringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text := stringValue(item); text != "" {
			out = append(out, text)
		}
	}
	return out
}
