package main

// Print view — `GET /invoices/{id}/print` returns a self-contained
// HTML page styled for `@media print`. No external CSS, no JS, no
// fetch-back. The browser does the PDF export when the user hits Cmd-P
// → "Save as PDF". This is the cheap, zero-dep fallback alongside
// the gofpdf path in pdf.go (which produces an actual PDF byte stream).
//
// The template is intentionally boring: a header with company /
// invoice number, a bill-to block, a line-item table, totals, status,
// and notes. v0.1.x can layer richer styling and a logo upload on
// top — the structure stays the same.

import (
	"bytes"
	"fmt"
	"html"
	"strings"
	"time"
)

// renderInvoiceHTML produces the self-contained print page for one
// invoice. customer is optional — when nil, the bill-to block falls
// back to "Customer #<id>". issuer is optional; when nil/unconfigured,
// the BILL FROM column shows a single placeholder line.
func renderInvoiceHTML(inv *Invoice, customer *Customer, issuer *Issuer) string {
	var b bytes.Buffer

	title := inv.Number
	if title == "" {
		title = fmt.Sprintf("Draft #%d", inv.ID)
	}

	b.WriteString(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(` — Invoice</title>
<style>
  :root {
    --ink: #111;
    --muted: #666;
    --line: #ddd;
    --accent: #2c5282;
    --paid: #2f855a;
    --void: #c53030;
  }
  * { box-sizing: border-box; }
  body {
    font: 13px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
    color: var(--ink);
    margin: 0;
    background: #f5f5f5;
  }
  .page {
    max-width: 720px;
    margin: 24px auto;
    background: #fff;
    padding: 48px 56px;
    box-shadow: 0 1px 4px rgba(0,0,0,0.08);
  }
  header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 16px; }
  .from h1 { margin: 0; font-size: 22px; font-weight: 600; }
  .from .tagline { color: var(--muted); font-size: 12px; margin-top: 2px; }
  .meta { text-align: right; }
  .meta .date-line { color: var(--muted); font-size: 12px; margin-top: 4px; }
  .bank { margin-top: 24px; padding-top: 16px; border-top: 1px solid var(--line); }
  .bank h2 { color: var(--muted); font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; font-weight: 600; margin: 0 0 6px 0; }
  .bank .row { font-size: 13px; line-height: 1.6; }
  .bank .row .label { display: inline-block; min-width: 90px; color: var(--muted); }
  .bank .iban { font-variant-numeric: tabular-nums; font-family: ui-monospace, "SF Mono", Consolas, monospace; }
  .footer-text { margin-top: 24px; padding-top: 16px; border-top: 1px solid var(--line); color: var(--muted); font-size: 11px; font-style: italic; text-align: center; }
  .reverse-charge { margin-top: 16px; padding: 10px 12px; border: 1px solid var(--line); border-left: 3px solid var(--accent); background: #f7fafc; font-size: 12px; }
  .reverse-charge .label { color: var(--muted); font-size: 10px; text-transform: uppercase; letter-spacing: 0.05em; font-weight: 600; margin-bottom: 4px; }
  .reverse-charge .body { color: var(--ink); }
  .meta .label { color: var(--muted); font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; }
  .meta .value { font-size: 18px; font-weight: 600; margin-top: 2px; }
  .pill {
    display: inline-block; padding: 2px 8px; border-radius: 3px;
    font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; font-weight: 600;
    background: #eee; color: var(--muted);
  }
  .pill.open { background: #ebf4ff; color: var(--accent); }
  .pill.paid { background: #e6fffa; color: var(--paid); }
  .pill.void { background: #fed7d7; color: var(--void); text-decoration: line-through; }
  .pill.uncollectible { background: #fffaf0; color: #b7791f; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 32px; margin: 24px 0; }
  .grid h2 { color: var(--muted); font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; font-weight: 600; margin: 0 0 6px 0; }
  .grid p { margin: 0; line-height: 1.55; }
  .grid p .secondary { color: var(--muted); font-size: 12px; }
  table { width: 100%; border-collapse: collapse; margin: 24px 0; }
  th, td { padding: 8px 4px; text-align: left; border-bottom: 1px solid var(--line); }
  th { color: var(--muted); font-weight: 500; font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; }
  td.num, th.num { text-align: right; font-variant-numeric: tabular-nums; }
  tfoot td { border-bottom: none; padding-top: 4px; padding-bottom: 4px; }
  tfoot tr.total td { font-weight: 600; border-top: 2px solid var(--ink); padding-top: 8px; }
  .notes { margin-top: 24px; padding-top: 16px; border-top: 1px solid var(--line); color: var(--muted); white-space: pre-wrap; font-size: 12px; }
  .toolbar { max-width: 720px; margin: 16px auto 0; display: flex; gap: 8px; }
  .toolbar button {
    border: 1px solid var(--accent); background: var(--accent); color: #fff;
    padding: 6px 14px; border-radius: 3px; font: inherit; cursor: pointer;
  }
  .toolbar a {
    border: 1px solid var(--line); background: #fff; color: var(--ink);
    padding: 6px 14px; border-radius: 3px; text-decoration: none;
  }
  @media print {
    body { background: #fff; }
    .page { box-shadow: none; margin: 0; max-width: none; padding: 24mm 20mm; }
    .toolbar { display: none; }
    @page { size: A4; margin: 0; }
  }
</style>
</head>
<body>
<div class="toolbar">
  <button type="button" onclick="window.print()">Print / Save as PDF</button>
  <a href="javascript:history.back()">Back</a>
</div>
<div class="page">
  <header>
    <div class="from">
      <h1>Invoice</h1>
    </div>
    <div class="meta">
      <div class="label">Invoice</div>
      <div class="value">`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(`</div>
      <div style="margin-top:6px;"><span class="pill `)
	b.WriteString(html.EscapeString(inv.Status))
	b.WriteString(`">`)
	b.WriteString(html.EscapeString(inv.Status))
	b.WriteString(`</span></div>`)
	if issueLine := formatInvoiceIssueLine(inv); issueLine != "" {
		b.WriteString(`
      <div class="date-line">`)
		b.WriteString(html.EscapeString(issueLine))
		b.WriteString(`</div>`)
	}
	if dueLine := formatInvoiceDueLine(inv); dueLine != "" {
		b.WriteString(`
      <div class="date-line">`)
		b.WriteString(html.EscapeString(dueLine))
		b.WriteString(`</div>`)
	}
	b.WriteString(`
    </div>
  </header>

  <div class="grid">
    <div>
      <h2>Bill from</h2>
      <p>`)
	writeIssuerHTML(&b, issuer)
	b.WriteString(`</p>
    </div>
    <div>
      <h2>Bill to</h2>
      <p>`)
	if customer != nil {
		b.WriteString(`<strong>`)
		b.WriteString(html.EscapeString(customer.Name))
		b.WriteString(`</strong>`)
		if customer.Email != "" {
			b.WriteString(`<br><span class="secondary">`)
			b.WriteString(html.EscapeString(customer.Email))
			b.WriteString(`</span>`)
		}
		if addr := formatBillingAddress(customer.BillingAddress); addr != "" {
			b.WriteString(`<br><span class="secondary">`)
			b.WriteString(strings.ReplaceAll(html.EscapeString(addr), "\n", "<br>"))
			b.WriteString(`</span>`)
		}
		if tids := formatTaxIDs(customer.TaxIDs); tids != "" {
			b.WriteString(`<br><span class="secondary">`)
			b.WriteString(html.EscapeString(tids))
			b.WriteString(`</span>`)
		}
	} else {
		fmt.Fprintf(&b, `Customer #%d`, inv.CustomerID)
	}
	b.WriteString(`</p>
    </div>
  </div>

  <table>
    <thead>
      <tr>
        <th>Description</th>
        <th class="num" style="width:60px;">Qty</th>
        <th class="num" style="width:110px;">Unit</th>
        <th class="num" style="width:60px;">Tax</th>
        <th class="num" style="width:120px;">Amount</th>
      </tr>
    </thead>
    <tbody>
`)
	for _, li := range inv.LineItems {
		fmt.Fprintf(&b,
			`      <tr><td>%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td></tr>
`,
			html.EscapeString(li.Description),
			html.EscapeString(formatQty(li.Quantity)),
			html.EscapeString(formatMoney(li.UnitPriceCents, inv.Currency)),
			html.EscapeString(fmt.Sprintf("%.2f%%", float64(li.TaxRateBps)/100)),
			html.EscapeString(formatMoney(li.AmountCents, inv.Currency)))
	}
	if len(inv.LineItems) == 0 {
		b.WriteString(`      <tr><td colspan="5" style="text-align:center;color:var(--muted);">No line items.</td></tr>
`)
	}
	b.WriteString(`    </tbody>
    <tfoot>
      <tr>
        <td colspan="4" class="num" style="color:var(--muted);">Subtotal</td>
        <td class="num">`)
	b.WriteString(html.EscapeString(formatMoney(inv.SubtotalCents, inv.Currency)))
	b.WriteString(`</td>
      </tr>
      <tr>
        <td colspan="4" class="num" style="color:var(--muted);">Tax</td>
        <td class="num">`)
	b.WriteString(html.EscapeString(formatMoney(inv.TaxCents, inv.Currency)))
	b.WriteString(`</td>
      </tr>
      <tr class="total">
        <td colspan="4" class="num">Total</td>
        <td class="num">`)
	b.WriteString(html.EscapeString(formatMoney(inv.TotalCents, inv.Currency)))
	b.WriteString(`</td>
      </tr>`)
	if inv.AmountPaidCents > 0 {
		fmt.Fprintf(&b, `
      <tr>
        <td colspan="4" class="num" style="color:var(--muted);">Paid</td>
        <td class="num">%s</td>
      </tr>
      <tr>
        <td colspan="4" class="num" style="color:var(--muted);">Balance due</td>
        <td class="num">%s</td>
      </tr>`,
			html.EscapeString(formatMoney(inv.AmountPaidCents, inv.Currency)),
			html.EscapeString(formatMoney(maxInt(0, inv.TotalCents-inv.AmountPaidCents), inv.Currency)))
	}
	b.WriteString(`
    </tfoot>
  </table>
`)

	// EU reverse-charge legal notice (appears before bank block).
	if qualifiesForEUReverseCharge(issuer, customer, inv) {
		b.WriteString(`  <div class="reverse-charge">
    <div class="label">Reverse charge — EU intra-community supply</div>
    <div class="body">`)
		b.WriteString(html.EscapeString(reverseChargeNotice))
		b.WriteString(`</div>
  </div>
`)
	}

	// PAY BY BANK TRANSFER block (only when issuer has IBAN)
	if issuer != nil && issuer.Configured {
		bank := parseBank(issuer.Bank)
		if bank.IBAN != "" {
			beneficiary := bank.Beneficiary
			if beneficiary == "" {
				beneficiary = issuer.LegalName
			}
			if beneficiary == "" {
				beneficiary = issuer.DisplayName
			}
			b.WriteString(`  <div class="bank">
    <h2>Pay by bank transfer</h2>
`)
			if beneficiary != "" {
				fmt.Fprintf(&b, `    <div class="row"><span class="label">Beneficiary</span>%s</div>
`, html.EscapeString(beneficiary))
			}
			fmt.Fprintf(&b, `    <div class="row"><span class="label">IBAN</span><span class="iban">%s</span></div>
`, html.EscapeString(formatIBAN(bank.IBAN)))
			if bank.BIC != "" {
				bicLine := bank.BIC
				if bank.BankCode != "" {
					bicLine += " · Bank code " + bank.BankCode
				}
				fmt.Fprintf(&b, `    <div class="row"><span class="label">BIC / SWIFT</span>%s</div>
`, html.EscapeString(bicLine))
			} else if bank.BankCode != "" {
				fmt.Fprintf(&b, `    <div class="row"><span class="label">Bank code</span>%s</div>
`, html.EscapeString(bank.BankCode))
			}
			if bank.BankName != "" {
				fmt.Fprintf(&b, `    <div class="row"><span class="label">Bank</span>%s</div>
`, html.EscapeString(bank.BankName))
			}
			b.WriteString(`  </div>
`)
		}
	}

	if inv.Notes != "" {
		b.WriteString(`  <div class="notes">`)
		b.WriteString(html.EscapeString(inv.Notes))
		b.WriteString(`</div>
`)
	}

	if issuer != nil && issuer.FooterText != "" {
		b.WriteString(`  <div class="footer-text">`)
		b.WriteString(html.EscapeString(issuer.FooterText))
		b.WriteString(`</div>
`)
	}

	b.WriteString(`</div>
</body>
</html>
`)
	return b.String()
}

// writeIssuerHTML emits the BILL FROM column body. Falls back to a
// single placeholder line when nothing's configured so the column
// doesn't collapse.
func writeIssuerHTML(b *bytes.Buffer, issuer *Issuer) {
	if issuer == nil || !issuer.Configured || issuer.DisplayName == "" {
		b.WriteString(`<span class="secondary">Issued by your Apteva project</span>`)
		return
	}
	b.WriteString(`<strong>`)
	b.WriteString(html.EscapeString(issuer.DisplayName))
	b.WriteString(`</strong>`)
	if issuer.LegalName != "" && issuer.LegalName != issuer.DisplayName {
		b.WriteString(`<br><span class="secondary">`)
		b.WriteString(html.EscapeString(issuer.LegalName))
		b.WriteString(`</span>`)
	}
	if addr := formatBillingAddress(issuer.Address); addr != "" {
		b.WriteString(`<br><span class="secondary">`)
		b.WriteString(strings.ReplaceAll(html.EscapeString(addr), "\n", "<br>"))
		b.WriteString(`</span>`)
	}
	if tids := formatTaxIDs(issuer.TaxIDs); tids != "" {
		b.WriteString(`<br><span class="secondary">`)
		b.WriteString(html.EscapeString(tids))
		b.WriteString(`</span>`)
	}
	if issuer.Email != "" {
		b.WriteString(`<br><span class="secondary">`)
		b.WriteString(html.EscapeString(issuer.Email))
		b.WriteString(`</span>`)
	}
}

// ─── Format helpers ─────────────────────────────────────────────────

// formatMoney renders integer cents as "$12.34" / "€12.34" /
// "USD 12.34" depending on the currency. Pure server-side; no Intl.
// Uses ASCII fallbacks for common currencies and 3-letter prefix
// otherwise.
func formatMoney(cents int64, currency string) string {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	sign := ""
	abs := cents
	if abs < 0 {
		sign = "-"
		abs = -abs
	}
	whole := abs / 100
	frac := abs % 100
	switch currency {
	case "USD", "CAD", "AUD", "NZD":
		return fmt.Sprintf("%s$%d.%02d", sign, whole, frac)
	case "EUR":
		return fmt.Sprintf("%s€%d.%02d", sign, whole, frac)
	case "GBP":
		return fmt.Sprintf("%s£%d.%02d", sign, whole, frac)
	case "JPY":
		// Yen has no fractional unit; we still keep the Cents-int
		// internal representation for arithmetic consistency, but
		// display whole units only.
		return fmt.Sprintf("%s¥%d", sign, whole)
	default:
		return fmt.Sprintf("%s%s %d.%02d", sign, currency, whole, frac)
	}
}

func formatQty(q float64) string {
	// Drop trailing zeros so "10.00" → "10" but "1.5" stays.
	s := fmt.Sprintf("%.2f", q)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

// formatInvoiceIssueLine returns "Issued <date>" once finalized, or
// "Created <date>" while a draft. Empty when neither timestamp is set.
func formatInvoiceIssueLine(inv *Invoice) string {
	if d := inv.FinalizedAt; d != "" {
		return "Issued " + formatDateOnly(d)
	}
	if d := inv.CreatedAt; d != "" {
		return "Created " + formatDateOnly(d)
	}
	return ""
}

// formatInvoiceDueLine returns "Due on receipt" when the user didn't
// set a due date OR set it ≤ the issue date (a same-day due date
// reads as 'pay now' in practice). Otherwise "Due <date>".
func formatInvoiceDueLine(inv *Invoice) string {
	if inv.DueDate == "" {
		return "Due on receipt"
	}
	issuedRaw := inv.FinalizedAt
	if issuedRaw == "" {
		issuedRaw = inv.CreatedAt
	}
	if dueOnReceipt(inv.DueDate, issuedRaw) {
		return "Due on receipt"
	}
	return "Due " + formatDateOnly(inv.DueDate)
}

// dueOnReceipt is true when due is at or before issued — meaning the
// invoice is payable immediately and the literal date adds no info.
func dueOnReceipt(due, issued string) bool {
	if due == "" {
		return true
	}
	if issued == "" {
		return false
	}
	d := dateOnlyKey(due)
	i := dateOnlyKey(issued)
	return d != "" && i != "" && d <= i
}

// dateOnlyKey extracts a YYYY-MM-DD key from either RFC3339 or a bare
// YYYY-MM-DD. Returns "" when unparseable. Lex-sortable so callers can
// compare with <= without a time.Parse round-trip.
func dateOnlyKey(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return s
	}
	return ""
}

func formatDateOnly(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		// Try date-only — billing accepts due_date as a YYYY-MM-DD too.
		if t2, err2 := time.Parse("2006-01-02", rfc3339); err2 == nil {
			return t2.Format("Jan 2, 2006")
		}
		return rfc3339
	}
	return t.UTC().Format("Jan 2, 2006")
}

// formatBillingAddress unmarshals the JSON blob into a multi-line
// human-readable string. Returns "" when empty / unparseable rather
// than leaking JSON into the page. Country code is rendered as a full
// name (e.g. "EE" → "Estonia") when known; falls back to the code.
func formatBillingAddress(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var addr struct {
		Line1      string `json:"line1"`
		Line2      string `json:"line2"`
		City       string `json:"city"`
		State      string `json:"state"`
		PostalCode string `json:"postal_code"`
		Country    string `json:"country"`
	}
	if err := jsonDecodeRaw(raw, &addr); err != nil {
		return ""
	}
	var lines []string
	if addr.Line1 != "" {
		lines = append(lines, addr.Line1)
	}
	if addr.Line2 != "" {
		lines = append(lines, addr.Line2)
	}
	cityStateZip := strings.TrimSpace(strings.Join([]string{addr.City, addr.State, addr.PostalCode}, " "))
	if cityStateZip != "" {
		lines = append(lines, cityStateZip)
	}
	if addr.Country != "" {
		lines = append(lines, countryName(addr.Country))
	}
	return strings.Join(lines, "\n")
}

// addressCountry extracts the country code from a billing-address JSON
// blob. Returns "" when missing/unparseable. Uppercased ISO-2.
func addressCountry(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var addr struct {
		Country string `json:"country"`
	}
	if err := jsonDecodeRaw(raw, &addr); err != nil {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(addr.Country))
}

// countryName looks up the English name for an ISO-3166-1 alpha-2 code.
// Falls back to the code itself for entries not in the table — we'd
// rather print "ZZ" than nothing when an unfamiliar country shows up.
func countryName(code string) string {
	c := strings.ToUpper(strings.TrimSpace(code))
	if name, ok := countryByCode[c]; ok {
		return name
	}
	return c
}

// Subset of ISO-3166-1 covering the EU + common trading partners.
// Extend as new customers come in. Not localized — English only.
var countryByCode = map[string]string{
	// EU 27
	"AT": "Austria", "BE": "Belgium", "BG": "Bulgaria", "HR": "Croatia",
	"CY": "Cyprus", "CZ": "Czech Republic", "DK": "Denmark", "EE": "Estonia",
	"FI": "Finland", "FR": "France", "DE": "Germany", "GR": "Greece",
	"HU": "Hungary", "IE": "Ireland", "IT": "Italy", "LV": "Latvia",
	"LT": "Lithuania", "LU": "Luxembourg", "MT": "Malta", "NL": "Netherlands",
	"PL": "Poland", "PT": "Portugal", "RO": "Romania", "SK": "Slovakia",
	"SI": "Slovenia", "ES": "Spain", "SE": "Sweden",
	// EEA + commonly-billed neighbours
	"GB": "United Kingdom", "CH": "Switzerland", "NO": "Norway",
	"IS": "Iceland", "LI": "Liechtenstein",
	// Anglosphere + major markets
	"US": "United States", "CA": "Canada", "AU": "Australia",
	"NZ": "New Zealand", "JP": "Japan", "CN": "China", "IN": "India",
	"BR": "Brazil", "MX": "Mexico", "AR": "Argentina", "ZA": "South Africa",
	"IL": "Israel", "AE": "United Arab Emirates", "SG": "Singapore",
	"HK": "Hong Kong", "KR": "South Korea", "TR": "Turkey",
}

// isEUMember reports whether code is one of the 27 EU member states.
// Used by the reverse-charge predicate; not exported as a country
// "fact" because the membership list is policy, not geography.
func isEUMember(code string) bool {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "AT", "BE", "BG", "HR", "CY", "CZ", "DK", "EE", "FI", "FR",
		"DE", "GR", "HU", "IE", "IT", "LV", "LT", "LU", "MT", "NL",
		"PL", "PT", "RO", "SK", "SI", "ES", "SE":
		return true
	}
	return false
}

