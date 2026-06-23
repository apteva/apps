package main

import (
	"encoding/json"
	"errors"
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
			httpJSON(w, map[string]any{"nodes": nodeTree(nodes)})
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
	default:
		httpErr(w, http.StatusNotFound, "not found")
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
		httpJSON(w, map[string]any{"node": n})
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
	if r.Body == nil {
		return nil
	}
	err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(dst)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
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
