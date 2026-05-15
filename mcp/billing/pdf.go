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
// issuer is optional; when nil or not configured, the BILL FROM block
// shows a single placeholder line so the layout doesn't collapse.
func renderInvoicePDF(inv *Invoice, customer *Customer, issuer *Issuer) ([]byte, error) {
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
	pdf.SetX(20 + usableWidth/2)
	pdf.CellFormat(usableWidth/2, 5, statusLabel(inv.Status), "", 0, "R", false, 0, "")
	pdf.Ln(8)

	// Thin rule under the header.
	pdf.SetDrawColor(200, 200, 200)
	pdf.Line(20, pdf.GetY(), pageWidth-20, pdf.GetY())
	pdf.Ln(6)

	// ── BILL FROM / BILL TO two-column ──
	colW := usableWidth / 2
	yStart := pdf.GetY()

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(120, 120, 120)
	pdf.CellFormat(colW, 5, "BILL FROM", "", 0, "L", false, 0, "")
	pdf.SetX(20 + colW)
	pdf.CellFormat(colW, 5, "BILL TO", "", 0, "L", false, 0, "")
	pdf.Ln(6)

	leftLines := buildIssuerLines(issuer)
	rightLines := buildBillToLines(inv, customer)

	maxLines := len(leftLines)
	if len(rightLines) > maxLines {
		maxLines = len(rightLines)
	}
	for i := 0; i < maxLines; i++ {
		// LEFT — issuer
		if i < len(leftLines) {
			pdf.SetXY(20, yStart+6+float64(i)*5)
			if i == 0 {
				pdf.SetFont("Helvetica", "B", 10)
				pdf.SetTextColor(40, 40, 40)
			} else {
				pdf.SetFont("Helvetica", "", 9)
				pdf.SetTextColor(110, 110, 110)
			}
			pdf.CellFormat(colW, 5, leftLines[i], "", 0, "L", false, 0, "")
		}
		// RIGHT — bill-to
		if i < len(rightLines) {
			pdf.SetXY(20+colW, yStart+6+float64(i)*5)
			if i == 0 {
				pdf.SetFont("Helvetica", "B", 10)
				pdf.SetTextColor(40, 40, 40)
			} else {
				pdf.SetFont("Helvetica", "", 9)
				pdf.SetTextColor(110, 110, 110)
			}
			pdf.CellFormat(colW, 5, rightLines[i], "", 0, "L", false, 0, "")
		}
	}
	pdf.SetXY(20, yStart+6+float64(maxLines)*5+4)

	// Inline details: Issued · Due · Currency · Provider.
	if details := buildDetailsInline(inv); details != "" {
		pdf.SetFont("Helvetica", "", 9)
		pdf.SetTextColor(110, 110, 110)
		pdf.CellFormat(usableWidth, 5, details, "", 0, "L", false, 0, "")
		pdf.Ln(6)
	}

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

	// ── PAY BY BANK TRANSFER ──
	drawBankBlock(pdf, issuer, pageWidth, usableWidth)

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

	// ── Footer text from issuer settings ──
	if issuer != nil && issuer.FooterText != "" {
		pdf.Ln(6)
		pdf.SetFont("Helvetica", "I", 8)
		pdf.SetTextColor(140, 140, 140)
		pdf.MultiCell(usableWidth, 4, issuer.FooterText, "", "C", false)
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
	if tids := formatTaxIDs(customer.TaxIDs); tids != "" {
		out = append(out, tids)
	}
	return out
}

// buildIssuerLines is the BILL FROM analogue of buildBillToLines.
// Falls back to a single placeholder line when nothing's configured —
// keeps the column from collapsing and signals to the user that the
// Settings tab still needs filling in.
func buildIssuerLines(issuer *Issuer) []string {
	if issuer == nil || !issuer.Configured || issuer.DisplayName == "" {
		return []string{"Issued by your Apteva project"}
	}
	out := []string{issuer.DisplayName}
	if issuer.LegalName != "" && issuer.LegalName != issuer.DisplayName {
		out = append(out, issuer.LegalName)
	}
	if addr := formatBillingAddress(issuer.Address); addr != "" {
		for _, ln := range strings.Split(addr, "\n") {
			out = append(out, ln)
		}
	}
	if tids := formatTaxIDs(issuer.TaxIDs); tids != "" {
		out = append(out, tids)
	}
	if issuer.Email != "" {
		out = append(out, issuer.Email)
	}
	return out
}

// buildDetailsInline collapses the old DETAILS column into a single
// dot-separated row that sits under the BILL FROM / BILL TO blocks.
func buildDetailsInline(inv *Invoice) string {
	var parts []string
	if inv.FinalizedAt != "" {
		parts = append(parts, "Issued: "+formatDateOnly(inv.FinalizedAt))
	} else if inv.CreatedAt != "" {
		parts = append(parts, "Created: "+formatDateOnly(inv.CreatedAt))
	}
	if inv.DueDate != "" {
		parts = append(parts, "Due: "+formatDateOnly(inv.DueDate))
	}
	parts = append(parts, "Currency: "+inv.Currency)
	if inv.Provider != "" && inv.Provider != "local" {
		parts = append(parts, "Provider: "+inv.Provider)
	}
	return strings.Join(parts, " · ")
}

// formatTaxIDs renders a JSON array of {type,value} as a single line
// with friendly type labels. Returns "" when there are no usable IDs.
func formatTaxIDs(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var arr []struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	if err := jsonDecodeRaw(raw, &arr); err != nil || len(arr) == 0 {
		return ""
	}
	parts := make([]string, 0, len(arr))
	for _, t := range arr {
		if t.Type == "" || t.Value == "" {
			continue
		}
		label := strings.ToUpper(t.Type)
		switch strings.ToLower(t.Type) {
		case "vat":
			label = "VAT"
		case "ein":
			label = "EIN"
		case "gst":
			label = "GST"
		case "abn":
			label = "ABN"
		case "company_reg":
			label = "Reg"
		case "siret":
			label = "SIRET"
		}
		parts = append(parts, label+" "+t.Value)
	}
	return strings.Join(parts, " · ")
}

// bankInfo + parseBank decode the issuer's bank JSON. Stays struct-
// internal because both pdf.go and print.go need the same fields.
type bankInfo struct {
	IBAN        string
	BIC         string
	BankName    string
	BankCode    string
	Beneficiary string
}

func parseBank(raw []byte) bankInfo {
	if len(raw) == 0 {
		return bankInfo{}
	}
	var m struct {
		IBAN        string `json:"iban"`
		BIC         string `json:"bic"`
		BankName    string `json:"bank_name"`
		BankCode    string `json:"bank_code"`
		Beneficiary string `json:"beneficiary"`
	}
	if err := jsonDecodeRaw(raw, &m); err != nil {
		return bankInfo{}
	}
	return bankInfo{
		IBAN:        m.IBAN,
		BIC:         m.BIC,
		BankName:    m.BankName,
		BankCode:    m.BankCode,
		Beneficiary: m.Beneficiary,
	}
}

// formatIBAN groups the IBAN into 4-char blocks: "EE2477007710073329 32"
// → "EE24 7700 7710 0733 2932". Cosmetic only.
func formatIBAN(s string) string {
	s = strings.ToUpper(strings.ReplaceAll(s, " ", ""))
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// drawBankBlock paints the "PAY BY BANK TRANSFER" section between
// the totals box and the notes block. Skipped silently when the
// issuer has no IBAN configured — most users on Stripe never need this.
func drawBankBlock(pdf *fpdf.Fpdf, issuer *Issuer, pageWidth, usableWidth float64) {
	if issuer == nil || !issuer.Configured {
		return
	}
	bank := parseBank(issuer.Bank)
	if bank.IBAN == "" {
		return
	}
	pdf.Ln(6)
	pdf.SetDrawColor(220, 220, 220)
	pdf.Line(20, pdf.GetY(), pageWidth-20, pdf.GetY())
	pdf.Ln(3)
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(120, 120, 120)
	pdf.CellFormat(usableWidth, 5, "PAY BY BANK TRANSFER", "", 0, "L", false, 0, "")
	pdf.Ln(5)

	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(60, 60, 60)
	beneficiary := bank.Beneficiary
	if beneficiary == "" {
		beneficiary = issuer.LegalName
	}
	if beneficiary == "" {
		beneficiary = issuer.DisplayName
	}
	if beneficiary != "" {
		pdf.CellFormat(usableWidth, 4.5, "Beneficiary: "+beneficiary, "", 0, "L", false, 0, "")
		pdf.Ln(4.5)
	}
	pdf.CellFormat(usableWidth, 4.5, "IBAN: "+formatIBAN(bank.IBAN), "", 0, "L", false, 0, "")
	pdf.Ln(4.5)
	if bank.BIC != "" {
		line := "BIC: " + bank.BIC
		if bank.BankCode != "" {
			line += " · Bank code " + bank.BankCode
		}
		pdf.CellFormat(usableWidth, 4.5, line, "", 0, "L", false, 0, "")
		pdf.Ln(4.5)
	} else if bank.BankCode != "" {
		pdf.CellFormat(usableWidth, 4.5, "Bank code: "+bank.BankCode, "", 0, "L", false, 0, "")
		pdf.Ln(4.5)
	}
	if bank.BankName != "" {
		pdf.CellFormat(usableWidth, 4.5, "Bank: "+bank.BankName, "", 0, "L", false, 0, "")
		pdf.Ln(4.5)
	}
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
