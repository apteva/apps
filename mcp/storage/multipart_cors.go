package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/minio/minio-go/v7/pkg/cors"
)

type browserUploadBackend interface {
	PrepareBrowserUpload(context.Context, string) error
}

func uploadOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}
func configuredUploadOrigin(app *sdk.AppCtx) string {
	if info, err := app.PlatformInfo(); err == nil && info != nil {
		return uploadOrigin(info.PublicURL)
	}
	return uploadOrigin(os.Getenv("APTEVA_PUBLIC_URL"))
}
func prepareBrowserUpload(c context.Context, app *sdk.AppCtx, requestOrigin string) bool {
	be, ok := backend().(browserUploadBackend)
	if !ok {
		return false
	}
	origin := configuredUploadOrigin(app)
	// Only the platform's configured public URL is trusted. In particular an
	// arbitrary Origin or forwarded Host must never grant itself bucket access.
	if origin == "" || (requestOrigin != "" && requestOrigin != origin) {
		return false
	}
	if err := be.PrepareBrowserUpload(c, origin); err != nil {
		app.Logger().Warn("bucket upload CORS unavailable; using multipart relay", "origin", origin, "err", err)
		return false
	}
	return true
}

func (s *s3Backend) PrepareBrowserUpload(parent context.Context, origin string) error {
	if origin == "" || uploadOrigin(origin) != origin {
		return errors.New("invalid dashboard origin")
	}
	s.corsMu.Lock()
	defer s.corsMu.Unlock()
	if s.corsOrigin == origin && time.Now().Before(s.corsUntil) {
		return s.corsErr
	}
	c, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	err := s.ensureUploadCors(c, origin)
	if parent.Err() != nil {
		return parent.Err()
	}
	ttl := 10 * time.Minute
	if err != nil {
		ttl = time.Minute
	}
	s.corsOrigin, s.corsUntil, s.corsErr = origin, time.Now().Add(ttl), err
	return err
}
func (s *s3Backend) ensureUploadCors(c context.Context, origin string) error {
	// Existing rules may already work even when this credential cannot manage CORS.
	if s.probeUploadCors(c, origin) == nil {
		return nil
	}
	config, err := s.client.GetBucketCors(c, s.bucket)
	if err != nil {
		return err
	}
	if config == nil {
		config = &cors.Config{}
	}
	id := fmt.Sprintf("apteva-upload-%x", sha256.Sum256([]byte(origin)))
	found := false
	for _, rule := range config.CORSRules {
		if rule.ID == id {
			found = true
			break
		}
	}
	if !found {
		if len(config.CORSRules) >= 100 {
			return errors.New("bucket CORS rule limit reached")
		}
		// Preserve every existing rule, including previous dashboard origins.
		config.CORSRules = append(config.CORSRules, cors.Rule{ID: id, AllowedOrigin: []string{origin}, AllowedMethod: []string{"PUT"}, AllowedHeader: []string{"content-type"}, ExposeHeader: []string{"ETag"}, MaxAgeSeconds: 3600})
		if err = s.client.SetBucketCors(c, s.bucket, config); err != nil {
			return err
		}
	}
	return s.probeUploadCors(c, origin)
}
func (s *s3Backend) probeUploadCors(c context.Context, origin string) error {
	signed, err := s.SignMultipartPart(c, "00/apteva-cors-check", "cors-check", 1, 1)
	if err != nil {
		return err
	}
	u, err := url.Parse(signed)
	if err != nil {
		return err
	}
	u.RawQuery = "" // OPTIONS is unsigned and never carries a credential.
	req, err := http.NewRequestWithContext(c, http.MethodOptions, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "PUT")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	allowed := res.Header.Get("Access-Control-Allow-Origin")
	if res.StatusCode < 200 || res.StatusCode >= 300 || (allowed != origin && allowed != "*") || !corsToken(res.Header.Get("Access-Control-Allow-Methods"), "PUT") || !corsToken(res.Header.Get("Access-Control-Allow-Headers"), "content-type") {
		return errors.New("bucket upload preflight denied")
	}
	return nil
}
func corsToken(value, want string) bool {
	for _, token := range strings.Split(value, ",") {
		if token = strings.TrimSpace(token); token == "*" || strings.EqualFold(token, want) {
			return true
		}
	}
	return false
}
