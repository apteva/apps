package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// Run chooser coverage even when no saved Patreon account is configured.
func TestLLMFileChooserImageUploadLive(t *testing.T) {
	runFileChooserImageUpload(t, false)
}

func TestLLMFileChooserImageUploadRecoveryLive(t *testing.T) {
	runFileChooserImageUpload(t, true)
}

func runFileChooserImageUpload(t *testing.T, recovery bool) {
	t.Helper()
	if os.Getenv("RUN_COMPUTER_LLM_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_LLM_TESTS=1")
	}
	backend := envDefault("COMPUTER_LLM_BROWSER_BACKEND", "local")
	sc := tk.SpawnSidecar(t, ".")
	c := &localComputerMCPClient{sidecar: sc}
	html := `<html><body><h1>Native image composer</h1><p>Choose an image for this draft.</p>
<section><button type="button" onclick="pick()">Browse</button></section><input id="decoy" type="file" hidden onchange="document.querySelector('#status').textContent='wrong input'">
<output id="status">No image uploaded</output><img id="preview"><script>
function pick(){let input=document.createElement('input');input.type='file';input.hidden=true;input.accept='image/png';document.body.append(input);
input.onchange=function(){let file=this.files[0];this.value='';let preview=document.querySelector('#preview');preview.onload=()=>{let status=document.querySelector('#status');status.textContent='Loaded '+file.name+' '+preview.naturalWidth+'x'+preview.naturalHeight;status.dataset.correct='true';};preview.src=URL.createObjectURL(file);};input.click();}
</script></body></html>`
	opened := sc.MCP("browser_session", map[string]any{"action": "open", "backend": backend, "url": "data:text/html," + url.PathEscape(html)})
	sid := stringValue(opened["session_id"])
	if sid == "" {
		t.Fatalf("open: %v", opened)
	}
	defer closePatreonTestSession(t, c, sid)
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	goal := fmt.Sprintf("Upload this PNG once into the native image composer using the visible Browse control with action=upload_file, filename=chooser-image.png, mime_type=image/png, and base64=%s. Use a fresh label or target ID. Do not open an OS dialog, navigate away, use scripting, or target hidden inputs by selector. Verify that the page reports Loaded chooser-image.png 8x8 before finishing.", base64.StdEncoding.EncodeToString(data.Bytes()))
	uploads := 0
	observe := func(args, result map[string]any) {
		if stringValue(args["action"]) == "upload_file" && boolFromAny(result["uploaded"]) {
			uploads++
		}
	}
	if recovery {
		runImageUploadRecoveryAgent(t, c, sid, goal, observe)
	} else {
		runPatreonAgent(t, c, sid, goal, 6, observe)
	}
	if uploads != 1 {
		t.Fatalf("successful uploads=%d, want one", uploads)
	}
	verified := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "match": "all", "timeout_ms": 1000, "conditions": []any{map[string]any{"type": "text_present", "value": "Loaded chooser-image.png 8x8"}, map[string]any{"type": "selector_present", "selector": "#status[data-correct=true]"}}})
	if !boolFromAny(verified["matched"]) {
		t.Fatal("image decoder did not confirm the exact uploaded file and dimensions")
	}
}

// Start with actual rejected calls and an obsolete observation. Unlike the
// ordinary workflow harness, do not refresh screenshots automatically: the
// model must request the observation needed to recover before uploading.
func runImageUploadRecoveryAgent(t *testing.T, c *localComputerMCPClient, sid, goal string, observe func(map[string]any, map[string]any)) {
	t.Helper()
	old := liveScreenshot(t, c, sid)
	current := liveScreenshot(t, c, sid)
	var label any
	for _, raw := range old["som"].([]any) {
		target := raw.(map[string]any)
		if stringValue(target["accessible_name"]) == "Browse" {
			label = target["label"]
		}
	}
	if label == nil {
		t.Fatal("fixture Browse label missing")
	}
	history := []any{}
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "upload_file", "label": label, "som_revision": old["som_revision"]}, "stale"},
		{map[string]any{"action": "upload_file", "selector": "button", "expected_text": "Browse"}, "expected_text is only valid"},
	} {
		tc.args["session_id"] = sid
		result := c.call(t, "computer_use", tc.args)
		if !strings.Contains(stringValue(result["error"]), tc.want) {
			t.Fatalf("expected %s rejection: %v", tc.want, result)
		}
		history = append(history, map[string]any{"arguments": tc.args, "result": result})
	}
	writePatreonEvidence(t, "initial-rejections.json", []byte(mustJSON(history)))
	var description string
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "computer_use" {
			description = tool.Description + "\nInput schema: " + mustJSON(tool.InputSchema)
		}
	}
	frame := decodeScreenshot(t, old)
	observation := old
	refreshed, attempted := false, false
	for step := 0; step < 7; step++ {
		prompt := "Choose ONE next computer_use action, or done when verified. Return arguments as a JSON string, omitting session_id. No scripting or external tools. Recover from the rejected upload attempts below and complete the task. The supplied image and observation precede those failures; newer screenshots are only available if you request one.\nTask: " + goal + "\nTool: " + description + "\nObservation: " + mustJSON(patreonEvidence(observation)) + "\nHistory: " + mustJSON(history)
		var d patreonAgentDecision
		callComputerLLM(t, frame, prompt, `{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["computer_use","done"]},"arguments_json":{"type":"string"},"reason":{"type":"string"}},"required":["action","arguments_json","reason"]}`, &d)
		t.Logf("RECOVERY step=%d decision=%s args=%s reason=%s", step, d.Action, d.Arguments, d.Reason)
		writePatreonEvidence(t, fmt.Sprintf("%02d-decision.json", step), []byte(mustJSON(d)))
		if d.Action == "done" {
			if !refreshed || !attempted {
				t.Fatal("finished without refreshing and uploading")
			}
			return
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(d.Arguments), &args); err != nil {
			t.Fatal(err)
		}
		args["session_id"] = sid
		action := stringValue(args["action"])
		if action == "upload_file" {
			if !refreshed || attempted || intArg(args, "som_revision") != intArg(observation, "som_revision") || (intArg(args, "label") <= 0 && stringValue(args["target_id"]) == "") || stringValue(args["expected_text"]) != "" {
				t.Fatalf("unsafe recovery: %+v", args)
			}
			attempted = true
		} else if action != "screenshot" && action != "wait_for" && action != "wait" {
			t.Fatalf("recovery changed operation instead of uploading: %v", args)
		}
		result := c.call(t, "computer_use", args)
		observe(args, result)
		if action == "screenshot" {
			if intArg(result, "som_revision") <= intArg(current, "som_revision") {
				t.Fatal("recovery did not obtain a fresh revision")
			}
			refreshed = true
			observation, frame = result, decodeScreenshot(t, result)
		}
		entry := map[string]any{"arguments": args, "result": patreonEvidence(result)}
		history = append(history, entry)
		writePatreonEvidence(t, fmt.Sprintf("%02d-action.json", step), []byte(mustJSON(entry)))
	}
	t.Fatal("agent did not complete upload recovery")
}

