package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

func TestClips4SalePreviewSlotIsolation(t *testing.T) {
	definition := realPerformerDefinition(t)
	for _, asset := range []struct {
		operation, kind, media, uploader, extension string
	}{
		{"set_clip_thumbnail", "thumbnail", "image", "thumb", "png"},
		{"set_clip_preview_video", "preview", "video", "preview", "mp4"},
	} {
		for _, visibility := range []string{"sfw", "nsfw"} {
			t.Run(asset.operation+"/"+visibility, func(t *testing.T) {
				selected, err := selectOperation(definition, asset.operation)
				if err != nil {
					t.Fatal(err)
				}
				def, err := renderActorDefinition(selected, map[string]any{
					"context_id": "test-context", "clip_id": "123", "expected_store_id": "456",
					"expected_store_name": "Test Store", "title": "Test Clip", "expected_saved_filename": "main.mp4",
					"visibility": visibility, "filename": "test." + asset.extension,
					"source_url": "https://example.com/media", "source_sha256": strings.Repeat("a", 64),
					"request_id": "test-request",
				})
				if err != nil {
					t.Fatal(err)
				}
				var opener, upload, copyGuard, savedSelector string
				for _, step := range def.Steps {
					switch step.Action {
					case "assert_element":
						copyGuard = step.Locator.Selector
					case "click":
						switch step.ExpectedEffect {
						case "navigation_only":
							opener = step.Locator.Selector
						case "immediate_external_commit":
							if step.ExpectedText != "Save and Exit" || step.OnceKey == "" {
								t.Fatalf("unexpected commit: %+v", step)
							}
						default:
							t.Fatalf("unguarded click: %+v", step)
						}
					case "upload_file":
						upload = strings.Replace(step.Locator.Selector,
							fmt.Sprintf(":is(#%s, #sfw_%s)", asset.uploader, asset.uploader),
							"[data-test-uploader]", 1)
						if !step.RequirePrepared || step.ExpectedSHA256 != strings.Repeat("a", 64) {
							t.Fatal("upload must use prepared bytes")
						}
					case "observe_page":
						if field, ok := step.Fields["saved_asset_path"]; ok {
							savedSelector = field.Selector
						}
					}
				}
				fixture := func(replacing, copied bool) *html.Node {
					t.Helper()
					slot := func(visibility string) string {
						prefix := "clip-" + asset.kind
						button := fmt.Sprintf(`<div data-testid="%s_%s_main-showcase-upload-preview-card"><button data-slot="%s">Upload</button></div>`, prefix, asset.media, visibility)
						if replacing && visibility == "nsfw" {
							button = fmt.Sprintf(`<button data-testid="%s_button_reupload" data-slot="%s">Replace</button>`, prefix, visibility)
						}
						if visibility == "nsfw" {
							disabled := ""
							if copied {
								disabled = " disabled"
							}
							button += fmt.Sprintf(`<button data-testid="%s_button_copy-preview"%s>Copy to SFW</button>`, prefix, disabled)
						}
						media := fmt.Sprintf(`<img data-testid="%s_image_main-showcase" src="https://example.com/%s.png" data-slot="%s">`, prefix, visibility, visibility)
						if asset.media == "video" {
							media = fmt.Sprintf(`<video data-testid="%s_video_main-showcase"><source src="https://example.com/%s.mp4" data-slot="%s"></video>`, prefix, visibility, visibility)
						}
						return fmt.Sprintf(`<div><div><div><span data-testid="%s_text_%s-title"></span></div></div><div>%s%s
							<div data-test-uploader><input type="file" name="files[]" data-slot="%s"><input type="file" name="files[]" webkitdirectory></div></div></div>`, prefix, visibility, media, button, visibility)
					}
					root, err := html.Parse(strings.NewReader("<body><div>" + slot("nsfw") + slot("sfw") + "</div></body>"))
					if err != nil {
						t.Fatal(err)
					}
					return root
				}
				for _, replacing := range []bool{false, true} {
					root := fixture(replacing, false)
					for _, selector := range []string{opener, upload, savedSelector} {
						matcher, err := cascadia.Compile(selector)
						if err != nil {
							t.Fatal(err)
						}
						matches := cascadia.QueryAll(root, matcher)
						if len(matches) != 1 {
							t.Fatalf("selector %q matched %d elements", selector, len(matches))
						}
						if slot, _ := htmlAttribute(matches[0], "data-slot"); slot != visibility {
							t.Fatalf("selector %q chose %q, want %q", selector, slot, visibility)
						}
					}
				}
				matcher, err := cascadia.Compile(copyGuard)
				if err != nil {
					t.Fatal(err)
				}
				if cascadia.Query(fixture(false, false), matcher) == nil {
					t.Fatal("independent assets failed the copy guard")
				}
				if allowed := cascadia.Query(fixture(false, true), matcher) != nil; allowed != (visibility == "sfw") {
					t.Fatalf("copy guard allowed=%v for %s", allowed, visibility)
				}
			})
		}
	}
}

