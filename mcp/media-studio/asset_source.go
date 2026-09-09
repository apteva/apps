package main

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
)

// Export through Media Studio's own Storage binding: bare Storage IDs are not
// portable across installations. Consumers retain the bytes and this receipt.
func (a *App) toolMediaAssetSource(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = withProjectScope(ctx, args)
	id := int64Arg(args, "id", 0)
	if id <= 0 {
		return nil, errors.New("generation id required")
	}
	row, e := queryGenerationByID(ctx, projectScope(ctx), id)
	if e != nil {
		return nil, e
	}
	if row == nil {
		return nil, errors.New("generation not found")
	}
	if row["status"] != "ready" {
		return nil, errors.New("generation is not ready")
	}
	ids, _ := row["storage_ids"].([]int64)
	index := intArg(args, "index", 0)
	if index < 0 || index >= len(ids) {
		return nil, errors.New("generation has no stored output at this index; bind Storage before generating")
	}
	var file map[string]any
	if e = ctx.PlatformAPI().CallAppResult("storage", "files_get_content", storageArgs(ctx, map[string]any{"id": ids[index]}), &file); e != nil {
		return nil, e
	}
	encoded, _ := file["content_base64"].(string)
	if len(encoded) > base64.StdEncoding.EncodedLen(25<<20) {
		return nil, errors.New("source exceeds 25 MiB")
	}
	b, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil || len(b) == 0 || int64Any(file["id"]) != ids[index] || int64Any(file["size_bytes"]) != int64(len(b)) {
		return nil, errors.New("invalid stored source")
	}
	return map[string]any{"generation_id": id, "kind": row["kind"], "provider": row["provider"], "model": row["model"], "request_json": row["request_json"], "cost_usd": row["cost_usd"], "storage_id": ids[index], "sha256": fmt.Sprintf("%x", sha256.Sum256(b)), "size_bytes": len(b), "content_type": file["content_type"], "content_base64": encoded}, nil
}
