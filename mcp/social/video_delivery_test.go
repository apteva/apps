package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
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

	sdk "github.com/apteva/app-sdk"
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

type videoCoverPlatform struct {
	*recordingPlatform
	coverURL string
}

func (p videoCoverPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app == "storage" && input["id"] == int64(8) {
		switch tool {
		case "files_get":
			return json.Unmarshal([]byte(`{"id":8,"content_type":"image/jpeg"}`), out)
		case "files_get_url":
			return json.Unmarshal(fmt.Appendf(nil, `{"url":%q}`, p.coverURL), out)
		}
	}
	return p.recordingPlatform.CallAppResult(app, tool, input, out)
}

func TestPublishFacebookVideoWithThumbnail(t *testing.T) {
	for _, page := range []string{"fb-page", "different-page"} {
		t.Run(page, func(t *testing.T) {
			const id = "fb-video"
			image := []byte{0xff, 0xd8, 0xff, 0xe0}
			cover := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "image/jpeg")
				w.Write(image)
			}))
			defer cover.Close()
			pf := newRecordingPlatform()
			configureTestVideo(t, pf, 7, int64(len(testMP4)))
			pf.executeResponses["post_video"] = graphOK(`{"id":"fb-video"}`)
			pf.executeResponses["facebook_set_video_thumbnail"] = graphOK(`{"success":true}`)
			ctx := newSocialCtx(t, videoCoverPlatform{pf, cover.URL})
			r, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,external_account_id,display_name,status,page_credentials)
				VALUES('test-proj','facebook',42,'fb-page','FB','active','{"access_token":"page-token"}')`)
			if err != nil {
				t.Fatal(err)
			}
			account, _ := r.LastInsertId()
			out, err := (&App{}).toolPostCreate(ctx, map[string]any{
				"mode": "publish", "body": "Facebook video", "media_storage_ids": []any{int64(7)},
				"targets": []any{map[string]any{"social_account_id": account, "thumbnail_storage_id": int64(8)}},
			})
			if err != nil {
				t.Fatal(err)
			}
			post := out.(map[string]any)["post_id"].(int64)
			target := assertYouTubeThumbnailState(t, ctx, post, id, "pending", 1)
			assertYouTubeThumbnailCalls(t, pf, id, "post_video")
			pf.executeQueues["facebook_get_video"] = []*sdk.ExecuteResult{
				graphOK(`{"id":"fb-video","from":{"id":"fb-page"},"status":{"video_status":"processing"}}`),
				graphOK(fmt.Sprintf(`{"id":"fb-video","from":{"id":%q},"status":{"video_status":"ready"}}`, page)),
			}
			runYouTubeThumbnailTick(t, ctx, post)
			assertYouTubeThumbnailState(t, ctx, post, id, "pending", 1)
			assertYouTubeThumbnailCalls(t, pf, id, "post_video", "facebook_get_video")
			runYouTubeThumbnailTick(t, ctx, post)
			if page != "fb-page" {
				assertYouTubeThumbnailState(t, ctx, post, id, "failed", 1)
				assertYouTubeThumbnailCalls(t, pf, id, "post_video", "facebook_get_video", "facebook_get_video")
				return
			}
			if got := assertYouTubeThumbnailState(t, ctx, post, id, "applied", 1); got != target {
				t.Fatalf("thumbnail job changed the target: %d, want %d", got, target)
			}
			runYouTubeThumbnailTick(t, ctx, post)
			assertYouTubeThumbnailCalls(t, pf, id, "post_video", "facebook_get_video", "facebook_get_video", "facebook_set_video_thumbnail")
			input := pf.executeCalls[3].Input
			thumb, _ := input["source"].(map[string]any)
			if input["videoId"] != id || input["source_filename"] != "thumbnail.jpg" ||
				input["access_token"] != "page-token" || input["is_preferred"] != true ||
				thumb["mimeType"] != "image/jpeg" || thumb["base64"] != base64.StdEncoding.EncodeToString(image) {
				t.Fatalf("unexpected multipart input: %+v", input)
			}
		})
	}
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
					_, err = (&App{}).publishSingle(ctx, platforms[platform], j)
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

func TestPostEditFacebookVideoCoverUsesExistingDelivery(t *testing.T) {
	image := []byte{0xff, 0xd8, 0xff, 0xe0}
	cover := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(image)
	}))
	t.Cleanup(cover.Close)
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, videoCoverPlatform{pf, cover.URL})
	app := &App{}
	execID := func(query string, args ...any) int64 {
		t.Helper()
		r, err := ctx.AppDB().Exec(query, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r.LastInsertId()
		return id
	}
	account := execID(`INSERT INTO social_accounts(project_id,platform,connection_id,external_account_id,display_name,status,page_credentials)
		VALUES('test-proj','facebook',42,'123','Page','active','{"access_token":"page-token"}')`)
	post := execID(`INSERT INTO posts(project_id,body,status,media_project_id) VALUES('test-proj','Original caption','published','video-project')`)
	target := execID(`INSERT INTO post_targets(post_id,social_account_id,status,platform_post_id,attempts,options)
		VALUES(?,?,'published','987',1,'{"body":"Original caption","thumbnail_storage_id":7,"thumbnail_project_id":"old-cover-project"}')`, post, account)

	edit := func() targetEditOutcome {
		t.Helper()
		result, err := app.toolPostEdit(ctx, map[string]any{
			"post_id": post,
			"targets": []any{map[string]any{"social_account_id": account, "thumbnail_storage_id": int64(8)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]any)["targets"].([]targetEditOutcome)[0]
	}
	if got := edit(); got.Status != "ok" || got.ThumbnailStatus != "pending" || got.TargetID != target || got.PlatformPostID != "987" {
		t.Fatalf("unexpected edit outcome: %+v", got)
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("cover-only edit called the provider before queuing: %+v", pf.executeCalls)
	}
	var sourceProject string
	if err := ctx.AppDB().QueryRow(`SELECT youtube_thumbnail_project_id FROM post_targets WHERE id=?`, target).Scan(&sourceProject); err != nil || sourceProject != "video-project" {
		t.Fatalf("thumbnail project=%q, err=%v", sourceProject, err)
	}
	pf.executeResponses["facebook_get_video"] = graphOK(`{"id":"987","from":{"id":"123"},"status":{"video_status":"ready"}}`)
	pf.executeResponses["facebook_set_video_thumbnail"] = graphOK(`{"success":true}`)
	runYouTubeThumbnailTick(t, ctx, post)
	runYouTubeThumbnailTick(t, ctx, post)
	if got := assertYouTubeThumbnailState(t, ctx, post, "987", "applied", 1); got != target {
		t.Fatalf("worker changed target: %d, want %d", got, target)
	}
	assertYouTubeThumbnailCalls(t, pf, "987", "facebook_get_video", "facebook_set_video_thumbnail")
	if pf.executeCalls[1].Input["videoId"] != "987" || pf.executeCalls[1].Input["is_preferred"] != true {
		t.Fatalf("unexpected thumbnail write: %+v", pf.executeCalls[1])
	}

	if _, err := ctx.AppDB().Exec(`UPDATE post_targets SET facebook_page_post_id='123_456' WHERE id=?`, target); err != nil {
		t.Fatal(err)
	}
	if got := edit(); got.Status != "unsupported" || len(pf.executeCalls) != 2 {
		t.Fatalf("photo delivery accepted a cover edit: %+v", got)
	}
}
