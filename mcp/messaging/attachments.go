package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	maxAttachmentCount       = 20
	maxAttachmentInlineBytes = int64(25 << 20)
	maxAttachmentTotalBytes  = int64(25 << 20)
	whatsAppImageMaxBytes    = int64(5 << 20)
	whatsAppMediaMaxBytes    = int64(16 << 20)
	whatsAppStickerMaxBytes  = int64(100 << 10)
)

type MessageAttachment struct {
	ID               int64  `json:"id"`
	ProjectID        string `json:"project_id,omitempty"`
	MessageID        int64  `json:"message_id,omitempty"`
	StorageID        int64  `json:"storage_id,omitempty"`
	URL              string `json:"url,omitempty"`
	Filename         string `json:"filename"`
	ContentType      string `json:"content_type,omitempty"`
	SizeBytes        int64  `json:"size_bytes,omitempty"`
	ContentID        string `json:"content_id,omitempty"`
	Disposition      string `json:"disposition"`
	Source           string `json:"source"`
	ProviderRef      string `json:"provider_ref,omitempty"`
	ProcessingStatus string `json:"processing_status"`
	ProcessingError  string `json:"processing_error,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
}

type attachmentInput struct {
	StorageID     int64
	URL           string
	Filename      string
	ContentType   string
	SizeBytes     int64
	ContentID     string
	Disposition   string
	Source        string
	ContentBase64 string
}

type providerAttachment struct {
	MessageAttachment
	Data     []byte
	MediaURL string
}

func prepareMessageAttachments(ctx *sdk.AppCtx, pid, channel string, args map[string]any) ([]providerAttachment, []int64, error) {
	inputs, err := attachmentInputsFromArgs(args)
	if err != nil {
		return nil, nil, err
	}
	if len(inputs) == 0 {
		return nil, nil, nil
	}
	if len(inputs) > maxAttachmentCount {
		return nil, nil, fmt.Errorf("at most %d attachments are allowed", maxAttachmentCount)
	}
	out := make([]providerAttachment, 0, len(inputs))
	storageIDs := []int64{}
	var totalBytes int64
	for _, in := range inputs {
		att, err := resolveAttachment(ctx, pid, channel, in)
		if err != nil {
			return nil, nil, err
		}
		if len(att.Data) > 0 {
			totalBytes += int64(len(att.Data))
			if totalBytes > maxAttachmentTotalBytes {
				return nil, nil, fmt.Errorf("attachments exceed %d bytes in total", maxAttachmentTotalBytes)
			}
		}
		out = append(out, att)
		if att.StorageID > 0 && !int64SliceContains(storageIDs, att.StorageID) {
			storageIDs = append(storageIDs, att.StorageID)
		}
	}
	return out, storageIDs, nil
}

func attachmentInputsFromArgs(args map[string]any) ([]attachmentInput, error) {
	out := []attachmentInput{}
	seenStorage := map[int64]bool{}
	seenURL := map[string]bool{}
	add := func(in attachmentInput) error {
		in.URL = strings.TrimSpace(in.URL)
		in.Filename = safeAttachmentFilename(in.Filename)
		in.ContentType = strings.TrimSpace(in.ContentType)
		in.ContentID = strings.Trim(strings.TrimSpace(in.ContentID), "<>")
		in.Disposition = normaliseAttachmentDisposition(in.Disposition)
		if in.Source == "" {
			switch {
			case in.StorageID > 0:
				in.Source = "storage"
			case in.URL != "":
				in.Source = "external_url"
			default:
				in.Source = "inline"
			}
		}
		sourceCount := 0
		if in.StorageID > 0 {
			sourceCount++
		}
		if in.URL != "" {
			sourceCount++
		}
		if in.ContentBase64 != "" {
			sourceCount++
		}
		if sourceCount == 0 {
			return errors.New("attachment requires exactly one of storage_id, url, or content_base64")
		}
		if sourceCount > 1 {
			return errors.New("attachment must use exactly one of storage_id, url, or content_base64")
		}
		if in.StorageID > 0 {
			if seenStorage[in.StorageID] {
				return nil
			}
			seenStorage[in.StorageID] = true
		}
		if in.URL != "" {
			if seenURL[in.URL] {
				return nil
			}
			seenURL[in.URL] = true
		}
		if in.Filename == "" {
			switch {
			case in.StorageID > 0:
				in.Filename = fmt.Sprintf("attachment-%d", in.StorageID)
			case in.URL != "":
				in.Filename = filenameFromURL(in.URL)
			default:
				in.Filename = "attachment"
			}
		}
		out = append(out, in)
		return nil
	}
	for _, id := range int64ArrayArg(args, "attachment_storage_ids") {
		if id > 0 {
			if err := add(attachmentInput{StorageID: id}); err != nil {
				return nil, err
			}
		}
	}
	if mediaURL := strings.TrimSpace(strArg(args, "media_url")); mediaURL != "" {
		if err := add(attachmentInput{URL: mediaURL, Source: "external_url"}); err != nil {
			return nil, err
		}
	}
	raw, ok := args["attachments"]
	if !ok || raw == nil {
		return out, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, errors.New("attachments must be an array")
	}
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("attachments[%d] must be an object", i)
		}
		in := attachmentInput{
			StorageID:     int64Arg(m, "storage_id"),
			URL:           strings.TrimSpace(strArg(m, "url")),
			Filename:      strArg(m, "filename"),
			ContentType:   strArg(m, "content_type"),
			SizeBytes:     int64Arg(m, "size_bytes"),
			ContentID:     strArg(m, "content_id"),
			Disposition:   strArg(m, "disposition"),
			Source:        strArg(m, "source"),
			ContentBase64: strArg(m, "content_base64"),
		}
		if err := add(in); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func validatePhoneMessageMode(channel, body, contentSID, contentVariables string, inputs []attachmentInput) error {
	hasMedia := len(inputs) > 0
	if contentSID != "" {
		if body != "" || hasMedia {
			return errors.New("content_sid cannot be combined with body, media_url, or attachments; template text and media must come from ContentVariables")
		}
		return nil
	}
	if contentVariables != "" {
		return errors.New("content_variables requires content_sid")
	}
	if body == "" && !hasMedia {
		return errors.New("body, media_url, or at least one attachment required")
	}
	if channel == channelWhatsApp && len(inputs) > 1 {
		return errors.New("whatsapp supports exactly one media attachment per message; send additional files as separate messages")
	}
	return nil
}

func validateWhatsAppAttachments(body string, attachments []providerAttachment) error {
	if len(attachments) > 1 {
		return errors.New("whatsapp supports exactly one media attachment per message; send additional files as separate messages")
	}
	if len(attachments) == 0 {
		return nil
	}
	att := attachments[0]
	if strings.TrimSpace(att.MediaURL) == "" {
		return errors.New("whatsapp attachment did not resolve to a provider-reachable media URL")
	}
	contentType := normaliseAttachmentMediaType(att.ContentType)
	kind, supported, captionCompatible := whatsAppMediaTypePolicy(contentType)
	if contentType != "" && contentType != "application/octet-stream" && !supported {
		return fmt.Errorf("whatsapp does not support attachment content_type %q", contentType)
	}
	if body != "" && supported && !captionCompatible {
		return fmt.Errorf("whatsapp cannot deliver body text with %s media (%s); send the text as a separate message", kind, contentType)
	}
	if att.SizeBytes <= 0 || !supported {
		return nil
	}
	limit := whatsAppMediaMaxBytes
	switch kind {
	case "image":
		limit = whatsAppImageMaxBytes
	case "sticker":
		limit = whatsAppStickerMaxBytes
	}
	if att.SizeBytes > limit {
		return fmt.Errorf("whatsapp %s attachment exceeds %d-byte limit (got %d bytes)", kind, limit, att.SizeBytes)
	}
	return nil
}

func normaliseAttachmentMediaType(contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
		return strings.ToLower(mediaType)
	}
	return contentType
}

func whatsAppMediaTypePolicy(contentType string) (kind string, supported, captionCompatible bool) {
	switch contentType {
	case "", "application/octet-stream":
		return "unknown", false, false
	case "image/jpeg", "image/jpg", "image/png":
		return "image", true, true
	case "image/heic":
		return "image", true, false
	case "image/webp":
		return "sticker", true, false
	case "audio/ogg", "audio/mpeg", "audio/mp3", "audio/3gpp", "audio/aac", "audio/ac3", "audio/amr", "audio/amr-nb":
		return "audio", true, false
	case "video/mp4", "video/mpeg4":
		return "video", true, false
	case "application/pdf", "application/msword", "application/vnd.ms-excel", "application/vnd.ms-powerpoint",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return "document", true, false
	case "application/vcard", "text/vcard", "text/x-vcard":
		return "contact", true, false
	default:
		return "unknown", false, false
	}
}

func resolveAttachment(ctx *sdk.AppCtx, pid, channel string, in attachmentInput) (providerAttachment, error) {
	att := providerAttachment{
		MessageAttachment: MessageAttachment{
			StorageID:        in.StorageID,
			URL:              in.URL,
			Filename:         in.Filename,
			ContentType:      in.ContentType,
			SizeBytes:        in.SizeBytes,
			ContentID:        in.ContentID,
			Disposition:      in.Disposition,
			Source:           in.Source,
			ProcessingStatus: "ready",
		},
	}
	if in.ContentBase64 != "" {
		data, err := decodeAttachmentBase64(in.ContentBase64)
		if err != nil {
			return att, err
		}
		att.Data = data
		att.SizeBytes = int64(len(data))
		if att.ContentType == "" {
			att.ContentType = http.DetectContentType(data)
		}
		if isAppDepBound(ctx, "storage") {
			stored, err := uploadAttachmentToStorage(ctx, pid, att)
			if err != nil {
				return att, err
			}
			att.StorageID = stored.StorageID
			if stored.Filename != "" {
				att.Filename = stored.Filename
			}
			if stored.ContentType != "" {
				att.ContentType = stored.ContentType
			}
			if stored.SizeBytes > 0 {
				att.SizeBytes = stored.SizeBytes
			}
			att.Source = "upload"
		} else if channel != channelEmail {
			return att, errors.New("phone-channel attachments with content_base64 require the storage app so Messaging can mint a Twilio-reachable media URL")
		} else {
			att.ProcessingStatus = "unavailable"
			att.ProcessingError = "storage app is not bound; attachment bytes were sent but could not be retained for other apps"
		}
	}
	switch {
	case att.StorageID > 0 && channel == channelEmail && len(att.Data) == 0:
		content, err := getAttachmentContent(ctx, pid, att.StorageID)
		if err != nil {
			return att, err
		}
		att.Data = content.Data
		if att.Filename == "" || strings.HasPrefix(att.Filename, "attachment-") {
			att.Filename = content.Filename
		}
		if att.ContentType == "" {
			att.ContentType = content.ContentType
		}
		att.SizeBytes = content.SizeBytes
	case att.StorageID > 0 && channel == channelEmail:
		// Inline/base64 bytes were already uploaded above and remain in
		// memory for this provider send; no need to download them again.
	case att.StorageID > 0:
		stored, err := getAttachmentURL(ctx, pid, att.StorageID)
		if err != nil {
			return att, err
		}
		att.MediaURL = stored.URL
		if att.Filename == "" || strings.HasPrefix(att.Filename, "attachment-") {
			att.Filename = firstNonEmpty(stored.Filename, att.Filename)
		}
		if att.ContentType == "" {
			att.ContentType = stored.ContentType
		}
		if att.SizeBytes == 0 {
			att.SizeBytes = stored.SizeBytes
		}
	case att.URL != "" && channel == channelEmail:
		data, contentType, size, err := fetchURLAttachment(att.URL)
		if err != nil {
			return att, err
		}
		att.Data = data
		if att.ContentType == "" {
			att.ContentType = contentType
		}
		att.SizeBytes = size
	case att.URL != "":
		u, err := url.Parse(strings.TrimSpace(att.URL))
		if err != nil {
			return att, errors.New("invalid attachment URL")
		}
		if err := validatePublicHTTPURL(u); err != nil {
			return att, fmt.Errorf("attachment URL: %w", err)
		}
		att.MediaURL = att.URL
	}
	if att.ContentType == "" {
		att.ContentType = "application/octet-stream"
	}
	if att.SizeBytes == 0 && len(att.Data) > 0 {
		att.SizeBytes = int64(len(att.Data))
	}
	return att, nil
}

type storageContentOut struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	ContentType   string `json:"content_type"`
	SizeBytes     int64  `json:"size_bytes"`
	ContentBase64 string `json:"content_base64"`
}

type attachmentContent struct {
	Filename    string
	ContentType string
	SizeBytes   int64
	Data        []byte
}

func getAttachmentContent(ctx *sdk.AppCtx, pid string, storageID int64) (attachmentContent, error) {
	var out storageContentOut
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_content", map[string]any{
		"_project_id": pid,
		"id":          storageID,
	}, &out); err != nil {
		return attachmentContent{}, fmt.Errorf("storage files_get_content %d: %w", storageID, err)
	}
	data, err := decodeAttachmentBase64(out.ContentBase64)
	if err != nil {
		return attachmentContent{}, fmt.Errorf("storage file %d content: %w", storageID, err)
	}
	return attachmentContent{
		Filename:    safeAttachmentFilename(out.Name),
		ContentType: out.ContentType,
		SizeBytes:   out.SizeBytes,
		Data:        data,
	}, nil
}

type attachmentURL struct {
	URL         string
	Filename    string
	ContentType string
	SizeBytes   int64
}

func getAttachmentURL(ctx *sdk.AppCtx, pid string, storageID int64) (attachmentURL, error) {
	var metadata struct {
		Found bool `json:"found"`
		File  *struct {
			Name        string `json:"name"`
			ContentType string `json:"content_type"`
			SizeBytes   int64  `json:"size_bytes"`
		} `json:"file"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get", map[string]any{
		"_project_id": pid,
		"id":          storageID,
	}, &metadata); err != nil {
		return attachmentURL{}, fmt.Errorf("storage files_get %d: %w", storageID, err)
	}
	if !metadata.Found || metadata.File == nil {
		return attachmentURL{}, fmt.Errorf("storage file %d not found", storageID)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", map[string]any{
		"_project_id": pid,
		"id":          storageID,
		"ttl_seconds": 86400,
	}, &out); err != nil {
		return attachmentURL{}, fmt.Errorf("storage files_get_url %d: %w", storageID, err)
	}
	if strings.TrimSpace(out.URL) == "" {
		return attachmentURL{}, fmt.Errorf("storage files_get_url %d returned empty url", storageID)
	}
	u, err := url.Parse(strings.TrimSpace(out.URL))
	if err != nil {
		return attachmentURL{}, fmt.Errorf("storage files_get_url %d returned invalid url", storageID)
	}
	if err := validatePublicHTTPURL(u); err != nil {
		return attachmentURL{}, fmt.Errorf("storage files_get_url %d: %w", storageID, err)
	}
	return attachmentURL{
		URL:         out.URL,
		Filename:    safeAttachmentFilename(metadata.File.Name),
		ContentType: normaliseAttachmentMediaType(metadata.File.ContentType),
		SizeBytes:   metadata.File.SizeBytes,
	}, nil
}

