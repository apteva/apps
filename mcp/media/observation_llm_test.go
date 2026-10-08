package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func codexOK(content string) json.RawMessage {
	return json.RawMessage(`{"status":"completed","reasoning":{"effort":"low"},"output":[{"type":"reasoning","summary":[]},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":` + jsonStr(content) + `}]}]}`)
}

func TestObservationCodexRequestPreservesGroundingWithLowEffort(t *testing.T) {
	messages := []map[string]any{
		{"role": "system", "content": "Only describe supported evidence."},
		{"role": "system", "content": "Project context."},
		{"role": "user", "content": []map[string]any{
			{"type": "text", "text": "Question and transcript at 5000 ms."},
			{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/frame.png", "detail": "high"}},
		}},
	}
	bound := &sdk.BoundIntegration{AppSlug: "openai-codex"}
	tool, request := observationRequest(bound, "gpt-6.1-sol", messages, 8000, 0.3)
	if tool != "responses_create" || request["model"] != "gpt-6.1-sol" || request["reasoning"].(map[string]any)["effort"] != "low" {
		t.Fatal("missing explicit Codex model/low effort")
	}
	if request["instructions"] != "Only describe supported evidence.\n\nProject context." {
		t.Fatal("system/project grounding lost")
	}
	input := request["input"].([]map[string]any)
	parts := input[0]["content"].([]map[string]any)
	if len(input) != 1 || input[0]["role"] != "user" || len(parts) != 2 || parts[0]["type"] != "input_text" || parts[0]["text"] != "Question and transcript at 5000 ms." || parts[1]["type"] != "input_image" || parts[1]["image_url"] != "https://example.test/frame.png" || parts[1]["detail"] != "high" {
		t.Fatal("text/image grounding changed")
	}
	for _, field := range []string{"messages", "temperature", "max_tokens", "max_output_tokens"} {
		if _, exists := request[field]; exists {
			t.Fatalf("unexpected compatibility parameter %s", field)
		}
	}
	if request["store"] != false || request["stream"] != true {
		t.Fatal("Codex streaming/privacy controls changed")
	}
}

func TestExtractObservationCodexAcceptsOnlyCompletedAssistantText(t *testing.T) {
	if got, err := extractObservationContent("openai-codex", codexOK("Supported observation.")); err != nil || got != "Supported observation." {
		t.Fatalf("%q %v", got, err)
	}
	for _, data := range []json.RawMessage{
		codexOK(""),
		json.RawMessage(strings.Replace(string(codexOK("Answer at wrong effort.")), `"effort":"low"`, `"effort":"medium"`, 1)),
		json.RawMessage(strings.Replace(string(codexOK("Answer without effort.")), `"reasoning":{"effort":"low"},`, ``, 1)),
		json.RawMessage(`{"status":"incomplete","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"partial answer"}]}]}`),
		json.RawMessage(`{"status":"completed","error":{"message":"failure"},"output":[]}`),
		json.RawMessage(`{"status":"completed","reasoning":{"effort":"low"},"output":[{"type":"reasoning","content":[{"type":"output_text","text":"private thought"}]}]}`),
		json.RawMessage(`{"status":"completed","reasoning":{"effort":"low"},"output":[{"type":"message","role":"assistant","status":"in_progress","content":[{"type":"output_text","text":"partial answer"}]}]}`),
		json.RawMessage(`{"status":"completed","reasoning":{"effort":"low"},"output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"Cannot answer"}]}]}`),
		json.RawMessage(`{"status":"completed","reasoning":{"effort":"low"},"output":[{"type":"message","role":"user","status":"completed","content":[{"type":"output_text","text":"echoed prompt"}]}]}`),
		json.RawMessage(`{`),
	} {
		if got, err := extractObservationContent("openai-codex", data); err == nil || got != "" {
			t.Fatalf("accepted invalid/partial/private output: %q %v", got, err)
		}
	}
	if got, err := extractObservationContent("openai-api", canonOK("Existing provider answer.")); err != nil || got != "Existing provider answer." {
		t.Fatalf("chat compatibility changed: %q %v", got, err)
	}
}

func TestMediaAskCodexUsesLowEffortForExistingVisualEvidence(t *testing.T) {
	stub := boundOpenAICodex()
	stub.executeResp = &sdk.ExecuteResult{Success: true, Status: 200, Data: codexOK("The face is visible.")}
	ctx := newTestCtxWithPlatform(t, stub)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "sha", "", "clip.mp4")
	upsertDerivation(ctx.AppDB(), testProj, "1", "keyframe", 99, 320, 180, 5000)
	out, err := (&App{}).toolAsk(ctx, map[string]any{"file_id": "1", "question": "Is the face visible?", "at_ms": 5000})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["answer"] != "The face is visible." || result["reasoning_effort"] != "low" || len(stub.ExecuteCalls) != 1 || stub.ExecuteCalls[0].Tool != "responses_create" {
		t.Fatal("Codex visual request did not use low effort")
	}
	input := stub.ExecuteCalls[0].Input["input"].([]map[string]any)
	parts := input[0]["content"].([]map[string]any)
	if len(parts) < 3 || !strings.Contains(parts[1]["text"].(string), "5000 ms") || parts[len(parts)-1]["type"] != "input_image" {
		t.Fatal("cached visual evidence missing")
	}
}

func TestCodexObservationRetainsExistingDescriptionCooldown(t *testing.T) {
	stub := boundOpenAICodex()
	ctx := newTestCtxWithPlatform(t, stub)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "", "audio.wav")
	upsertTranscript(ctx.AppDB(), &TranscriptRow{FileID: "1", ProjectID: testProj, Status: "ok", Text: "Spoken words."})
	now := time.Now()
	_, err := recordDescriptionBackoff(ctx.AppDB(), 13, "chat_completion", "gpt-5.5", descriptionRetryInfo{}, now.Add(10*time.Minute), now, 600)
	if err != nil {
		t.Fatal(err)
	}
	runOneDescription(ctx, ctx.IntegrationFor("descriptions"), testProj, "1")
	if len(stub.ExecuteCalls) != 0 {
		t.Fatal("switching request tool bypassed persisted cooldown")
	}
}
