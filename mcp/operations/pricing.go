package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ─── pricing engine ─────────────────────────────────────────────────
//
// The only place prices are computed. Agents choose rate items and
// quantities (or give intake answers); the engine returns lines,
// totals, margin, duration, the resources the work needs, and policy
// flags that send the work to the owner.

type ItemRequest struct {
	Code string   `json:"code"`
	Qty  *float64 `json:"qty,omitempty"`
}

type PriceContext struct {
	BrandID     int64 // prices, modifiers and policies of this brand (0 = shared only)
	WorkTypeKey string
	Priority    string
	PartySize   int
	Answers     map[string]any
	StartsAt    *time.Time
	Location    *time.Location
	Flags       []string
	DiscountPct float64
	DefaultDur  int
	TypeReqs    []Requirement
	AutoMax     int64 // 0 = use policy
	// The contractor doing the work: its own rate items replace the
	// shared ones. SettlementMode picks the margin check (resale) or the
	// commission check (commission).
	ResourceID     int64
	SettlementMode string
	Terms          Settlement
}

type PricedLine struct {
	RateItemID     int64   `json:"rate_item_id,omitempty"`
	Code           string  `json:"code"`
	Kind           string  `json:"kind"` // item | modifier | custom
	Description    string  `json:"description"`
	Qty            float64 `json:"qty"`
	Unit           string  `json:"unit,omitempty"`
	UnitPriceCents int64   `json:"unit_price_cents"`
	TotalCents     int64   `json:"total_cents"`
	CostCents      int64   `json:"cost_cents"`
}

type PriceResult struct {
	Lines         []PricedLine  `json:"lines"`
	SubtotalCents int64         `json:"subtotal_cents"`
	TotalCents    int64         `json:"total_cents"`
	CostCents     int64         `json:"cost_cents"`
	MarginPct     *float64      `json:"margin_pct"`
	DurationMin   int           `json:"duration_min"`
	Requirements  []Requirement `json:"requirements"`
	Materials     []Material    `json:"materials,omitempty"`
	Flags         []string      `json:"flags"`
	NeedsOwner    bool          `json:"needs_owner"`
	// resale: margin_check "margin" against min_margin_pct. commission:
	// "commission" against min_commission_cents, with the expected
	// commission on this total.
	SettlementMode          string `json:"settlement_mode"`
	MarginCheck             string `json:"margin_check"`
	ExpectedCommissionCents *int64 `json:"expected_commission_cents,omitempty"`
}

