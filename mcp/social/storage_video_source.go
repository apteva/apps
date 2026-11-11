package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Storage's legacy source is provenance, not a delivery descriptor. Keep the
// wire decoder separate from persisted recovery proofs, which are always typed.
type storageSourceField struct{ Descriptor *storageVideoSource }

type storageVideoURLResponse struct {
	URL    string             `json:"url"`
	FileID int64              `json:"file_id"`
	Source storageSourceField `json:"source"`
	File   struct {
		Source storageSourceField `json:"source"`
	} `json:"file"`
}

func (s *storageSourceField) UnmarshalJSON(raw []byte) error {
	s.Descriptor = nil
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if len(raw) > 0 && raw[0] == '"' {
		var provenance string
		return json.Unmarshal(raw, &provenance)
	}
	var source storageVideoSource
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &source) != nil || !source.validDescriptor() {
		return &sourceDeliveryError{Code: "invalid_source_descriptor", Reason: "Storage advertised an invalid storage-source/v1 descriptor"}
	}
	s.Descriptor = &source
	return nil
}

func storageResponseSource(top, nested storageSourceField) (*storageVideoSource, error) {
	if top.Descriptor != nil && nested.Descriptor != nil && *top.Descriptor != *nested.Descriptor {
		return nil, &sourceDeliveryError{Code: "source_changed", Reason: "Storage returned conflicting source descriptors"}
	}
	if top.Descriptor != nil {
		return top.Descriptor, nil
	}
	return nested.Descriptor, nil
}

type storageVideoChecksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
	Status    string `json:"status"`
}

// This descriptor is authoritative only when Storage enforces its content
// revision on the signed delivery URL and every response. Older Storage
// versions omit it and use fully verified disk staging.
type storageVideoSource struct {
	Schema           string               `json:"schema"`
	StorageInstallID int64                `json:"storage_install_id"`
	ProjectID        string               `json:"project_id"`
	FileID           int64                `json:"file_id"`
	ContentRevision  string               `json:"content_revision"`
	SizeBytes        int64                `json:"size_bytes"`
	ContentType      string               `json:"content_type"`
	Checksum         storageVideoChecksum `json:"checksum"`
}

func (s storageVideoSource) validDescriptor() bool {
	return s.Schema == "storage-source/v1" && s.StorageInstallID > 0 && strings.TrimSpace(s.ProjectID) != "" && s.FileID > 0 && validContentRevision(s.ContentRevision) &&
		s.SizeBytes > 0 && s.SizeBytes <= maxPreparedVideoBytes && strings.HasPrefix(canonicalVideoMIME(s.ContentType), "video/") &&
		s.Checksum.Algorithm == "sha256" && s.Checksum.Status == "verified" && validSHA256(s.Checksum.Value)
}

func (s *storageVideoSource) streamingEligible() bool {
	return s != nil && s.Schema == "storage-source/v1" &&
		s.Checksum.Status == "verified" && s.Checksum.Algorithm == "sha256"
}

func (s storageVideoSource) validFor(item mediaItem, project string, install int64) bool {
	return s.Schema == "storage-source/v1" && s.StorageInstallID > 0 && s.StorageInstallID == install &&
		s.ProjectID == project && s.FileID == item.ID && validContentRevision(s.ContentRevision) &&
		s.SizeBytes > 0 && s.SizeBytes <= maxPreparedVideoBytes && s.SizeBytes == item.Bytes &&
		canonicalVideoMIME(s.ContentType) == canonicalVideoMIME(item.Mime) &&
		s.Checksum.Algorithm == "sha256" && s.Checksum.Status == "verified" && len(s.Checksum.Value) == 64 &&
		validSHA256(s.Checksum.Value) && strings.EqualFold(s.Checksum.Value, item.SHA256)
}

