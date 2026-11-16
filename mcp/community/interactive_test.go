package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type runnerPlatform struct {
	tk.BasePlatformClient
	calls []struct {
		app, tool string
		input     map[string]any
	}
}

func (p *runnerPlatform) CallAppResult(app, tool string, input map[string]any, output any) error {
	p.calls = append(p.calls, struct {
		app, tool string
		input     map[string]any
	}{app, tool, input})
	encoded, _ := json.Marshal(map[string]any{"ok": true, "echo": input["input"]})
	return json.Unmarshal(encoded, output)
}

const testElementHTML = `<!doctype html><html><head></head><body><script type="module">const s = await lesson.ready();</script></body></html>`

func mustElement(t *testing.T, ctx *sdk.AppCtx, communityID, slug string, runners []any) CourseElement {
	t.Helper()
	out, err := toolCourseElementsSave(ctx, map[string]any{
		"community_id": communityID, "slug": slug, "name": strings.ToUpper(slug), "html": testElementHTML, "runners": runners,
	})
	if err != nil {
		t.Fatal(err)
	}
	e := out.(CourseElement)
	if _, err := toolCourseElementsPublish(ctx, map[string]any{"id": e.ID}); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestElementVersionsAreImmutableAndBlocksPinThem(t *testing.T) {
	ctx, communityID, _, _, _, lessonID, _ := setupCourse(t)
	e := mustElement(t, ctx, communityID, "lab", nil)
	// Publishing an unchanged draft does not create a version.
	out, err := toolCourseElementsPublish(ctx, map[string]any{"id": e.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res := out.(map[string]any); res["created"] != false || res["version"] != int64(1) {
		t.Fatalf("unchanged publish = %+v", res)
	}
	if _, err := toolLessonBlocksSet(ctx, map[string]any{"lesson_id": lessonID, "layout": "stage", "blocks": []any{
		map[string]any{"type": "markdown", "slot": "side", "title": "Overview", "settings": map[string]any{"body": "# Hi"}},
		map[string]any{"type": "element", "slot": "stage", "element_id": e.ID, "required_steps": []any{"done"}},
	}}); err != nil {
		t.Fatal(err)
	}
	// Edit and publish v2: the block stays on v1 until upgraded.
	if _, err := toolCourseElementsSave(ctx, map[string]any{"id": e.ID, "html": testElementHTML + "<!-- v2 -->"}); err != nil {
		t.Fatal(err)
	}
	out, err = toolCourseElementsPublish(ctx, map[string]any{"id": e.ID})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["version"] != int64(2) {
		t.Fatalf("second publish = %+v", out)
	}
	blocks, _ := listLessonBlocks(ctx.AppDB(), lessonID)
	if len(blocks) != 2 || *blocks[1].ElementVersion != 1 {
		t.Fatalf("block should stay pinned to v1: %+v", blocks)
	}
	out, err = toolCourseElementsPublish(ctx, map[string]any{"id": e.ID, "upgrade_blocks": true})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["upgraded_blocks"] != int64(1) {
		t.Fatalf("upgrade = %+v", out)
	}
	src, err := toolLessonElementSource(ctx, map[string]any{"lesson_id": lessonID, "element_id": e.ID, "version": int64(2)})
	if err != nil || !strings.Contains(src.(map[string]any)["source"].(string), "v2") {
		t.Fatalf("source v2 = %v, %v", src, err)
	}
	if _, err := toolLessonElementSource(ctx, map[string]any{"lesson_id": lessonID, "element_id": e.ID, "version": int64(1)}); err == nil {
		t.Fatal("a version no block uses should not be served")
	}
}

func TestLessonBlocksValidation(t *testing.T) {
	ctx, communityID, _, _, _, lessonID, _ := setupCourse(t)
	draft, err := toolCourseElementsSave(ctx, map[string]any{"community_id": communityID, "slug": "draft-only", "name": "Draft", "html": testElementHTML})
	if err != nil {
		t.Fatal(err)
	}
	other := mustCreateCommunity(t, ctx, "other", "Other")
	foreign := mustElement(t, ctx, other.ID, "foreign", nil)
	for name, block := range map[string]map[string]any{
		"unknown type":      {"type": "iframe"},
		"bad slot":          {"type": "lesson_body", "slot": "header"},
		"empty markdown":    {"type": "markdown"},
		"unpublished":       {"type": "element", "element_id": draft.(CourseElement).ID},
		"other community":   {"type": "element", "element_id": foreign.ID},
		"steps on markdown": {"type": "markdown", "settings": map[string]any{"body": "x"}, "required_steps": []any{"a"}},
		"bad step":          {"type": "element", "element_id": foreign.ID, "required_steps": []any{"has space"}},
		"foreign block id":  {"id": "blk_nope", "type": "lesson_body"},
	} {
		if _, err := toolLessonBlocksSet(ctx, map[string]any{"lesson_id": lessonID, "blocks": []any{block}}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := toolLessonBlocksSet(ctx, map[string]any{"lesson_id": lessonID, "layout": "grid", "blocks": []any{}}); err == nil {
		t.Error("bad layout should fail")
	}
}

func TestDelegatedElementStateStepsCompleteLesson(t *testing.T) {
	ctx, communityID, memberID, courseID, _, lessonID, _ := setupCourse(t)
	if _, err := ctx.AppDB().Exec(`UPDATE members SET auth_user_id='auth-alice' WHERE id=?`, memberID); err != nil {
		t.Fatal(err)
	}
	e := mustElement(t, ctx, communityID, "lab", nil)
	out, err := toolLessonBlocksSet(ctx, map[string]any{"lesson_id": lessonID, "layout": "stage", "blocks": []any{
		map[string]any{"type": "element", "slot": "stage", "element_id": e.ID, "required_steps": []any{"a", "b"}},
		map[string]any{"type": "lesson_body"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	blockID := out.(map[string]any)["blocks"].([]LessonBlock)[0].ID
	user := userCallContext("auth-alice")
	save := delegatedTool(t, "element_state_save")
	step := delegatedTool(t, "lesson_step_complete")
	if _, err := save(user, ctx, map[string]any{"lesson_id": lessonID, "block_id": blockID, "state": map[string]any{"x": 1}}); err == nil || !strings.Contains(err.Error(), "enrollment") {
		t.Fatalf("unenrolled save should fail, got %v", err)
	}
	if _, err := toolCourseEnroll(ctx, map[string]any{"space_id": courseID, "member_id": memberID}); err != nil {
		t.Fatal(err)
	}
	if _, err := save(user, ctx, map[string]any{"lesson_id": lessonID, "block_id": blockID, "member_id": "spoofed", "state": map[string]any{"x": 1}}); err != nil {
		t.Fatal(err)
	}
	big := map[string]any{"x": strings.Repeat("a", elementStateLimit)}
	if _, err := save(user, ctx, map[string]any{"lesson_id": lessonID, "block_id": blockID, "state": big}); err == nil {
		t.Fatal("oversized state should fail")
	}
	res, err := step(user, ctx, map[string]any{"lesson_id": lessonID, "block_id": blockID, "step": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["lesson_completed"] != false {
		t.Fatalf("one of two steps should not complete: %+v", res)
	}
	res, err = step(user, ctx, map[string]any{"lesson_id": lessonID, "block_id": blockID, "step": "b"})
	if err != nil || res.(map[string]any)["lesson_completed"] != true {
		t.Fatalf("both steps should complete the lesson: %+v %v", res, err)
	}
	prog, ok, err := loadLessonProgress(ctx.AppDB(), lessonID, memberID)
	if err != nil || !ok || prog.Status != "complete" {
		t.Fatalf("progress = %+v %v %v", prog, ok, err)
	}
	bundle, err := delegatedTool(t, "lesson_bundle_get")(user, ctx, map[string]any{"id": lessonID})
	if err != nil {
		t.Fatal(err)
	}
	b := bundle.(map[string]any)
	if b["layout"] != "stage" || len(b["blocks"].([]LessonBlock)) != 2 {
		t.Fatalf("bundle layout/blocks = %v %v", b["layout"], b["blocks"])
	}
	if state := b["element_state"].(map[string]json.RawMessage)[blockID]; string(state) != `{"x":1}` {
		t.Fatalf("state = %s", state)
	}
	if steps := b["steps"].(map[string][]string)[blockID]; len(steps) != 2 {
		t.Fatalf("steps = %v", steps)
	}
	// Keeping the block id across a blocks_set preserves the member's state.
	if _, err := toolLessonBlocksSet(ctx, map[string]any{"lesson_id": lessonID, "blocks": []any{
		map[string]any{"id": blockID, "type": "element", "element_id": e.ID},
	}}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM element_state WHERE block_id=?`, blockID).Scan(&n)
	if n != 1 {
		t.Fatalf("state rows after re-set = %d", n)
	}
}

func TestDelegatedElementRunChecksRunnerAndBinding(t *testing.T) {
	platform := &runnerPlatform{}
	ctx, communityID, memberID, courseID, _, lessonID, _ := setupCourse(t, tk.WithPlatform(platform))
	if _, err := ctx.AppDB().Exec(`UPDATE members SET auth_user_id='auth-alice' WHERE id=?`, memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := toolCourseEnroll(ctx, map[string]any{"space_id": courseID, "member_id": memberID}); err != nil {
		t.Fatal(err)
	}
	e := mustElement(t, ctx, communityID, "board", []any{"arduino"})
	out, err := toolLessonBlocksSet(ctx, map[string]any{"lesson_id": lessonID, "blocks": []any{
		map[string]any{"type": "element", "element_id": e.ID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	blockID := out.(map[string]any)["blocks"].([]LessonBlock)[0].ID
	run := delegatedTool(t, "element_run")
	user := userCallContext("auth-alice")
	args := func(runner string) map[string]any {
		return map[string]any{"lesson_id": lessonID, "block_id": blockID, "runner": runner, "input": map[string]any{"source": "void setup(){}"}}
	}
	if _, err := run(user, ctx, args("python")); err == nil || !strings.Contains(err.Error(), "may not use") {
		t.Fatalf("undeclared runner should fail, got %v", err)
	}
	if _, err := run(user, ctx, args("arduino")); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("unbound runner should fail, got %v", err)
	}
	if _, err := toolElementRunnersSet(ctx, map[string]any{"community_id": communityID, "runners": []any{
		map[string]any{"name": "arduino", "app": "community", "tool": "x"},
	}}); err == nil {
		t.Fatal("binding a runner to community itself should fail")
	}
	if _, err := toolElementRunnersSet(ctx, map[string]any{"community_id": communityID, "runners": []any{
		map[string]any{"name": "arduino", "app": "pcb", "tool": "pcb_sketch_run"},
	}}); err != nil {
		t.Fatal(err)
	}
	res, err := run(user, ctx, args("arduino"))
	if err != nil {
		t.Fatal(err)
	}
	if len(platform.calls) != 1 || platform.calls[0].app != "pcb" || platform.calls[0].tool != "pcb_sketch_run" || platform.calls[0].input["element"] != "board" {
		t.Fatalf("calls = %+v", platform.calls)
	}
	if _, has := platform.calls[0].input["member_id"]; has {
		t.Fatal("runner input must not carry member identity")
	}
	if res.(map[string]any)["result"] == nil {
		t.Fatalf("result = %+v", res)
	}
	if _, err := delegatedTool(t, "element_runners_set")(user, ctx, map[string]any{"community_id": communityID, "runners": []any{}}); err == nil {
		t.Fatal("members must not change runner bindings")
	}
}

func TestStartersInstallAndCourseExportImportRoundTrip(t *testing.T) {
	ctx, communityID, _, courseID, _, lessonID, _ := setupCourse(t)
	out, err := toolCourseElementsInstallStarters(ctx, map[string]any{"community_id": communityID})
	if err != nil {
		t.Fatal(err)
	}
	installed := out.(map[string]any)["installed"].([]CourseElement)
	if len(installed) != 3 {
		t.Fatalf("installed = %d", len(installed))
	}
	again, _ := toolCourseElementsInstallStarters(ctx, map[string]any{"community_id": communityID})
	if len(again.(map[string]any)["skipped"].([]string)) != 3 {
		t.Fatalf("second install should skip all: %+v", again)
	}
	var lab CourseElement
	for _, e := range installed {
		if e.Slug == "finance-lab" {
			lab = e
		}
		if e.CurrentVersion != 1 {
			t.Fatalf("%s version = %d", e.Slug, e.CurrentVersion)
		}
	}
	if _, err := toolLessonBlocksSet(ctx, map[string]any{"lesson_id": lessonID, "layout": "stage", "blocks": []any{
		map[string]any{"type": "element", "slot": "stage", "element_id": lab.ID, "settings": map[string]any{"model": "growth"}, "required_steps": []any{"reached-target"}, "height": 520},
		map[string]any{"type": "markdown", "slot": "side", "title": "Overview", "settings": map[string]any{"body": "Grow it"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolQuizzesCreate(ctx, map[string]any{"lesson_id": lessonID, "title": "Check", "questions": []any{map[string]any{"prompt": "1+1?", "options": []string{"2", "3"}, "correct_index": 0}}}); err != nil {
		t.Fatal(err)
	}
	exported, err := toolCourseExport(ctx, map[string]any{"space_id": courseID})
	if err != nil {
		t.Fatal(err)
	}
	// Through JSON, as an agent or the panel would pass it.
	raw, _ := json.Marshal(exported)
	var pkg map[string]any
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	if len(pkg["elements"].([]any)) != 1 {
		t.Fatalf("export should carry only used elements: %d", len(pkg["elements"].([]any)))
	}
	res, err := toolCourseImport(ctx, map[string]any{"community_id": communityID, "package": pkg, "slug": "onboarding-copy", "name": "Copy"})
	if err != nil {
		t.Fatal(err)
	}
	r := res.(map[string]any)
	if r["lessons"] != 2 {
		t.Fatalf("import = %+v", r)
	}
	// Same checksum → the existing element version is reused, not duplicated.
	var versions int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM course_element_versions WHERE element_id=?`, lab.ID).Scan(&versions)
	if versions != 1 {
		t.Fatalf("versions after import = %d", versions)
	}
	var blockCount, quizCount int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM lesson_blocks b JOIN lessons l ON l.id=b.lesson_id JOIN sections s ON s.id=l.section_id WHERE s.space_id=? AND b.element_id=? AND b.height=520`, r["space_id"], lab.ID).Scan(&blockCount)
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM quizzes q JOIN lessons l ON l.id=q.lesson_id JOIN sections s ON s.id=l.section_id WHERE s.space_id=?`, r["space_id"]).Scan(&quizCount)
	if blockCount != 1 || quizCount != 1 {
		t.Fatalf("imported blocks=%d quizzes=%d", blockCount, quizCount)
	}
	// A tampered package is refused.
	pkg["elements"].([]any)[0].(map[string]any)["html"] = "<script>evil()</script>"
	if _, err := toolCourseImport(ctx, map[string]any{"community_id": communityID, "package": pkg, "slug": "tampered"}); err == nil {
		t.Fatal("checksum mismatch should fail")
	}
}
