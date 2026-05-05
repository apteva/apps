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
// back to "Customer #<id>".
func renderInvoiceHTML(inv *Invoice, customer *Customer) string {
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
  header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 32px; }
  .from h1 { margin: 0; font-size: 22px; font-weight: 600; }
  .from .tagline { color: var(--muted); font-size: 12px; margin-top: 2px; }
  .meta { text-align: right; }
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
      <div class="tagline">Issued by your Apteva project</div>
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
	b.WriteString(`</span></div>
    </div>
  </header>

  <div class="grid">
    <div>
      <h2>Bill to</h2>
      <p>`)
	if customer != nil {
		b.WriteString(`<strong>`)
		b.WriteString(html.EscapeString(customer.Name))
		b.WriteString(`</strong><br>`)
		if customer.Email != "" {
			b.WriteString(`<span class="secondary">`)
			b.WriteString(html.EscapeString(customer.Email))
			b.WriteString(`</span>`)
		}
		if addr := formatBillingAddress(customer.BillingAddress); addr != "" {
			b.WriteString(`<br><span class="secondary">`)
			b.WriteString(strings.ReplaceAll(html.EscapeString(addr), "\n", "<br>"))
			b.WriteString(`</span>`)
		}
	} else {
		fmt.Fprintf(&b, `Customer #%d`, inv.CustomerID)
	}
	b.WriteString(`</p>
    </div>
    <div>
      <h2>Details</h2>
      <p>`)
	if d := inv.FinalizedAt; d != "" {
		fmt.Fprintf(&b, "Issued: %s<br>", html.EscapeString(formatDateOnly(d)))
	} else if d := inv.CreatedAt; d != "" {
		fmt.Fprintf(&b, "Created: %s<br>", html.EscapeString(formatDateOnly(d)))
	}
	if inv.DueDate != "" {
		fmt.Fprintf(&b, "Due: %s<br>", html.EscapeString(formatDateOnly(inv.DueDate)))
	}
	fmt.Fprintf(&b, `<span class="secondary">Currency: %s · Provider: %s</span>`,
		html.EscapeString(inv.Currency), html.EscapeString(inv.Provider))
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

	if inv.Notes != "" {
		b.WriteString(`  <div class="notes">`)
		b.WriteString(html.EscapeString(inv.Notes))
		b.WriteString(`</div>
`)
	}

	b.WriteString(`</div>
</body>
</html>
`)
	return b.String()
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
// than leaking JSON into the page.
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
		lines = append(lines, addr.Country)
	}
	return strings.Join(lines, "\n")
}

func maxInt(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
