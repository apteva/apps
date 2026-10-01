package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

var playPackagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
var playMonthPattern = regexp.MustCompile(`^[0-9]{4}(0[1-9]|1[0-2])$`)
var playMoneyPattern = regexp.MustCompile(`^-?([0-9]+|[0-9]{1,3}(,[0-9]{3})+)(\.[0-9]+)?$`)

func validPlayPackage(s string) bool { return len(s) <= 255 && playPackagePattern.MatchString(s) }

// A missing monthly export is normal before Google publishes it. Other errors
// remain failures so missing permissions and malformed reports are visible.
func playReportTool(ctx *sdk.AppCtx, s GameScope, source map[string]any, tool, month string) (any, bool, error) {
	id := number(source["connection_id"])
	if err := metricConnection(ctx, s, id, "google-play-developer"); err != nil {
		return nil, false, err
	}
	r, err := ctx.PlatformAPI().ExecuteIntegrationTool(id, tool, map[string]any{"year_month": month})
	if err != nil {
		return nil, false, err
	}
	if r != nil && r.Status == 404 {
		return nil, false, nil
	}
	if r == nil || !r.Success || r.Status >= 400 {
		return nil, false, fmt.Errorf("Google Play rejected %s", tool)
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(r.Data))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, false, err
	}
	if object(value)["error"] != nil {
		return nil, false, errors.New("Google Play returned a report error")
	}
	return value, true, nil
}

func playAvailableSalesMonths(ctx *sdk.AppCtx, s GameScope, source map[string]any) ([]string, error) {
	months := map[string]bool{}
	token := ""
	for page := 0; page < 20; page++ {
		v, err := metricExecute(ctx, s, source, "list_sales_reports", map[string]any{"pageToken": token, "maxResults": 1000})
		if err != nil {
			return nil, err
		}
		m := object(v)
		for _, item := range records(m["items"]) {
			name := txt(item["name"])
			if strings.HasPrefix(name, "sales/salesreport_") && strings.HasSuffix(name, ".zip") {
				month := strings.TrimSuffix(strings.TrimPrefix(name, "sales/salesreport_"), ".zip")
				if playMonthPattern.MatchString(month) {
					months[month] = true
				}
			}
		}
		next := txt(m["nextPageToken"])
		if next == "" {
			out := make([]string, 0, len(months))
			for month := range months {
				out = append(out, month)
			}
			sort.Sort(sort.Reverse(sort.StringSlice(out)))
			return out, nil
		}
		if next == token {
			return nil, errors.New("Google Play report pagination did not advance")
		}
		token = next
	}
	return nil, errors.New("Google Play report listing exceeds page limit")
}

func playReportMonths(ctx *sdk.AppCtx, s GameScope, source map[string]any, requested string) ([]string, error) {
	now := time.Now().UTC()
	current := now.Format("200601")
	if requested != "" {
		if !playMonthPattern.MatchString(requested) || requested > current {
			return nil, errors.New("month must be YYYYMM and no later than the current month")
		}
		return []string{requested}, nil
	}
	if source["family"] == "earnings" {
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return []string{first.AddDate(0, -1, 0).Format("200601"), first.AddDate(0, -2, 0).Format("200601")}, nil
	}
	available, err := playAvailableSalesMonths(ctx, s, source)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, month := range available {
		if month <= current {
			out = append(out, month)
		}
		if len(out) == 2 {
			break
		}
	}
	return out, nil
}

