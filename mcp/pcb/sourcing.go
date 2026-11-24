package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const sourcingSchema = "apteva-pcb-sourcing/v1"

// SourcingPart is the stock and price summary for one manufacturer part
// number, normalized from whatever shape the component-data provider returns.
type SourcingPart struct {
	MPN          string   `json:"mpn"`
	Designators  []string `json:"designators"`
	Quantity     int      `json:"quantity"`
	Found        bool     `json:"found"`
	Manufacturer string   `json:"manufacturer,omitempty"`
	Stock        int64    `json:"stock"`
	Sellers      int      `json:"sellers"`
	UnitPrice    float64  `json:"unit_price,omitempty"`
	Currency     string   `json:"currency,omitempty"`
	Status       string   `json:"status"` // in_stock | short | out_of_stock | not_found
}

type SourcingSummary struct {
	Schema         string         `json:"schema"`
	Provider       string         `json:"provider"`
	Country        string         `json:"country,omitempty"`
	Currency       string         `json:"currency,omitempty"`
	BuildQuantity  int            `json:"build_quantity"`
	Parts          []SourcingPart `json:"parts"`
	MissingMPN     []string       `json:"missing_mpn_designators"`
	OrderTotal     float64        `json:"order_total,omitempty"`
	ShortParts     int            `json:"short_parts"`
	UnpricedParts  int            `json:"unpriced_parts"`
	SourceRevision int64          `json:"source_revision_id,omitempty"`
}

// bomMPNs groups the revision's components by MPN.
func bomMPNs(def *Definition) (map[string][]string, []string) {
	byMPN := map[string][]string{}
	missing := []string{}
	for _, c := range def.Components {
		mpn := strings.TrimSpace(c.MPN)
		if mpn == "" {
			missing = append(missing, c.Designator)
			continue
		}
		byMPN[mpn] = append(byMPN[mpn], c.Designator)
	}
	sort.Slice(missing, func(i, j int) bool { return naturalLess(missing[i], missing[j]) })
	return byMPN, missing
}

// summarizeSourcing normalizes a provider response. It walks the JSON for
// part objects that carry an "mpn" field (the Nexar/Octopart supply schema:
// part.mpn, part.totalAvail, part.sellers[].offers[].inventoryLevel and
// prices[]{quantity, price, currency}) and tolerates flatter shapes with
// stock/price fields at the part level.
func summarizeSourcing(provider string, raw any, def *Definition, buildQuantity int, currency string) *SourcingSummary {
	if buildQuantity <= 0 {
		buildQuantity = 1
	}
	byMPN, missing := bomMPNs(def)
	found := map[string]*SourcingPart{}
	var walk func(v any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			if mpn, ok := node["mpn"].(string); ok {
				key := matchMPN(mpn, byMPN)
				if key != "" {
					part := found[key]
					if part == nil {
						part = &SourcingPart{MPN: key}
						found[key] = part
					}
					mergeSourcingPart(part, node, buildQuantity*len(byMPN[key]), currency)
					return
				}
			}
			keys := make([]string, 0, len(node))
			for k := range node {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(node[k])
			}
		case []any:
			for _, item := range node {
				walk(item)
			}
		}
	}
	walk(raw)
	summary := &SourcingSummary{Schema: sourcingSchema, Provider: provider, Currency: currency, BuildQuantity: buildQuantity, Parts: []SourcingPart{}, MissingMPN: missing}
	mpns := make([]string, 0, len(byMPN))
	for mpn := range byMPN {
		mpns = append(mpns, mpn)
	}
	sort.Strings(mpns)
	for _, mpn := range mpns {
		refs := append([]string(nil), byMPN[mpn]...)
		sort.Slice(refs, func(i, j int) bool { return naturalLess(refs[i], refs[j]) })
		need := buildQuantity * len(refs)
		part := SourcingPart{MPN: mpn, Status: "not_found"}
		if f := found[mpn]; f != nil {
			part = *f
		}
		part.Designators, part.Quantity = refs, need
		switch {
		case !part.Found:
			part.Status = "not_found"
		case part.Stock <= 0:
			part.Status = "out_of_stock"
		case part.Stock < int64(need):
			part.Status = "short"
		default:
			part.Status = "in_stock"
		}
		if part.Status != "in_stock" {
			summary.ShortParts++
		}
		if part.UnitPrice > 0 {
			summary.OrderTotal += part.UnitPrice * float64(need)
		} else {
			summary.UnpricedParts++
		}
		summary.Parts = append(summary.Parts, part)
	}
	summary.OrderTotal = math.Round(summary.OrderTotal*100) / 100
	return summary
}

func matchMPN(candidate string, byMPN map[string][]string) string {
	candidate = strings.TrimSpace(candidate)
	if _, ok := byMPN[candidate]; ok {
		return candidate
	}
	for mpn := range byMPN {
		if strings.EqualFold(mpn, candidate) {
			return mpn
		}
	}
	return ""
}