func TestClips4SaleDraftIDWithCustomThumbnail(t *testing.T) {
	definition := realPerformerDefinition(t)
	for _, step := range definition.Operations["list_clips"].Steps {
		if step.ReadViews == nil {
			continue
		}
		for _, view := range step.ReadViews.Views {
			if view.Name != "drafts" {
				continue
			}
			root, err := html.Parse(strings.NewReader(`<img data-testid="clip_[0]_image_thumb" src="https://imagecdn.clips4sale.com/accounts99/572201/clip_images/rsz/rect_sm/clip_image_34928667_1793882277.jpg?ts=fixture">`))
			if err != nil {
				t.Fatal(err)
			}
			fields := map[string]actorField{"clip_id": view.Fields["clip_id"]}
			got, err := extractNodeItem(root, fields, "https://workspace.clips4sale.com/content/drafts")
			if err != nil {
				t.Fatal(err)
			}
			if got["clip_id"] != "34928667" {
				t.Fatalf("unexpected clip ID: %v", got["clip_id"])
			}
			return
		}
	}
	t.Fatal("drafts view missing from list_clips")
}

func TestClips4SaleThumbnailVerificationUsesExactNativeFilename(t *testing.T) {
	def := realPerformerDefinition(t)
	selected, err := selectOperation(def, "set_clip_thumbnail")
	if err != nil {
		t.Fatal(err)
	}
	for _, visibility := range []string{"sfw", "nsfw"} {
		t.Run(visibility, func(t *testing.T) {
			op, err := renderActorDefinition(selected, map[string]any{
				"context_id": "saved", "clip_id": "34928667", "expected_store_id": "572201",
				"expected_store_name": "Test", "title": "Test", "expected_saved_filename": "main.mp4",
				"visibility": visibility, "filename": "test.jpg", "source_url": "https://example.com/test.jpg",
				"source_sha256": strings.Repeat("a", 64), "request_id": "test",
			})
			if err != nil {
				t.Fatal(err)
			}
			var staged actorField
			for _, step := range op.Steps {
				if f, ok := step.Fields["staged_thumbnail_filename"]; ok {
					staged = f
				}
			}
			prefix := ""
			if visibility == "sfw" {
				prefix = "sfw_"
			}
			filename := prefix + "clip_image_34928667_1793882267.jpg"
			src := "https://imagecdn.clips4sale.com/accounts99/572201/clip_images/" + filename + "?ts=123"
			root, err := html.Parse(strings.NewReader(`<img src="` + src + `">`))
			if err != nil {
				t.Fatal(err)
			}
			staged.Selector = "img"
			item, err := extractNodeItem(root, map[string]actorField{"staged_thumbnail_filename": staged}, src)
			if err != nil || item["staged_thumbnail_filename"] != filename {
				t.Fatalf("staged filename proof failed: %v, %v", item, err)
			}
			receipt := op.Steps[len(op.Steps)-2].JSONDocument
			native, err := extractJSONDocument(receipt, `{"data":{"id":34928667,"store_id":572201,"status":"draft","title":"Test","thumbnail":"clip_image_34928667_1793882267.jpg","sfw_thumbnail":"sfw_clip_image_34928667_1793882267.jpg"}}`)
			if err != nil {
				t.Fatal(err)
			}
			native["document_url"] = op.Steps[len(op.Steps)-3].URL
			native["staged_thumbnail_filename"] = filename
			e := &actorExecution{lastValues: native}
			proof := op.Steps[len(op.Steps)-1]
			if err := e.assertValues(proof); err != nil {
				t.Fatal(err)
			}
			native["native_thumbnail_"+visibility] = prefix + "clip_image_34928667_123.jpg"
			if err := e.assertValues(proof); err == nil {
				t.Fatal("stale thumbnail accepted")
			}
		})
	}
}