func syncPlayReports(ctx *sdk.AppCtx, s GameScope, source map[string]any, requested, leaseToken string) (any, error) {
	months, err := playReportMonths(ctx, s, source, requested)
	if err != nil {
		return nil, err
	}
	tool := "get_sales_report"
	if source["family"] == "earnings" {
		tool = "get_earnings_report"
	}
	count := 0
	for _, month := range months {
		v, found, err := playReportTool(ctx, s, source, tool, month)
		if err != nil {
			return nil, err
		}
		if !found {
			if requested != "" {
				return nil, fmt.Errorf("Google Play report for %s is not available", month)
			}
			continue
		}
		facts, err := parsePlayReport(v, source, month)
		if err != nil {
			return nil, fmt.Errorf("Google Play %s report %s: %w", source["family"], month, err)
		}
		zone, _ := time.LoadLocation(txt(source["timezone"]))
		period, _ := time.ParseInLocation("200601", month, zone)
		if err := writeMetricFacts(ctx, s, source, "provider_monthly", month, period, facts); err != nil {
			return nil, err
		}
		count++
		_, err = ctx.AppDB().Exec(`UPDATE game_metric_syncs SET lease_until=? WHERE project_id=? AND game_id=? AND source_id=? AND lease_token=?`, time.Now().Add(30*time.Minute).UTC().Format(time.RFC3339), s.ProjectID, s.GameID, source["id"], leaseToken)
		if err != nil {
			return nil, err
		}
	}
	if count == 0 {
		return nil, errors.New("no Google Play reports are available for the selected period")
	}
	return map[string]any{"months": count, "source_id": source["id"], "status": "synced"}, nil
}

func writeMetricFacts(ctx *sdk.AppCtx, s GameScope, source map[string]any, topic, period string, at time.Time, facts []map[string]any) error {
	props := map[string]any{"game_id": s.GameID, "source_id": source["id"], "provider": source["provider"], "family": source["family"], "platform": source["platform"], "external_id": source["external_id"], "facts": facts, "refreshed_at": nowRFC(), "timezone": source["timezone"]}
	if topic == "provider_monthly" {
		props["month"] = period
	} else {
		props["date"] = period
	}
	identity := []any{s.ProjectID, s.GameID, source["id"], period}
	if topic == "provider_monthly" {
		identity = append(identity, topic)
	}
	key := studioHash(identity)
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("analytics", "analytics_track", map[string]any{"_project_id": s.ProjectID, "app": "games", "event": topic, "upsert_key": "games:report:" + key, "ts": at.UnixMilli(), "props": props}, &out); err != nil {
		return err
	}
	if out["reject"] == true || out["rejected"] == true || out["valid"] == false {
		return errors.New("Analytics rejected the report event")
	}
	return nil
}

func parsePlayReport(v any, source map[string]any, month string) ([]map[string]any, error) {
	m := object(v)
	if m["_binary"] != true || len(txt(m["base64"])) > 32<<20 {
		return nil, errors.New("expected bounded binary ZIP report")
	}
	b, err := base64.StdEncoding.DecodeString(txt(m["base64"]))
	if err != nil || len(b) > 24<<20 {
		return nil, errors.New("invalid or oversized ZIP report")
	}
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil || len(z.File) == 0 || len(z.File) > 16 {
		return nil, errors.New("invalid ZIP report")
	}
	grouped := map[string]map[string]any{}
	csvCount := 0
	for _, file := range z.File {
		if file.FileInfo().IsDir() || !strings.HasSuffix(strings.ToLower(file.Name), ".csv") {
			continue
		}
		if csvCount != 0 {
			return nil, errors.New("ZIP report contains multiple CSV files")
		}
		if file.UncompressedSize64 > 64<<20 {
			return nil, errors.New("expanded report exceeds limit")
		}
		r, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(r, 64<<20+1))
		closeErr := r.Close()
		if readErr != nil || closeErr != nil || len(data) > 64<<20 {
			return nil, errors.New("expanded report too large or invalid")
		}
		if err := parsePlayCSV(data, source, month, grouped); err != nil {
			return nil, err
		}
		csvCount++
	}
	if csvCount == 0 {
		return nil, errors.New("ZIP report contains no CSV")
	}
	keys := make([]string, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, grouped[k])
	}
	return out, nil
}

