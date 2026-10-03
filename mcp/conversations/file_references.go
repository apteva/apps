package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var _ sdk.FileReferenceSource = (*App)(nil)

func hasTransferableAttachments(msg *Message) bool {
	for _, item := range msg.Attachments {
		if item.ID != "" && item.Type != "image" {
			return true
		}
	}
	return false
}

// Registration requires a canonical MIME type without charset parameters.
func attachmentMIME(value string) string {
	media, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "application/octet-stream"
	}
	return media
}

func sharedFileReferences(app *sdk.AppCtx, project string) (sdk.FileReferencesClient, error) {
	scoped := app.WithProject(project)
	info, err := scoped.PlatformInfo()
	if err != nil {
		return nil, fmt.Errorf("check attachment platform support: %w", err)
	}
	if info == nil || info.FileReferences != sdk.FileReferencesVersion || !info.FileReferenceDispatch["app_mcp"] {
		return nil, errors.New("file attachment delivery requires a server/Core update with shared file-reference support")
	}
	client, ok := scoped.PlatformAPI().(sdk.FileReferencesClient)
	if !ok {
		return nil, errors.New("platform client does not support file references")
	}
	return client, nil
}

// Registrations use the upload's immutable checksum as their version. Repeated
// deliveries (including after a restart) obtain the same handle and event hash.
// Only the server stores grants; no handle is accepted from a browser payload.
func (a *App) registerAgentFileReferences(ctx context.Context, files sdk.FileReferencesClient, conv *Conversation, msg *Message, agent int64, thread string) (map[string]sdk.FileHandle, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	refs := make(map[string]sdk.FileHandle)
	for _, item := range msg.Attachments {
		if item.ID == "" || item.Type == "image" {
			continue
		}
		var metadata, digest string
		var size int64
		err := a.store.db.QueryRowContext(ctx, `SELECT metadata,sha256,length(content) FROM conversation_attachments
			WHERE id=? AND conversation_id=? AND `+attachmentLinkedSQL(), item.ID, conv.ID).Scan(&metadata, &digest, &size)
		if err != nil {
			return nil, fmt.Errorf("attachment not available for delivery: %w", err)
		}
		var saved Attachment
		if err = json.Unmarshal([]byte(metadata), &saved); err != nil {
			return nil, err
		}
		ref, err := files.RegisterFileReference(ctx, sdk.RegisterFileReferenceRequest{
			ProjectID: conv.ProjectID, AttachmentID: item.ID, Version: digest,
			Filename: saved.Name, MIMEType: attachmentMIME(saved.MimeType), Size: size, SHA256: digest,
		})
		if err != nil {
			return nil, fmt.Errorf("register attachment: %w", err)
		}
		if ref == nil || !sdk.IsFileReference(ref.Ref) || ref.Revoked || ref.ProjectID != conv.ProjectID || ref.AttachmentID != item.ID || ref.Version != digest || ref.SHA256 != digest || ref.Size != size || ref.Filename != saved.Name || ref.MIMEType != attachmentMIME(saved.MimeType) {
			return nil, errors.New("platform returned an invalid attachment reference")
		}
		if err = files.GrantFileReference(ctx, sdk.FileReferenceGrant{Ref: ref.Ref, AgentID: agent, ThreadID: thread}); err != nil {
			return nil, fmt.Errorf("grant attachment: %w", err)
		}
		refs[item.ID] = ref.Handle()
	}
	return refs, nil
}

// The SDK mounts this reader behind installation auth and a platform signature.
// Recheck the local conversation binding and participant at read time so removal
// from a conversation immediately invalidates access, even with a stored grant.
func (a *App) OpenFileReference(ctx context.Context, req sdk.FileReadRequest) (io.ReadCloser, error) {
	var chat, metadata, digest string
	var raw []byte
	err := a.store.db.QueryRowContext(ctx, `SELECT conversation_attachments.conversation_id,metadata,content,sha256
		FROM conversation_attachments JOIN conversations c ON c.id=conversation_attachments.conversation_id
		WHERE conversation_attachments.id=? AND c.project_id=? AND `+attachmentLinkedSQL(), req.AttachmentID, req.ProjectID).Scan(&chat, &metadata, &raw, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &sdk.FileReferenceError{Code: "file_deleted", Message: "attachment no longer available"}
	}
	if err != nil {
		return nil, err
	}
	state, err := a.store.AgentThread(chat, req.AgentID)
	if err != nil || state == nil || state.ThreadID != req.ThreadID {
		return nil, &sdk.FileReferenceError{Code: "file_inaccessible", Message: "attachment belongs to another conversation thread"}
	}
	member, err := a.store.IsParticipantAgent(chat, req.AgentID)
	if err != nil || !member {
		return nil, &sdk.FileReferenceError{Code: "file_inaccessible", Message: "agent is no longer a participant"}
	}
	var item Attachment
	if err = json.Unmarshal([]byte(metadata), &item); err != nil {
		return nil, err
	}
	if item.Type == "image" || req.Version != digest || req.SHA256 != digest || req.Size != int64(len(raw)) || req.Filename != item.Name || req.MIMEType != attachmentMIME(item.MimeType) || fmt.Sprintf("%x", sha256.Sum256(raw)) != digest {
		return nil, &sdk.FileReferenceError{Code: "file_inaccessible", Message: "attachment does not match its immutable registration"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}
