// MCP tools for the core/form block: listing forms, browsing
// submissions, fetching one in detail.

package main

import (
	"database/sql"
	"errors"

	sdk "github.com/apteva/app-sdk"
)

// toolFormsList walks all published posts in the project, finds
// every core/form block, and returns a flat list with submission
// counts so the panel can render a "forms inbox" without one query
// per form.
func (a *App) toolFormsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`
        SELECT id, kind, slug, title, body_blocks
        FROM posts
        WHERE project_id = ? AND deleted_at IS NULL
          AND body_blocks LIKE '%"type":"core/form"%'
    `, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type formRow struct {
		PostID           int64  `json:"post_id"`
		PostKind         string `json:"post_kind"`
		PostSlug         string `json:"post_slug"`
		PostTitle        string `json:"post_title"`
		BlockID          string `json:"block_id"`
		FieldsCount      int    `json:"fields_count"`
		ActionsCount     int    `json:"actions_count"`
		SubmissionsCount int    `json:"submissions_count"`
		LastSubmissionAt int64  `json:"last_submission_at,omitempty"`
	}
	var out []formRow
	for rows.Next() {
		var id int64
		var kind, slug, title, body string
		if err := rows.Scan(&id, &kind, &slug, &title, &body); err != nil {
			return nil, err
		}
		doc, err := parseDocument(body)
		if err != nil {
			continue
		}
		collectFormBlocks(doc.Blocks, func(b Block) {
			fc, _ := b.Attrs["fields"].([]any)
			ac, _ := b.Attrs["actions"].([]any)
			row := formRow{
				PostID: id, PostKind: kind, PostSlug: slug, PostTitle: title,
				BlockID:      b.ID,
				FieldsCount:  len(fc),
				ActionsCount: len(ac),
			}

			out = append(out, row)
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	counts, err := ctx.AppDB().Query(`SELECT block_id,COUNT(*),COALESCE(MAX(created_at),0) FROM form_submissions WHERE project_id=? GROUP BY block_id`, pid)
	if err != nil {
		return nil, err
	}
	defer counts.Close()
	type aggregate struct {
		count int
		last  int64
	}
	totals := map[string]aggregate{}
	for counts.Next() {
		var id string
		var v aggregate
		if err := counts.Scan(&id, &v.count, &v.last); err != nil {
			return nil, err
		}
		totals[id] = v
	}
	if err := counts.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		v := totals[out[i].BlockID]
		out[i].SubmissionsCount = v.count
		out[i].LastSubmissionAt = v.last
	}
	return map[string]any{"forms": out, "count": len(out)}, nil
}

func collectFormBlocks(bs []Block, fn func(Block)) {
	for _, b := range bs {
		if b.Type == "core/form" {
			fn(b)
		}
		if len(b.Inner) > 0 {
			collectFormBlocks(b.Inner, fn)
		}
	}
}

func (a *App) toolFormsSubmissionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	blockID := asString(args["block_id"])
	limit := 50
	if n, ok := asInt64(args["limit"]); ok && n > 0 {
		limit = int(n)
	}
	subs, err := dbListFormSubmissions(ctx.AppDB(), pid, blockID, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"submissions": subs, "count": len(subs)}, nil
}

func (a *App) toolFormsSubmissionGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["id"])
	if !ok || id <= 0 {
		return nil, errors.New("id required")
	}
	sub, err := dbGetFormSubmission(ctx.AppDB(), pid, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("submission not found")
		}
		return nil, err
	}
	return map[string]any{"submission": sub}, nil
}
