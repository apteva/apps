package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/digitorus/pdf"
	"github.com/digitorus/pdfsign/sign"
	"github.com/digitorus/pdfsign/verify"
	"software.sslmate.com/src/go-pkcs12"
)

// The completed PDF carries a certification signature (DocMDP P=1):
// Acrobat shows the "Certified by …" banner, and any byte change or
// later incremental update invalidates it. The seal belongs to this
// install, like DocuSign's platform seal; signers never hold keys.
//
// Identity comes from config (a PKCS#12 or PEM bundle — use a
// certificate on Adobe's AATL so Acrobat trusts it with no setup), or
// falls back to a self-signed certificate generated once per install.
// The self-signed seal is just as tamper-evident, but Acrobat reports
// the signer's identity as unknown until the certificate is trusted.

const (
	sealSourceConfigured = "configured"
	sealSourceSelfSigned = "self_signed"
	sealSelfSignedCN     = "Apteva Signatures Document Seal"
)

var (
	oidKPDocumentSigning   = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 36}
	oidMSDocumentSigning   = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 10, 3, 12}
	oidAdobeAuthenticDocTr = asn1.ObjectIdentifier{1, 2, 840, 113583, 1, 1, 5}
)

type sealIdentity struct {
	Signer      crypto.Signer
	Certificate *x509.Certificate
	Chain       []*x509.Certificate
	Source      string
}

// SealCertificateInfo is the public view of the seal identity.
type SealCertificateInfo struct {
	Source            string `json:"source"`
	Subject           string `json:"subject"`
	Issuer            string `json:"issuer"`
	SerialNumber      string `json:"serial_number"`
	SHA256Fingerprint string `json:"sha256_fingerprint"`
	NotBefore         string `json:"not_before"`
	NotAfter          string `json:"not_after"`
	TimestampURL      string `json:"timestamp_url,omitempty"`
	CertificatePEM    string `json:"certificate_pem"`
	TrustNote         string `json:"trust_note"`
}

func (s *sealIdentity) info(ctx *sdk.AppCtx) SealCertificateInfo {
	note := "Issued by a configured certificate authority. Acrobat trusts it automatically when the issuing CA is on the Adobe Approved Trust List (AATL)."
	if s.Source == sealSourceSelfSigned {
		note = "Self-signed install certificate. The seal detects any change to the PDF, but Acrobat shows the signer identity as unknown until this certificate is added to Trusted Certificates (Preferences > Signatures > Identities & Trusted Certificates) or an AATL certificate is configured."
	}
	return SealCertificateInfo{
		Source:            s.Source,
		Subject:           s.Certificate.Subject.String(),
		Issuer:            s.Certificate.Issuer.String(),
		SerialNumber:      s.Certificate.SerialNumber.Text(16),
		SHA256Fingerprint: certFingerprint(s.Certificate),
		NotBefore:         s.Certificate.NotBefore.UTC().Format(time.RFC3339),
		NotAfter:          s.Certificate.NotAfter.UTC().Format(time.RFC3339),
		TimestampURL:      timestampURL(ctx),
		CertificatePEM:    string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate.Raw})),
		TrustNote:         note,
	}
}

func certFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func timestampURL(ctx *sdk.AppCtx) string {
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(ctx.Config().Get("timestamp_url"))
}

func loadSealIdentity(ctx *sdk.AppCtx) (*sealIdentity, error) {
	if ctx == nil {
		return nil, errors.New("app is not mounted")
	}
	if bundle := strings.TrimSpace(ctx.Config().Get("seal_certificate")); bundle != "" {
		id, err := parseSealBundle(bundle, ctx.Config().Get("seal_certificate_password"))
		if err != nil {
			return nil, fmt.Errorf("seal_certificate: %w", err)
		}
		return id, nil
	}
	return selfSignedSeal(ctx.AppDB())
}

// parseSealBundle accepts a base64 PKCS#12 (.p12/.pfx) file or PEM text
// holding the private key and certificate chain (leaf first or not).
func parseSealBundle(bundle, password string) (*sealIdentity, error) {
	if strings.Contains(bundle, "-----BEGIN") {
		return parseSealPEM([]byte(bundle))
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(bundle), ""))
	if err != nil {
		return nil, errors.New("expected PEM text or a base64-encoded PKCS#12 file")
	}
	key, cert, chain, err := pkcs12.DecodeChain(raw, password)
	if err != nil {
		return nil, fmt.Errorf("decode PKCS#12: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("PKCS#12 private key cannot sign")
	}
	return newSealIdentity(signer, cert, chain, sealSourceConfigured)
}

