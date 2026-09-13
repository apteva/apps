package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

const maxUploadBytes = 10 << 20

// Originals are bounded blobs in the app database, committed atomically with
// metadata. This keeps app backup/restore and conversation deletion coherent.
func imagePreview(raw []byte) (string, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return "", errors.New("image exceeds 40 megapixels")
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	w, h := cfg.Width, cfg.Height
	if w > 1600 || h > 1600 {
		if w >= h {
			h = h * 1600 / w
			w = 1600
		} else {
			w = w * 1600 / h
			h = 1600
		}
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	for {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		b := img.Bounds()
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
		var out bytes.Buffer
		if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); err != nil {
			return "", err
		}
		if out.Len() <= 350<<10 {
			return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(out.Bytes()), nil
		}
		w = w * 3 / 4
		h = h * 3 / 4
		if w < 1 || h < 1 {
			return "", errors.New("cannot resize image")
		}
	}
}
func (a *App) loadAttachment(chat, id string) (Attachment, []byte, error) {
	var meta string
	var data []byte
	var item Attachment
	err := a.store.db.QueryRow(`SELECT metadata,content FROM conversation_attachments WHERE conversation_id=? AND id=?`, chat, id).Scan(&meta, &data)
	if err != nil {
		return item, nil, err
	}
	err = json.Unmarshal([]byte(meta), &item)
	return item, data, err
}
func attachmentLinkedSQL() string {
	return `EXISTS(SELECT 1 FROM messages m, json_each(m.attachments_json) j WHERE m.conversation_id=conversation_attachments.conversation_id AND json_extract(j.value,'$.id')=conversation_attachments.id)`
}
func (a *App) handleAttachments(w http.ResponseWriter, r *http.Request) {
	chat := r.URL.Query().Get("chat_id")
	conv, err := a.authorizeConversation(r, chat)
	if err != nil {
		http.Error(w, "conversation not found", 404)
		return
	}
	id := r.URL.Query().Get("id")
	switch r.Method {
	case "GET":
		item, data, err := a.loadAttachment(chat, id)
		if err != nil {
			http.Error(w, "attachment not found", 404)
			return
		}
		var owner int64
		var linked bool
		err = a.store.db.QueryRow(`SELECT user_id,`+attachmentLinkedSQL()+` FROM conversation_attachments WHERE id=?`, id).Scan(&owner, &linked)
		if err != nil || (!linked && owner != requestUser(r)) {
			http.Error(w, "attachment not found", 404)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, map[string]any{"attachment": item, "content_base64": base64.StdEncoding.EncodeToString(data)})
	case "POST":
		if !a.attachmentMu.TryLock() {
			http.Error(w, "another upload is in progress; retry", 429)
			return
		}
		defer a.attachmentMu.Unlock()
		var archived bool
		a.store.db.QueryRow(`SELECT archived_at IS NOT NULL FROM conversations WHERE id=?`, conv.ID).Scan(&archived)
		if archived {
			http.Error(w, "conversation archived", 409)
			return
		}
		var in struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Content string `json:"content_base64"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 15<<20)).Decode(&in) != nil {
			http.Error(w, "invalid or oversized upload", 400)
			return
		}
		if len(in.ID) < 16 || len(in.ID) > 80 || strings.ContainsAny(in.ID, "/\\\x00") {
			http.Error(w, "invalid upload id", 400)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(in.Content)
		if err != nil || len(raw) == 0 || len(raw) > maxUploadBytes {
			http.Error(w, "file must be between 1 byte and 10 MiB", 400)
			return
		}
		name := strings.TrimSpace(filepath.Base(strings.ReplaceAll(in.Name, "\\", "/")))
		if name == "" || name == "." || len(name) > 255 || strings.ContainsAny(name, "\r\n\x00") {
			http.Error(w, "invalid filename", 400)
			return
		}
		hash := fmt.Sprintf("%x", sha256.Sum256(raw))
		id = "att-" + fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", chat, requestUser(r), in.ID))))
		if old, _, err := a.loadAttachment(chat, id); err == nil {
			var saved string
			a.store.db.QueryRow(`SELECT sha256 FROM conversation_attachments WHERE id=?`, id).Scan(&saved)
			if saved != hash || old.Name != name {
				http.Error(w, "upload id already used", 409)
				return
			}
			writeJSON(w, old)
			return
		}
		// Reclaim abandoned local drafts. Attached originals are retained with messages.
		_, _ = a.store.db.Exec(`DELETE FROM conversation_attachments WHERE created_at<datetime('now','-1 day') AND NOT ` + attachmentLinkedSQL() + ` AND json_extract(metadata,'$.file_id') IS NULL`)
		var total int64
		a.store.db.QueryRow(`SELECT coalesce(sum(length(content)),0) FROM conversation_attachments WHERE conversation_id=? AND NOT `+attachmentLinkedSQL(), chat).Scan(&total)
		if total+int64(len(raw)) > 100<<20 {
			http.Error(w, "too many pending attachments", 413)
			return
		}
		item := Attachment{ID: id, Type: "file", Name: name, MimeType: http.DetectContentType(raw), Size: int64(len(raw))}
		if strings.HasPrefix(item.MimeType, "image/") {
			item.DataURL, err = imagePreview(raw)
			if err != nil {
				http.Error(w, "image must be a valid PNG, JPEG, WebP or GIF up to 40 megapixels", 400)
				return
			}
			item.Type = "image"
		}
		meta, _ := json.Marshal(item)
		_, err = a.store.db.Exec(`INSERT INTO conversation_attachments(id,conversation_id,user_id,metadata,content,sha256) VALUES(?,?,?,?,?,?)`, id, chat, requestUser(r), string(meta), raw, hash)
		if err != nil {
			http.Error(w, "upload unavailable; retry with the same id", 500)
			return
		}
		writeJSON(w, item)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", 405)
	}
}

// Resolve references from this conversation, never client-supplied URLs or
// storage IDs. Existing inline images stay backward compatible.
func (a *App) resolveAttachments(chat string, user int64, items []Attachment) ([]Attachment, error) {
	out := make([]Attachment, 0, len(items))
	for _, item := range items {
		if item.ID != "" {
			saved, _, err := a.loadAttachment(chat, item.ID)
			if err != nil {
				return nil, errors.New("attachment not found")
			}
			var owner int64
			var linked bool
			err = a.store.db.QueryRow(`SELECT user_id,`+attachmentLinkedSQL()+` FROM conversation_attachments WHERE id=?`, item.ID).Scan(&owner, &linked)
			if err != nil || owner != user {
				return nil, errors.New("attachment does not belong to sender")
			}
			item = saved
		} else if item.Type != "image" || !strings.HasPrefix(item.DataURL, "data:image/") {
			return nil, errors.New("upload the attachment first")
		}
		out = append(out, item)
	}
	return out, nil
}

func (a *App) mirrorAttachments(app *sdk.AppCtx, conv *Conversation, msg *Message) error {
	if app == nil || app.Config().Get("attachment_storage") != "true" {
		return nil
	}
	for i, item := range msg.Attachments {
		if item.ID == "" {
			continue
		}
		saved, raw, err := a.loadAttachment(conv.ID, item.ID)
		if err != nil {
			return err
		}
		if saved.FileID == 0 {
			var result struct {
				ID int64 `json:"id"`
			}
			err = app.PlatformAPI().CallAppResult("storage", "files_upload", map[string]any{"project_id": conv.ProjectID, "name": saved.Name, "folder": "/conversations/" + conv.ID + "/" + saved.ID, "content_base64": base64.StdEncoding.EncodeToString(raw), "content_type": saved.MimeType, "visibility": "private", "source": "conversations"}, &result)
			if err != nil {
				return fmt.Errorf("attachment storage unavailable: %w", err)
			}
			if result.ID <= 0 {
				return errors.New("storage returned no file id")
			}
			saved.FileID = result.ID
			saved.StorageApp = "storage"
			meta, _ := json.Marshal(saved)
			if _, err = a.store.db.Exec(`UPDATE conversation_attachments SET metadata=? WHERE id=?`, string(meta), saved.ID); err != nil {
				return err
			}
		}
		msg.Attachments[i] = saved
	}
	return nil
}

func (a *App) toolReadAttachment(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := a.requireParticipant(from, stringArg(args, "conversation_id"))
	if err != nil {
		return nil, err
	}
	id := stringArg(args, "attachment_id")
	var linked bool
	if a.store.db.QueryRow(`SELECT `+attachmentLinkedSQL()+` FROM conversation_attachments WHERE conversation_id=? AND id=?`, conv.ID, id).Scan(&linked) != nil || !linked {
		return nil, errors.New("attachment not found")
	}
	item, raw, err := a.loadAttachment(conv.ID, id)
	if err != nil {
		return nil, err
	}
	if item.Type == "image" {
		return map[string]any{"attachment": item}, nil
	}
	if utf8.Valid(raw) && !bytes.ContainsRune(raw, 0) {
		n := min(len(raw), 64000)
		for n > 0 && !utf8.Valid(raw[:n]) {
			n--
		}
		return map[string]any{"attachment": item, "text": string(raw[:n]), "truncated": n < len(raw)}, nil
	}
	offset := intArg(args, "offset", 0)
	if offset < 0 || offset > len(raw) {
		return nil, errors.New("offset outside file")
	}
	end := min(offset+48*1024, len(raw))
	return map[string]any{"attachment": item, "content_base64": base64.StdEncoding.EncodeToString(raw[offset:end]), "offset": offset, "next_offset": end, "has_more": end < len(raw), "note": "Binary file bytes; content has not been extracted. Read further chunks with offset=next_offset, or use the Storage file_id with document/media tools."}, nil
}
