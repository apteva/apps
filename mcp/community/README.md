# Community v0.16.0

Checkout v0.4.0 supplies reusable order bumps on the product checkout page.
Configure them in **Checkout → Order bumps**, restricting the source to
`community` and the context to the Community ID. For an additional course,
select its Catalog price, set the delivery app to `community`, and set its
delivery reference to the course space ID. The course must have an active paid offer for the same Catalog product; a
separate bump price can provide a discount. Products from another app retain that app's delivery owner.

Buyers can opt in before or after sign-in and recover the same selection after
email verification or reload. Sign-in only binds verified identity; Billing
records are created when the buyer clicks Pay. One payment covers the primary
offer and selected extras. Community grants extra courses from paid Checkout
item receipts and revokes only the refunded item's grant. Independent manual
access survives refunds. Main-course purchase/refund accounting uses its own
line balance, preserving the existing promotion and acquisition snapshots.

One-time extras can accompany a membership's first invoice. Recurring prices
and renewal cycles contain only the membership price; the page shows the
amount due today separately from the renewal price. Courses already included
in the selected membership are not offered as extras. Unsupported recurring
bump prices are rejected by Checkout.

Refund add-ons from **Checkout → Sessions → Purchased items**. Use item refunds
for reliable access attribution; an external partial invoice refund is flagged
for accounting review instead of guessing which course to revoke.

Community provides a branded member portal, discussions, direct messages, courses, a public storefront, and one-time or recurring course access. The native dashboard panel manages communities, members, content, offers, instructors, and branding.

## Interactive lessons (0.17.0)

0.17.1 keeps running elements mounted when the portal refreshes a member's session (about every 48 seconds); before, each refresh reloaded the lesson and reset simulations.

A lesson can be built from ordered **blocks**: `markdown`, `lesson_body`, `video`, `resources`, `quizzes`, `assignments`, and `element`. Lessons with no blocks render the classic way, so existing courses are unchanged. `lesson_blocks_set` replaces a lesson's blocks and picks a layout: `article` (one column, in order) or `stage` (a large stage, a side panel where Markdown blocks become tabs, and blocks below). Keep a block's `id` when re-saving to preserve members' saved state.

An **element** is a full HTML/JS document from the community's element library. The portal mounts it in an iframe with `sandbox="allow-scripts"` (opaque origin: no cookies, storage, or access to the portal), inserts a strict CSP (`connect-src 'none'`; scripts only inline, blob, or from jsDelivr, unpkg, and cdnjs) and the element SDK, and talks to it over a private `MessagePort`. Elements get `window.lesson`:

| Call | Effect |
|---|---|
| `await lesson.ready()` | `{settings, state, steps, lesson, block}`: the block's settings and the member's saved state |
| `lesson.save(state)` | Saves up to 64 KB of member state for the block (debounced) |
| `lesson.emit(name, payload)` / `lesson.on(name, fn)` | Events between the elements of one lesson |
| `lesson.complete(step)` | Records a step; when every `required_steps` entry of the lesson's element blocks is recorded, the lesson completes |
| `await lesson.run(runner, input)` | Calls a runner the element version declares and the community bound |

Only operators can save or publish elements (`course_elements_save`, `course_elements_publish`). Published versions are immutable and blocks pin a version; `course_elements_publish {upgrade_blocks: true}` moves blocks forward. `course_elements_install_starters` installs `code-playground`, `finance-lab`, and `board-scene` (Arduino sketches are translated in the browser; set `settings.runner` to use a bound runner instead).

`element_runners_set` binds runner names to app tools, which receive `{runner, input, element, version}` with no member identity. Members are limited to 20 runner calls per minute. `course_export` produces a JSON package with sections, lessons, blocks, quizzes, assignments, and the element versions used; `course_import` creates a new course from it, reusing element versions with the same checksum. Videos and resource files are Storage files and are not included.

The dashboard panel adds **Interactive elements** (library, sandboxed preview, runners), **Blocks** on a lesson, and course **Export** / **Import**.

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

## Brand production profiles

Instructor profiles support an optional `persona_id` (zero clears it). This private production reference links the instructor to Persona Studio voice/avatar assets while course and learner records remain in Community. Public instructor payloads do not expose the production reference.
