package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"sort"
	"strings"
	"time"
)

type conversionPoint struct {
	EntityID    string  `json:"entity_id"`
	Date        string  `json:"date"`
	EventID     string  `json:"event_id"`
	Event       string  `json:"event"`
	Source      string  `json:"measurement_source"`
	Window      string  `json:"attribution_window"`
	Count       float64 `json:"conversions"`
	ValueMicros *int64  `json:"value_micros"`
	Currency    string  `json:"currency"`
	FetchedAt   string  `json:"fetched_at"`
}

func (a *App) toolConversionPerformance(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, out := a.resolveAdAccount(ctx, args)
	if out != nil {
		return out, nil
	}
	request, err := validateGenericPerformanceRequest(args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	empty, out, err := a.scopePerformanceRequest(ctx, acct, def, request)
	if err != nil {
		return nil, err
	}
	if out != nil {
		return out, nil
	}
	if appID := int64(intArg(args, "mobile_app_resource_id", 0)); appID > 0 {
		if _, out := a.mobileApp(ctx, acct, appID); out != nil {
			return out, nil
		}
		if request.Level == "account" {
			return mcpError("app filtering requires campaign, ad_group or ad level"), nil
		}
		ids, err := mobileReportEntityIDs(ctx, acct, appID, request.Level)
		if err != nil {
			return nil, err
		}
		wanted := map[string]bool{}
		for _, id := range request.EntityIDs {
			wanted[id] = true
		}
		filtered := []string{}
		for _, id := range ids {
			if len(wanted) == 0 || wanted[id] {
				filtered = append(filtered, id)
			}
		}
		request.EntityIDs = filtered
		empty = len(filtered) == 0
	}
	if empty {
		return conversionReport([]conversionPoint{}, nil, "cache"), nil
	}
	request.IncludeEvents = true
	if request.Refresh {
		_, providerErr, syncErr, _ := a.syncAnalytics(ctx, acct.ProjectID, acct, request, "manual")
		if syncErr != nil {
			return nil, syncErr
		}
		if providerErr != nil {
			recordConversionSync(ctx, acct, request.Level, mcpErrorTextValue(providerErr))
			return providerErr, nil
		}
	}

	base, err := loadAnalyticsPoints(ctx, acct.ProjectID, acct.ID, request)
	if err != nil {
		return nil, err
	}
	if request.Refresh {
		if out := a.syncConversionCache(ctx, acct, request, base); out != nil {
			return out, nil
		}
	}
	events, err := loadConversionPoints(ctx, acct, request, args)
	if err != nil {
		return nil, err
	}
	source := "cache"
	if request.Refresh {
		source = "live"
	}
	report := conversionReport(events, base, source)
	sync, err := conversionSyncStatus(ctx, acct, request.Level)
	if err != nil {
		return nil, err
	}
	report["sync"] = sync
	if len(events) == 0 {
		report["measurement_status"] = "unavailable_or_no_reported_events"
	}
	return report, nil
}
func (a *App) fetchGoogleConversionPoints(ctx *sdk.AppCtx, acct *adAccount, r *genericPerformanceRequest) ([]conversionPoint, map[string]any) {
	if err := validateProviderAnalyticsIDs("google", r.EntityIDs); err != nil {
		return nil, mcpError(err.Error())
	}
	resource := map[string]string{"account": "customer", "campaign": "campaign", "ad_group": "ad_group", "ad": "ad_group_ad"}[r.Level]
	idField := map[string]string{"account": "customer.id", "campaign": "campaign.id", "ad_group": "ad_group.id", "ad": "ad_group_ad.ad.id"}[r.Level]
	// Conversion segmentation excludes delivery metrics by design. Costs remain
	// in ad_metric_points and are joined by entity/date only after aggregation.
	query := "SELECT segments.date, " + idField + ", segments.conversion_action, segments.conversion_action_name, segments.conversion_action_category, metrics.all_conversions, metrics.all_conversions_value FROM " + resource + " WHERE segments.date BETWEEN '" + r.DateFrom + "' AND '" + r.DateTo + "'"
	if len(r.EntityIDs) > 0 {
		query += " AND " + idField + " IN (" + strings.Join(r.EntityIDs, ",") + ")"
	}
	rows, out := a.googleResourceRows(ctx, acct, query)
	if out != nil {
		return nil, out
	}
	actions, err := a.listResources(ctx, acct, resourceConversionAction)
	if err != nil {
		return nil, mcpError(err.Error())
	}
	byID := map[string]adResource{}
	for _, action := range actions {
		byID[action.NativeID] = action
	}
	points := []conversionPoint{}
	for _, row := range rows {
		segments := mapAt(row, "segments")
		metrics := mapAt(row, "metrics")
		native := firstString(segments, "conversionAction", "conversion_action")
		id := lastResourceSegment(native)
		event := "custom"
		source := "provider"
		if action, ok := byID[id]; ok {
			event = firstString(action.Metadata, "event")
			if event == "" {
				event = googleMobileEvent(firstString(action.Metadata, "category"), action.DisplayName)
			}
			typ := firstString(action.Metadata, "type")
			if strings.Contains(typ, "FIREBASE") {
				source = "firebase"
			} else if strings.Contains(typ, "THIRD_PARTY_APP") {
				source = "mmp"
			} else if strings.Contains(typ, "GOOGLE_PLAY") {
				source = "google_play"
			} else if strings.HasPrefix(typ, "WEBPAGE") || strings.HasPrefix(typ, "GOOGLE_ANALYTICS_4") {
				source = "website"
			}
		}
		if event == "custom" {
			category := firstString(segments, "conversionActionCategory", "conversion_action_category")
			if category != "DOWNLOAD" {
				event = googleMobileEvent(category, "")
			}
		}
		entity := ""
		switch r.Level {
		case "account":
			entity = firstString(mapAt(row, "customer"), "id")
		case "campaign":
			entity = firstString(mapAt(row, "campaign"), "id")
		case "ad_group":
			entity = firstString(mapAt(row, "adGroup"), "id")
			if entity == "" {
				entity = firstString(mapAt(row, "ad_group"), "id")
			}
		case "ad":
			v := mapAt(row, "adGroupAd")
			if len(v) == 0 {
				v = mapAt(row, "ad_group_ad")
			}
			entity = firstString(asMap(v["ad"]), "id")
		}
		count := numericArgAny(metrics["allConversions"])
		if metrics["all_conversions"] != nil {
			count = numericArgAny(metrics["all_conversions"])
		}
		var valuePtr *int64
		valueText := firstString(metrics, "allConversionsValue", "all_conversions_value")
		value, err := decimalToMicros(valueText)
		if err != nil {
			return nil, mcpError(err.Error())
		}
		if valueText != "" {
			valuePtr = &value
		}
		points = append(points, conversionPoint{EntityID: entity, Date: firstString(segments, "date"), EventID: native, Event: event, Source: source, Window: "provider_default", Count: count, ValueMicros: valuePtr, Currency: acct.Currency, FetchedAt: time.Now().UTC().Format(time.RFC3339)})
	}
	return points, nil
}

// Each provider action remains its own row. For example an omni purchase total
// and an app purchase submetric are different measurements, never added together.
func providerConversionPoints(p analyticsPoint) []conversionPoint {
	out := []conversionPoint{}
	appendPoint := func(id, event, source string, count float64, value *int64) {
		out = append(out, conversionPoint{EntityID: p.EntityID, Date: p.Date, EventID: id, Event: event, Source: source, Window: "provider_default", Count: count, ValueMicros: value, Currency: p.Currency, FetchedAt: p.FetchedAt})
	}
	switch p.Platform {
	case "meta":
		values := actionMetricValues(p.ProviderMetrics["action_values"])
		for id, count := range actionMetricValues(p.ProviderMetrics["actions"]) {
			event, source, ok := metaEventKind(id)
			if !ok {
				continue
			}
			var value *int64
			if v, exists := values[id]; exists {
				micros := int64(v * 1e6)
				value = &micros
			}
			appendPoint(id, event, source, count, value)
		}
	case "x":
		for id, raw := range p.ProviderMetrics {
			event, source, ok := xEventKind(id)
			if ok {
				if windows := asMap(raw); len(windows) > 0 {
					for window, count := range windows {
						if window == "post_view" || window == "post_engagement" || window == "post_click" {
							appendPoint(id, event, source, numericArgAny(count), nil)
							out[len(out)-1].Window = window
						}
					}
				} else {
					appendPoint(id, event, source, numericArgAny(raw), nil)
				}
			}
		}
	case "reddit":
		metrics := asMap(p.ProviderMetrics["metrics"])
		if len(metrics) == 0 {
			metrics = p.ProviderMetrics
		}
		for id, raw := range metrics {
			upper := strings.ToUpper(id)
			source := "provider"
			event := ""
			if strings.HasPrefix(upper, "APP_INSTALL_") && strings.HasSuffix(upper, "_COUNT") {
				name := strings.TrimSuffix(strings.TrimPrefix(upper, "APP_INSTALL_"), "_COUNT")
				if strings.HasPrefix(name, "MMP_") {
					source = "mmp"
					name = strings.TrimPrefix(name, "MMP_")
				}
				if strings.HasPrefix(name, "SKAN_") {
					source = "skadnetwork"
					name = strings.TrimPrefix(name, "SKAN_")
				}
				event = redditReportEvent(name)
			} else if upper == "CONVERSION_PURCHASE_CLICKS" || upper == "CONVERSION_PURCHASE_VIEWS" {
				event = "purchase"
				source = "website"
			}
			if event != "" {
				count := numericArgAny(raw)
				if source == "skadnetwork" {
					count /= 1e6
				} // Reddit reports APP_INSTALL_SKAN* in millionths.
				appendPoint(upper, event, source, count, nil)
				if source == "website" && strings.HasSuffix(upper, "_CLICKS") {
					out[len(out)-1].Window = "post_click"
				}
				if source == "website" && strings.HasSuffix(upper, "_VIEWS") {
					out[len(out)-1].Window = "post_view"
				}
			}
		}
		for _, spec := range []struct{ field, source string }{{"APP_INSTALL_REVENUE", "provider"}, {"APP_INSTALL_MMP_REVENUE", "mmp"}, {"APP_INSTALL_SKAN_REVENUE", "skadnetwork"}} {
			if raw := metricFoldRaw(metrics, spec.field); raw != nil {
				value := int64(numericArgAny(raw)) // APP_INSTALL*REVENUE is already micros.
				appendPoint(spec.field, "revenue", spec.source, 0, &value)
			}
		}
	}
	return out
}
func metricFoldRaw(m map[string]any, key string) any {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return nil
}
func redditReportEvent(name string) string {
	return map[string]string{"INSTALL": "install", "TOTAL_INSTALL": "install_total", "REINSTALL": "reinstall", "APP_LAUNCH": "app_launch", "SIGN_UP": "registration", "START_TRIAL": "trial", "PURCHASE": "purchase", "FIRST_TIME_PURCHASE": "first_purchase", "SUBSCRIBE": "subscription", "ADD_TO_CART": "add_to_cart", "LEVEL_ACHIEVED": "level_achieved"}[name]
}
func metaEventKind(id string) (string, string, bool) {
	exact := map[string]string{"mobile_app_install": "install", "app_install": "install", "omni_app_install": "install_total", "app_custom_event.fb_mobile_complete_registration": "registration", "app_custom_event.fb_mobile_start_trial": "trial", "app_custom_event.fb_mobile_purchase": "purchase", "app_custom_event.fb_mobile_subscribe": "subscription", "app_custom_event.fb_mobile_add_to_cart": "add_to_cart", "offsite_conversion.fb_pixel_purchase": "purchase", "offsite_conversion.fb_pixel_lead": "lead", "offsite_conversion.fb_pixel_complete_registration": "registration", "omni_purchase": "purchase_total", "purchase": "purchase", "lead": "lead", "offsite_conversion": "conversion", "onsite_conversion": "conversion", "conversion": "conversion"}
	event, ok := exact[id]
	source := "provider"
	if strings.HasPrefix(id, "app_custom_event.") || id == "mobile_app_install" || id == "app_install" {
		source = "app"
	}
	if strings.HasPrefix(id, "offsite_conversion.fb_pixel_") {
		source = "website"
	}
	if !ok && strings.HasPrefix(id, "app_custom_event.") {
		event = "custom"
		ok = true
	}
	return event, source, ok
}
func xEventKind(id string) (string, string, bool) {
	// X uses mobile_conversion_* for app results, distinct from website conversion_*.
	for _, prefix := range []string{"mobile_conversion_", "conversion_"} {
		if strings.HasPrefix(id, prefix) {
			suffix := strings.TrimPrefix(id, prefix)
			source := "website"
			if prefix == "mobile_conversion_" {
				source = "mmp"
			}
			event := map[string]string{"installs": "install", "install": "install", "purchases": "purchase", "sign_ups": "registration", "downloads": "download", "site_visits": "page_view", "re_engages": "reengagement", "reinstalls": "reinstall"}[suffix]
			return event, source, event != ""
		}
	}
	return "", "", false
}

func storeConversionPoints(ctx *sdk.AppCtx, acct *adAccount, r *genericPerformanceRequest, points []conversionPoint) error {
	allowed := map[string]bool{}
	for _, id := range r.EntityIDs {
		allowed[id] = true
	}
	for _, p := range points {
		if p.EntityID == "" || p.EventID == "" || p.Date < r.DateFrom || p.Date > r.DateTo || (len(allowed) > 0 && !allowed[p.EntityID]) {
			return fmt.Errorf("conversion report returned a point outside the requested scope")
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	where := `project_id=? AND ad_account_id=? AND level=? AND point_date BETWEEN ? AND ?`
	args := []any{acct.ProjectID, acct.ID, r.Level, r.DateFrom, r.DateTo}
	if len(r.EntityIDs) > 0 {
		where += " AND native_entity_id IN (" + analyticsSQLPlaceholders(len(r.EntityIDs)) + ")"
		for _, id := range r.EntityIDs {
			args = append(args, id)
		}
	}
	if _, err = tx.Exec("DELETE FROM ad_conversion_points WHERE "+where, args...); err != nil {
		return err
	}
	for _, p := range points {
		_, err = tx.Exec(`INSERT INTO ad_conversion_points(project_id,ad_account_id,level,native_entity_id,point_date,event_id,event_name,measurement_source,attribution_window,conversions,value_micros,currency,fetched_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO UPDATE SET conversions=excluded.conversions,value_micros=excluded.value_micros,currency=excluded.currency,fetched_at=excluded.fetched_at`, acct.ProjectID, acct.ID, r.Level, p.EntityID, p.Date, p.EventID, p.Event, p.Source, p.Window, p.Count, p.ValueMicros, p.Currency, p.FetchedAt)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func loadConversionPoints(ctx *sdk.AppCtx, acct *adAccount, r *genericPerformanceRequest, filters map[string]any) ([]conversionPoint, error) {
	query := `SELECT native_entity_id,point_date,event_id,event_name,measurement_source,attribution_window,conversions,value_micros,currency,fetched_at FROM ad_conversion_points WHERE project_id=? AND ad_account_id=? AND level=? AND point_date BETWEEN ? AND ?`
	args := []any{acct.ProjectID, acct.ID, r.Level, r.DateFrom, r.DateTo}
	if len(r.EntityIDs) > 0 {
		query += " AND native_entity_id IN (" + analyticsSQLPlaceholders(len(r.EntityIDs)) + ")"
		for _, id := range r.EntityIDs {
			args = append(args, id)
		}
	}
	for _, f := range []struct{ arg, column string }{{"event", "event_name"}, {"measurement_source", "measurement_source"}} {
		if v := firstString(filters, f.arg); v != "" {
			query += " AND " + f.column + "=?"
			args = append(args, v)
		}
	}
	query += " ORDER BY point_date,native_entity_id,event_id,measurement_source"
	rows, err := ctx.AppDB().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []conversionPoint{}
	for rows.Next() {
		var p conversionPoint
		if err = rows.Scan(&p.EntityID, &p.Date, &p.EventID, &p.Event, &p.Source, &p.Window, &p.Count, &p.ValueMicros, &p.Currency, &p.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func conversionReport(points []conversionPoint, base []analyticsPoint, source string) map[string]any {
	spend := int64(0)
	for _, p := range base {
		spend += p.SpendMicros
	}
	groups := map[string]map[string]any{}
	incompleteValues := map[string]bool{}
	oldest, newest := "", ""
	for _, p := range points {
		key := p.EventID + "\x00" + p.Source + "\x00" + p.Window
		if p.ValueMicros == nil {
			incompleteValues[key] = true
		}
		if oldest == "" || p.FetchedAt < oldest {
			oldest = p.FetchedAt
		}
		if p.FetchedAt > newest {
			newest = p.FetchedAt
		}
		g := groups[key]
		if g == nil {
			g = map[string]any{"event_id": p.EventID, "event": p.Event, "measurement_source": p.Source, "attribution_window": p.Window, "conversions": float64(0), "value_micros": nil, "cost_per_event_micros": nil, "cpi_micros": nil, "roas": nil, "currency": p.Currency}
			groups[key] = g
		}
		g["conversions"] = g["conversions"].(float64) + p.Count
		if p.ValueMicros != nil {
			value := int64(0)
			if previous, ok := g["value_micros"].(int64); ok {
				value = previous
			}
			g["value_micros"] = value + *p.ValueMicros
		}
	}
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	summary := []map[string]any{}
	for _, key := range keys {
		g := groups[key]
		if incompleteValues[key] {
			g["value_micros"] = nil
		}
		count := g["conversions"].(float64)
		if count > 0 {
			g["cost_per_event_micros"] = safeRoundedRatio(spend, count)
			if g["event"] == "install" {
				g["cpi_micros"] = g["cost_per_event_micros"]
			}
		}
		if value, ok := g["value_micros"].(int64); ok && spend > 0 {
			g["roas"] = float64(value) / float64(spend)
		}
		summary = append(summary, g)
	}
	return map[string]any{"data": points, "events": summary, "spend_micros": spend, "source": source, "freshness": map[string]any{"fetched_at": oldest, "latest_fetched_at": newest}, "delivery_freshness": analyticsFreshness(base), "metric_notes": map[string]any{"conversions": "Each event/source/window is a separate measurement; totals, component events and sources can overlap and must not be summed.", "google": "Reports all_conversions, including secondary actions; existing performance_get retains its original conversions metric.", "missing": "Unavailable event/value data stays absent or null. A first open is distinct from an install.", "attribution": "provider_default uses provider-configured attribution; this response does not attest identical windows across networks."}}
}

func actionMetricValues(raw any) map[string]float64 {
	switch v := raw.(type) {
	case map[string]float64:
		return v
	case map[string]any:
		out := map[string]float64{}
		for k, value := range v {
			out[k] = numericArgAny(value)
		}
		return out
	default:
		return actionValues(raw)
	}
}

func mobileReportEntityIDs(ctx *sdk.AppCtx, acct *adAccount, appID int64, level string) ([]string, error) {
	query := `SELECT campaign_id FROM ad_mobile_campaigns WHERE project_id=? AND ad_account_id=? AND mobile_app_resource_id=?`
	if level != "campaign" {
		query = `SELECT native_entity_id FROM ad_entities WHERE project_id=? AND ad_account_id=? AND level='` + level + `' AND campaign_id IN (SELECT campaign_id FROM ad_mobile_campaigns WHERE mobile_app_resource_id=? AND project_id=ad_entities.project_id AND ad_account_id=ad_entities.ad_account_id)`
	}
	rows, err := ctx.AppDB().Query(query, acct.ProjectID, acct.ID, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
