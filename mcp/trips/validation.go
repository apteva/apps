package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// HTTP and MCP share the same mutation validation; reject inputs rather than
// silently truncating IDs or monetary values to a different record/amount.
func validatePlanningArgs(args map[string]any) error {
	for _, field := range []string{"id", "trip_id", "destination_id", "from_destination_id", "to_destination_id", "cost_estimated", "cost_actual", "total_budget", "amount", "daily_search_budget_cents"} {
		value, present := args[field]
		if !present {
			continue
		}
		if value == nil {
			if field == "id" || field == "trip_id" || field == "amount" {
				return fmt.Errorf("%s required", field)
			}
			continue
		}
		n, err := favoriteInteger(value, field)
		if err != nil {
			return err
		}
		if (field == "id" || field == "trip_id") && n == 0 {
			return fmt.Errorf("%s must be positive", field)
		}
	}
	for _, field := range []string{"sync_calendar", "archived", "include_done", "include_archived"} {
		if value, present := args[field]; present {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be a boolean", field)
			}
		}
	}
	for _, field := range []string{"lat", "lng"} {
		value, present := args[field]
		if !present || value == nil {
			continue
		}
		var n float64
		switch v := value.(type) {
		case float64:
			n = v
		case int:
			n = float64(v)
		case int64:
			n = float64(v)
		default:
			return fmt.Errorf("%s must be a number or null", field)
		}
		limit := 90.0
		if field == "lng" {
			limit = 180
		}
		if math.IsNaN(n) || math.IsInf(n, 0) || n < -limit || n > limit {
			return fmt.Errorf("%s is outside its geographic range", field)
		}
		args[field] = n
	}
	return nil
}

func decodeRequestBody(w http.ResponseWriter, r *http.Request, args *map[string]any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(args); err != nil || *args == nil {
		http.Error(w, "JSON object required", http.StatusBadRequest)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		http.Error(w, "exactly one JSON object required", http.StatusBadRequest)
		return false
	}
	return true
}

func currencyMinorDigits(currency string) int {
	switch strings.ToUpper(currency) {
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF", "UGX", "UYI", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	case "CLF", "UYW":
		return 4
	default:
		return 2
	}
}

// Decimal provider amounts use the currency's minor units. Never overflow or
// quietly trim significant decimals. Existing callers without a currency use EUR.
func parseMoneyDecimal(s string, currencies ...string) (int64, error) {
	digits := 2
	if len(currencies) > 0 {
		digits = currencyMinorDigits(currencies[0])
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty amount")
	}
	negative := strings.HasPrefix(s, "-")
	if negative {
		s = s[1:]
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, errors.New("invalid decimal amount")
	}
	whole := parts[0]
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	for _, part := range []string{whole, fraction} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, errors.New("invalid decimal amount")
			}
		}
	}
	if len(fraction) > digits {
		if strings.Trim(fraction[digits:], "0") != "" {
			return 0, errors.New("too many decimal places for currency")
		}
		fraction = fraction[:digits]
	}
	fraction += strings.Repeat("0", digits-len(fraction))
	n, err := strconv.ParseInt(whole+fraction, 10, 64)
	if err != nil || n > 9007199254740991 {
		return 0, errors.New("amount exceeds supported range")
	}
	if negative {
		n = -n
	}
	return n, nil
}
