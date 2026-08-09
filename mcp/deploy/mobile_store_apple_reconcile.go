package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) applyAppleDistribution(bound *sdk.BoundIntegration, appID string, doc StoreDocument) error {
	price := func() error {
		if body := providerExtensionBody(doc, "app_store_connect", "price_schedule_body"); body != nil && !strings.EqualFold(doc.Distribution.PriceTier, "FREE") {
			_, err := executeIntegration(bound, "create_app_price_schedule", map[string]any{"body": body})
			return err
		}
		if strings.EqualFold(doc.Distribution.PriceTier, "FREE") {
			return reconcileAppleFreePrice(bound, appID, doc)
		}
		return nil
	}
	availability := func() error {
		if body := providerExtensionBody(doc, "app_store_connect", "availability_body"); body != nil {
			availabilityID := jsonStringFromValue(body, "data", "id")
			if availabilityID != "" {
				return errors.New("App Store availability resources cannot be updated; use structured distribution availability")
			}
			if _, err := executeIntegration(bound, "get_app_availability", map[string]any{"app_id": appID}); err == nil {
				return errors.New("App Store availability already exists; use structured distribution availability to reconcile territories")
			} else if integrationErrorStatus(err) != http.StatusNotFound {
				return err
			}
			_, err := executeIntegration(bound, "create_app_availability", map[string]any{"body": body})
			return err
		}
		if storeAvailabilityConfigured(doc.Distribution) {
			return reconcileAppleAvailability(bound, appID, doc.Distribution)
		}
		return nil
	}
	return applyIndependentStoreOperations(price, availability)
}

func applyIndependentStoreOperations(operations ...func() error) error {
	var operationErrors []error
	for _, operation := range operations {
		if operation == nil {
			continue
		}
		if err := operation(); err != nil {
			operationErrors = append(operationErrors, err)
		}
	}
	return errors.Join(operationErrors...)
}

func reconcileAppleFreePrice(bound *sdk.BoundIntegration, appID string, doc StoreDocument) error {
	baseTerritory := appleBasePriceTerritory(doc.Distribution)
	current, err := executeIntegration(bound, "get_app_price_schedule", map[string]any{"app_id": appID, "include": "baseTerritory,manualPrices"})
	if err == nil && applePriceScheduleIsFree(bound, current, appID, baseTerritory) {
		return nil
	}
	if err != nil && integrationErrorStatus(err) != http.StatusNotFound {
		return err
	}
	points, err := executeIntegration(bound, "list_app_price_points", map[string]any{
		"app_id": appID, "territory": baseTerritory, "fields": "customerPrice,proceeds", "include": "territory", "limit": 200,
	})
	if err != nil {
		return err
	}
	zeroID := firstZeroApplePricePoint(points)
	if zeroID == "" {
		return fmt.Errorf("App Store Connect returned no zero price point for base territory %s", baseTerritory)
	}
	body := appleFreePriceScheduleBody(appID, baseTerritory, zeroID)
	_, err = executeIntegration(bound, "create_app_price_schedule", map[string]any{"body": body})
	return err
}

func appleBasePriceTerritory(distribution StoreDistribution) string {
	if value := strings.TrimSpace(jsonStringFromValue(distribution.Provider, "base_territory")); value != "" {
		return strings.ToUpper(value)
	}
	return "USA"
}

func appleFreePriceScheduleBody(appID, baseTerritory, pricePointID string) map[string]any {
	manualPriceID := "${deploy-free-" + strings.ToLower(baseTerritory) + "}"
	return map[string]any{
		"data": map[string]any{
			"type": "appPriceSchedules",
			"relationships": map[string]any{
				"app":           map[string]any{"data": map[string]any{"type": "apps", "id": appID}},
				"baseTerritory": map[string]any{"data": map[string]any{"type": "territories", "id": baseTerritory}},
				"manualPrices":  map[string]any{"data": []any{map[string]any{"type": "appPrices", "id": manualPriceID}}},
			},
		},
		"included": []any{map[string]any{
			"type": "appPrices", "id": manualPriceID,
			"relationships": map[string]any{
				"appPricePoint": map[string]any{"data": map[string]any{"type": "appPricePoints", "id": pricePointID}},
			},
		}},
	}
}

