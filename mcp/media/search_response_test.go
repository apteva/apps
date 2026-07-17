package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSearchMedia_TextFilters(t *testing.T) {
	ctx := newTestCtx(t)
	rows := []struct {
		id, name, title, description, alt string
	}{
		{"1", "course Part 3.mp4", "", "", ""},
		{"2", "workshop.mp4", "Part 3 Workshop", "", ""},
		{"3", "discussion.mp4", "", "A discussion covering Part 3", ""},
		{"4", "demo.mp4", "", "", "Screen recording for part 3"},
	}
	for _, row := range rows {
		if err := upsertMedia(ctx.AppDB(), testProj, row.id, sampleVideoProbe(), "sha-"+row.id, "/course/", row.name); err != nil {
			t.Fatal(err)
		}
		if row.title != "" || row.description != "" || row.alt != "" {
			title, description, alt := row.title, row.description, row.alt
			if _, err := setDescription(ctx.AppDB(), testProj, row.id, DescriptionFields{
				Title: &title, Description: &description, AltText: &alt,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	got, err := searchMedia(ctx.AppDB(), testProj, SearchFilters{Q: "PART 3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("q should search filename/title/description/alt text; got %d rows", len(got))
	}
	got, err = searchMedia(ctx.AppDB(), testProj, SearchFilters{Filename: "part 3"})
	if err != nil || len(got) != 1 || got[0].FileID != "1" {
		t.Fatalf("filename filter = %+v, %v", got, err)
	}
	got, err = searchMedia(ctx.AppDB(), testProj, SearchFilters{Title: "part 3"})
	if err != nil || len(got) != 1 || got[0].FileID != "2" {
		t.Fatalf("title filter = %+v, %v", got, err)
	}
}

func TestSearchMedia_TextFilterTreatsLIKEWildcardsLiterally(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "percent", sampleVideoProbe(), "a", "/", "100%-complete.mp4")
	upsertMedia(ctx.AppDB(), testProj, "plain", sampleVideoProbe(), "b", "/", "100x-complete.mp4")

	got, err := searchMedia(ctx.AppDB(), testProj, SearchFilters{Q: "100%"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FileID != "percent" {
		t.Fatalf("literal wildcard search returned %+v", got)
	}
}

func TestToolSearch_DefaultCompactAndDetailOptIn(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "42", sampleVideoProbe(), "source-sha", "/indexed/", "indexed-name.mp4"); err != nil {
		t.Fatal(err)
	}
	if err := upsertDerivation(ctx.AppDB(), testProj, "42", "thumbnail", 420, 320, 180, 0); err != nil {
		t.Fatal(err)
	}
	_, cleanup := newFakeStorage(t, []StorageFile{
		{ID: 42, Name: "current-name.mp4", Folder: "/current/", URL: "https://example.test/source"},
		{ID: 420, Name: "42.jpg", Folder: "/.media/thumbnail/", ContentType: "image/jpeg", Source: "media-derivation", URL: "https://example.test/thumb"},
	})
	defer cleanup()

	app := &App{}
	out, err := app.toolSearch(ctx, map[string]any{"_project_id": testProj})
	if err != nil {
		t.Fatal(err)
	}
	compact := out.(map[string]any)["media"].([]MediaSearchRow)
	if len(compact) != 1 {
		t.Fatalf("compact rows=%d", len(compact))
	}
	row := compact[0]
	if row.Filename != "current-name.mp4" || row.Folder != "/current/" || row.MediaType != "video" {
		t.Fatalf("compact row missing discovery fields: %+v", row)
	}
	if row.Thumbnail == nil || row.Thumbnail.StorageFileID != "420" || row.Thumbnail.URL != "https://example.test/thumb" {
		t.Fatalf("compact thumbnail = %+v", row.Thumbnail)
	}
	encoded, _ := json.Marshal(row)
	for _, forbidden := range []string{"source_sha256", "probe_status", "derivations", "video_codec"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("compact row leaked %q: %s", forbidden, encoded)
		}
	}

	out, err = app.toolSearch(ctx, map[string]any{"_project_id": testProj, "detail": true})
	if err != nil {
		t.Fatal(err)
	}
	detail := out.(map[string]any)["media"].([]MediaResponseRow)
	if len(detail) != 1 || detail[0].SourceSHA256 != "source-sha" || detail[0].RawProbe != nil {
		t.Fatalf("detail row = %+v", detail)
	}

	out, err = app.toolSearch(ctx, map[string]any{"_project_id": testProj, "include_raw_probe": true})
	if err != nil {
		t.Fatal(err)
	}
	withRaw := out.(map[string]any)["media"].([]MediaResponseRow)
	if len(withRaw) != 1 || withRaw[0].RawProbe == nil {
		t.Fatalf("include_raw_probe should imply detail: %+v", withRaw)
	}
}

func TestToolSearch_DefaultLimitAndCursor(t *testing.T) {
	ctx := newTestCtx(t)
	storageFiles := make([]StorageFile, 0, 25)
	for i := 1; i <= 25; i++ {
		id := fmt.Sprintf("%d", i)
		if err := upsertMedia(ctx.AppDB(), testProj, id, sampleVideoProbe(), "sha", "/episodes/", "episode-"+id+".mp4"); err != nil {
			t.Fatal(err)
		}
		storageFiles = append(storageFiles, StorageFile{ID: int64(i), Name: "episode-" + id + ".mp4", Folder: "/episodes/"})
	}
	_, cleanup := newFakeStorage(t, storageFiles)
	defer cleanup()

	app := &App{}
	firstAny, err := app.toolSearch(ctx, map[string]any{"_project_id": testProj, "q": "episode"})
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(map[string]any)
	firstRows := first["media"].([]MediaSearchRow)
	if len(firstRows) != mediaSearchDefaultLimit || first["has_more"] != true {
		t.Fatalf("first page = rows:%d response:%+v", len(firstRows), first)
	}
	cursor, _ := first["next_cursor"].(string)
	if cursor == "" {
		t.Fatal("first page missing next_cursor")
	}

	secondAny, err := app.toolSearch(ctx, map[string]any{"_project_id": testProj, "q": "episode", "cursor": cursor})
	if err != nil {
		t.Fatal(err)
	}
	second := secondAny.(map[string]any)
	secondRows := second["media"].([]MediaSearchRow)
	if len(secondRows) != 5 || second["has_more"] != false {
		t.Fatalf("second page = rows:%d response:%+v", len(secondRows), second)
	}
	seen := map[string]bool{}
	for _, row := range firstRows {
		seen[row.FileID] = true
	}
	for _, row := range secondRows {
		if seen[row.FileID] {
			t.Fatalf("cursor repeated file_id %s", row.FileID)
		}
	}
}

func TestFitMediaSearchPage_EnforcesSerializedBudget(t *testing.T) {
	type largeRow struct {
		ID   int    `json:"id"`
		Text string `json:"text"`
	}
	items := make([]largeRow, 100)
	for i := range items {
		items[i] = largeRow{ID: i, Text: strings.Repeat("x", 2048)}
	}
	page, err := fitMediaSearchPage(items, 0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > mediaSearchMaxResponseBytes {
		t.Fatalf("response is %d bytes, limit %d", len(encoded), mediaSearchMaxResponseBytes)
	}
	returned := page["returned"].(int)
	if returned <= 0 || returned >= len(items) || page["response_truncated"] != true || page["has_more"] != true {
		t.Fatalf("budget did not truncate safely: %+v", page)
	}
	offset, err := decodeMediaSearchCursor(page["next_cursor"].(string))
	if err != nil || offset != returned {
		t.Fatalf("cursor offset=%d err=%v, returned=%d", offset, err, returned)
	}
}

func TestMediaSearchCursorValidation(t *testing.T) {
	if _, err := decodeMediaSearchCursor("not-a-cursor"); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	cursor := encodeMediaSearchCursor(12)
	if got, err := mediaSearchOffset(map[string]any{"cursor": cursor}); err != nil || got != 12 {
		t.Fatalf("cursor decoded to %d, %v", got, err)
	}
	if _, err := mediaSearchOffset(map[string]any{"cursor": cursor, "offset": 3}); err == nil {
		t.Fatal("cursor + offset should be rejected")
	}
}

func TestMediaSearchToolDescriptionGuidesTwoStepFlow(t *testing.T) {
	var found bool
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name != "media_search" {
			continue
		}
		found = true
		description := strings.ToLower(tool.Description)
		for _, term := range []string{"q", "compact", "media_get", "next_cursor", "default limit is 20"} {
			if !strings.Contains(description, term) {
				t.Errorf("media_search description missing %q: %s", term, tool.Description)
			}
		}
		properties := tool.InputSchema["properties"].(map[string]any)
		for _, name := range []string{"q", "filename", "title", "cursor", "detail", "include_raw_probe"} {
			if _, ok := properties[name]; !ok {
				t.Errorf("media_search schema missing %q", name)
			}
		}
	}
	if !found {
		t.Fatal("media_search tool missing")
	}
}
