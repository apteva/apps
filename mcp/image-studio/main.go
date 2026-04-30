// Image Studio v0.1 — generate images via any compatible provider.
//
// Architecture:
//   - manifest declares two integration deps: provider (required, kind=integration,
//     compatible_slugs=[openai-api]) and storage (optional, kind=app).
//   - operator binds at install time; image-studio reads via ctx.IntegrationFor.
//   - image_generate tool calls the bound provider via PlatformAPI's
//     ExecuteIntegrationTool — credentials never enter this process.
//   - bytes are downloaded from the upstream URL while it's still fresh,
//     handed off to storage via CallApp("storage", "files_from_url", ...) when
//     bound, or kept in a 24h app-local cache otherwise.
//   - response is MCP content blocks: image (thumbnail base64, ~30KB), text
//     (summary), resource (apteva://storage/file/<id> when bound).
//
// History lives in the app's own DB so the panel can render a gallery
// across restarts and sessions.
package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: image-studio
display_name: Image Studio
version: 0.1.0
description: |
  Generate images via any compatible provider. Optionally saves outputs
  to the Storage app for permanent references.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
    - platform.apps.call
  integrations:
    - role: provider
      kind: integration
      compatible_slugs: [openai-api]
      capabilities: [image.generate]
      tools:
        image.generate: generate_image
      required: true
      label: "Image-generation provider"
    - role: storage
      kind: app
      compatible_app_names: [storage]
      capabilities: [files.write]
      required: false
      label: "Storage (optional)"
      hint: "Save generated images permanently. Without this, results are returned inline only."
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: image_generate, description: "Generate an image. Args: prompt, size?, quality?, n?." }
    - { name: image_history,  description: "List recent generations. Args: limit?, since?." }
  ui_panels:
    - slot: project.page
      label: Studio
      icon: image
      entry: /ui/StudioPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/image-studio
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/image-studio.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("image-studio requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("image-studio mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error         { return nil }
func (a *App) Channels() []sdk.ChannelFactory      { return nil }
func (a *App) Workers() []sdk.Worker               { return nil }
func (a *App) EventHandlers() []sdk.EventHandler   { return nil }

// ─── HTTP routes (panel data) ──────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/generations", Handler: a.handleListGenerations},
	}
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "image_generate",
			Description: "Generate an image from a prompt using the bound provider. Args: prompt (required), size?, quality?, n?. " +
				"Returns MCP content blocks: image (thumbnail base64), text (summary with storage IDs when storage is bound), resource (apteva://storage/file/<id>). " +
				"The prompt may be revised by the provider (e.g. DALL·E 3) — the revised version is included in the response.",
			InputSchema: schemaObject(map[string]any{
				"prompt":  map[string]any{"type": "string"},
				"size":    map[string]any{"type": "string", "default": "1024x1024"},
				"quality": map[string]any{"type": "string", "default": "standard"},
				"n":       map[string]any{"type": "integer", "default": 1},
			}, []string{"prompt"}),
			Handler: a.toolImageGenerate,
		},
		{
			Name:        "image_history",
			Description: "List recent generations for this project. Args: limit (default 50), since (ISO8601 timestamp).",
			InputSchema: schemaObject(map[string]any{
				"limit": map[string]any{"type": "integer", "default": 50},
				"since": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolImageHistory,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── image_generate ────────────────────────────────────────────────

func (a *App) toolImageGenerate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("prompt required")
	}
	bound := ctx.IntegrationFor("provider")
	if bound == nil {
		// Surface as an MCP error rather than a Go error so the agent
		// sees a usable message instead of an opaque MCP -32000 wrapper.
		return mcpError("no provider bound — pick an image-generation integration in app settings"), nil
	}

	size := strArg(args, "size", "1024x1024")
	quality := strArg(args, "quality", "standard")
	n := intArg(args, "n", 1)

	// 1. Call the integration via the platform.
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(
		bound.ConnectionID,
		bound.ToolFor("image.generate"),
		map[string]any{
			"prompt":  prompt,
			"size":    size,
			"quality": quality,
			"n":       n,
		},
	)
	if err != nil {
		return mcpError("provider call failed: " + err.Error()), nil
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return mcpError("provider returned non-2xx: " + body), nil
	}

	// 2. Normalize across providers.
	images, revisedPrompt, model, err := normalizeImageResponse(bound.AppSlug, res.Data)
	if err != nil {
		return mcpError("provider response parse: " + err.Error()), nil
	}
	if len(images) == 0 {
		return mcpError("provider returned zero images"), nil
	}

	// 3. Persist via storage if bound; else thumbnail-only.
	storage := ctx.IntegrationFor("storage")
	pid := os.Getenv("APTEVA_PROJECT_ID")
	storageIDs := make([]int64, 0, len(images))
	upstreamURLs := make([]string, 0, len(images))
	var firstThumbB64 string
	for i, img := range images {
		upstreamURLs = append(upstreamURLs, img.UpstreamURL)

		// Fetch bytes from the upstream URL (1h TTL is plenty here).
		body, err := fetchBytes(img.UpstreamURL)
		if err != nil {
			ctx.Logger().Warn("fetch upstream image failed", "url", img.UpstreamURL, "err", err)
			continue
		}
		// Build a thumbnail for the response. Best-effort; if it fails
		// we ship without one and the agent renders the storage URL.
		thumb := makeThumbnail(body, 256)
		if i == 0 {
			firstThumbB64 = base64.StdEncoding.EncodeToString(thumb)
		}

		if storage != nil {
			// Hand off to storage via CallApp. files_from_url accepts
			// a URL string, not bytes, so we pass the upstream URL —
			// storage will fetch its own bytes (the URL is still
			// fresh at this point). When the upstream URL has a short
			// TTL (OpenAI: 1h) this is fine.
			res, err := ctx.PlatformAPI().CallApp("storage", "files_from_url", map[string]any{
				"url":    img.UpstreamURL,
				"folder": "/generated/",
				"name":   fmt.Sprintf("img-%d-%d.png", time.Now().Unix(), i),
				"tags":   []string{"ai", "generated", bound.AppSlug},
			})
			if err != nil {
				ctx.Logger().Warn("storage save failed", "err", err)
				continue
			}
			// Storage's MCP response is wrapped in {content:[{text:json}]}.
			// Extract the inner JSON.
			id := extractStorageID(res)
			if id != 0 {
				storageIDs = append(storageIDs, id)
			}
		}
	}

	// 4. Persist a row in image-studio's history.
	a.dbInsertGeneration(pid, prompt, revisedPrompt, bound.AppSlug, model, size, storageIDs, upstreamURLs, firstThumbB64, len(images))

	// 5. Emit live event so the panel refreshes.
	ctx.Emit("image.generated", map[string]any{
		"prompt": prompt, "model": model, "count": len(images),
	})

	// 6. Build MCP response.
	return buildMCPResult(prompt, revisedPrompt, model, bound.AppSlug, storageIDs, upstreamURLs, firstThumbB64, len(images)), nil
}

// ─── image_history ─────────────────────────────────────────────────

func (a *App) toolImageHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	limit := intArg(args, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id, prompt, revised_prompt, provider, model, size,
		        storage_ids, upstream_urls, thumbnail_b64, count, created_at
		 FROM generations
		 WHERE project_id = ?
		 ORDER BY id DESC LIMIT ?`,
		pid, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id, count                                    int64
			prompt, revised, provider, model, size       string
			storageIDsJSON, upstreamURLsJSON, thumbB64   string
			createdAt                                    string
		)
		if err := rows.Scan(&id, &prompt, &revised, &provider, &model, &size,
			&storageIDsJSON, &upstreamURLsJSON, &thumbB64, &count, &createdAt); err != nil {
			continue
		}
		var storageIDs []int64
		_ = json.Unmarshal([]byte(storageIDsJSON), &storageIDs)
		var upstreamURLs []string
		_ = json.Unmarshal([]byte(upstreamURLsJSON), &upstreamURLs)
		out = append(out, map[string]any{
			"id":             id,
			"prompt":         prompt,
			"revised_prompt": revised,
			"provider":       provider,
			"model":          model,
			"size":           size,
			"storage_ids":    storageIDs,
			"upstream_urls":  upstreamURLs,
			"thumbnail_b64":  thumbB64,
			"count":          count,
			"created_at":     createdAt,
		})
	}
	return map[string]any{"generations": out}, nil
}

// HTTP variant for the panel.
func (a *App) handleListGenerations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}
	out, err := a.toolImageHistoryFor(pid, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (a *App) toolImageHistoryFor(pid string, limit int) (map[string]any, error) {
	args := map[string]any{"limit": limit}
	res, err := a.toolImageHistory(globalCtx, argsWithProject(args, pid))
	if err != nil {
		return nil, err
	}
	return res.(map[string]any), nil
}

// ─── DB helpers ────────────────────────────────────────────────────

func (a *App) dbInsertGeneration(
	pid, prompt, revised, provider, model, size string,
	storageIDs []int64, upstreamURLs []string,
	thumbB64 string, count int,
) {
	if globalCtx == nil {
		return
	}
	sj, _ := json.Marshal(storageIDs)
	uj, _ := json.Marshal(upstreamURLs)
	_, err := globalCtx.AppDB().Exec(
		`INSERT INTO generations
			(project_id, prompt, revised_prompt, provider, model, size,
			 storage_ids, upstream_urls, thumbnail_b64, count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, prompt, revised, provider, model, size,
		string(sj), string(uj), thumbB64, count,
	)
	if err != nil {
		globalCtx.Logger().Warn("dbInsertGeneration failed", "err", err)
	}
}

// ─── Provider response normalization ───────────────────────────────

type generatedImage struct {
	UpstreamURL string
}

// normalizeImageResponse parses provider-specific shapes into a uniform
// list. Today only openai-api is supported (compatible_slugs in the
// manifest); extend as new providers land.
func normalizeImageResponse(slug string, raw json.RawMessage) ([]generatedImage, string, string, error) {
	switch slug {
	case "openai-api":
		// {data:[{url, revised_prompt}]}
		var body struct {
			Data []struct {
				URL           string `json:"url"`
				B64JSON       string `json:"b64_json"`
				RevisedPrompt string `json:"revised_prompt"`
			} `json:"data"`
			Created int64 `json:"created"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, "", "", err
		}
		images := make([]generatedImage, 0, len(body.Data))
		var revised string
		for i, d := range body.Data {
			images = append(images, generatedImage{UpstreamURL: d.URL})
			if i == 0 {
				revised = d.RevisedPrompt
			}
		}
		return images, revised, "dall-e-3", nil
	}
	return nil, "", "", fmt.Errorf("unsupported provider slug: %q", slug)
}

// ─── MCP response builders ────────────────────────────────────────

func buildMCPResult(prompt, revised, model, provider string, storageIDs []int64, upstreamURLs []string, thumbB64 string, count int) map[string]any {
	content := []map[string]any{}
	if thumbB64 != "" {
		content = append(content, map[string]any{
			"type":     "image",
			"data":     thumbB64,
			"mimeType": "image/jpeg",
		})
	}
	summary := fmt.Sprintf("Generated %d image(s) via %s (model=%s).\nPrompt: %q",
		count, provider, model, prompt)
	if revised != "" && revised != prompt {
		summary += "\nRevised: " + revised
	}
	if len(storageIDs) > 0 {
		ids := make([]string, len(storageIDs))
		for i, id := range storageIDs {
			ids[i] = strconv.FormatInt(id, 10)
		}
		summary += "\nSaved to storage: " + strings.Join(ids, ", ")
	}
	content = append(content, map[string]any{"type": "text", "text": summary})
	for _, id := range storageIDs {
		content = append(content, map[string]any{
			"type": "resource",
			"resource": map[string]any{
				"uri":      fmt.Sprintf("apteva://storage/file/%d", id),
				"mimeType": "image/png",
			},
		})
	}
	return map[string]any{
		"content": content,
		"_meta": map[string]any{
			"prompt":         prompt,
			"revised_prompt": revised,
			"model":          model,
			"provider":       provider,
			"storage_ids":    storageIDs,
			"upstream_urls":  upstreamURLs,
		},
	}
}

func mcpError(msg string) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{
			{"type": "text", "text": msg},
		},
	}
}

// ─── helpers ───────────────────────────────────────────────────────

func fetchBytes(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Apteva image-studio)")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 50<<20))
}

// makeThumbnail JPEG-compresses to ~30KB at the given max edge.
// Best-effort; on any decode failure returns nil so the caller skips
// the image content block.
func makeThumbnail(src []byte, maxEdge int) []byte {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return nil
	}
	// Naive nearest-neighbour scale via image.NewRGBA — keeps the
	// import surface tiny. For high quality, swap to golang.org/x/image/draw.
	scale := 1.0
	if w > maxEdge || h > maxEdge {
		if w >= h {
			scale = float64(maxEdge) / float64(w)
		} else {
			scale = float64(maxEdge) / float64(h)
		}
	}
	tw, th := int(float64(w)*scale), int(float64(h)*scale)
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	thumb := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			sx := int(float64(x) / scale)
			sy := int(float64(y) / scale)
			thumb.Set(x, y, img.At(sx, sy))
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 75}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// extractStorageID pulls the file id out of storage's MCP tools/call
// response shape. Storage's tool returns its result as {content:[{text:"<json>"}]}
// — we parse the inner JSON to find {id, url, sha256, ...}.
func extractStorageID(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	// Try direct shape first ({id, url, sha256}).
	var direct struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(raw, &direct) == nil && direct.ID > 0 {
		return direct.ID
	}
	// Fall back to MCP-wrapped: {result:{content:[{text:"<json>"}]}}.
	var wrapped struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &wrapped) == nil {
		for _, c := range wrapped.Result.Content {
			if c.Type == "text" && c.Text != "" {
				var inner struct {
					ID int64 `json:"id"`
				}
				if json.Unmarshal([]byte(c.Text), &inner) == nil && inner.ID > 0 {
					return inner.ID
				}
			}
		}
	}
	return 0
}

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strArg(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func intArg(m map[string]any, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return def
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	if v := os.Getenv("APTEVA_PROJECT_ID"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required")
}

func argsWithProject(args map[string]any, pid string) map[string]any {
	out := map[string]any{}
	for k, v := range args {
		out[k] = v
	}
	if pid != "" {
		out["project_id"] = pid
	}
	return out
}

// quiet "imported and not used" for stdlib pkgs only used in some paths.
var _ = sql.Drivers