func firstZeroApplePricePoint(raw json.RawMessage) string {
	var payload struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				CustomerPrice string `json:"customerPrice"`
			} `json:"attributes"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &payload)
	for _, point := range payload.Data {
		price, err := strconv.ParseFloat(point.Attributes.CustomerPrice, 64)
		if err == nil && price == 0 {
			return point.ID
		}
	}
	return ""
}

func reconcileAppleAvailability(bound *sdk.BoundIntegration, appID string, distribution StoreDistribution) error {
	desired, availableInNew, err := desiredAppleTerritories(bound, distribution)
	if err != nil {
		return err
	}
	current, err := executeIntegration(bound, "get_app_availability", map[string]any{"app_id": appID})
	if err != nil {
		if integrationErrorStatus(err) != http.StatusNotFound {
			return err
		}
		body := appleAvailabilityCreateBody(appID, desired, availableInNew)
		_, err = executeIntegration(bound, "create_app_availability", map[string]any{"body": body})
		return err
	}
	availabilityID := firstJSONAPIID(current)
	if availabilityID == "" {
		return errors.New("App Store availability response missing id")
	}
	territoryPages, err := executeAppleCollectionPages(bound, "list_app_availability_territories", map[string]any{
		"availability_id": availabilityID, "include": "territory", "limit": 200,
	})
	if err != nil {
		return err
	}
	currentAvailableInNew, ok := appleAvailableInNewTerritories(current)
	if !ok {
		return errors.New("App Store availability response missing availableInNewTerritories")
	}
	if currentAvailableInNew != availableInNew {
		return errors.New("App Store available-in-new-territories setting cannot be updated through this API; change it in App Store Connect")
	}
	actual := appleTerritoryAvailabilityStateFromPages(territoryPages)
	resources := appleTerritoryAvailabilityIDsFromPages(territoryPages)
	seen := map[string]bool{}
	for resourceID, territoryID := range resources {
		available, ok := desired[territoryID]
		if !ok {
			continue
		}
		seen[territoryID] = true
		if actual[territoryID] == available {
			continue
		}
		_, err := executeIntegration(bound, "update_territory_availability", map[string]any{
			"territory_availability_id": resourceID,
			"body": map[string]any{"data": map[string]any{
				"type": "territoryAvailabilities", "id": resourceID,
				"attributes": map[string]any{"available": available},
			}},
		})
		if err != nil {
			return err
		}
	}
	for territoryID := range desired {
		if !seen[territoryID] {
			return fmt.Errorf("App Store availability is missing territory %s", territoryID)
		}
	}
	return nil
}

func desiredAppleTerritories(bound *sdk.BoundIntegration, distribution StoreDistribution) (map[string]bool, bool, error) {
	pages, err := executeAppleCollectionPages(bound, "list_territories", map[string]any{"fields": "currency", "limit": 200})
	if err != nil {
		return nil, false, err
	}
	all := jsonAPIResourceIDsFromPages(pages)
	if len(all) == 0 {
		return nil, false, errors.New("App Store Connect returned no territories")
	}
	availability := distribution.Availability
	if availability.Mode == "" {
		availability.Mode = "only"
		availability.IncludedTerritories = distribution.Territories
	}
	included := upperStringSet(availability.IncludedTerritories)
	excluded := upperStringSet(availability.ExcludedTerritories)
	desired := map[string]bool{}
	for _, territory := range all {
		switch availability.Mode {
		case "all":
			desired[territory] = true
		case "all_except":
			desired[territory] = !excluded[territory]
		case "only":
			desired[territory] = included[territory]
		default:
			return nil, false, fmt.Errorf("unsupported availability mode %q", availability.Mode)
		}
	}
	availableInNew := availability.Mode == "all" || availability.Mode == "all_except"
	if availability.AvailableInNewTerritories != nil {
		availableInNew = *availability.AvailableInNewTerritories
	}
	return desired, availableInNew, nil
}

func appleAvailabilityCreateBody(appID string, desired map[string]bool, availableInNew bool) map[string]any {
	ids := make([]string, 0, len(desired))
	for territory := range desired {
		ids = append(ids, territory)
	}
	sort.Strings(ids)
	linkages := make([]any, 0, len(ids))
	included := make([]any, 0, len(ids))
	for _, territory := range ids {
		id := "${deploy-territory-" + strings.ToLower(territory) + "}"
		linkages = append(linkages, map[string]any{"type": "territoryAvailabilities", "id": id})
		included = append(included, map[string]any{
			"type": "territoryAvailabilities", "id": id,
			"attributes":    map[string]any{"available": desired[territory]},
			"relationships": map[string]any{"territory": map[string]any{"data": map[string]any{"type": "territories", "id": territory}}},
		})
	}
	return map[string]any{
		"data": map[string]any{
			"type": "appAvailabilities", "attributes": map[string]any{"availableInNewTerritories": availableInNew},
			"relationships": map[string]any{
				"app":                     map[string]any{"data": map[string]any{"type": "apps", "id": appID}},
				"territoryAvailabilities": map[string]any{"data": linkages},
			},
		},
		"included": included,
	}
}

func appleTerritoryAvailabilityIDs(raw json.RawMessage) map[string]string {
	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			Relationships struct {
				Territory struct {
					Data struct {
						ID string `json:"id"`
					} `json:"data"`
				} `json:"territory"`
			} `json:"relationships"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &payload)
	out := map[string]string{}
	for _, item := range payload.Data {
		out[item.ID] = strings.ToUpper(item.Relationships.Territory.Data.ID)
	}
	return out
}

