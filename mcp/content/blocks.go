// Block tree manipulation + core block registry.
//
// The canonical post body is a JSON tree of blocks. Each block has a
// stable id (so the agent can target it across revisions), a typed
// `type` ("core/heading", "image-studio/generated"), an `attrs` map
// validated against a JSON Schema declared by the block type, and an
// optional `inner` array for container blocks (columns, group, quote).
//
// All MCP block_* tools and the renderer operate on the tree through
// the walk helpers here; the SQL layer just stores the JSON.

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Document is the persisted body shape. Version exists so future
// breaking attr changes can be migrated without ambiguity.
type Document struct {
	Version int     `json:"version"`
	Blocks  []Block `json:"blocks"`
}

// Block is one node in the body tree.
type Block struct {
	ID    string         `json:"id"`
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
	Inner []Block        `json:"inner,omitempty"`
}

const documentVersion = 1

func emptyDocumentJSON() string { return `{"version":1,"blocks":[]}` }

// parseDocument decodes the body_blocks column. Tolerates legacy/empty
// values by returning an empty Document.
func parseDocument(s string) (Document, error) {
	if s == "" {
		return Document{Version: documentVersion}, nil
	}
	var d Document
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		return Document{}, fmt.Errorf("body_blocks: %w", err)
	}
	if d.Version == 0 {
		d.Version = documentVersion
	}
	return d, nil
}

