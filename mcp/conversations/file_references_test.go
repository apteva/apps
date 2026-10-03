package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type fileReferenceTestPlatform struct {
	*recordingPlatform
	refs   map[string]sdk.FileReference
	grants []sdk.FileReferenceGrant
}

func (p *fileReferenceTestPlatform) PlatformInfo() (*sdk.PlatformInfo, error) {
	return &sdk.PlatformInfo{
		FileReferences: sdk.FileReferencesVersion,
		FileReferenceDispatch: map[string]bool{
			"app_mcp": true,
		},
	}, nil
}

func (p *fileReferenceTestPlatform) RegisterFileReference(_ context.Context, req sdk.RegisterFileReferenceRequest) (*sdk.FileReference, error) {
	if p.refs == nil {
		p.refs = map[string]sdk.FileReference{}
	}
	key := req.ProjectID + ":" + req.AttachmentID + ":" + req.Version
	if ref, ok := p.refs[key]; ok {
		return &ref, nil
	}
	digest := sha256.Sum256([]byte(key))
	ref := sdk.FileReference{File: true, Ref: "blobref://test-" + hex.EncodeToString(digest[:])[:12], ProjectID: req.ProjectID, AttachmentID: req.AttachmentID, Version: req.Version, Filename: req.Filename, MIMEType: req.MIMEType, Size: req.Size, SHA256: req.SHA256}
	p.refs[key] = ref
	return &ref, nil
}
func (p *fileReferenceTestPlatform) GrantFileReference(_ context.Context, grant sdk.FileReferenceGrant) error {
	p.grants = append(p.grants, grant)
	return nil
}
func (p *fileReferenceTestPlatform) RevokeFileReferenceGrant(context.Context, sdk.FileReferenceGrant) error {
	return nil
}
func (p *fileReferenceTestPlatform) RevokeFileReference(context.Context, string) error { return nil }
func (p *fileReferenceTestPlatform) GetFileReference(_ context.Context, ref string) (*sdk.FileReference, error) {
	for _, item := range p.refs {
		if item.Ref == ref {
			copy := item
			return &copy, nil
		}
	}
	return nil, &sdk.FileReferenceError{Code: "file_not_found", Message: "not found"}
}

func TestConversationAttachmentUsesSharedFileReferenceWithoutHandoffTool(t *testing.T) {
	p := &fileReferenceTestPlatform{recordingPlatform: &recordingPlatform{projects: []sdk.PlatformProject{{ID: testProject, Name: "Project One"}}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject), tk.WithPlatform(p))
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.OnUnmount(ctx)
	conv := mkConversation(t, a, 41)
	raw := []byte("<h1>hello</h1>")
	digest := sha256.Sum256(raw)
	sha := hex.EncodeToString(digest[:])
	item := Attachment{ID: "att-shared-file", Type: "file", Name: "index.html", MimeType: "text/html", Size: int64(len(raw))}
	metadata, _ := json.Marshal(item)
	if _, err := a.store.db.Exec(`INSERT INTO conversation_attachments(id,conversation_id,user_id,metadata,content,sha256) VALUES(?,?,?,?,?,?)`, item.ID, conv.ID, 1, string(metadata), raw, sha); err != nil {
		t.Fatal(err)
	}
	msg, _, err := a.store.AppendMessageWithDeliveries(&Message{ConversationID: conv.ID, Role: "user", UserID: 1, Content: "Import this file", Attachments: []Attachment{item}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&agentInboundAdapter{app: a}).Deliver(ctx, "41", conv, msg); err != nil {
		t.Fatal(err)
	}
	if len(p.ensures) != 2 || len(p.ensures[0].Events) != 0 || len(p.ensures[1].Events) != 1 {
		t.Fatalf("expected scope ensure followed by event ensure: %+v", p.ensures)
	}
	if len(p.grants) != 1 || p.grants[0].AgentID != 41 || p.grants[0].ThreadID != conversationThreadID(conv.ID) {
		t.Fatalf("unexpected file grant: %+v", p.grants)
	}
	payload, _ := json.Marshal(p.ensures[1].Events[0].Message)
	if string(payload) == "" || !containsBytes(payload, []byte(`"type":"file_ref"`)) || containsBytes(payload, []byte("attachment_to_blob")) {
		t.Fatalf("event did not carry a direct shared file handle: %s", payload)
	}
	ref := p.grants[0].Ref
	registered, err := p.GetFileReference(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := a.OpenFileReference(context.Background(), sdk.FileReadRequest{FileReference: *registered, AgentID: 41, ThreadID: conversationThreadID(conv.ID), Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(got) != string(raw) {
		t.Fatalf("source reader bytes=%q err=%v", got, err)
	}
	if _, err := a.OpenFileReference(context.Background(), sdk.FileReadRequest{
		FileReference: *registered,
		AgentID:       41,
		ThreadID:      "thread-from-another-conversation",
		Deadline:      time.Now().Add(time.Minute),
	}); err == nil {
		t.Fatal("cross-thread file reference read unexpectedly succeeded")
	}
}

func TestConversationsSendAcceptsCanonicalGeneratedImageHandle(t *testing.T) {
	items, err := attachmentsArg(map[string]any{"attachments": []any{map[string]any{
		"_file": true, "ref": "blobref://generated-image", "filename": "generated-image.png",
		"mimeType": "image/png", "size": 123,
	}}}, "attachments")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].File || items[0].Ref != "blobref://generated-image" || items[0].Type != "image" || items[0].Name != "generated-image.png" || items[0].MimeType != "image/png" || items[0].Size != 123 {
		t.Fatalf("canonical image handle was not normalized: %#v", items)
	}
}

func containsBytes(value, needle []byte) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		match := true
		for j := range needle {
			if value[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
