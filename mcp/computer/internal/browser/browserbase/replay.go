package browserbase

import (
	"context"
	"encoding/json"
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
	return body, contentType, nil
}

var _ replay.Resolver = (*ReplayResolver)(nil)