// Exercise native image attachment, not URL embedding. The model chooses the
// visible Browse control and uses the same source_url upload path as agents.
func TestLLMPatreonNativeImageUploadLive(t *testing.T) {
	requirePatreonTier3(t)
	c := newLocalComputerMCPClient(t)
	opened := c.call(t, "browser_session", map[string]any{"action": "open", "context_id": os.Getenv("COMPUTER_PATREON_CONTEXT_ID"), "url": requireLiveEnv(t, "COMPUTER_PATREON_CREATOR_URL")})
	sid := stringValue(opened["session_id"])
	if sid == "" {
		t.Fatalf("open: %v", opened)
	}
	defer closePatreonTestSession(t, c, sid)
	writePatreonEvidence(t, "opened.json", []byte(mustJSON(patreonEvidence(opened))))
	title := "Computer tier 3 native image " + time.Now().UTC().Format("20060102-150405")
	filename := "computer-tier3-native-image.png"
	source := "https://the-internet.herokuapp.com/img/forkme_right_green_007200.png"
	var draft map[string]any
	if !t.Run("prepare", func(t *testing.T) {
		draft = runPatreonAgent(t, c, sid, fmt.Sprintf("On this disposable Patreon test creator, create exactly ONE new native IMAGE post draft titled %q. Choose the image composer and stop with its visible Browse/upload control ready for a file. Do not use video or embed URL, upload anything yet, publish, schedule, or change other posts.", title), 15)
		assertPatreonTitle(t, draft, title)
	}) {
		return
	}
	host, postID := patreonPostIdentity(firstNonEmpty(stringValue(draft["current_url"]), stringValue(draft["url"])))
	if postID == "" {
		t.Fatal("image composer has no draft identity")
	}
	t.Run("upload_and_reload", func(t *testing.T) {
		uploads, attempts := 0, 0
		goal := fmt.Sprintf("In this existing native image draft %q, use computer_use action=upload_file against the current visible Browse/upload control, with source_url=%s and filename=%s. Use a fresh screenshot label or target ID; do not click Browse to open an OS dialog and do not add click-only arguments. Upload exactly once, then wait for the native image preview and saved draft. Do not embed the URL, publish, schedule, change other drafts, or add the file twice. If upload_file returns an error, stop and report it rather than retrying.", title, source, filename)
		shot := runPatreonAgent(t, c, sid, goal, 12, func(args, result map[string]any) {
			if stringValue(args["action"]) == "upload_file" {
				attempts++
			}
			if stringValue(args["action"]) == "upload_file" && boolFromAny(result["uploaded"]) && stringValue(result["filename"]) == filename && stringValue(result["file_source"]) == "source_url" {
				uploads++
			}
		})
		if uploads != 1 || attempts != 1 {
			t.Fatalf("successful native image uploads=%d, want exactly one; inspect action evidence", uploads)
		}
		assertPatreonTitle(t, shot, title)
		actualHost, actualID := patreonPostIdentity(firstNonEmpty(stringValue(shot["current_url"]), stringValue(shot["url"])))
		if actualHost != host || actualID != postID {
			t.Fatal("image upload changed draft identity")
		}
		if stringValue(shot["draft_save_state"]) != "saved" {
			t.Fatal("native image draft not confirmed saved")
		}
		c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "reload"})
		persisted := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "timeout_ms": 30000, "conditions": []any{map[string]any{"type": "selector_present", "selector": `#post-editor-content img[src*="/post/` + postID + `/"]`}}})
		writePatreonEvidence(t, "image-after-reload.json", []byte(mustJSON(patreonEvidence(persisted))))
		if !boolFromAny(persisted["matched"]) {
			t.Fatal("native image did not persist after reloading the saved draft")
		}
		assertPatreonTitle(t, liveScreenshot(t, c, sid), title)
	})
}
