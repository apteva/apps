package main

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"testing"
)

// The page has no drawn content: the only visible text is the appearance of
// a filled AcroForm widget. Rendering just the page therefore yields white.
func filledInvoiceFormPDF() []byte {
	appearance := "BT /F1 20 Tf 0 g 5 10 Td (100.00 GBP) Tj ET\n"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R /AcroForm 6 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Resources << /Font << /F1 5 0 R >> >> /Annots [4 0 R] >>",
		"<< /Type /Annot /Subtype /Widget /FT /Tx /T (Total) /V (100.00 GBP) /Rect [40 80 220 120] /P 3 0 R /F 4 /DA (/F1 20 Tf 0 g) /AP << /N 7 0 R >> >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Fields [4 0 R] /DR << /Font << /F1 5 0 R >> >> /DA (/F1 20 Tf 0 g) /NeedAppearances false >>",
		fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 180 40] /Resources << /Font << /F1 5 0 R >> >> /Length %d >>\nstream\n%sendstream", len(appearance), appearance),
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, obj := range objects {
		offsets[i+1] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return pdf.Bytes()
}

func TestRenderPDFIncludesFilledFormValues(t *testing.T) {
	// Reusing the instance also verifies that encoding finishes before the
	// bitmap is released and that subsequent renders still work.
	for run := 0; run < 3; run++ {
		pages, err := renderPDFToJPEGs(filledInvoiceFormPDF(), 72, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(pages) != 1 {
			t.Fatalf("pages=%d", len(pages))
		}
		img, err := jpeg.Decode(bytes.NewReader(pages[0]))
		if err != nil {
			t.Fatal(err)
		}
		darkPixels := 0
		for y := 80; y < 120; y++ {
			for x := 40; x < 220; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				if r < 0x8000 && g < 0x8000 && b < 0x8000 {
					darkPixels++
				}
			}
		}
		if darkPixels < 100 {
			t.Fatalf("run %d: filled total absent from rendered page (%d dark pixels)", run, darkPixels)
		}
	}
}
