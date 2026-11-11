package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestStorageMetadataLegacyProvenanceStagesAndVerifies(t *testing.T) {
	for _, location := range []string{"flat", "file"} {
		for _, source := range []string{`"human"`, `"media-render"`, `null`, ``} {
			t.Run(location+source, func(t *testing.T) {
				pf := newRecordingPlatform()
				ctx := newSocialCtx(t, pf)
				configureTestVideo(t, pf, 7, 24)
				hash := sha256.Sum256(testMP4)
				file := map[string]any{"id": 7, "content_type": "video/mp4", "size_bytes": 24, "sha256": hex.EncodeToString(hash[:])}
				if source != "" {
					file["source"] = json.RawMessage(source)
				}
				var metadata any = file
				if location == "file" {
					metadata = map[string]any{"file": file}
				}
				pf.callAppResponses["storage:files_get"], _ = json.Marshal(metadata)
				// Signed URL responses may also carry provenance, both initially
				// and when full staging renews a URL.
				var signed map[string]any
				_ = json.Unmarshal(pf.callAppResponses["storage:files_get_url"], &signed)
				if source != "" {
					signed["source"] = json.RawMessage(source)
				}
				pf.callAppResponses["storage:files_get_url"], _ = json.Marshal(signed)
				items, err := (&App{}).resolveMedia(ctx, []int64{7}, "test-proj")
				if err != nil {
					t.Fatal(err)
				}
				if len(items) != 1 || items[0].Source != nil {
					t.Fatalf("provenance became descriptor: %+v", items)
				}
				p, err := (&App{}).prepareVideoSource(ctx, publishJob{mediaProjectID: "test-proj"}, items[0], "FILE_UPLOAD")
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close()
				if p.File == nil || p.source != nil || p.Size != 24 || p.SHA256 != hex.EncodeToString(hash[:]) || p.Diagnostics.Source != nil || p.Diagnostics.VerifiedSourceBytes == nil || *p.Diagnostics.VerifiedSourceBytes != 24 {
					t.Fatalf("fallback was not fully verified: %+v", p.Diagnostics)
				}
				if len(pf.executeCalls) != 0 {
					t.Fatal("verification initialized provider")
				}
			})
		}
	}
}

func TestStorageMetadataDescriptorObjectsAreStrict(t *testing.T) {
	for _, raw := range []string{`true`, `7`, `[]`, `{}`, `{"schema":"other/v1"}`, `{"schema":"storage-source/v1"}`, `{"schema":17}`} {
		t.Run(raw, func(t *testing.T) {
			var field storageSourceField
			err := json.Unmarshal([]byte(raw), &field)
			var sourceErr *sourceDeliveryError
			if !errors.As(err, &sourceErr) || sourceErr.Code != "invalid_source_descriptor" || field.Descriptor != nil {
				t.Fatalf("silently downgraded %s: %v", raw, err)
			}
		})
	}
	f := newRangeVideoFixture(t, 24)
	for _, change := range []string{"schema", "install", "project", "file", "revision", "size", "mime", "algorithm", "digest", "status"} {
		t.Run(change, func(t *testing.T) {
			s := f.source
			switch change {
			case "schema":
				s.Schema = "storage-source/v2"
			case "install":
				s.StorageInstallID = 0
			case "project":
				s.ProjectID = ""
			case "file":
				s.FileID = 0
			case "revision":
				s.ContentRevision = "bad revision"
			case "size":
				s.SizeBytes = 0
			case "mime":
				s.ContentType = "image/png"
			case "algorithm":
				s.Checksum.Algorithm = "etag"
			case "digest":
				s.Checksum.Value = "backend-etag"
			case "status":
				s.Checksum.Status = "pending"
			}
			raw, _ := json.Marshal(s)
			var field storageSourceField
			if err := json.Unmarshal(raw, &field); err == nil {
				t.Fatalf("invalid advertised descriptor accepted: %+v", s)
			}
		})
	}
}

func TestStorageMetadataLegacyCannotBypassFullVerification(t *testing.T) {
	for _, scenario := range []string{"checksum", "truncated", "container", "malformed URL descriptor"} {
		t.Run(scenario, func(t *testing.T) {
			pf := newRecordingPlatform()
			ctx := newSocialCtx(t, pf)
			configureTestVideo(t, pf, 7, 24)
			hash := sha256.Sum256(testMP4)
			file := map[string]any{"id": 7, "source": "human", "content_type": "video/mp4", "size_bytes": 24, "sha256": hex.EncodeToString(hash[:])}
			switch scenario {
			case "checksum":
				file["sha256"] = strings.Repeat("a", 64)
			case "truncated":
				file["size_bytes"] = 25
			case "container":
				file["content_type"] = "video/webm"
			case "malformed URL descriptor":
				var signed map[string]any
				_ = json.Unmarshal(pf.callAppResponses["storage:files_get_url"], &signed)
				signed["file"] = map[string]any{"source": map[string]any{}}
				pf.callAppResponses["storage:files_get_url"], _ = json.Marshal(signed)
			}
			pf.callAppResponses["storage:files_get"], _ = json.Marshal(map[string]any{"file": file})
			items, err := (&App{}).resolveMedia(ctx, []int64{7}, "test-proj")
			if err == nil {
				var prepared *preparedVideo
				prepared, err = (&App{}).prepareVideoSource(ctx, publishJob{mediaProjectID: "test-proj"}, items[0], "FILE_UPLOAD")
				if prepared != nil {
					prepared.Close()
				}
			}
			if err == nil || len(pf.executeCalls) != 0 {
				t.Fatalf("legacy %s bypassed verification: %v", scenario, err)
			}
		})
	}
}

