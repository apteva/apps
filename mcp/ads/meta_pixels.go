package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Meta Pixel ownership. Ad accounts that belong to a Business usually cannot
// own Pixels: POST /act_<id>/adspixels fails with error 6200 while the same
// request against /<business_id>/adspixels succeeds. A business-owned Pixel is
// invisible to the ad account until it is shared with it, so creation is two
// provider calls and the association is reported separately.

const (
	metaPixelOwnerAuto      = "auto"
	metaPixelOwnerBusiness  = "business"
	metaPixelOwnerAdAccount = "ad_account"

	metaPixelOwnerUnsupportedCode = 6200
)

// Meta exposes no public operation that names a domain's source category or
// requests its review; say so wherever readiness is reported.
var metaTrackingLimitations = []string{
	"Meta can restrict events from a domain by source category (for example sensitive health or financial categories). The public Marketing API does not name the category or accept a review request; resolve it in Events Manager or with Meta support. Creating another Pixel does not lift a domain restriction.",
	"Readiness covers event receipt and Meta's data-quality checks. It does not establish that events are eligible for ad optimization or attribution.",
}

func metaNumericAccountID(native string) string {
	return strings.TrimPrefix(strings.TrimSpace(native), "act_")
}

func (a *App) metaAdAccountBusinessID(ctx *sdk.AppCtx, acct *adAccount) (string, map[string]any) {
	parsed, errOut := a.execIntegrationTool(ctx, acct, "ad_account_get", map[string]any{
		"adAccountId": acct.NativeAccountID,
		"fields":      "id,business{id}",
	})
	if errOut != nil {
		return "", errOut
	}
	return firstString(asMap(asMap(parsed)["business"]), "id"), nil
}

func (a *App) createMetaPixel(
	ctx *sdk.AppCtx,
	acct *adAccount,
	args map[string]any,
	name string,
	setDefault, reuseExisting bool,
	knownMatches map[string]bool,
) (any, error) {
	owner := strings.ToLower(strings.TrimSpace(stringArgAny(args, "owner")))
	if owner == "" {
		owner = metaPixelOwnerAuto
	}
	switch owner {
	case metaPixelOwnerAuto, metaPixelOwnerBusiness, metaPixelOwnerAdAccount:
	default:
		return mcpError("owner must be auto, business, or ad_account"), nil
	}
	businessID := ""
	if owner != metaPixelOwnerAdAccount {
		id, errOut := a.metaAdAccountBusinessID(ctx, acct)
		if errOut != nil {
			return errOut, nil
		}
		businessID = id
		if owner == metaPixelOwnerBusiness && businessID == "" {
			out := mcpError("this ad account does not belong to a Business visible to the connection, so a business-owned Pixel cannot be created")
			out["code"] = "business_required"
			return out, nil
		}
	}
	if businessID == "" {
		return a.createAdAccountPixel(ctx, acct, name, setDefault, knownMatches)
	}
	return a.createBusinessPixel(ctx, acct, businessID, name, setDefault, reuseExisting)
}

func (a *App) createAdAccountPixel(ctx *sdk.AppCtx, acct *adAccount, name string, setDefault bool, knownMatches map[string]bool) (any, error) {
	parsed, createErr := a.execIntegrationTool(ctx, acct, "pixel_create", map[string]any{
		"adAccountId": acct.NativeAccountID,
		"name":        name,
	})
	if createErr != nil {
		if shouldReconcileTrackingSourceCreate(createErr) {
			matches, refreshErr := a.refreshAndMatchTrackingSources(ctx, acct, name)
			matches = trackingSourcesNotPreviouslyPresent(matches, knownMatches)
			if refreshErr == nil && len(matches) == 1 {
				resource, err := a.markTrackingSourceManaged(ctx, acct, &matches[0])
				if err != nil {
					return nil, err
				}
				result, err := a.trackingSourceResult(ctx, acct, resource, false, false, true, setDefault)
				if err == nil {
					result["owner"] = metaPixelOwnerAdAccount
					a.emitTrackingSourceCreated(ctx, acct, resource, setDefault)
				}
				return result, err
			}
			if refreshErr == nil && len(matches) > 1 {
				return trackingSourceSelectionRequired(matches), nil
			}
			createErr["reconciliation_attempted"] = true
		}
		if intArg(createErr, "provider_code", 0) == metaPixelOwnerUnsupportedCode {
			createErr["code"] = "pixel_owner_unsupported"
			createErr["hint"] = "Meta does not let this ad account own Pixels. Create it under the owning Business with owner=business; if the connection cannot see a Business, grant it business_management access to one."
		}
		return createErr, nil
	}
	nativeID := trackingSourceNativeID(parsed)
	if nativeID == "" {
		out := mcpError("pixel_create returned no tracking source id")
		out["code"] = "provider_response_invalid"
		out["platform"] = acct.Platform
		return out, nil
	}
	resource, err := a.upsertResource(ctx, acct, metaPixelResource(nativeID, name, metaPixelOwnerAdAccount, true))
	if err != nil {
		return nil, err
	}
	result, err := a.trackingSourceResult(ctx, acct, resource, true, false, false, setDefault)
	if err == nil {
		result["owner"] = metaPixelOwnerAdAccount
		a.emitTrackingSourceCreated(ctx, acct, resource, setDefault)
	}
	return result, err
}

