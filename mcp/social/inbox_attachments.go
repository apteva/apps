package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	inboxAttachmentImage = "image"
	inboxAttachmentVideo = "video"
	inboxAttachmentAudio = "audio"
	inboxAttachmentFile  = "file"
)

var validInboxAttachmentKinds = map[string]bool{
	inboxAttachmentImage: true,
	inboxAttachmentVideo: true,
	inboxAttachmentAudio: true,
	inboxAttachmentFile:  true,
}

// inboxAttachment is the provider-neutral representation persisted in
// inbox_items.media_json and handed to platform adapters.
type inboxAttachment struct {
	Kind         string `json:"kind"`
	Source       string `json:"source,omitempty"`
	URL          string `json:"url"`
	MIME         string `json:"mime,omitempty"`
	Name         string `json:"name,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	StorageID    int64  `json:"storage_id,omitempty"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
}

type inboxMessage struct {
	Body        string
	Attachments []inboxAttachment
}

type inboxDelivery struct {
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	ExternalID string `json:"external_id,omitempty"`
	Error      string `json:"error,omitempty"`
}

type inboxMessagePart struct {
	Kind       string
	Body       string
	Attachment *inboxAttachment
}

func inboxMessageParts(message inboxMessage) []inboxMessagePart {
	parts := make([]inboxMessagePart, 0, len(message.Attachments)+1)
	if message.Body != "" {
		parts = append(parts, inboxMessagePart{Kind: "text", Body: message.Body})
	}
	for i := range message.Attachments {
		attachment := message.Attachments[i]
		parts = append(parts, inboxMessagePart{
			Kind:       attachment.Kind,
			Attachment: &attachment,
		})
	}
	return parts
}

func outcomeFromInboxDeliveries(out inboxOutcome, deliveries []inboxDelivery) inboxOutcome {
	out.Deliveries = deliveries
	okCount := 0
	errors := make([]string, 0)
	for _, delivery := range deliveries {
		if delivery.Status == "ok" {
			okCount++
			if out.ExternalID == "" {
				out.ExternalID = delivery.ExternalID
			}
			continue
		}
		if delivery.Error != "" {
			errors = append(errors, delivery.Kind+": "+delivery.Error)
		}
	}
	switch {
	case len(deliveries) > 0 && okCount == len(deliveries):
		out.Status = "ok"
	case okCount > 0:
		out.Status = "partial"
		out.Error = strings.Join(errors, "; ")
	default:
		out.Status = "failed"
		out.Error = strings.Join(errors, "; ")
		if out.Error == "" {
			out.Error = "message delivery failed"
		}
	}
	return out
}

