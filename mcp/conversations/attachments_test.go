package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func attachmentRequest(a *App, method, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	authorizeTestRequest(r)
	w := httptest.NewRecorder()
	for _, route := range a.HTTPRoutes() {
		if route.Pattern == r.URL.Path && (route.Method == "" || route.Method == method) {
			route.Handler(w, r)
			return w
		}
	}
	w.WriteHeader(404)
	return w
}
func TestAttachmentRoundTripAndCoreVision(t *testing.T) {
	a, ctx, p := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	caller := boundConversationCaller(t, a, conv, 41)
	img := image.NewRGBA(image.Rect(0, 0, 2400, 1200))
	for y := 0; y < 1200; y++ {
		for x := 0; x < 2400; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var raw bytes.Buffer
	png.Encode(&raw, img)
	input := map[string]any{"id": "upload-stable-123456", "name": "red.png", "content_base64": base64.StdEncoding.EncodeToString(raw.Bytes())}
	upload := attachmentRequest(a, "POST", "/attachments?chat_id="+conv.ID, input)
	if upload.Code != 200 {
		t.Fatal(upload.Code, upload.Body.String())
	}
	var item Attachment
	json.Unmarshal(upload.Body.Bytes(), &item)
	if item.Type != "image" || !strings.HasPrefix(item.DataURL, "data:image/jpeg;base64,") {
		t.Fatal(item)
	}
	duplicate := attachmentRequest(a, "POST", "/attachments?chat_id="+conv.ID, input)
	if duplicate.Body.String() != upload.Body.String() {
		t.Fatal("upload retry changed identity")
	}
	if _, err := a.toolReadAttachment(caller, ctx, map[string]any{"conversation_id": conv.ID, "attachment_id": item.ID}); err == nil {
		t.Fatal("agent read unsent draft")
	}
	send := map[string]any{"content": "What color is this?", "client_message_id": "image-message", "attachments": []map[string]any{{"id": item.ID, "type": "image", "data_url": "forged"}}}
	result := attachmentRequest(a, "POST", "/messages?chat_id="+conv.ID, send)
	if result.Code != 200 {
		t.Fatal(result.Code, result.Body.String())
	}
	var msg Message
	json.Unmarshal(result.Body.Bytes(), &msg)
	if len(msg.Attachments) != 1 || msg.Attachments[0].DataURL != item.DataURL {
		t.Fatal("did not resolve authoritative image")
	}
	if len(p.ensures) != 1 || len(p.ensures[0].Events) != 1 {
		t.Fatal("expected one thread event", p.ensures)
	}
	payload, _ := json.Marshal(p.ensures[0].Events[0].Message)
	if !bytes.Contains(payload, []byte(`"type":"image_url"`)) || !bytes.Contains(payload, []byte(item.DataURL)) {
		t.Fatal("actual Core event lacks image")
	}
	if bytes.Contains(payload, []byte("Use conversations_read_attachment")) || bytes.Contains(payload, []byte("red.png")) || bytes.Contains(payload, []byte("file_id=")) {
		t.Fatal("current image was described as a file requiring retrieval")
	}
	if !bytes.Contains(payload, []byte("reply directly with conversations_send phase=final")) {
		t.Fatal("missing direct image-answer guidance")
	}
	result = attachmentRequest(a, "POST", "/messages?chat_id="+conv.ID, send)
	if result.Code != 200 || len(p.ensures) != 1 {
		t.Fatal("message retry duplicated delivery")
	}
	download := attachmentRequest(a, "GET", "/attachments?chat_id="+conv.ID+"&id="+item.ID, nil)
	if download.Code != 200 || !bytes.Contains(download.Body.Bytes(), []byte(input["content_base64"].(string))) {
		t.Fatal("original download lost")
	}
	if _, err := a.toolReadAttachment(caller, ctx, map[string]any{"conversation_id": conv.ID, "attachment_id": item.ID}); err != nil {
		t.Fatal(err)
	}
	other := mkConversation(t, a, 41)
	if attachmentRequest(a, "GET", "/attachments?chat_id="+other.ID+"&id="+item.ID, nil).Code != 404 {
		t.Fatal("cross conversation download")
	}
	if attachmentRequest(a, "POST", "/messages?chat_id="+other.ID, send).Code != 400 {
		t.Fatal("cross conversation send")
	}
	input["content_base64"] = base64.StdEncoding.EncodeToString([]byte("changed"))
	if attachmentRequest(a, "POST", "/attachments?chat_id="+conv.ID, input).Code != 409 {
		t.Fatal("upload conflict ignored")
	}
}
func TestAttachmentDelegatedAccessAndFileRead(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	makeChat := func(subject string) Conversation {
		w := visitorRequest(a, subject, "POST", "/chats", map[string]any{"title": "Files", "agent_ids": []int{41}, "lead_agent_id": 41}, visitorActions)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var c Conversation
		json.Unmarshal(w.Body.Bytes(), &c)
		return c
	}
	one, two := makeChat("one"), makeChat("two")
	input := map[string]any{"id": "upload-text-12345678", "name": "notes.txt", "content_base64": base64.StdEncoding.EncodeToString([]byte("Meeting at noon"))}
	w := visitorRequest(a, "one", "POST", "/attachments?chat_id="+one.ID, input, visitorActions)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var item Attachment
	json.Unmarshal(w.Body.Bytes(), &item)
	if visitorRequest(a, "two", "GET", "/attachments?chat_id="+one.ID+"&id="+item.ID, nil, visitorActions).Code != 404 {
		t.Fatal("subject leak")
	}
	if visitorRequest(a, "one", "GET", "/attachments?chat_id="+one.ID+"&id="+item.ID, nil, []string{"chat.read"}).Code != 403 {
		t.Fatal("missing read permission")
	}
	send := map[string]any{"content": "", "client_message_id": "file-only", "attachments": []Attachment{{ID: item.ID, Type: "file"}}}
	if visitorRequest(a, "two", "POST", "/messages?chat_id="+two.ID, send, visitorActions).Code != 400 {
		t.Fatal("cross subject reference")
	}
	w = visitorRequest(a, "one", "POST", "/messages?chat_id="+one.ID, send, visitorActions)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	caller := boundConversationCaller(t, a, &one, 41)
	result, err := a.toolReadAttachment(caller, ctx, map[string]any{"conversation_id": one.ID, "attachment_id": item.ID})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(result)
	if !bytes.Contains(b, []byte("Meeting at noon")) {
		t.Fatal(string(b))
	}
	a.store.db.Exec(`UPDATE conversation_attachments SET created_at=datetime('now','-2 days')`)
	input["id"] = "another-upload-1234567"
	visitorRequest(a, "one", "POST", "/attachments?chat_id="+one.ID, input, visitorActions)
	if _, _, err := a.loadAttachment(one.ID, item.ID); err != nil {
		t.Fatal("pruned sent attachment")
	}
}

type attachmentStoragePlatform struct {
	*recordingPlatform
	uploads int
}

func (p *attachmentStoragePlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	if app != "storage" || tool != "files_upload" || in["visibility"] != "private" {
		panic("invalid storage call")
	}
	p.uploads++
	raw, _ := json.Marshal(map[string]any{"id": 123})
	return json.Unmarshal(raw, out)
}
func TestAttachmentOptionalStorageReturnsStableFileID(t *testing.T) {
	p := &attachmentStoragePlatform{recordingPlatform: &recordingPlatform{}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject), tk.WithPlatform(p), tk.WithConfig(map[string]string{"attachment_storage": "true"}))
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.OnUnmount(ctx)
	conv := mkConversation(t, a, 41)
	input := map[string]any{"id": "storage-file-1234567", "name": "notes.txt", "content_base64": "SGVsbG8="}
	w := attachmentRequest(a, "POST", "/attachments?chat_id="+conv.ID, input)
	var item Attachment
	json.Unmarshal(w.Body.Bytes(), &item)
	msg := &Message{Attachments: []Attachment{item}}
	for i := 0; i < 2; i++ {
		if err := a.mirrorAttachments(ctx, conv, msg); err != nil {
			t.Fatal(err)
		}
	}
	if p.uploads != 1 || msg.Attachments[0].FileID != 123 {
		t.Fatal("storage retry or file reference wrong")
	}
}

