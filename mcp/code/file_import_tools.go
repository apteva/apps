package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) fileImportTools() []sdk.Tool {
	return []sdk.Tool{{
		Name: "code_import_file",
		Description: "Import one text or binary file into an existing Code repository, preserving exact bytes. " +
			"Pass file as a blobref:// handle through Core (Core injects the binary envelope), a binary envelope, or base64. " +
			"Requires slug and destination path; never uses the uploaded filename as a path. New files are create-only by default. " +
			"To overwrite, supply the destination's current expected_sha256; create_only=true always forbids overwrite. " +
			"Returns file metadata including size and SHA-256. Imported bytes are saved to the working tree; no Git commit or native checkpoint is required. " +
			"Example: {\"slug\":\"demo\",\"path\":\"assets/logo.png\",\"file\":\"blobref://<uploaded-file>\",\"create_only\":true}.",
		InputSchema: schemaObject(map[string]any{
			"slug":            map[string]any{"type": "string"},
			"path":            map[string]any{"type": "string", "description": "Destination path inside the repository, e.g. assets/logo.png."},
			"file":            map[string]any{"type": "string", "description": "blobref:// handle rehydrated by Core, base64 bytes, or JSON binary envelope. Pass a handle directly; do not read or inline its bytes yourself."},
			"create_only":     map[string]any{"type": "boolean", "description": "Always reject an existing destination when true. Without expected_sha256, the import is create-only even if this option is omitted."},
			"expected_sha256": map[string]any{"type": "string", "description": "Current destination SHA-256, required for overwrite. Stale or missing destinations fail without changing files."},
		}, []string{"slug", "path", "file"}),
		Handler: a.toolImportFile,
	}}
}

// Core rehydrates scalar blobrefs into JSON envelope strings. Accept the
// decoded object too for direct MCP callers. Do not fetch URLs or resolve blobs
// here: only Core has the session authority to do that.
func decodeImportFile(arg any) ([]byte, error) {
	var encoded string
	var declaredSize *int64
	var raw []byte
	switch value := arg.(type) {
	case string:
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "blobref://") {
			return nil, errors.New("unresolved blobref:// handle; pass file through Core so the binary payload is rehydrated")
		}
		if strings.HasPrefix(value, "{") {
			raw = []byte(value)
		} else {
			encoded = value
		}
	case map[string]any:
		var err error
		raw, err = json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode file envelope: %w", err)
		}
	default:
		return nil, errors.New("file must be base64 bytes or a rehydrated binary envelope")
	}
	if raw != nil {
		var envelope struct {
			Binary bool    `json:"_binary"`
			Base64 *string `json:"base64"`
			Size   *int64  `json:"size"`
			Ref    string  `json:"_file_ref"`
			File   bool    `json:"_file"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("invalid file envelope: %w", err)
		}
		if envelope.Ref != "" || envelope.File {
			return nil, errors.New("unresolved file handle; pass its blobref:// reference through Core so the binary payload is rehydrated")
		}
		if !envelope.Binary || envelope.Base64 == nil {
			return nil, errors.New("file envelope requires _binary=true and base64")
		}
		encoded, declaredSize = *envelope.Base64, envelope.Size
	}
	limit := min(maxFileBytes(), currentImportLimits().FileBytes)
	if declaredSize != nil && (*declaredSize < 0 || *declaredSize > limit) {
		return nil, fmt.Errorf("file envelope size must be between 0 and %d bytes", limit)
	}
	// Bound the allocation before decoding, allowing only padding overhead.
	if int64(len(encoded)) > ((limit+2)/3)*4 || int64(base64.StdEncoding.DecodedLen(len(encoded))) > limit+2 {
		return nil, fmt.Errorf("file exceeds size limit of %d bytes", limit)
	}
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid file base64: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("file exceeds size limit of %d bytes", limit)
	}
	if declaredSize != nil && *declaredSize != int64(len(body)) {
		return nil, fmt.Errorf("file envelope size mismatch: declared %d bytes, decoded %d", *declaredSize, len(body))
	}
	return body, nil
}

func (a *App) toolImportFile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	rel, err := normalisePath(strArg(args, "path"))
	if err != nil {
		return nil, err
	}
	if value, ok := args["create_only"]; ok {
		if _, valid := value.(bool); !valid {
			return nil, errors.New("create_only must be a boolean")
		}
	}
	expected := strArg(args, "expected_sha256")
	if value, ok := args["expected_sha256"]; ok {
		if _, valid := value.(string); !valid {
			return nil, errors.New("expected_sha256 must be a string")
		}
	}
	if value, ok := args["create_only"]; ok && value == false && expected == "" {
		return nil, errors.New("overwriting requires expected_sha256; omit create_only or set it true to create a new file")
	}
	body, err := decodeImportFile(args["file"])
	if err != nil {
		return nil, err
	}
	slug := repo.Slug
	meta, err := writeConditional(a.storeFor(repo), slug, rel, body, expected, expected == "" || boolArg(args, "create_only"))
	if err != nil {
		return nil, err
	}
	emitFileChange(ctx, "file.changed", slug, rel)
	return map[string]any{"file": meta}, nil
}
