package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"
)

// The listener consults this on each handshake. Certificate bytes are immutable
// once published; a partial/invalid renewal never replaces a valid certificate.
type sipTLSCertificate struct {
	mu  sync.Mutex
	cfg sipGatewayConfig

	current        *tls.Certificate
	certHash       [sha256.Size]byte
	keyHash        [sha256.Size]byte
	hasFingerprint bool
	lastError      string
}

// Renewal writers do not all use atomic renames. Some replace the contents of
// the existing files quickly enough that inode, size, and filesystem mtime can
// all remain unchanged. Keep content fingerprints so those renewals are still
// observed. The certificate and key are small, and handshakes are infrequent
// compared with media packets, so reading them on a handshake is deliberate.
func (s *sipTLSCertificate) getCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	certPEM, err := os.ReadFile(s.cfg.TLSCertFile)
	if err != nil {
		return s.keepCurrentOrError(fmt.Errorf("read SIP certificate: %w", err))
	}
	keyPEM, err := os.ReadFile(s.cfg.TLSKeyFile)
	if err != nil {
		return s.keepCurrentOrError(fmt.Errorf("read SIP private key: %w", err))
	}
	certHash := sha256.Sum256(certPEM)
	keyHash := sha256.Sum256(keyPEM)
	if s.current != nil && s.hasFingerprint && s.certHash == certHash && s.keyHash == keyHash {
		return s.checkedCurrent()
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err == nil {
		cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
	}
	if err == nil {
		if hostnameErr := cert.Leaf.VerifyHostname(s.cfg.PublicHost); hostnameErr != nil {
			err = fmt.Errorf("SIP certificate does not cover %s: %w", s.cfg.PublicHost, hostnameErr)
		}
	}
	if err == nil && (time.Now().Before(cert.Leaf.NotBefore) || !time.Now().Before(cert.Leaf.NotAfter)) {
		err = fmt.Errorf("certificate is outside its validity period")
	}
	if err != nil {
		return s.keepCurrentOrError(fmt.Errorf("load renewed SIP certificate: %w", err))
	}

	s.current = &cert
	s.certHash = certHash
	s.keyHash = keyHash
	s.hasFingerprint = true
	s.lastError = ""
	return s.checkedCurrent()
}

func (s *sipTLSCertificate) keepCurrentOrError(err error) (*tls.Certificate, error) {
	s.lastError = err.Error()
	if s.current == nil {
		return nil, err
	}
	return s.checkedCurrent()
}

func (s *sipTLSCertificate) checkedCurrent() (*tls.Certificate, error) {
	if s.current == nil {
		return nil, fmt.Errorf("SIP certificate is not loaded")
	}
	if !time.Now().Before(s.current.Leaf.NotAfter) {
		return nil, fmt.Errorf("SIP certificate for %s has expired", s.cfg.PublicHost)
	}
	return s.current, nil
}
func (s *sipTLSCertificate) status() map[string]any {
	_, err := s.getCertificate(nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	result := map[string]any{"ready": err == nil, "renewal_error": s.lastError}
	if s.current != nil {
		result["expires_at"] = s.current.Leaf.NotAfter.UTC().Format(time.RFC3339)
	}
	if err != nil {
		result["error"] = err.Error()
	}
	return result
}
