package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestAskFailureClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		err    error
		retry  bool
		code   string
	}{
		{"reported", 200, `{"error":"Codex stream ended with error"}`, nil, true, "stream_failure_unknown"},
		{"truncated", 200, `{"error":"Codex stream ended without response.completed"}`, nil, true, "stream_failure_unknown"},
		{"incomplete", 200, `{"error":"Codex stream ended with response.incomplete"}`, nil, false, "upstream_error"},
		{"auth", 401, `{"error":"invalid_api_key"}`, nil, false, "authentication"},
		{"wrappedAuth", 500, `{"error":"authentication_error"}`, nil, false, "authentication"},
		{"httpAuth", 200, `{"error":"HTTP 401: Codex stream ended with error"}`, nil, false, "permanent_http_error"},
		{"quota", 429, `{"error":{"type":"insufficient_quota","message":"Codex stream ended with error"}}`, nil, false, "quota_or_billing"},
		{"usageLimit", 429, `{"error":{"type":"usage_limit_reached"}}`, nil, false, "quota_or_billing"},
		{"payment", 402, `{"error":"payment required"}`, nil, false, "permanent_http_error"},
		{"input", 400, `{"error":"invalid_request_error"}`, nil, false, "invalid_request_or_unavailable"},
		{"image", 500, `{"error":"invalid_image"}`, nil, false, "invalid_request_or_unavailable"},
		{"rate", 429, `{"error":{"type":"rate_limit_exceeded"}}`, nil, true, "rate_limited"},
		{"server", 503, `{"error":"service unavailable"}`, nil, true, "transient_http_error"},
		{"network", 0, "", errors.New("connection reset by peer"), true, "transport_error"},
		{"cancel", 0, "", context.Canceled, false, "cancelled_or_timed_out"},
		{"unknown", 200, `{"error":"unexpected provider failure"}`, nil, false, "upstream_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var res *sdk.ExecuteResult
			if tc.body != "" {
				res = &sdk.ExecuteResult{Status: tc.status, Data: json.RawMessage(tc.body)}
			}
			got, _ := classifyAskFailure(res, tc.err)
			if got.Retryable != tc.retry || got.Code != tc.code {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestAskRetrySuccessExhaustionAndPermanentStop(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		for _, succeed := range []bool{false, true} {
			t.Run(fmt.Sprintf("permanent%v-success%v", permanent, succeed), func(t *testing.T) {
				calls := 0
				var delays []time.Duration
				result, d, err := runAskIntegrationAttempts(context.Background(), func(context.Context) (*sdk.ExecuteResult, error) {
					calls++
					if succeed && calls == 2 {
						return &sdk.ExecuteResult{Success: true, Status: 200, Data: canonOK("Completed review.")}, nil
					}
					body := `{"error":"Codex stream ended with error"}`
					status := 200
					if permanent {
						body = `{"error":{"type":"insufficient_quota"}}`
						status = 429
					}
					return &sdk.ExecuteResult{Status: status, Data: json.RawMessage(body)}, nil
				}, func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil })
				if permanent {
					if err == nil || calls != 1 || len(delays) != 0 || d.StopReason != "quota_or_billing" {
						t.Fatalf("%v %+v calls=%d delays=%v", err, d, calls, delays)
					}
				} else if succeed {
					if err != nil || result == nil || calls != 2 || len(delays) != 1 || delays[0] != time.Second {
						t.Fatalf("%v %+v %v", err, d, delays)
					}
				} else if err == nil || calls != 3 || len(delays) != 2 || delays[1] != 2*time.Second || d.StopReason != "attempts_exhausted" || !strings.Contains(err.Error(), "Codex stream ended with error") {
					t.Fatalf("%v %+v %v", err, d, delays)
				}
			})
		}
	}
}

func TestAskRetryHonorsResetWithoutExceedingBudget(t *testing.T) {
	for _, seconds := range []string{"3", "120"} {
		t.Run(seconds, func(t *testing.T) {
			calls := 0
			var delays []time.Duration
			_, d, err := runAskIntegrationAttempts(context.Background(), func(context.Context) (*sdk.ExecuteResult, error) {
				calls++
				return &sdk.ExecuteResult{Status: 429, Data: json.RawMessage(`{"error":"Too Many Requests"}`), Headers: map[string]string{"Retry-After": seconds, "Set-Cookie": "secret"}}, nil
			}, func(_ context.Context, delay time.Duration) error { delays = append(delays, delay); return nil })
			if err == nil || strings.Contains(err.Error(), "secret") || d.History[0].RetryInfo.RetryAfter != seconds {
				t.Fatalf("%v %+v", err, d)
			}
			if seconds == "120" {
				if calls != 1 || len(delays) != 0 || d.StopReason != "upstream_retry_deferred" {
					t.Fatal(d)
				}
			} else if len(delays) != 2 || delays[0] < 2*time.Second || delays[0] > 3*time.Second {
				t.Fatalf("%+v %v", d, delays)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	calls := 0
	_, d, err := runAskIntegrationAttempts(ctx, func(context.Context) (*sdk.ExecuteResult, error) {
		calls++
		return nil, errors.New("connection reset by peer")
	}, func(context.Context, time.Duration) error { t.Fatal("must not sleep beyond deadline"); return nil })
	if err == nil || calls != 1 || d.StopReason != "insufficient_retry_budget" {
		t.Fatalf("%v %+v", err, d)
	}
}

type askSequencePlatform struct {
	*stubPlatform
	count atomic.Int32
	block <-chan struct{}
}

func (s *askSequencePlatform) ExecuteIntegrationTool(conn int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	call := s.count.Add(1)
	s.mu.Lock()
	s.ExecuteCalls = append(s.ExecuteCalls, executeCall{ConnID: conn, Tool: tool, Input: input})
	s.mu.Unlock()
	if s.block != nil {
		<-s.block
	}
	if call == 1 {
		return &sdk.ExecuteResult{Status: 200, Data: json.RawMessage(`{"error":"Codex stream ended with error"}`)}, nil
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: codexOK("Face and hands are visible.")}, nil
}

func TestMediaAskRetriesSameEvidenceWithoutWrites(t *testing.T) {
	stub := &askSequencePlatform{stubPlatform: boundOpenAICodex()}
	app := newTestCtxWithPlatform(t, stub)
	upsertMedia(app.AppDB(), testProj, "1", sampleVideoProbe(), "sha", "/clips/", "clip.mp4")
	upsertDerivation(app.AppDB(), testProj, "1", "thumbnail", 99, 320, 180, 0)
	out, err := (&App{}).toolAsk(app, map[string]any{"file_id": "1", "question": "Are face and hands in the crop?", "include_transcript": false})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	d := result["request_diagnostics"].(askRetryDiagnostics)
	if d.Attempts != 2 || result["answer"] != "Face and hands are visible." || result["coverage"].(askCoverage).ArtifactsCreated {
		t.Fatal(result)
	}
	if result["reasoning_effort"] != "low" {
		t.Fatal(result)
	}
	for _, call := range stub.ExecuteCalls {
		if call.Tool != "responses_create" || call.Input["reasoning"].(map[string]any)["effort"] != "low" {
			t.Fatalf("wrong Codex request: %s", call.Tool)
		}
	}
	a, _ := json.Marshal(stub.ExecuteCalls[0].Input)
	b, _ := json.Marshal(stub.ExecuteCalls[1].Input)
	if string(a) != string(b) {
		t.Fatal("retry changed evidence/model/question")
	}
	row, _ := getMedia(app.AppDB(), testProj, "1")
	if row.Description != "" || len(row.Derivations) != 1 {
		t.Fatalf("ask mutated catalog: %+v", row)
	}
}

func TestAskTimeoutKeepsLegacyRequestSlotUntilActualExit(t *testing.T) {
	unblock := make(chan struct{})
	t.Cleanup(func() {
		close(unblock)
		end := time.Now().Add(time.Second)
		for time.Now().Before(end) {
			if _, busy := integrationCallsInFlight.Load("media_ask:13:chat_completion"); !busy {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("legacy call did not release its slot after actual exit")
	})
	stub := &askSequencePlatform{stubPlatform: boundOpenAICodex(), block: unblock}
	app := newTestCtxWithPlatform(t, stub)
	_, d, err := executeAskIntegrationWithRetry(app, 13, "chat_completion", map[string]any{}, 20*time.Millisecond)
	if err == nil || d.Attempts != 1 {
		t.Fatalf("%v %+v", err, d)
	}
	_, _, err = executeAskIntegrationWithRetry(app, 13, "chat_completion", map[string]any{}, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "still running") || stub.count.Load() != 1 {
		t.Fatalf("%v calls=%d", err, stub.count.Load())
	}
}

type deadlineAskPlatform struct {
	*stubPlatform
	calls     atomic.Int32
	deadlines chan time.Time
}

func (s *deadlineAskPlatform) ExecuteIntegrationToolContext(ctx context.Context, _ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
	s.calls.Add(1)
	deadline, _ := ctx.Deadline()
	s.deadlines <- deadline
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestAskForwardsOneOverallDeadline(t *testing.T) {
	stub := &deadlineAskPlatform{stubPlatform: boundOpenAICodex(), deadlines: make(chan time.Time, 1)}
	app := newTestCtxWithPlatform(t, stub)
	start := time.Now()
	_, d, err := executeAskIntegrationWithRetry(app, 13, "chat_completion", map[string]any{}, 25*time.Millisecond)
	if err == nil || stub.calls.Load() != 1 || d.Attempts != 1 {
		t.Fatalf("%v %+v calls=%d", err, d, stub.calls.Load())
	}
	// The call's deadline is no later than the original overall budget.
	deadline := <-stub.deadlines
	if deadline.IsZero() || deadline.After(start.Add(30*time.Millisecond)) {
		t.Fatal(deadline)
	}
}

func TestAskCancellationDuringBackoffStopsAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	_, d, err := runAskIntegrationAttempts(ctx, func(context.Context) (*sdk.ExecuteResult, error) {
		calls++
		return nil, errors.New("connection reset by peer")
	}, func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() })
	if !errors.Is(err, context.Canceled) || calls != 1 || d.StopReason != "retry_interrupted" || !strings.Contains(err.Error(), "connection reset by peer") {
		t.Fatalf("%v %+v calls=%d", err, d, calls)
	}
}
func TestMediaAskEmptySuccessRemainsFailure(t *testing.T) {
	stub := boundOpenAI()
	stub.executeResp = &sdk.ExecuteResult{Success: true, Status: 200, Data: canonOK("")}
	app := newTestCtxWithPlatform(t, stub)
	upsertMedia(app.AppDB(), testProj, "1", sampleVideoProbe(), "sha", "/clips/", "clip.mp4")
	upsertDerivation(app.AppDB(), testProj, "1", "thumbnail", 99, 320, 180, 0)
	out, err := (&App{}).toolAsk(app, map[string]any{"file_id": "1", "question": "Is the crop usable?"})
	var failure *askIntegrationError
	if out != nil || !errors.As(err, &failure) || failure.Diagnostics.StopReason != "invalid_provider_response" || len(stub.ExecuteCalls) != 1 {
		t.Fatalf("out=%v err=%v calls=%d", out, err, len(stub.ExecuteCalls))
	}
}

func TestMediaAskLegacyProjectScopeCompatibility(t *testing.T) {
	stub := boundOpenAI()
	stub.executeResp = &sdk.ExecuteResult{Success: true, Status: 200, Data: canonOK("Visible face.")}
	app := newTestCtxWithPlatform(t, stub).WithProject(testProj)
	upsertMedia(app.AppDB(), testProj, "1", sampleVideoProbe(), "sha", "/clips/", "clip.mp4")
	upsertDerivation(app.AppDB(), testProj, "1", "thumbnail", 99, 320, 180, 0)
	_, err := (&App{}).toolAsk(app, map[string]any{"file_id": "1", "question": "Is the face visible?"})
	if err != nil || len(stub.ExecuteCalls) != 1 {
		t.Fatalf("%v calls=%d", err, len(stub.ExecuteCalls))
	}
}