func appleTerritoryAvailabilityIDsFromPages(pages []json.RawMessage) map[string]string {
	out := map[string]string{}
	for _, page := range pages {
		for resourceID, territoryID := range appleTerritoryAvailabilityIDs(page) {
			out[resourceID] = territoryID
		}
	}
	return out
}

func appleAvailabilityMatches(bound *sdk.BoundIntegration, distribution StoreDistribution, availability json.RawMessage) (bool, map[string]bool, error) {
	if !storeAvailabilityConfigured(distribution) {
		return jsonValueHasData(decodeJSONValue(availability)), nil, nil
	}
	availabilityID := firstJSONAPIID(availability)
	if availabilityID == "" {
		return false, nil, errors.New("App Store availability response missing id")
	}
	pages, err := executeAppleCollectionPages(bound, "list_app_availability_territories", map[string]any{
		"availability_id": availabilityID, "include": "territory", "limit": 200,
	})
	if err != nil {
		return false, nil, err
	}
	actual := appleTerritoryAvailabilityStateFromPages(pages)
	desired, desiredAvailableInNew, err := desiredAppleTerritories(bound, distribution)
	if err != nil {
		return false, actual, err
	}
	actualAvailableInNew, ok := appleAvailableInNewTerritories(availability)
	if !ok || actualAvailableInNew != desiredAvailableInNew {
		return false, actual, nil
	}
	if len(actual) != len(desired) {
		return false, actual, nil
	}
	for territory, available := range desired {
		if actual[territory] != available {
			return false, actual, nil
		}
	}
	return true, actual, nil
}

