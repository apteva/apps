package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxMoney int64 = 1_000_000_000_000

func supportedCurrency(s string) bool {
	return len(s) == 3 && strings.Contains(" AED AFN ALL AMD ANG AOA ARS AUD AWG AZN BAM BBD BDT BGN BMD BND BOB BRL BSD BTN BWP BYN BZD CAD CDF CHF CNY COP CRC CUP CVE CZK DKK DOP DZD EGP ERN ETB EUR FJD FKP GBP GEL GHS GIP GMD GTQ GYD HKD HNL HRK HTG HUF IDR ILS INR IRR ISK JMD KES KGS KHR KPW KYD KZT LAK LBP LKR LRD LSL MAD MDL MKD MMK MNT MOP MRU MUR MVR MWK MXN MYR MZN NAD NGN NIO NOK NPR NZD PAB PEN PGK PHP PKR PLN QAR RON RSD RUB SAR SBD SCR SDG SEK SGD SHP SLE SOS SRD SSP STN SVC SYP SZL THB TJS TMT TOP TRY TTD TWD TZS UAH USD UYU UZS VES WST XCD YER ZAR ZMW ZWG ", " "+s+" ")
}

func numeric(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	case string:
		return strconv.ParseFloat(strings.TrimSpace(n), 64)
	}
	return 0, errors.New("must be a number")
}
func validateLineInput(m map[string]any) error {
	for _, key := range []string{"quantity", "unit_price_cents", "tax_rate_bps"} {
		v, ok := m[key]
		if !ok {
			continue
		}
		n, err := numeric(v)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Errorf("%s must be finite", key)
		}
		if key == "quantity" {
			if n <= 0 || n > 1e6 {
				return errors.New("quantity must be > 0 and <= 1000000")
			}
		} else if n != math.Trunc(n) {
			return fmt.Errorf("%s must be an integer", key)
		}
		if key == "unit_price_cents" && math.Abs(n) > float64(maxMoney) {
			return errors.New("unit price exceeds supported range")
		}
		if key == "tax_rate_bps" && (n < 0 || n > 100000) {
			return errors.New("tax_rate_bps out of range")
		}
	}
	if len(strArg(m, "description")) > 16000 {
		return errors.New("description too long")
	}
	if math.Abs(float64Arg(m, "quantity", 1)*float64(int64Arg(m, "unit_price_cents"))) > float64(maxMoney) {
		return errors.New("line amount exceeds supported range")
	}
	return nil
}
func validateInput(m map[string]any) error {
	for k, v := range m {
		if strings.HasPrefix(k, "_") {
			continue
		}
		if k == "line_items" {
			items, ok := v.([]any)
			if !ok || len(items) > 1000 {
				return errors.New("line_items must be an array of at most 1000 items")
			}
			for _, raw := range items {
				li, ok := raw.(map[string]any)
				if !ok {
					return errors.New("line item must be an object")
				}
				if err := validateLineInput(li); err != nil {
					return err
				}
			}
			continue
		}
		if k == "patch" || k == "defaults" {
			obj, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("%s must be an object", k)
			}
			if err := validateInput(obj); err != nil {
				return err
			}
			continue
		}
		if k == "metadata" || k == "billing_address" || k == "address" || k == "bank" {
			if _, ok := v.(map[string]any); !ok {
				if _, raw := v.(json.RawMessage); !raw {
					return fmt.Errorf("%s must be an object", k)
				}
			}
			continue
		}
		if k == "tax_ids" {
			if _, ok := v.([]any); !ok {
				return errors.New("tax_ids must be an array")
			}
			continue
		}
		if k == "currency" {
			if strArg(m, k) != "" && !supportedCurrency(strings.ToUpper(strArg(m, k))) {
				return errors.New("unsupported currency")
			}
		}
		if k == "due_date" || k == "accounting_date" {
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("%s must be a string", k)
			}
			if s != "" {
				if _, e := parseBillingTime(s); e != nil {
					return fmt.Errorf("%s must be a valid date in YYYY-MM-DD format or RFC3339 timestamp", k)
				}
			}
		}
		if k == "quantity" || k == "unit_price_cents" || k == "tax_rate_bps" {
			if err := validateLineInput(m); err != nil {
				return err
			}
		}
		if k == "amount_cents" || k == "id" || strings.Contains(" invoice_id customer_id payment_method_id line_item_id product_id price_id payment_id ", " "+k+" ") || k == "limit" || k == "offset" {
			n, e := numeric(v)
			if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || math.Abs(n) > float64(maxMoney) {
				return fmt.Errorf("%s must be an integer within supported range", k)
			}
			if k != "amount_cents" && n < 0 {
				return fmt.Errorf("%s must not be negative", k)
			}
		}
		if s, ok := v.(string); ok && len(s) > 64000 {
			return fmt.Errorf("%s too long", k)
		}
	}
	return nil
}
func validatedHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" || r.Method == "PATCH" || r.Method == "PUT" {
			if r.URL.Path != "/webhooks/stripe" {
				raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
				if err != nil {
					httpErr(w, 413, "request body too large")
					return
				}
				if len(bytes.TrimSpace(raw)) > 0 {
					var m map[string]any
					if json.Unmarshal(raw, &m) != nil || m == nil {
						httpErr(w, 400, "expected JSON object")
						return
					}
					if err := validateInput(m); err != nil {
						httpErr(w, 400, err.Error())
						return
					}
				}
				r.Body = io.NopCloser(bytes.NewReader(raw))
			}
		}
		next(w, r)
	}
}
func parseBillingTime(s string) (time.Time, error) {
	for _, f := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, e := time.Parse(f, s); e == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("expected ISO date or RFC3339 timestamp")
}
func invoiceCurrency(ctx *sdk.AppCtx, pid string, cid int64, explicit, catalog string) (string, error) {
	cur := strings.ToUpper(strings.TrimSpace(explicit))
	catalog = strings.ToUpper(catalog)
	if cur != "" && catalog != "" && cur != catalog {
		return "", errors.New("invoice currency does not match catalog prices")
	}
	if cur == "" {
		cur = catalog
	}
	if cur == "" {
		c, e := dbCustomerGetByID(ctx.AppReadDB(), pid, cid)
		if e != nil {
			return "", e
		}
		if c != nil {
			cur = strings.ToUpper(c.Currency)
		}
	}
	if cur == "" {
		cur = strings.ToUpper(configString(ctx, "default_currency", "USD"))
	}
	if !supportedCurrency(cur) {
		return "", errors.New("unsupported ISO 4217 invoice currency")
	}
	return cur, nil
}
func validateCatalogCurrency(db *sql.DB, pid string, id int64, lines []any) error {
	inv, e := dbInvoiceGetByID(db, pid, id)
	if e != nil {
		return e
	}
	if inv == nil {
		return errors.New("invoice not found")
	}
	for _, r := range lines {
		if m, ok := r.(map[string]any); ok {
			if cur := strArg(m, "_catalog_currency"); cur != "" && cur != inv.Currency {
				return errors.New("catalog currency does not match invoice")
			}
		}
	}
	return nil
}
func cachedCatalogCall(api sdk.PlatformClient, cache map[string]map[string]any, tool string, id int64, out *map[string]any) error {
	key := fmt.Sprintf("%s:%d", tool, id)
	if v, ok := cache[key]; ok {
		*out = v
		return nil
	}
	if err := api.CallAppResult("catalog", tool, map[string]any{"id": id}, out); err != nil {
		return err
	}
	cache[key] = *out
	return nil
}

func sliceFromAny(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	b, _ := json.Marshal(v)
	var a []any
	_ = json.Unmarshal(b, &a)
	return a
}

func validateDateRange(since, until string) error {
	var first, last time.Time
	var err error
	if since != "" {
		first, err = parseBillingTime(since)
		if err != nil {
			return fmt.Errorf("invalid since: %w", err)
		}
	}
	if until != "" {
		last, err = parseBillingTime(until)
		if err != nil {
			return fmt.Errorf("invalid until: %w", err)
		}
	}
	if !first.IsZero() && !last.IsZero() && last.Before(first) {
		return errors.New("until must not precede since")
	}
	return nil
}
