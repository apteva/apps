package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The LCSC catalog is JLCPCB's assembly parts library. PCB Studio reads it
// through a keyless JSON mirror (jlcsearch, by tscircuit) that exposes LCSC
// number, MPN, package, stock, price breaks and the basic/extended flag. The
// base URL is the lcsc_catalog_url config value so a self-hosted mirror can
// replace it; "off" disables the lookup.
const defaultLCSCCatalogURL = "https://jlcsearch.tscircuit.com"

// LCSCPart is one catalog row.
type LCSCPart struct {
	LCSC        string `json:"lcsc"`
	MPN         string `json:"mpn"`
	Package     string `json:"package"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	Stock       int64  `json:"stock"`
	Basic       bool   `json:"basic"`
	Preferred   bool   `json:"preferred"`
	// StockUnknown marks rows where the catalog mirror has no stock figure;
	// confirm availability on jlcpcb.com before ordering.
	StockUnknown bool         `json:"stock_unknown,omitempty"`
	PriceBreaks  []PriceBreak `json:"price_breaks,omitempty"`
}

// PriceBreak is one quantity tier: Min..Max (0 = no upper bound) at UnitUSD.
type PriceBreak struct {
	Min     int     `json:"min"`
	Max     int     `json:"max,omitempty"`
	UnitUSD float64 `json:"unit_usd"`
}

type lcscCatalog struct {
	base   string
	client *http.Client
}

func newLCSCCatalog(base string) (*lcscCatalog, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultLCSCCatalogURL
	}
	if strings.EqualFold(base, "off") {
		return nil, errors.New("LCSC catalog lookups are disabled (lcsc_catalog_url = off)")
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("lcsc_catalog_url %q is not an http(s) URL", base)
	}
	return &lcscCatalog{base: base, client: &http.Client{Timeout: 15 * time.Second}}, nil
}

// Search returns catalog parts matching a free-text query (MPN, value,
// package), in-stock parts first, basic parts before extended.
func (c *lcscCatalog) Search(query, pkg string, limit int) ([]LCSCPart, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("query is required")
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	values := url.Values{"search": {query}, "limit": {strconv.Itoa(limit)}}
	if pkg = strings.TrimSpace(pkg); pkg != "" {
		values.Set("package", pkg)
	}
	resp, err := c.client.Get(c.base + "/components/list.json?" + values.Encode())
	if err != nil {
		return nil, fmt.Errorf("LCSC catalog request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LCSC catalog returned HTTP %d", resp.StatusCode)
	}
	parts, err := parseLCSCList(body)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(parts, func(i, j int) bool {
		a, b := parts[i], parts[j]
		if (a.Stock > 0) != (b.Stock > 0) {
			return a.Stock > 0
		}
		if a.Basic != b.Basic {
			return a.Basic
		}
		return a.Stock > b.Stock
	})
	return parts, nil
}

func parseLCSCList(body []byte) ([]LCSCPart, error) {
	var raw struct {
		Components []struct {
			LCSC        json.Number `json:"lcsc"`
			MFR         string      `json:"mfr"`
			Package     string      `json:"package"`
			Description string      `json:"description"`
			Category    string      `json:"subcategory"`
			Stock       int64       `json:"stock"`
			Price       any         `json:"price"`
			IsBasic     bool        `json:"is_basic"`
			IsPreferred bool        `json:"is_preferred"`
		} `json:"components"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("LCSC catalog response could not be parsed: %w", err)
	}
	out := make([]LCSCPart, 0, len(raw.Components))
	for _, c := range raw.Components {
		number := strings.TrimPrefix(strings.ToUpper(c.LCSC.String()), "C")
		if number == "" {
			continue
		}
		out = append(out, LCSCPart{LCSC: "C" + number, MPN: c.MFR, Package: c.Package, Description: c.Description, Category: c.Category, Stock: c.Stock, Basic: c.IsBasic, Preferred: c.IsPreferred, PriceBreaks: parsePriceBreaks(c.Price)})
	}
	return out, nil
}

// parsePriceBreaks reads "1-49:0.1089,50-149:0.0877,6000-:0.0546" or a JSON
// array of {qFrom,qTo,price}.
func parsePriceBreaks(value any) []PriceBreak {
	out := []PriceBreak{}
	switch v := value.(type) {
	case string:
		for _, tier := range strings.Split(v, ",") {
			rng, price, ok := strings.Cut(strings.TrimSpace(tier), ":")
			if !ok {
				continue
			}
			lo, hi, _ := strings.Cut(rng, "-")
			min, err1 := strconv.Atoi(strings.TrimSpace(lo))
			unit, err2 := strconv.ParseFloat(strings.TrimSpace(price), 64)
			if err1 != nil || err2 != nil {
				continue
			}
			max, _ := strconv.Atoi(strings.TrimSpace(hi))
			out = append(out, PriceBreak{Min: min, Max: max, UnitUSD: unit})
		}
	case []any:
		for _, item := range v {
			m, _ := item.(map[string]any)
			min, _ := numberField(m["qFrom"])
			max, _ := numberField(m["qTo"])
			unit, ok := numberField(m["price"])
			if ok {
				out = append(out, PriceBreak{Min: int(min), Max: int(max), UnitUSD: unit})
			}
		}
	}
	return out
}