func TestStorageMetadataValidDescriptorsAndIdentityMismatches(t *testing.T) {
	for _, scenario := range []string{"nested", "top with provenance", "descriptor checksum only", "conflicting descriptors", "wrong file", "wrong checksum", "wrong metadata id", "invalid URL descriptor", "invalid refreshed descriptor"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRangeVideoFixture(t, 24)
			file := map[string]any{"id": 7, "content_type": "video/mp4", "size_bytes": 24, "sha256": f.source.Checksum.Value, "source": f.source}
			meta := map[string]any{"file": file}
			switch scenario {
			case "top with provenance":
				meta["source"] = f.source
				file["source"] = "human"
			case "descriptor checksum only":
				delete(file, "sha256")
			case "conflicting descriptors":
				other := f.source
				other.ContentRevision = "other"
				meta["source"] = other
			case "wrong file":
				other := f.source
				other.FileID = 8
				file["source"] = other
			case "wrong checksum":
				file["sha256"] = strings.Repeat("a", 64)
			case "wrong metadata id":
				file["id"] = 8
			case "invalid URL descriptor":
				f.platform.callAppResponses["storage:files_get_url"] = json.RawMessage(`{"url":"https://example.com/movie","file_id":7,"source":{}}`)
			}
			f.platform.callAppResponses["storage:files_get"], _ = json.Marshal(meta)
			items, err := (&App{}).resolveMedia(f.ctx, []int64{7}, "test-proj")
			if scenario == "nested" || scenario == "top with provenance" || scenario == "descriptor checksum only" || scenario == "invalid refreshed descriptor" {
				if err != nil || items[0].Source == nil {
					t.Fatalf("valid descriptor: %+v %v", items, err)
				}
				if scenario == "invalid refreshed descriptor" {
					file["source"] = map[string]any{}
					f.platform.callAppResponses["storage:files_get"], _ = json.Marshal(meta)
				}
				p, err := (&App{}).prepareVideoSource(f.ctx, publishJob{mediaProjectID: "test-proj"}, items[0], "FILE_UPLOAD")
				if scenario == "invalid refreshed descriptor" {
					if err == nil {
						p.Close()
						t.Fatal("refresh silently downgraded")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close()
				if p.source == nil || p.File != nil {
					t.Fatal("valid descriptor did not stream")
				}
			} else if err == nil {
				t.Fatalf("source mismatch accepted: %s", scenario)
			}
			if len(f.platform.executeCalls) != 0 {
				t.Fatal("bad metadata initialized provider")
			}
		})
	}
}

func metadataRecoveryFixture(t *testing.T) (*App, *sdk.AppCtx, *membershipPlatform, int64, int64, int64) {
	t.Helper()
	a, ctx, pf, post, target, sibling := recoveryFixture(t, "youtube")
	if _, err := ctx.AppDB().Exec(`UPDATE post_targets SET failure_code='validation_error',last_error='attached media could not be resolved: storage files_get(7): json: cannot unmarshal string into Go struct field .file.source of type main.storageVideoSource',options='{"visibility":"private"}',delivery_diagnostics='{"operation_state":"not_started"}' WHERE id=?`, target); err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	_ = json.Unmarshal(pf.callAppResponses["storage:files_get"], &file)
	if wrapped, ok := file["file"].(map[string]any); ok {
		wrapped["source"] = "human"
	} else {
		file["source"] = "human"
	}
	pf.callAppResponses["storage:files_get"], _ = json.Marshal(file)
	return a, ctx, pf, post, target, sibling
}