func parsePlayCSV(data []byte, source map[string]any, month string, grouped map[string]map[string]any) error {
	var err error
	data, err = decodePlayCSV(data)
	if err != nil {
		return err
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return err
	}
	columns := map[string]int{}
	for i, name := range header {
		columns[strings.TrimSpace(strings.TrimPrefix(name, "\ufeff"))] = i
	}
	family := txt(source["family"])
	required := []string{"Package ID"}
	if family == "sales" {
		required = append(required, "Order Charged Date", "Financial Status", "Currency of Sale", "Charged Amount")
	} else {
		required = append(required, "Transaction Type", "Merchant Currency", "Amount (Merchant Currency)")
	}
	for _, key := range required {
		if _, ok := columns[key]; !ok {
			return fmt.Errorf("Play report column missing: %s", key)
		}
	}
	for rowNumber := 0; ; rowNumber++ {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || rowNumber > 500000 || len(row) != len(header) {
			return errors.New("invalid or oversized Play CSV")
		}
		if row[columns["Package ID"]] != txt(source["external_id"]) {
			continue
		}
		currencyKey, amountKey, typeKey := "Currency of Sale", "Charged Amount", "Financial Status"
		field := "buyer_paid_micros"
		basis := "estimated_buyer_paid_before_fees"
		if family == "earnings" {
			currencyKey, amountKey, typeKey = "Merchant Currency", "Amount (Merchant Currency)", "Transaction Type"
			field, basis = "merchant_amount_micros", "earnings_report_line"
		}
		currency, typ := strings.TrimSpace(row[columns[currencyKey]]), strings.TrimSpace(row[columns[typeKey]])
		if len(currency) != 3 || typ == "" {
			return errors.New("invalid Play currency or transaction type")
		}
		amount, err := decimalMicros(row[columns[amountKey]])
		if err != nil {
			return err
		}
		key := currency + "\x00" + typ
		fact := grouped[key]
		if fact == nil {
			fact = map[string]any{"month": month, "timezone": source["timezone"], "currency": currency, "basis": basis, "rows": "0", field: "0"}
			if family == "sales" {
				fact["financial_status"] = typ
			} else {
				fact["transaction_type"] = typ
			}
			grouped[key] = fact
		}
		old, _ := new(big.Int).SetString(txt(fact[field]), 10)
		fact[field] = old.Add(old, amount).String()
		count, _ := new(big.Int).SetString(txt(fact["rows"]), 10)
		fact["rows"] = count.Add(count, big.NewInt(1)).String()
	}
	return nil
}

func decodePlayCSV(data []byte) ([]byte, error) {
	little, isUTF16 := false, false
	if len(data) >= 2 {
		switch {
		case data[0] == 0xff && data[1] == 0xfe:
			little, isUTF16, data = true, true, data[2:]
		case data[0] == 0xfe && data[1] == 0xff:
			isUTF16, data = true, data[2:]
		case data[1] == 0:
			little, isUTF16 = true, true
		case data[0] == 0:
			isUTF16 = true
		}
	}
	if !isUTF16 {
		if !utf8.Valid(data) {
			return nil, errors.New("invalid Play CSV encoding")
		}
		return bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}), nil
	}
	if len(data)%2 != 0 {
		return nil, errors.New("truncated UTF-16 Play CSV")
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		if little {
			units[i] = uint16(data[2*i]) | uint16(data[2*i+1])<<8
		} else {
			units[i] = uint16(data[2*i])<<8 | uint16(data[2*i+1])
		}
	}
	return []byte(string(utf16.Decode(units))), nil
}

func decimalMicros(value string) (*big.Int, error) {
	value = strings.TrimSpace(value)
	if !playMoneyPattern.MatchString(value) {
		return nil, errors.New("invalid Play monetary amount")
	}
	value = strings.ReplaceAll(value, ",", "")
	n, ok := new(big.Rat).SetString(value)
	if !ok {
		return nil, errors.New("invalid Play monetary amount")
	}
	n.Mul(n, big.NewRat(1000000, 1))
	if !n.IsInt() {
		return nil, errors.New("Play monetary amount exceeds micros precision")
	}
	return n.Num(), nil
}
