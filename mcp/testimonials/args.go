package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func strArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func boolArg(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, _ := strconv.ParseBool(x)
		return b
	default:
		return false
	}
}

func intArg(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return 0, false
	}
	return intFromAny(v)
}

func int64Arg(args map[string]any, key string) (int64, bool) {
	n, ok := intArg(args, key)
	return int64(n), ok
}

func intFromAny(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		if int64(int(x)) != x {
			return 0, false
		}
		return int(x), true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Trunc(x) != x || x > float64(math.MaxInt) || x < float64(math.MinInt) {
			return 0, false
		}
		return int(x), true
	case json.Number:
		n, err := x.Int64()
		if err != nil || int64(int(n)) != n {
			return 0, false
		}
		return int(n), true
	case string:
		n, err := strconv.Atoi(x)
		return n, err == nil
	default:
		return 0, false
	}
}

func stringSliceArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok || v == nil {
		return []string{}
	}
	switch x := v.(type) {
	case []string:
		return cleanStrings(x)
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if item != nil {
				out = append(out, fmt.Sprint(item))
			}
		}
		return cleanStrings(out)
	case string:
		if x == "" {
			return []string{}
		}
		return cleanStrings([]string{x})
	default:
		return []string{}
	}
}

func cleanStrings(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func mapArg(args map[string]any, key string) (map[string]any, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, false
	}
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	return nil, false
}

func testimonialFromArgs(args map[string]any) (Testimonial, error) {
	var t Testimonial
	_, err := applyTestimonialPatch(&t, args)
	return t, err
}
