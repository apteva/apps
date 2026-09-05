package main

import (
	"errors"
	"math/big"
	"strconv"
)

// Decimal inputs and FX quotes use rational arithmetic; only the final target
// minor unit is rounded, half away from zero, once per source event.
func convertMinor(amount string, rate moneyRateUse, sourceDigits, targetDigits int, unit string) (int64, error) {
	value, ok := new(big.Rat).SetString(amount)
	if !ok {
		return 0, errors.New("invalid decimal amount")
	}
	quoted := rate.QuotedRate
	if quoted == 0 {
		quoted = rate.Rate
	}
	factor, ok := new(big.Rat).SetString(strconv.FormatFloat(quoted, 'g', -1, 64))
	if !ok || factor.Sign() <= 0 {
		return 0, errors.New("invalid conversion rate")
	}
	if rate.Inverse {
		factor.Inv(factor)
	}
	value.Mul(value, factor)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(targetDigits)), nil)
	value.Mul(value, new(big.Rat).SetInt(scale))
	if unit == "minor" {
		scale.Exp(big.NewInt(10), big.NewInt(int64(sourceDigits)), nil)
		value.Quo(value, new(big.Rat).SetInt(scale))
	}
	sign := value.Sign()
	num := new(big.Int).Abs(value.Num())
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(num, value.Denom(), remainder)
	if remainder.Lsh(remainder, 1).Cmp(value.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if sign < 0 {
		quotient.Neg(quotient)
	}
	if !quotient.IsInt64() {
		return 0, errors.New("converted amount overflows int64 minor units")
	}
	return quotient.Int64(), nil
}