func uploadAttachmentToStorage(ctx *sdk.AppCtx, pid string, att providerAttachment) (MessageAttachment, error) {
	var out struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		ContentType string `json:"content_type"`
		SizeBytes   int64  `json:"size_bytes"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", map[string]any{
		"_project_id":    pid,
		"name":           att.Filename,
		"content_type":   att.ContentType,
		"content_base64": base64.StdEncoding.EncodeToString(att.Data),
		"folder":         "/messaging/attachments/",
		"source":         "messaging",
		"visibility":     "signed",
	}, &out); err != nil {
		return MessageAttachment{}, fmt.Errorf("storage files_upload %q: %w", att.Filename, err)
	}
	if out.ID == 0 {
		return MessageAttachment{}, fmt.Errorf("storage files_upload %q returned no id", att.Filename)
	}
	return MessageAttachment{
		StorageID:   out.ID,
		Filename:    safeAttachmentFilename(firstNonEmpty(out.Name, att.Filename)),
		ContentType: firstNonEmpty(out.ContentType, att.ContentType),
		SizeBytes:   firstPositiveInt64(out.SizeBytes, att.SizeBytes),
	}, nil
}

// prepareInboundAttachments turns provider bytes into durable, app-facing
// metadata. Storage is the durable boundary: routed apps receive IDs and
// descriptive metadata, never MIME bytes, base64, provider credentials, or
// signed Storage URLs. A failed file does not fail the parent message.
func prepareInboundAttachments(ctx *sdk.AppCtx, pid string, inputs []providerAttachment) []providerAttachment {
	if len(inputs) > maxAttachmentCount {
		inputs = inputs[:maxAttachmentCount]
	}
	out := make([]providerAttachment, 0, len(inputs))
	var totalBytes int64
	for i := range inputs {
		att := inputs[i]
		att.Filename = safeAttachmentFilename(att.Filename)
		if att.Filename == "" {
			att.Filename = fmt.Sprintf("attachment-%d", i+1)
		}
		att.ContentType = strings.TrimSpace(att.ContentType)
		if att.ContentType == "" && len(att.Data) > 0 {
			att.ContentType = http.DetectContentType(att.Data)
		}
		if att.ContentType == "" {
			att.ContentType = "application/octet-stream"
		}
		att.ContentID = strings.Trim(strings.TrimSpace(att.ContentID), "<>")
		att.Disposition = normaliseAttachmentDisposition(att.Disposition)
		if att.Source == "" {
			att.Source = "provider"
		}
		if att.ProcessingStatus == "" {
			att.ProcessingStatus = "ready"
		}
		if len(att.Data) > 0 {
			att.SizeBytes = int64(len(att.Data))
			totalBytes += att.SizeBytes
			switch {
			case att.SizeBytes > maxAttachmentInlineBytes:
				att.ProcessingStatus = "failed"
				att.ProcessingError = fmt.Sprintf("attachment exceeds %d bytes", maxAttachmentInlineBytes)
			case totalBytes > maxAttachmentTotalBytes:
				att.ProcessingStatus = "failed"
				att.ProcessingError = fmt.Sprintf("attachments exceed %d bytes in total", maxAttachmentTotalBytes)
			case !isAppDepBound(ctx, "storage"):
				att.ProcessingStatus = "unavailable"
				att.ProcessingError = "storage app is not bound; inbound bytes could not be made durable"
			default:
				stored, err := uploadAttachmentToStorage(ctx, pid, att)
				if err != nil {
					att.ProcessingStatus = "failed"
					att.ProcessingError = truncate(err.Error(), 500)
				} else {
					att.StorageID = stored.StorageID
					att.URL = ""
					att.Filename = firstNonEmpty(stored.Filename, att.Filename)
					att.ContentType = firstNonEmpty(stored.ContentType, att.ContentType)
					att.SizeBytes = firstPositiveInt64(stored.SizeBytes, att.SizeBytes)
					att.Source = "storage"
					att.ProcessingStatus = "ready"
					att.ProcessingError = ""
				}
			}
		}
		// Provider bytes are transport-only and must never survive into a
		// routed payload or tool response.
		att.Data = nil
		att.MediaURL = ""
		out = append(out, att)
	}
	return out
}

// consumerAttachmentMetadata returns a stable non-nil slice for app-to-app
// contracts and strips the internal project owner field. The type contains no
// byte-bearing field, so it is safe to use in send results and route payloads.
func consumerAttachmentMetadata(in []MessageAttachment) []MessageAttachment {
	out := make([]MessageAttachment, len(in))
	copy(out, in)
	for i := range out {
		out[i].ProjectID = ""
		if out[i].ProcessingStatus == "" {
			out[i].ProcessingStatus = "ready"
		}
	}
	return out
}

func messageAttachmentCount(message *Message) int {
	if message == nil {
		return 0
	}
	return len(message.Attachments)
}

func fetchTwilioInboundMedia(ctx context.Context, rawURL, accountSID, authToken string) ([]byte, string, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, "", "", errors.New("invalid Twilio media URL")
	}
	if err := validatePublicHTTPURL(u); err != nil {
		return nil, "", "", fmt.Errorf("Twilio media URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") || !isTrustedTwilioMediaHost(u.Hostname()) {
		return nil, "", "", errors.New("Twilio media URL must use HTTPS on a Twilio-owned host")
	}
	client := newPublicHTTPClient(10 * time.Second)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many Twilio media redirects")
		}
		if !strings.EqualFold(req.URL.Scheme, "https") {
			return errors.New("Twilio media redirect must use HTTPS")
		}
		if err := validatePublicHTTPURL(req.URL); err != nil {
			return fmt.Errorf("Twilio media redirect: %w", err)
		}
		if !isTrustedTwilioMediaHost(req.URL.Hostname()) {
			return errors.New("Twilio media redirect must remain on a Twilio-owned host")
		}
		if isTwilioCredentialHost(req.URL.Hostname()) {
			req.SetBasicAuth(accountSID, authToken)
		} else {
			req.Header.Del("Authorization")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", "", err
	}
	if isTwilioCredentialHost(u.Hostname()) {
		req.SetBasicAuth(accountSID, authToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("fetch Twilio media: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", "", fmt.Errorf("fetch Twilio media: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentInlineBytes+1))
	if err != nil {
		return nil, "", "", err
	}
	if int64(len(data)) > maxAttachmentInlineBytes {
		return nil, "", "", fmt.Errorf("Twilio media exceeds %d bytes", maxAttachmentInlineBytes)
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = mediaType
	}
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	filename := ""
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		filename = safeAttachmentFilename(params["filename"])
	}
	if filename == "" {
		filename = filenameFromURL(resp.Request.URL.String())
	}
	return data, contentType, filename, nil
}

var twilioMediaFetcher = fetchTwilioInboundMedia

func twilioInboundAttachments(form url.Values, messageSID, accountSID, authToken string) []providerAttachment {
	count, _ := strconv.Atoi(strings.TrimSpace(form.Get("NumMedia")))
	if count <= 0 {
		return nil
	}
	if count > maxAttachmentCount {
		count = maxAttachmentCount
	}
	out := make([]providerAttachment, count)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := 0; i < count; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rawURL := strings.TrimSpace(form.Get(fmt.Sprintf("MediaUrl%d", i)))
			contentType := strings.TrimSpace(form.Get(fmt.Sprintf("MediaContentType%d", i)))
			att := providerAttachment{MessageAttachment: MessageAttachment{
				Filename:         fmt.Sprintf("media-%d", i+1),
				ContentType:      contentType,
				Disposition:      "attachment",
				Source:           "twilio",
				ProviderRef:      fmt.Sprintf("twilio:%s:%d", messageSID, i),
				ProcessingStatus: "pending",
			}}
			if rawURL == "" {
				att.ProcessingStatus = "failed"
				att.ProcessingError = fmt.Sprintf("MediaUrl%d is missing", i)
				out[i] = att
				return
			}
			data, fetchedType, filename, err := twilioMediaFetcher(ctx, rawURL, accountSID, authToken)
			if err != nil {
				att.ProcessingStatus = "failed"
				att.ProcessingError = truncate(err.Error(), 500)
				out[i] = att
				return
			}
			att.Data = data
			att.SizeBytes = int64(len(data))
			att.Filename = firstNonEmpty(filename, att.Filename)
			att.ContentType = firstNonEmpty(contentType, fetchedType)
			out[i] = att
		}()
	}
	wg.Wait()
	return out
}

func isTrustedTwilioMediaHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "twilio.com" || strings.HasSuffix(host, ".twilio.com") ||
		host == "twiliocdn.com" || strings.HasSuffix(host, ".twiliocdn.com")
}

func isTwilioCredentialHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "twilio.com" || strings.HasSuffix(host, ".twilio.com")
}

func fetchURLAttachment(rawURL string) ([]byte, string, int64, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, "", 0, errors.New("invalid attachment URL")
	}
	if err := validatePublicHTTPURL(u); err != nil {
		return nil, "", 0, fmt.Errorf("attachment URL: %w", err)
	}
	client := newPublicHTTPClient(30 * time.Second)
	resp, err := client.Get(u.String())
	if err != nil {
		return nil, "", 0, fmt.Errorf("fetch attachment url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", 0, fmt.Errorf("fetch attachment url: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentInlineBytes+1))
	if err != nil {
		return nil, "", 0, err
	}
	if int64(len(data)) > maxAttachmentInlineBytes {
		return nil, "", 0, fmt.Errorf("attachment url exceeds %d bytes", maxAttachmentInlineBytes)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "" {
		if mediaType, _, err := mime.ParseMediaType(ct); err == nil {
			ct = mediaType
		}
	}
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	return data, ct, int64(len(data)), nil
}

func dbInsertMessageAttachments(db *sql.DB, pid string, messageID int64, attachments []providerAttachment) error {
	for _, att := range attachments {
		status := strings.TrimSpace(att.ProcessingStatus)
		if status == "" {
			status = "ready"
		}
		_, err := db.Exec(`INSERT OR IGNORE INTO message_attachments
			(project_id, message_id, storage_id, url, filename, content_type, size_bytes,
			 content_id, disposition, source, provider_ref, processing_status, processing_error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			pid, messageID, nullableInt64(att.StorageID), nullableString(att.URL),
			att.Filename, nullableString(att.ContentType), att.SizeBytes,
			nullableString(att.ContentID), att.Disposition, att.Source, nullableString(att.ProviderRef),
			status, nullableString(att.ProcessingError),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func dbMessageAttachments(db *sql.DB, pid string, messageID int64) []MessageAttachment {
	rows, err := db.Query(`SELECT id, project_id, message_id, COALESCE(storage_id,0), COALESCE(url,''),
		filename, COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(content_id,''),
		disposition, source, COALESCE(provider_ref,''), COALESCE(processing_status,'ready'),
		COALESCE(processing_error,''), COALESCE(created_at,'')
		FROM message_attachments
		WHERE message_id = ? AND (? = '' OR project_id = ?)
		ORDER BY id ASC`, messageID, pid, pid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []MessageAttachment{}
	for rows.Next() {
		var a MessageAttachment
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.MessageID, &a.StorageID, &a.URL, &a.Filename,
			&a.ContentType, &a.SizeBytes, &a.ContentID, &a.Disposition, &a.Source, &a.ProviderRef,
			&a.ProcessingStatus, &a.ProcessingError, &a.CreatedAt); err == nil {
			out = append(out, a)
		}
	}
	return out
}

func dbMessageAttachmentsBatch(db *sql.DB, pid string, messageIDs []int64) map[int64][]MessageAttachment {
	out := map[int64][]MessageAttachment{}
	if len(messageIDs) == 0 {
		return out
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(messageIDs)), ",")
	args := make([]any, 0, len(messageIDs)+2)
	for _, id := range messageIDs {
		args = append(args, id)
	}
	args = append(args, pid, pid)
	rows, err := db.Query(`SELECT id, project_id, message_id, COALESCE(storage_id,0), COALESCE(url,''),
		filename, COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(content_id,''),
		disposition, source, COALESCE(provider_ref,''), COALESCE(processing_status,'ready'),
		COALESCE(processing_error,''), COALESCE(created_at,'')
		FROM message_attachments
		WHERE message_id IN (`+placeholders+`) AND (? = '' OR project_id = ?)
		ORDER BY message_id, id`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var attachment MessageAttachment
		if rows.Scan(&attachment.ID, &attachment.ProjectID, &attachment.MessageID, &attachment.StorageID, &attachment.URL,
			&attachment.Filename, &attachment.ContentType, &attachment.SizeBytes, &attachment.ContentID, &attachment.Disposition,
			&attachment.Source, &attachment.ProviderRef, &attachment.ProcessingStatus, &attachment.ProcessingError,
			&attachment.CreatedAt) == nil {
			out[attachment.MessageID] = append(out[attachment.MessageID], attachment)
		}
	}
	return out
}

func decodeAttachmentBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, ",") && strings.HasPrefix(strings.ToLower(s), "data:") {
		parts := strings.SplitN(s, ",", 2)
		s = parts[1]
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxAttachmentInlineBytes {
		return nil, fmt.Errorf("attachment exceeds %d bytes", maxAttachmentInlineBytes)
	}
	return data, nil
}

func safeAttachmentFilename(name string) string {
	name = strings.TrimSpace(strings.NewReplacer("\x00", "", "\r", " ", "\n", " ").Replace(name))
	if name == "" {
		return ""
	}
	name = filepath.Base(name)
	if name == "." || name == "/" {
		return ""
	}
	return name
}

func filenameFromURL(raw string) string {
	trimmed := strings.SplitN(raw, "?", 2)[0]
	name := safeAttachmentFilename(trimmed)
	if name == "" || name == "." {
		return "attachment"
	}
	return name
}

func normaliseAttachmentDisposition(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "inline":
		return "inline"
	default:
		return "attachment"
	}
}

func int64SliceContains(xs []int64, n int64) bool {
	for _, x := range xs {
		if x == n {
			return true
		}
	}
	return false
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func firstPositiveInt64(a, b int64) int64 {
	if a > 0 {
		return a
	}
	return b
}