func (a *App) createBusinessPixel(ctx *sdk.AppCtx, acct *adAccount, businessID, name string, setDefault, reuseExisting bool) (any, error) {
	before, errOut := a.businessPixelsNamed(ctx, acct, businessID, name)
	if errOut != nil {
		return errOut, nil
	}
	created, reused, reconciled := false, false, false
	nativeID := ""
	switch {
	case reuseExisting && len(before) > 1:
		out := mcpError("the Business owns several Pixels with this name that are not shared with this ad account; rename or share one before retrying")
		out["code"] = "selection_required"
		out["matches"] = len(before)
		return out, nil
	case reuseExisting && len(before) == 1:
		nativeID, reused = before[0], true
	default:
		parsed, createErr := a.execIntegrationTool(ctx, acct, "business_pixel_create", map[string]any{
			"businessId": businessID,
			"name":       name,
		})
		if createErr != nil {
			if !shouldReconcileTrackingSourceCreate(createErr) {
				return createErr, nil
			}
			after, listErr := a.businessPixelsNamed(ctx, acct, businessID, name)
			fresh := newStrings(after, before)
			if listErr != nil || len(fresh) != 1 {
				createErr["reconciliation_attempted"] = true
				return createErr, nil
			}
			nativeID, reconciled = fresh[0], true
		} else {
			nativeID, created = trackingSourceNativeID(parsed), true
		}
		if nativeID == "" {
			out := mcpError("business_pixel_create returned no tracking source id")
			out["code"] = "provider_response_invalid"
			out["platform"] = acct.Platform
			return out, nil
		}
	}

	association := map[string]any{"status": "shared"}
	_, shareErr := a.execIntegrationTool(ctx, acct, "pixel_share_with_ad_account", map[string]any{
		"pixelId":    nativeID,
		"account_id": metaNumericAccountID(acct.NativeAccountID),
		"business":   businessID,
	})
	if shareErr != nil {
		// The Pixel exists either way. Report the association failure instead
		// of failing the call, so a retry reuses this Pixel rather than
		// creating another one.
		association = map[string]any{
			"status": "failed",
			"error":  mcpErrorMessage(shareErr),
			"hint":   "Retry tracking_source_create with the same name to share the existing Pixel, or share it with this ad account in Business Settings.",
		}
		if code := intArg(shareErr, "provider_code", 0); code != 0 {
			association["provider_code"] = code
		}
	}
	resource, err := a.upsertResource(ctx, acct, metaPixelResource(nativeID, name, metaPixelOwnerBusiness, shareErr == nil))
	if err != nil {
		return nil, err
	}
	result, err := a.trackingSourceResult(ctx, acct, resource, created, reused, reconciled, setDefault && shareErr == nil)
	if err != nil {
		return nil, err
	}
	result["owner"] = metaPixelOwnerBusiness
	result["ad_account_association"] = association
	if created || reconciled {
		a.emitTrackingSourceCreated(ctx, acct, resource, setDefault && shareErr == nil)
	}
	return result, nil
}

func metaPixelResource(nativeID, name, owner string, associated bool) discoveredResource {
	return discoveredResource{
		Kind: resourceTrackingSource, ProviderType: "meta_pixel", NativeID: nativeID,
		DisplayName: name, Status: "active",
		Capabilities: []string{"conversion_tracking", "server_events"},
		Metadata: map[string]any{
			"last_fired_time":       nil,
			"owner":                 owner,
			"ad_account_associated": associated,
			"tracking_verified":     false,
		},
		ManagedByApp: true,
	}
}

