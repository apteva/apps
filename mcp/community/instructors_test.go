package main

import (
	"strings"
	"testing"
)

func TestInstructorProfilesOneTableAssignmentsAndCalculatedStatistics(t *testing.T) {
	ctx, communityID, memberID, courseID, _, _, _ := setupCourse(t)
	primaryOut, err := toolInstructorProfilesCreate(ctx, map[string]any{
		"community_id": communityID, "member_id": memberID, "display_name": "Ada Maker",
		"professional_title": "Embedded systems educator", "sales_bio": "Builds practical connected devices.",
		"credentials": []any{"MSc Engineering"}, "accomplishments": []any{"Taught 10,000 learners"},
		"links":          []any{map[string]any{"label": "Website", "url": "https://example.test/ada"}},
		"public_visible": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondaryOut, err := toolInstructorProfilesCreate(ctx, map[string]any{
		"community_id": communityID, "display_name": "Grace Builder", "public_visible": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	primary := primaryOut.(InstructorProfile)
	secondary := secondaryOut.(InstructorProfile)

	if _, err := toolCourseEnroll(ctx, map[string]any{"space_id": courseID, "member_id": memberID}); err != nil {
		t.Fatal(err)
	}
	setOut, err := toolCourseInstructorsSet(ctx, map[string]any{
		"space_id": courseID, "instructor_ids": []any{secondary.ID, primary.ID}, "primary_instructor_id": primary.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	setResult := setOut.(map[string]any)
	profiles := setResult["instructors"].([]InstructorProfile)
	if len(profiles) != 2 || profiles[0].ID != secondary.ID || profiles[1].ID != primary.ID {
		t.Fatalf("ordered instructors=%+v", profiles)
	}
	if profiles[1].Statistics == nil || profiles[1].Statistics.CoursesTaught != 1 || profiles[1].Statistics.PublishedLessons != 2 || profiles[1].Statistics.ActiveStudents != 1 {
		t.Fatalf("calculated statistics=%+v", profiles[1].Statistics)
	}

	public, err := publicInstructorsForCourse(ctx.AppDB(), courseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(public) != 1 || public[0].ID != primary.ID || !public[0].Primary || public[0].DisplayName != "Ada Maker" {
		t.Fatalf("public instructors=%+v", public)
	}
	if len(public[0].Credentials) != 1 || len(public[0].Links) != 1 || public[0].Statistics.PublishedLessons != 2 {
		t.Fatalf("public profile content=%+v", public[0])
	}

	if _, err := toolInstructorProfilesArchive(ctx, map[string]any{"id": primary.ID}); err == nil || !strings.Contains(err.Error(), "assigned") {
		t.Fatalf("assigned instructor archived without force: %v", err)
	}
	if _, err := toolInstructorProfilesArchive(ctx, map[string]any{"id": primary.ID, "force": true}); err != nil {
		t.Fatal(err)
	}
	after, err := toolCourseInstructorsGet(ctx, map[string]any{"space_id": courseID})
	if err != nil {
		t.Fatal(err)
	}
	afterResult := after.(map[string]any)
	afterProfiles := afterResult["instructors"].([]InstructorProfile)
	if len(afterProfiles) != 1 || afterProfiles[0].ID != secondary.ID {
		t.Fatalf("force archive did not clean assignment: %+v", afterResult)
	}
	primaryID, _ := afterResult["primary_instructor_id"].(*string)
	if primaryID == nil || *primaryID != secondary.ID {
		t.Fatalf("remaining instructor was not promoted: %+v", afterResult["primary_instructor_id"])
	}
}

func TestInstructorProfilesValidateLinksAndCommunityAssignments(t *testing.T) {
	ctx, _ := newTestCtx(t)
	communityA := mustCreateCommunity(t, ctx, "community-a", "A")
	communityB := mustCreateCommunity(t, ctx, "community-b", "B")
	course := mustCreateSpace(t, ctx, communityA.ID, "course", "course")
	if _, err := toolInstructorProfilesCreate(ctx, map[string]any{
		"community_id": communityA.ID, "display_name": "Bad Link",
		"links": []any{map[string]any{"url": "javascript:alert(1)"}},
	}); err == nil {
		t.Fatal("unsafe instructor link accepted")
	}
	otherOut, err := toolInstructorProfilesCreate(ctx, map[string]any{"community_id": communityB.ID, "display_name": "Other"})
	if err != nil {
		t.Fatal(err)
	}
	other := otherOut.(InstructorProfile)
	if _, err := toolCourseInstructorsSet(ctx, map[string]any{"space_id": course.ID, "instructor_ids": []any{other.ID}}); err == nil {
		t.Fatal("cross-community instructor assignment accepted")
	}
}

func TestPublicInstructorFallsBackToLegacyCourseName(t *testing.T) {
	ctx, _, _, courseID, _, _, _ := setupCourse(t)
	if _, err := toolCoursesUpdateDetails(ctx, map[string]any{"space_id": courseID, "instructor_name": "Legacy Instructor"}); err != nil {
		t.Fatal(err)
	}
	public, err := publicInstructorsForCourse(ctx.AppDB(), courseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(public) != 1 || public[0].DisplayName != "Legacy Instructor" || !public[0].Primary {
		t.Fatalf("legacy fallback=%+v", public)
	}
}