func (a *App) resolveInboxMessage(ctx *sdk.AppCtx, args map[string]any, projectID string) (inboxMessage, error) {
	message := inboxMessage{Body: strings.TrimSpace(toString(args["body"]))}
	defaultProjectID := strings.TrimSpace(stringArgAny(args, "media_project_id", "storage_project_id"))
	if defaultProjectID == "" {
		defaultProjectID = projectID
	}
	if defaultProjectID != projectID {
		return message, fmt.Errorf("attachment project_id must match the current project")
	}

	seenStorage := map[int64]bool{}
	storageIDs := make([]int64, 0)
	for _, raw := range toAnySlice(args["media_storage_ids"]) {
		if id := toInt64Loose(raw); id > 0 && !seenStorage[id] {
			seenStorage[id] = true
			storageIDs = append(storageIDs, id)
		}
	}

	for _, raw := range toAnySlice(args["attachments"]) {
		input, ok := raw.(map[string]any)
		if !ok {
			return message, fmt.Errorf("attachments must contain objects")
		}
		source := strings.ToLower(strings.TrimSpace(firstString(input, "source")))
		if source == "" {
			if toInt64Loose(input["storage_id"]) > 0 {
				source = "storage"
			} else if firstString(input, "url") != "" {
				source = "url"
			}
		}
		switch source {
		case "storage":
			id := toInt64Loose(input["storage_id"])
			if id <= 0 {
				return message, fmt.Errorf("storage attachment requires storage_id")
			}
			attachmentProjectID := strings.TrimSpace(firstString(input, "project_id"))
			if attachmentProjectID != "" && attachmentProjectID != projectID {
				return message, fmt.Errorf("attachment project_id must match the current project")
			}
			if !seenStorage[id] {
				seenStorage[id] = true
				storageIDs = append(storageIDs, id)
			}
		case "url":
			attachment, err := externalInboxAttachment(input)
			if err != nil {
				return message, err
			}
			message.Attachments = append(message.Attachments, attachment)
		default:
			return message, fmt.Errorf("attachment source must be storage or url")
		}
	}

	if len(storageIDs) > 0 {
		media, err := a.resolveMedia(ctx, storageIDs, projectID)
		if err != nil {
			return message, err
		}
		for _, item := range media {
			kind := inboxAttachmentKind(item.Mime, item.Name, "")
			if kind == "" {
				kind = inboxAttachmentFile
			}
			message.Attachments = append(message.Attachments, inboxAttachment{
				Kind:      kind,
				Source:    "storage",
				URL:       item.URL,
				MIME:      item.Mime,
				Name:      item.Name,
				SizeBytes: item.Bytes,
				StorageID: item.ID,
			})
		}
	}
	if message.Body == "" && len(message.Attachments) == 0 {
		return message, fmt.Errorf("body or attachment required")
	}
	return message, nil
}

func externalInboxAttachment(input map[string]any) (inboxAttachment, error) {
	rawURL := strings.TrimSpace(firstString(input, "url"))
	if err := validatePublicAttachmentURL(rawURL); err != nil {
		return inboxAttachment{}, err
	}
	mime := strings.ToLower(strings.TrimSpace(firstString(input, "mime", "mime_type", "content_type")))
	name := strings.TrimSpace(firstString(input, "name", "filename"))
	kind := inboxAttachmentKind(mime, name, firstString(input, "type", "kind"))
	if kind == "" {
		return inboxAttachment{}, fmt.Errorf("URL attachment requires type=image, video, audio, or file")
	}
	return inboxAttachment{
		Kind:      kind,
		Source:    "url",
		URL:       rawURL,
		MIME:      mime,
		Name:      name,
		SizeBytes: toInt64Loose(input["size_bytes"]),
	}, nil
}

func validatePublicAttachmentURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("attachment URL must be a public HTTPS URL")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("attachment URL must not target localhost")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()) {
		return fmt.Errorf("attachment URL must not target a private address")
	}
	return nil
}

func inboxAttachmentKind(mime, name, explicit string) string {
	explicit = strings.ToLower(strings.TrimSpace(explicit))
	if validInboxAttachmentKinds[explicit] {
		return explicit
	}
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch {
	case strings.HasPrefix(mime, "image/"):
		return inboxAttachmentImage
	case strings.HasPrefix(mime, "video/"):
		return inboxAttachmentVideo
	case strings.HasPrefix(mime, "audio/"):
		return inboxAttachmentAudio
	case mime != "":
		return inboxAttachmentFile
	}
	extensionName := name
	if parsed, err := url.Parse(name); err == nil && parsed.Path != "" {
		extensionName = parsed.Path
	}
	switch strings.ToLower(path.Ext(extensionName)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif":
		return inboxAttachmentImage
	case ".mp4", ".mov", ".webm", ".m4v":
		return inboxAttachmentVideo
	case ".mp3", ".m4a", ".aac", ".wav", ".ogg", ".opus", ".flac":
		return inboxAttachmentAudio
	}
	return ""
}