func priceItems(db *sql.DB, pid string, items []ItemRequest, pc PriceContext) (*PriceResult, error) {
	res := &PriceResult{Lines: []PricedLine{}, Flags: []string{}}
	reqs := append([]Requirement{}, pc.TypeReqs...)
	duration := 0
	for _, it := range items {
		ri, err := getRateItemFor(db, pid, it.Code, pc.BrandID, pc.ResourceID)
		if err != nil {
			return nil, notFound(err, "rate item "+it.Code)
		}
		if !ri.Active {
			return nil, fmt.Errorf("rate item %s is inactive", it.Code)
		}
		qty, err := itemQty(ri, it, pc.Answers)
		if err != nil {
			return nil, err
		}
		line, err := priceLine(ri, qty, pc.Answers)
		if err != nil {
			return nil, err
		}
		if ri.Pricing.Mode == "quote_only" {
			res.Flags = append(res.Flags, "quote_only:"+ri.Code)
		}
		res.Lines = append(res.Lines, line)
		res.SubtotalCents += line.TotalCents
		res.CostCents += line.CostCents
		duration += ri.Duration.FixedMin + int(math.Ceil(ri.Duration.PerUnitMin*qty))
		reqs = mergeRequirements(reqs, ri.Requirements)
		for _, m := range ri.Materials {
			res.Materials = append(res.Materials, Material{SKU: m.SKU, QtyPerUnit: round2(m.QtyPerUnit * qty)})
		}
	}
	if duration == 0 {
		duration = pc.DefaultDur
	}
	res.DurationMin = duration
	res.Requirements = reqs

	mods, err := effectiveModifiers(db, pid, pc.BrandID)
	if err != nil {
		return nil, err
	}
	total := res.SubtotalCents
	for _, m := range mods {
		if !m.Active || !modifierApplies(m, pc) {
			continue
		}
		amt := m.Amount.Cents
		if m.Amount.Pct != 0 {
			amt = int64(math.Round(float64(res.SubtotalCents) * m.Amount.Pct / 100))
		}
		if m.Kind == "discount" {
			amt = -abs64(amt)
		}
		res.Lines = append(res.Lines, PricedLine{Code: m.Key, Kind: "modifier", Description: m.Name, Qty: 1,
			UnitPriceCents: amt, TotalCents: amt})
		total += amt
	}
	if pc.DiscountPct > 0 {
		amt := -int64(math.Round(float64(res.SubtotalCents) * pc.DiscountPct / 100))
		res.Lines = append(res.Lines, PricedLine{Code: "discount", Kind: "modifier",
			Description: fmt.Sprintf("Discount %.4g%%", pc.DiscountPct), Qty: 1, UnitPriceCents: amt, TotalCents: amt})
		total += amt
		if maxPct := policyInt(db, pid, pc.BrandID, "max_discount_pct", 0); float64(maxPct) < pc.DiscountPct {
			res.Flags = append(res.Flags, fmt.Sprintf("discount_above_max:%d", maxPct))
		}
	}
	if total < 0 {
		total = 0
	}
	res.TotalCents = total
	res.SettlementMode = firstNonEmpty(pc.SettlementMode, modeResale)
	if res.SettlementMode == modeCommission {
		// The contractor bills the client; what the brand keeps is the
		// commission, so margin rules don't apply.
		res.MarginCheck = modeCommission
		exp := commissionAmount(pc.Terms.normalized(), total)
		res.ExpectedCommissionCents = &exp
		if minC := policyInt(db, pid, pc.BrandID, "min_commission_cents", 0); minC > 0 && exp < minC {
			res.Flags = append(res.Flags, fmt.Sprintf("below_min_commission:%d", minC))
		}
	} else {
		res.MarginCheck = "margin"
		if total > 0 {
			m := round2(float64(total-res.CostCents) / float64(total) * 100)
			res.MarginPct = &m
			if minMargin := policyInt(db, pid, pc.BrandID, "min_margin_pct", -1); minMargin >= 0 && m < float64(minMargin) {
				res.Flags = append(res.Flags, fmt.Sprintf("below_min_margin:%d", minMargin))
			}
		}
	}
	autoMax := pc.AutoMax
	if autoMax == 0 {
		autoMax = policyInt(db, pid, pc.BrandID, "auto_quote_max_cents", 0)
	}
	if autoMax > 0 && total > autoMax {
		res.Flags = append(res.Flags, fmt.Sprintf("above_auto_approve:%d", autoMax))
	}
	res.NeedsOwner = len(res.Flags) > 0
	return res, nil
}

func itemQty(ri *RateItem, it ItemRequest, answers map[string]any) (float64, error) {
	if it.Qty != nil {
		if *it.Qty <= 0 {
			return 0, fmt.Errorf("rate item %s: qty must be > 0", ri.Code)
		}
		return *it.Qty, nil
	}
	if k := ri.Pricing.QtyFrom; k != "" {
		v, ok := answers[k]
		if !ok {
			return 0, fmt.Errorf("rate item %s needs answer %q (or an explicit qty)", ri.Code, k)
		}
		f := floatArg(map[string]any{"v": v}, "v", -1)
		if f <= 0 {
			return 0, fmt.Errorf("rate item %s: answer %q must be a positive number", ri.Code, k)
		}
		return f, nil
	}
	return 1, nil
}

func priceLine(ri *RateItem, qty float64, answers map[string]any) (PricedLine, error) {
	p := ri.Pricing
	line := PricedLine{RateItemID: ri.ID, Code: ri.Code, Kind: "item", Description: ri.Name, Qty: qty, Unit: p.Unit}
	var total int64
	switch p.Mode {
	case "flat", "per_unit", "hourly":
		line.UnitPriceCents = p.UnitCents
		total = int64(math.Round(float64(p.UnitCents) * qty))
	case "tiered":
		unit := p.Tiers[len(p.Tiers)-1].UnitCents
		for _, t := range p.Tiers {
			if t.UpTo == 0 || qty <= t.UpTo {
				unit = t.UnitCents
				break
			}
		}
		line.UnitPriceCents = unit
		total = int64(math.Round(float64(unit) * qty))
	case "formula":
		f, err := parseFormula(p.Formula)
		if err != nil {
			return line, fmt.Errorf("rate item %s: %w", ri.Code, err)
		}
		vars := map[string]float64{"qty": qty}
		for k, v := range answers {
			if n := floatArg(map[string]any{"v": v}, "v", math.NaN()); !math.IsNaN(n) {
				vars[k] = n
			} else if b, ok := v.(bool); ok {
				vars[k] = float64(boolInt(b))
			}
		}
		v, err := f(vars)
		if err != nil {
			return line, fmt.Errorf("rate item %s: %w", ri.Code, err)
		}
		total = int64(math.Round(v))
		line.Qty = 1
		line.UnitPriceCents = total
	case "quote_only":
		total = 0
	}
	if p.MinChargeCents > 0 && total < p.MinChargeCents && p.Mode != "quote_only" {
		total = p.MinChargeCents
		if line.Qty > 0 {
			line.UnitPriceCents = int64(math.Round(float64(total) / line.Qty))
		}
	}
	line.TotalCents = total
	line.CostCents = int64(math.Round(float64(ri.CostCents) * qty))
	return line, nil
}