func parseSealPEM(data []byte) (*sealIdentity, error) {
	var signer crypto.Signer
	var certs []*x509.Certificate
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, err
			}
			certs = append(certs, cert)
		case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
			key, err := parsePrivateKey(block)
			if err != nil {
				return nil, err
			}
			signer = key
		}
	}
	if signer == nil || len(certs) == 0 {
		return nil, errors.New("PEM bundle needs a private key and at least one certificate")
	}
	var leaf *x509.Certificate
	var chain []*x509.Certificate
	for _, cert := range certs {
		if leaf == nil && publicKeysEqual(cert.PublicKey, signer.Public()) {
			leaf = cert
			continue
		}
		chain = append(chain, cert)
	}
	if leaf == nil {
		return nil, errors.New("no certificate in the PEM bundle matches the private key")
	}
	return newSealIdentity(signer, leaf, chain, sealSourceConfigured)
}

func parsePrivateKey(block *pem.Block) (crypto.Signer, error) {
	var key any
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	}
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("private key cannot sign")
	}
	return signer, nil
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	if e, ok := a.(equaler); ok {
		return e.Equal(b)
	}
	return false
}

func newSealIdentity(signer crypto.Signer, cert *x509.Certificate, chain []*x509.Certificate, source string) (*sealIdentity, error) {
	if cert == nil {
		return nil, errors.New("missing certificate")
	}
	if !publicKeysEqual(cert.PublicKey, signer.Public()) {
		return nil, errors.New("certificate does not match the private key")
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return nil, fmt.Errorf("certificate is not valid now (valid %s to %s)", cert.NotBefore.UTC().Format(time.RFC3339), cert.NotAfter.UTC().Format(time.RFC3339))
	}
	return &sealIdentity{Signer: signer, Certificate: cert, Chain: chain, Source: source}, nil
}

