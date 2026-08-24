package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	maxMessageAttachments   = 20
	maxMessageJSONBodyBytes = int64(40 << 20)
)

// messagingAttachment mirrors the stable attachment metadata returned and
// dispatched by Messaging v0.13.43. CRM deliberately stores references and
// metadata only; file bytes continue to live in Storage/Messaging.
type messagingAttachment struct {
	ID               int64  `json:"id"`
	MessageID        int64  `json:"message_id,omitempty"`
	StorageID        int64  `json:"storage_id,omitempty"`
	URL              string `json:"url,omitempty"`
	Filename         string `json:"filename,omitempty"`
	ContentType      string `json:"content_type,omitempty"`
	SizeBytes        int64  `json:"size_bytes,omitempty"`
	ContentID        string `json:"content_id,omitempty"`
	Disposition      string `json:"disposition,omitempty"`
	Source           string `json:"source,omitempty"`
	ProviderRef      string `json:"provider_ref,omitempty"`
	ProcessingStatus string `json:"processing_status,omitempty"`
	ProcessingError  string `json:"processing_error,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
}

type ActivityAttachment struct {
	ID                    int64  `json:"id"`
	MessagingAttachmentID int64  `json:"messaging_attachment_id"`
	StorageID             int64  `json:"storage_id,omitempty"`
	URL                   string `json:"url,omitempty"`
	DownloadURL           string `json:"download_url,omitempty"`
	Filename              string `json:"filename,omitempty"`
	ContentType           string `json:"content_type,omitempty"`
	SizeBytes             int64  `json:"size_bytes,omitempty"`
	ContentID             string `json:"content_id,omitempty"`
	Disposition           string `json:"disposition,omitempty"`
	Source                string `json:"source,omitempty"`
	ProviderRef           string `json:"provider_ref,omitempty"`
	ProcessingStatus      string `json:"processing_status,omitempty"`
	ProcessingError       string `json:"processing_error,omitempty"`
	CreatedAt             string `json:"created_at,omitempty"`
}

func messagingAttachmentsFromAny(raw any) ([]messagingAttachment, error) {
	if raw == nil {
		return []messagingAttachment{}, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, errors.New("attachments must be an array")
	}
	var out []messagingAttachment
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, errors.New("attachments must be an array of attachment objects")
	}
	if len(out) > maxMessageAttachments {
		return nil, fmt.Errorf("attachments supports at most %d files", maxMessageAttachments)
	}
	return out, nil
}

func validateOutboundAttachments(args map[string]any) error {
	count := 0
	if raw, ok := args["attachments"]; ok {
		items, ok := raw.([]any)
		if !ok {
			// Tool callers in Go tests may provide typed slices.
			b, err := json.Marshal(raw)
			if err != nil {
				return errors.New("attachments must be an array")
			}
			if err := json.Unmarshal(b, &items); err != nil {
				return errors.New("attachments must be an array")
			}
		}
		count += len(items)
		for i, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("attachments[%d] must be an object", i)
			}
			sources := 0
			if int64FromAny(m["storage_id"]) > 0 {
				sources++
			}
			if strings.TrimSpace(anyString(m["url"])) != "" {
				sources++
			}
			if strings.TrimSpace(anyString(m["content_base64"])) != "" {
				sources++
			}
			if sources != 1 {
				return fmt.Errorf("attachments[%d] must provide exactly one of storage_id, url, or content_base64", i)
			}
		}
	}
	if raw, ok := args["attachment_storage_ids"]; ok {
		ids, ok := raw.([]any)
		if !ok {
			b, err := json.Marshal(raw)
			if err != nil || json.Unmarshal(b, &ids) != nil {
				return errors.New("attachment_storage_ids must be an array")
			}
		}
		for i, id := range ids {
			if int64FromAny(id) <= 0 {
				return fmt.Errorf("attachment_storage_ids[%d] must be a positive integer", i)
			}
		}
		count += len(ids)
	}
	if count > maxMessageAttachments {
		return fmt.Errorf("attachments supports at most %d files", maxMessageAttachments)
	}
	return nil
}

func insertActivityAttachmentsTx(tx *sql.Tx, pid string, activityID int64, attachments []messagingAttachment) error {
	for _, a := range attachments {
		if a.ID <= 0 {
			continue
		}
		status := strings.TrimSpace(a.ProcessingStatus)
		if status == "" {
			status = "ready"
		}
		_, err := tx.Exec(`INSERT INTO contact_activity_attachments
			(project_id, activity_id, messaging_attachment_id, storage_id, url,
			 filename, content_type, size_bytes, content_id, disposition, source,
			 provider_ref, processing_status, processing_error, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(NULLIF(?,''), CURRENT_TIMESTAMP))
			ON CONFLICT(project_id, activity_id, messaging_attachment_id) DO UPDATE SET
			 storage_id=excluded.storage_id, url=excluded.url, filename=excluded.filename,
			 content_type=excluded.content_type, size_bytes=excluded.size_bytes,
			 content_id=excluded.content_id, disposition=excluded.disposition,
			 source=excluded.source, provider_ref=excluded.provider_ref,
			 processing_status=excluded.processing_status,
			 processing_error=excluded.processing_error`,
			pid, activityID, a.ID, nullInt64(a.StorageID), nullStr(a.URL), nullStr(a.Filename),
			nullStr(a.ContentType), a.SizeBytes, nullStr(a.ContentID), nullStr(a.Disposition),
			nullStr(a.Source), nullStr(a.ProviderRef), status, nullStr(a.ProcessingError), a.CreatedAt)
		if err != nil {
			return err
		}
	}
	return nil
}

func nullInt64(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func upsertActivityAttachments(db *sql.DB, pid string, activityID int64, attachments []messagingAttachment) error {
	if len(attachments) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertActivityAttachmentsTx(tx, pid, activityID, attachments); err != nil {
		return err
	}
	return tx.Commit()
}

func activityAttachmentDownloadURL(pid string, a *ActivityAttachment) string {
	if a == nil || (a.ProcessingStatus != "" && a.ProcessingStatus != "ready") {
		return ""
	}
	if a.StorageID > 0 {
		return fmt.Sprintf("/api/apps/storage/files/%d/content?project_id=%s", a.StorageID, url.QueryEscape(pid))
	}
	return a.URL
}

func loadActivityAttachments(db *sql.DB, pid string, activities []*Activity) error {
	ids := make([]int64, 0, len(activities))
	byID := make(map[int64]*Activity, len(activities))
	for _, a := range activities {
		if a == nil {
			continue
		}
		a.Attachments = []ActivityAttachment{}
		if a.ID > 0 {
			ids = append(ids, a.ID)
			byID[a.ID] = a
		}
	}
	if len(ids) == 0 {
		return nil
	}
	marks := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, pid)
	for i, id := range ids {
		marks[i] = "?"
		args = append(args, id)
	}
	rows, err := db.Query(`SELECT id, activity_id, messaging_attachment_id,
		COALESCE(storage_id,0), COALESCE(url,''), COALESCE(filename,''),
		COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(content_id,''),
		COALESCE(disposition,''), COALESCE(source,''), COALESCE(provider_ref,''),
		COALESCE(processing_status,'ready'), COALESCE(processing_error,''), COALESCE(created_at,'')
		FROM contact_activity_attachments
		WHERE project_id = ? AND activity_id IN (`+strings.Join(marks, ",")+`)
		ORDER BY activity_id, id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var activityID int64
		var a ActivityAttachment
		if err := rows.Scan(&a.ID, &activityID, &a.MessagingAttachmentID, &a.StorageID,
			&a.URL, &a.Filename, &a.ContentType, &a.SizeBytes, &a.ContentID,
			&a.Disposition, &a.Source, &a.ProviderRef, &a.ProcessingStatus,
			&a.ProcessingError, &a.CreatedAt); err != nil {
			return err
		}
		a.DownloadURL = activityAttachmentDownloadURL(pid, &a)
		if act := byID[activityID]; act != nil {
			act.Attachments = append(act.Attachments, a)
		}
	}
	return rows.Err()
}
