package sdk

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"
)

const HeaderClientIPMetadata = "X-Apteva-Client-IP-Metadata"
const HeaderClientIPSignature = "X-Apteva-Client-IP-Signature"

// Separate the signature from other platform assertions using the same key.
const clientIPSignatureDomain = "apteva-client-ip-v1\n"

type clientIPMetadata struct {
	Version   int    `json:"version"`
	IP        string `json:"ip"`
	Method    string `json:"method"`
	Target    string `json:"target"`
	ExpiresAt int64  `json:"expires_at"`
}

// SetClientIPHeaders is for platform gateways forwarding to an app. It always
// strips incoming assertions, then signs the already-resolved address with the
// destination installation's inbound token. Call after rewriting the URL.
// An empty token or invalid address leaves no assertion. This does not change
// Authorization, routing, or the destination application's access checks.
func SetClientIPHeaders(r *http.Request, ip, token string) {
	if r == nil {
		return
	}
	if r.Header == nil {
		r.Header = make(http.Header)
	}
	r.Header.Del(HeaderClientIPMetadata)
	r.Header.Del(HeaderClientIPSignature)
	// Do not let an unsigned, caller-supplied header masquerade as this API.
	r.Header.Del("X-Apteva-Client-IP")
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil || addr.Zone() != "" || token == "" || r.URL == nil {
		return
	}
	metadata := clientIPMetadata{Version: 1, IP: addr.Unmap().String(), Method: r.Method,
		Target: r.URL.RequestURI(), ExpiresAt: time.Now().Add(time.Minute).Unix()}
	raw, _ := json.Marshal(metadata)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	r.Header.Set(HeaderClientIPMetadata, payload)
	r.Header.Set(HeaderClientIPSignature, hex.EncodeToString(clientIPMAC(token, payload)))
}

// ClientIPFromRequest returns the IP resolved and signed by the platform
// gateway. It verifies with this installation's APTEVA_APP_TOKEN, including on
// NoAuth routes. Missing metadata returns "", nil; malformed, forged, expired,
// or request-mismatched metadata returns an error. It never trusts arbitrary
// forwarded headers or falls back to the proxy's RemoteAddr.
//
// For WebSockets, read this during the HTTP handshake and retain the result for
// that connection. A reconnect is a new request with a freshly resolved IP.
// This identifies the connecting peer, which may be a carrier or another app;
// it is neither an authenticated user identity nor proof of VPN use.
func ClientIPFromRequest(r *http.Request) (string, error) {
	if r == nil {
		return "", nil
	}
	payloads, signatures := r.Header.Values(HeaderClientIPMetadata), r.Header.Values(HeaderClientIPSignature)
	if len(payloads) == 0 && len(signatures) == 0 {
		return "", nil
	}
	if len(payloads) != 1 || len(signatures) != 1 || len(payloads[0]) == 0 || len(payloads[0]) > 16384 || len(signatures[0]) != sha256.Size*2 {
		return "", errors.New("invalid client IP metadata headers")
	}
	payload, signature := payloads[0], signatures[0]
	token := os.Getenv("APTEVA_APP_TOKEN")
	if token == "" {
		return "", errors.New("cannot verify client IP without app token")
	}
	got, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(got, clientIPMAC(token, payload)) {
		return "", errors.New("invalid client IP signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", errors.New("invalid client IP metadata encoding")
	}
	var metadata clientIPMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return "", errors.New("invalid client IP metadata JSON")
	}
	if metadata.Version != 1 || metadata.ExpiresAt <= time.Now().Unix() {
		return "", errors.New("expired or unsupported client IP metadata")
	}
	if r.URL == nil || metadata.Method != r.Method || metadata.Target != r.URL.RequestURI() {
		return "", errors.New("client IP metadata does not match request")
	}
	addr, err := netip.ParseAddr(metadata.IP)
	if err != nil || addr.Zone() != "" {
		return "", errors.New("invalid client IP address")
	}
	return addr.Unmap().String(), nil
}

func clientIPMAC(token, payload string) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(clientIPSignatureDomain))
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}
