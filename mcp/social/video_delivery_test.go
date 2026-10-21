package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// Minimal ISO BMFF ftyp box. This tests transport/container validation, not codec decoding.
var testMP4 = []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0, 'm', 'p', '4', '2', 'i', 's', 'o', 'm'}

type zeroVideoReader struct{}

func (zeroVideoReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
func configureTestVideo(t *testing.T, pf *recordingPlatform, id int64, size int64) string {
	t.Helper()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", fmt.Sprint(size))
		w.Write(testMP4)
		io.CopyN(w, zeroVideoReader{}, size-int64(len(testMP4)))
	}))
	t.Cleanup(source.Close)
	meta, _ := json.Marshal(map[string]any{"id": id, "content_type": "video/mp4", "size_bytes": size})
	loc, _ := json.Marshal(map[string]any{"url": source.URL + "/source?sig=TOPSECRET"})
	pf.callAppResponses["storage:files_get"] = meta
	pf.callAppResponses["storage:files_get_url"] = loc
	return source.URL
}
func testSourceItem() mediaItem {
	return mediaItem{ID: 7, Mime: "video/mp4", Bytes: int64(len(testMP4)), MediaProjectID: "test-proj"}
}
func setSourceURL(pf *recordingPlatform, url string) {
	raw, _ := json.Marshal(map[string]any{"url": url})
	pf.callAppResponses["storage:files_get_url"] = raw
}
func TestVideoSourceRejectsBeforeProviderInitialization(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		mime     string
		body     []byte
		length   int
		checksum string
		calls    int
	}{
		{"protected", 401, "text/html", []byte("forbidden"), 0, "", 3},
		{"expired", 403, "text/html", []byte("expired"), 0, "", 3},
		{"persistent502", 502, "text/html", []byte("gateway error"), 0, "", 3},
		{"html", 200, "text/html", []byte("<html>error</html>"), 0, "", 1},
		{"fake_video", 200, "video/mp4", bytes.Repeat([]byte("x"), 24), 0, "", 1},
		{"truncated", 200, "video/mp4", testMP4[:12], 24, "", 3},
		{"checksum", 200, "video/mp4", testMP4, 0, strings.Repeat("a", 64), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gets atomic.Int64
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gets.Add(1)
				w.Header().Set("Content-Type", tc.mime)
				if tc.length > 0 {
					w.Header().Set("Content-Length", fmt.Sprint(tc.length))
				}
				w.WriteHeader(tc.status)
				w.Write(tc.body)
			}))
			defer source.Close()
			for _, platform := range []string{"youtube", "tiktok", "facebook"} {
				pf := newRecordingPlatform()
				setSourceURL(pf, source.URL+"?sig=secret")
				ctx := newSocialCtx(t, pf)
				item := testSourceItem()
				item.SHA256 = tc.checksum
				j := publishJob{platform: platform, connID: 42, media: []mediaItem{item}, mediaProjectID: "test-proj"}
				before := gets.Load()
				var err error
				switch platform {
				case "youtube":
					_, _, err = (&App{}).publishYoutube(ctx, platforms[platform], j)
				case "tiktok":
					_, _, err = (&App{}).publishTikTokVideo(ctx, platforms[platform], j)
				default:
					_, _, err = (&App{}).publishSingle(ctx, platforms[platform], j)
				}
				if err == nil || len(pf.executeCalls) != 0 {
					t.Fatalf("%s initialized provider on invalid source: %v %+v", platform, err, pf.executeCalls)
				}
				if strings.Contains(err.Error(), "secret") {
					t.Fatal("signed URL leaked", err)
				}
				if gets.Load()-before != int64(tc.calls) {
					t.Fatalf("%s attempts=%d want %d", platform, gets.Load()-before, tc.calls)
				}
			}
		})
	}
}

type sourceRoutePlatform struct {
	*recordingPlatform
	base string
}

