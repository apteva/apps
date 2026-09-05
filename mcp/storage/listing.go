package main

import (
	"context"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

func escapeLike(s string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(s)
}
func (a *App) listFilesPage(c context.Context, app *sdk.AppCtx, args map[string]any, list bool) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	folder := strArg(args, "folder")
	if list || folder != "" {
		folder, err = validateFolder(folder)
		if err != nil {
			return nil, err
		}
	}
	limit := intArg(args, "limit", 50)
	if list {
		limit = intArg(args, "limit", 200)
	}
	limit = min(max(limit, 1), 500)
	offset := max(intArg(args, "offset", 0), 0)
	recursive, _ := args["recursive"].(bool)
	var allow func(*File) bool
	if caller := sdk.CallerFrom(c); caller != nil {
		allow = func(f *File) bool { return caller.Allows("files.read", fileResource(f.Folder)) }
	}
	opts := searchOpts{Folder: folder, Recursive: recursive, SortByName: list, Q: strArg(args, "q"), ContentType: strArg(args, "content_type"), SHA256: strArg(args, "sha256"), Tag: strArg(args, "tag"), Source: strArg(args, "source"), Limit: limit + 1, Offset: offset}
	files, err := dbSearchFiltered(app.AppDB(), pid, opts, allow)
	if err != nil {
		return nil, err
	}
	more := len(files) > limit
	if more {
		files = files[:limit]
	}
	return map[string]any{"files": files, "count": len(files), "folder": folder, "recursive": recursive, "offset": offset, "next_offset": offset + len(files), "has_more": more}, nil
}
