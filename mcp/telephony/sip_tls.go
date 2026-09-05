package main

import (
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
	mu                sync.Mutex
	cfg               sipGatewayConfig
	current           *tls.Certificate
	certInfo, keyInfo os.FileInfo
	lastError         string
}

func (s *sipTLSCertificate) getCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	certInfo, certErr := os.Stat(s.cfg.TLSCertFile)
	keyInfo, keyErr := os.Stat(s.cfg.TLSKeyFile)
	changed := s.current == nil || certErr != nil || keyErr != nil || !sameSIPCertificateFile(s.certInfo, certInfo) || !sameSIPCertificateFile(s.keyInfo, keyInfo)
	if changed {
		cert, err := tls.LoadX509KeyPair(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		if err == nil {
			cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
			if err == nil {
				if e := cert.Leaf.VerifyHostname(s.cfg.PublicHost); e != nil {
					err = fmt.Errorf("SIP certificate does not cover %s: %w", s.cfg.PublicHost, e)
				}
			}
			if err == nil && (time.Now().Before(cert.Leaf.NotBefore) || !time.Now().Before(cert.Leaf.NotAfter)) {
				err = fmt.Errorf("certificate is outside its validity period")
			}
		}
		if err == nil {
			s.current = &cert
			s.certInfo = certInfo
			s.keyInfo = keyInfo
			s.lastError = ""
		} else {
			s.lastError = fmt.Sprintf("load renewed SIP certificate: %v", err)
		}
	}
	if s.current == nil {
		return nil, fmt.Errorf("%s", s.lastError)
	}
	if !time.Now().Before(s.current.Leaf.NotAfter) {
		return nil, fmt.Errorf("SIP certificate for %s has expired", s.cfg.PublicHost)
	}
	return s.current, nil
}
func sameSIPCertificateFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
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