func appleAvailableInNewTerritories(raw json.RawMessage) (bool, bool) {
	var payload struct {
		Data struct {
			Attributes struct {
				AvailableInNewTerritories *bool `json:"availableInNewTerritories"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Data.Attributes.AvailableInNewTerritories == nil {
		return false, false
	}
	return *payload.Data.Attributes.AvailableInNewTerritories, true
}

func appleTerritoryAvailabilityState(raw json.RawMessage) map[string]bool {
	var payload struct {
		Data []struct {
			Attributes struct {
				Available bool `json:"available"`
			} `json:"attributes"`
			Relationships struct {
				Territory struct {
					Data struct {
						ID string `json:"id"`
					} `json:"data"`
				} `json:"territory"`
			} `json:"relationships"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &payload)
	out := map[string]bool{}
	for _, item := range payload.Data {
		if territory := strings.ToUpper(item.Relationships.Territory.Data.ID); territory != "" {
			out[territory] = item.Attributes.Available
		}
	}
	return out
}

func appleTerritoryAvailabilityStateFromPages(pages []json.RawMessage) map[string]bool {
	out := map[string]bool{}
	for _, page := range pages {
		for territoryID, available := range appleTerritoryAvailabilityState(page) {
			out[territoryID] = available
		}
	}
	return out
}

func jsonAPIResourceIDs(raw json.RawMessage) []string {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &payload)
	out := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		if item.ID != "" {
			out = append(out, strings.ToUpper(item.ID))
		}
	}
	return out
}

