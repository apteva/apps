package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Attachment accepts both Conversations' historical wire shape and the
// platform FileHandle shape. Handles are normalized to the former for the
// transcript/UI while retaining the opaque ref for authorized presentation.
func (a *Attachment) UnmarshalJSON(raw []byte) error {
	type plain Attachment
	var wire struct {
		plain
		Filename string `json:"filename"`
		MIMEType string `json:"mimeType"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	*a = Attachment(wire.plain)
	if a.Name == "" {
		a.Name = wire.Filename
	}
	if a.MimeType == "" {
		a.MimeType = wire.MIMEType
	}
	if a.Ref != "" {
		a.File = true
		a.Ref = sdk.CanonicalFileReference(a.Ref)
		if a.Type == "" {
			if strings.HasPrefix(strings.ToLower(a.MimeType), "image/") {
				a.Type = "image"
			} else {
				a.Type = "file"
			}
		}
	}
	return nil
}

func normalizeReferenceAttachment(item *Attachment) error {
	if item == nil || item.Ref == "" {
		return nil
	}
	if !sdk.IsFileReference(item.Ref) {
		return errors.New("invalid file reference")
	}
	item.File = true
	item.Ref = sdk.CanonicalFileReference(item.Ref)
	if item.Name == "" || item.MimeType == "" || item.Size < 0 {
		return errors.New("file reference metadata is incomplete")
	}
	if item.Type == "" {
		if strings.HasPrefix(strings.ToLower(item.MimeType), "image/") {
			item.Type = "image"
		} else {
			item.Type = "file"
		}
	}
	return nil
}

func (a Attachment) isReference() bool { return a.Ref != "" }

func (a Attachment) validateReferenceMetadata() error {
	if !a.isReference() {
		return nil
	}
	if err := normalizeReferenceAttachment(&a); err != nil {
		return err
	}
	if a.Size > sdk.MaxFileReferenceBytes {
		return fmt.Errorf("file reference exceeds %d bytes", sdk.MaxFileReferenceBytes)
	}
	return nil
}

func (a *App) authorizeAgentReferenceAttachments(ctx context.Context, app *sdk.AppCtx, conv *Conversation, items []Attachment) error {
	var refs []int
	for i := range items {
		if items[i].Ref != "" {
			refs = append(refs, i)
		}
	}
	if len(refs) == 0 {
		return nil
	}
	files, err := sharedFileReferences(app, conv.ProjectID)
	if err != nil {
		return err
	}
	for _, index := range refs {
		item := &items[index]
		ref, err := files.GetFileReference(ctx, item.Ref)
		if err != nil || ref == nil || ref.Revoked || ref.ProjectID != conv.ProjectID || !sdk.IsFileReference(ref.Ref) {
			if err != nil {
				return fmt.Errorf("attachment reference unavailable: %w", err)
			}
			return errors.New("attachment reference unavailable")
		}
		item.Ref = sdk.CanonicalFileReference(ref.Ref)
		item.File = true
		item.Name = ref.Filename
		item.MimeType = ref.MIMEType
		item.Size = ref.Size
		if strings.HasPrefix(strings.ToLower(ref.MIMEType), "image/") {
			item.Type = "image"
		} else {
			item.Type = "file"
		}
	}
	return nil
}