// encodeDocument serializes back to the column shape; assigns ids to
// any block that's missing one before persisting (defensive — every
// mutator already assigns ids, but agents may submit blocks_replace_all
// with unhydrated trees).
func encodeDocument(d Document) (string, error) {
	if d.Version == 0 {
		d.Version = documentVersion
	}
	assignMissingIDs(d.Blocks)
	if err := validateDocument(d); err != nil {
		return "", err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// validateDocument walks the tree and checks: every block has a known
// type, attrs are objects (not arrays), no duplicate ids.
func validateDocument(d Document) error {
	seen := map[string]bool{}
	var walk func([]Block) error
	walk = func(bs []Block) error {
		for i := range bs {
			b := &bs[i]
			if _, known := coreBlockTypes[b.Type]; !known && !looksNamespaced(b.Type) {
				return fmt.Errorf("unknown block type %q at id %q", b.Type, b.ID)
			}
			if b.ID == "" {
				return fmt.Errorf("block of type %q missing id", b.Type)
			}
			if seen[b.ID] {
				return fmt.Errorf("duplicate block id %q", b.ID)
			}
			seen[b.ID] = true
			if err := walk(b.Inner); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(d.Blocks)
}

// looksNamespaced returns true for cross-app block types like
// "image-studio/generated". v1 doesn't register these yet (the registry
// hook is reserved for v1.1), but we accept them in storage so a
// forward-rolling install doesn't lose data.
func looksNamespaced(t string) bool {
	for i := 0; i < len(t); i++ {
		if t[i] == '/' && i > 0 && i < len(t)-1 {
			return true
		}
	}
	return false
}

// newBlockID generates a stable id with a "b_" prefix + 8 hex chars.
// Short enough to read in JSON, plenty of entropy for a single post.
func newBlockID() string {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand failing is exceptional; fall back to a
		// time-based id so writes don't crash the sidecar.
		return fmt.Sprintf("b_fallback%d", randomFallback())
	}
	return "b_" + hex.EncodeToString(raw[:])
}

var fallbackCounter uint64

func randomFallback() uint64 {
	fallbackCounter++
	return fallbackCounter
}

// assignMissingIDs walks the tree and fills in any empty id field.
// Safe to call multiple times — existing ids are preserved.
func assignMissingIDs(bs []Block) {
	for i := range bs {
		if bs[i].ID == "" {
			bs[i].ID = newBlockID()
		}
		assignMissingIDs(bs[i].Inner)
	}
}

// findBlock returns a pointer to the block with the given id along
// with the parent slice and index, so callers can mutate or remove.
// Returns (nil, nil, -1) if not found.
func findBlock(bs []Block, id string) (*Block, []Block, int) {
	for i := range bs {
		if bs[i].ID == id {
			return &bs[i], bs, i
		}
		if p, parent, idx := findBlock(bs[i].Inner, id); p != nil {
			// Note: parent is a fresh slice (the inner of bs[i]); the
			// caller has to mutate via bs[i].Inner reassignment for
			// inserts/removes to stick. The mutate helpers below do.
			_ = parent
			_ = idx
			return p, bs[i].Inner, idx
		}
	}
	return nil, nil, -1
}

// insertBlock places `nb` at the requested position in the tree.
// Resolution order: position.After → position.Before → position.Index
// inside the root (or inside parent if `parentID` is set). Returns the
// updated root slice and the new block's id.
type insertPosition struct {
	After    string
	Before   string
	Index    int
	ParentID string
	UseIndex bool
}

func insertBlock(root []Block, nb Block, pos insertPosition) ([]Block, string, error) {
	if nb.ID == "" {
		nb.ID = newBlockID()
	}
	assignMissingIDs(nb.Inner)

	// After / Before reference any block in the tree.
	if pos.After != "" {
		if !insertRelative(&root, pos.After, nb, +1) {
			return root, "", fmt.Errorf("after id %q not found", pos.After)
		}
		return root, nb.ID, nil
	}
	if pos.Before != "" {
		if !insertRelative(&root, pos.Before, nb, 0) {
			return root, "", fmt.Errorf("before id %q not found", pos.Before)
		}
		return root, nb.ID, nil
	}

	// Index — root or inside parent.
	target := &root
	if pos.ParentID != "" {
		if !insertIntoParent(&root, pos.ParentID, nb, pos.Index, pos.UseIndex) {
			return root, "", fmt.Errorf("parent id %q not found", pos.ParentID)
		}
		return root, nb.ID, nil
	}
	if pos.UseIndex {
		idx := pos.Index
		if idx < 0 {
			idx = 0
		}
		if idx > len(*target) {
			idx = len(*target)
		}
		*target = append((*target)[:idx], append([]Block{nb}, (*target)[idx:]...)...)
	} else {
		*target = append(*target, nb)
	}
	return root, nb.ID, nil
}

func insertRelative(bs *[]Block, anchorID string, nb Block, offset int) bool {
	for i := range *bs {
		if (*bs)[i].ID == anchorID {
			at := i + offset
			if at < 0 {
				at = 0
			}
			if at > len(*bs) {
				at = len(*bs)
			}
			*bs = append((*bs)[:at], append([]Block{nb}, (*bs)[at:]...)...)
			return true
		}
		inner := (*bs)[i].Inner
		if insertRelative(&inner, anchorID, nb, offset) {
			(*bs)[i].Inner = inner
			return true
		}
	}
	return false
}

func insertIntoParent(bs *[]Block, parentID string, nb Block, idx int, useIndex bool) bool {
	for i := range *bs {
		if (*bs)[i].ID == parentID {
			inner := (*bs)[i].Inner
			if useIndex {
				if idx < 0 {
					idx = 0
				}
				if idx > len(inner) {
					idx = len(inner)
				}
				inner = append(inner[:idx], append([]Block{nb}, inner[idx:]...)...)
			} else {
				inner = append(inner, nb)
			}
			(*bs)[i].Inner = inner
			return true
		}
		inner := (*bs)[i].Inner
		if insertIntoParent(&inner, parentID, nb, idx, useIndex) {
			(*bs)[i].Inner = inner
			return true
		}
	}
	return false
}

// updateBlock mutates attrs/inner of one block by id.
func updateBlock(root []Block, id string, attrs map[string]any, inner *[]Block) error {
	for i := range root {
		if root[i].ID == id {
			if attrs != nil {
				root[i].Attrs = attrs
			}
			if inner != nil {
				assignMissingIDs(*inner)
				root[i].Inner = *inner
			}
			return nil
		}
		if err := updateBlock(root[i].Inner, id, attrs, inner); err == nil {
			return nil
		}
	}
	return errBlockNotFound(id)
}

// deleteBlock removes one block by id; returns the (possibly mutated)
// root.
func deleteBlock(root []Block, id string) ([]Block, error) {
	for i := range root {
		if root[i].ID == id {
			return append(root[:i], root[i+1:]...), nil
		}
		next, err := deleteBlock(root[i].Inner, id)
		if err == nil {
			root[i].Inner = next
			return root, nil
		}
	}
	return root, errBlockNotFound(id)
}

// moveBlock removes a block and reinserts it at the requested position.
func moveBlock(root []Block, id string, pos insertPosition) ([]Block, error) {
	target, _, _ := findBlock(root, id)
	if target == nil {
		return root, errBlockNotFound(id)
	}
	// Deep copy so the removal doesn't blank the reinsert.
	moved := *target
	next, err := deleteBlock(root, id)
	if err != nil {
		return root, err
	}
	moved.ID = id // preserve identity
	out, _, err := insertBlock(next, moved, pos)
	if err != nil {
		return next, err
	}
	return out, nil
}

// duplicateBlock copies a block (with a fresh id tree) and inserts it
// immediately after the original. Returns the new block's id.
func duplicateBlock(root []Block, id string) ([]Block, string, error) {
	src, _, _ := findBlock(root, id)
	if src == nil {
		return root, "", errBlockNotFound(id)
	}
	dup := deepCopyBlock(*src)
	dup.ID = newBlockID()
	stripIDsRecursive(dup.Inner)
	assignMissingIDs(dup.Inner)
	out, _, err := insertBlock(root, dup, insertPosition{After: id})
	if err != nil {
		return root, "", err
	}
	return out, dup.ID, nil
}

func deepCopyBlock(b Block) Block {
	cp := Block{ID: b.ID, Type: b.Type}
	if b.Attrs != nil {
		cp.Attrs = make(map[string]any, len(b.Attrs))
		for k, v := range b.Attrs {
			cp.Attrs[k] = v
		}
	}
	if len(b.Inner) > 0 {
		cp.Inner = make([]Block, len(b.Inner))
		for i := range b.Inner {
			cp.Inner[i] = deepCopyBlock(b.Inner[i])
		}
	}
	return cp
}

func stripIDsRecursive(bs []Block) {
	for i := range bs {
		bs[i].ID = ""
		stripIDsRecursive(bs[i].Inner)
	}
}

func errBlockNotFound(id string) error {
	return fmt.Errorf("block id %q not found", id)
}

// ── Core block registry ───────────────────────────────────────────
//
// One row per built-in type. AttrsSchema is informational in v1; we
// only soft-validate by reading well-known keys at render time. Strict
// JSON Schema validation lands in v1.1 alongside the editor.
//
// Category drives the editor's insertion menu grouping.

type BlockTypeInfo struct {
	Name        string         `json:"name"`
	DisplayName string         `json:"display_name"`
	Category    string         `json:"category"`
	Description string         `json:"description"`
	AttrsSchema map[string]any `json:"attrs_schema,omitempty"`
	Container   bool           `json:"container"` // accepts inner blocks
}

var coreBlockTypes = map[string]BlockTypeInfo{
	"core/heading": {
		Name: "core/heading", DisplayName: "Heading", Category: "text",
		Description: "H1–H6 heading.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"level": map[string]any{"type": "integer", "minimum": 1, "maximum": 6},
				"text":  map[string]any{"type": "string"},
			},
		},
	},
	"core/paragraph": {
		Name: "core/paragraph", DisplayName: "Paragraph", Category: "text",
		Description: "A paragraph; text_md accepts inline markdown.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text_md": map[string]any{"type": "string"},
				"align":   map[string]any{"type": "string", "enum": []string{"left", "center", "right"}},
			},
		},
	},
	"core/image": {
		Name: "core/image", DisplayName: "Image", Category: "media",
		Description: "Image from the media library.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"media_id": map[string]any{"type": "integer"},
				"alt":      map[string]any{"type": "string"},
				"caption":  map[string]any{"type": "string"},
				"size":     map[string]any{"type": "string", "enum": []string{"inline", "wide", "full"}},
				"link":     map[string]any{"type": "string"},
			},
		},
	},
	"core/gallery": {
		Name: "core/gallery", DisplayName: "Gallery", Category: "media",
		Description: "Grid of images.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"media_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"columns":   map[string]any{"type": "integer"},
			},
		},
	},
	"core/list": {
		Name: "core/list", DisplayName: "List", Category: "text",
		Description: "Bulleted or numbered list.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"style": map[string]any{"type": "string", "enum": []string{"bullet", "number"}},
				"items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
		},
	},
	"core/quote": {
		Name: "core/quote", DisplayName: "Quote", Category: "text", Container: true,
		Description: "Pull-quote; inner blocks form the quoted body.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"citation": map[string]any{"type": "string"},
			},
		},
	},
	"core/code": {
		Name: "core/code", DisplayName: "Code", Category: "text",
		Description: "Monospace code block with optional language hint.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"language": map[string]any{"type": "string"},
				"source":   map[string]any{"type": "string"},
			},
		},
	},
	"core/embed": {
		Name: "core/embed", DisplayName: "Embed", Category: "embed",
		Description: "Generic oEmbed by URL.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":           map[string]any{"type": "string"},
				"cached_html":   map[string]any{"type": "string"},
				"provider_name": map[string]any{"type": "string"},
			},
		},
	},
	"core/separator": {
		Name: "core/separator", DisplayName: "Separator", Category: "layout",
		Description: "Horizontal rule.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"style": map[string]any{"type": "string", "enum": []string{"plain", "wide", "dots"}},
			},
		},
	},
	"core/columns": {
		Name: "core/columns", DisplayName: "Columns", Category: "layout", Container: true,
		Description: "Side-by-side columns; each inner block becomes one column.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ratio": map[string]any{"type": "string"},
			},
		},
	},
	"core/group": {
		Name: "core/group", DisplayName: "Group", Category: "layout", Container: true,
		Description: "Logical grouping wrapper.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"background": map[string]any{"type": "string"},
				"padding":    map[string]any{"type": "string"},
			},
		},
	},
	"core/html": {
		Name: "core/html", DisplayName: "Raw HTML", Category: "advanced",
		Description: "Raw HTML, sanitized at render time.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source": map[string]any{"type": "string"},
			},
		},
	},
	"core/markdown": {
		Name: "core/markdown", DisplayName: "Markdown", Category: "text",
		Description: "Multi-paragraph markdown; the escape hatch for power users.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source": map[string]any{"type": "string"},
			},
		},
	},
	"core/table": {
		Name: "core/table", DisplayName: "Table", Category: "text",
		Description: "Rows + cells; header row optional.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"header": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"rows":   map[string]any{"type": "array"},
			},
		},
	},
	"core/button": {
		Name: "core/button", DisplayName: "Button", Category: "layout",
		Description: "Link styled as a button.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"label": map[string]any{"type": "string"},
				"url":   map[string]any{"type": "string"},
				"style": map[string]any{"type": "string", "enum": []string{"primary", "secondary", "ghost"}},
			},
		},
	},
	"core/cta": {
		Name: "core/cta", DisplayName: "Call to action", Category: "layout", Container: true,
		Description: "Heading + body + button bundle.",
		AttrsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"heading":      map[string]any{"type": "string"},
				"body":         map[string]any{"type": "string"},
				"button_label": map[string]any{"type": "string"},
				"button_url":   map[string]any{"type": "string"},
			},
		},
	},
}