func jsonAPIResourceIDsFromPages(pages []json.RawMessage) []string {
	seen := map[string]bool{}
	var out []string
	for _, page := range pages {
		for _, id := range jsonAPIResourceIDs(page) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

func executeAppleCollectionPages(bound *sdk.BoundIntegration, tool string, input map[string]any) ([]json.RawMessage, error) {
	params := make(map[string]any, len(input)+1)
	for key, value := range input {
		params[key] = value
	}
	seen := map[string]bool{}
	pages := make([]json.RawMessage, 0, 1)
	for len(pages) < 100 {
		callInput := make(map[string]any, len(params))
		for key, value := range params {
			callInput[key] = value
		}
		raw, err := executeIntegration(bound, tool, callInput)
		if err != nil {
			return nil, err
		}
		pages = append(pages, raw)
		cursor, err := appleJSONAPINextCursor(raw)
		if err != nil {
			return nil, fmt.Errorf("%s pagination: %w", tool, err)
		}
		if cursor == "" {
			return pages, nil
		}
		if seen[cursor] {
			return nil, fmt.Errorf("%s pagination repeated cursor %q", tool, cursor)
		}
		seen[cursor] = true
		params["cursor"] = cursor
	}
	return nil, fmt.Errorf("%s pagination exceeded 100 pages", tool)
}

func appleJSONAPINextCursor(raw json.RawMessage) (string, error) {
	var payload struct {
		Links struct {
			Next json.RawMessage `json:"next"`
		} `json:"links"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", err
	}
	if len(payload.Links.Next) == 0 || string(payload.Links.Next) == "null" {
		return "", nil
	}
	var next string
	if err := json.Unmarshal(payload.Links.Next, &next); err != nil {
		return "", errors.New("links.next is not a URL string")
	}
	if strings.TrimSpace(next) == "" {
		return "", nil
	}
	parsed, err := url.Parse(next)
	if err != nil {
		return "", fmt.Errorf("parse links.next: %w", err)
	}
	cursor := strings.TrimSpace(parsed.Query().Get("cursor"))
	if cursor == "" {
		return "", errors.New("links.next is missing cursor")
	}
	return cursor, nil
}

func upperStringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[strings.ToUpper(strings.TrimSpace(value))] = true
	}
	return out
}

type appleScreenshotResource struct {
	ID       string
	Filename string
	State    string
}

func (a *App) observeAppleScreenshots(bound *sdk.BoundIntegration, d *Deployment, localizationID, locale string, doc StoreDocument) (bool, map[string]any, error) {
	grouped := map[string][]StoreAsset{}
	for _, asset := range doc.Assets {
		if !strings.Contains(asset.Kind, "screenshot") || defaultStr(asset.Locale, doc.DefaultLocale) != locale {
			continue
		}
		target := appleScreenshotDisplayTarget(asset)
		grouped[target] = append(grouped[target], asset)
	}
	desired := map[string][]string{}
	for target, assets := range grouped {
		sort.SliceStable(assets, func(i, j int) bool { return assets[i].Order < assets[j].Order })
		for _, asset := range assets {
			path, err := resolveStoreAssetPath(a.dataDir, d, asset.Path)
			if err != nil {
				return false, nil, err
			}
			desired[target] = append(desired[target], storeUploadFilename(path, asset.SHA256))
		}
	}
	setsRaw, err := executeIntegration(bound, "list_screenshot_sets", map[string]any{"localization_id": localizationID})
	if err != nil {
		return false, nil, err
	}
	sets := parseAppleScreenshotSets(setsRaw)
	state := map[string]any{}
	ready := len(desired) > 0
	for target, names := range desired {
		setID := ""
		for _, set := range sets {
			if strings.EqualFold(set.DisplayType, target) {
				setID = set.ID
				break
			}
		}
		if setID == "" {
			state[target] = map[string]any{"status": "missing"}
			ready = false
			continue
		}
		raw, err := executeIntegration(bound, "list_screenshots", map[string]any{"set_id": setID})
		if err != nil {
			return false, state, err
		}
		resources := parseAppleScreenshots(raw)
		complete := appleScreenshotsCompleteInOrder(resources, names)
		state[target] = map[string]any{"set_id": setID, "screenshots": decodeJSONValue(raw), "verified": complete}
		ready = ready && complete
	}
	return ready, state, nil
}

func (a *App) reconcileAppleScreenshots(bound *sdk.BoundIntegration, d *Deployment, localizationIDs map[string]string, doc StoreDocument) error {
	grouped := map[string][]StoreAsset{}
	for _, asset := range doc.Assets {
		if !strings.Contains(asset.Kind, "screenshot") {
			continue
		}
		locale := defaultStr(asset.Locale, doc.DefaultLocale)
		key := locale + "\x00" + appleScreenshotDisplayTarget(asset)
		grouped[key] = append(grouped[key], asset)
	}
	for locale, localizationID := range localizationIDs {
		setsRaw, err := executeIntegration(bound, "list_screenshot_sets", map[string]any{"localization_id": localizationID})
		if err != nil {
			return err
		}
		for _, set := range parseAppleScreenshotSets(setsRaw) {
			if _, desired := grouped[locale+"\x00"+set.DisplayType]; desired {
				continue
			}
			existingRaw, listErr := executeIntegration(bound, "list_screenshots", map[string]any{"set_id": set.ID})
			if listErr != nil {
				return listErr
			}
			for _, screenshot := range parseAppleScreenshots(existingRaw) {
				if _, deleteErr := executeIntegration(bound, "delete_screenshot", map[string]any{"screenshot_id": screenshot.ID}); deleteErr != nil {
					return deleteErr
				}
			}
		}
	}
	for key, assets := range grouped {
		parts := strings.SplitN(key, "\x00", 2)
		localizationID := localizationIDs[parts[0]]
		if localizationID == "" {
			return fmt.Errorf("asset group references unknown locale %s", parts[0])
		}
		sort.SliceStable(assets, func(i, j int) bool { return assets[i].Order < assets[j].Order })
		sets, err := executeIntegration(bound, "list_screenshot_sets", map[string]any{"localization_id": localizationID})
		if err != nil {
			return err
		}
		setID := appleScreenshotSetID(sets, parts[1])
		if setID == "" {
			created, createErr := executeIntegration(bound, "create_screenshot_set", map[string]any{"localization_id": localizationID, "screenshotDisplayType": parts[1]})
			if createErr != nil {
				return createErr
			}
			setID = firstJSONAPIID(created)
		}
		existingRaw, err := executeIntegration(bound, "list_screenshots", map[string]any{"set_id": setID})
		if err != nil {
			return err
		}
		existing := parseAppleScreenshots(existingRaw)
		desiredNames := make([]string, 0, len(assets))
		for _, asset := range assets {
			path, pathErr := resolveStoreAssetPath(a.dataDir, d, asset.Path)
			if pathErr != nil {
				return pathErr
			}
			desiredNames = append(desiredNames, storeUploadFilename(path, asset.SHA256))
		}
		if !appleScreenshotsCompleteInOrder(existing, desiredNames) {
			for _, screenshot := range existing {
				if _, err := executeIntegration(bound, "delete_screenshot", map[string]any{"screenshot_id": screenshot.ID}); err != nil {
					return err
				}
			}
			for _, asset := range assets {
				if err := a.uploadAppleScreenshot(bound, d, localizationID, asset); err != nil {
					return err
				}
			}
		}
		if err := waitForAppleScreenshots(bound, setID, desiredNames); err != nil {
			return err
		}
	}
	return nil
}

type appleScreenshotSet struct {
	ID          string
	DisplayType string
}

func parseAppleScreenshotSets(raw json.RawMessage) []appleScreenshotSet {
	var payload struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				DisplayType string `json:"screenshotDisplayType"`
			} `json:"attributes"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &payload)
	out := make([]appleScreenshotSet, 0, len(payload.Data))
	for _, item := range payload.Data {
		out = append(out, appleScreenshotSet{ID: item.ID, DisplayType: item.Attributes.DisplayType})
	}
	return out
}

func appleScreenshotSetID(raw json.RawMessage, displayType string) string {
	displayType = strings.ToUpper(strings.TrimSpace(displayType))
	for _, set := range parseAppleScreenshotSets(raw) {
		if strings.ToUpper(strings.TrimSpace(set.DisplayType)) == displayType {
			return set.ID
		}
	}
	return ""
}

func parseAppleScreenshots(raw json.RawMessage) []appleScreenshotResource {
	var payload struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				FileName           string `json:"fileName"`
				AssetDeliveryState struct {
					State string `json:"state"`
				} `json:"assetDeliveryState"`
			} `json:"attributes"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &payload)
	out := make([]appleScreenshotResource, 0, len(payload.Data))
	for _, item := range payload.Data {
		out = append(out, appleScreenshotResource{ID: item.ID, Filename: item.Attributes.FileName, State: item.Attributes.AssetDeliveryState.State})
	}
	return out
}

func sameAppleScreenshotOrder(existing []appleScreenshotResource, desired []string) bool {
	if len(existing) != len(desired) {
		return false
	}
	for i := range desired {
		if existing[i].Filename != desired[i] {
			return false
		}
	}
	return true
}

func appleScreenshotsCompleteInOrder(existing []appleScreenshotResource, desired []string) bool {
	if !sameAppleScreenshotOrder(existing, desired) {
		return false
	}
	for _, resource := range existing {
		if !strings.EqualFold(resource.State, "COMPLETE") {
			return false
		}
	}
	return true
}

func waitForAppleScreenshots(bound *sdk.BoundIntegration, setID string, desired []string) error {
	for attempt := 0; attempt < 20; attempt++ {
		raw, err := executeIntegration(bound, "list_screenshots", map[string]any{"set_id": setID})
		if err != nil {
			return err
		}
		resources := parseAppleScreenshots(raw)
		if sameAppleScreenshotOrder(resources, desired) {
			pending := false
			for _, resource := range resources {
				state := strings.ToUpper(resource.State)
				if strings.Contains(state, "FAIL") {
					return fmt.Errorf("Apple screenshot %s delivery failed", resource.Filename)
				}
				if state != "" && state != "COMPLETE" {
					pending = true
				}
			}
			if !pending {
				return nil
			}
		}
		if attempt < 19 {
			time.Sleep(3 * time.Second)
		}
	}
	return errors.New("timed out waiting for Apple screenshot delivery")
}
