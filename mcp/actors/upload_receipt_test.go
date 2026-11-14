package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestUploadReceiptStopsPublication(t *testing.T) {
	hash := strings.Repeat("a", 64)
	tests := []struct {
		name     string
		change   map[string]any
		remove   string
		callErr  error
		valid    bool
		notReady bool
	}{
		{name: "valid", valid: true},
		{name: "uppercase checksum", change: map[string]any{"sha256": strings.ToUpper(hash)}, valid: true},
		{name: "missing receipt", remove: "all"},
		{name: "missing uploaded", remove: "uploaded"},
		{name: "not uploaded", change: map[string]any{"uploaded": false}},
		{name: "string uploaded", change: map[string]any{"uploaded": "true"}},
		{name: "rejected success", change: map[string]any{"success": false}},
		{name: "rejected dispatch", change: map[string]any{"action_dispatched": false}},
		{name: "failed", change: map[string]any{"failed": true}},
		{name: "missing filename", remove: "filename"},
		{name: "blank filename", change: map[string]any{"filename": "  "}},
		{name: "wrong filename", change: map[string]any{"filename": "wrong.png"}},
		{name: "missing size", remove: "size_bytes"},
		{name: "zero size", change: map[string]any{"size_bytes": 0}},
		{name: "negative size", change: map[string]any{"size_bytes": -5}},
		{name: "fractional size", change: map[string]any{"size_bytes": 0.5}},
		{name: "string size", change: map[string]any{"size_bytes": "5"}},
		{name: "overflow size", change: map[string]any{"size_bytes": 1e30}},
		{name: "missing checksum", remove: "sha256"},
		{name: "wrong checksum", change: map[string]any{"sha256": strings.Repeat("b", 64)}},
		{name: "staging is not delivery", change: map[string]any{"prepared": true, "uploaded": false}},
		{name: "transport error", callErr: errors.New("upload failed https://private.test/file?token=secret")},
		{name: "receipt does not prove attachment ready", valid: true, notReady: true},
	}
	for _, semantic := range []bool{false, true} {
		path := "selector"
		if semantic {
			path = "semantic"
		}
		for _, tc := range tests {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				p := newFakePlatform()
				p.mediaSOM = true
				p.waitOutput = map[string]any{"matched": !tc.notReady, "timed_out": tc.notReady}
				if tc.callErr != nil {
					p.actionErrors = map[string][]error{"upload_file": {tc.callErr}}
				}
				p.readViewsResponse = func(tool string, args map[string]any) map[string]any {
					if tool != "computer.computer_use" || args["action"] != "upload_file" {
						return nil
					}
					out := map[string]any{"uploaded": true, "success": true, "action_dispatched": true, "filename": "image.png", "size_bytes": 5, "sha256": hash, "mime_type": "image/png", "file_source": "base64"}
					if tc.remove == "all" {
						out = map[string]any{}
					} else {
						delete(out, tc.remove)
					}
					for key, value := range tc.change {
						out[key] = value
					}
					return out
				}
				ctx, app := newTestCtx(t, p)
				locator := map[string]any{"selector": "input[type=file]"}
				if semantic {
					locator = map[string]any{"text": "Add image", "role": "button", "som_only": true}
				}
				rec := saveFixtureActor(t, ctx, app, map[string]any{
					"schema_version": 1, "allowed_hosts": []string{"example.com"},
					"limits": map[string]any{"step_retries": 3},
					"steps": []any{
						map[string]any{"action": "goto", "url": "https://example.com/new"},
						// Even an optional upload must not skip an unconfirmed receipt.
						map[string]any{"action": "upload_file", "optional": true, "locator": locator, "base64": "aGVsbG8=", "filename": "image.png", "expected_sha256": hash, "mime_type": "image/png"},
						map[string]any{"action": "wait_for", "conditions": []any{map[string]any{"type": "selector_present", "selector": ".attachment-ready"}}},
						map[string]any{"action": "click", "locator": map[string]any{"selector": "button#publish"}},
					},
				})
				if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
					t.Fatal(err)
				}
				queued, err := claimActorRun(ctx)
				if err != nil {
					t.Fatal(err)
				}
				_ = app.executeActorRun(context.Background(), ctx, queued)
				run, err := getActorRun(ctx, queued.ID)
				if err != nil {
					t.Fatal(err)
				}
				completed := tc.valid && !tc.notReady
				if (run["status"] == "completed") != completed {
					t.Fatalf("unexpected run: %v", run)
				}
				output := mapFromAny(run["output"])
				if !tc.valid {
					if !strings.Contains(stringFromAny(run["error"]), "upload_not_confirmed") || intFromAny(mapFromAny(output["execution_progress"])["completed_steps"]) != 1 {
						t.Fatalf("upload failure not preserved: %v", run)
					}
					if output["media"] != nil {
						t.Fatalf("failed receipt became successful media: %v", output["media"])
					}
				} else {
					want := []any{map[string]any{"uploaded": true, "filename": "image.png", "size_bytes": float64(5), "mime_type": "image/png", "file_source": "base64"}}
					if !reflect.DeepEqual(output["media"], want) {
						t.Fatalf("media format changed: %v", output["media"])
					}
				}
				var trace []map[string]any
				for _, call := range p.callsSnapshot() {
					if call.app == "storage" && call.tool == "files_upload" && call.args["source"] == "actors:trace" {
						b, err := base64.StdEncoding.DecodeString(stringFromAny(call.args["content_base64"]))
						if err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(b, &trace); err != nil {
							t.Fatal(err)
						}
					}
				}
				foundReceipt := false
				for _, event := range trace {
					if event["action"] == "upload_file.receipt" {
						foundReceipt = true
						if (event["status"] == "confirmed") != tc.valid || mapFromAny(event["expected"])["filename"] != "image.png" || event["computer_response"] == nil {
							t.Fatalf("missing receipt evidence: %v", event)
						}
					}
					if !tc.valid && event["action"] == "upload_file" && (event["status"] == "completed" || event["status"] == "skipped" || event["status"] == "retry") {
						t.Fatalf("unconfirmed upload continued: %v", event)
					}
				}
				if tc.callErr != nil {
					b, _ := json.Marshal([]any{run, trace})
					if strings.Contains(string(b), "token=secret") {
						t.Fatal("raw Computer error leaked into persisted output")
					}
				}
				if !foundReceipt || output["trace_artifact_id"] == nil {
					t.Fatalf("receipt trace was not retained: %v", output)
				}
				var uploads, waits, publications int
				for _, call := range p.callsSnapshot() {
					if call.tool != "computer_use" {
						continue
					}
					switch call.args["action"] {
					case "upload_file":
						uploads++
					case "wait_for":
						waits++
					case "click":
						publications++
					}
				}
				if uploads != 1 || (!tc.valid && waits != 0) || (publications > 0) != completed {
					t.Fatalf("uploads=%d waits=%d publications=%d", uploads, waits, publications)
				}
			})
		}
	}
}

