package downloads

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

const (
	DefaultMaxFileBytes    = int64(64 << 20)
	DefaultMaxSessionBytes = int64(256 << 20)
	DefaultMaxFiles        = 32
)

var ErrClosed = errors.New("session_closed")
var ErrNotFound = errors.New("download_not_found_or_not_owned")

type OpenFunc func(context.Context, string, computer.Download) (io.ReadCloser, error)

type Options struct {
	Directory       string
	DownloadPath    string
	Behavior        cdpbrowser.SetDownloadBehaviorBehavior
	Open            OpenFunc
	Cancel          func(string)
	Lifetime        context.Context
	MaxFileBytes    int64
	MaxSessionBytes int64
	MaxFiles        int
}

type entry struct {
	meta         computer.Download
	guid         string
	providerPath string
	sequence     uint64
}

// Tracker turns Chrome's browser-level download events into a session-scoped
// lifecycle. The disk/provider path never leaves this package.
type Tracker struct {
	attached        map[context.Context]context.CancelFunc
	lifetimeOnce    sync.Once
	mu              sync.Mutex
	byID            map[string]*entry
	byGUID          map[string]*entry
	order           []string
	sequence        uint64
	changed         chan struct{}
	closed          bool
	directory       string
	downloadPath    string
	behavior        cdpbrowser.SetDownloadBehaviorBehavior
	opener          OpenFunc
	cancelDownload  func(string)
	lifetime        context.Context
	maxFileBytes    int64
	maxSessionBytes int64
	maxFiles        int
	completedBytes  int64
}

func New(opts Options) *Tracker {
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = DefaultMaxFileBytes
	}
	if opts.MaxSessionBytes <= 0 {
		opts.MaxSessionBytes = DefaultMaxSessionBytes
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = DefaultMaxFiles
	}
	if opts.Behavior == "" {
		opts.Behavior = cdpxAllowAndName()
	}
	return &Tracker{
		byID: make(map[string]*entry), byGUID: make(map[string]*entry), changed: make(chan struct{}),
		directory: opts.Directory, downloadPath: opts.DownloadPath, behavior: opts.Behavior, opener: opts.Open,
		cancelDownload: opts.Cancel, lifetime: opts.Lifetime,
		maxFileBytes: opts.MaxFileBytes, maxSessionBytes: opts.MaxSessionBytes, maxFiles: opts.MaxFiles,
	}
}

// Attach enables downloads and subscribes before the page can dispatch any
// download. Browser-level events cover navigations, POST responses and blob URLs.
func (t *Tracker) Attach(ctx context.Context) error {
	path := t.downloadPath
	if path == "" {
		path = t.directory
	}
	if path == "" {
		return errors.New("download path is required")
	}
	// Chrome emits Browser.download* events on the target event stream used by
	// chromedp (the same wiring as chromedp's own download integration test).
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return ErrClosed
	}
	if t.attached == nil {
		t.attached = make(map[context.Context]context.CancelFunc)
	}
	if t.attached[ctx] != nil {
		t.mu.Unlock()
		return nil
	}
	listenerCtx, listenerCancel := context.WithCancel(ctx)
	t.attached[ctx] = listenerCancel
	t.mu.Unlock()
	chromedp.ListenTarget(listenerCtx, t.handleEvent)
	t.mu.Lock()
	if t.cancelDownload == nil {
		t.cancelDownload = func(guid string) {
			cancelCtx, cancel := context.WithTimeout(t.lifetimeOr(ctx), 5*time.Second)
			defer cancel()
			if c := chromedp.FromContext(ctx); c != nil && c.Browser != nil {
				_ = cdpbrowser.CancelDownload(guid).Do(cdp.WithExecutor(cancelCtx, c.Browser))
			}
		}
	}
	t.mu.Unlock()
	if err := cdputil.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpxSetBehavior(t.behavior, path).Do(ctx)
	})); err != nil {
		t.Detach(ctx)
		return err
	}
	if t.lifetime != nil {
		t.lifetimeOnce.Do(func() {
			go func() {
				<-t.lifetime.Done()
				_ = t.Close()
			}()
		})
	}
	return nil
}

func (t *Tracker) lifetimeOr(ctx context.Context) context.Context {
	if t.lifetime != nil {
		return t.lifetime
	}
	return ctx
}

func cdpxAllowAndName() cdpbrowser.SetDownloadBehaviorBehavior {
	return cdpbrowser.SetDownloadBehaviorBehaviorAllowAndName
}

func cdpxSetBehavior(behavior cdpbrowser.SetDownloadBehaviorBehavior, path string) *cdpbrowser.SetDownloadBehaviorParams {
	return cdpbrowser.SetDownloadBehavior(behavior).WithDownloadPath(path).WithEventsEnabled(true)
}

func (t *Tracker) handleEvent(ev any) {
	switch event := ev.(type) {
	case *cdpbrowser.EventDownloadWillBegin:
		t.started(event.GUID, event.SuggestedFilename, event.URL)
	case *cdpbrowser.EventDownloadProgress:
		t.progress(event.GUID, int64(event.ReceivedBytes), int64(event.TotalBytes), string(event.State), event.FilePath)
	}
}

