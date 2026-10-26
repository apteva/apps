package main

import "testing"

func TestInstructorPersonaLinkPersistsAndClearsWithoutCourseReplacement(t *testing.T) {
	ctx, community, _, course, _, _, _ := setupCourse(t)
	out, err := toolInstructorProfilesCreate(ctx, map[string]any{"community_id": community, "display_name": "Marco", "persona_id": int64(3)})
	if err != nil {
		t.Fatal(err)
	}
	p := out.(InstructorProfile)
	if p.PersonaID != 3 {
		t.Fatal("persona lost", p)
	}
	if _, err := toolCourseInstructorsSet(ctx, map[string]any{"space_id": course, "instructor_ids": []any{p.ID}}); err != nil {
		t.Fatal(err)
	}
	out, err = toolInstructorProfilesUpdate(ctx, map[string]any{"id": p.ID, "persona_id": int64(0)})
	if err != nil || out.(InstructorProfile).PersonaID != 0 {
		t.Fatalf("clear: %+v %v", out, err)
	}
	got, err := loadInstructorProfile(ctx.AppDB(), p.ID)
	if err != nil || got.PersonaID != 0 {
		t.Fatal(got, err)
	}
	if _, err := toolCourseInstructorsGet(ctx, map[string]any{"space_id": course}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolInstructorProfilesUpdate(ctx, map[string]any{"id": p.ID, "persona_id": -1}); err == nil {
		t.Fatal("negative persona accepted")
	}
}