// hasVATID reports whether the tax_ids JSON array contains at least
// one entry with type="vat" and a non-empty value.
func hasVATID(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var arr []struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	if err := jsonDecodeRaw(raw, &arr); err != nil {
		return false
	}
	for _, t := range arr {
		if strings.EqualFold(strings.TrimSpace(t.Type), "vat") &&
			strings.TrimSpace(t.Value) != "" {
			return true
		}
	}
	return false
}

// qualifiesForEUReverseCharge: both parties are EU businesses with
// valid VAT IDs, billing across borders, and the invoice has no VAT
// charged. When true, the rendered invoice must surface the legal
// notice citing Article 196 of Directive 2006/112/EC — otherwise the
// invoice isn't compliant with intra-community supply rules.
//
// Same-country EU B2B uses domestic VAT, not reverse charge, so we
// require issuer.country != customer.country.
func qualifiesForEUReverseCharge(issuer *Issuer, customer *Customer, inv *Invoice) bool {
	if inv == nil || inv.TaxCents != 0 {
		return false
	}
	if issuer == nil || !issuer.Configured || customer == nil {
		return false
	}
	ic := addressCountry(issuer.Address)
	cc := addressCountry(customer.BillingAddress)
	if ic == "" || cc == "" || ic == cc {
		return false
	}
	if !isEUMember(ic) || !isEUMember(cc) {
		return false
	}
	return hasVATID(issuer.TaxIDs) && hasVATID(customer.TaxIDs)
}

const reverseChargeNotice = "Reverse charge — VAT to be accounted for by the recipient. Article 196 of Council Directive 2006/112/EC."

func maxInt(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