// selfSignedSeal returns the install's generated seal, creating it on
// first use. Concurrent first calls race on the single-row insert; the
// loser discards its key and reads the winner's.
func selfSignedSeal(db *sql.DB) (*sealIdentity, error) {
	if db == nil {
		return nil, errors.New("database unavailable")
	}
	if id, err := readSelfSignedSeal(db); err == nil {
		return id, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	keyPEM, certPEM, err := generateSelfSignedSeal()
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO seal_keys (id, key_pem, cert_pem, created_at) VALUES (1, ?, ?, ?)`, keyPEM, certPEM, nowUTC()); err != nil {
		return nil, err
	}
	return readSelfSignedSeal(db)
}

func readSelfSignedSeal(db *sql.DB) (*sealIdentity, error) {
	var keyPEM, certPEM string
	if err := db.QueryRow(`SELECT key_pem, cert_pem FROM seal_keys WHERE id = 1`).Scan(&keyPEM, &certPEM); err != nil {
		return nil, err
	}
	id, err := parseSealPEM([]byte(keyPEM + "\n" + certPEM))
	if err != nil {
		return nil, fmt.Errorf("stored seal: %w", err)
	}
	id.Source = sealSourceSelfSigned
	return id, nil
}

func generateSelfSignedSeal() (keyPEM, certPEM string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: sealSelfSignedCN, Organization: []string{"Apteva Signatures"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
		UnknownExtKeyUsage:    []asn1.ObjectIdentifier{oidKPDocumentSigning, oidMSDocumentSigning, oidAdobeAuthenticDocTr},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return keyPEM, certPEM, nil
}

type sealDetails struct {
	SignerName string
	Reason     string
	Location   string
	TSAURL     string
}

// sealPDF appends a certification signature that covers every byte of
// body. It must be the last change made to the PDF.
func sealPDF(body []byte, id *sealIdentity, details sealDetails) ([]byte, error) {
	if id == nil {
		return nil, errors.New("missing seal identity")
	}
	rdr, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, fmt.Errorf("read PDF for sealing: %w", err)
	}
	var chains [][]*x509.Certificate
	if len(id.Chain) > 0 {
		chains = [][]*x509.Certificate{append([]*x509.Certificate{id.Certificate}, id.Chain...)}
	}
	var out bytes.Buffer
	// sign.Sign starts from a 512-byte placeholder that misses RSA
	// signature sizes, and its grow-and-retry path corrupts the output
	// (pdfsign v0.9.0). Build the context directly with room for any
	// RSA-4096 signature plus signed attributes; certificates, chain and
	// timestamp token are still added on top by the library. The input
	// is always an unsigned PDF, so there are no existing signatures to
	// carry over.
	signer := sign.SignContext{
		PDFReader:              rdr,
		InputFile:              bytes.NewReader(body),
		OutputFile:             &out,
		SignatureMaxLengthBase: uint32(hex.EncodedLen(4096)),
	}
	signer.SignData = sign.SignData{
		Signature: sign.SignDataSignature{
			CertType:   sign.CertificationSignature,
			DocMDPPerm: sign.DoNotAllowAnyChangesPerms,
			Info: sign.SignDataSignatureInfo{
				Name:     details.SignerName,
				Location: details.Location,
				Reason:   details.Reason,
				Date:     time.Now().UTC(),
			},
		},
		Signer:            id.Signer,
		DigestAlgorithm:   crypto.SHA256,
		Certificate:       id.Certificate,
		CertificateChains: chains,
		TSA:               sign.TSA{URL: details.TSAURL},
	}
	if err := signer.SignPDF(); err != nil {
		return nil, fmt.Errorf("seal PDF: %w", err)
	}
	return out.Bytes(), nil
}

// SealVerification reports whether a PDF still carries an intact seal.
type SealVerification struct {
	Sealed            bool     `json:"sealed"`
	Intact            bool     `json:"intact"`
	CoversWholeFile   bool     `json:"covers_whole_file"`
	SignatureCount    int      `json:"signature_count"`
	SignerName        string   `json:"signer_name,omitempty"`
	SignerSubject     string   `json:"signer_subject,omitempty"`
	SHA256Fingerprint string   `json:"sha256_fingerprint,omitempty"`
	MatchesThisSeal   bool     `json:"matches_this_install_seal"`
	SignedAt          string   `json:"signed_at,omitempty"`
	TimestampStatus   string   `json:"timestamp_status,omitempty"`
	Problems          []string `json:"problems,omitempty"`
}

var byteRangePattern = regexp.MustCompile(`/ByteRange\s*\[\s*(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s*\]`)

// verifySealedPDF checks the cryptographic signature and that its byte
// range reaches the end of the file, so an appended incremental update
// (the way PDF editors save changes) counts as tampering.
func verifySealedPDF(body []byte, id *sealIdentity) SealVerification {
	var result SealVerification
	ranges := byteRangePattern.FindAllSubmatch(body, -1)
	if len(ranges) == 0 {
		result.Problems = append(result.Problems, "the PDF has no digital signature")
		return result
	}
	result.Sealed = true
	opts := verify.DefaultVerifyOptions()
	opts.AllowUntrustedRoots = true
	opts.RequiredEKUs = nil
	resp, err := verify.VerifyWithOptions(bytes.NewReader(body), int64(len(body)), opts)
	if err != nil {
		result.Problems = append(result.Problems, "signature check failed: "+err.Error())
		return result
	}
	if resp == nil || len(resp.Signers) == 0 {
		result.Problems = append(result.Problems, "no readable signature")
		return result
	}
	result.SignatureCount = len(resp.Signers)
	result.Intact = true
	for _, signer := range resp.Signers {
		if !signer.ValidSignature {
			result.Intact = false
			result.Problems = append(result.Problems, "signature does not match the document bytes")
		}
	}
	last := ranges[len(ranges)-1]
	start, _ := strconv.ParseInt(string(last[3]), 10, 64)
	length, _ := strconv.ParseInt(string(last[4]), 10, 64)
	end := start + length
	trailing := bytes.TrimRight(body[min(int(end), len(body)):], "\r\n ")
	result.CoversWholeFile = end <= int64(len(body)) && len(trailing) == 0
	if !result.CoversWholeFile {
		result.Intact = false
		result.Problems = append(result.Problems, "the PDF was changed after it was sealed")
	}
	signer := resp.Signers[len(resp.Signers)-1]
	result.SignerName = signer.Name
	if signer.SignatureTime != nil {
		result.SignedAt = signer.SignatureTime.UTC().Format(time.RFC3339)
	}
	result.TimestampStatus = signer.TimestampStatus
	if len(signer.Certificates) > 0 && signer.Certificates[0].Certificate != nil {
		leaf := signer.Certificates[0].Certificate
		result.SignerSubject = leaf.Subject.String()
		result.SHA256Fingerprint = certFingerprint(leaf)
		result.MatchesThisSeal = id != nil && bytes.Equal(leaf.Raw, id.Certificate.Raw)
	}
	return result
}