func validContentRevision(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func videoRangeChunkSize(ctx *sdk.AppCtx) (int64, error) {
	raw := ctx.Config().Get("video_source_chunk_mb")
	if raw == "" {
		return 4 << 20, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 || n > 16 {
		return 0, &sourceDeliveryError{Reason: "video_source_chunk_mb must be an integer between 1 and 16"}
	}
	return n << 20, nil
}

type storageVideoReader struct {
	mu      sync.Mutex
	app     *sdk.AppCtx
	item    mediaItem
	source  storageVideoSource
	ctx     context.Context
	cancel  context.CancelFunc
	chunk   int64
	url     string
	start   int64
	cache   []byte
	lastErr error
	closed  bool
}

func (r *storageVideoReader) Close() error {
	r.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.cache = nil
	return nil
}

func (r *storageVideoReader) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

func (r *storageVideoReader) ReadAt(destination []byte, offset int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx.Err() != nil {
		return 0, context.Canceled
	}
	if offset < 0 {
		return 0, errors.New("negative video source offset")
	}
	if len(destination) == 0 {
		return 0, nil
	}
	r.lastErr = nil
	n := 0
	for n < len(destination) && offset < r.source.SizeBytes {
		start := offset / r.chunk * r.chunk
		if len(r.cache) == 0 || r.start != start {
			if err := r.readChunk(start); err != nil {
				r.lastErr = err
				return n, err
			}
		}
		count := copy(destination[n:], r.cache[offset-r.start:])
		n += count
		offset += int64(count)
	}
	if n != len(destination) {
		return n, io.EOF
	}
	return n, nil
}

func (r *storageVideoReader) readChunk(start int64) error {
	count := min(r.chunk, r.source.SizeBytes-start)
	end := start + count - 1
	for attempt := 0; attempt < maxVideoSourceAttempts; attempt++ {
		route := "apteva"
		if attempt > 0 {
			route = "proxy"
		}
		if r.url == "" || attempt > 0 {
			url, err := mintVideoSourceURL(r.app, r.item, r.source.ProjectID, route, &r.source)
			if err != nil {
				return err
			}
			r.url = url
		}
		request, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
		if err != nil {
			return &sourceDeliveryError{Code: "source_changed", Reason: "invalid version-bound source URL"}
		}
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		request.Header.Set("Accept-Encoding", "identity")
		request.Header.Set("X-Storage-Expected-Content-Revision", r.source.ContentRevision)
		client := *mediaHTTPClient
		client.Timeout = 90 * time.Second
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		response, err := client.Do(request)
		if err != nil {
			if r.ctx.Err() != nil {
				return r.ctx.Err()
			}
			continue
		}
		if sourceStatusRecoverable(response.StatusCode) {
			response.Body.Close()
			continue
		}
		if response.StatusCode == http.StatusPreconditionFailed || response.StatusCode == http.StatusNotFound {
			response.Body.Close()
			return &sourceDeliveryError{Code: "source_changed", Reason: "Storage no longer serves the approved object version"}
		}
		if response.StatusCode != http.StatusPartialContent {
			response.Body.Close()
			return &sourceDeliveryError{Code: "invalid_source_range", Reason: "Storage did not return a partial response for the requested version"}
		}
		if response.Header.Get("X-Storage-Content-Revision") != r.source.ContentRevision ||
			response.Header.Get("X-Storage-File-ID") != strconv.FormatInt(r.source.FileID, 10) {
			response.Body.Close()
			return &sourceDeliveryError{Code: "source_changed", Reason: "Storage response does not identify the approved object"}
		}
		contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
		expectedRange := fmt.Sprintf("bytes %d-%d/%d", start, end, r.source.SizeBytes)
		if response.Header.Get("Content-Range") != expectedRange ||
			response.ContentLength >= 0 && response.ContentLength != count ||
			response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity" ||
			canonicalVideoMIME(contentType) != canonicalVideoMIME(r.source.ContentType) {
			response.Body.Close()
			return &sourceDeliveryError{Code: "invalid_source_range", Reason: "Storage returned an incorrect range, size, or media type"}
		}
		if cap(r.cache) < int(count) {
			r.cache = make([]byte, count)
		} else {
			r.cache = r.cache[:count]
		}
		_, err = io.ReadFull(response.Body, r.cache)
		var extra [1]byte
		if err == nil {
			var n int
			n, err = io.ReadFull(response.Body, extra[:])
			if n != 0 || !errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			} else {
				err = nil
			}
		}
		response.Body.Close()
		if err != nil {
			r.cache = r.cache[:0]
			return &sourceDeliveryError{Code: "source_truncated", Reason: "Storage did not complete the exact requested range", Transient: true}
		}
		r.start = start
		return nil
	}
	return &sourceDeliveryError{Reason: "version-bound Storage request failed after URL renewal", Transient: true}
}

func (a *App) prepareStorageVideo(ctx *sdk.AppCtx, j publishJob, item mediaItem, d *deliveryDiagnostics) (*preparedVideo, error) {
	if item.Source == nil {
		return nil, nil
	}
	source := *item.Source
	if !source.validFor(item, d.MediaProjectID, d.StorageInstallID) {
		return nil, &sourceDeliveryError{Code: "source_changed", Reason: "Storage source descriptor differs from the approved media identity"}
	}
	// Refresh the descriptor before trusting cached media metadata. Byte reads
	// are then bound by Storage to this same immutable content revision.
	var metadata struct {
		Source storageSourceField `json:"source"`
		File   struct {
			Source storageSourceField `json:"source"`
		} `json:"file"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get", map[string]any{"id": item.ID, "_project_id": d.MediaProjectID}, &metadata); err != nil {
		var sourceError *sourceDeliveryError
		if errors.As(err, &sourceError) {
			return nil, err
		}
		return nil, &sourceDeliveryError{Reason: "Storage source identity could not be refreshed", Transient: true}
	}
	fresh, err := storageResponseSource(metadata.Source, metadata.File.Source)
	if err != nil {
		return nil, err
	}
	if fresh == nil || *fresh != source {
		return nil, &sourceDeliveryError{Code: "source_changed", Reason: "Storage source identity changed during preparation"}
	}
	chunk, err := videoRangeChunkSize(ctx)
	if err != nil {
		return nil, err
	}
	readCtx, cancel := context.WithCancel(context.Background())
	reader := &storageVideoReader{app: ctx, item: item, source: source, ctx: readCtx, cancel: cancel, chunk: chunk}
	p := &preparedVideo{source: reader, Size: source.SizeBytes, SHA256: source.Checksum.Value, MIME: source.ContentType}
	var header [512]byte
	n, err := reader.ReadAt(header[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		p.Close()
		return nil, err
	}
	actual := videoContainerMIME(header[:n], source.SizeBytes)
	if actual == "" || canonicalVideoMIME(actual) != canonicalVideoMIME(source.ContentType) {
		p.Close()
		return nil, &sourceDeliveryError{Reason: "source bytes do not match the verified video's container"}
	}
	p.URL = reader.url
	d.Source = &source
	d.SourceSHA256 = p.SHA256
	d.ExpectedBytes = p.Size
	d.VerifiedSourceBytes = bytePointer(p.Size)
	d.SourceRoute = "version_bound_range"
	d.SourceBytes = int64(len(reader.cache))
	d.SourceHTTPStatus = http.StatusPartialContent
	d.SourceContentType = p.MIME
	if err := verifyRecoveryVideoSource(ctx, j, p.SHA256, &source); err != nil {
		p.Close()
		return nil, err
	}
	if err := d.event(ctx, j.targetID, deliveryEvent{Stage: "source_fetch", Bytes: d.SourceBytes, Result: "verified_storage_source"}); err != nil {
		p.Close()
		return nil, err
	}
	p.Diagnostics = *d
	return p, nil
}

func verifyRecoveryVideoSource(ctx *sdk.AppCtx, j publishJob, checksum string, source *storageVideoSource) error {
	if j.targetID <= 0 {
		return nil
	}
	var proof string
	err := ctx.AppDB().QueryRow(`SELECT r.evidence FROM delivery_recovery_requests r JOIN post_targets t ON t.id=r.target_id JOIN posts p ON p.id=t.post_id WHERE r.project_id=? AND r.target_id=? AND r.status='consumed' AND r.decision IN ('retry','resume') AND r.revision=p.revision AND r.source_attempt=t.attempts-1`, projectScope(ctx), j.targetID).Scan(&proof)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var evidence struct {
		SHA    string              `json:"source_sha256"`
		Source *storageVideoSource `json:"source"`
	}
	if json.Unmarshal([]byte(proof), &evidence) != nil || evidence.SHA == "" || evidence.SHA != checksum ||
		evidence.Source != nil && (source == nil || *source != *evidence.Source) {
		return &sourceDeliveryError{Code: "source_changed", Reason: "source changed after recovery authorization"}
	}
	return nil
}