func TestStorageMetadataRecoveryReusesOriginalPrivateTarget(t *testing.T) {
	a, ctx, pf, post, target, sibling := metadataRecoveryFixture(t)
	before := rowSnapshot(t, ctx, "post_targets", sibling)
	var uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads.Add(1)
		body, _ := io.ReadAll(r.Body)
		if string(body) != string(testMP4) {
			t.Error("upload did not contain verified source")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"private-preparation"}`))
	}))
	defer server.Close()
	oldClient := youtubeVideoUploadClient
	origin, _ := url.Parse(server.URL)
	youtubeVideoUploadClient = &http.Client{Transport: rewriteUploadTransport{http.DefaultTransport, origin}}
	defer func() { youtubeVideoUploadClient = oldClient }()
	pf.executeResponses["upload_video_init"] = &sdk.ExecuteResult{Success: true, Status: 200, Headers: map[string]string{"Location": testYouTubeSession}, Data: json.RawMessage(`{}`)}
	grant := mustRevalidate(t, a, ctx, post, target)
	if grant["decision"] != "retry" || grant["recovery_kind"] != "storage_metadata_compatibility" || grant["historical_failure"] == nil || len(pf.executeCalls) != 0 {
		t.Fatalf("%+v calls=%+v", grant, pf.executeCalls)
	}
	if _, err := a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, grant)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, grant)); err != nil {
		t.Fatal(err)
	}
	if uploads.Load() != 1 || len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "upload_video_init" {
		t.Fatalf("repeated upload: %d %+v", uploads.Load(), pf.executeCalls)
	}
	status, ok := pf.executeCalls[0].Input["status"].(map[string]any)
	if !ok || status["privacyStatus"] != "private" {
		t.Fatalf("private visibility changed: %+v", pf.executeCalls[0].Input)
	}
	if rowSnapshot(t, ctx, "post_targets", sibling) != before {
		t.Fatal("successful sibling repeated or changed")
	}
	var identity, audience, options, reason string
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT platform_post_id,audience_status,options FROM post_targets WHERE id=?`, target).Scan(&identity, &audience, &options); err != nil {
		t.Fatal(err)
	}
	if identity != "private-preparation" || audience != "not_required" || options != `{"visibility":"private"}` {
		t.Fatalf("private preparation became membership: %s %s %s", identity, audience, options)
	}
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets WHERE post_id=?`, post).Scan(&count); err != nil || count != 2 {
		t.Fatal("duplicate Social target")
	}
	if err := ctx.AppDB().QueryRow(`SELECT reason FROM target_delivery_attempts WHERE target_id=?`, target).Scan(&reason); err != nil || !strings.Contains(reason, "cannot unmarshal string") {
		t.Fatal("historical metadata failure lost", reason, err)
	}
}

func TestStorageMetadataRecoveryBlocksUnprovenOutcomesAndChangedSource(t *testing.T) {
	for _, scenario := range []string{"wrong file in error", "unrelated validation", "submitted", "operation", "session", "receipt", "pending diagnostics", "malformed descriptor", "bad checksum", "changed original size", "changed original digest", "stale grant"} {
		t.Run(scenario, func(t *testing.T) {
			a, ctx, pf, post, target, _ := metadataRecoveryFixture(t)
			switch scenario {
			case "wrong file in error":
				_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET last_error=REPLACE(last_error,'(7)','(8)') WHERE id=?`, target)
			case "unrelated validation":
				_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET last_error='invalid video metadata' WHERE id=?`, target)
			case "submitted":
				_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET submitted_at=CURRENT_TIMESTAMP WHERE id=?`, target)
			case "operation":
				_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET publish_operation_id='uncertain' WHERE id=?`, target)
			case "session":
				_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET youtube_upload_session='https://evil.example/session' WHERE id=?`, target)
			case "receipt":
				_, _ = ctx.AppDB().Exec(`INSERT INTO delivery_upload_operations(project_id,target_id,operation_id,correlation_id,source_attempt,initialized_at,uploader_state,provenance) VALUES('test-proj',?,'op','corr',1,CURRENT_TIMESTAMP,'stopped','social_upload_receipt')`, target)
			case "pending diagnostics":
				_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET delivery_diagnostics='{"operation_state":"pending"}' WHERE id=?`, target)
			case "malformed descriptor":
				pf.callAppResponses["storage:files_get"] = json.RawMessage(`{"id":7,"source":{},"content_type":"video/mp4","size_bytes":24}`)
			case "bad checksum":
				pf.callAppResponses["storage:files_get"], _ = json.Marshal(map[string]any{"id": 7, "source": "human", "content_type": "video/mp4", "size_bytes": 24, "sha256": strings.Repeat("a", 64)})
			case "changed original size":
				_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET delivery_diagnostics='{"operation_state":"not_started","expected_bytes":25}' WHERE id=?`, target)
			case "changed original digest":
				_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET delivery_diagnostics=? WHERE id=?`, `{"operation_state":"not_started","source_sha256":"`+strings.Repeat("a", 64)+`"}`, target)
			}
			grant := mustRevalidate(t, a, ctx, post, target)
			if scenario == "stale grant" {
				if grant["decision"] != "retry" {
					t.Fatal(grant)
				}
				_, _ = ctx.AppDB().Exec(`UPDATE posts SET media_storage_ids='[8]' WHERE id=?`, post)
				if _, err := a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, grant)); err != nil {
					t.Fatal(err)
				}
			} else if grant["decision"] == "retry" || grant["decision"] == "resume" {
				t.Fatalf("unsafe recovery: %+v", grant)
			}
			if scenario == "changed original digest" {
				second := mustRevalidate(t, a, ctx, post, target)
				if second["decision"] == "retry" {
					t.Fatal("failed validation erased the original source digest")
				}
			}
			if len(pf.executeCalls) != 0 {
				t.Fatal("blocked metadata recovery initialized upload")
			}
		})
	}
}