func modifierApplies(m *Modifier, pc PriceContext) bool {
	w := m.When
	if len(w.Priorities) > 0 && !contains(w.Priorities, pc.Priority) {
		return false
	}
	if len(w.WorkTypes) > 0 && !contains(w.WorkTypes, pc.WorkTypeKey) {
		return false
	}
	if w.PartySizeGTE > 0 && pc.PartySize < w.PartySizeGTE {
		return false
	}
	for _, f := range w.Flags {
		if !contains(pc.Flags, f) {
			return false
		}
	}
	if len(w.Weekdays) > 0 || w.HourFrom != nil {
		if pc.StartsAt == nil {
			return false
		}
		loc := pc.Location
		if loc == nil {
			loc = time.UTC
		}
		t := pc.StartsAt.In(loc)
		if len(w.Weekdays) > 0 {
			wd := int(t.Weekday())
			if wd == 0 {
				wd = 7
			}
			ok := false
			for _, d := range w.Weekdays {
				if d == wd {
					ok = true
				}
			}
			if !ok {
				return false
			}
		}
		if w.HourFrom != nil && w.HourTo != nil {
			h, from, to := t.Hour(), *w.HourFrom, *w.HourTo
			in := h >= from && h < to
			if to <= from {
				in = h >= from || h < to
			}
			if !in {
				return false
			}
		}
	}
	return true
}