func (a *App) businessPixelsNamed(ctx *sdk.AppCtx, acct *adAccount, businessID, name string) ([]string, map[string]any) {
	rows, errOut := a.metaResourceRows(ctx, acct, "business_pixel_list", map[string]any{
		"businessId": businessID,
		"fields":     "id,name",
		"limit":      100,
	})
	if errOut != nil {
		return nil, errOut
	}
	ids := []string{}
	for _, row := range rows {
		if strings.EqualFold(strings.TrimSpace(firstString(row, "name")), name) {
			if id := firstString(row, "id"); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

func newStrings(after, before []string) []string {
	seen := make(map[string]bool, len(before))
	for _, id := range before {
		seen[id] = true
	}
	out := []string{}
	for _, id := range after {
		if !seen[id] {
			out = append(out, id)
		}
	}
	return out
}

// ---- Readiness ----

func trackingSourceReadinessSchema() map[string]any {
	return schemaObject(map[string]any{
		"ad_account_id":               map[string]any{"type": "integer"},
		"tracking_source_resource_id": map[string]any{"type": "integer"},
		"domains": map[string]any{
			"type": "array", "maxItems": 10,
			"items":       map[string]any{"type": "string"},
			"description": "Hostnames that should be sending events, such as shop.example.com. Each is checked for event receipt and, unless check_installation=false, for the Pixel in its homepage HTML.",
		},
		"required_events": map[string]any{
			"type": "array", "maxItems": 20,
			"items":       map[string]any{"type": "string"},
			"description": "Event names that must have been received, such as PageView, Lead, CompleteRegistration. Defaults to PageView.",
		},
		"lookback_hours":     map[string]any{"type": "integer", "minimum": 1, "maximum": 672, "default": 168},
		"check_installation": map[string]any{"type": "boolean", "default": true},
	}, []string{"ad_account_id"})
}

func (a *App) toolTrackingSourceReadinessGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, resource, errOut := a.resolveMetaTrackingSource(ctx, args, "tracking_source_readiness_get")
	if errOut != nil {
		return errOut, nil
	}
	domains, err := readinessDomains(args["domains"])
	if err != nil {
		return mcpError(err.Error()), nil
	}
	required := stringList(args["required_events"])
	if len(required) == 0 {
		required = []string{"PageView"}
	}
	lookback := intArg(args, "lookback_hours", 168)
	if lookback < 1 || lookback > 672 {
		return mcpError("lookback_hours must be between 1 and 672"), nil
	}
	since := time.Now().Add(-time.Duration(lookback) * time.Hour).Unix()
	pixelID := resource.NativeID

	checks := map[string]any{}
	blocking := []string{}
	unknown := []string{}

	pixel, pixelErr := a.execIntegrationTool(ctx, acct, "pixel_get", map[string]any{
		"pixelId": pixelID,
		"fields":  "id,owner_business{id},owner_ad_account{id,account_id},is_unavailable,last_fired_time",
	})
	ownerBusiness := ""
	if pixelErr != nil {
		checks["provider_available"] = map[string]any{"status": "unknown", "error": mcpErrorMessage(pixelErr)}
		unknown = append(unknown, "provider_available")
	} else {
		root := asMap(pixel)
		unavailable, _ := root["is_unavailable"].(bool)
		ownerBusiness = firstString(asMap(root["owner_business"]), "id")
		ownerType := metaPixelOwnerAdAccount
		if ownerBusiness != "" {
			ownerType = metaPixelOwnerBusiness
		}
		status := "pass"
		if unavailable {
			status = "fail"
			blocking = append(blocking, "Meta reports the Pixel as unavailable")
		}
		checks["provider_available"] = map[string]any{
			"status": status, "owner": ownerType, "last_fired_time": root["last_fired_time"],
			"note": "Availability does not mean events from a given domain are accepted.",
		}
		checks["ad_account_association"] = a.metaPixelAssociation(ctx, acct, pixelID, ownerBusiness, root)
		switch checks["ad_account_association"].(map[string]any)["status"] {
		case "fail":
			blocking = append(blocking, "the Pixel is not shared with this ad account")
		case "unknown":
			unknown = append(unknown, "ad_account_association")
		}
	}

	eventCounts, eventsErr := a.metaPixelStats(ctx, acct, pixelID, "event", since)
	if eventsErr != nil {
		checks["events_received"] = map[string]any{"status": "unknown", "error": mcpErrorMessage(eventsErr)}
		unknown = append(unknown, "events_received")
	} else {
		total := int64(0)
		for _, count := range eventCounts {
			total += count
		}
		status := "pass"
		if total == 0 {
			status = "fail"
			blocking = append(blocking, fmt.Sprintf("Meta received no events in the last %d hours", lookback))
		}
		checks["events_received"] = map[string]any{"status": status, "total": total, "by_event": eventCounts, "lookback_hours": lookback}
		requiredOut := []map[string]any{}
		for _, event := range required {
			count := eventCounts[event]
			requiredOut = append(requiredOut, map[string]any{"event": event, "received": count, "seen": count > 0})
			if count == 0 {
				blocking = append(blocking, event+" was not received")
			}
		}
		checks["required_events"] = requiredOut
	}

	if len(domains) > 0 {
		hostCounts, hostErr := a.metaPixelStats(ctx, acct, pixelID, "host", since)
		checkInstall := boolArgDefault(args, "check_installation", true)
		domainOut := []map[string]any{}
		for _, domain := range domains {
			entry := map[string]any{"domain": domain}
			if hostErr != nil {
				entry["events_received"] = map[string]any{"status": "unknown", "error": mcpErrorMessage(hostErr)}
				unknown = append(unknown, "domain:"+domain)
			} else {
				count := hostEventCount(hostCounts, domain)
				status := "pass"
				if count == 0 {
					status = "fail"
					blocking = append(blocking, "no events received from "+domain)
				}
				entry["events_received"] = map[string]any{"status": status, "count": count}
			}
			if checkInstall {
				entry["installation"] = checkPixelInstallation(context.Background(), domain, pixelID)
			}
			domainOut = append(domainOut, entry)
		}
		checks["domains"] = domainOut
	}

	diagnostics, diagErr := a.metaPixelDiagnostics(ctx, acct, pixelID)
	if diagErr != nil {
		checks["diagnostics"] = map[string]any{"status": "unknown", "error": mcpErrorMessage(diagErr)}
		unknown = append(unknown, "diagnostics")
	} else {
		status := "pass"
		for _, d := range diagnostics {
			if d["result"] == "failed" {
				status = "fail"
				blocking = append(blocking, "Meta diagnostic failed: "+firstString(d, "title", "key"))
			}
		}
		checks["diagnostics"] = map[string]any{"status": status, "issues": diagnostics}
	}

	readiness := "ready"
	switch {
	case len(blocking) > 0:
		readiness = "not_ready"
	case len(unknown) > 0:
		readiness = "unverified"
	}
	sort.Strings(unknown)
	return map[string]any{
		"resource":                resource.response(),
		"readiness":               readiness,
		"blocking_reasons":        blocking,
		"unverified_checks":       unknown,
		"checks":                  checks,
		"advertising_eligibility": "not_established",
		"limitations":             metaTrackingLimitations,
	}, nil
}

func (a *App) resolveMetaTrackingSource(ctx *sdk.AppCtx, args map[string]any, operation string) (*adAccount, *adResource, map[string]any) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return nil, nil, errOut
	}
	if acct.Platform != "meta" {
		out := mcpError(operation + " is not supported for " + acct.Platform)
		out["code"] = "unsupported_operation"
		out["operation"] = operation
		out["platform"] = acct.Platform
		return nil, nil, out
	}
	resource, resourceErr := a.resolveResourceChoice(
		ctx, acct, trackingSourceDefaultPurpose, resourceTrackingSource, "meta_pixel",
		int64(intArg(args, "tracking_source_resource_id", 0)),
	)
	if resourceErr != nil {
		return nil, nil, resourceErr
	}
	return acct, resource, nil
}