func validateInboxAttachments(item *inboxItem, mode string, providerBacked bool, attachments []inboxAttachment) (string, bool) {
	if len(attachments) == 0 {
		return "", true
	}
	if mode == inboxReplyModePrivate {
		return "private comment replies only support text", false
	}
	var allowed []string
	maxItems := 0
	if providerBacked {
		if item.Kind == inboxKindDM {
			allowed = []string{inboxAttachmentImage, inboxAttachmentVideo, inboxAttachmentAudio, inboxAttachmentFile}
			maxItems = 1
		}
	} else if def, ok := platforms[item.Platform]; ok {
		switch item.Kind {
		case inboxKindDM:
			allowed = def.Inbox.DMAttachmentTypes
			maxItems = def.Inbox.DMMaxAttachments
		case inboxKindComment, inboxKindMention:
			allowed = def.Inbox.CommentAttachmentTypes
			maxItems = def.Inbox.CommentMaxAttachments
		}
	}
	if len(allowed) == 0 {
		return fmt.Sprintf("%s does not support attachments for %s replies", item.Platform, item.Kind), false
	}
	if maxItems > 0 && len(attachments) > maxItems {
		return fmt.Sprintf("%s accepts at most %d attachment(s) for this reply", item.Platform, maxItems), false
	}
	allowedSet := map[string]bool{}
	for _, kind := range allowed {
		allowedSet[kind] = true
	}
	for _, attachment := range attachments {
		if !allowedSet[attachment.Kind] {
			return fmt.Sprintf("%s does not support %s attachments for this reply", item.Platform, attachment.Kind), false
		}
	}
	return "", true
}

func marshalInboxAttachments(attachments []inboxAttachment) string {
	if len(attachments) == 0 {
		return ""
	}
	raw, err := json.Marshal(attachments)
	if err != nil {
		return ""
	}
	return string(raw)
}

// normalizeInboxMediaJSON converts common Graph/provider attachment
// envelopes into the stable array shape consumed by the panel.
func normalizeInboxMediaJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" || string(raw) == "[]" {
		return ""
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return string(raw)
	}
	items := attachmentItems(decoded)
	normalized := make([]inboxAttachment, 0, len(items))
	for _, item := range items {
		rawURL := ""
		if direct, ok := item.(string); ok {
			rawURL = strings.TrimSpace(direct)
		} else {
			rawURL = firstDeepString(item, "url", "file_url", "video_url", "image_url", "src")
		}
		if rawURL == "" {
			continue
		}
		mime := firstDeepString(item, "mime_type", "mime", "content_type")
		name := firstDeepString(item, "name", "filename", "title")
		inferenceName := name
		if inferenceName == "" {
			inferenceName = rawURL
		}
		kind := inboxAttachmentKind(mime, inferenceName, firstDeepString(item, "type", "kind"))
		if kind == "" {
			kind = inboxAttachmentFile
		}
		normalized = append(normalized, inboxAttachment{
			Kind:         kind,
			Source:       "provider",
			URL:          rawURL,
			MIME:         mime,
			Name:         name,
			SizeBytes:    toInt64Loose(firstDeepValue(item, "size", "size_bytes")),
			ThumbnailURL: firstDeepString(item, "thumbnail_url", "preview_url"),
		})
	}
	if len(normalized) == 0 {
		return string(raw)
	}
	return marshalInboxAttachments(normalized)
}

func attachmentItems(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case map[string]any:
		for _, key := range []string{"data", "attachments", "items", "media"} {
			if items, ok := typed[key].([]any); ok {
				return items
			}
		}
		return []any{typed}
	case string:
		return []any{map[string]any{"url": typed}}
	default:
		return nil
	}
}

func firstDeepValue(value any, keys ...string) any {
	wanted := map[string]bool{}
	for _, key := range keys {
		wanted[key] = true
	}
	var visit func(any) any
	visit = func(current any) any {
		switch typed := current.(type) {
		case map[string]any:
			for key, candidate := range typed {
				if wanted[key] && candidate != nil {
					return candidate
				}
			}
			for _, candidate := range typed {
				if found := visit(candidate); found != nil {
					return found
				}
			}
		case []any:
			for _, candidate := range typed {
				if found := visit(candidate); found != nil {
					return found
				}
			}
		}
		return nil
	}
	return visit(value)
}