// mergeRequirements adds item requirements to the type's. Requirements
// for the same kind of resource in the same zone merge: the resource
// must hold every qualification (a cut + colour needs one stylist who
// does both) and the larger count wins.
func mergeRequirements(base, add []Requirement) []Requirement {
	key := func(r Requirement) string { return r.Kind + "|" + r.Zone + "|" + r.CapacityFrom }
	idx := map[string]int{}
	for i, r := range base {
		idx[key(r)] = i
	}
	for _, r := range add {
		if r.Count <= 0 {
			r.Count = 1
		}
		if i, ok := idx[key(r)]; ok {
			if base[i].Count < r.Count {
				base[i].Count = r.Count
			}
			for _, q := range r.Quals {
				if !contains(base[i].Quals, q) {
					base[i].Quals = append(append([]string{}, base[i].Quals...), q)
				}
			}
			sort.Strings(base[i].Quals)
			if base[i].Role == "" {
				base[i].Role = r.Role
			}
			continue
		}
		idx[key(r)] = len(base)
		base = append(base, r)
	}
	for i := range base {
		if base[i].Count <= 0 {
			base[i].Count = 1
		}
	}
	return base
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// ─── formula evaluator ──────────────────────────────────────────────
//
// Small, safe arithmetic: numbers, identifiers (intake answers, qty),
// + - * / and parentheses, and min(), max(), ceil(), floor(), round().
// Unknown identifiers are an error so a missing answer is never priced
// as zero.

type formulaFn func(vars map[string]float64) (float64, error)

func parseFormula(src string) (formulaFn, error) {
	if strings.TrimSpace(src) == "" {
		return nil, errors.New("empty formula")
	}
	p := &fparser{src: src}
	p.next()
	f, err := p.expr()
	if err != nil {
		return nil, err
	}
	if p.tok != "" {
		return nil, fmt.Errorf("unexpected %q", p.tok)
	}
	return f, nil
}

type fparser struct {
	src string
	pos int
	tok string
}

func (p *fparser) next() {
	for p.pos < len(p.src) && unicode.IsSpace(rune(p.src[p.pos])) {
		p.pos++
	}
	if p.pos >= len(p.src) {
		p.tok = ""
		return
	}
	c := rune(p.src[p.pos])
	start := p.pos
	switch {
	case unicode.IsDigit(c) || c == '.':
		for p.pos < len(p.src) && (unicode.IsDigit(rune(p.src[p.pos])) || p.src[p.pos] == '.') {
			p.pos++
		}
	case unicode.IsLetter(c) || c == '_':
		for p.pos < len(p.src) && (unicode.IsLetter(rune(p.src[p.pos])) || unicode.IsDigit(rune(p.src[p.pos])) || p.src[p.pos] == '_') {
			p.pos++
		}
	default:
		p.pos++
	}
	p.tok = p.src[start:p.pos]
}

func (p *fparser) expr() (formulaFn, error) {
	left, err := p.term()
	if err != nil {
		return nil, err
	}
	for p.tok == "+" || p.tok == "-" {
		op := p.tok
		p.next()
		right, err := p.term()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		if op == "+" {
			left = func(v map[string]float64) (float64, error) {
				return bin(l, r, v, func(a, b float64) float64 { return a + b })
			}
		} else {
			left = func(v map[string]float64) (float64, error) {
				return bin(l, r, v, func(a, b float64) float64 { return a - b })
			}
		}
	}
	return left, nil
}

func (p *fparser) term() (formulaFn, error) {
	left, err := p.factor()
	if err != nil {
		return nil, err
	}
	for p.tok == "*" || p.tok == "/" {
		op := p.tok
		p.next()
		right, err := p.factor()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		if op == "*" {
			left = func(v map[string]float64) (float64, error) {
				return bin(l, r, v, func(a, b float64) float64 { return a * b })
			}
		} else {
			left = func(v map[string]float64) (float64, error) {
				b, err := r(v)
				if err != nil {
					return 0, err
				}
				if b == 0 {
					return 0, errors.New("division by zero")
				}
				a, err := l(v)
				return a / b, err
			}
		}
	}
	return left, nil
}

func (p *fparser) factor() (formulaFn, error) {
	tok := p.tok
	switch {
	case tok == "":
		return nil, errors.New("unexpected end of formula")
	case tok == "-":
		p.next()
		f, err := p.factor()
		if err != nil {
			return nil, err
		}
		return func(v map[string]float64) (float64, error) { x, err := f(v); return -x, err }, nil
	case tok == "(":
		p.next()
		f, err := p.expr()
		if err != nil {
			return nil, err
		}
		if p.tok != ")" {
			return nil, errors.New("missing )")
		}
		p.next()
		return f, nil
	case unicode.IsDigit(rune(tok[0])) || tok[0] == '.':
		n, err := strconv.ParseFloat(tok, 64)
		if err != nil {
			return nil, fmt.Errorf("bad number %q", tok)
		}
		p.next()
		return func(map[string]float64) (float64, error) { return n, nil }, nil
	case unicode.IsLetter(rune(tok[0])) || tok[0] == '_':
		p.next()
		if p.tok == "(" {
			return p.call(tok)
		}
		name := tok
		return func(v map[string]float64) (float64, error) {
			x, ok := v[name]
			if !ok {
				return 0, fmt.Errorf("missing answer %q", name)
			}
			return x, nil
		}, nil
	}
	return nil, fmt.Errorf("unexpected %q", tok)
}

func (p *fparser) call(name string) (formulaFn, error) {
	p.next() // (
	var args []formulaFn
	for p.tok != ")" {
		a, err := p.expr()
		if err != nil {
			return nil, err
		}
		args = append(args, a)
		if p.tok == "," {
			p.next()
			continue
		}
		if p.tok != ")" {
			return nil, fmt.Errorf("expected , or ) in %s()", name)
		}
	}
	p.next()
	eval := func(v map[string]float64) ([]float64, error) {
		out := make([]float64, len(args))
		for i, a := range args {
			x, err := a(v)
			if err != nil {
				return nil, err
			}
			out[i] = x
		}
		return out, nil
	}
	one := func(fn func(float64) float64) (formulaFn, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s() takes 1 argument", name)
		}
		return func(v map[string]float64) (float64, error) {
			xs, err := eval(v)
			if err != nil {
				return 0, err
			}
			return fn(xs[0]), nil
		}, nil
	}
	switch name {
	case "ceil":
		return one(math.Ceil)
	case "floor":
		return one(math.Floor)
	case "round":
		return one(math.Round)
	case "min", "max":
		if len(args) < 2 {
			return nil, fmt.Errorf("%s() takes at least 2 arguments", name)
		}
		isMin := name == "min"
		return func(v map[string]float64) (float64, error) {
			xs, err := eval(v)
			if err != nil {
				return 0, err
			}
			best := xs[0]
			for _, x := range xs[1:] {
				if (isMin && x < best) || (!isMin && x > best) {
					best = x
				}
			}
			return best, nil
		}, nil
	}
	return nil, fmt.Errorf("unknown function %s()", name)
}

func bin(l, r formulaFn, v map[string]float64, op func(a, b float64) float64) (float64, error) {
	a, err := l(v)
	if err != nil {
		return 0, err
	}
	b, err := r(v)
	if err != nil {
		return 0, err
	}
	return op(a, b), nil
}