func (a *App) metaPixelAssociation(ctx *sdk.AppCtx, acct *adAccount, pixelID, ownerBusiness string, pixel map[string]any) map[string]any {
	accountID := metaNumericAccountID(acct.NativeAccountID)
	ownerAccount := asMap(pixel["owner_ad_account"])
	if metaNumericAccountID(firstString(ownerAccount, "account_id", "id")) == accountID {
		return map[string]any{"status": "pass", "via": "owner"}
	}
	if ownerBusiness == "" {
		return map[string]any{"status": "unknown", "error": "the Pixel has no visible owning Business or ad account"}
	}
	rows, errOut := a.metaResourceRows(ctx, acct, "pixel_shared_accounts_list", map[string]any{
		"pixelId": pixelID, "business": ownerBusiness, "fields": "id,account_id", "limit": 100,
	})
	if errOut != nil {
		return map[string]any{"status": "unknown", "error": mcpErrorMessage(errOut)}
	}
	for _, row := range rows {
		if metaNumericAccountID(firstString(row, "account_id", "id")) == accountID {
			return map[string]any{"status": "pass", "via": "business_share"}
		}
	}
	return map[string]any{"status": "fail", "shared_account_count": len(rows)}
}

// metaPixelStats sums counts per value across the hourly buckets /stats
// returns: {"data":[{"aggregation":"event","data":[{"value":"PageView","count":3}]}]}.
func (a *App) metaPixelStats(ctx *sdk.AppCtx, acct *adAccount, pixelID, aggregation string, since int64) (map[string]int64, map[string]any) {
	parsed, errOut := a.execIntegrationTool(ctx, acct, "pixel_stats", map[string]any{
		"pixelId":     pixelID,
		"aggregation": aggregation,
		"start_time":  fmt.Sprint(since),
	})
	if errOut != nil {
		return nil, errOut
	}
	counts := map[string]int64{}
	add := func(entry map[string]any) {
		if value := firstString(entry, "value"); value != "" {
			counts[value] += int64ArgAny(entry["count"])
		}
	}
	for _, bucket := range resultRows(parsed) {
		inner, _ := bucket["data"].([]any)
		if len(inner) == 0 {
			add(bucket)
			continue
		}
		for _, item := range inner {
			add(asMap(item))
		}
	}
	return counts, nil
}

