package main

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phpdave11/gofpdf"
	"software.sslmate.com/src/go-pkcs12"
)

func sealedFixture(t *testing.T, id *sealIdentity) []byte {
	t.Helper()
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 14)
	pdf.Text(20, 30, "Model release")
	var source bytes.Buffer
	if err := pdf.Output(&source); err != nil {
		t.Fatal(err)
	}
	env := &Envelope{PublicID: "env-seal", Title: "Model release", SourceName: "release.pdf", SourceSHA256: bytesHash(source.Bytes()), SentAt: nowUTC()}
	recipients := []Recipient{{ID: 1, Name: "Ada Lovelace", Role: "signer", Status: "signed", CompletedAt: nowUTC()}}
	values := []completionValue{{
		Field:     Field{ID: 1, FieldType: "signature", Page: 1, X: .55, Y: .72, Width: .3, Height: .08},
		ValueText: drawnSignatureFixture(t), RecipientName: "Ada Lovelace", SignedAt: nowUTC(),
	}}
	completed, err := renderCompletedPDF(source.Bytes(), env, recipients, values, id)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealPDF(completed, id, sealDetails{SignerName: "Apteva Signatures", Reason: "Signed envelope env-seal"})
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestSelfSignedSealIsCreatedOnce(t *testing.T) {
	db := testDB(t)
	first, err := selfSignedSeal(db)
	if err != nil {
		t.Fatal(err)
	}
	second, err := selfSignedSeal(db)
	if err != nil {
		t.Fatal(err)
	}
	if first.Source != sealSourceSelfSigned || !bytes.Equal(first.Certificate.Raw, second.Certificate.Raw) {
		t.Fatal("self-signed seal was not reused")
	}
}

func TestSealedPDFVerifiesAndDetectsTampering(t *testing.T) {
	id, err := selfSignedSeal(testDB(t))
	if err != nil {
		t.Fatal(err)
	}
	sealed := sealedFixture(t, id)
	got := verifySealedPDF(sealed, id)
	if !got.Sealed || !got.Intact || !got.CoversWholeFile || !got.MatchesThisSeal {
		t.Fatalf("fresh seal not verified: %+v", got)
	}
	if pages, err := pdfPageCount(sealed); err != nil || pages != 2 {
		t.Fatalf("sealed page count = %d, %v", pages, err)
	}

	// Flip one byte inside the signed content (the page text).
	idx := bytes.Index(sealed, []byte("Model release"))
	if idx < 0 {
		// Content stream is compressed; flip a byte early in the file instead.
		idx = 200
	}
	tampered := append([]byte(nil), sealed...)
	tampered[idx] ^= 0x01
	if got := verifySealedPDF(tampered, id); got.Intact {
		t.Fatalf("byte flip not detected: %+v", got)
	}

	// An incremental update appended after the seal, as PDF editors save.
	appended := append(append([]byte(nil), sealed...), []byte("\n1 0 obj\n<< /Evil true >>\nendobj\n%%EOF\n")...)
	if got := verifySealedPDF(appended, id); got.Intact || got.CoversWholeFile {
		t.Fatalf("appended update not detected: %+v", got)
	}

	if got := verifySealedPDF([]byte("%PDF-1.4\n%%EOF"), id); got.Sealed {
		t.Fatal("unsigned PDF reported as sealed")
	}
}

// Independent check with poppler's pdfsig when it is installed.
func TestSealedPDFPassesPdfsig(t *testing.T) {
	bin, err := exec.LookPath("pdfsig")
	if err != nil {
		t.Skip("pdfsig not installed")
	}
	id, err := selfSignedSeal(testDB(t))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sealed.pdf")
	if err := os.WriteFile(path, sealedFixture(t, id), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command(bin, path).CombinedOutput()
	text := string(out)
	if !strings.Contains(text, "Signature Validation: Signature is Valid.") || !strings.Contains(text, "Signature Type: adbe.pkcs7.detached") || !strings.Contains(text, "Total document signed") {
		t.Fatalf("pdfsig did not validate the seal:\n%s", text)
	}
}

func TestParseSealBundleFormats(t *testing.T) {
	generated, err := selfSignedSeal(testDB(t))
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(generated.Signer)
	if err != nil {
		t.Fatal(err)
	}
	pemBundle := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: generated.Certificate.Raw})) +
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	fromPEM, err := parseSealBundle(pemBundle, "")
	if err != nil || fromPEM.Source != sealSourceConfigured {
		t.Fatalf("PEM bundle: %v", err)
	}
	p12, err := pkcs12.Modern.Encode(generated.Signer, generated.Certificate, nil, "secret")
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(p12)
	fromP12, err := parseSealBundle(encoded, "secret")
	if err != nil || !bytes.Equal(fromP12.Certificate.Raw, generated.Certificate.Raw) {
		t.Fatalf("PKCS#12 bundle: %v", err)
	}
	if _, err := parseSealBundle(encoded, "wrong"); err == nil {
		t.Fatal("wrong PKCS#12 password accepted")
	}
	if _, err := parseSealBundle("not a certificate", ""); err == nil {
		t.Fatal("garbage bundle accepted")
	}
}

// Network test against a real RFC 3161 server:
// SIGNATURES_TSA_URL=http://timestamp.digicert.com go test -run TSA
func TestSealWithTimestampAuthority(t *testing.T) {
	tsa := os.Getenv("SIGNATURES_TSA_URL")
	if tsa == "" {
		t.Skip("SIGNATURES_TSA_URL not set")
	}
	id, err := selfSignedSeal(testDB(t))
	if err != nil {
		t.Fatal(err)
	}
	unsigned, err := renderCompletedPDF(sealedFixtureSource(t), &Envelope{PublicID: "env-tsa", Title: "TSA"}, nil, nil, id)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealPDF(unsigned, id, sealDetails{SignerName: "Apteva Signatures", TSAURL: tsa})
	if err != nil {
		t.Fatal(err)
	}
	got := verifySealedPDF(sealed, id)
	if !got.Intact || got.TimestampStatus != "valid" {
		t.Fatalf("timestamped seal: %+v", got)
	}
	if out := os.Getenv("SIGNATURES_SEAL_OUT"); out != "" {
		_ = os.WriteFile(out, sealed, 0o600)
	}
}

func sealedFixtureSource(t *testing.T) []byte {
	t.Helper()
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 14)
	pdf.Text(20, 30, "Timestamped")
	var source bytes.Buffer
	if err := pdf.Output(&source); err != nil {
		t.Fatal(err)
	}
	return source.Bytes()
}
