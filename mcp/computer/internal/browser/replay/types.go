// Package replay defines provider-neutral recording retrieval.
//
// Replay resolvers are deliberately separate from api.Computer: recordings
// outlive the live browser connection and must remain retrievable after the
// in-memory Computer has been closed or the sidecar has restarted.
package replay

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrNotFound         = errors.New("recording not found")
	ErrExternalResource = errors.New("external recording resource")
)

type Recording struct {
	Supported bool              `json:"supported"`
	Status    string            `json:"status"`
	Streams   []RecordingStream `json:"streams,omitempty"`
}

type RecordingStream struct {
	ID        string `json:"id"`
	StartMS   int64  `json:"start_ms,omitempty"`
	EndMS     int64  `json:"end_ms,omitempty"`
	SourceURL string `json:"-"`
}

type Resolver interface {
	Metadata(ctx context.Context, providerSessionID string) (Recording, error)
	Playlist(ctx context.Context, providerSessionID, streamID string) ([]byte, string, error)
}

// ResourceResolver is implemented by providers whose HLS playlists contain
// authenticated child playlists or media resources. Tokens are opaque,
// provider-signed capabilities scoped to one provider session.
type ResourceResolver interface {
	SignResource(providerSessionID, resourceURL string) (string, error)
	Resource(ctx context.Context, providerSessionID, token string) ([]byte, string, error)
}

type HTTPError struct {
	Provider string
	Op       string
	Status   int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d", e.Provider, e.Op, e.Status)
}
