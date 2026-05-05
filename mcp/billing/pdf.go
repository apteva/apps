package main

// PDF rendering — server-side via gofpdf. One page per invoice when
// line items fit; gofpdf paginates automatically when they don't.
//
// Produces a clean A4 layout: title block (invoice number + status),
// bill-to + meta two-column, line item table, totals, optional notes.
// Uses only the standard 14 fonts (Helvetica) so no embedding +
// no external font assets ship with the binary.
//
// Two surfaces use this:
//   - GET /invoices/{id}/pdf            → application/pdf bytes
//   - invoices_render_pdf MCP tool      → {pdf_base64} or, with
//                                          save_to_storage=true,
//                                          {file_id, signed_url}
//                                          via the storage app.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

// renderInvoicePDF builds the PDF document and returns its bytes.
// customer is optional; when nil, bill-to falls back to "Customer #<id>".
func renderInvoicePDF(inv *Invoice, customer *Customer) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(20, 20, 20)
	pdf.SetAutoPageBreak(true, 20)
	pdf.AddPage()
	pdf.SetTitle(invoiceTitle(inv), false)
	pdf.SetCreator("Apteva billing", false)

	pageWidth, _ := pdf.GetPageSize()
	usableWidth := pageWidth - 40 // page minus L+R margins

	// ── Header ──
	pdf.SetFont("Helvetica", "B", 22)
	pdf.SetTextColor(20, 20, 20)
	pdf.CellFormat(usableWidth/2, 10, "Invoice", "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(100, 100, 100)
	pdf.CellFormat(usableWidth/2, 10, invoiceTitle(inv), "", 0, "R", false, 0, "")
	pdf.Ln(10)

	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(120, 120, 120)
	pdf.Cell(usableWidth/2, 5, "Issued by your Apteva project")
	pdf.SetX(20 + usableWidth/2)
	pdf.CellFormat(usableWidth/2, 5, statusLabel(inv.Status), "", 0, "R", false, 0, "")
	pdf.Ln(12)

	// Thin rule under the header.
	pdf.SetDrawColor(200, 200, 200)
	pdf.Line(20, pdf.GetY(), pageWidth-20, pdf.GetY())
	pdf.Ln(8)

	// ── Bill-to / Details two-column ──
	colW := usableWidth / 2
	yStart := pdf.GetY()

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(120, 120, 120)
	pdf.CellFormat(colW, 5, "BILL TO", "", 0, "L", false, 0, "")
	pdf.SetX(20 + colW)
	pdf.CellFormat(colW, 5, "DETAILS", "", 0, "L", false, 0, "")
	pdf.Ln(6)

	// Render two columns side by side. fpdf doesn't have native
	// columns, so we measure both blocks and advance to the taller.
	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(40, 40, 40)
	leftLines := buildBillToLines(inv, customer)
	rightLines := buildDetailsLines(inv)

	maxLines := len(leftLines)
	if len(rightLines) > maxLines {
		maxLines = len(rightLines)
	}
	for i := 0; i < maxLines; i++ {
		pdf.SetXY(20, yStart+6+float64(i)*5)
		if i < len(leftLines) {
			if i == 0 {
				pdf.SetFont("Helvetica", "B", 10)
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

	// ── Line item table ──
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(120, 120, 120)
	pdf.SetFillColor(245, 245, 245)
	descW := usableWidth - 100 // 4 fixed cols of 25mm
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
	if len(inv.LineItems) == 0 {
		pdf.SetTextColor(160, 160, 160)
		pdf.CellFormat(usableWidth, 8, "No line items.", "", 0, "C", false, 0, "")
		pdf.Ln(8)
	} else {
		for _, li := range inv.LineItems {
			pdf.CellFormat(descW, 6, li.Description, "", 0, "L", false, 0, "")
			pdf.CellFormat(20, 6, formatQty(li.Quantity), "", 0, "R", false, 0, "")
			pdf.CellFormat(30, 6, formatMoney(li.UnitPriceCents, inv.Currency), "", 0, "R", false, 0, "")
			pdf.CellFormat(20, 6, fmt.Sprintf("%.2f%%", float64(li.TaxRateBps)/100), "", 0, "R", false, 0, "")
			pdf.CellFormat(30, 6, formatMoney(li.AmountCents, inv.Currency), "", 0, "R", false, 0, "")
			pdf.Ln(6)
		}
	}

	// ── Totals box on the right ──
	pdf.Ln(4)
	pdf.SetDrawColor(220, 220, 220)
	pdf.Line(20, pdf.GetY(), pageWidth-20, pdf.GetY())
	pdf.Ln(2)

	totalsX := pageWidth - 20 - 70 // right-aligned totals block, 70mm wide
	totalsLabelW := 40.0
	totalsValueW := 30.0
	drawTotalRow := func(label, value string, bold, separator bool) {
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
		if separator {
			pdf.SetX(totalsX)
			pdf.SetDrawColor(160, 160, 160)
			pdf.Line(totalsX, pdf.GetY(), pageWidth-20, pdf.GetY())
			pdf.Ln(1)
		}
	}
	drawTotalRow("Subtotal", formatMoney(inv.SubtotalCents, inv.Currency), false, false)
	drawTotalRow("Tax", formatMoney(inv.TaxCents, inv.Currency), false, true)
	drawTotalRow("Total", formatMoney(inv.TotalCents, inv.Currency), true, false)

	if inv.AmountPaidCents > 0 {
		drawTotalRow("Paid", formatMoney(inv.AmountPaidCents, inv.Currency), false, false)
		balance := inv.TotalCents - inv.AmountPaidCents
		if balance < 0 {
			balance = 0
		}
		drawTotalRow("Balance due", formatMoney(balance, inv.Currency), true, false)
	}

	// ── Notes ──
	if inv.Notes != "" {
		pdf.Ln(8)
		pdf.SetDrawColor(220, 220, 220)
		pdf.Line(20, pdf.GetY(), pageWidth-20, pdf.GetY())
		pdf.Ln(3)
		pdf.SetFont("Helvetica", "B", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(usableWidth, 5, "NOTES", "", 0, "L", false, 0, "")
		pdf.Ln(5)
		pdf.SetFont("Helvetica", "", 9)
		pdf.SetTextColor(80, 80, 80)
		pdf.MultiCell(usableWidth, 4.5, inv.Notes, "", "L", false)
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf output: %w", err)
	}
	return buf.Bytes(), nil
}

// buildBillToLines returns the customer block as a slice of lines
// for renderInvoicePDF's two-column layout.
func buildBillToLines(inv *Invoice, customer *Customer) []string {
	if customer == nil {
		return []string{fmt.Sprintf("Customer #%d", inv.CustomerID)}
	}
	out := []string{customer.Name}
	if customer.Email != "" {
		out = append(out, customer.Email)
	}
	if addr := formatBillingAddress(customer.BillingAddress); addr != "" {
		for _, ln := range strings.Split(addr, "\n") {
			out = append(out, ln)
		}
	}
	return out
}

func buildDetailsLines(inv *Invoice) []string {
	var out []string
	if inv.FinalizedAt != "" {
		out = append(out, "Issued: "+formatDateOnly(inv.FinalizedAt))
	} else if inv.CreatedAt != "" {
		out = append(out, "Created: "+formatDateOnly(inv.CreatedAt))
	}
	if inv.DueDate != "" {
		out = append(out, "Due: "+formatDateOnly(inv.DueDate))
	}
	out = append(out, fmt.Sprintf("Currency: %s", inv.Currency))
	out = append(out, fmt.Sprintf("Provider: %s", inv.Provider))
	return out
}

func invoiceTitle(inv *Invoice) string {
	if inv.Number != "" {
		return inv.Number
	}
	return fmt.Sprintf("Draft #%d", inv.ID)
}

func statusLabel(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// suggestPDFFilename produces a safe, filesystem-friendly filename
// for the rendered invoice. "INV-2026-0042.pdf" or
// "draft-12.pdf" for unfinalized invoices.
func suggestPDFFilename(inv *Invoice) string {
	base := inv.Number
	if base == "" {
		base = fmt.Sprintf("draft-%d", inv.ID)
	}
	// Replace any path-unsafe chars defensively.
	out := make([]byte, 0, len(base))
	for i := 0; i < len(base); i++ {
		c := base[i]
		switch {
		case c == '/' || c == '\\' || c == ':' || c < 0x20:
			out = append(out, '_')
		default:
			out = append(out, c)
		}
	}
	return string(out) + ".pdf"
}

// jsonDecodeRaw is a tiny wrapper kept here so print.go's
// formatBillingAddress doesn't need to import encoding/json directly.
func jsonDecodeRaw(raw []byte, dst any) error {
	return json.Unmarshal(raw, dst)
}

// _ = time so go vet stays quiet if a future edit of pdf.go drops the
// time import without noticing pdf.go itself doesn't use it. We could
// remove this once buildDetailsLines starts inlining timestamps.
var _ = time.RFC3339
