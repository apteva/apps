package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type queryDB interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func decodeBody(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	d.UseNumber()
	var body map[string]any
	if err := d.Decode(&body); err != nil {
		return nil, errors.New("invalid JSON object")
	}
	if body == nil {
		return nil, errors.New("JSON object required")
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return nil, errors.New("exactly one JSON object required")
	}
	return body, nil
}

func copyArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	return out
}

func validateStrings(args map[string]any, keys ...string) error {
	for _, key := range keys {
		if v, ok := args[key]; ok {
			if _, ok := v.(string); !ok {
				return fmt.Errorf("%s must be a string", key)
			}
		}
	}
	return nil
}

func validateToolInput(args map[string]any, schema map[string]any) error {
	if required, ok := schema["required"].([]string); ok {
		for _, k := range required {
			if args[k] == nil {
				return fmt.Errorf("%s required", k)
			}
		}
	}
	props, _ := schema["properties"].(map[string]any)
	for k, v := range args {
		prop, known := props[k].(map[string]any)
		if !known {
			continue
		}
		switch prop["type"] {
		case "integer":
			if _, e := integer(args, k, 0); e != nil {
				return e
			}
		case "number":
			n := float64Arg(args, k, math.NaN())
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return fmt.Errorf("%s must be a finite number", k)
			}
		case "string":
			if _, ok := v.(string); !ok {
				return fmt.Errorf("%s must be a string", k)
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%s must be boolean", k)
			}
		case "object":
			if _, ok := v.(map[string]any); !ok {
				return fmt.Errorf("%s must be an object", k)
			}
		case "array":
			if _, ok := v.([]any); !ok {
				return fmt.Errorf("%s must be an array", k)
			}
		}
	}
	return nil
}

func integer(args map[string]any, key string, def int64) (int64, error) {
	v, ok := args[key]
	if !ok {
		return def, nil
	}
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case json.Number:
		i, e := n.Int64()
		if e == nil {
			return i, nil
		}
	case string:
		i, e := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if e == nil {
			return i, nil
		}
	case float64:
		if !math.IsNaN(n) && !math.IsInf(n, 0) && n == math.Trunc(n) && n >= -9007199254740991 && n <= 9007199254740991 {
			return int64(n), nil
		}
	}
	return 0, fmt.Errorf("%s must be an exact integer", key)
}

func validateIDs(args map[string]any, keys ...string) error {
	for _, key := range keys {
		if _, ok := args[key]; !ok {
			continue
		}
		n, e := integer(args, key, 0)
		if e != nil {
			return e
		}
		if n <= 0 {
			return fmt.Errorf("%s must be positive", key)
		}
		args[key] = n
	}
	return nil
}

func number(args map[string]any, key string, def float64) (float64, error) {
	if _, ok := args[key]; !ok {
		return def, nil
	}
	n := float64Arg(args, key, math.NaN())
	if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return 0, fmt.Errorf("%s must be finite and positive", key)
	}
	return n, nil
}

func metadata(v any) (string, error) {
	if v == nil {
		return "{}", nil
	}
	// Strings are deliberately not treated as already-encoded JSON on input.
	m, ok := v.(map[string]any)
	if !ok {
		return "", errors.New("metadata must be a JSON object")
	}
	b, e := json.Marshal(m)
	if e != nil {
		return "", fmt.Errorf("invalid metadata: %w", e)
	}
	return string(b), nil
}

func currencyCode(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 3 {
		return "", errors.New("currency must be a three-letter code")
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return "", errors.New("currency must be a three-letter code")
		}
	}
	return s, nil
}

func parseDate(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02", "2006-01-02 15:04:05"} {
		if t, e := time.Parse(layout, s); e == nil && t.Year() >= 1 && t.Year() <= 9999 {
			return t.UTC().Truncate(time.Second), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", s)
}
func dates(args map[string]any, keys ...string) error {
	for _, k := range keys {
		if v, ok := args[k]; ok {
			if v == nil {
				continue
			}
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("%s must be a timestamp", k)
			}
			if strings.TrimSpace(s) == "" {
				continue
			}
			t, e := parseDate(strings.TrimSpace(s))
			if e != nil {
				return fmt.Errorf("%s: %w", k, e)
			}
			args[k] = t.Format(time.RFC3339)
		}
	}
	return nil
}
func period(start, end string) error {
	a, e := parseDate(start)
	if e != nil {
		return e
	}
	b, e := parseDate(end)
	if e != nil {
		return e
	}
	if !b.After(a) {
		return errors.New("period_end must be after period_start")
	}
	return nil
}

func recurrence(interval string, count int64) error {
	if count <= 0 || count > 1200 {
		return errors.New("interval_count must be between 1 and 1200")
	}
	switch interval {
	case "day", "week", "month", "year":
		return nil
	}
	return errors.New("interval must be day, week, month, or year")
}

// Preserve the original billing day across short months (Jan 31 -> Feb 28 -> Mar 31).
func nextPeriod(start time.Time, interval string, count int64, anchor int) (time.Time, error) {
	if e := recurrence(interval, count); e != nil {
		return time.Time{}, e
	}
	var end time.Time
	switch interval {
	case "day":
		end = start.AddDate(0, 0, int(count))
	case "week":
		end = start.AddDate(0, 0, 7*int(count))
	default:
		months := int(count)
		if interval == "year" {
			months *= 12
		}
		first := time.Date(start.Year(), start.Month()+time.Month(months), 1, start.Hour(), start.Minute(), start.Second(), 0, time.UTC)
		last := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		day := anchor
		if day < 1 {
			day = start.Day()
		}
		if day > last {
			day = last
		}
		end = time.Date(first.Year(), first.Month(), day, start.Hour(), start.Minute(), start.Second(), 0, time.UTC)
	}
	if end.Year() > 9999 {
		return time.Time{}, errors.New("renewal exceeds supported date range")
	}
	return end, nil
}

// Quantities are decimal; round half up once per line, without floating-point cents.
func lineAmount(unit int64, quantity float64) (int64, error) {
	if unit < 0 || quantity <= 0 || math.IsNaN(quantity) || math.IsInf(quantity, 0) {
		return 0, errors.New("invalid line amount")
	}
	q, ok := new(big.Rat).SetString(strconv.FormatFloat(quantity, 'f', -1, 64))
	if !ok {
		return 0, errors.New("invalid quantity")
	}
	q.Mul(q, new(big.Rat).SetInt64(unit))
	q.Add(q, big.NewRat(1, 2))
	n := new(big.Int).Quo(q.Num(), q.Denom())
	if !n.IsInt64() {
		return 0, errors.New("line amount overflow")
	}
	return n.Int64(), nil
}
func addAmounts(values ...int64) (int64, error) {
	var sum int64
	for _, n := range values {
		if n < 0 || sum > math.MaxInt64-n {
			return 0, errors.New("invalid or overflowing amount")
		}
		sum += n
	}
	return sum, nil
}

func validateScalarFields(args map[string]any) error {
	return validateStrings(args, "customer_email", "customer_name", "kind", "status", "billing_provider", "external_id", "currency", "interval", "source", "source_ref", "actor", "note", "reason", "payment_status", "fulfillment_status")
}