// listBlockTypes returns the registry sorted by category for stable
// agent-facing output.
func listBlockTypes(category string) []BlockTypeInfo {
	out := make([]BlockTypeInfo, 0, len(coreBlockTypes))
	for _, info := range coreBlockTypes {
		if category != "" && info.Category != category {
			continue
		}
		out = append(out, info)
	}
	// Stable ordering: by category then name.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := out[j-1], out[j]
			if a.Category > b.Category || (a.Category == b.Category && a.Name > b.Name) {
				out[j-1], out[j] = b, a
				continue
			}
			break
		}
	}
	return out
}

// ── input parsing helpers (used by block_* tool handlers) ──────────

// parseInsertPosition extracts the position fields from a tool args
// map. Returns ErrAmbiguous if more than one of after/before/index is
// supplied.
func parseInsertPosition(args map[string]any) (insertPosition, error) {
	var p insertPosition
	if v, ok := args["after_id"].(string); ok && v != "" {
		p.After = v
	}
	if v, ok := args["before_id"].(string); ok && v != "" {
		p.Before = v
	}
	if v, ok := args["parent_id"].(string); ok && v != "" {
		p.ParentID = v
	}
	if v, ok := asInt64(args["index"]); ok {
		p.Index = int(v)
		p.UseIndex = true
	}
	count := 0
	if p.After != "" {
		count++
	}
	if p.Before != "" {
		count++
	}
	if p.UseIndex {
		count++
	}
	if count > 1 {
		return p, errors.New("supply at most one of after_id, before_id, index")
	}
	return p, nil
}

// parseBlockFromArgs builds a Block from MCP args (type, attrs, inner).
func parseBlockFromArgs(args map[string]any) (Block, error) {
	t, _ := args["type"].(string)
	if t == "" {
		return Block{}, errors.New("type required")
	}
	if _, known := coreBlockTypes[t]; !known && !looksNamespaced(t) {
		return Block{}, fmt.Errorf("unknown block type %q", t)
	}
	attrs, _ := args["attrs"].(map[string]any)
	var inner []Block
	if raw, ok := args["inner"]; ok && raw != nil {
		b, err := json.Marshal(raw)
		if err != nil {
			return Block{}, err
		}
		if err := json.Unmarshal(b, &inner); err != nil {
			return Block{}, fmt.Errorf("inner: %w", err)
		}
	}
	return Block{Type: t, Attrs: attrs, Inner: inner}, nil
}
