package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) handleBooksCollection(w http.ResponseWriter, r *http.Request) {
	app := ctxForRequest(r)
	switch r.Method {
	case http.MethodGet:
		books, err := listBooks(app.AppDB(), app.CurrentProject(), r.URL.Query().Get("include_archived") == "1")
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"books": books})
	case http.MethodPost:
		var body struct {
			Book
			CreateStarter bool `json:"create_starter"`
		}
		if err := decodeJSON(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		book, err := createBook(app.AppDB(), app.CurrentProject(), &body.Book, body.CreateStarter)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusCreated)
		httpJSON(w, map[string]any{"book": book})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleBooksItem(w http.ResponseWriter, r *http.Request) {
	app := ctxForRequest(r)
	rest := strings.TrimPrefix(r.URL.Path, "/books/")
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid book id")
		return
	}
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	switch tail {
	case "":
		switch r.Method {
		case http.MethodGet:
			book, err := getBook(app.AppDB(), id, app.CurrentProject())
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if book == nil {
				httpErr(w, http.StatusNotFound, "book not found")
				return
			}
			out := map[string]any{"book": book}
			if r.URL.Query().Get("include_tree") == "1" {
				nodes, err := listNodes(app.AppDB(), id)
				if err != nil {
					httpErr(w, http.StatusInternalServerError, err.Error())
					return
				}
				out["nodes"] = nodeTree(nodes)
			}
			httpJSON(w, out)
		case http.MethodPatch:
			var fields map[string]any
			if err := decodeJSON(r, &fields); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			if err := updateBook(app.AppDB(), id, app.CurrentProject(), fields); err != nil {
				writeStoreErr(w, err, "book not found")
				return
			}
			httpJSON(w, map[string]any{"updated": true, "id": id})
		default:
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "archive":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if err := archiveBook(app.AppDB(), id, app.CurrentProject()); err != nil {
			writeStoreErr(w, err, "book not found")
			return
		}
		httpJSON(w, map[string]any{"archived": true, "id": id})
	case "nodes":
		switch r.Method {
		case http.MethodGet:
			nodes, err := listNodes(app.AppDB(), id)
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			httpJSON(w, map[string]any{"nodes": nodeTreeForList(nodes, r.URL.Query().Get("include_body") == "1")})
		case http.MethodPost:
			var n BookNode
			if err := decodeJSON(r, &n); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			n.BookID = id
			if n.Position == 0 && r.URL.Query().Get("position") == "" {
				n.Position = -1
			}
			node, err := createNode(app.AppDB(), &n)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			w.WriteHeader(http.StatusCreated)
			httpJSON(w, map[string]any{"node": node})
		default:
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "notes":
		switch r.Method {
		case http.MethodGet:
			var nodeID *int64
			if raw := r.URL.Query().Get("node_id"); raw != "" {
				if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
					nodeID = &n
				}
			}
			notes, err := listNotes(app.AppDB(), id, nodeID)
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			httpJSON(w, map[string]any{"notes": notes})
		case http.MethodPost:
			var note BookNote
			if err := decodeJSON(r, &note); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			note.BookID = id
			created, err := createNote(app.AppDB(), &note)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			w.WriteHeader(http.StatusCreated)
			httpJSON(w, map[string]any{"note": created})
		default:
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "assets":
		switch r.Method {
		case http.MethodGet:
			assets, err := listAssets(app.AppDB(), id)
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			httpJSON(w, map[string]any{"assets": assets})
		case http.MethodPost:
			var body struct {
				NodeID        *int64 `json:"node_id"`
				Kind          string `json:"kind"`
				Filename      string `json:"filename"`
				ContentType   string `json:"content_type"`
				ContentBase64 string `json:"content_base64"`
				AltText       string `json:"alt_text"`
				Caption       string `json:"caption"`
			}
			if err := decodeJSONLimit(r, &body, maxAssetUploadRequestBytes); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			content, err := base64.StdEncoding.DecodeString(body.ContentBase64)
			if err != nil {
				httpErr(w, http.StatusBadRequest, "content_base64 is invalid")
				return
			}
			asset, err := createAsset(app.AppDB(), &BookAsset{BookID: id, NodeID: body.NodeID, Kind: body.Kind, Filename: body.Filename, ContentType: body.ContentType, AltText: body.AltText, Caption: body.Caption, Content: content})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			w.WriteHeader(http.StatusCreated)
			httpJSON(w, map[string]any{"asset": asset})
		default:
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "export":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var args map[string]any
		_ = decodeJSON(r, &args)
		if args == nil {
			args = map[string]any{}
		}
		args["book_id"] = id
		out, err := exportBook(app, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case "publication-check":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var args map[string]any
		_ = decodeJSON(r, &args)
		if args == nil {
			args = map[string]any{}
		}
		args["book_id"] = id
		out, err := a.toolPublicationCheck(app, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusNotFound, "not found")
	}
}

func (a *App) handleAssetsItem(w http.ResponseWriter, r *http.Request) {
	app := ctxForRequest(r)
	rest := strings.TrimPrefix(r.URL.Path, "/assets/")
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid asset id")
		return
	}
	asset, err := getAsset(app.AppDB(), id, true)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if asset == nil {
		httpErr(w, http.StatusNotFound, "asset not found")
		return
	}
	book, err := getBook(app.AppDB(), asset.BookID, app.CurrentProject())
	if err != nil || book == nil {
		httpErr(w, http.StatusNotFound, "asset not found")
		return
	}
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	if r.Method == http.MethodGet && tail == "content" {
		w.Header().Set("Content-Type", asset.ContentType)
		w.Header().Set("Content-Disposition", `inline; filename="`+strings.ReplaceAll(asset.Filename, `"`, "")+`"`)
		w.Header().Set("Cache-Control", "private, max-age=300")
		_, _ = w.Write(asset.Content)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var fields map[string]any
		if err := decodeJSON(r, &fields); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := updateAsset(app.AppDB(), id, fields); err != nil {
			writeStoreErr(w, err, "asset not found")
			return
		}
		httpJSON(w, map[string]any{"updated": true, "id": id})
	case http.MethodDelete:
		if err := deleteAsset(app.AppDB(), id); err != nil {
			writeStoreErr(w, err, "asset not found")
			return
		}
		httpJSON(w, map[string]any{"deleted": true, "id": id})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleNodesItem(w http.ResponseWriter, r *http.Request) {
	app := ctxForRequest(r)
	rest := strings.TrimPrefix(r.URL.Path, "/nodes/")
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid node id")
		return
	}
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	switch r.Method {
	case http.MethodGet:
		n, err := getNode(app.AppDB(), id)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if n == nil {
			httpErr(w, http.StatusNotFound, "node not found")
			return
		}
		httpJSON(w, map[string]any{"node": n, "body_sha256": nodeBodySHA256(n.BodyMarkdown)})
	case http.MethodPatch:
		var fields map[string]any
		if err := decodeJSON(r, &fields); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := updateNode(app.AppDB(), id, fields); err != nil {
			writeStoreErr(w, err, "node not found")
			return
		}
		httpJSON(w, map[string]any{"updated": true, "id": id})
	case http.MethodDelete:
		if err := deleteNode(app.AppDB(), id); err != nil {
			writeStoreErr(w, err, "node not found")
			return
		}
		httpJSON(w, map[string]any{"deleted": true, "id": id})
	case http.MethodPost:
		if tail == "body" {
			var fields map[string]any
			if err := decodeJSON(r, &fields); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			result, err := editNodeBody(
				app.AppDB(), id, strArg(fields, "operation"), strArg(fields, "content"),
				strArg(fields, "match"), strArg(fields, "expected_body_sha256"), strArg(fields, "change_summary"),
			)
			if errors.Is(err, errNodeEditConflict) {
				httpErr(w, http.StatusConflict, err.Error())
				return
			}
			if err != nil {
				writeStoreErr(w, err, "node not found")
				return
			}
			httpJSON(w, map[string]any{"updated": true, "edit": result})
			return
		}
		var body struct {
			ParentID *int64 `json:"parent_id"`
			Position int    `json:"position"`
		}
		if tail == "move" {
			if err := decodeJSON(r, &body); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			if err := moveNode(app.AppDB(), id, body.ParentID, body.Position); err != nil {
				writeStoreErr(w, err, "node not found")
				return
			}
			httpJSON(w, map[string]any{"moved": true, "id": id})
			return
		}
		httpErr(w, http.StatusNotFound, "not found")
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleNotesItem(w http.ResponseWriter, r *http.Request) {
	app := ctxForRequest(r)
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/notes/"), 10, 64)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid note id")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var fields map[string]any
		if err := decodeJSON(r, &fields); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := updateNote(app.AppDB(), id, fields); err != nil {
			writeStoreErr(w, err, "note not found")
			return
		}
		httpJSON(w, map[string]any{"updated": true, "id": id})
	case http.MethodDelete:
		if err := deleteNote(app.AppDB(), id); err != nil {
			writeStoreErr(w, err, "note not found")
			return
		}
		httpJSON(w, map[string]any{"deleted": true, "id": id})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func ctxForRequest(r *http.Request) *sdk.AppCtx {
	if globalCtx == nil {
		return nil
	}
	if projectID := r.URL.Query().Get("project_id"); projectID != "" {
		return globalCtx.WithProject(projectID)
	}
	return globalCtx
}

func decodeJSON(r *http.Request, dst any) error {
	return decodeJSONLimit(r, dst, 2<<20)
}

// Base64 expands a binary asset by roughly one third. Leave another MiB for
// JSON field names and metadata while preserving the 25 MiB decoded-asset
// limit enforced by createAsset.
const maxAssetUploadRequestBytes = ((maxAssetBytes + 2) / 3 * 4) + (1 << 20)

func decodeJSONLimit(r *http.Request, dst any, maxBytes int64) error {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		return errors.New("read json: " + err.Error())
	}
	if int64(len(body)) > maxBytes {
		return fmt.Errorf("request body exceeds %d MB limit", (maxBytes+(1<<20)-1)>>(20))
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return errors.New("invalid json: " + err.Error())
	}
	return nil
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeStoreErr(w http.ResponseWriter, err error, notFound string) {
	if errors.Is(err, errNotFound) {
		httpErr(w, http.StatusNotFound, notFound)
		return
	}
	httpErr(w, http.StatusBadRequest, err.Error())
}
