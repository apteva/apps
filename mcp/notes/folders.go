package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Folders are user-created and start empty. A note belongs to at most one
// folder (notes.folder_id; 0 = no folder).

type Folder struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	NoteCount int    `json:"note_count"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const maxFolderName = 80

var errFolderNotFound = errors.New("folder not found")

// --- HTTP -----------------------------------------------------------------------

func (a *App) handleFolders(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	switch r.Method {
	case http.MethodGet:
		folders, err := listFolders(ctx.AppDB(), ctx.CurrentProject())
		if err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"folders": folders})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		f, err := createFolder(ctx.AppDB(), ctx.CurrentProject(), body.Name)
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"folder": f})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleFolderItem(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	id, _ := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/folders/"), "/"), 10, 64)
	if id == 0 {
		http.Error(w, "folder id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		f, err := renameFolder(ctx.AppDB(), ctx.CurrentProject(), id, body.Name)
		if err != nil {
			folderHTTPError(w, err)
			return
		}
		writeJSON(w, map[string]any{"folder": f})
	case http.MethodDelete:
		moved, err := deleteFolder(ctx.AppDB(), ctx.CurrentProject(), id)
		if err != nil {
			folderHTTPError(w, err)
			return
		}
		writeJSON(w, map[string]any{"deleted": id, "notes_unfiled": moved})
	default:
		http.Error(w, "PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func folderHTTPError(w http.ResponseWriter, err error) {
	if errors.Is(err, errFolderNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	httpError(w, err, http.StatusBadRequest)
}

// --- MCP ------------------------------------------------------------------------

func (a *App) folderTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "notes_folders_list",
			Description: "List note folders with their active note counts.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler: func(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
				folders, err := listFolders(ctx.AppDB(), ctx.CurrentProject())
				if err != nil {
					return nil, err
				}
				return map[string]any{"folders": folders}, nil
			},
		},
		{
			Name:        "notes_folder_create",
			Description: "Create a note folder. Args: name. Names are unique per project (case-insensitive).",
			InputSchema: schemaObject(map[string]any{"name": sString()}, []string{"name"}),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				f, err := createFolder(ctx.AppDB(), ctx.CurrentProject(), stringArg(args, "name"))
				if err != nil {
					return nil, err
				}
				return map[string]any{"folder": f}, nil
			},
		},
		{
			Name:        "notes_folder_rename",
			Description: "Rename a note folder. Args: id, name.",
			InputSchema: schemaObject(map[string]any{"id": sInteger(), "name": sString()}, []string{"id", "name"}),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				f, err := renameFolder(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "id"), stringArg(args, "name"))
				if err != nil {
					return nil, err
				}
				return map[string]any{"folder": f}, nil
			},
		},
		{
			Name:        "notes_folder_delete",
			Description: "Delete a note folder. Its notes are kept and moved out of the folder. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": sInteger()}, []string{"id"}),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				id := int64Arg(args, "id")
				moved, err := deleteFolder(ctx.AppDB(), ctx.CurrentProject(), id)
				if err != nil {
					return nil, err
				}
				return map[string]any{"deleted": id, "notes_unfiled": moved}, nil
			},
		},
	}
}

// --- DB -------------------------------------------------------------------------

func cleanFolderName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", errors.New("folder name required")
	}
	if len([]rune(name)) > maxFolderName {
		return "", errors.New("folder name too long (max 80 characters)")
	}
	return name, nil
}

func listFolders(db *sql.DB, projectID string) ([]*Folder, error) {
	rows, err := db.Query(`
		SELECT f.id, f.name, f.created_at, f.updated_at,
		       (SELECT COUNT(*) FROM notes n
		        WHERE n.project_id = f.project_id AND n.folder_id = f.id AND n.status = 'active')
		FROM folders f
		WHERE f.project_id = ?
		ORDER BY f.name COLLATE NOCASE, f.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Folder{}
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.Name, &f.CreatedAt, &f.UpdatedAt, &f.NoteCount); err != nil {
			return nil, err
		}
		out = append(out, &f)
	}
	return out, rows.Err()
}

func getFolder(db *sql.DB, projectID string, id int64) (*Folder, error) {
	var f Folder
	err := db.QueryRow(`
		SELECT f.id, f.name, f.created_at, f.updated_at,
		       (SELECT COUNT(*) FROM notes n
		        WHERE n.project_id = f.project_id AND n.folder_id = f.id AND n.status = 'active')
		FROM folders f WHERE f.id = ? AND f.project_id = ?`, id, projectID).
		Scan(&f.ID, &f.Name, &f.CreatedAt, &f.UpdatedAt, &f.NoteCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errFolderNotFound
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func findFolderByName(db *sql.DB, projectID, name string) (*Folder, error) {
	var id int64
	err := db.QueryRow(`SELECT id FROM folders WHERE project_id = ? AND name = ? COLLATE NOCASE`, projectID, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return getFolder(db, projectID, id)
}

func createFolder(db *sql.DB, projectID, name string) (*Folder, error) {
	name, err := cleanFolderName(name)
	if err != nil {
		return nil, err
	}
	if existing, err := findFolderByName(db, projectID, name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, errors.New("a folder named \"" + existing.Name + "\" already exists")
	}
	now := nowUTC()
	res, err := db.Exec(`INSERT INTO folders (project_id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		projectID, name, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getFolder(db, projectID, id)
}

func renameFolder(db *sql.DB, projectID string, id int64, name string) (*Folder, error) {
	name, err := cleanFolderName(name)
	if err != nil {
		return nil, err
	}
	if existing, err := findFolderByName(db, projectID, name); err != nil {
		return nil, err
	} else if existing != nil && existing.ID != id {
		return nil, errors.New("a folder named \"" + existing.Name + "\" already exists")
	}
	res, err := db.Exec(`UPDATE folders SET name = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		name, nowUTC(), id, projectID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errFolderNotFound
	}
	return getFolder(db, projectID, id)
}

// deleteFolder removes the folder and moves its notes (active and archived)
// out of it. Returns how many notes were moved.
func deleteFolder(db *sql.DB, projectID string, id int64) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM folders WHERE id = ? AND project_id = ?`, id, projectID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, errFolderNotFound
	}
	res, err = tx.Exec(`UPDATE notes SET folder_id = 0 WHERE folder_id = ? AND project_id = ?`, id, projectID)
	if err != nil {
		return 0, err
	}
	moved, _ := res.RowsAffected()
	return moved, tx.Commit()
}

// resolveFolderArg turns a note's folder_id / folder args into a folder id.
// folder_id wins; 0 or null clears the folder. `folder` is a name: an existing
// folder is matched case-insensitively and a missing one is created, so agents
// can file notes by name in one call. ok=false means neither arg was given.
func resolveFolderArg(db *sql.DB, projectID string, args map[string]any) (id int64, ok bool, err error) {
	if v, present := args["folder_id"]; present {
		if v == nil {
			return 0, true, nil
		}
		id = int64Arg(args, "folder_id")
		if id == 0 {
			return 0, true, nil
		}
		if _, err := getFolder(db, projectID, id); err != nil {
			return 0, true, err
		}
		return id, true, nil
	}
	if _, present := args["folder"]; present {
		name := strings.TrimSpace(stringArg(args, "folder"))
		if name == "" {
			return 0, true, nil
		}
		f, err := findFolderByName(db, projectID, strings.Join(strings.Fields(name), " "))
		if err != nil {
			return 0, true, err
		}
		if f == nil {
			if f, err = createFolder(db, projectID, name); err != nil {
				return 0, true, err
			}
		}
		return f.ID, true, nil
	}
	return 0, false, nil
}
