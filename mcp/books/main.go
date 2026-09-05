// Books is a structured manuscript workspace for Apteva.
//
// Books owns manuscript structure, publication metadata, digital and print
// assets, validated exports, and store-ready publication packages. Store
// submission remains an explicit human-controlled step.
package main

import (
	"errors"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: books
display_name: Books
version: 0.2.2
description: |
  Publication-ready book workspace for Apteva. Write structured manuscripts,
  manage covers and images, validate EPUB 3, typeset print PDFs, and build
  platform-specific publication packages.
author: Apteva
homepage: https://github.com/apteva/apps/tree/main/mcp/books
icon: /ui/icon.svg
icon_style: monochrome
tags: [books, writing, manuscripts, authors]
scopes: [project, global]
min_apteva_version: "0.11.0"
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  apps:
    - name: storage
      optional: true
      reason: Store exported manuscript files.
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: books_create, description: "Create a book project." }
    - { name: books_list, description: "List book projects." }
    - { name: books_get, description: "Fetch one book with metadata and optional manuscript tree." }
    - { name: books_update, description: "Update book metadata and status." }
    - { name: books_archive, description: "Archive a book." }
    - { name: book_nodes_create, description: "Create a manuscript node such as a part, chapter, section, front matter, back matter, or appendix." }
    - { name: book_nodes_list, description: "List manuscript nodes for a book as an ordered tree." }
    - { name: book_nodes_get, description: "Fetch one manuscript node with body content." }
    - { name: book_nodes_update, description: "Update a node's title, body, status, summary, or target word count." }
    - { name: book_node_body_edit, description: "Atomically append, prepend, or replace exact text in a manuscript body with an optional checksum guard." }
    - { name: book_nodes_move, description: "Move a node to a new parent or position." }
    - { name: book_nodes_delete, description: "Delete a manuscript node." }
    - { name: book_notes_create, description: "Create a note attached to a book or manuscript node." }
    - { name: book_notes_list, description: "List notes for a book or node." }
    - { name: book_notes_update, description: "Update a note." }
    - { name: book_notes_delete, description: "Delete a note." }
    - { name: book_revisions_list, description: "List revisions for a manuscript node." }
    - { name: book_revision_restore, description: "Restore a previous node revision." }
    - { name: book_assets_create, description: "Attach an ebook cover, full-wrap print cover, or interior image." }
    - { name: book_assets_list, description: "List publication assets for a book." }
    - { name: book_assets_update, description: "Update image alt text or caption." }
    - { name: book_assets_delete, description: "Delete a publication asset." }
    - { name: books_publication_check, description: "Build a store-specific publication readiness checklist and EPUB validation report." }
    - { name: books_export, description: "Export markdown, EPUB 3, print PDF, or a platform publication ZIP." }
  ui_panels:
    - slot: project.page
      label: Books
      icon: book-open
      entry: /ui/BooksPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/books
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/books.db
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
		return errors.New("books requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("books mounted", "version", "0.2.2")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/books", Handler: a.handleBooksCollection},
		{Pattern: "/books/", Handler: a.handleBooksItem},
		{Pattern: "/nodes/", Handler: a.handleNodesItem},
		{Pattern: "/notes/", Handler: a.handleNotesItem},
		{Pattern: "/assets/", Handler: a.handleAssetsItem},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "books_create", Description: "Create a book project.", InputSchema: schemaObject(map[string]any{
			"title":             map[string]any{"type": "string"},
			"subtitle":          map[string]any{"type": "string"},
			"author_name":       map[string]any{"type": "string"},
			"description":       map[string]any{"type": "string"},
			"kind":              map[string]any{"type": "string"},
			"language":          map[string]any{"type": "string"},
			"target_word_count": map[string]any{"type": "integer"},
			"create_starter":    map[string]any{"type": "boolean"},
			"publication":       map[string]any{"type": "object"},
		}, []string{"title"}), Handler: a.toolBooksCreate},
		{Name: "books_list", Description: "List book projects.", InputSchema: schemaObject(map[string]any{
			"include_archived": map[string]any{"type": "boolean"},
		}, nil), Handler: a.toolBooksList},
		{Name: "books_get", Description: "Fetch one book with metadata and optional manuscript tree.", InputSchema: schemaObject(map[string]any{
			"id":           map[string]any{"type": "integer"},
			"include_tree": map[string]any{"type": "boolean"},
		}, []string{"id"}), Handler: a.toolBooksGet},
		{Name: "books_update", Description: "Update book metadata and status.", InputSchema: schemaObject(bookUpdateSchema(), []string{"id"}), Handler: a.toolBooksUpdate},
		{Name: "books_archive", Description: "Archive a book.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolBooksArchive},
		{Name: "book_nodes_create", Description: "Create a manuscript node.", InputSchema: schemaObject(map[string]any{
			"book_id":           map[string]any{"type": "integer"},
			"parent_id":         map[string]any{"type": "integer"},
			"type":              map[string]any{"type": "string"},
			"title":             map[string]any{"type": "string"},
			"body_markdown":     map[string]any{"type": "string"},
			"summary":           map[string]any{"type": "string"},
			"position":          map[string]any{"type": "integer"},
			"status":            map[string]any{"type": "string"},
			"target_word_count": map[string]any{"type": "integer"},
		}, []string{"book_id", "title"}), Handler: a.toolNodesCreate},
		{Name: "book_nodes_list", Description: "List manuscript node metadata as an ordered tree. Bodies are omitted by default; fetch one body with book_nodes_get.", InputSchema: schemaObject(map[string]any{
			"book_id":      map[string]any{"type": "integer"},
			"include_body": map[string]any{"type": "boolean"},
		}, []string{"book_id"}), Handler: a.toolNodesList},
		{Name: "book_nodes_get", Description: "Fetch one manuscript node with body content.", InputSchema: schemaObject(map[string]any{
			"id": map[string]any{"type": "integer"},
		}, []string{"id"}), Handler: a.toolNodesGet},
		{Name: "book_nodes_update", Description: "Update a node and snapshot a revision.", InputSchema: schemaObject(nodeUpdateSchema(), []string{"id"}), Handler: a.toolNodesUpdate},
		{Name: "book_node_body_edit", Description: "Atomically edit part of a manuscript body without resubmitting the whole node. Use expected_body_sha256 to reject stale edits.", InputSchema: schemaObject(map[string]any{
			"id":                   map[string]any{"type": "integer"},
			"operation":            map[string]any{"type": "string", "enum": []string{"append", "prepend", "replace"}},
			"content":              map[string]any{"type": "string"},
			"match":                map[string]any{"type": "string"},
			"expected_body_sha256": map[string]any{"type": "string"},
			"change_summary":       map[string]any{"type": "string"},
		}, []string{"id", "operation", "content"}), Handler: a.toolNodeBodyEdit},
		{Name: "book_nodes_move", Description: "Move a node to a new parent or position.", InputSchema: schemaObject(map[string]any{
			"id":        map[string]any{"type": "integer"},
			"parent_id": map[string]any{"type": "integer"},
			"position":  map[string]any{"type": "integer"},
		}, []string{"id"}), Handler: a.toolNodesMove},
		{Name: "book_nodes_delete", Description: "Delete a manuscript node.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolNodesDelete},
		{Name: "book_notes_create", Description: "Create a note attached to a book or manuscript node.", InputSchema: schemaObject(noteCreateSchema(), []string{"book_id", "title"}), Handler: a.toolNotesCreate},
		{Name: "book_notes_list", Description: "List notes for a book or node.", InputSchema: schemaObject(map[string]any{
			"book_id": map[string]any{"type": "integer"},
			"node_id": map[string]any{"type": "integer"},
		}, []string{"book_id"}), Handler: a.toolNotesList},
		{Name: "book_notes_update", Description: "Update a note.", InputSchema: schemaObject(noteUpdateSchema(), []string{"id"}), Handler: a.toolNotesUpdate},
		{Name: "book_notes_delete", Description: "Delete a note.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolNotesDelete},
		{Name: "book_revisions_list", Description: "List revisions for a manuscript node.", InputSchema: schemaObject(map[string]any{
			"node_id": map[string]any{"type": "integer"},
			"limit":   map[string]any{"type": "integer"},
		}, []string{"node_id"}), Handler: a.toolRevisionsList},
		{Name: "book_revision_restore", Description: "Restore a previous node revision.", InputSchema: schemaObject(map[string]any{
			"revision_id": map[string]any{"type": "integer"},
		}, []string{"revision_id"}), Handler: a.toolRevisionRestore},
		{Name: "book_assets_create", Description: "Attach an ebook cover, full-wrap print cover, or interior image.", InputSchema: schemaObject(map[string]any{
			"book_id": map[string]any{"type": "integer"}, "node_id": map[string]any{"type": "integer"},
			"kind": map[string]any{"type": "string"}, "filename": map[string]any{"type": "string"},
			"content_type": map[string]any{"type": "string"}, "content_base64": map[string]any{"type": "string"},
			"alt_text": map[string]any{"type": "string"}, "caption": map[string]any{"type": "string"},
		}, []string{"book_id", "filename", "content_base64"}), Handler: a.toolAssetsCreate},
		{Name: "book_assets_list", Description: "List publication assets for a book.", InputSchema: schemaObject(map[string]any{"book_id": map[string]any{"type": "integer"}}, []string{"book_id"}), Handler: a.toolAssetsList},
		{Name: "book_assets_update", Description: "Update image alt text or caption.", InputSchema: schemaObject(map[string]any{
			"id": map[string]any{"type": "integer"}, "alt_text": map[string]any{"type": "string"}, "caption": map[string]any{"type": "string"},
		}, []string{"id"}), Handler: a.toolAssetsUpdate},
		{Name: "book_assets_delete", Description: "Delete a publication asset.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolAssetsDelete},
		{Name: "books_publication_check", Description: "Build a store-specific publication readiness checklist and EPUB validation report.", InputSchema: schemaObject(map[string]any{
			"book_id": map[string]any{"type": "integer"}, "platform": map[string]any{"type": "string"}, "include_notes": map[string]any{"type": "boolean"},
		}, []string{"book_id"}), Handler: a.toolPublicationCheck},
		{Name: "books_export", Description: "Export markdown, EPUB 3, print PDF, or a platform publication ZIP.", InputSchema: schemaObject(map[string]any{
			"book_id":        map[string]any{"type": "integer"},
			"format":         map[string]any{"type": "string"},
			"platform":       map[string]any{"type": "string"},
			"store":          map[string]any{"type": "boolean"},
			"output_name":    map[string]any{"type": "string"},
			"output_folder":  map[string]any{"type": "string"},
			"include_notes":  map[string]any{"type": "boolean"},
			"include_status": map[string]any{"type": "boolean"},
		}, []string{"book_id"}), Handler: a.toolBooksExport},
	}
}

func main() {
	sdk.Run(&App{})
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func bookUpdateSchema() map[string]any {
	return map[string]any{
		"id":                map[string]any{"type": "integer"},
		"title":             map[string]any{"type": "string"},
		"subtitle":          map[string]any{"type": "string"},
		"author_name":       map[string]any{"type": "string"},
		"description":       map[string]any{"type": "string"},
		"kind":              map[string]any{"type": "string"},
		"language":          map[string]any{"type": "string"},
		"target_word_count": map[string]any{"type": "integer"},
		"status":            map[string]any{"type": "string"},
		"publication":       map[string]any{"type": "object"},
	}
}

func nodeUpdateSchema() map[string]any {
	return map[string]any{
		"id":                map[string]any{"type": "integer"},
		"type":              map[string]any{"type": "string"},
		"title":             map[string]any{"type": "string"},
		"body_markdown":     map[string]any{"type": "string"},
		"summary":           map[string]any{"type": "string"},
		"status":            map[string]any{"type": "string"},
		"target_word_count": map[string]any{"type": "integer"},
		"change_summary":    map[string]any{"type": "string"},
	}
}

func noteCreateSchema() map[string]any {
	return map[string]any{
		"book_id": map[string]any{"type": "integer"},
		"node_id": map[string]any{"type": "integer"},
		"type":    map[string]any{"type": "string"},
		"title":   map[string]any{"type": "string"},
		"body":    map[string]any{"type": "string"},
		"url":     map[string]any{"type": "string"},
		"tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}
}

func noteUpdateSchema() map[string]any {
	props := noteCreateSchema()
	props["id"] = map[string]any{"type": "integer"}
	delete(props, "book_id")
	return props
}
