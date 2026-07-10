package browserbase

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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
		http: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 0 && !sameOrigin(req.URL.String(), via[0].URL.String()) {
					req.Header.Del("X-BB-API-Key")
				}
				return nil
			},
		},
	}
}

func (r *ReplayResolver) Metadata(ctx context.Context, providerSessionID string) (replay.Recording, error) {
	if strings.TrimSpace(r.apiKey) == "" {
		return replay.Recording{}, fmt.Errorf("browserbase: api_key is required for recording retrieval")
	}
	endpoint := apiBase + "/sessions/" + url.PathEscape(providerSessionID) + "/replays"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return replay.Recording{}, err
	}
	req.Header.Set("X-BB-API-Key", r.apiKey)
	resp, err := r.http.Do(req)
	if err != nil {
		return replay.Recording{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return replay.Recording{}, replay.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return replay.Recording{}, &replay.HTTPError{Provider: "browserbase", Op: "replay metadata", Status: resp.StatusCode}
	}
	var body struct {
		Pages []struct {
			PageID      string `json:"pageId"`
			URL         string `json:"url"`
			StartTimeMS int64  `json:"startTimeMs"`
			EndTimeMS   int64  `json:"endTimeMs"`
		} `json:"pages"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body); err != nil {
		return replay.Recording{}, fmt.Errorf("browserbase replay metadata: %w", err)
	}
	streams := make([]replay.RecordingStream, 0, len(body.Pages))
	for _, page := range body.Pages {
		if page.PageID == "" {
			continue
		}
		streams = append(streams, replay.RecordingStream{
			ID:        page.PageID,
			StartMS:   page.StartTimeMS,
			EndMS:     page.EndTimeMS,
			SourceURL: page.URL,
		})
	}
	if len(streams) == 0 {
		return replay.Recording{}, replay.ErrNotFound
	}
	return replay.Recording{Supported: true, Status: "ready", Streams: streams}, nil
}

func (r *ReplayResolver) Playlist(ctx context.Context, providerSessionID, streamID string) ([]byte, string, error) {
	if strings.TrimSpace(r.apiKey) == "" {
		return nil, "", fmt.Errorf("browserbase: api_key is required for recording retrieval")
	}
	endpoint := apiBase + "/sessions/" + url.PathEscape(providerSessionID) + "/replays/" + url.PathEscape(streamID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("X-BB-API-Key", r.apiKey)
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", replay.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", &replay.HTTPError{Provider: "browserbase", Op: "replay playlist", Status: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, "", err
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if contentType == "" {
		contentType = "application/vnd.apple.mpegurl"
	}
	if isHLS(contentType, body) {
		body = normalizePlaylistURLs(body, resp.Request.URL.String())
	}
	return body, contentType, nil
}

func (r *ReplayResolver) SignResource(providerSessionID, resourceURL string) (string, error) {
	if err := validateResourceURL(resourceURL); err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(resourceURL))
	mac := hmac.New(sha256.New, []byte(r.apiKey))
	_, _ = mac.Write([]byte(providerSessionID))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(resourceURL))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + signature, nil
}

func (r *ReplayResolver) Resource(ctx context.Context, providerSessionID, token string) ([]byte, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, "", errors.New("browserbase recording resource: invalid token")
	}
	rawURL, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, "", errors.New("browserbase recording resource: invalid token")
	}
	resourceURL := string(rawURL)
	want, err := r.SignResource(providerSessionID, resourceURL)
	if err != nil || !hmac.Equal([]byte(want), []byte(token)) {
		return nil, "", errors.New("browserbase recording resource: invalid token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resourceURL, nil)
	if err != nil {
		return nil, "", err
	}
	if sameOrigin(resourceURL, apiBase) {
		req.Header.Set("X-BB-API-Key", r.apiKey)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", replay.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, "", &replay.HTTPError{Provider: "browserbase", Op: "recording resource", Status: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, "", err
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if isHLS(contentType, body) {
		body = normalizePlaylistURLs(body, resp.Request.URL.String())
		if contentType == "" {
			contentType = "application/vnd.apple.mpegurl"
		}
	}
	return body, contentType, nil
}

func validateResourceURL(raw string) error {
	resource, err := url.Parse(raw)
	if err != nil || resource.Host == "" || resource.User != nil {
		return errors.New("browserbase recording resource: invalid URL")
	}
	base, err := url.Parse(apiBase)
	if err != nil {
		return errors.New("browserbase recording resource: invalid provider URL")
	}
	if resource.Scheme != "https" && !(base.Scheme == "http" && resource.Scheme == "http") {
		return replay.ErrExternalResource
	}
	return nil
}

func sameOrigin(left, right string) bool {
	a, errA := url.Parse(left)
	b, errB := url.Parse(right)
	return errA == nil && errB == nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func normalizePlaylistURLs(body []byte, baseURL string) []byte {
	base, err := url.Parse(baseURL)
	if err != nil {
		return body
	}
	lines := strings.Split(string(body), "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			lines[index] = rewriteURIAttributes(line, base)
			continue
		}
		if ref, err := url.Parse(trimmed); err == nil {
			lines[index] = base.ResolveReference(ref).String()
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
		if ref, err := url.Parse(line[start:end]); err == nil {
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
