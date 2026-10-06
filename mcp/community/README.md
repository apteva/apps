# Community v0.14.0

Community provides a branded member portal, discussions, direct messages, courses, a public storefront, and one-time or recurring course access. The native dashboard panel manages communities, members, content, offers, instructors, and branding.

## Version 0.13.0

This release fixes resource-based delegated authorization, global-install HTTP project scoping, default member listing, same-second DM unread counts, lesson-bundle progress identity, empty conversations, and thread activity after post removal. It preserves the storefront, payments, memberships, instructor profiles, previews, and custom domains from 0.12.3.

The member portal now renders safe Markdown, plays storage-backed lesson videos, opens protected resources, grades and saves multiple-choice quizzes, saves editable text assignment submissions, displays printable earned certificates, and edits member profiles. Directories and conversations support additional pages. Load failures have visible errors and retry controls; mobile navigation is hidden from keyboard focus when closed.

This release adds track-aware roadmaps, instructor assignment review, and student milestones. Lessons with no track assignment are shared; assigning a lesson to one or more tracks limits it to members who selected one of those tracks. Switching tracks preserves the member's saved lesson progress. Assignment submissions accept text, links, and Storage file IDs, retain versions, and move through `submitted`, `needs_changes`, and `approved` with instructor feedback. Milestones support evidence and optional instructor approval and emit `milestone.achieved` and `milestone.reviewed` events. Instructors can use `assignment_reviews_list`, `assignment_review`, and the portal review queue.

## Quiz authoring

Use `quizzes_create` or `quizzes_update` with questions in this shape:

```json
[
  {"prompt": "What is 2 + 2?", "options": ["4", "5"], "correct_index": 0}
]
```

`correct_index` is zero-based. Answer keys are available to operators and excluded from member responses. `quiz_submit` grades on the server and records each attempt. `learning_status` returns the member's latest attempt for each quiz and saved assignment submissions. Existing quizzes without multiple-choice options and an answer key require operator configuration before grading; their answers cannot be inferred automatically.

## Files and certificates

Bind Storage and attach numeric Storage file IDs to lessons, resources, or assignment attachments. `lesson_file_url` checks lesson access and file association before minting a short-lived URL through the binding. Members do not need direct Storage permissions. Completed courses issue certificates according to the existing certificate settings; `issued_certificate_get` returns the caller's earned certificate.

## Upgrade

Migration 012 preserves existing DM messages and read state while adding an immutable message sequence. It also creates quiz-attempt and assignment-submission tables. The portal sends the last displayed message ID when marking DMs read so messages arriving during a read remain unread. File IDs, course progress, offers, and enrollment data retain their existing formats.

## Validation

```sh
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
GOWORK=off go build .
cd ui/portal
bun install --frozen-lockfile
bunx tsc --noEmit
bunx --no-install playwright install chromium
bun run test:browser
```

Browser tests use deterministic API fixtures and cover desktop/mobile layout, empty states, safe Markdown, profile editing, saved learning results, service failures, and pagination. Backend regression tests cover delegated authorization, enrollment gates, migrations, file permissions, read cursors, content limits, and progress identity.
