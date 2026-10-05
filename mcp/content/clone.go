package main

import (
	"database/sql"
	"errors"

	sdk "github.com/apteva/app-sdk"
)

// sites_clone makes an editable, hostname-free copy of a site and remaps all
// local IDs used by pages, taxonomy, menus, and media blocks.
func (a *App) toolSitesClone(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	sourceID, ok := asInt64(args["source_id"])
	if !ok || sourceID <= 0 {
		return nil, errors.New("source_id required")
	}
	source, err := dbGetSite(ctx.AppDB(), pid, sourceID)
	if err != nil {
		return nil, err
	}
	dest, err := dbCreateSite(ctx.AppDB(), pid, asString(args["slug"]), asString(args["name"]), "")
	if err != nil {
		return nil, err
	}
	if err := cloneSiteData(ctx.AppDB(), pid, source.ID, dest.ID); err != nil {
		_ = dbArchiveSite(ctx.AppDB(), pid, dest.ID)
		return nil, err
	}
	invalidatePageCacheForSite(dest.ID)
	ctx.Emit("site.cloned", map[string]any{"source_id": source.ID, "site_id": dest.ID})
	return map[string]any{"site": dest, "source_site": source}, nil
}

func cloneSiteData(db *sql.DB, pid string, sourceID, destID int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO settings(project_id,site_id,key,value,updated_at) SELECT project_id,?,key,value,updated_at FROM settings WHERE project_id=? AND site_id=? ON CONFLICT(project_id,site_id,key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, destID, pid, sourceID); err != nil {
		return err
	}
	mediaMap := map[int64]int64{}
	rows, err := tx.Query(`SELECT id,kind,storage_path,filename,mime,width,height,byte_size,alt,caption,source FROM media WHERE project_id=? AND site_id=?`, pid, sourceID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var old int64
		var kind, path, filename, mime, alt, caption, source string
		var width, height sql.NullInt64
		var size int64
		if err := rows.Scan(&old, &kind, &path, &filename, &mime, &width, &height, &size, &alt, &caption, &source); err != nil {
			rows.Close()
			return err
		}
		res, err := tx.Exec(`INSERT INTO media(project_id,site_id,kind,storage_path,filename,mime,width,height,byte_size,alt,caption,source) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, pid, destID, kind, path, filename, mime, width, height, size, alt, caption, source)
		if err != nil {
			rows.Close()
			return err
		}
		id, _ := res.LastInsertId()
		mediaMap[old] = id
	}
	rows.Close()
	type postRow struct {
		old                                                              int64
		kind, slug, locale, status, title, excerpt, blocks, html, author string
		featured, parent                                                 sql.NullInt64
		order                                                            int
		template, seoTitle, seoDesc, seoCanonical                        string
		og                                                               sql.NullInt64
		published, scheduled                                             sql.NullString
	}
	posts := []postRow{}
	rows, err = tx.Query(`SELECT id,kind,slug,locale,status,title,excerpt,body_blocks,body_html,author,featured_media_id,parent_id,menu_order,template,seo_title,seo_description,seo_canonical,og_image_media_id,published_at,scheduled_at FROM posts WHERE project_id=? AND site_id=? AND deleted_at IS NULL ORDER BY id`, pid, sourceID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p postRow
		if err := rows.Scan(&p.old, &p.kind, &p.slug, &p.locale, &p.status, &p.title, &p.excerpt, &p.blocks, &p.html, &p.author, &p.featured, &p.parent, &p.order, &p.template, &p.seoTitle, &p.seoDesc, &p.seoCanonical, &p.og, &p.published, &p.scheduled); err != nil {
			rows.Close()
			return err
		}
		posts = append(posts, p)
	}
	rows.Close()
	postMap := map[int64]int64{}
	for _, p := range posts {
		p.blocks = remapMediaIDs(p.blocks, mediaMap)
		var f, og any
		if p.featured.Valid {
			f = mediaMap[p.featured.Int64]
		}
		if p.og.Valid {
			og = mediaMap[p.og.Int64]
		}
		res, err := tx.Exec(`INSERT INTO posts(project_id,site_id,kind,slug,locale,status,title,excerpt,body_blocks,body_html,author,featured_media_id,parent_id,menu_order,template,seo_title,seo_description,seo_canonical,og_image_media_id,published_at,scheduled_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, pid, destID, p.kind, p.slug, p.locale, p.status, p.title, p.excerpt, p.blocks, p.html, p.author, f, nil, p.order, p.template, p.seoTitle, p.seoDesc, p.seoCanonical, og, p.published, p.scheduled)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		postMap[p.old] = id
	}
	for _, p := range posts {
		if p.parent.Valid {
			if _, err := tx.Exec(`UPDATE posts SET parent_id=? WHERE id=?`, postMap[p.parent.Int64], postMap[p.old]); err != nil {
				return err
			}
		}
	}
	type termRow struct {
		old                    int64
		kind, name, slug, desc string
		parent                 sql.NullInt64
	}
	terms := []termRow{}
	rows, err = tx.Query(`SELECT id,kind,name,slug,description,parent_id FROM terms WHERE project_id=? AND site_id=? ORDER BY id`, pid, sourceID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var t termRow
		if err := rows.Scan(&t.old, &t.kind, &t.name, &t.slug, &t.desc, &t.parent); err != nil {
			rows.Close()
			return err
		}
		terms = append(terms, t)
	}
	rows.Close()
	termMap := map[int64]int64{}
	for _, t := range terms {
		res, err := tx.Exec(`INSERT INTO terms(project_id,site_id,kind,name,slug,parent_id,description) VALUES(?,?,?,?,?,?,?)`, pid, destID, t.kind, t.name, t.slug, nil, t.desc)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		termMap[t.old] = id
	}
	for _, t := range terms {
		if t.parent.Valid {
			if _, err := tx.Exec(`UPDATE terms SET parent_id=? WHERE id=?`, termMap[t.parent.Int64], termMap[t.old]); err != nil {
				return err
			}
		}
	}
	rows, err = tx.Query(`SELECT post_id,term_id FROM post_terms WHERE post_id IN (SELECT id FROM posts WHERE project_id=? AND site_id=?)`, pid, sourceID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p, t int64
		if err := rows.Scan(&p, &t); err != nil {
			rows.Close()
			return err
		}
		if _, err := tx.Exec(`INSERT INTO post_terms(post_id,term_id) VALUES(?,?)`, postMap[p], termMap[t]); err != nil {
			rows.Close()
			return err
		}
	}
	rows.Close()
	type item struct {
		id               int64
		parent           sql.NullInt64
		label, kind, url string
		target           sql.NullInt64
		pos              int
	}
	menuMap := map[int64]int64{}
	rows, err = tx.Query(`SELECT id,slug,name FROM menus WHERE project_id=? AND site_id=?`, pid, sourceID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var old int64
		var slug, name string
		if err := rows.Scan(&old, &slug, &name); err != nil {
			rows.Close()
			return err
		}
		res, err := tx.Exec(`INSERT INTO menus(project_id,site_id,slug,name) VALUES(?,?,?,?)`, pid, destID, slug, name)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		menuMap[old] = id
	}
	rows.Close()
	for old, newID := range menuMap {
		items := []item{}
		rows, err = tx.Query(`SELECT id,parent_id,label,target_kind,target_id,target_url,position FROM menu_items WHERE menu_id=? ORDER BY position,id`, old)
		if err != nil {
			return err
		}
		for rows.Next() {
			var i item
			if err := rows.Scan(&i.id, &i.parent, &i.label, &i.kind, &i.target, &i.url, &i.pos); err != nil {
				rows.Close()
				return err
			}
			items = append(items, i)
		}
		rows.Close()
		itemMap := map[int64]int64{}
		for _, i := range items {
			var target any
			if i.target.Valid {
				target = postMap[i.target.Int64]
				if i.kind == "term" {
					target = termMap[i.target.Int64]
				}
			}
			res, err := tx.Exec(`INSERT INTO menu_items(menu_id,parent_id,label,target_kind,target_id,target_url,position) VALUES(?,?,?,?,?,?,?)`, newID, nil, i.label, i.kind, target, i.url, i.pos)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			itemMap[i.id] = id
		}
		for _, i := range items {
			if i.parent.Valid {
				if _, err := tx.Exec(`UPDATE menu_items SET parent_id=? WHERE id=?`, itemMap[i.parent.Int64], itemMap[i.id]); err != nil {
					return err
				}
			}
		}
	}
	if _, err = tx.Exec(`INSERT INTO redirects(project_id,site_id,from_path,to_path,code) SELECT project_id,?,from_path,to_path,code FROM redirects WHERE project_id=? AND site_id=?`, destID, pid, sourceID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO content_extensions(project_id,site_id,extension_key,provider_app,display_name,version,status,draft_manifest,published_manifest,published_at) SELECT project_id,?,extension_key,provider_app,display_name,version,status,draft_manifest,published_manifest,published_at FROM content_extensions WHERE project_id=? AND site_id=?`, destID, pid, sourceID); err != nil {
		return err
	}
	return tx.Commit()
}

func remapMediaIDs(raw string, mediaMap map[int64]int64) string {
	doc, err := parseDocument(raw)
	if err != nil {
		return raw
	}
	walkBlocks(doc.Blocks, func(block *Block) {
		for _, key := range []string{"media_id", "image_media_id"} {
			if id, ok := asInt64(block.Attrs[key]); ok {
				if next, exists := mediaMap[id]; exists {
					block.Attrs[key] = next
				}
			}
		}
		if values, ok := block.Attrs["media_ids"].([]any); ok {
			for i, value := range values {
				if id, ok := asInt64(value); ok {
					if next, exists := mediaMap[id]; exists {
						values[i] = next
					}
				}
			}
		}
	})
	encoded, err := encodeDocument(doc)
	if err != nil {
		return raw
	}
	return encoded
}