func (t *Tracker) started(guid, filename, source string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || guid == "" || strings.ContainsAny(guid, `/\\`) || guid == "." || guid == ".." || t.byGUID[guid] != nil {
		return
	}
	if len(t.order) >= t.maxFiles+1 {
		t.cancelLocked(guid)
		return
	}
	t.sequence++
	now := time.Now().UTC()
	item := &entry{guid: guid, sequence: t.sequence, meta: computer.Download{
		OriginalFilename: filename, ID: newID(), Filename: SanitizeFilename(filename), MIMEType: mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))),
		Status: computer.DownloadInProgress, SourceOrigin: sourceOrigin(source), CreatedAt: now,
	}}
	if t.behavior == cdpbrowser.SetDownloadBehaviorBehaviorAllowAndName && t.downloadPath != "" {
		item.providerPath = strings.TrimRight(t.downloadPath, "/") + "/" + guid
	}
	t.byGUID[guid] = item
	t.byID[item.meta.ID] = item
	t.order = append(t.order, item.meta.ID)
	if len(t.order) > t.maxFiles {
		item.meta.Status = computer.DownloadFailed
		item.meta.ErrorCode = "download_session_limit_exceeded"
		item.meta.CompletedAt = &now
		t.cancelLocked(guid)
	}
	t.notifyLocked()
}

func (t *Tracker) progress(guid string, received, total int64, state, filePath string) {
	t.mu.Lock()
	item := t.byGUID[guid]
	if item == nil || t.closed {
		t.mu.Unlock()
		return
	}
	item.meta.ReceivedBytes = received
	if total > 0 {
		item.meta.Size = total
	}
	if filePath != "" {
		item.providerPath = filePath
	}
	if item.meta.Status == computer.DownloadFailed {
		t.notifyLocked()
		t.mu.Unlock()
		return
	}
	if total > t.maxFileBytes || received > t.maxFileBytes {
		item.meta.Status = computer.DownloadFailed
		item.meta.ErrorCode = "download_size_limit_exceeded"
		now := time.Now().UTC()
		item.meta.CompletedAt = &now
		t.cancelLocked(guid)
		t.notifyLocked()
		t.mu.Unlock()
		return
	}
	observedBytes := t.completedBytes
	for _, active := range t.byID {
		if active.meta.Status == computer.DownloadInProgress {
			observedBytes += active.meta.ReceivedBytes
		}
	}
	if observedBytes > t.maxSessionBytes {
		item.meta.Status = computer.DownloadFailed
		item.meta.ErrorCode = "download_session_limit_exceeded"
		now := time.Now().UTC()
		item.meta.CompletedAt = &now
		t.cancelLocked(guid)
		t.notifyLocked()
		t.mu.Unlock()
		return
	}
	switch state {
	case "canceled":
		now := time.Now().UTC()
		item.meta.Status = computer.DownloadCancelled
		item.meta.ErrorCode = "download_cancelled"
		item.meta.CompletedAt = &now
		t.notifyLocked()
		t.mu.Unlock()
	case "completed":
		t.mu.Unlock()
		if t.directory != "" {
			t.finalizeLocal(item.meta.ID)
			return
		}
		t.mu.Lock()
		if current := t.byID[item.meta.ID]; current != nil && current.meta.Status == computer.DownloadInProgress {
			size := current.meta.Size
			if size <= 0 {
				size = current.meta.ReceivedBytes
			}
			if t.completedBytes+size > t.maxSessionBytes {
				current.meta.Status = computer.DownloadFailed
				current.meta.ErrorCode = "download_session_limit_exceeded"
			} else {
				t.completedBytes += size
				now := time.Now().UTC()
				current.meta.Status = computer.DownloadCompleted
				current.meta.Size = size
				current.meta.CompletedAt = &now
			}
			t.notifyLocked()
		}
		t.mu.Unlock()
	default:
		t.notifyLocked()
		t.mu.Unlock()
	}
}