func mergeSourcingPart(part *SourcingPart, node map[string]any, need int, currency string) {
	part.Found = true
	if m, ok := node["manufacturer"].(map[string]any); ok {
		if name, ok := m["name"].(string); ok && part.Manufacturer == "" {
			part.Manufacturer = name
		}
	} else if name, ok := node["manufacturer"].(string); ok && part.Manufacturer == "" {
		part.Manufacturer = name
	}
	stock := int64(-1)
	for _, key := range []string{"totalAvail", "total_avail", "stock", "inventoryLevel", "inventory_level", "quantity_available", "availability"} {
		if v, ok := numberField(node[key]); ok {
			stock = int64(v)
			break
		}
	}
	offerStock := int64(0)
	best := 0.0
	bestCurrency := ""
	considerPrices := func(prices any) {
		list, _ := prices.([]any)
		for _, item := range list {
			p, _ := item.(map[string]any)
			price, ok := numberField(p["price"])
			if !ok || price <= 0 {
				continue
			}
			qty, _ := numberField(p["quantity"])
			cur, _ := p["currency"].(string)
			if currency != "" && cur != "" && !strings.EqualFold(cur, currency) {
				continue
			}
			// Use the price break that applies to the quantity needed.
			if qty > float64(need) && qty > 1 {
				continue
			}
			if best == 0 || price < best {
				best, bestCurrency = price, cur
			}
		}
	}
	if sellers, ok := node["sellers"].([]any); ok {
		for _, s := range sellers {
			seller, _ := s.(map[string]any)
			offers, _ := seller["offers"].([]any)
			if len(offers) > 0 {
				part.Sellers++
			}
			for _, o := range offers {
				offer, _ := o.(map[string]any)
				if v, ok := numberField(offer["inventoryLevel"]); ok && v > 0 {
					offerStock += int64(v)
				}
				considerPrices(offer["prices"])
			}
		}
	}
	considerPrices(node["prices"])
	if v, ok := numberField(node["unit_price"]); ok && v > 0 && best == 0 {
		best = v
	}
	if stock < 0 {
		stock = offerStock
	}
	if stock > part.Stock {
		part.Stock = stock
	}
	if best > 0 && (part.UnitPrice == 0 || best < part.UnitPrice) {
		part.UnitPrice = best
		if bestCurrency != "" {
			part.Currency = bestCurrency
		} else {
			part.Currency = currency
		}
	}
}

func numberField(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

// renderPricedBOM writes the BOM with stock and price columns from a
// sourcing summary.
func renderPricedBOM(summary *SourcingSummary) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"mpn", "designators", "quantity", "status", "stock", "sellers", "unit_price", "extended_price", "currency"})
	for _, p := range summary.Parts {
		unit, ext := "", ""
		if p.UnitPrice > 0 {
			unit = strconv.FormatFloat(p.UnitPrice, 'f', 4, 64)
			ext = strconv.FormatFloat(p.UnitPrice*float64(p.Quantity), 'f', 2, 64)
		}
		_ = w.Write([]string{p.MPN, strings.Join(p.Designators, " "), strconv.Itoa(p.Quantity), p.Status, strconv.FormatInt(p.Stock, 10), strconv.Itoa(p.Sellers), unit, ext, p.Currency})
	}
	for _, ref := range summary.MissingMPN {
		_ = w.Write([]string{"", ref, "1", "no_mpn", "", "", "", "", ""})
	}
	w.Flush()
	return buf.Bytes()
}

// sourcingChecks turns the latest sourcing summary into readiness checks.
func sourcingChecks(def *Definition, summary *SourcingSummary) []Check {
	byMPN, _ := bomMPNs(def)
	if len(byMPN) == 0 {
		return nil
	}
	if summary == nil {
		return []Check{{Code: "SOURCING_UNKNOWN", Severity: "warning", Message: "Stock has not been checked for this revision; run pcb_bom_source with a component-data provider bound"}}
	}
	checks := []Check{}
	covered := map[string]bool{}
	for _, part := range summary.Parts {
		covered[part.MPN] = true
		switch part.Status {
		case "not_found":
			checks = append(checks, Check{Code: "SOURCING_NOT_FOUND", Severity: "error", Message: fmt.Sprintf("%s was not found at any distributor", part.MPN), ObjectIDs: part.Designators})
		case "out_of_stock":
			checks = append(checks, Check{Code: "SOURCING_OUT_OF_STOCK", Severity: "error", Message: fmt.Sprintf("%s is out of stock", part.MPN), ObjectIDs: part.Designators})
		case "short":
			checks = append(checks, Check{Code: "SOURCING_SHORT", Severity: "error", Message: fmt.Sprintf("%s has %d in stock; the build needs %d", part.MPN, part.Stock, part.Quantity), ObjectIDs: part.Designators})
		}
	}
	for mpn, refs := range byMPN {
		if !covered[mpn] {
			checks = append(checks, Check{Code: "SOURCING_STALE", Severity: "warning", Message: fmt.Sprintf("%s was added after the last stock check", mpn), ObjectIDs: refs})
		}
	}
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Code != checks[j].Code {
			return checks[i].Code < checks[j].Code
		}
		return checks[i].Message < checks[j].Message
	})
	return checks
}
