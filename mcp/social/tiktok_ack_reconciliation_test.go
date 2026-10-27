package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestTikTokSupportedInclusiveAndExclusiveAcknowledgements(t *testing.T) {
	for _, headers := range []http.Header{
		{"Content-Range": []string{"bytes 0-23/24"}},
		{"Content-Range": []string{"bytes 0-24/24"}},
		{"Content-Range": []string{"BYTES=0 - 24 / 24"}},
		{"Range": []string{"bytes=0-23"}},
		{"Range": []string{"bytes=0-24"}},
		{"Content-Range": []string{"bytes 0-24/24"}, "Range": []string{"bytes=0-23"}},
	} {
		t.Run(fmt.Sprint(headers), func(t *testing.T) {
			_, ctx, _, _, target, _ := recoveryFixture(t, "tiktok")
			file, err := os.CreateTemp(t.TempDir(), "source")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			file.Write(testMP4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				for key, values := range headers {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				w.WriteHeader(http.StatusCreated)
			}))
			defer server.Close()
			d := deliveryDiagnostics{CorrelationID: randomDeliveryID(), ProgressVersion: 1, AttemptedBytes: bytePointer(0), AcknowledgedBytes: bytePointer(0), UploadURLSHA256: uploadURLHash(server.URL)}
			if err = uploadTikTokChunks(ctx, target, &preparedVideo{File: file, Size: 24, MIME: "video/mp4"}, &d, server.URL, 24, 1); err != nil || *d.AcknowledgedBytes != 24 || d.TransferredBytes != 24 || d.LatestUploadError != nil {
				t.Fatal("valid acknowledgement rejected", d, err)
			}
		})
	}
}

func TestTikTokNormalizationPreservesRealAcknowledgementErrors(t *testing.T) {
	for _, headers := range []http.Header{
		{"Content-Range": []string{"bytes 0-22/24"}},
		{"Content-Range": []string{"bytes 0-25/24"}},
		{"Content-Range": []string{"bytes 0-24/25"}},
		{"Content-Range": []string{"bytes 1-24/24"}},
		{"Content-Range": []string{"bytes 0-24/*"}},
		{"Content-Range": []string{"bytes 0-24/24", "bytes 0-20/24"}},
		{"Content-Range": []string{"bytes 0-24/24"}, "Range": []string{"bytes=0-22"}},
	} {
		if validateTikTokAcknowledgement(headers, 0, 23, 24) == nil {
			t.Fatal("genuine mismatch accepted", headers)
		}
	}
	for _, headers := range []http.Header{{"Content-Range": []string{"bytes 0-24/36"}}, {"Content-Range": []string{"bytes 12-24/36"}}} {
		if err := validateTikTokAcknowledgement(headers, 12, 23, 36); err != nil {
			t.Fatal("cumulative/chunk exclusive intermediate range rejected", err)
		}
	}
}

func savedExclusiveTikTokACK() deliveryDiagnostics {
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	out := &deliveryOutcome{At: at, HTTPStatus: 201, ExpectedStatus: 201, Result: "invalid_acknowledgement", ContentRange: "bytes 0-15864631/15864632", ResponseRange: "bytes 0-15864632/15864632", ResponseExcerpt: "Content-Range acknowledgement does not match the attempted chunk", RequestID: "saved-request", Chunk: 1}
	return deliveryDiagnostics{CorrelationID: randomDeliveryID(), ProgressVersion: 1, Method: "FILE_UPLOAD", OperationID: "existing-operation", SourceBytes: 15864632, AttemptedBytes: bytePointer(15864632), AcknowledgedBytes: bytePointer(0), LastUpload: out, FirstUploadError: out, LatestUploadError: out, ProviderStatus: "PROCESSING_UPLOAD", OperationState: "pending"}
}

