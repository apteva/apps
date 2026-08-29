package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
)

func TestParseRawEmlExtractsAttachmentAndInlinePart(t *testing.T) {
	fileData := []byte("invoice bytes")
	imageData := []byte{0x89, 0x50, 0x4e, 0x47, 0x01, 0x02}
	raw := "From: Customer <customer@example.com>\r\n" +
		"To: support@example.com\r\n" +
		"Subject: Files\r\n" +
		"Message-ID: <files@example.com>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/related; boundary=outer\r\n\r\n" +
		"--outer\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nPlease see the files.\r\n" +
		"--outer\r\nContent-Type: application/pdf; name=invoice.pdf\r\n" +
		"Content-Disposition: attachment; filename=invoice.pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(fileData) + "\r\n" +
		"--outer\r\nContent-Type: image/png; name=logo.png\r\n" +
		"Content-Disposition: inline; filename=logo.png\r\n" +
		"Content-ID: <brand-logo>\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(imageData) + "\r\n" +
		"--outer--\r\n"

	parsed, err := parseRawEml([]byte(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.BodyText != "Please see the files." && parsed.BodyText != "Please see the files.\r\n" {
		t.Fatalf("body=%q", parsed.BodyText)
	}
	if len(parsed.Attachments) != 2 {
		t.Fatalf("attachments=%+v", parsed.Attachments)
	}
	invoice, logo := parsed.Attachments[0], parsed.Attachments[1]
	if invoice.Filename != "invoice.pdf" || invoice.ContentType != "application/pdf" || invoice.Disposition != "attachment" || !bytes.Equal(invoice.Data, fileData) {
		t.Fatalf("invoice=%+v data=%q", invoice.MessageAttachment, invoice.Data)
	}
	if logo.Filename != "logo.png" || logo.Disposition != "inline" || logo.ContentID != "brand-logo" || !bytes.Equal(logo.Data, imageData) {
		t.Fatalf("logo=%+v data=%v", logo.MessageAttachment, logo.Data)
	}
}

func TestPrepareInboundAttachmentsStoresBytesAndReturnsMetadataOnly(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{"email_provider": float64(1), "storage": float64(2)},
		callAppReply: json.RawMessage(`{
			"id": 901,
			"name": "invoice.pdf",
			"content_type": "application/pdf",
			"size_bytes": 13
		}`),
	}
	ctx := newTestCtx(t, plat)
	prepared := prepareInboundAttachments(ctx, "test-proj", []providerAttachment{{
		MessageAttachment: MessageAttachment{
			Filename:         "invoice.pdf",
			ContentType:      "application/pdf",
			Disposition:      "attachment",
			Source:           "mime",
			ProviderRef:      "mime:0.1",
			ProcessingStatus: "pending",
		},
		Data: []byte("invoice bytes"),
	}})
	if len(prepared) != 1 {
		t.Fatalf("prepared=%+v", prepared)
	}
	att := prepared[0]
	if att.StorageID != 901 || att.Source != "storage" || att.ProcessingStatus != "ready" || len(att.Data) != 0 || att.MediaURL != "" || att.URL != "" {
		t.Fatalf("prepared attachment=%+v data=%v", att.MessageAttachment, att.Data)
	}
	if len(plat.callAppCalls) != 1 || plat.callAppCalls[0].App != "storage" || plat.callAppCalls[0].Tool != "files_upload" {
		t.Fatalf("storage calls=%+v", plat.callAppCalls)
	}
	uploadJSON, _ := json.Marshal(plat.callAppCalls[0].Input)
	if !bytes.Contains(uploadJSON, []byte(base64.StdEncoding.EncodeToString([]byte("invoice bytes")))) {
		t.Fatalf("storage upload did not receive bytes: %s", uploadJSON)
	}
	consumerJSON, _ := json.Marshal(consumerAttachmentMetadata([]MessageAttachment{att.MessageAttachment}))
	if bytes.Contains(consumerJSON, []byte("content_base64")) || bytes.Contains(consumerJSON, []byte("invoice bytes")) {
		t.Fatalf("consumer metadata leaked bytes: %s", consumerJSON)
	}
}

func TestTwilioInboundAttachmentsIngestsEveryMediaItem(t *testing.T) {
	originalFetcher := twilioMediaFetcher
	t.Cleanup(func() { twilioMediaFetcher = originalFetcher })
	twilioMediaFetcher = func(_ context.Context, rawURL, accountSID, authToken string) ([]byte, string, string, error) {
		if accountSID != "AC123" || authToken != "secret" {
			return nil, "", "", fmt.Errorf("unexpected credentials sid=%q token=%q", accountSID, authToken)
		}
		return []byte(rawURL), "application/octet-stream", "", nil
	}
	form := url.Values{
		"NumMedia":          {"2"},
		"MediaUrl0":         {"https://api.twilio.com/media/first"},
		"MediaContentType0": {"image/jpeg"},
		"MediaUrl1":         {"https://api.twilio.com/media/second"},
		"MediaContentType1": {"application/pdf"},
	}
	attachments := twilioInboundAttachments(form, "SM123", "AC123", "secret")
	if len(attachments) != 2 {
		t.Fatalf("attachments=%+v", attachments)
	}
	for i, att := range attachments {
		if len(att.Data) == 0 || att.ProviderRef != "twilio:SM123:"+string(rune('0'+i)) || att.ProcessingStatus != "pending" {
			t.Fatalf("attachment %d=%+v data=%q", i, att.MessageAttachment, att.Data)
		}
	}
}

func TestTwilioInboundVoiceNotePreservesAudioMetadata(t *testing.T) {
	originalFetcher := twilioMediaFetcher
	t.Cleanup(func() { twilioMediaFetcher = originalFetcher })
	twilioMediaFetcher = func(_ context.Context, rawURL, accountSID, authToken string) ([]byte, string, string, error) {
		return []byte("ogg-opus-bytes"), "audio/ogg", "voice-note.ogg", nil
	}
	attachments := twilioInboundAttachments(url.Values{
		"NumMedia":          {"1"},
		"MediaUrl0":         {"https://api.twilio.com/media/voice"},
		"MediaContentType0": {"audio/ogg; codecs=opus"},
	}, "SMvoice", "AC123", "secret")
	if len(attachments) != 1 {
		t.Fatalf("attachments=%+v", attachments)
	}
	att := attachments[0]
	if att.ContentType != "audio/ogg; codecs=opus" || att.Filename != "voice-note.ogg" || att.SizeBytes != int64(len("ogg-opus-bytes")) || !bytes.Equal(att.Data, []byte("ogg-opus-bytes")) {
		t.Fatalf("voice attachment=%+v data=%q", att.MessageAttachment, att.Data)
	}
}

func TestInboundAttachmentInsertIsIdempotentByProviderRef(t *testing.T) {
	ctx := newTestCtx(t, nil)
	res, err := ctx.AppDB().Exec(`INSERT INTO messages
		(project_id, channel, direction, from_addr, to_addrs, status)
		VALUES ('test-proj', 'email', 'in', 'sender@example.com', '["support@example.com"]', 'received')`)
	if err != nil {
		t.Fatal(err)
	}
	messageID, _ := res.LastInsertId()
	attachment := providerAttachment{MessageAttachment: MessageAttachment{
		Filename:         "invoice.pdf",
		ContentType:      "application/pdf",
		Disposition:      "attachment",
		Source:           "storage",
		ProviderRef:      "mime:0.1",
		ProcessingStatus: "ready",
	}}
	if err := dbInsertMessageAttachments(ctx.AppDB(), "test-proj", messageID, []providerAttachment{attachment, attachment}); err != nil {
		t.Fatal(err)
	}
	got := dbMessageAttachments(ctx.AppDB(), "test-proj", messageID)
	if len(got) != 1 {
		t.Fatalf("attachments=%+v, want one idempotent row", got)
	}
}
