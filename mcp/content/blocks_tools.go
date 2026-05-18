// MCP tool handlers for block_* operations. Each tool loads the post,
// applies one structured mutation to its block tree, persists the
// updated body (creating a revision via dbUpdatePost), and returns the
// new tree. Multi-site (v2.0): every handler resolves the site upfront.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolBlocksGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["post_id"])
	if !ok || id == 0 {
		return nil, errors.New("post_id required")
	}
	post, err := dbGetPost(ctx.AppDB(), pid, siteID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"blocks": post.BodyBlocks}, nil
}

func (a *App) toolBlocksInsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["post_id"])
	if !ok || id == 0 {
		return nil, errors.New("post_id required")
	}
	post, err := dbGetPost(ctx.AppDB(), pid, siteID, id)
	if err != nil {
		return nil, err
	}
	nb, err := parseBlockFromArgs(args)
	if err != nil {
		return nil, err
	}
	pos, err := parseInsertPosition(args)
	if err != nil {
		return nil, err
	}
	updated, newID, err := insertBlock(post.BodyBlocks.Blocks, nb, pos)
	if err != nil {
		return nil, err
	}
	doc := Document{Version: documentVersion, Blocks: updated}
	if _, err := dbUpdatePost(ctx.AppDB(), pid, siteID, id, PostPatch{Blocks: &doc}, "", asStringDefault(args["source"], "agent"), fmt.Sprintf("inserted block %s", newID)); err != nil {
		return nil, err
	}
	return map[string]any{"block_id": newID, "blocks": doc}, nil
}

func (a *App) toolBlocksUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["post_id"])
	if !ok || id == 0 {
		return nil, errors.New("post_id required")
	}
	blockID := asString(args["block_id"])
	if blockID == "" {
		return nil, errors.New("block_id required")
	}
	post, err := dbGetPost(ctx.AppDB(), pid, siteID, id)
	if err != nil {
		return nil, err
	}
	var attrs map[string]any
	if v, ok := args["attrs"].(map[string]any); ok {
		attrs = v
	}
	var innerPtr *[]Block
	if raw, ok := args["inner"]; ok && raw != nil {
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var inner []Block
		if err := json.Unmarshal(b, &inner); err != nil {
			return nil, fmt.Errorf("inner: %w", err)
		}
		innerPtr = &inner
	}
	if attrs == nil && innerPtr == nil {
		return nil, errors.New("attrs or inner required")
	}
	if err := updateBlock(post.BodyBlocks.Blocks, blockID, attrs, innerPtr); err != nil {
		return nil, err
	}
	doc := Document{Version: documentVersion, Blocks: post.BodyBlocks.Blocks}
	if _, err := dbUpdatePost(ctx.AppDB(), pid, siteID, id, PostPatch{Blocks: &doc}, "", asStringDefault(args["source"], "agent"), fmt.Sprintf("updated block %s", blockID)); err != nil {
		return nil, err
	}
	return map[string]any{"block_id": blockID, "blocks": doc}, nil
}

func (a *App) toolBlocksMove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["post_id"])
	if !ok || id == 0 {
		return nil, errors.New("post_id required")
	}
	blockID := asString(args["block_id"])
	if blockID == "" {
		return nil, errors.New("block_id required")
	}
	pos, err := parseInsertPosition(args)
	if err != nil {
		return nil, err
	}
	post, err := dbGetPost(ctx.AppDB(), pid, siteID, id)
	if err != nil {
		return nil, err
	}
	updated, err := moveBlock(post.BodyBlocks.Blocks, blockID, pos)
	if err != nil {
		return nil, err
	}
	doc := Document{Version: documentVersion, Blocks: updated}
	if _, err := dbUpdatePost(ctx.AppDB(), pid, siteID, id, PostPatch{Blocks: &doc}, "", asStringDefault(args["source"], "agent"), fmt.Sprintf("moved block %s", blockID)); err != nil {
		return nil, err
	}
	return map[string]any{"block_id": blockID, "blocks": doc}, nil
}

func (a *App) toolBlocksDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["post_id"])
	if !ok || id == 0 {
		return nil, errors.New("post_id required")
	}
	blockID := asString(args["block_id"])
	if blockID == "" {
		return nil, errors.New("block_id required")
	}
	post, err := dbGetPost(ctx.AppDB(), pid, siteID, id)
	if err != nil {
		return nil, err
	}
	updated, err := deleteBlock(post.BodyBlocks.Blocks, blockID)
	if err != nil {
		return nil, err
	}
	doc := Document{Version: documentVersion, Blocks: updated}
	if _, err := dbUpdatePost(ctx.AppDB(), pid, siteID, id, PostPatch{Blocks: &doc}, "", asStringDefault(args["source"], "agent"), fmt.Sprintf("deleted block %s", blockID)); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "blocks": doc}, nil
}

func (a *App) toolBlocksReplaceAll(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["post_id"])
	if !ok || id == 0 {
		return nil, errors.New("post_id required")
	}
	raw, ok := args["blocks"]
	if !ok || raw == nil {
		return nil, errors.New("blocks required")
	}
	doc, err := coerceBlocksArg(raw)
	if err != nil {
		return nil, err
	}
	if _, err := dbUpdatePost(ctx.AppDB(), pid, siteID, id, PostPatch{Blocks: &doc}, "", asStringDefault(args["source"], "agent"), "replaced all blocks"); err != nil {
		return nil, err
	}
	return map[string]any{"blocks": doc}, nil
}

func (a *App) toolBlocksDuplicate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["post_id"])
	if !ok || id == 0 {
		return nil, errors.New("post_id required")
	}
	blockID := asString(args["block_id"])
	if blockID == "" {
		return nil, errors.New("block_id required")
	}
	post, err := dbGetPost(ctx.AppDB(), pid, siteID, id)
	if err != nil {
		return nil, err
	}
	updated, newID, err := duplicateBlock(post.BodyBlocks.Blocks, blockID)
	if err != nil {
		return nil, err
	}
	doc := Document{Version: documentVersion, Blocks: updated}
	if _, err := dbUpdatePost(ctx.AppDB(), pid, siteID, id, PostPatch{Blocks: &doc}, "", asStringDefault(args["source"], "agent"), fmt.Sprintf("duplicated block %s → %s", blockID, newID)); err != nil {
		return nil, err
	}
	return map[string]any{"block_id": newID, "blocks": doc}, nil
}

func (a *App) toolBlocksRegistry(_ *sdk.AppCtx, args map[string]any) (any, error) {
	cat := asString(args["category"])
	return map[string]any{"types": listBlockTypes(cat)}, nil
}

// handleHTTPBlockTypes — GET /admin/block-types. Same registry as the
// blocks_registry MCP tool, served as plain REST so the dashboard
// editor's insertion menu doesn't have to go through /tools/call.
func (a *App) handleHTTPBlockTypes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	cat := r.URL.Query().Get("category")
	httpJSON(w, map[string]any{"types": listBlockTypes(cat)})
}
