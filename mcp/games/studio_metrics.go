package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func metricConnection(ctx *sdk.AppCtx, s GameScope, id int64, provider string) error {
	found := false
	for _, b := range ctx.IntegrationsFor("reporting") {
		if b.ConnectionID == id && b.AppSlug == provider {
			found = true
		}
	}
	if !found {
		return errors.New("reporting connection is not authorized for Games or provider differs")
	}
	c, e := ctx.PlatformAPI().GetConnection(id)
	if e != nil {
		return e
	}
	if c == nil || c.AppSlug != provider || (c.ProjectID != "" && c.ProjectID != s.ProjectID) {
		return errors.New("reporting connection belongs to another project/provider")
	}
	return nil
}
func metricExecute(ctx *sdk.AppCtx, s GameScope, source map[string]any, tool string, input map[string]any) (any, error) {
	id := number(source["connection_id"])
	if e := metricConnection(ctx, s, id, txt(source["provider"])); e != nil {
		return nil, e
	}
	r, e := ctx.PlatformAPI().ExecuteIntegrationTool(id, tool, input)
	if e != nil {
		return nil, e
	}
	if r == nil || !r.Success || r.Status >= 400 {
		return nil, fmt.Errorf("report provider rejected %s", tool)
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(r.Data))
	d.UseNumber()
	e = d.Decode(&v)
	if object(v)["error"] != nil {
		return nil, errors.New("provider returned a report error")
	}
	return v, e
}
func metricDiscover(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	if txt(args["provider"]) == "" {
		out := []map[string]any{}
		for _, b := range ctx.IntegrationsFor("reporting") {
			if metricConnection(ctx, s, b.ConnectionID, b.AppSlug) == nil {
				out = append(out, map[string]any{"connection_id": b.ConnectionID, "provider": b.AppSlug})
			}
		}
		return out, nil
	}
	switch txt(args["provider"]) {
	case "admob":
		if txt(args["account_id"]) == "" {
			return metricExecute(ctx, s, args, "list_accounts", nil)
		}
		return metricExecute(ctx, s, args, "list_apps", map[string]any{"account_id": args["account_id"], "pageToken": args["page_token"]})
	case "app-store-connect":
		return metricExecute(ctx, s, args, "list_apps", nil)
	case "google-analytics":
		return map[string]any{"required": []string{"external_id: GA4 property ID", "stream_id: game app stream ID"}}, nil
	}
	return nil, errors.New("unsupported reporting provider")
}
func metricSourceSet(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	provider := txt(args["provider"])
	if provider != "admob" && provider != "google-analytics" && provider != "app-store-connect" {
		return nil, errors.New("supported reporting providers: admob, google-analytics, app-store-connect")
	}
	external, e := requiredText(args, "external_id")
	if e != nil {
		return nil, e
	}
	if e = metricConnection(ctx, s, number(args["connection_id"]), provider); e != nil {
		return nil, e
	}
	source := map[string]any{"provider": provider, "connection_id": number(args["connection_id"]), "external_id": external, "account_id": txt(args["account_id"]), "stream_id": txt(args["stream_id"]), "platform": txt(args["platform"]), "family": txt(args["family"]), "timezone": txt(args["timezone"]), "config": object(args["config"])}
	switch provider {
	case "admob":
		if !strings.HasPrefix(txt(source["account_id"]), "pub-") {
			return nil, errors.New("AdMob account_id must be a publisher ID (pub-…)")
		}
		if source["family"] != "network" && source["family"] != "mediation" {
			return nil, errors.New("choose network or mediation; their totals must not be combined")
		}
		account, e := metricExecute(ctx, s, source, "get_account", map[string]any{"account_id": source["account_id"]})
		if e != nil {
			return nil, e
		}
		source["currency"] = object(account)["currencyCode"]
		source["timezone"] = object(account)["reportingTimeZone"]
		found := false
		token := ""
		for page := 0; page < 50; page++ {
			v, e := metricExecute(ctx, s, source, "list_apps", map[string]any{"account_id": source["account_id"], "pageToken": token})
			if e != nil {
				return nil, e
			}
			for _, app := range records(object(v)["apps"]) {
				if txt(app["appId"]) == external {
					found = true
				}
			}
			token = txt(object(v)["nextPageToken"])
			if found || token == "" {
				break
			}
		}
		if !found {
			return nil, errors.New("AdMob app not found in this publisher account")
		}
	case "google-analytics":
		if txt(source["stream_id"]) == "" {
			return nil, errors.New("stream_id required to isolate the game's GA4 stream")
		}
		source["family"] = "engagement"
		facts, err := fetchGA4(ctx, s, source, time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"))
		if err != nil {
			return nil, err
		}
		source["timezone"] = facts[0]["timezone"]
	case "app-store-connect":
		source["family"] = "store_sales"
		if txt(object(source["config"])["vendor_number"]) == "" {
			return nil, errors.New("config.vendor_number required for Apple sales reports")
		}
		app, e := metricExecute(ctx, s, source, "get_app", map[string]any{"app_id": external})
		if e != nil {
			return nil, e
		}
		if txt(object(object(app)["data"])["id"]) != external {
			return nil, errors.New("store app not found in selected account")
		}
		source["timezone"] = "America/Los_Angeles"
	}
	if _, e = time.LoadLocation(txt(source["timezone"])); e != nil {
		return nil, errors.New("valid provider reporting timezone required")
	}
	id := studioHash([]any{provider, source["connection_id"], external, source["account_id"], source["stream_id"], source["family"]})[:24]
	source["id"] = id
	if e = linkPut(ctx, s, "metric", id, source); e != nil {
		return nil, e
	}
	_, e = ctx.AppDB().Exec(`INSERT OR IGNORE INTO game_metric_syncs(project_id,game_id,source_id) VALUES(?,?,?)`, s.ProjectID, s.GameID, id)
	return source, e
}
func metricSources(ctx *sdk.AppCtx, s GameScope) ([]map[string]any, error) {
	sources, e := linksList(ctx, s, "metric")
	if e != nil {
		return nil, e
	}
	for _, source := range sources {
		var success, err, next string
		var failures int
		e = ctx.AppDB().QueryRow(`SELECT last_success,last_error,next_attempt,failures FROM game_metric_syncs WHERE project_id=? AND game_id=? AND source_id=?`, s.ProjectID, s.GameID, source["id"]).Scan(&success, &err, &next, &failures)
		if e != nil {
			return nil, e
		}
		source["last_success"] = success
		source["last_error"] = err
		source["next_attempt"] = next
		source["failures"] = failures
	}
	return sources, nil
}
func dayParts(day string) map[string]any {
	d, _ := time.Parse("2006-01-02", day)
	return map[string]any{"year": d.Year(), "month": int(d.Month()), "day": d.Day()}
}

// Each daily fact keeps provider-specific units and basis. Raw micros remain
// decimal strings; they are never parsed through float64.
func fetchAdMob(ctx *sdk.AppCtx, s GameScope, source map[string]any, day string) ([]map[string]any, error) {
	tool := "generate_network_report"
	if source["family"] == "mediation" {
		tool = "generate_mediation_report"
	}
	v, e := metricExecute(ctx, s, source, tool, map[string]any{"account_id": source["account_id"], "reportSpec": map[string]any{"dateRange": map[string]any{"startDate": dayParts(day), "endDate": dayParts(day)}, "dimensions": []string{"DATE", "APP"}, "metrics": []string{"ESTIMATED_EARNINGS", "IMPRESSIONS", "CLICKS"}, "dimensionFilters": []any{map[string]any{"dimension": "APP", "matchesAny": map[string]any{"values": []string{txt(source["external_id"])}}}}, "maxReportRows": 10000}})
	if e != nil {
		return nil, e
	}
	return parseAdMob(v, source, day)
}
func parseAdMob(v any, source map[string]any, day string) ([]map[string]any, error) {
	messages, ok := v.([]any)
	if !ok {
		return nil, errors.New("invalid AdMob report envelope")
	}
	header, footer := map[string]any(nil), map[string]any(nil)
	rows := []map[string]any{}
	for _, message := range messages {
		m := object(message)
		if m["error"] != nil {
			return nil, errors.New("AdMob report contains an error")
		}
		if h := object(m["header"]); h != nil {
			if header != nil {
				return nil, errors.New("duplicate report header")
			}
			header = h
		}
		if f := object(m["footer"]); f != nil {
			if footer != nil {
				return nil, errors.New("duplicate report footer")
			}
			footer = f
		}
		if r := object(m["row"]); r != nil {
			rows = append(rows, r)
		}
	}
	if header == nil || footer == nil {
		return nil, errors.New("incomplete AdMob report")
	}
	if warnings, ok := footer["warnings"].([]any); ok && len(warnings) > 0 {
		return nil, errors.New("AdMob warnings require review; report not imported")
	}
	matching := fmt.Sprint(footer["matchingRowCount"])
	n, e := strconv.Atoi(matching)
	if e != nil || n != len(rows) || n > 1 {
		return nil, errors.New("truncated or unexpected daily AdMob report")
	}
	localization := object(header["localizationSettings"])
	currency := txt(localization["currencyCode"])
	zone := txt(localization["reportingTimeZone"])
	if currency == "" {
		return nil, errors.New("AdMob currency missing")
	}
	if zone == "" {
		zone = txt(source["timezone"])
	}
	fact := map[string]any{"date": day, "currency": currency, "timezone": zone, "basis": "estimated", "earnings_micros": "0", "impressions": "0", "clicks": "0"}
	for _, row := range rows {
		dims := object(row["dimensionValues"])
		if txt(object(dims["APP"])["value"]) != txt(source["external_id"]) || txt(object(dims["DATE"])["value"]) != strings.ReplaceAll(day, "-", "") {
			return nil, errors.New("AdMob report returned a different app/date")
		}
		values := object(row["metricValues"])
		for _, f := range []struct{ metric, field, encoding string }{{"ESTIMATED_EARNINGS", "earnings_micros", "microsValue"}, {"IMPRESSIONS", "impressions", "integerValue"}, {"CLICKS", "clicks", "integerValue"}} {
			value := txt(object(values[f.metric])[f.encoding])
			n, ok := new(big.Int).SetString(value, 10)
			if !ok || n.Sign() < 0 {
				return nil, errors.New("invalid AdMob metric")
			}
			fact[f.field] = n.String()
		}
	}
	return []map[string]any{fact}, nil
}
func fetchGA4(ctx *sdk.AppCtx, s GameScope, source map[string]any, day string) ([]map[string]any, error) {
	names := []string{"activeUsers", "sessions", "eventCount"}
	metrics := []any{}
	for _, name := range names {
		metrics = append(metrics, map[string]any{"name": name})
	}
	v, e := metricExecute(ctx, s, source, "run_report", map[string]any{"property_id": source["external_id"], "dateRanges": []any{map[string]any{"startDate": day, "endDate": day}}, "dimensions": []any{map[string]any{"name": "date"}, map[string]any{"name": "streamId"}}, "metrics": metrics, "dimensionFilter": map[string]any{"filter": map[string]any{"fieldName": "streamId", "stringFilter": map[string]any{"matchType": "EXACT", "value": source["stream_id"]}}}, "limit": 10000})
	if e != nil {
		return nil, e
	}
	m := object(v)
	rows := records(m["rows"])
	if number(m["rowCount"]) != int64(len(rows)) || len(rows) > 1 {
		return nil, errors.New("GA4 report incomplete or has unexpected dimensions")
	}
	meta := object(m["metadata"])
	if meta["subjectToThresholding"] == true || meta["dataLossFromOtherRow"] == true {
		return nil, errors.New("GA4 report is thresholded or has lost detail")
	}
	if samples, ok := meta["samplingMetadatas"].([]any); ok && len(samples) > 0 {
		return nil, errors.New("sampled GA4 report requires review")
	}
	zone := txt(meta["timeZone"])
	if zone == "" {
		zone = txt(source["timezone"])
	}
	if zone == "" {
		return nil, errors.New("GA4 reporting timezone missing")
	}
	fact := map[string]any{"date": day, "timezone": zone, "basis": "reported", "active_users": "0", "sessions": "0", "event_count": "0"}
	for _, row := range rows {
		dims := records(row["dimensionValues"])
		values := records(row["metricValues"])
		if len(dims) != 2 || txt(dims[0]["value"]) != strings.ReplaceAll(day, "-", "") || txt(dims[1]["value"]) != txt(source["stream_id"]) || len(values) != 3 {
			return nil, errors.New("GA4 app/date/metric mismatch")
		}
		for i, key := range []string{"active_users", "sessions", "event_count"} {
			n, ok := new(big.Int).SetString(txt(values[i]["value"]), 10)
			if !ok || n.Sign() < 0 {
				return nil, errors.New("invalid GA4 count")
			}
			fact[key] = n.String()
		}
	}
	return []map[string]any{fact}, nil
}
func fetchAppleSales(ctx *sdk.AppCtx, s GameScope, source map[string]any, day string) ([]map[string]any, error) {
	v, e := metricExecute(ctx, s, source, "download_sales_report", map[string]any{"vendor_number": object(source["config"])["vendor_number"], "report_type": "SALES", "report_subtype": "SUMMARY", "frequency": "DAILY", "report_date": day, "version": "1_0"})
	if e != nil {
		return nil, e
	}
	m := object(v)
	if m["_binary"] != true {
		return nil, errors.New("expected binary Apple sales report")
	}
	if len(txt(m["base64"])) > 32<<20 {
		return nil, errors.New("sales report exceeds import limit")
	}
	b, e := base64.StdEncoding.DecodeString(txt(m["base64"]))
	if e != nil {
		return nil, e
	}
	z, e := gzip.NewReader(bytes.NewReader(b))
	if e != nil {
		return nil, e
	}
	defer z.Close()
	b, e = io.ReadAll(io.LimitReader(z, 64<<20+1))
	if e != nil || len(b) > 64<<20 {
		return nil, errors.New("expanded sales report too large or invalid")
	}
	return parseAppleSales(b, source, day)
}
func parseAppleSales(data []byte, source map[string]any, day string) ([]map[string]any, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = '\t'
	header, e := reader.Read()
	if e != nil {
		return nil, e
	}
	columns := map[string]int{}
	for i, name := range header {
		columns[strings.TrimPrefix(name, "\ufeff")] = i
	}
	for _, name := range []string{"Apple Identifier", "Units", "Developer Proceeds", "Currency of Proceeds", "Begin Date", "End Date"} {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("sales column missing: %s", name)
		}
	}
	grouped := map[string]map[string]any{}
	for {
		row, e := reader.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if row[columns["Apple Identifier"]] != txt(source["external_id"]) {
			continue
		}
		for _, field := range []string{"Begin Date", "End Date"} {
			d, e := time.Parse("01/02/2006", row[columns[field]])
			if e != nil || d.Format("2006-01-02") != day {
				return nil, errors.New("sales report date mismatch")
			}
		}
		units, ok := new(big.Int).SetString(row[columns["Units"]], 10)
		if !ok {
			return nil, errors.New("invalid store units")
		}
		proceeds, ok := new(big.Rat).SetString(row[columns["Developer Proceeds"]])
		if !ok {
			return nil, errors.New("invalid store proceeds")
		}
		proceeds.Mul(proceeds, new(big.Rat).SetInt(units))
		proceeds.Mul(proceeds, big.NewRat(1000000, 1))
		if !proceeds.IsInt() {
			return nil, errors.New("store proceeds exceed micros precision")
		}
		currency := row[columns["Currency of Proceeds"]]
		if len(currency) != 3 {
			return nil, errors.New("invalid store currency")
		}
		fact := grouped[currency]
		if fact == nil {
			fact = map[string]any{"date": day, "timezone": "America/Los_Angeles", "currency": currency, "basis": "sales_report_proceeds", "units": "0", "proceeds_micros": "0"}
			grouped[currency] = fact
		}
		old, _ := new(big.Int).SetString(txt(fact["units"]), 10)
		fact["units"] = old.Add(old, units).String()
		old, _ = new(big.Int).SetString(txt(fact["proceeds_micros"]), 10)
		fact["proceeds_micros"] = old.Add(old, proceeds.Num()).String()
	}
	out := []map[string]any{}
	for _, fact := range grouped {
		out = append(out, fact)
	}
	return out, nil
}
func syncMetricSource(ctx *sdk.AppCtx, s GameScope, id string, days int) (result any, err error) {
	source, err := linkGet(ctx, s, "metric", id)
	if err != nil {
		return nil, err
	}
	if _, err = studioBinding(ctx, "analytics"); err != nil {
		return nil, err
	}
	token := randomID()
	now := time.Now().UTC()
	res, err := ctx.AppDB().Exec(`UPDATE game_metric_syncs SET lease_until=?,lease_token=? WHERE project_id=? AND game_id=? AND source_id=? AND lease_until<?`, now.Add(30*time.Minute).Format(time.RFC3339), token, s.ProjectID, s.GameID, id, now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, errors.New("source is already syncing")
	}
	defer func() {
		message := ""
		success := ""
		next := time.Now().Add(6 * time.Hour)
		if err != nil {
			message = err.Error()
			next = time.Now().Add(30 * time.Minute)
		} else {
			success = nowRFC()
		}
		_, e := ctx.AppDB().Exec(`UPDATE game_metric_syncs SET last_error=?,last_success=CASE WHEN ?='' THEN last_success ELSE ? END,next_attempt=?,lease_until='',lease_token='',failures=CASE WHEN ?='' THEN 0 ELSE failures+1 END WHERE project_id=? AND game_id=? AND source_id=? AND lease_token=?`, message, success, success, next.UTC().Format(time.RFC3339), message, s.ProjectID, s.GameID, id, token)
		if err == nil {
			err = e
		}
	}()
	zone, err := time.LoadLocation(txt(source["timezone"]))
	if err != nil {
		return nil, err
	}
	count := 0
	for i := days; i >= 1; i-- {
		day := now.In(zone).AddDate(0, 0, -i).Format("2006-01-02")
		var facts []map[string]any
		switch source["provider"] {
		case "admob":
			facts, err = fetchAdMob(ctx, s, source, day)
		case "google-analytics":
			facts, err = fetchGA4(ctx, s, source, day)
		case "app-store-connect":
			facts, err = fetchAppleSales(ctx, s, source, day)
		default:
			err = errors.New("unsupported provider")
		}
		if err != nil {
			return nil, err
		}
		// One replaceable event per source/day contains the complete report result,
		// including an empty result. Currency rows removed in corrections disappear.
		props := map[string]any{"game_id": s.GameID, "source_id": id, "provider": source["provider"], "family": source["family"], "platform": source["platform"], "external_id": source["external_id"], "date": day, "facts": facts, "refreshed_at": nowRFC(), "timezone": source["timezone"]}
		d, _ := time.ParseInLocation("2006-01-02", day, zone)
		key := studioHash([]any{s.ProjectID, s.GameID, id, day})
		var out map[string]any
		err = ctx.PlatformAPI().CallAppResult("analytics", "analytics_track", map[string]any{"_project_id": s.ProjectID, "app": "games", "event": "provider_daily", "upsert_key": "games:report:" + key, "ts": d.UnixMilli(), "props": props}, &out)
		if err != nil {
			return nil, err
		}
		if out["reject"] == true || out["rejected"] == true || out["valid"] == false {
			return nil, errors.New("Analytics rejected the report event")
		}
		count++
		if _, err = ctx.AppDB().Exec(`UPDATE game_metric_syncs SET lease_until=? WHERE project_id=? AND game_id=? AND source_id=? AND lease_token=?`, time.Now().Add(30*time.Minute).UTC().Format(time.RFC3339), s.ProjectID, s.GameID, id, token); err != nil {
			return nil, err
		}
	}
	return map[string]any{"days": count, "source_id": id, "status": "synced"}, nil
}
func gameMetricsQuery(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	if _, e := studioBinding(ctx, "analytics"); e != nil {
		return nil, e
	}
	where := map[string]any{"props.game_id": s.GameID}
	if id := txt(args["source_id"]); id != "" {
		if _, e := linkGet(ctx, s, "metric", id); e != nil {
			return nil, e
		}
		where["props.source_id"] = id
	}
	topic := stringArg(args, "topic", "provider_daily")
	if topic != "provider_daily" && !strings.HasPrefix(topic, "play.") {
		return nil, errors.New("choose provider_daily or a play.* telemetry topic")
	}
	in := map[string]any{"app": "games", "topic": topic, "where": where, "limit": boundedArg(args, "limit", 100, 1, 500)}
	for _, k := range []string{"since", "until"} {
		if v := number(args[k]); v > 0 {
			in[k] = v
		}
	}
	return studioCall(ctx, s, "analytics", "analytics_query", in)
}
func studioMetricsWorker(call context.Context, ctx *sdk.AppCtx) error {
	rows, e := ctx.AppDB().Query(`SELECT s.project_id,s.game_id,s.source_id FROM game_metric_syncs s JOIN games g ON g.project_id=s.project_id AND g.id=s.game_id WHERE g.status='active' AND s.next_attempt<=? AND s.lease_until<? ORDER BY s.next_attempt LIMIT 4`, nowRFC(), nowRFC())
	if e != nil {
		return e
	}
	type task struct{ p, g, id string }
	tasks := []task{}
	for rows.Next() {
		var t task
		if e = rows.Scan(&t.p, &t.g, &t.id); e != nil {
			rows.Close()
			return e
		}
		tasks = append(tasks, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, t := range tasks {
		if call.Err() != nil {
			return call.Err()
		}
		if _, e = syncMetricSource(ctx, GameScope{t.p, t.g}, t.id, 7); e != nil {
			ctx.Logger().Warn("game report sync failed", "source", t.id, "error", e)
		}
	}
	return nil
}
