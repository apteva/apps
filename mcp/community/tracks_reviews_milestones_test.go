package main

import (
	"strings"
	"testing"
)

func TestTracksReviewsAndMilestones(t *testing.T) {
	ctx, rec := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	member := mustCreateMember(t, ctx, c.ID, "alice")
	course := mustCreateSpace(t, ctx, c.ID, "blueprint", "course")
	section, err := toolSectionsCreate(ctx, map[string]any{"space_id": course.ID, "title": "Roadmap"})
	if err != nil {
		t.Fatal(err)
	}
	lessonOut, err := toolLessonsCreate(ctx, map[string]any{"section_id": section.(Section).ID, "title": "Offer", "body": "body"})
	if err != nil {
		t.Fatal(err)
	}
	lesson := lessonOut.(Lesson)
	if _, err = toolLessonsPublish(ctx, map[string]any{"id": lesson.ID, "published": true}); err != nil {
		t.Fatal(err)
	}
	trackOut, err := toolCourseTracksCreate(ctx, map[string]any{"space_id": course.ID, "slug": "existing-clients", "name": "Existing Clients"})
	if err != nil {
		t.Fatal(err)
	}
	track := trackOut.(CourseTrack)
	if _, err = toolCourseTrackLessonsSet(ctx, map[string]any{"lesson_id": lesson.ID, "track_ids": []any{track.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err = toolCourseEnroll(ctx, map[string]any{"space_id": course.ID, "member_id": member.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = toolCourseTrackSelect(ctx, map[string]any{"space_id": course.ID, "track_id": track.ID, "member_id": member.ID}); err != nil {
		t.Fatal(err)
	}
	tracks, err := toolCourseTracksList(ctx, map[string]any{"space_id": course.ID, "member_id": member.ID})
	if err != nil || tracks.(map[string]any)["selected_track_id"] != track.ID {
		t.Fatalf("selected track: %#v %v", tracks, err)
	}
	assignmentOut, err := toolAssignmentsCreate(ctx, map[string]any{"lesson_id": lesson.ID, "title": "Validate offer"})
	if err != nil {
		t.Fatal(err)
	}
	assignment := assignmentOut.(Assignment)
	sub, err := toolAssignmentSubmit(ctx, map[string]any{"assignment_id": assignment.ID, "member_id": member.ID, "body": "validated", "links": []any{"https://example.test/evidence"}})
	if err != nil {
		t.Fatal(err)
	}
	if sub.(AssignmentSubmission).Status != "submitted" || sub.(AssignmentSubmission).Version != 1 {
		t.Fatalf("submission: %#v", sub)
	}
	if _, err = toolAssignmentReview(ctx, map[string]any{"assignment_id": assignment.ID, "member_id": member.ID, "status": "needs_changes", "feedback": "Add the target customer."}); err != nil {
		t.Fatal(err)
	}
	queue, err := toolAssignmentReviewsList(ctx, map[string]any{"space_id": course.ID, "status": "needs_changes"})
	if err != nil || len(queue.(map[string]any)["submissions"].([]AssignmentReviewItem)) != 1 {
		t.Fatalf("review queue: %#v %v", queue, err)
	}
	milestoneOut, err := toolMilestonesCreate(ctx, map[string]any{"space_id": course.ID, "title": "Offer validated", "requires_approval": true, "evidence_type": "link"})
	if err != nil {
		t.Fatal(err)
	}
	milestone := milestoneOut.(MilestoneDefinition)
	if _, err = toolMilestoneSubmit(ctx, map[string]any{"definition_id": milestone.ID, "member_id": member.ID, "evidence_links": []any{"https://example.test/offer"}}); err != nil {
		t.Fatal(err)
	}
	list, err := toolMilestonesList(ctx, map[string]any{"space_id": course.ID, "member_id": member.ID})
	if err != nil {
		t.Fatal(err)
	}
	items := list.(map[string]any)["milestones"].([]MemberMilestone)
	if len(items) != 1 || items[0].Status != "submitted" {
		t.Fatalf("milestones: %#v", items)
	}
	if _, err = toolMilestoneReview(ctx, map[string]any{"definition_id": milestone.ID, "member_id": member.ID, "status": "approved"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.WaitForTopic("assignment.submitted", 100000000); !ok {
		t.Fatal("assignment.submitted event missing")
	}
	if _, ok := rec.WaitForTopic("assignment.reviewed", 100000000); !ok {
		t.Fatal("assignment.reviewed event missing")
	}
	if _, ok := rec.WaitForTopic("milestone.achieved", 100000000); !ok {
		t.Fatal("milestone.achieved event missing")
	}
	if _, ok := rec.WaitForTopic("milestone.reviewed", 100000000); !ok {
		t.Fatal("milestone.reviewed event missing")
	}
	if _, err := toolCourseTrackSelect(ctx, map[string]any{"space_id": course.ID, "track_id": "missing", "member_id": member.ID}); err == nil || !strings.Contains(err.Error(), "track") {
		t.Fatal("missing track accepted")
	}
}
