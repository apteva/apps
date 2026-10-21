package sdk

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

const HeaderTrustedPrincipal = "X-Apteva-Trusted-Principal"
const HeaderTrustedPrincipalSignature = "X-Apteva-Trusted-Principal-Signature"
const TrustedPrincipalVersion = 1

// TrustedPrincipal is an authenticated end-user identity minted by
// apteva-server for one request to an app. It is intentionally small and
// contains no session credential. Apps can use it instead of making a second
// Auth /me request, while authorization and revocation remain at the server
// boundary on every incoming request.
type TrustedPrincipal struct {
	Version          int    `json:"version"`
	UserID           int64  `json:"user_id,omitempty"`
	Email            string `json:"email,omitempty"`
	SubjectType      string `json:"subject_type,omitempty"`
	SubjectID        string `json:"subject_id,omitempty"`
	SubjectEmail     string `json:"subject_email,omitempty"`
	OrganizationID   string `json:"organization_id,omitempty"`
	OrganizationSlug string `json:"organization_slug,omitempty"`
	ProjectID        string `json:"project_id,omitempty"`
	ExpiresAt        int64  `json:"expires_at"`
}

// PrincipalFromRequest verifies and decodes the principal attached by the
// platform proxy. Verification uses this install's APTEVA_APP_TOKEN, so raw
// client-supplied identity headers never become a trusted Principal. A nil
// principal with nil error means the platform did not attach one.
func PrincipalFromRequest(r *http.Request) (*TrustedPrincipal, error) {
	if r == nil {
		return nil, nil
	}
	payload := strings.TrimSpace(r.Header.Get(HeaderTrustedPrincipal))
	signature := strings.TrimSpace(r.Header.Get(HeaderTrustedPrincipalSignature))
	if payload == "" && signature == "" {
		return nil, nil
	}
	if payload == "" || signature == "" {
		return nil, errors.New("incomplete trusted principal")
	}
	token := os.Getenv("APTEVA_APP_TOKEN")
	if token == "" {
		return nil, errors.New("cannot verify trusted principal without app token")
	}
	got, err := hex.DecodeString(signature)
	if err != nil {
		return nil, errors.New("invalid trusted principal signature")
	}
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return nil, errors.New("invalid trusted principal signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, errors.New("invalid trusted principal encoding")
	}
	var principal TrustedPrincipal
	if err := json.Unmarshal(raw, &principal); err != nil {
		return nil, errors.New("invalid trusted principal JSON")
	}
	if principal.Version != TrustedPrincipalVersion || principal.ExpiresAt <= 0 || time.Now().Unix() > principal.ExpiresAt {
		return nil, errors.New("expired or unsupported trusted principal")
	}
	if principal.UserID <= 0 && (principal.SubjectType == "" || principal.SubjectID == "") {
		return nil, errors.New("trusted principal has no identity")
	}
	return &principal, nil
}
