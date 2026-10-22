package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

func certificatePEM(text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if len(text) > 64<<10 {
		return nil, errors.New("TLS material exceeds 64 KiB")
	}
	if strings.HasPrefix(text, "-----BEGIN ") {
		return []byte(text), nil
	}
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, errors.New("TLS material must be PEM or base64-encoded PEM")
	}
	return decoded, nil
}

func parseCertificate(certPEM, keyPEM, host string, now time.Time) (*tls.Certificate, error) {
	if len(certPEM)+len(keyPEM) > 64<<10 {
		return nil, errors.New("combined TLS certificate and key material must not exceed 64 KiB")
	}
	if strings.TrimSpace(certPEM) == "" && strings.TrimSpace(keyPEM) == "" {
		return nil, nil
	}
	if strings.TrimSpace(certPEM) == "" || strings.TrimSpace(keyPEM) == "" {
		return nil, errors.New("both TLS certificate chain and private key are required")
	}
	certBytes, err := certificatePEM(certPEM)
	if err != nil {
		return nil, err
	}
	keyBytes, err := certificatePEM(keyPEM)
	if err != nil {
		return nil, err
	}
	certificate, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil {
		return nil, errors.New("TLS certificate chain and private key are invalid or do not match")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, errors.New("TLS leaf certificate is invalid")
	}
	if leaf.IsCA || leaf.VerifyHostname(host) != nil {
		return nil, errors.New("TLS certificate must be a server certificate covering public_host")
	}
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, errors.New("TLS certificate is not currently valid")
	}
	if len(leaf.ExtKeyUsage) > 0 {
		serverAuth := false
		for _, usage := range leaf.ExtKeyUsage {
			if usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny {
				serverAuth = true
			}
		}
		if !serverAuth {
			return nil, errors.New("TLS certificate does not permit server authentication")
		}
	}
	certificate.Leaf = leaf
	return &certificate, nil
}

func (c config) tlsValid(now time.Time) bool {
	return c.certificate != nil && c.certificate.Leaf != nil && !now.Before(c.certificate.Leaf.NotBefore) && now.Before(c.certificate.Leaf.NotAfter)
}

func (c config) tlsConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		if !c.tlsValid(time.Now()) {
			return nil, errors.New("a valid TURN TLS certificate is not configured")
		}
		return c.certificate, nil
	}}
}

func (c config) certificateStatus(now time.Time) map[string]any {
	out := map[string]any{"configured": c.certificate != nil, "valid": c.tlsValid(now), "hostname": c.publicHost, "port": tlsPort}
	if c.certificate != nil {
		leaf := c.certificate.Leaf
		sum := sha256.Sum256(leaf.Raw)
		out["issuer"] = leaf.Issuer.String()
		out["not_before"] = leaf.NotBefore.Unix()
		out["expires_at"] = leaf.NotAfter.Unix()
		out["fingerprint_sha256"] = hex.EncodeToString(sum[:])
		out["days_remaining"] = int(leaf.NotAfter.Sub(now).Hours() / 24)
	}
	return out
}
