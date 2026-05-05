package main

// Tier 1 — pure rendering tests. No DB.

import (
	"encoding/json"
	"strings"
	"testing"
)

func sampleBill() *Bill {
	return &Bill{
		ID:                  42,
		VendorInvoiceNumber: "VENDOR-INV-2026-0001",
		VendorInvoiceDate:   "2026-04-15T00:00:00Z",
		VendorID:            7,
		Provider:            "local",
		Status:              "approved",
		Currency:            "USD",
		SubtotalCents:       100000,
		TaxCents:            10000,
		TotalCents:          110000,
		AmountPaidCents:     0,
		DueDate:             "2026-05-15T00:00:00Z",
		Notes:               "Net 30. Subscription renewal.",
		Category:            "software",
		ApprovedAt:          "2026-04-20T09:30:00Z",
		ApprovedBy:          "human:42",
		LineItems: []BillLineItem{
			{Position: 0, Description: "Annual licence", Quantity: 1, UnitPriceCents: 100000, AmountCents: 100000, TaxRateBps: 1000},
		},
	}
}

func sampleVendor() *Vendor {
	addr, _ := json.Marshal(map[string]any{
		"line1": "1 Vendor St", "city": "Seattle",
		"state": "WA", "postal_code": "98101", "country": "USA",
	})
	return &Vendor{
		ID: 7, Name: "AWS", Email: "billing@aws.amazon.com",
		BillingAddress: addr,
	}
}

func TestRenderBillPDF_ProducesValidPDF(t *testing.T) {
	bill := sampleBill()
	bytes, err := renderBillPDF(bill, sampleVendor())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(bytes[:4]), "%PDF") {
		t.Errorf("expected %%PDF magic, got %q", string(bytes[:8]))
	}
	if !strings.Contains(string(bytes[len(bytes)-8:]), "%%EOF") {
		t.Error("missing PDF EOF marker in tail")
	}
	if len(bytes) < 500 {
		t.Errorf("PDF suspiciously small: %d bytes", len(bytes))
	}
}

func TestRenderBillPDF_HandlesMissingVendor(t *testing.T) {
	bytes, err := renderBillPDF(sampleBill(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes) < 500 {
		t.Error("PDF too small without vendor")
	}
}

func TestRenderBillHTML_ContainsKeyFields(t *testing.T) {
	got := renderBillHTML(sampleBill(), sampleVendor())
	for _, frag := range []string{
		"VENDOR-INV-2026-0001",
		"AWS",
		"billing@aws.amazon.com",
		"Annual licence",
		"$1000.00", // 100000c subtotal
		"$1100.00", // 110000c total
		"VOUCHER",
		"Approved",
		"software",
		"Net 30",
	} {
		if !strings.Contains(got, frag) {
			t.Errorf("HTML missing %q", frag)
		}
	}
	if !strings.Contains(got, "@media print") {
		t.Error("expected @media print")
	}
	if !strings.HasPrefix(got, "<!doctype html>") {
		t.Error("expected <!doctype html> prefix")
	}
}

func TestRenderBillHTML_EscapesUserInput(t *testing.T) {
	bill := sampleBill()
	bill.Notes = `<script>alert('xss')</script>`
	v := sampleVendor()
	v.Name = `Mallory <img src=x onerror=alert(1)>`

	got := renderBillHTML(bill, v)
	for _, d := range []string{"<script>alert", "<img src=x"} {
		if strings.Contains(got, d) {
			t.Errorf("renderBillHTML allowed unescaped %q", d)
		}
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Error("expected escaped form &lt;script&gt;")
	}
}

func TestFormatMoney(t *testing.T) {
	cases := map[string]struct {
		cents    int64
		currency string
		want     string
	}{
		"USD":      {150000, "USD", "$1500.00"},
		"EUR":      {99, "EUR", "€0.99"},
		"GBP":      {1234, "GBP", "£12.34"},
		"JPY":      {15000, "JPY", "¥150"},
		"unknown":  {2500, "XYZ", "XYZ 25.00"},
		"negative": {-100, "USD", "-$1.00"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := formatMoney(c.cents, c.currency); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSuggestPDFFilename(t *testing.T) {
	got := suggestPDFFilename(sampleBill(), sampleVendor())
	if !strings.HasSuffix(got, ".pdf") {
		t.Errorf("filename %q missing .pdf suffix", got)
	}
	if !strings.Contains(got, "AWS") || !strings.Contains(got, "VENDOR-INV") {
		t.Errorf("filename %q should include vendor + invoice number", got)
	}
	// Sanitiser strips path separators.
	bill := sampleBill()
	bill.VendorInvoiceNumber = "../../etc/passwd"
	got2 := suggestPDFFilename(bill, sampleVendor())
	if strings.ContainsAny(got2, "/\\:") {
		t.Errorf("filename %q contains path separators", got2)
	}
}

func TestFormatBillingAddress(t *testing.T) {
	good := `{"line1":"1 Market","city":"SF","state":"CA","postal_code":"94105","country":"USA"}`
	want := "1 Market\nSF CA 94105\nUSA"
	if got := formatBillingAddress([]byte(good)); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := formatBillingAddress([]byte("garbage")); got != "" {
		t.Errorf("garbage JSON should yield empty, got %q", got)
	}
	if got := formatBillingAddress(nil); got != "" {
		t.Errorf("nil should yield empty, got %q", got)
	}
}