func unitPriceAt(breaks []PriceBreak, qty int) float64 {
	price := 0.0
	for _, b := range breaks {
		if qty >= b.Min && (b.Max == 0 || qty <= b.Max) {
			return b.UnitUSD
		}
		if qty >= b.Min {
			price = b.UnitUSD
		}
	}
	if price == 0 && len(breaks) > 0 {
		price = breaks[0].UnitUSD
	}
	return price
}

// LCSCMatch is the proposed catalog part for one BOM line.
type LCSCMatch struct {
	ComponentIDs []string  `json:"component_ids"`
	Designators  []string  `json:"designators"`
	Query        string    `json:"query"`
	Current      string    `json:"current_lcsc,omitempty"`
	Match        *LCSCPart `json:"match,omitempty"`
	Exact        bool      `json:"exact_mpn"`
	// Basis is how the match was found: mpn (exact), compatible_mpn (another
	// orderable variant the frozen footprint lists; applying it also switches
	// the component MPN), parametric (same value and size, chip passives) or
	// keyword (closest text hit; check it).
	Basis   string  `json:"basis,omitempty"`
	Status  string  `json:"status"` // matched | already_set | no_match | lookup_failed
	Error   string  `json:"error,omitempty"`
	UnitUSD float64 `json:"unit_usd,omitempty"`
}

// lcscMatchBOM looks up every placed BOM line by MPN (or value + package when
// there is no MPN). Only exact MPN matches are proposed for assignment;
// value-based hits are returned as candidates the caller must confirm.
func lcscMatchBOM(catalog *lcscCatalog, def *Definition, buildQuantity int, overwrite bool) []LCSCMatch {
	if catalog == nil {
		return nil
	}
	type line struct {
		query, mpn, current string
		ids, refs           []string
		component           Component
	}
	placed := map[string]bool{}
	for _, row := range placementRows(def) {
		placed[row.Designator] = true
	}
	lines := map[string]*line{}
	order := []string{}
	for _, c := range def.Components {
		if !placed[c.Designator] {
			continue
		}
		mpn := strings.TrimSpace(c.MPN)
		query := mpn
		if query == "" {
			query = strings.TrimSpace(c.Value + " " + c.Footprint)
		}
		if query == "" {
			continue
		}
		key := strings.ToUpper(query)
		if lines[key] == nil {
			lines[key] = &line{query: query, mpn: mpn, current: c.LCSC, component: c}
			order = append(order, key)
		}
		lines[key].ids = append(lines[key].ids, c.ID)
		lines[key].refs = append(lines[key].refs, c.Designator)
	}
	sort.Strings(order)
	out := []LCSCMatch{}
	for _, key := range order {
		l := lines[key]
		m := LCSCMatch{ComponentIDs: l.ids, Designators: l.refs, Query: l.query, Current: l.current}
		if l.current != "" && !overwrite {
			m.Status = "already_set"
			out = append(out, m)
			continue
		}
		parts, err := catalog.Search(l.query, "", 10)
		if err != nil {
			m.Status, m.Error = "lookup_failed", err.Error()
			out = append(out, m)
			continue
		}
		var best *LCSCPart
		for i := range parts {
			if l.mpn != "" && strings.EqualFold(parts[i].MPN, l.mpn) {
				best, m.Exact = &parts[i], true
				break
			}
		}
		if best == nil && l.component.FootprintSpec != nil {
			// The exact MPN is not stocked: try the other orderable variants
			// the frozen footprint accepts (e.g. SHTC3 -DIS vs -TR reels).
			for _, alt := range l.component.FootprintSpec.CompatibleMPNs {
				if strings.EqualFold(alt, l.mpn) {
					continue
				}
				altParts, err := catalog.Search(alt, "", 10)
				if err != nil {
					continue
				}
				for i := range altParts {
					if strings.EqualFold(altParts[i].MPN, alt) && (best == nil || altParts[i].Stock > best.Stock) {
						best, m.Basis = &altParts[i], "compatible_mpn"
					}
				}
			}
		}
		if best == nil {
			// Chip passives: an equivalent stocked part of the same value and
			// size is the usual JLCPCB substitution (basic parts first).
			if kind, value, pkg, ok := passiveQuery(l.component); ok {
				if passives, err := catalog.SearchPassive(kind, value, pkg); err == nil && len(passives) > 0 {
					best, m.Basis = &passives[0], "parametric"
				}
			}
		}
		if best == nil && len(parts) > 0 {
			best, m.Basis = &parts[0], "keyword"
		}
		if m.Exact {
			m.Basis = "mpn"
		}
		if best == nil {
			m.Status = "no_match"
		} else {
			m.Match, m.Status = best, "matched"
			m.UnitUSD = unitPriceAt(best.PriceBreaks, buildQuantity*len(l.ids))
		}
		out = append(out, m)
	}
	return out
}

