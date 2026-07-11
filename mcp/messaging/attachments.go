package main

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	maxAttachmentCount       = 20
	maxAttachmentInlineBytes = int64(25 << 20)
	maxAttachmentTotalBytes  = int64(25 << 20)
)

type MessageAttachment struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id,omitempty"`
	MessageID   int64  `json:"message_id,omitempty"`
	StorageID   int64  `json:"storage_id,omitempty"`
	URL         string `json:"url,omitempty"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	ContentID   string `json:"content_id,omitempty"`
	Disposition string `json:"disposition"`
	Source      string `json:"source"`
	ProviderRef string `json:"provider_ref,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
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
	storageIDs := int64ArrayArg(args, "attachment_storage_ids")
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
	add := func(in attachmentInput) error {
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
		if in.StorageID == 0 && in.URL == "" && in.ContentBase64 == "" {
			return errors.New("attachment requires storage_id, url, or content_base64")
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
	raw, ok := args["attachments"]
	if !ok || raw == nil {
		return out, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, errors.New("attachments must be an array")
	}
	seenStorage := map[int64]bool{}
	for _, existing := range out {
		if existing.StorageID > 0 {
			seenStorage[existing.StorageID] = true
		}
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
		if in.StorageID > 0 && seenStorage[in.StorageID] {
			continue
		}
		if err := add(in); err != nil {
			return nil, err
		}
		if in.StorageID > 0 {
			seenStorage[in.StorageID] = true
		}
	}
	return out, nil
}

func resolveAttachment(ctx *sdk.AppCtx, pid, channel string, in attachmentInput) (providerAttachment, error) {
	att := providerAttachment{
		MessageAttachment: MessageAttachment{
			StorageID:   in.StorageID,
			URL:         in.URL,
			Filename:    in.Filename,
			ContentType: in.ContentType,
			SizeBytes:   in.SizeBytes,
			ContentID:   in.ContentID,
			Disposition: in.Disposition,
			Source:      in.Source,
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
		}
	}
	switch {
	case att.StorageID > 0 && channel == channelEmail:
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
	case att.StorageID > 0:
		u, err := getAttachmentURL(ctx, pid, att.StorageID)
		if err != nil {
			return att, err
		}
		att.MediaURL = u
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

func getAttachmentURL(ctx *sdk.AppCtx, pid string, storageID int64) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", map[string]any{
		"_project_id": pid,
		"id":          storageID,
		"ttl_seconds": 86400,
	}, &out); err != nil {
		return "", fmt.Errorf("storage files_get_url %d: %w", storageID, err)
	}
	if strings.TrimSpace(out.URL) == "" {
		return "", fmt.Errorf("storage files_get_url %d returned empty url", storageID)
	}
	return out.URL, nil
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
		_, err := db.Exec(`INSERT INTO message_attachments
			(project_id, message_id, storage_id, url, filename, content_type, size_bytes,
			 content_id, disposition, source, provider_ref)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			pid, messageID, nullableInt64(att.StorageID), nullableString(att.URL),
			att.Filename, nullableString(att.ContentType), att.SizeBytes,
			nullableString(att.ContentID), att.Disposition, att.Source, nullableString(att.ProviderRef),
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
		disposition, source, COALESCE(provider_ref,''), COALESCE(created_at,'')
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
			&a.ContentType, &a.SizeBytes, &a.ContentID, &a.Disposition, &a.Source, &a.ProviderRef, &a.CreatedAt); err == nil {
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
		disposition, source, COALESCE(provider_ref,''), COALESCE(created_at,'')
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
			&attachment.Source, &attachment.ProviderRef, &attachment.CreatedAt) == nil {
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
