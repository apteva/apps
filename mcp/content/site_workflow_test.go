package main

import (
	"testing"
	"time"
)

func TestCloneSiteCopiesBrandingAndContent(t *testing.T) {
	db := hardeningTestDB(t)
	source, err := dbCreateSite(db, "clone-project", "source", "Source", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbCreateSite(db, "clone-project", "other", "Other", ""); err != nil {
		t.Fatal(err)
	}
	if err := dbSetSetting(db, "clone-project", source.ID, "brand_primary_color", "#123456"); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO posts(project_id,site_id,kind,slug,status,title,body_blocks) VALUES(?,?,?,?,?,?,?)`, "clone-project", source.ID, "page", "home", "published", "Home", `{"version":1,"blocks":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO menus(project_id,site_id,slug,name) VALUES(?,?,?,?)`, "clone-project", source.ID, "primary", "Primary")
	if err != nil {
		t.Fatal(err)
	}
	menuID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO menu_items(menu_id,label,target_kind,target_id,target_url,position) VALUES(?,?,?,?,?,?)`, menuID, "Home", "page", postID, "", 0); err != nil {
		t.Fatal(err)
	}
	dest, err := dbCreateSite(db, "clone-project", "copy", "Copy", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cloneSiteData(db, "clone-project", source.ID, dest.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := dbGetSetting(db, "clone-project", dest.ID, "brand_primary_color"); got != "#123456" {
		t.Fatalf("branding = %q", got)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM posts WHERE project_id=? AND site_id=? AND slug='home'`, "clone-project", dest.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("cloned posts=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM menu_items mi JOIN menus m ON m.id=mi.menu_id WHERE m.project_id=? AND m.site_id=?`, "clone-project", dest.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("cloned menu items=%d err=%v", count, err)
	}
}

func TestBrandCSSRejectsUnsafeValues(t *testing.T) {
	css := brandCSS(map[string]string{"brand_primary_color": "#123456", "brand_secondary_color": "red;body{display:none}"})
	if css != ":root{--accent:#123456}" {
		t.Fatalf("css = %q", css)
	}
}

func TestPreviewLinksCanBeRevoked(t *testing.T) {
	db := hardeningTestDB(t)
	site, err := dbCreateSite(db, "preview-project", "main", "Main", "")
	if err != nil {
		t.Fatal(err)
	}
	token := signSitePreview(site.ID, 24*60*60*1e9)
	if err := recordPreviewLink(db, "preview-project", site.ID, token, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !previewLinkActive(db, "preview-project", site.ID, token) {
		t.Fatal("preview link should be active")
	}
	if err := revokePreviewLink(db, "preview-project", token); err != nil {
		t.Fatal(err)
	}
	if previewLinkActive(db, "preview-project", site.ID, token) {
		t.Fatal("preview link should be revoked")
	}
}

func TestTemplateExportDropsSiteLocalMediaIDs(t *testing.T) {
	blocks := []Block{{Type: "core/image", Attrs: map[string]any{"media_id": int64(4), "alt": "hero"}, Inner: []Block{{Type: "core/gallery", Attrs: map[string]any{"media_ids": []any{int64(4)}}}}}}
	stripTemplateMediaIDs(blocks)
	if _, ok := blocks[0].Attrs["media_id"]; ok {
		t.Fatal("media id retained")
	}
	if _, ok := blocks[0].Inner[0].Attrs["media_ids"]; ok {
		t.Fatal("nested media ids retained")
	}
}