func TestMixedAttachmentEventPreservesVisionAndFileAccess(t *testing.T) {
	a, _, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	imageURL := "data:image/jpeg;base64,aW1hZ2U="
	msg := &Message{Content: "Compare the image with my notes", Attachments: []Attachment{
		{ID: "photo", Type: "image", DataURL: imageURL, Name: "photo.jpg"},
		{ID: "notes", Type: "file", Name: "notes.txt", MimeType: "text/plain", Size: 12, StorageApp: "storage", FileID: 42},
	}}
	parts := a.agentEventPayload(conv, msg, 41, []int64{41}).([]map[string]any)
	images := 0
	var text strings.Builder
	for _, part := range parts {
		if part["type"] == "image_url" {
			images++
			if part["image_url"].(map[string]any)["url"] != imageURL {
				t.Fatal("image bytes changed")
			}
		} else {
			text.WriteString(part["text"].(string))
		}
	}
	if images != 1 || !strings.Contains(text.String(), "attachment_id=notes") || !strings.Contains(text.String(), "Use conversations_read_attachment") || !strings.Contains(text.String(), "file_id=42") {
		t.Fatal("mixed attachment routing lost image or file access")
	}
	if strings.Contains(text.String(), "attachment_id=photo") {
		t.Fatal("image routed to file reader")
	}
}