func (p *sourceRoutePlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app == "storage" && tool == "files_get_url" {
		p.mu.Lock()
		p.callAppCalls = append(p.callAppCalls, callAppCall{AppName: app, Tool: tool, Input: input})
		p.mu.Unlock()
		route := toString(input["delivery"])
		raw, _ := json.Marshal(map[string]any{"url": p.base + "/" + route + "?sig=fresh"})
		return json.Unmarshal(raw, out)
	}
	return p.recordingPlatform.CallAppResult(app, tool, input, out)
}
func TestVideoSourceProxyFallbackScopeChecksumAndCleanup(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Correlation-ID") == "" {
			t.Error("missing correlation")
		}
		if r.URL.Path == "/apteva" {
			w.WriteHeader(502)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("X-Request-ID", "storage-request-1")
		w.Write(testMP4)
	}))
	defer source.Close()
	pf := &sourceRoutePlatform{newRecordingPlatform(), source.URL}
	ctx := newSocialCtx(t, pf)
	item := testSourceItem()
	digest := sha256.Sum256(testMP4)
	item.SHA256 = hex.EncodeToString(digest[:])
	p, err := (&App{}).prepareVideoSource(ctx, publishJob{mediaProjectID: "test-proj"}, item, "FILE_UPLOAD")
	if err != nil {
		t.Fatal(err)
	}
	name := p.File.Name()
	raw, _ := io.ReadAll(p.File)
	if !bytes.Equal(raw, testMP4) || p.SHA256 != item.SHA256 {
		t.Fatal("different source")
	}
	if p.Diagnostics.SourceHTTPStatus != 200 || p.Diagnostics.SourceBytes != 24 || p.Diagnostics.SourceRoute != "proxy" {
		t.Fatal("missing persistent source summary", p.Diagnostics)
	}
	if len(p.Diagnostics.Events) != 2 || p.Diagnostics.Events[1].Route != "proxy" || p.Diagnostics.Events[1].RequestID != "storage-request-1" {
		t.Fatal(p.Diagnostics)
	}
	for _, call := range pf.callAppCalls {
		if call.Input["id"] != int64(7) || call.Input["_project_id"] != "test-proj" {
			t.Fatal("wrong source identity", call)
		}
	}
	p.Close()
	if _, err = os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("spool retained", err)
	}
	if len(videoSpoolSlots) != 0 {
		t.Fatal("spool capacity leaked")
	}
}
func TestVideoSourceRejectsScopedAndExpiredURLs(t *testing.T) {
	for _, query := range []string{"project_id=another", "exp=1"} {
		t.Run(query, func(t *testing.T) {
			pf := newRecordingPlatform()
			setSourceURL(pf, "https://storage.test/file?"+query)
			ctx := newSocialCtx(t, pf)
			_, err := (&App{}).prepareVideoSource(ctx, publishJob{mediaProjectID: "test-proj"}, testSourceItem(), "FILE_UPLOAD")
			if err == nil || len(pf.executeCalls) > 0 {
				t.Fatal(err)
			}
		})
	}
}
func TestVideoDeliveryRedactsSecrets(t *testing.T) {
	raw := `https://user:pass@host.test/file?sig=SIGNED#secret https://host.test/api/relay/TOKEN/clip.mp4 https://host.test/upload/youtube/v3/SESSION?upload_id=PRIVATE {"access_token":"TOKENVALUE","authorization":"Bearer BEARERVALUE","session_url":"https://host.test/upload/PRIVATE"}`
	cleaned := redactDeliverySecrets(raw)
	for _, secret := range []string{"user", "pass", "SIGNED", "TOKEN", "SESSION", "PRIVATE", "BEARERVALUE"} {
		if strings.Contains(cleaned, secret) {
			t.Fatalf("leaked %s: %s", secret, cleaned)
		}
	}
	var value any
	if err := json.Unmarshal([]byte(redactDeliverySecrets(`{"access_token":"SECRET","url":"https://host.test/file?sig=SECRET"}`)), &value); err != nil {
		t.Fatal("redaction corrupted JSON", err)
	}
}

func TestVideoContainerRejectsTruncatedBoxAndSupportsMIMEAliases(t *testing.T) {
	if got := videoContainerMIME(testMP4[:12], 12); got != "" {
		t.Fatal("accepted truncated ftyp", got)
	}
	if got := videoContainerMIME(testMP4, 24); got != "video/mp4" {
		t.Fatal(got)
	}
	if canonicalVideoMIME("video/x-m4v") != "video/mp4" || canonicalVideoMIME("video/x-msvideo") != "video/avi" {
		t.Fatal("valid MIME alias rejected")
	}
}
