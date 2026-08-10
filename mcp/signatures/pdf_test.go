package main

import (
	"bytes"
	"testing"

	"github.com/phpdave11/gofpdf"
)

func TestRenderCompletedPDFAddsFieldsAndCertificate(t *testing.T) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 14)
	pdf.Text(20, 30, "Agreement")
	var source bytes.Buffer
	if err := pdf.Output(&source); err != nil {
		t.Fatal(err)
	}
	env := &Envelope{PublicID: "env-test", Title: "Agreement", SourceName: "agreement.pdf", SourceSHA256: bytesHash(source.Bytes()), SentAt: nowUTC()}
	recipients := []Recipient{{ID: 1, Name: "Ada Lovelace", Role: "signer", Status: "signed", CompletedAt: nowUTC()}}
	values := []completionValue{{
		Field:     Field{ID: 1, FieldType: "signature", Page: 1, X: .55, Y: .72, Width: .3, Height: .08},
		ValueText: "Ada Lovelace", RecipientName: "Ada Lovelace", SignedAt: nowUTC(),
	}}
	out, err := renderCompletedPDF(source.Bytes(), env, recipients, values)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := pdfPageCount(out)
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 {
		t.Fatalf("page count = %d, want 2", pages)
	}
	if len(out) <= len(source.Bytes()) {
		t.Fatal("completed PDF did not grow")
	}
}