var (
	passivePackagePattern   = regexp.MustCompile(`(?i)(?:^|[^0-9])(0201|0402|0603|0805|1206|1210)(?:[^0-9]|$)`)
	resistanceValuePattern  = regexp.MustCompile(`(?i)^\s*([0-9]*\.?[0-9]+)\s*([rkm]?)([0-9]*)\s*(?:ohms?|Ω)?\s*$`)
	capacitanceValuePattern = regexp.MustCompile(`(?i)^\s*([0-9]*\.?[0-9]+)\s*([pnuμm]?)\s*f?\s*$`)
)

// passiveQuery recognises a chip resistor or capacitor by designator prefix,
// value and footprint size, for parametric catalog lookup.
func passiveQuery(c Component) (kind string, value float64, pkg string, ok bool) {
	m := passivePackagePattern.FindStringSubmatch(c.Footprint)
	if m == nil {
		return "", 0, "", false
	}
	pkg = m[1]
	prefix := strings.ToUpper(strings.TrimRight(c.Designator, "0123456789"))
	switch prefix {
	case "R":
		v, ok := parseResistance(c.Value)
		return "resistor", v, pkg, ok
	case "C":
		v, ok := parseCapacitance(c.Value)
		return "capacitor", v, pkg, ok
	}
	return "", 0, "", false
}

func parseResistance(text string) (float64, bool) {
	m := resistanceValuePattern.FindStringSubmatch(strings.ReplaceAll(text, " ", ""))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	mult := map[string]float64{"": 1, "r": 1, "k": 1e3, "m": 1e6}[strings.ToLower(m[2])]
	if m[3] != "" { // 4k7 style
		if m[2] == "" || strings.Contains(m[1], ".") {
			return 0, false
		}
		frac, _ := strconv.ParseFloat("0."+m[3], 64)
		v += frac
	}
	return v * mult, v > 0 || m[1] == "0"
}

func parseCapacitance(text string) (float64, bool) {
	m := capacitanceValuePattern.FindStringSubmatch(text)
	if m == nil || m[2] == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	mult := map[string]float64{"p": 1e-12, "n": 1e-9, "u": 1e-6, "μ": 1e-6, "m": 1e-3}[strings.ToLower(m[2])]
	return v * mult, mult > 0
}

// SearchPassive finds stocked chip resistors or capacitors by value and size.
func (c *lcscCatalog) SearchPassive(kind string, value float64, pkg string) ([]LCSCPart, error) {
	values := url.Values{"package": {pkg}, "limit": {"20"}}
	path := "/resistors/list.json"
	key := "resistors"
	if kind == "capacitor" {
		path, key = "/capacitors/list.json", "capacitors"
		values.Set("capacitance", strconv.FormatFloat(value, 'g', 10, 64))
	} else {
		values.Set("resistance", strconv.FormatFloat(value, 'g', 10, 64))
	}
	resp, err := c.client.Get(c.base + path + "?" + values.Encode())
	if err != nil {
		return nil, fmt.Errorf("LCSC catalog request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LCSC catalog returned HTTP %d", resp.StatusCode)
	}
	var raw map[string][]struct {
		LCSC        json.Number `json:"lcsc"`
		MFR         string      `json:"mfr"`
		Description string      `json:"description"`
		Stock       *int64      `json:"stock"`
		Price1      float64     `json:"price1"`
		InStock     bool        `json:"in_stock"`
		Package     string      `json:"package"`
		IsBasic     bool        `json:"is_basic"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("LCSC catalog response could not be parsed: %w", err)
	}
	out := []LCSCPart{}
	for _, r := range raw[key] {
		number := strings.TrimPrefix(r.LCSC.String(), "C")
		if number == "" {
			continue
		}
		stock := int64(0)
		if r.Stock != nil {
			stock = *r.Stock
		}
		part := LCSCPart{LCSC: "C" + number, MPN: r.MFR, Package: r.Package, Description: r.Description, Stock: stock, Basic: r.IsBasic, StockUnknown: r.Stock == nil}
		if !r.InStock && r.Stock != nil {
			continue // known out of stock
		}
		if r.Price1 > 0 {
			part.PriceBreaks = []PriceBreak{{Min: 1, UnitUSD: r.Price1}}
		}
		out = append(out, part)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StockUnknown != out[j].StockUnknown {
			return !out[i].StockUnknown
		}
		if out[i].Basic != out[j].Basic {
			return out[i].Basic
		}
		return out[i].Stock > out[j].Stock
	})
	return out, nil
}
