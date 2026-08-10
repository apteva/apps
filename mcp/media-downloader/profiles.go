package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	encryptedPrefix = "v1:"
	plainPrefix     = "plain:"
)

var sourceProfileProviders = map[string][]string{
	"youtube":   {"youtube.com", "google.com"},
	"patreon":   {"patreon.com"},
	"instagram": {"instagram.com"},
}

func profileSecret(ctx *sdk.AppCtx) string {
	if v := strings.TrimSpace(os.Getenv("MEDIA_DOWNLOADER_SECRET")); v != "" {
		return v
	}
	if ctx != nil {
		if v := strings.TrimSpace(ctx.Config().Get("shared_secret")); v != "" {
			return v
		}
	}
	return strings.TrimSpace(os.Getenv("APTEVA_SECRET"))
}

func encryptPayload(ctx *sdk.AppCtx, payload profilePayload) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	secret := profileSecret(ctx)
	if secret == "" {
		if configBool(ctx, "allow_plaintext_profiles", false) {
			return plainPrefix + base64.StdEncoding.EncodeToString(body), nil
		}
		return "", errors.New("source profiles require MEDIA_DOWNLOADER_SECRET, APTEVA_SECRET, or shared_secret config; set allow_plaintext_profiles=true only for local development")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, body, nil)
	packed := append(nonce, ciphertext...)
	return encryptedPrefix + base64.StdEncoding.EncodeToString(packed), nil
}

func decryptPayload(ctx *sdk.AppCtx, encoded string) (profilePayload, error) {
	var payload profilePayload
	if strings.HasPrefix(encoded, plainPrefix) {
		if !configBool(ctx, "allow_plaintext_profiles", false) {
			return payload, errors.New("profile was stored as plaintext but allow_plaintext_profiles is disabled")
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, plainPrefix))
		if err != nil {
			return payload, err
		}
		return payload, json.Unmarshal(raw, &payload)
	}
	if !strings.HasPrefix(encoded, encryptedPrefix) {
		return payload, errors.New("unknown profile payload format")
	}
	secret := profileSecret(ctx)
	if secret == "" {
		return payload, errors.New("profile encryption secret is not configured")
	}
	packed, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, encryptedPrefix))
	if err != nil {
		return payload, err
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return payload, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return payload, err
	}
	if len(packed) < gcm.NonceSize() {
		return payload, errors.New("profile payload is truncated")
	}
	nonce, ciphertext := packed[:gcm.NonceSize()], packed[gcm.NonceSize():]
	raw, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return payload, err
	}
	return payload, json.Unmarshal(raw, &payload)
}

func validateCookieProfile(provider, authType string, payload profilePayload) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	authType = strings.ToLower(strings.TrimSpace(authType))
	if provider == "" {
		provider = "youtube"
	}
	if authType == "" {
		authType = "cookies_netscape"
	}
	domains, ok := sourceProfileProviders[provider]
	if !ok {
		return fmt.Errorf("provider %q is not supported; supported providers: youtube, patreon, instagram", provider)
	}
	if authType != "cookies_netscape" {
		return fmt.Errorf("auth_type %q is not supported in v0.2", authType)
	}
	cookies := strings.TrimSpace(payload.CookiesNetscape)
	if cookies == "" {
		return errors.New("cookies_netscape is required")
	}
	foundCookie := false
	foundRelevantDomain := false
	for _, line := range strings.Split(cookies, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 7 {
			return errors.New("cookies_netscape must use Netscape cookie format with 7 tab-separated fields")
		}
		foundCookie = true
		domain := strings.TrimPrefix(strings.ToLower(parts[0]), "#httponly_")
		domain = strings.TrimPrefix(domain, ".")
		if matchesDomain(domain, domains) {
			foundRelevantDomain = true
		}
	}
	if !foundCookie {
		return errors.New("cookies_netscape contains no cookie rows")
	}
	if !foundRelevantDomain {
		return fmt.Errorf("%s profiles must include cookies for %s", provider, strings.Join(domains, " or "))
	}
	return nil
}

func writeCookieFile(dir string, payload profilePayload) (string, error) {
	if strings.TrimSpace(payload.CookiesNetscape) == "" {
		return "", nil
	}
	path := filepath.Join(dir, "cookies.txt")
	if err := os.WriteFile(path, []byte(payload.CookiesNetscape), 0600); err != nil {
		return "", err
	}
	return path, nil
}
