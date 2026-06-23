package main

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolBooksCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	target, _ := intArg(args, "target_word_count")
	book, err := createBook(ctx.AppDB(), ctx.CurrentProject(), &Book{
		Title:           strArg(args, "title"),
		Subtitle:        strArg(args, "subtitle"),
		AuthorName:      strArg(args, "author_name"),
		Description:     strArg(args, "description"),
		Kind:            strArg(args, "kind"),
		Language:        strArg(args, "language"),
		TargetWordCount: target,
	}, boolArg(args, "create_starter"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"created": true, "book": book}, nil
}

func (a *App) toolBooksList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	books, err := listBooks(ctx.AppDB(), ctx.CurrentProject(), boolArg(args, "include_archived"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"books": books, "count": len(books)}, nil
}

func (a *App) toolBooksGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	book, err := getBook(ctx.AppDB(), id, ctx.CurrentProject())
	if err != nil {
		return nil, err
	}
	if book == nil {
		return map[string]any{"found": false}, nil
	}
	out := map[string]any{"found": true, "book": book}
	if boolArg(args, "include_tree") {
		nodes, err := listNodes(ctx.AppDB(), id)
		if err != nil {
			return nil, err
		}
		out["nodes"] = nodeTree(nodes)
	}
	return out, nil
}

func (a *App) toolBooksUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	if err := updateBook(ctx.AppDB(), id, ctx.CurrentProject(), args); err != nil {
		if errors.Is(err, errNotFound) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"updated": true, "id": id}, nil
}

func (a *App) toolBooksArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	if err := archiveBook(ctx.AppDB(), id, ctx.CurrentProject()); err != nil {
		if errors.Is(err, errNotFound) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"archived": true, "id": id}, nil
}

func (a *App) toolNodesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	bookID, _ := int64Arg(args, "book_id")
	target, _ := intArg(args, "target_word_count")
	position, ok := intArg(args, "position")
	if !ok {
		position = -1
	}
	parentID, _ := nullableInt64Arg(args, "parent_id")
	node, err := createNode(ctx.AppDB(), &BookNode{
		BookID:          bookID,
		ParentID:        parentID,
		Type:            strArg(args, "type"),
		Title:           strArg(args, "title"),
		BodyMarkdown:    strArg(args, "body_markdown"),
		Summary:         strArg(args, "summary"),
		Position:        position,
		Status:          strArg(args, "status"),
		TargetWordCount: target,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"created": true, "node": node}, nil
}

func (a *App) toolNodesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	bookID, _ := int64Arg(args, "book_id")
	nodes, err := listNodes(ctx.AppDB(), bookID)
	if err != nil {
		return nil, err
	}
	tree := nodeTree(nodes)
	return map[string]any{"nodes": tree, "count": len(nodes)}, nil
}

func (a *App) toolNodesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	node, err := getNode(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return map[string]any{"found": false}, nil
	}
	return map[string]any{"found": true, "node": node}, nil
}

func (a *App) toolNodesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	if err := updateNode(ctx.AppDB(), id, args); err != nil {
		if errors.Is(err, errNotFound) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"updated": true, "id": id}, nil
}

func (a *App) toolNodesMove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	parentID, _ := nullableInt64Arg(args, "parent_id")
	position, ok := intArg(args, "position")
	if !ok {
		position = -1
	}
	if err := moveNode(ctx.AppDB(), id, parentID, position); err != nil {
		if errors.Is(err, errNotFound) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"moved": true, "id": id}, nil
}

func (a *App) toolNodesDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	if err := deleteNode(ctx.AppDB(), id); err != nil {
		if errors.Is(err, errNotFound) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"deleted": true, "id": id}, nil
}

func (a *App) toolNotesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	bookID, _ := int64Arg(args, "book_id")
	nodeID, _ := nullableInt64Arg(args, "node_id")
	note, err := createNote(ctx.AppDB(), &BookNote{
		BookID: bookID,
		NodeID: nodeID,
		Type:   strArg(args, "type"),
		Title:  strArg(args, "title"),
		Body:   strArg(args, "body"),
		URL:    strArg(args, "url"),
		Tags:   stringSliceArg(args, "tags"),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"created": true, "note": note}, nil
}

func (a *App) toolNotesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	bookID, _ := int64Arg(args, "book_id")
	nodeID, _ := nullableInt64Arg(args, "node_id")
	notes, err := listNotes(ctx.AppDB(), bookID, nodeID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"notes": notes, "count": len(notes)}, nil
}

func (a *App) toolNotesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	if err := updateNote(ctx.AppDB(), id, args); err != nil {
		if errors.Is(err, errNotFound) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"updated": true, "id": id}, nil
}

func (a *App) toolNotesDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	if err := deleteNote(ctx.AppDB(), id); err != nil {
		if errors.Is(err, errNotFound) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"deleted": true, "id": id}, nil
}

func (a *App) toolRevisionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	nodeID, _ := int64Arg(args, "node_id")
	limit, _ := intArg(args, "limit")
	revisions, err := listRevisions(ctx.AppDB(), nodeID, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"revisions": revisions, "count": len(revisions)}, nil
}

func (a *App) toolRevisionRestore(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	revisionID, _ := int64Arg(args, "revision_id")
	if err := restoreRevision(ctx.AppDB(), revisionID); err != nil {
		if errors.Is(err, errNotFound) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"restored": true, "revision_id": revisionID}, nil
}

func (a *App) toolBooksExport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return exportBook(ctx, args)
}

func exportBook(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	bookID, _ := int64Arg(args, "book_id")
	if bookID <= 0 {
		return nil, errors.New("book_id required")
	}
	format := strArg(args, "format")
	if format == "" {
		format = "markdown"
	}
	if format != "markdown" && format != "md" {
		return nil, fmt.Errorf("unsupported export format %q; v1 supports markdown only", format)
	}
	book, err := getBook(ctx.AppDB(), bookID, ctx.CurrentProject())
	if err != nil {
		return nil, err
	}
	if book == nil {
		return map[string]any{"found": false}, nil
	}
	nodes, err := listNodes(ctx.AppDB(), bookID)
	if err != nil {
		return nil, err
	}
	content := renderMarkdownExport(book, nodes, boolArg(args, "include_status"))
	name := strArg(args, "output_name")
	if name == "" {
		name = slugFilename(book.Title) + ".md"
	}
	if !stringsHasSuffixFold(name, ".md") {
		name += ".md"
	}
	result := ExportResult{
		Content:     content,
		ContentType: "text/markdown; charset=utf-8",
		SizeBytes:   len([]byte(content)),
	}
	export := BookExport{BookID: bookID, Format: "markdown", Filename: name, Status: "created"}
	if boolArg(args, "store") {
		folder := strArg(args, "output_folder")
		if folder == "" {
			folder = "/books/"
		}
		uploaded, err := uploadToStorage(ctx, name, folder, "text/markdown; charset=utf-8", []byte(content))
		if err != nil {
			export.Status = "failed"
			export.Error = err.Error()
			row, _ := createExportRow(ctx.AppDB(), &export)
			result.Export = row
			return nil, err
		}
		export.StorageFileID = fmt.Sprint(uploaded.ID)
		result.StorageFileID = export.StorageFileID
		result.URL = uploaded.URL
	}
	row, err := createExportRow(ctx.AppDB(), &export)
	if err != nil {
		return nil, err
	}
	result.Export = row
	return result, nil
}

func stringsHasSuffixFold(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return strings.EqualFold(s[len(s)-len(suffix):], suffix)
}