func TestTikTokSaved201WarningReconcilesExistingOperationWithoutResending(t *testing.T) {
	for _, status := range []string{"PROCESSING_UPLOAD", "PUBLISH_COMPLETE"} {
		t.Run(status, func(t *testing.T) {
			a, ctx, pf, post, target, sibling := recoveryFixture(t, "tiktok")
			original := savedExclusiveTikTokACK()
			if err := original.save(ctx, target); err != nil {
				t.Fatal(err)
			}
			data := map[string]any{"status": status, "uploaded_bytes": 15864632}
			if status == "PUBLISH_COMPLETE" {
				data["publicaly_available_post_id"] = []string{"published-tiktok"}
			}
			pf.executeResponses["get_publish_status"] = historyFixtureResult(map[string]any{"data": data, "error": map[string]any{"code": "ok"}})
			d := loadDeliveryDiagnostics(ctx, target)
			if d.UploadAcknowledgementResolution == nil || *d.AcknowledgedBytes != 15864632 || d.LastUpload.Result != "invalid_acknowledgement" || d.FirstUploadError.RequestID != "saved-request" || d.LocalDiagnostic != "" || d.OperationState != "pending" {
				t.Fatal("saved response incorrectly reconciled", d)
			}
			grant := mustRevalidate(t, a, ctx, post, target)
			if _, err := a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, grant)); err != nil {
				t.Fatal(err)
			}
			for _, call := range pf.executeCalls {
				if call.Tool != "get_publish_status" {
					t.Fatal("accepted upload was resubmitted", call)
				}
			}
			d = loadDeliveryDiagnostics(ctx, target)
			if d.UploadAcknowledgementResolution == nil || d.LatestUploadError.Result != "invalid_acknowledgement" || d.FirstUploadError.ResponseRange != original.FirstUploadError.ResponseRange {
				t.Fatal("normalization lost durable original evidence", d)
			}
			var operation, local, siblingStatus string
			ctx.AppDB().QueryRow(`SELECT publish_operation_id,status FROM post_targets WHERE id=?`, target).Scan(&operation, &local)
			ctx.AppDB().QueryRow(`SELECT status FROM post_targets WHERE id=?`, sibling).Scan(&siblingStatus)
			want := "publishing"
			if status == "PUBLISH_COMPLETE" {
				want = "published"
			}
			if operation != "existing-operation" || local != want || siblingStatus != "published" {
				t.Fatal("operation or successful sibling changed", operation, local, siblingStatus)
			}
		})
	}
}

func TestTikTokSavedGenuineErrorsNeverBecomeAcknowledgements(t *testing.T) {
	for _, kind := range []string{"rejected", "short-range", "wrong-total", "missing-source-proof", "missing-prior-chunk"} {
		t.Run(kind, func(t *testing.T) {
			d := savedExclusiveTikTokACK()
			out := *d.LastUpload
			switch kind {
			case "rejected":
				out.HTTPStatus = 404
				out.Result = "http_rejected"
			case "short-range":
				out.ResponseRange = "bytes 0-15864630/15864632"
			case "wrong-total":
				out.ResponseRange = "bytes 0-15864632/15864633"
			case "missing-source-proof":
				d.SourceBytes = 0
			case "missing-prior-chunk":
				out.ContentRange = "bytes 12-15864631/15864632"
				out.ResponseRange = "bytes 12-15864632/15864632"
			}
			d.LastUpload = &out
			d.LatestUploadError = &out
			d.FirstUploadError = &out
			raw, _ := json.Marshal(d)
			got := decodeDeliveryDiagnostics(string(raw), "tiktok", "")
			if got.UploadAcknowledgementResolution != nil || *got.AcknowledgedBytes != 0 || got.LatestUploadError.Result != out.Result {
				t.Fatal("genuine error or unknown progress overwritten", got)
			}
		})
	}
}

func TestTikTokWorkerReconcilesSavedAcknowledgementAgainstOriginalOperation(t *testing.T) {
	a, ctx, pf, post, target, sibling := recoveryFixture(t, "tiktok")
	d := savedExclusiveTikTokACK()
	if err := d.save(ctx, target); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().Exec(`UPDATE post_targets SET status='publishing',identity_resolve_after=NULL,last_error='TikTok chunk upload returned HTTP 201 (invalid_acknowledgement)' WHERE id=?`, target)
	for _, status := range []string{"PROCESSING_UPLOAD", "PUBLISH_COMPLETE"} {
		data := map[string]any{"status": status}
		if status == "PUBLISH_COMPLETE" {
			data["publicaly_available_post_id"] = []string{"native-result"}
		}
		pf.executeResponses["get_publish_status"] = historyFixtureResult(map[string]any{"data": data, "error": map[string]any{"code": "ok"}})
		ctx.AppDB().Exec(`UPDATE post_targets SET identity_resolve_after=NULL WHERE id=?`, target)
		a.recoverTikTokDeliveries(ctx)
	}
	var local, publicID, siblingStatus string
	ctx.AppDB().QueryRow(`SELECT status,COALESCE(platform_post_id,'') FROM post_targets WHERE id=?`, target).Scan(&local, &publicID)
	ctx.AppDB().QueryRow(`SELECT status FROM post_targets WHERE id=?`, sibling).Scan(&siblingStatus)
	if local != "published" || publicID != "native-result" || siblingStatus != "published" {
		t.Fatal("existing operation was not reconciled", post, local, publicID, siblingStatus)
	}
	for _, call := range pf.executeCalls {
		if call.Tool != "get_publish_status" || call.Input["publish_id"] != "existing-operation" {
			t.Fatal("worker resent or switched operation", call)
		}
	}
	evidence := loadDeliveryDiagnostics(ctx, target)
	if evidence.UploadAcknowledgementResolution == nil || evidence.LatestUploadError.HTTPStatus != 201 {
		t.Fatal("worker lost response evidence", evidence)
	}
}
