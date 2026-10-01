package main

// v0.82.0 does not yet export the signed-principal helper. Verify the
// platform's envelope here without depending on unpublished SDK changes.
import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

type principal struct {
	Version     int    `json:"version"`
	UserID      int64  `json:"user_id"`
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	ProjectID   string `json:"project_id"`
	ExpiresAt   int64  `json:"expires_at"`
}

func principalFromRequest(r *http.Request) (*principal, error) {
	payload := r.Header.Get("X-Apteva-Trusted-Principal")
	signature := r.Header.Get("X-Apteva-Trusted-Principal-Signature")
	token := os.Getenv("APTEVA_APP_TOKEN")
	if payload == "" || signature == "" || token == "" {
		return nil, fmt.Errorf("incomplete principal")
	}
	got, err := hex.DecodeString(signature)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return nil, fmt.Errorf("invalid signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, err
	}
	var p principal
	if err = json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Version != 1 || p.ExpiresAt < time.Now().Unix() || (p.UserID <= 0 && (p.SubjectType == "" || p.SubjectID == "")) {
		return nil, fmt.Errorf("invalid principal")
	}
	return &p, nil
}
