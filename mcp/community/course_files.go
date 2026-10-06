package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	sdk "github.com/apteva/app-sdk"
)

func courseFileTools() []sdk.Tool {
	str := map[string]any{"type": "string"}
	return []sdk.Tool{
		{Name: "course_file_upload", Description: "Upload a private assignment or milestone evidence file through Storage. Args: name, content_base64, content_type?, assignment_id? or definition_id?.", InputSchema: schemaObject(map[string]any{"name": str, "content_base64": str, "content_type": str, "assignment_id": str, "definition_id": str}, []string{"name", "content_base64"}), Handler: toolCourseFileUpload},
		{Name: "course_file_url", Description: "Mint a URL for an assignment or milestone evidence file. Args: file_id, assignment_id? or definition_id?, member_id?.", InputSchema: schemaObject(map[string]any{"file_id": str, "assignment_id": str, "definition_id": str, "member_id": str}, []string{"file_id"}), Handler: toolCourseFileURL},
	}
}

func courseFileSpace(db *sql.DB, args map[string]any) (string, error) {
	if id := strArg(args, "assignment_id", ""); id != "" {
		a, err := loadAssignment(db, id)
		if err != nil {
			return "", err
		}
		return spaceByLesson(db, a.LessonID)
	}
	if id := strArg(args, "definition_id", ""); id != "" {
		var space string
		err := db.QueryRow(`SELECT space_id FROM milestone_definitions WHERE id=?`, id).Scan(&space)
		return space, err
	}
	return "", errors.New("assignment_id or definition_id required")
}

func toolCourseFileUpload(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name, err := mustStr(args, "name")
	if err != nil {
		return nil, err
	}
	b64, err := mustStr(args, "content_base64")
	if err != nil {
		return nil, err
	}
	member := strArg(args, "member_id", "")
	if member == "" {
		return nil, errors.New("member_id required")
	}
	space, err := courseFileSpace(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	if err = ensureActiveEnrollment(ctx.AppDB(), space, member); err != nil {
		return nil, err
	}
	if ctx.PlatformAPI() == nil || ctx.IntegrationFor("storage") == nil {
		return nil, errors.New("storage integration is unavailable")
	}
	var out struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Size int64  `json:"size_bytes"`
		URL  string `json:"url"`
	}
	err = ctx.PlatformAPI().CallAppResult("storage", "files_upload", map[string]any{"name": name, "content_base64": b64, "content_type": strArg(args, "content_type", "application/octet-stream"), "folder": "/.community/course-submissions/", "source": "community-course", "visibility": "private"}, &out)
	if err != nil {
		return nil, err
	}
	if out.ID == 0 {
		return nil, errors.New("storage returned no file id")
	}
	if _, err = ctx.AppDB().Exec(`INSERT OR IGNORE INTO learning_files(file_id,space_id,member_id,name) VALUES(?,?,?,?)`, strconv.FormatInt(out.ID, 10), space, member, name); err != nil {
		return nil, err
	}
	return map[string]any{"file_id": strconv.FormatInt(out.ID, 10), "name": name, "size_bytes": out.Size, "url": out.URL}, nil
}

func toolCourseFileURL(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	fileID, err := mustStr(args, "file_id")
	if err != nil {
		return nil, err
	}
	space, err := courseFileSpace(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	member := strArg(args, "member_id", "")
	viewer := strArg(args, "_viewer_member_id", member)
	if member == "" {
		member = viewer
	}
	var owner string
	if err = ctx.AppDB().QueryRow(`SELECT member_id FROM learning_files WHERE file_id=? AND space_id=?`, fileID, space).Scan(&owner); err != nil {
		return nil, errors.New("file is not attached to this course")
	}
	if owner != viewer && !isCourseInstructor(ctx.AppDB(), space, viewer) {
		return nil, errors.New("instructor access required")
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("storage integration is unavailable")
	}
	id, err := strconv.ParseInt(fileID, 10, 64)
	if err != nil {
		return nil, errors.New("invalid file id")
	}
	var out struct {
		URL       string `json:"url"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err = ctx.PlatformAPI().CallAppResult("storage", "files_get_url", map[string]any{"id": id, "ttl_seconds": 3600, "delivery": "direct"}, &out); err != nil {
		return nil, err
	}
	if out.URL == "" {
		return nil, fmt.Errorf("storage returned an empty file URL")
	}
	return out, nil
}

func validateCourseFiles(db *sql.DB, space, member string, files []string) error {
	for _, file := range files {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM learning_files WHERE file_id=? AND space_id=? AND member_id=?`, file, space, member).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("file %q is not an uploaded course file", file)
		}
	}
	return nil
}