func (a *App) metaPixelDiagnostics(ctx *sdk.AppCtx, acct *adAccount, pixelID string) ([]map[string]any, map[string]any) {
	parsed, errOut := a.execIntegrationTool(ctx, acct, "pixel_diagnostics", map[string]any{"pixelId": pixelID})
	if errOut != nil {
		return nil, errOut
	}
	issues := []map[string]any{}
	for _, row := range resultRows(parsed) {
		result := strings.ToLower(firstString(row, "result"))
		if result == "passed" {
			continue
		}
		issues = append(issues, map[string]any{
			"key": firstString(row, "key"), "title": firstString(row, "title"),
			"result": result, "description": firstString(row, "description"),
			"message": firstString(row, "user_message"), "action_uri": firstString(row, "action_uri"),
		})
	}
	return issues, nil
}

func hostEventCount(hosts map[string]int64, domain string) int64 {
	total := int64(0)
	for host, count := range hosts {
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if host == domain || host == "www."+domain || strings.TrimPrefix(domain, "www.") == host {
			total += count
		}
	}
	return total
}

var hostnamePattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

func readinessDomains(raw any) ([]string, error) {
	out := []string{}
	for _, value := range stringList(raw) {
		host := strings.ToLower(strings.TrimSpace(value))
		host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		host = strings.TrimSuffix(strings.SplitN(host, "/", 2)[0], ".")
		if !hostnamePattern.MatchString(host) || net.ParseIP(host) != nil {
			return nil, fmt.Errorf("domains must be public hostnames, got %q", value)
		}
		out = append(out, host)
	}
	if len(out) > 10 {
		return nil, fmt.Errorf("domains accepts at most 10 hostnames")
	}
	return out, nil
}

