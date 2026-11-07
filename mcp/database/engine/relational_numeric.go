package engine

import (
	"encoding/json"
	"math"
	"math/big"
	"regexp"
)

// Numeric expressions carry exact rationals until a typed computed field is
// published. Integer arithmetic is checked at each operator, not only at SUM.
// Binary floating point is introduced only by explicitly publishing a number.
const maxExprBits = 4096

var decimalLiteral = regexp.MustCompile(`^[+-]?[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]{1,3})?$`)

func exprRat(v any) (*big.Rat, bool) {
	switch x := v.(type) {
	case *big.Rat:
		return new(big.Rat).Set(x), true
	case string:
		if len(x) > 128 || !decimalLiteral.MatchString(x) {
			return nil, false
		}
		r, ok := new(big.Rat).SetString(x)
		return r, ok
	case json.Number:
		return exprRat(x.String())
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, false
		}
		return new(big.Rat).SetFloat64(x), true
	}
	return nil, false
}
func numericType(t string) bool {
	return t == "integer" || t == "number" || t == "rational" || t == "null"
}
func mergeExprTypes(a, b string) (string, error) {
	if a == "null" {
		return b, nil
	}
	if b == "null" || a == b {
		return a, nil
	}
	if numericType(a) && numericType(b) {
		if a == "number" || b == "number" {
			return "number", nil
		}
		return "rational", nil
	}
	return "", Invalid("expression branches have incompatible types %s and %s", a, b)
}
func numericExprType(view Collection, x *RelExpr, depth int) (string, error) {
	if depth > 8 {
		return "", Invalid("computed expressions nest at most 8 levels")
	}
	if x == nil || x.forms() == 0 {
		return "null", nil
	}
	if x.forms() != 1 {
		return "", Invalid("a computed expression has exactly one form")
	}
	if x.Field != "" {
		f, e := view.field(x.Field)
		return f.Type, e
	}
	if x.Const != nil {
		if r, ok := exprRat(x.Const); ok {
			if r.IsInt() {
				if !r.Num().IsInt64() {
					return "", fail("resource_limit", "integer constant overflow")
				}
				return "integer", nil
			}
			return "rational", nil
		}
		switch x.Const.(type) {
		case string:
			return "text", nil
		case bool:
			return "boolean", nil
		default:
			return "", Invalid("numeric expression needs a scalar constant")
		}
	}
	if x.Op == "" {
		if x.If != nil {
			a, e := numericExprType(view, x.Then, depth+1)
			if e != nil {
				return "", e
			}
			b, e := numericExprType(view, x.Else, depth+1)
			if e != nil {
				return "", e
			}
			return mergeExprTypes(a, b)
		}
		if x.Coalesce != nil {
			typ := "null"
			for i := range x.Coalesce {
				t, e := numericExprType(view, &x.Coalesce[i], depth+1)
				if e != nil {
					return "", e
				}
				typ, e = mergeExprTypes(typ, t)
				if e != nil {
					return "", e
				}
			}
			return typ, nil
		}
		// Legacy text/JSON transformations have no implicit numeric cast.
		return "text", nil
	}
	minArgs, maxArgs := 2, 2
	switch x.Op {
	case "add", "multiply", "min", "max", "and", "or":
		maxArgs = 16
	case "subtract", "safe_divide", "eq", "ne", "gt", "gte", "lt", "lte":
	case "if":
		minArgs, maxArgs = 3, 3
	case "round", "not":
		minArgs, maxArgs = 1, 1
	default:
		return "", Invalid("unknown expression operator %s", x.Op)
	}
	if len(x.Args) < minArgs || len(x.Args) > maxArgs {
		return "", Invalid("%s requires %d–%d args", x.Op, minArgs, maxArgs)
	}
	if x.Op != "round" && (x.Round != nil || x.RoundMode != "") {
		return "", Invalid("round and roundMode require op round")
	}
	if x.Op == "round" {
		if x.Round == nil || *x.Round < 0 || *x.Round > 12 {
			return "", Invalid("round requires round: 0–12")
		}
		switch x.RoundMode {
		case "", "half_away_from_zero", "half_even", "floor", "ceil", "trunc":
		default:
			return "", Invalid("unknown roundMode %s", x.RoundMode)
		}
	}
	types := make([]string, len(x.Args))
	for i := range x.Args {
		var e error
		types[i], e = numericExprType(view, &x.Args[i], depth+1)
		if e != nil {
			return "", e
		}
	}
	if x.Op == "if" {
		if types[0] != "boolean" && types[0] != "null" {
			return "", Invalid("if requires a boolean condition")
		}
		return mergeExprTypes(types[1], types[2])
	}
	if x.Op == "and" || x.Op == "or" || x.Op == "not" {
		for _, t := range types {
			if t != "boolean" && t != "null" {
				return "", Invalid("%s requires booleans", x.Op)
			}
		}
		return "boolean", nil
	}
	typ := "integer"
	for _, t := range types {
		if !numericType(t) {
			return "", Invalid("%s requires numeric args, got %s", x.Op, t)
		}
		var e error
		typ, e = mergeExprTypes(typ, t)
		if e != nil {
			return "", e
		}
	}
	switch x.Op {
	case "eq", "ne", "gt", "gte", "lt", "lte":
		return "boolean", nil
	case "safe_divide":
		return "rational", nil
	case "round":
		if *x.Round == 0 {
			return "integer", nil
		}
		return "rational", nil
	}
	return typ, nil
}

