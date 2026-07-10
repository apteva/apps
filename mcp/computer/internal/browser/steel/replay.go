package steel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/apteva/apps/mcp/computer/internal/browser/replay"
)

type ReplayResolver struct {
	apiKey string
	http   *http.Client
}

func NewReplayResolver(apiKey string) *ReplayResolver {
	return &ReplayResolver{
		apiKey: apiKey,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (r *ReplayResolver) Metadata(ctx context.Context, providerSessionID string) (replay.Recording, error) {
	body, _, _, err := r.fetch(ctx, providerSessionID, r.playlistURL(providerSessionID))
	if err != nil {
		return replay.Recording{}, err
	}
	if len(body) == 0 {
		return replay.Recording{}, replay.ErrNotFound
	}
	return replay.Recording{
		Supported: true,
		Status:    "ready",
		Streams:   []replay.RecordingStream{{ID: "0"}},
	}, nil
}

func (r *ReplayResolver) Playlist(ctx context.Context, providerSessionID, streamID string) ([]byte, string, error) {
	if streamID != "0" {
		return nil, "", replay.ErrNotFound
	}
	body, contentType, requestURL, err := r.fetch(ctx, providerSessionID, r.playlistURL(providerSessionID))
	if err != nil {
		return nil, "", err
	}
	return normalizePlaylistURLs(body, requestURL), contentType, nil
}

func (r *ReplayResolver) SignResource(providerSessionID, resourceURL string) (string, error) {
	if err := r.validateResourceURL(resourceURL); err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(resourceURL))
	mac := hmac.New(sha256.New, []byte(r.apiKey))
	_, _ = mac.Write([]byte(providerSessionID))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(resourceURL))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig, nil
}

func (r *ReplayResolver) Resource(ctx context.Context, providerSessionID, token string) ([]byte, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, "", fmt.Errorf("steel recording resource: invalid token")
	}
	rawURL, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, "", fmt.Errorf("steel recording resource: invalid token")
	}
	resourceURL := string(rawURL)
	want, err := r.SignResource(providerSessionID, resourceURL)
	if err != nil || !hmac.Equal([]byte(want), []byte(token)) {
		return nil, "", fmt.Errorf("steel recording resource: invalid token")
	}
	body, contentType, requestURL, err := r.fetch(ctx, providerSessionID, resourceURL)
	if err != nil {
		return nil, "", err
	}
	if isHLS(contentType, body) {
		body = normalizePlaylistURLs(body, requestURL)
	}
	return body, contentType, nil
}

func (r *ReplayResolver) playlistURL(providerSessionID string) string {
	return apiBase + "/sessions/" + url.PathEscape(providerSessionID) + "/hls"
}

func (r *ReplayResolver) fetch(ctx context.Context, providerSessionID, endpoint string) ([]byte, string, string, error) {
	if strings.TrimSpace(r.apiKey) == "" {
		return nil, "", "", fmt.Errorf("steel: api_key is required for recording retrieval")
	}
	if endpoint != r.playlistURL(providerSessionID) {
		if err := r.validateResourceURL(endpoint); err != nil {
			return nil, "", "", err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("Steel-Api-Key", r.apiKey)
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", "", replay.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, "", "", &replay.HTTPError{Provider: "steel", Op: "recording resource", Status: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, "", "", err
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if contentType == "" && isHLS("", body) {
		contentType = "application/vnd.apple.mpegurl"
	}
	return body, contentType, resp.Request.URL.String(), nil
}

func (r *ReplayResolver) validateResourceURL(resourceURL string) error {
	resource, err := url.Parse(resourceURL)
	if err != nil || resource.Scheme == "" || resource.Host == "" {
		return fmt.Errorf("steel recording resource: invalid URL")
	}
	base, err := url.Parse(apiBase)
	if err != nil {
		return fmt.Errorf("steel recording resource: invalid provider URL")
	}
	if !strings.EqualFold(resource.Scheme, base.Scheme) || !strings.EqualFold(resource.Host, base.Host) {
		return replay.ErrExternalResource
	}
	return nil
}

func normalizePlaylistURLs(body []byte, baseURL string) []byte {
	base, err := url.Parse(baseURL)
	if err != nil {
		return body
	}
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			lines[i] = rewriteURIAttributes(line, base)
			continue
		}
		if ref, err := url.Parse(trimmed); err == nil {
			lines[i] = base.ResolveReference(ref).String()
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

func rewriteURIAttributes(line string, base *url.URL) string {
	const marker = `URI="`
	from := 0
	for {
		start := strings.Index(line[from:], marker)
		if start < 0 {
			return line
		}
		start += from + len(marker)
		end := strings.IndexByte(line[start:], '"')
		if end < 0 {
			return line
		}
		end += start
		ref, err := url.Parse(line[start:end])
		if err == nil {
			resolved := base.ResolveReference(ref).String()
			line = line[:start] + resolved + line[end:]
			end = start + len(resolved)
		}
		from = end + 1
	}
}

func isHLS(contentType string, body []byte) bool {
	return strings.Contains(strings.ToLower(contentType), "mpegurl") || strings.HasPrefix(strings.TrimSpace(string(body)), "#EXTM3U")
}

var _ replay.Resolver = (*ReplayResolver)(nil)
var _ replay.ResourceResolver = (*ReplayResolver)(nil)
