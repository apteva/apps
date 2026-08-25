package main

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var decimalRE = regexp.MustCompile(`^\+?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)

func parsePositiveDecimal(s string) (*big.Rat, string, error) {
	s = strings.TrimSpace(s)
	if !decimalRE.MatchString(s) {
		return nil, "", errors.New("rate must be a positive plain decimal string")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() <= 0 {
		return nil, "", errors.New("rate must be greater than zero")
	}
	s = strings.TrimPrefix(s, "+")
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	parts := strings.SplitN(s, ".", 2)
	parts[0] = strings.TrimLeft(parts[0], "0")
	if parts[0] == "" {
		parts[0] = "0"
	}
	s = strings.Join(parts, ".")
	return r, s, nil
}

func ratDecimal(r *big.Rat) string {
	if r == nil {
		return ""
	}
	s := r.FloatString(18)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "-0" || s == "" {
		return "0"
	}
	return s
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

func roundRatToInt64(r *big.Rat, mode string) (int64, bool, error) {
	if r == nil {
		return 0, false, errors.New("nil amount")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "half_even"
	}
	if mode != "half_even" && mode != "half_up" && mode != "down" && mode != "up" {
		return 0, false, fmt.Errorf("unsupported rounding mode %q", mode)
	}

	num := new(big.Int).Set(r.Num())
	negative := num.Sign() < 0
	num.Abs(num)
	den := new(big.Int).Set(r.Denom())
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(num, den, rem)
	rounded := rem.Sign() != 0
	increment := false
	switch mode {
	case "up":
		increment = rounded
	case "half_up", "half_even":
		cmp := new(big.Int).Lsh(new(big.Int).Set(rem), 1).Cmp(den)
		increment = cmp > 0 || cmp == 0 && (mode == "half_up" || q.Bit(0) == 1)
	}
	if increment {
		q.Add(q, big.NewInt(1))
	}
	if negative {
		q.Neg(q)
	}
	if !q.IsInt64() {
		return 0, rounded, errors.New("converted amount exceeds signed 64-bit minor-unit range")
	}
	return q.Int64(), rounded, nil
}

func convertMinor(amount int64, fromExponent, toExponent int, rate *big.Rat, rounding string) (int64, bool, error) {
	if fromExponent < 0 || toExponent < 0 {
		return 0, false, errors.New("currency has no ISO minor-unit exponent")
	}
	r := new(big.Rat).SetInt64(amount)
	r.Mul(r, rate)
	r.Mul(r, new(big.Rat).SetInt(pow10(toExponent)))
	r.Quo(r, new(big.Rat).SetInt(pow10(fromExponent)))
	return roundRatToInt64(r, rounding)
}