func TestUploadReceiptWithoutOptionalExpectations(t *testing.T) {
	if err := validateUploadReceipt(actorStep{}, map[string]any{"uploaded": true, "filename": "inferred.png", "size_bytes": float64(5)}); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareUploadRemainsStaging(t *testing.T) {
	p := newFakePlatform()
	p.readViewsResponse = func(tool string, args map[string]any) map[string]any {
		if tool == "computer.computer_use" && args["action"] == "prepare_upload" {
			return map[string]any{"prepared": true, "uploaded": false, "backend": "local", "filename": "image.png", "size_bytes": 5, "sha256": strings.Repeat("a", 64)}
		}
		return nil
	}
	ctx, app := newTestCtx(t, p)
	e := &actorExecution{app: app, ctx: ctx, session: &browserSession{SessionID: "fixture", Backend: "local"}, workerCtx: context.Background(), startedAt: time.Now()}
	if err := e.prepareUpload(actorStep{Base64: "aGVsbG8=", Filename: "image.png", ExpectedSHA256: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	if len(e.media) != 0 || len(e.trace) != 1 || e.trace[0]["status"] != "prepared" || e.trace[0]["site_upload_dispatched"] != false {
		t.Fatalf("staging was treated as an upload: media=%v trace=%v", e.media, e.trace)
	}
}

func TestUploadReceiptTraceIsBoundedAndSanitized(t *testing.T) {
	step := actorStep{Filename: "image.png", SourceURL: "https://files.test/private?token=source-secret", Base64: "cHJpdmF0ZS1ieXRlcw==", FilePath: "/private/user/source.png", ExpectedSHA256: strings.Repeat("a", 64)}
	e := &actorExecution{startedAt: time.Now(), trace: make([]map[string]any, maxActorTraceEvents)}
	out := map[string]any{
		"uploaded": false, "filename": "wrong.png", "size_bytes": 123,
		"error":   "request " + step.SourceURL + " payload " + step.Base64 + " at " + step.FilePath + " Authorization: Bearer auth-secret cookie=session-secret " + strings.Repeat("x", 10000),
		"message": "https://other.test/secret?token=other-secret",
		"headers": map[string]any{"Authorization": "header-secret"},
		"base64":  step.Base64, "screenshot": strings.Repeat("y", 10000),
		"status": map[string]any{"secret": "nested-secret"},
	}
	e.traceUploadReceipt(step, out, errActorUploadNotConfirmed)
	if len(e.trace) != maxActorTraceEvents {
		t.Fatal("trace bound exceeded")
	}
	event := e.trace[len(e.trace)-1]
	if mapFromAny(event["observed"])["filename"] != "wrong.png" || mapFromAny(event["expected"])["sha256"] != step.ExpectedSHA256 {
		t.Fatalf("lost expected/observed evidence: %v", event)
	}
	b, _ := json.Marshal(event)
	if len(b) > 8192 {
		t.Fatalf("unbounded diagnostic: %d", len(b))
	}
	for _, secret := range []string{"source-secret", "auth-secret", "session-secret", "other-secret", "header-secret", "nested-secret", step.Base64, step.FilePath} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("secret leaked in receipt trace: %s", secret)
		}
	}
}

func TestUploadErrorPreservesCancellationAndTimeout(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errActorCancelled} {
		err := &actorUploadError{cause: cause}
		if !errors.Is(err, cause) || !errors.Is(err, errActorUploadNotConfirmed) || retryableActorError(err) {
			t.Fatalf("lost upload error identity: %v", err)
		}
	}
	if !actorOperationTimedOut(&actorUploadError{cause: errors.New("action_timeout: upload_file")}) {
		t.Fatal("structured Computer timeout lost its reconciliation handling")
	}
}