func (t *Tracker) finalizeLocal(id string) {
	t.mu.Lock()
	item := t.byID[id]
	if item == nil || t.closed {
		t.mu.Unlock()
		return
	}
	// allowAndName fixes the disk path to our private directory plus Chrome's
	// GUID. Do not trust a path carried by an event when reading local files.
	path := filepath.Join(t.directory, item.guid)
	t.mu.Unlock()

	var file *os.File
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		file, err = os.Open(path)
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.fail(id, "download_bytes_unavailable")
		return
	}
	defer file.Close()
	hash := sha256.New()
	first := make([]byte, 512)
	n, readErr := io.ReadFull(file, first)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		t.fail(id, "download_bytes_unavailable")
		return
	}
	if _, err := hash.Write(first[:n]); err != nil {
		t.fail(id, "download_bytes_unavailable")
		return
	}
	written, err := io.Copy(hash, io.LimitReader(file, t.maxFileBytes+1-int64(n)))
	size := int64(n) + written
	if err != nil {
		t.fail(id, "download_bytes_unavailable")
		return
	}
	if size > t.maxFileBytes {
		_ = os.Remove(path)
		t.fail(id, "download_size_limit_exceeded")
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	item = t.byID[id]
	if item == nil || t.closed || item.meta.Status != computer.DownloadInProgress {
		return
	}
	if t.completedBytes+size > t.maxSessionBytes {
		item.meta.Status = computer.DownloadFailed
		item.meta.ErrorCode = "download_session_limit_exceeded"
		_ = os.Remove(path)
	} else {
		t.completedBytes += size
		now := time.Now().UTC()
		item.providerPath = path
		item.meta.Status = computer.DownloadCompleted
		item.meta.Size = size
		item.meta.ReceivedBytes = size
		item.meta.SHA256 = hex.EncodeToString(hash.Sum(nil))
		if item.meta.MIMEType == "" || item.meta.MIMEType == "application/octet-stream" {
			item.meta.MIMEType = http.DetectContentType(first[:n])
		}
		item.meta.CompletedAt = &now
	}
	t.notifyLocked()
}

func (t *Tracker) fail(id, code string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if item := t.byID[id]; item != nil && item.meta.Status == computer.DownloadInProgress {
		now := time.Now().UTC()
		item.meta.Status = computer.DownloadFailed
		item.meta.ErrorCode = code
		item.meta.CompletedAt = &now
		t.notifyLocked()
	}
}

func (t *Tracker) DownloadEventCursor() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sequence
}

func (t *Tracker) DownloadsStartedSince(cursor uint64) []computer.Download {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []computer.Download
	for _, id := range t.order {
		item := t.byID[id]
		if item != nil && item.sequence > cursor {
			out = append(out, item.meta)
		}
	}
	return out
}

func (t *Tracker) ListDownloads(context.Context) ([]computer.Download, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, ErrClosed
	}
	out := make([]computer.Download, 0, len(t.order))
	for _, id := range t.order {
		if item := t.byID[id]; item != nil {
			out = append(out, item.meta)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (t *Tracker) WaitForDownload(ctx context.Context, id string) (computer.Download, error) {
	for {
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return computer.Download{}, ErrClosed
		}
		item := t.byID[id]
		if item == nil {
			t.mu.Unlock()
			return computer.Download{}, ErrNotFound
		}
		meta := item.meta
		changed := t.changed
		t.mu.Unlock()
		if meta.Status != computer.DownloadInProgress {
			return meta, nil
		}
		select {
		case <-ctx.Done():
			return meta, ctx.Err()
		case <-changed:
		}
	}
}

func (t *Tracker) OpenDownload(ctx context.Context, id string) (io.ReadCloser, computer.Download, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, computer.Download{}, ErrClosed
	}
	item := t.byID[id]
	if item == nil {
		t.mu.Unlock()
		return nil, computer.Download{}, ErrNotFound
	}
	meta, path := item.meta, item.providerPath
	if meta.Status != computer.DownloadCompleted {
		t.mu.Unlock()
		return nil, meta, fmt.Errorf("download_not_ready: status=%s", meta.Status)
	}
	if path == "" && t.directory != "" {
		path = filepath.Join(t.directory, item.guid)
	}
	opener := t.opener
	t.mu.Unlock()
	if opener != nil {
		reader, err := opener(ctx, path, meta)
		return reader, meta, err
	}
	reader, err := os.Open(path)
	return reader, meta, err
}

func (t *Tracker) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	for _, cancel := range t.attached {
		cancel()
	}
	t.attached = nil
	t.notifyLocked()
	dir := t.directory
	t.mu.Unlock()
	if dir != "" {
		return os.RemoveAll(dir)
	}
	return nil
}

func (t *Tracker) notifyLocked() {
	close(t.changed)
	t.changed = make(chan struct{})
}

func (t *Tracker) cancelLocked(guid string) {
	if t.cancelDownload != nil {
		go t.cancelDownload(guid)
	}
}

func newID() string {
	var value [12]byte
	_, _ = rand.Read(value[:])
	return "dl_" + hex.EncodeToString(value[:])
}

func SanitizeFilename(value string) string {
	value = filepath.Base(strings.ReplaceAll(value, "\\", "/"))
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == 0 || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." {
		return "download"
	}
	if len(value) > 180 {
		ext := filepath.Ext(value)
		base := strings.TrimSuffix(value, ext)
		limit := 180 - len(ext)
		if limit < 1 {
			limit = 180
			ext = ""
		}
		for len(base) > limit {
			runes := []rune(base)
			if len(runes) == 0 {
				break
			}
			base = string(runes[:len(runes)-1])
		}
		value = base + ext
	}
	return value
}

func sourceOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func (t *Tracker) Detach(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cancel := t.attached[ctx]; cancel != nil {
		cancel()
		delete(t.attached, ctx)
	}
}