func stringList(raw any) []string {
	out := []string{}
	switch v := raw.(type) {
	case []string:
		for _, s := range v {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}

// fetchSiteHTML is replaced in tests.
var fetchSiteHTML = func(ctx context.Context, url string) (string, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", "Apteva-Ads-TrackingCheck/1.0")
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	return string(body), res.StatusCode, err
}

// checkPixelInstallation inspects the served homepage HTML. Consent managers
// and tag managers inject the Pixel after load, so absence in the initial HTML
// is reported as such, not as proof the Pixel is missing.
func checkPixelInstallation(ctx context.Context, domain, pixelID string) map[string]any {
	html, status, err := fetchSiteHTML(ctx, "https://"+domain+"/")
	if err != nil {
		return map[string]any{"status": "fetch_failed", "error": err.Error()}
	}
	if status >= 400 {
		return map[string]any{"status": "fetch_failed", "http_status": status}
	}
	hasBase := strings.Contains(html, "fbevents.js") || strings.Contains(html, "fbq(")
	hasTagManager := strings.Contains(html, "googletagmanager.com/gtm.js")
	switch {
	case hasBase && strings.Contains(html, pixelID):
		return map[string]any{"status": "pixel_found", "http_status": status}
	case hasBase:
		return map[string]any{"status": "other_pixel_found", "http_status": status,
			"note": "Meta base code is present but initializes a different Pixel or loads the id dynamically."}
	default:
		out := map[string]any{"status": "not_found_in_initial_html", "http_status": status,
			"note": "The Pixel may still load through a consent or tag manager; rely on events_received for this domain."}
		if hasTagManager {
			out["tag_manager_detected"] = true
		}
		return out
	}
}

// ---- Test events ----

var metaTestEventNames = []string{
	"PageView", "ViewContent", "Search", "Lead", "CompleteRegistration", "Contact",
	"AddToCart", "InitiateCheckout", "AddPaymentInfo", "Purchase",
}

func trackingSourceTestEventSchema() map[string]any {
	return schemaObject(map[string]any{
		"ad_account_id":               map[string]any{"type": "integer"},
		"tracking_source_resource_id": map[string]any{"type": "integer"},
		"test_event_code": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 64,
			"description": "Code shown in Events Manager > Test events for an open test session.",
		},
		"event_name":       map[string]any{"type": "string", "enum": metaTestEventNames, "default": "PageView"},
		"event_source_url": map[string]any{"type": "string", "description": "https URL on the site being tested."},
	}, []string{"ad_account_id", "test_event_code", "event_source_url"})
}

func (a *App) toolTrackingSourceTestEventSend(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, resource, errOut := a.resolveMetaTrackingSource(ctx, args, "tracking_source_test_event_send")
	if errOut != nil {
		return errOut, nil
	}
	code := strings.TrimSpace(stringArgAny(args, "test_event_code"))
	if code == "" || len(code) > 64 {
		return mcpError("test_event_code required (1-64 characters)"), nil
	}
	sourceURL := strings.TrimSpace(stringArgAny(args, "event_source_url"))
	if !strings.HasPrefix(sourceURL, "https://") {
		return mcpError("event_source_url must be an https URL"), nil
	}
	eventName := stringArgAny(args, "event_name")
	if eventName == "" {
		eventName = "PageView"
	}
	known := false
	for _, name := range metaTestEventNames {
		known = known || name == eventName
	}
	if !known {
		return mcpError("event_name must be one of " + strings.Join(metaTestEventNames, ", ")), nil
	}
	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	eventID := "apteva-test-" + hex.EncodeToString(nonce)
	externalID := sha256.Sum256([]byte(eventID))
	event := map[string]any{
		"event_name":       eventName,
		"event_time":       time.Now().Unix(),
		"event_id":         eventID,
		"action_source":    "website",
		"event_source_url": sourceURL,
		"user_data": map[string]any{
			"client_user_agent": "Apteva-Ads-TestEvent/1.0",
			"external_id":       []string{hex.EncodeToString(externalID[:])},
		},
	}
	if eventName == "Purchase" {
		event["custom_data"] = map[string]any{"currency": acct.Currency, "value": "1.00"}
	}
	parsed, sendErr := a.execIntegrationToolWithPolicy(ctx, acct, "pixel_send_event", map[string]any{
		"pixelId":         resource.NativeID,
		"data":            []any{event},
		"test_event_code": code,
	}, integrationExecPolicy{maxAttempts: 1})
	if sendErr != nil {
		return sendErr, nil
	}
	root := asMap(parsed)
	received := int64ArgAny(root["events_received"])
	messages := stringList(root["messages"])
	match := "unconfirmed"
	for _, message := range messages {
		lower := strings.ToLower(message)
		if strings.Contains(lower, "test") && (strings.Contains(lower, "code") || strings.Contains(lower, "session")) {
			match = "not_matched"
		}
	}
	return map[string]any{
		"resource":           resource.response(),
		"event_name":         eventName,
		"event_id":           eventID,
		"api_accepted":       received > 0,
		"events_received":    received,
		"provider_messages":  messages,
		"test_session_match": match,
		"verification": map[string]any{
			"visible_in_test_events":  "unconfirmed",
			"counted_in_reporting":    false,
			"advertising_eligibility": "not_established",
		},
		"note": "api_accepted only means the Conversions API took the payload. Confirm the event in Events Manager > Test events while the session for this code is open; test events never count toward reporting or optimization. Use tracking_source_readiness_get for real event receipt.",
	}, nil
}
