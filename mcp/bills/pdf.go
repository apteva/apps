package main

// Voucher rendering — the AP equivalent of billing's invoice
// rendering. Crucially this is OUR copy for filing/audit, NOT a
// document we send to the vendor. Layout emphasises:
//   1. Vendor's invoice number + date (their reference, prominent)
//   2. Our internal bill id + status (small, top-right)
//   3. Approval trail and payment history (bottom)
//
// Same gofpdf base + same Helvetica + same A4 margins as billing's
// pdf.go. Format helpers (formatMoney etc.) are duplicated by design
// per the proposal — three-line rule says we extract once a third
// app needs them. Keeping the docs explicit so future-us doesn't
// "fix" the duplication prematurely.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

// renderBillPDF builds the voucher PDF and returns its bytes. vendor
// may be nil (soft-deleted-after-receipt case) — bill_to falls back
// to "Vendor #<id>".
func renderBillPDF(bill *Bill, vendor *Vendor) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(20, 20, 20)
	pdf.SetAutoPageBreak(true, 20)
	pdf.AddPage()
	pdf.SetTitle(voucherTitle(bill), false)
	pdf.SetCreator("Apteva bills (voucher)", false)

	pageWidth, _ := pdf.GetPageSize()
	usable := pageWidth - 40

	// ── Header: BIG vendor invoice number + small "voucher" stamp ──
	pdf.SetFont("Helvetica", "B", 22)
	pdf.SetTextColor(20, 20, 20)
	pdf.CellFormat(usable*2/3, 10, voucherTitle(bill), "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetTextColor(140, 80, 80)
	pdf.CellFormat(usable/3, 10, "VOUCHER (our copy)", "", 0, "R", false, 0, "")
	pdf.Ln(10)

	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(120, 120, 120)
	pdf.Cell(usable*2/3, 5, "Bill received from vendor")
	pdf.SetX(20 + usable*2/3)
	pdf.CellFormat(usable/3, 5,
		fmt.Sprintf("Internal id: %d · %s", bill.ID, statusLabel(bill.Status)),
		"", 0, "R", false, 0, "")
	pdf.Ln(12)

	pdf.SetDrawColor(200, 200, 200)
	pdf.Line(20, pdf.GetY(), pageWidth-20, pdf.GetY())
	pdf.Ln(8)

	// ── Vendor + Details ──
	colW := usable / 2
	yStart := pdf.GetY()

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(120, 120, 120)
	pdf.CellFormat(colW, 5, "VENDOR", "", 0, "L", false, 0, "")
	pdf.SetX(20 + colW)
	pdf.CellFormat(colW, 5, "DETAILS", "", 0, "L", false, 0, "")
	pdf.Ln(6)

	leftLines := buildVendorBlockLines(bill, vendor)
	rightLines := buildBillDetailLines(bill)

	maxLines := len(leftLines)
	if len(rightLines) > maxLines {
		maxLines = len(rightLines)
	}
	for i := 0; i < maxLines; i++ {
		pdf.SetXY(20, yStart+6+float64(i)*5)
		if i < len(leftLines) {
			if i == 0 {
				pdf.SetFont("Helvetica", "B", 10)
				pdf.SetTextColor(40, 40, 40)
			} else {
				pdf.SetFont("Helvetica", "", 9)
				pdf.SetTextColor(110, 110, 110)
			}
			pdf.CellFormat(colW, 5, leftLines[i], "", 0, "L", false, 0, "")
		}
		if i < len(rightLines) {
			pdf.SetXY(20+colW, yStart+6+float64(i)*5)
			pdf.SetFont("Helvetica", "", 9)
			pdf.SetTextColor(110, 110, 110)
			pdf.CellFormat(colW, 5, rightLines[i], "", 0, "L", false, 0, "")
		}
	}
	pdf.SetXY(20, yStart+6+float64(maxLines)*5+6)

	// ── Line items ──
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(120, 120, 120)
	descW := usable - 100
	pdf.CellFormat(descW, 7, "DESCRIPTION", "", 0, "L", false, 0, "")
	pdf.CellFormat(20, 7, "QTY", "", 0, "R", false, 0, "")
	pdf.CellFormat(30, 7, "UNIT", "", 0, "R", false, 0, "")
	pdf.CellFormat(20, 7, "TAX", "", 0, "R", false, 0, "")
	pdf.CellFormat(30, 7, "AMOUNT", "", 0, "R", false, 0, "")
	pdf.Ln(7)
	pdf.SetDrawColor(220, 220, 220)
	pdf.Line(20, pdf.GetY(), pageWidth-20, pdf.GetY())
	pdf.Ln(2)

	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(40, 40, 40)
	if len(bill.LineItems) == 0 {
		pdf.SetTextColor(160, 160, 160)
		pdf.CellFormat(usable, 8, "No line items.", "", 0, "C", false, 0, "")
		pdf.Ln(8)
	} else {
		for _, li := range bill.LineItems {
			pdf.CellFormat(descW, 6, li.Description, "", 0, "L", false, 0, "")
			pdf.CellFormat(20, 6, formatQty(li.Quantity), "", 0, "R", false, 0, "")
			pdf.CellFormat(30, 6, formatMoney(li.UnitPriceCents, bill.Currency), "", 0, "R", false, 0, "")
			pdf.CellFormat(20, 6, fmt.Sprintf("%.2f%%", float64(li.TaxRateBps)/100), "", 0, "R", false, 0, "")
			pdf.CellFormat(30, 6, formatMoney(li.AmountCents, bill.Currency), "", 0, "R", false, 0, "")
			pdf.Ln(6)
		}
	}

	// ── Totals (right-aligned) ──
	pdf.Ln(4)
	pdf.SetDrawColor(220, 220, 220)
	pdf.Line(20, pdf.GetY(), pageWidth-20, pdf.GetY())
	pdf.Ln(2)

	totalsX := pageWidth - 20 - 70
	totalsLabelW := 40.0
	totalsValueW := 30.0
	drawTotalRow := func(label, value string, bold, sep bool) {
		pdf.SetX(totalsX)
		if bold {
			pdf.SetFont("Helvetica", "B", 10)
			pdf.SetTextColor(20, 20, 20)
		} else {
			pdf.SetFont("Helvetica", "", 10)
			pdf.SetTextColor(110, 110, 110)
		}
		pdf.CellFormat(totalsLabelW, 6, label, "", 0, "L", false, 0, "")
		pdf.SetTextColor(40, 40, 40)
		pdf.CellFormat(totalsValueW, 6, value, "", 0, "R", false, 0, "")
		pdf.Ln(6)
		if sep {
			pdf.SetX(totalsX)
			pdf.SetDrawColor(160, 160, 160)
			pdf.Line(totalsX, pdf.GetY(), pageWidth-20, pdf.GetY())
			pdf.Ln(1)
		}
	}
	drawTotalRow("Subtotal", formatMoney(bill.SubtotalCents, bill.Currency), false, false)
	drawTotalRow("Input tax", formatMoney(bill.TaxCents, bill.Currency), false, true)
	drawTotalRow("Total", formatMoney(bill.TotalCents, bill.Currency), true, false)
	if bill.AmountPaidCents > 0 {
		drawTotalRow("Paid", formatMoney(bill.AmountPaidCents, bill.Currency), false, false)
		balance := bill.TotalCents - bill.AmountPaidCents
		if balance < 0 {
			balance = 0
		}
		drawTotalRow("Balance owed", formatMoney(balance, bill.Currency), true, false)
	}

	// ── Approval trail + payment history ──
	if bill.ApprovedAt != "" || len(bill.Payments) > 0 || bill.VoidedAt != "" || bill.DisputedAt != "" {
		pdf.Ln(8)
		pdf.SetDrawColor(220, 220, 220)
		pdf.Line(20, pdf.GetY(), pageWidth-20, pdf.GetY())
		pdf.Ln(3)
		pdf.SetFont("Helvetica", "B", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(usable, 5, "WORKFLOW + PAYMENTS", "", 0, "L", false, 0, "")
		pdf.Ln(5)

		pdf.SetFont("Helvetica", "", 9)
		pdf.SetTextColor(80, 80, 80)
		if bill.ApprovedAt != "" {
			line := fmt.Sprintf("Approved %s", formatDateOnly(bill.ApprovedAt))
			if bill.ApprovedBy != "" {
				line += " by " + bill.ApprovedBy
			}
			pdf.MultiCell(usable, 4.5, line, "", "L", false)
		}
		if bill.ScheduledFor != "" {
			pdf.MultiCell(usable, 4.5,
				fmt.Sprintf("Scheduled for %s (%s)",
					formatDateOnly(bill.ScheduledFor), bill.ScheduledMethod),
				"", "L", false)
		}
		if bill.DisputedAt != "" {
			pdf.MultiCell(usable, 4.5,
				fmt.Sprintf("Disputed %s — see audit log for reason", formatDateOnly(bill.DisputedAt)),
				"", "L", false)
		}
		if bill.VoidedAt != "" {
			pdf.MultiCell(usable, 4.5,
				fmt.Sprintf("Voided %s", formatDateOnly(bill.VoidedAt)),
				"", "L", false)
		}
		for _, p := range bill.Payments {
			pdf.MultiCell(usable, 4.5,
				fmt.Sprintf("• Paid %s on %s via %s",
					formatMoney(p.AmountCents, p.Currency),
					formatDateOnly(p.SentAt), p.Method),
				"", "L", false)
		}
	}

	// ── Notes ──
	if bill.Notes != "" {
		pdf.Ln(6)
		pdf.SetDrawColor(220, 220, 220)
		pdf.Line(20, pdf.GetY(), pageWidth-20, pdf.GetY())
		pdf.Ln(3)
		pdf.SetFont("Helvetica", "B", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(usable, 5, "NOTES", "", 0, "L", false, 0, "")
		pdf.Ln(5)
		pdf.SetFont("Helvetica", "", 9)
		pdf.SetTextColor(80, 80, 80)
		pdf.MultiCell(usable, 4.5, bill.Notes, "", "L", false)
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf output: %w", err)
	}
	return buf.Bytes(), nil
}

func buildVendorBlockLines(bill *Bill, vendor *Vendor) []string {
	if vendor == nil {
		return []string{fmt.Sprintf("Vendor #%d", bill.VendorID)}
	}
	out := []string{vendor.Name}
	if vendor.Email != "" {
		out = append(out, vendor.Email)
	}
	if addr := formatBillingAddress(vendor.BillingAddress); addr != "" {
		for _, ln := range strings.Split(addr, "\n") {
			out = append(out, ln)
		}
	}
	return out
}

func buildBillDetailLines(bill *Bill) []string {
	var out []string
	if bill.VendorInvoiceDate != "" {
		out = append(out, "Vendor's date: "+formatDateOnly(bill.VendorInvoiceDate))
	}
	if bill.CreatedAt != "" {
		out = append(out, "Received: "+formatDateOnly(bill.CreatedAt))
	}
	if bill.DueDate != "" {
		out = append(out, "Due: "+formatDateOnly(bill.DueDate))
	}
	out = append(out, fmt.Sprintf("Currency: %s", bill.Currency))
	if bill.Category != "" {
		out = append(out, "Category: "+bill.Category)
	}
	if bill.GLAccount != "" {
		out = append(out, "GL: "+bill.GLAccount)
	}
	return out
}

func voucherTitle(bill *Bill) string {
	if bill.VendorInvoiceNumber != "" {
		return bill.VendorInvoiceNumber
	}
	return fmt.Sprintf("Bill #%d", bill.ID)
}

func statusLabel(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

func suggestPDFFilename(bill *Bill, vendor *Vendor) string {
	parts := []string{"voucher"}
	if vendor != nil && vendor.Name != "" {
		parts = append(parts, sanitiseForFilename(vendor.Name))
	}
	if bill.VendorInvoiceNumber != "" {
		parts = append(parts, sanitiseForFilename(bill.VendorInvoiceNumber))
	} else {
		parts = append(parts, fmt.Sprintf("bill-%d", bill.ID))
	}
	return strings.Join(parts, "-") + ".pdf"
}

func sanitiseForFilename(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '/' || c == '\\' || c == ':' || c == ' ' || c < 0x20:
			out = append(out, '_')
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// ─── Print view (HTML) ──────────────────────────────────────────────

// renderBillHTML produces a self-contained HTML page styled for
// `@media print`. Same shape as billing's print view; copy/paste
// per the proposal's three-line rule. Keep them in sync if you
// fix a bug here — same fix likely applies there.
func renderBillHTML(bill *Bill, vendor *Vendor) string {
	var b bytes.Buffer
	title := voucherTitle(bill)

	b.WriteString(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(` — Bill voucher</title>
<style>
  :root {
    --ink: #111;
    --muted: #666;
    --line: #ddd;
    --accent: #c05621;            /* amber for AP — visually distinct from billing's blue AR */
    --paid: #2f855a;
    --void: #c53030;
    --disp: #b7791f;
  }
  body { font: 13px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif; color: var(--ink); margin: 0; background: #f5f5f5; }
  .page { max-width: 720px; margin: 24px auto; background: #fff; padding: 48px 56px; box-shadow: 0 1px 4px rgba(0,0,0,0.08); }
  header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 32px; }
  .from h1 { margin: 0; font-size: 22px; font-weight: 600; }
  .from .tagline { color: var(--muted); font-size: 12px; margin-top: 2px; }
  .stamp { color: var(--accent); font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; font-weight: 700; }
  .meta { text-align: right; font-size: 12px; color: var(--muted); }
  .pill { display: inline-block; padding: 2px 8px; border-radius: 3px; font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; font-weight: 600; background: #eee; color: var(--muted); }
  .pill.received { background: #fef5e7; color: #d69e2e; }
  .pill.approved { background: #ebf4ff; color: #2c5282; }
  .pill.scheduled { background: #e6fffa; color: #2c7a7b; }
  .pill.paid { background: #e6fffa; color: var(--paid); }
  .pill.disputed { background: #fffaf0; color: var(--disp); }
  .pill.void { background: #fed7d7; color: var(--void); text-decoration: line-through; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 32px; margin: 24px 0; }
  .grid h2 { color: var(--muted); font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; font-weight: 600; margin: 0 0 6px 0; }
  .grid p { margin: 0; line-height: 1.55; }
  .grid p .secondary { color: var(--muted); font-size: 12px; }
  table { width: 100%; border-collapse: collapse; margin: 24px 0; }
  th, td { padding: 8px 4px; text-align: left; border-bottom: 1px solid var(--line); }
  th { color: var(--muted); font-weight: 500; font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; }
  td.num, th.num { text-align: right; font-variant-numeric: tabular-nums; }
  tfoot td { border-bottom: none; padding: 4px; }
  tfoot tr.total td { font-weight: 600; border-top: 2px solid var(--ink); padding-top: 8px; }
  .workflow { margin-top: 24px; padding-top: 16px; border-top: 1px solid var(--line); font-size: 12px; color: var(--muted); }
  .workflow ul { margin: 4px 0 0 16px; padding: 0; }
  .notes { margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--line); color: var(--muted); white-space: pre-wrap; font-size: 12px; }
  .toolbar { max-width: 720px; margin: 16px auto 0; display: flex; gap: 8px; }
  .toolbar button { border: 1px solid var(--accent); background: var(--accent); color: #fff; padding: 6px 14px; border-radius: 3px; font: inherit; cursor: pointer; }
  .toolbar a { border: 1px solid var(--line); background: #fff; color: var(--ink); padding: 6px 14px; border-radius: 3px; text-decoration: none; }
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
      <h1>`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(`</h1>
      <div class="tagline">Bill received — our voucher copy (not for vendor)</div>
      <div class="stamp" style="margin-top:6px;">VOUCHER</div>
    </div>
    <div class="meta">
      <span class="pill `)
	b.WriteString(html.EscapeString(bill.Status))
	b.WriteString(`">`)
	b.WriteString(html.EscapeString(bill.Status))
	b.WriteString(`</span>`)
	fmt.Fprintf(&b, `<div style="margin-top:4px;">Internal id: %d</div>`, bill.ID)
	b.WriteString(`</div>
  </header>

  <div class="grid">
    <div>
      <h2>Vendor</h2>
      <p>`)
	if vendor != nil {
		b.WriteString(`<strong>`)
		b.WriteString(html.EscapeString(vendor.Name))
		b.WriteString(`</strong>`)
		if vendor.Email != "" {
			b.WriteString(`<br><span class="secondary">`)
			b.WriteString(html.EscapeString(vendor.Email))
			b.WriteString(`</span>`)
		}
		if addr := formatBillingAddress(vendor.BillingAddress); addr != "" {
			b.WriteString(`<br><span class="secondary">`)
			b.WriteString(strings.ReplaceAll(html.EscapeString(addr), "\n", "<br>"))
			b.WriteString(`</span>`)
		}
	} else {
		fmt.Fprintf(&b, `Vendor #%d`, bill.VendorID)
	}
	b.WriteString(`</p>
    </div>
    <div>
      <h2>Details</h2>
      <p>`)
	if bill.VendorInvoiceDate != "" {
		fmt.Fprintf(&b, "Vendor's date: %s<br>", html.EscapeString(formatDateOnly(bill.VendorInvoiceDate)))
	}
	if bill.CreatedAt != "" {
		fmt.Fprintf(&b, "Received: %s<br>", html.EscapeString(formatDateOnly(bill.CreatedAt)))
	}
	if bill.DueDate != "" {
		fmt.Fprintf(&b, "Due: %s<br>", html.EscapeString(formatDateOnly(bill.DueDate)))
	}
	fmt.Fprintf(&b, `<span class="secondary">Currency: %s · Provider: %s</span>`,
		html.EscapeString(bill.Currency), html.EscapeString(bill.Provider))
	if bill.Category != "" {
		fmt.Fprintf(&b, `<br><span class="secondary">Category: %s</span>`, html.EscapeString(bill.Category))
	}
	if bill.GLAccount != "" {
		fmt.Fprintf(&b, `<br><span class="secondary">GL: %s</span>`, html.EscapeString(bill.GLAccount))
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
	for _, li := range bill.LineItems {
		fmt.Fprintf(&b,
			`      <tr><td>%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td></tr>
`,
			html.EscapeString(li.Description),
			html.EscapeString(formatQty(li.Quantity)),
			html.EscapeString(formatMoney(li.UnitPriceCents, bill.Currency)),
			html.EscapeString(fmt.Sprintf("%.2f%%", float64(li.TaxRateBps)/100)),
			html.EscapeString(formatMoney(li.AmountCents, bill.Currency)))
	}
	if len(bill.LineItems) == 0 {
		b.WriteString(`      <tr><td colspan="5" style="text-align:center;color:var(--muted);">No line items.</td></tr>
`)
	}
	b.WriteString(`    </tbody>
    <tfoot>
      <tr>
        <td colspan="4" class="num" style="color:var(--muted);">Subtotal</td>
        <td class="num">`)
	b.WriteString(html.EscapeString(formatMoney(bill.SubtotalCents, bill.Currency)))
	b.WriteString(`</td>
      </tr>
      <tr>
        <td colspan="4" class="num" style="color:var(--muted);">Input tax</td>
        <td class="num">`)
	b.WriteString(html.EscapeString(formatMoney(bill.TaxCents, bill.Currency)))
	b.WriteString(`</td>
      </tr>
      <tr class="total">
        <td colspan="4" class="num">Total</td>
        <td class="num">`)
	b.WriteString(html.EscapeString(formatMoney(bill.TotalCents, bill.Currency)))
	b.WriteString(`</td>
      </tr>`)
	if bill.AmountPaidCents > 0 {
		fmt.Fprintf(&b, `
      <tr>
        <td colspan="4" class="num" style="color:var(--muted);">Paid</td>
        <td class="num">%s</td>
      </tr>
      <tr>
        <td colspan="4" class="num" style="color:var(--muted);">Balance owed</td>
        <td class="num">%s</td>
      </tr>`,
			html.EscapeString(formatMoney(bill.AmountPaidCents, bill.Currency)),
			html.EscapeString(formatMoney(maxInt(0, bill.TotalCents-bill.AmountPaidCents), bill.Currency)))
	}
	b.WriteString(`
    </tfoot>
  </table>
`)

	if bill.ApprovedAt != "" || len(bill.Payments) > 0 || bill.VoidedAt != "" || bill.DisputedAt != "" {
		b.WriteString(`  <div class="workflow"><h2 style="margin:0 0 6px 0;font-size:11px;text-transform:uppercase;letter-spacing:0.05em;color:var(--muted);">Workflow + payments</h2><ul>`)
		if bill.ApprovedAt != "" {
			fmt.Fprintf(&b, `<li>Approved %s`, html.EscapeString(formatDateOnly(bill.ApprovedAt)))
			if bill.ApprovedBy != "" {
				fmt.Fprintf(&b, ` by %s`, html.EscapeString(bill.ApprovedBy))
			}
			b.WriteString(`</li>`)
		}
		if bill.ScheduledFor != "" {
			fmt.Fprintf(&b, `<li>Scheduled for %s (%s)</li>`,
				html.EscapeString(formatDateOnly(bill.ScheduledFor)),
				html.EscapeString(bill.ScheduledMethod))
		}
		if bill.DisputedAt != "" {
			fmt.Fprintf(&b, `<li>Disputed %s</li>`, html.EscapeString(formatDateOnly(bill.DisputedAt)))
		}
		if bill.VoidedAt != "" {
			fmt.Fprintf(&b, `<li>Voided %s</li>`, html.EscapeString(formatDateOnly(bill.VoidedAt)))
		}
		for _, p := range bill.Payments {
			fmt.Fprintf(&b, `<li>Paid %s on %s via %s</li>`,
				html.EscapeString(formatMoney(p.AmountCents, p.Currency)),
				html.EscapeString(formatDateOnly(p.SentAt)),
				html.EscapeString(p.Method))
		}
		b.WriteString(`</ul></div>
`)
	}

	if bill.Notes != "" {
		b.WriteString(`  <div class="notes">`)
		b.WriteString(html.EscapeString(bill.Notes))
		b.WriteString(`</div>
`)
	}

	b.WriteString(`</div>
</body>
</html>
`)
	return b.String()
}

// ─── Format helpers (duplicated from billing/print.go per proposal) ─

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
		return fmt.Sprintf("%s¥%d", sign, whole)
	default:
		return fmt.Sprintf("%s%s %d.%02d", sign, currency, whole, frac)
	}
}

func formatQty(q float64) string {
	s := fmt.Sprintf("%.2f", q)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

func formatDateOnly(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		if t2, err2 := time.Parse("2006-01-02", rfc3339); err2 == nil {
			return t2.Format("Jan 2, 2006")
		}
		return rfc3339
	}
	return t.UTC().Format("Jan 2, 2006")
}

func formatBillingAddress(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var addr struct {
		Line1, Line2, City, State, PostalCode, Country string
	}
	// JSON tags via reflection — same shape as billing's parser.
	var generic map[string]string
	if err := json.Unmarshal(raw, &generic); err != nil {
		return ""
	}
	addr.Line1 = generic["line1"]
	addr.Line2 = generic["line2"]
	addr.City = generic["city"]
	addr.State = generic["state"]
	addr.PostalCode = generic["postal_code"]
	addr.Country = generic["country"]
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
