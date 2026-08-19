package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/phpdave11/gofpdf"
)

func drawnSignatureFixture(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 240, 60))
	for x := 20; x < 220; x++ {
		img.Set(x, 30+int(8*float64(x%20)/20), color.RGBA{23, 32, 51, 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return drawnSignaturePrefix + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestRenderCompletedPDFStampsDrawnSignature(t *testing.T) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 14)
	pdf.Text(20, 30, "Agreement")
	var source bytes.Buffer
	if err := pdf.Output(&source); err != nil {
		t.Fatal(err)
	}
	env := &Envelope{PublicID: "env-drawn", Title: "Agreement", SourceName: "agreement.pdf", SourceSHA256: bytesHash(source.Bytes()), SentAt: nowUTC()}
	recipients := []Recipient{{ID: 1, Name: "Ada Lovelace", Role: "signer", Status: "signed", CompletedAt: nowUTC()}}
	values := []completionValue{{
		Field:     Field{ID: 1, FieldType: "signature", Page: 1, X: .55, Y: .72, Width: .3, Height: .08},
		ValueText: drawnSignatureFixture(t), RecipientName: "Ada Lovelace", SignedAt: nowUTC(),
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

func TestValidateDrawnSignature(t *testing.T) {
	if err := validateDrawnSignature(drawnSignatureFixture(t)); err != nil {
		t.Fatalf("valid pad output rejected: %v", err)
	}
	if err := validateDrawnSignature(drawnSignaturePrefix + "bm90LWEtcG5n"); err == nil {
		t.Fatal("non-PNG payload accepted")
	}
	if err := validateDrawnSignature("data:image/svg+xml;base64,AAAA"); err == nil {
		t.Fatal("non-PNG data URL accepted")
	}
}

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