func (x *RelExpr) evalNumeric(view Collection, row Record) (any, error) {
	if x.Op == "if" {
		c, e := x.Args[0].evalChecked(view, row)
		if e != nil {
			return nil, e
		}
		index := 2
		if c == true {
			index = 1
		}
		v, e := x.Args[index].evalChecked(view, row)
		if e != nil || v == nil {
			return v, e
		}
		typ := x.resultType
		if typ == "" {
			typ, e = numericExprType(view, x, 0)
			if e != nil {
				return nil, e
			}
		}
		if numericType(typ) {
			r, ok := exprRat(v)
			if !ok {
				return nil, Invalid("if selected a non-numeric value")
			}
			return r, nil
		}
		return v, nil
	}
	if x.Op == "and" || x.Op == "or" || x.Op == "not" {
		unknown := false
		for i := range x.Args {
			v, e := x.Args[i].evalChecked(view, row)
			if e != nil {
				return nil, e
			}
			if v == nil {
				unknown = true
				continue
			}
			if x.Op == "not" {
				return !v.(bool), nil
			}
			if x.Op == "and" && v == false {
				return false, nil
			}
			if x.Op == "or" && v == true {
				return true, nil
			}
		}
		if unknown {
			return nil, nil
		}
		return x.Op == "and", nil
	}
	values := make([]*big.Rat, len(x.Args))
	null := false
	for i := range x.Args {
		v, e := x.Args[i].evalChecked(view, row)
		if e != nil {
			return nil, e
		}
		if v == nil {
			null = true
			continue
		}
		r, ok := exprRat(v)
		if !ok {
			return nil, Invalid("%s requires numeric values", x.Op)
		}
		values[i] = r
	}
	if null {
		return nil, nil
	}
	a := values[0]
	b := func() *big.Rat { return values[1] }
	switch x.Op {
	case "eq":
		return a.Cmp(b()) == 0, nil
	case "ne":
		return a.Cmp(b()) != 0, nil
	case "gt":
		return a.Cmp(b()) > 0, nil
	case "gte":
		return a.Cmp(b()) >= 0, nil
	case "lt":
		return a.Cmp(b()) < 0, nil
	case "lte":
		return a.Cmp(b()) <= 0, nil
	case "safe_divide":
		if b().Sign() == 0 {
			return nil, nil
		}
		a.Quo(a, b())
	case "subtract":
		a.Sub(a, b())
	case "round":
		a = roundRational(a, *x.Round, x.RoundMode)
	default:
		for _, v := range values[1:] {
			switch x.Op {
			case "add":
				a.Add(a, v)
			case "multiply":
				a.Mul(a, v)
			case "min":
				if a.Cmp(v) > 0 {
					a.Set(v)
				}
			case "max":
				if a.Cmp(v) < 0 {
					a.Set(v)
				}
			}
			if a.Num().BitLen() > maxExprBits || a.Denom().BitLen() > maxExprBits {
				return nil, fail("resource_limit", "numeric expression exceeds precision budget")
			}
		}
	}
	if a.Num().BitLen() > maxExprBits || a.Denom().BitLen() > maxExprBits {
		return nil, fail("resource_limit", "numeric expression exceeds precision budget")
	}
	typ := x.resultType
	if typ == "" {
		var e error
		typ, e = numericExprType(view, x, 0)
		if e != nil {
			return nil, e
		}
	}
	if typ == "integer" && (!a.IsInt() || !a.Num().IsInt64()) {
		return nil, fail("resource_limit", "%s integer overflow", x.Op)
	}
	return a, nil
}
func roundRational(r *big.Rat, digits int, mode string) *big.Rat {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	n := new(big.Int).Mul(r.Num(), scale)
	d := r.Denom()
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(n, d, rem)
	sign := int64(n.Sign())
	switch mode {
	case "floor":
		if sign < 0 && rem.Sign() != 0 {
			q.Sub(q, big.NewInt(1))
		}
	case "ceil":
		if sign > 0 && rem.Sign() != 0 {
			q.Add(q, big.NewInt(1))
		}
	case "trunc":
	default:
		cmp := new(big.Int).Lsh(new(big.Int).Abs(rem), 1).Cmp(d)
		if cmp > 0 || cmp == 0 && (mode != "half_even" || q.Bit(0) != 0) {
			q.Add(q, big.NewInt(sign))
		}
	}
	return new(big.Rat).SetFrac(q, scale)
}
