package main

import "testing"

func videoUploadRule() []any {
	return []any{map[string]any{
		"instruction_kind": "audio",
		"response": map[string]any{
			"note": map[string]any{"enabled": true, "required": false},
			"files": map[string]any{
				"enabled":     true,
				"required":    true,
				"accept":      []any{"video/*"},
				"min_items":   1,
				"max_items":   1,
				"max_size_mb": 2048,
			},
		},
	}}
}

func TestTemplateResponseRuleAppliesToFixedComposition(t *testing.T) {
	ctx := testCtx(t)
	app := &App{}
	audioOut, err := app.toolInstructionsCreate(ctx, map[string]any{
		"_project_id": "project-a",
		"name":        "Recording audio",
		"kind":        "audio",
		"body":        map[string]any{"storage_file_id": 91},
	})
	if err != nil {
		t.Fatal(err)
	}
	audio := audioOut.(map[string]any)["instruction"].(*instruction)
	templateOut, err := app.toolTemplatesCreate(ctx, map[string]any{
		"_project_id":    "project-a",
		"name":           "Recording template",
		"title_template": "Recording",
		"response_rules": videoUploadRule(),
	})
	if err != nil {
		t.Fatal(err)
	}
	tpl := templateOut.(map[string]any)["template"].(*template)
	updated, err := app.toolTemplatesSetInstructions(ctx, map[string]any{
		"_project_id": "project-a",
		"id":          tpl.ID,
		"instructions": []any{map[string]any{
			"instruction_id": audio.ID,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tpl = updated.(map[string]any)["template"].(*template)
	if got := responseSpecFromBody(tpl.CurrentVersion.Composition[0].Body); !got.Files.Required || len(got.Files.Accept) != 1 || got.Files.Accept[0] != "video/*" || got.Files.MaxItems != 1 {
		t.Fatalf("effective response=%+v", got)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE template_versions SET status='active' WHERE id=?`, tpl.CurrentVersion.ID); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolGigsCreateFromTemplate(ctx, map[string]any{"_project_id": "project-a", "template_id": tpl.ID})
	if err != nil {
		t.Fatal(err)
	}
	g := out.(map[string]any)["gig"].(*gig)
	if got := responseSpecFromBody(g.Composition[0].RenderedBody); !got.Files.Required || got.Files.MinItems != 1 || got.Files.MaxItems != 1 {
		t.Fatalf("snapshot response=%+v", got)
	}
}

func TestDynamicInstructionsInheritTemplateResponseRules(t *testing.T) {
	ctx := testCtx(t)
	app := &App{}
	audioOut, err := app.toolInstructionsCreate(ctx, map[string]any{
		"_project_id": "project-a", "name": "Audio", "kind": "audio", "body": map[string]any{"storage_file_id": 91},
	})
	if err != nil {
		t.Fatal(err)
	}
	textOut, err := app.toolInstructionsCreate(ctx, map[string]any{
		"_project_id": "project-a", "name": "Read", "kind": "text", "body": map[string]any{"markdown": "No upload"},
	})
	if err != nil {
		t.Fatal(err)
	}
	audio := audioOut.(map[string]any)["instruction"].(*instruction)
	read := textOut.(map[string]any)["instruction"].(*instruction)
	templateOut, err := app.toolTemplatesCreate(ctx, map[string]any{
		"_project_id": "project-a", "name": "Dynamic recording", "title_template": "Unused", "response_rules": videoUploadRule(),
	})
	if err != nil {
		t.Fatal(err)
	}
	tpl := templateOut.(map[string]any)["template"].(*template)
	if _, err := ctx.AppDB().Exec(`UPDATE template_versions SET status='active' WHERE id=?`, tpl.CurrentVersion.ID); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolGigsCreateFromInstructions(ctx, map[string]any{
		"_project_id": "project-a",
		"template_id": tpl.ID,
		"title":       "Dynamic recording",
		"instructions": []any{
			map[string]any{"instruction_id": read.ID},
			map[string]any{"instruction_id": audio.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	g := out.(map[string]any)["gig"].(*gig)
	if g.TemplateVersionID != tpl.CurrentVersion.ID {
		t.Fatalf("template_version_id=%d want %d", g.TemplateVersionID, tpl.CurrentVersion.ID)
	}
	if got := responseSpecFromBody(g.Composition[0].RenderedBody); got.Note.Enabled || got.Files.Enabled {
		t.Fatalf("text instruction unexpectedly got response=%+v", got)
	}
	if got := responseSpecFromBody(g.Composition[1].RenderedBody); !got.Files.Required || got.Files.MinItems != 1 || got.Files.MaxItems != 1 || len(got.Files.Accept) != 1 || got.Files.Accept[0] != "video/*" {
		t.Fatalf("audio response=%+v", got)
	}
}

func TestTemplateResponseRulesRejectStructuredInputKindsAndDuplicates(t *testing.T) {
	for _, rules := range [][]any{
		{map[string]any{"instruction_kind": "input_short_text", "response": map[string]any{"note": map[string]any{"enabled": true}}}},
		{
			map[string]any{"instruction_kind": "audio", "response": map[string]any{"files": map[string]any{"enabled": true}}},
			map[string]any{"instruction_kind": "audio", "response": map[string]any{"files": map[string]any{"enabled": true}}},
		},
	} {
		if _, err := templateResponseRulesArg(map[string]any{"response_rules": rules}); err == nil {
			t.Fatalf("invalid rules accepted: %#v", rules)
		}
	}
}
