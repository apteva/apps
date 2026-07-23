---
name: how-to-use-computer
triggers:
  - browser_session
  - computer_use
  - computer_context_create
  - computer_context_list
  - computer_context_get
  - browser_recording
  - dialog
  - modal
  - embed
  - compose
  - "cookie banner"
  - "login form"
---

# Computer — chat attachments + web-browsing guide

## SoM badge colors

Every interactive element on a screenshot has a colored numeric badge:

- **ORANGE** — text inputs, textareas, contenteditable, selects.
  *Need to type? Click an orange badge.*
- **GREEN** — buttons, `role=button`, submit controls.
  *Need to click an action? Look at green badges.*
- **BLUE** — `<a href>`, `role=link`. Navigation.
- **GRAY** — generic `onclick` / `tabindex` wrappers. Prefer the
  more specific neighbour.

Lower label number = higher priority. When two labels match your
goal, pick the lowest.

`browser_screenshot` is for clean capture/export screenshots and defaults to
no Set-of-Mark labels. Use `computer_use(action="screenshot")` for navigation,
or pass `annotate=true` to `browser_screenshot` only when labels are desired.

## Chat attachments

When you do something visible to the browser, attach a component
instead of describing it in prose. All render-only — never use them
to ask the operator a question.

| When | Component | Key props |
|---|---|---|
| After `browser_session(open)` succeeds | `browser-card` | `instance_id`, `backend`, `url`, `status` |
| After `screenshot` | `screenshot-with-som` | `screenshot_url`, `som: [{label,x,y,w,h,kind}]`, `caption` |
| After traversing several pages | `navigation-timeline` | `steps: [{url,title,thumbnail,ts}]` |
| Mid-flow "watch me work" tile | `live-view` | `instance_id`, `height`, `mode` |

Attach via:

```
respond(components=[{ app: "computer", name: "<from-table>", props: {...} }])
```

Default `live-view` to `mode: "thumb"` (polled image). `mode: "live"`
is the full screencast — only when the view IS the message; multiple
live tiles in one transcript get expensive.

## Session cleanup

Treat browser sessions like resources you open and close.

- If you open a browser session for a task, close it when the task is done.
- Close after the final screenshot, final data extraction, or successful
  form/action completion.
- Use `browser_session(action="close", session_id=...)`.
- Do not close when the user explicitly asked to keep the browser open, when
  waiting for human input in the live page, or when the next immediate step
  still needs the same session.
- For persisted contexts, closing is the clean handoff point for saving
  provider/browser state.
- Browserbase and Steel record hosted sessions automatically. After closing,
  call `browser_recording(session_id=...)`; retry while its status is
  `processing`, then use the returned app-owned playlist URL when `ready`.
- Local, Browser Engine, and service sessions currently return
  `status="unsupported"` for recordings.

## Presentation mode for recordings

Open a session with `presentation_mode="demo"` when the browser is performing
a user-facing walkthrough or recording:

```
browser_session(
  action="open",
  backend="browserbase",
  presentation_mode="demo",
  url="https://example.com"
)
```

- The default `fast` mode preserves normal automation timing and behavior.
- On local, Browserbase, Steel, and Browser Engine backends, demo mode adds an
  in-page cursor and click pulse, types short `computer_use(action="type")`
  text character by character, and holds each completed action long enough to
  read in a live view or recording.
- Browserbase and Steel provider recordings capture the in-page presentation
  layer. Local and Browser Engine expose it in their live browser views but do
  not currently produce recordings.
- Service backends get the paced typing and holds without an in-page cursor,
  because their wire protocol does not expose a browser DOM connection.
- For visible short-form entry in a demo, click the field and use `type`. Keep
  using `set_text` for long content, rich editors, or exact bulk replacement;
  those actions remain atomic.
- Presentation rendering is best-effort. If a restricted page prevents the
  cursor overlay from being injected, the underlying click still runs.

## Web-browsing patterns

## Structured controls first

Use the specialized DOM actions before falling back to human-like
click/key sequences:

- For native `<select>`, dropdowns, ARIA `role=combobox`, `role=listbox`,
  and multiselect controls, use `computer_use(action="select_option", ...)`
  first. Do not click options one by one or use ArrowDown/Enter unless
  `select_option` fails.
- Pass the control's SoM `label` or a CSS `selector`, then the desired
  `text`/`value`; for several options use `texts`/`values`.
- For multiselects, use `mode="replace"` to set the exact selection,
  `mode="add"` to add, `mode="remove"` to unselect, or `mode="toggle"`
  only when the requested final state is genuinely a toggle.
- For checkboxes, radios, and switches, use
  `computer_use(action="set_checked", checked=true|false, ...)` instead
  of clicking and guessing the final state.
- For long text fields, textareas, contenteditable editors, or public
  message/post composers, use
  `computer_use(action="set_text", text="...", mode="replace", ...)`
  instead of click + `Control+A` + `type`. Use `newline_mode="compact"`
  when blank paragraph gaps are not desired.
- For date/time scheduler fields, use
  `computer_use(action="set_temporal", value="2026-07-01", ...)` or
  `value="11:00 AM"` when the field can be targeted directly.
- If the page shows separate date and time fields, set them separately with
  their own `label` or `selector`. Use a combined date-time value only when the
  page has one actual datetime field.

Examples:

```json
{"action":"select_option","session_id":"br_...","label":19,"texts":["Leather seats","Front seat warmers"],"mode":"replace"}
```

```json
{"action":"select_option","session_id":"br_...","label":19,"text":"Front seat warmers","mode":"remove"}
```

```json
{"action":"set_checked","session_id":"br_...","label":7,"checked":false}
```

## Persistent contexts

Use app contexts when cookies/storage should survive across sessions.

- Create or import with `computer_context_create(name, backend?,
  provider_context_id?, persist_default?)`.
- List existing contexts with `computer_context_list()` without a `backend`
  first. That returns all saved contexts across Local, Browserbase, Steel, and
  Browser Engine. Use `backend=default` for the Computer app default provider,
  or a concrete backend only when the user explicitly asks for it.
- Reopen with `browser_session(action="open", context_name=..., backend=...)`
  or `browser_session(action="open", context_id=<app_context_id>)`.
- Prefer reopening by `context_id` from `computer_context_list`; it avoids
  guessing which provider owns the saved state. `context_name` works when the
  name is unique across providers.
- For a new saved context and immediate session, call
  `browser_session(action="open", context_name=..., auto_create_context=true,
  persist=true)`.
- Always provide a meaningful `context_name` when creating a reusable saved
  context. If omitted with `auto_create_context=true`, the app creates a
  generated fallback name for recovery, but agents should not rely on it.
- `provider_context_id` is the raw Browserbase context / Steel profile /
  Browser Engine context id. Prefer app `context_id` or `context_name` unless
  importing an existing provider context.
- `persist=false` means load the context read-only for that session.

**Cookie / consent banners.** Dismiss FIRST. Look for "Accept",
"Accept all", "OK", "Agree", "Got it". Some live in closed shadow
DOM but the AX-tree fallback surfaces them.

**Login forms.** Email/username (orange, topmost) → type → click
"Continue"/"Next" (green) → password if shown → submit. Some sites
skip the password step if cookies/IP are trusted.

**Floating modals** (overlay + Cancel/X at corner). Click ONLY inside
the modal's box. Sidebar / page-behind labels are visually covered.

**Inline panels** (no overlay; replaces a section of the page). Common
when clicking "Video" / "Link" / "Embed" toolbar buttons in editors —
the picker takes over the area where the body editor was. Click the
input INSIDE the panel, type, press `Enter` to commit. Most pickers
auto-commit on Enter — there's often NO visible "Insert" button.

**Search auto-suggest.** Type → `ArrowDown` + `Enter` to pick a
suggestion, or just `Enter` for the raw query.

**Lazy-loaded content.** If your target is below the fold or the
page seems incomplete: `computer_use(action=scroll, direction=down)`.

**Form errors.** Rejected input shows red text/icon near the field.
Read it before retrying.

**Click did nothing.** Two consecutive screenshots identical after a
click → press `Escape`, take a fresh screenshot, retry with a
different label (the click likely hit an invisible overlay).

## Composers (post / blog / comment editors)

**`/new` URLs allocate server state.** `/posts/new`, `/compose`,
`/create` create a draft on first visit then redirect to
`/posts/<id>/edit`. Each visit spawns a duplicate. Recover
IN-PLACE — don't navigate to /new again as a "reset".

**Body editor vs picker buttons.** Body = ORANGE, large empty area,
placeholder like "Start writing…". Pickers = GREEN, small icon
toolbar items that open inline panels. To type post content, click
the orange body area first; clicking pickers opens menus.

**Publish is usually two clicks.** First "Publish" opens a
confirmation step (visibility / schedule / audience). The post is
NOT live until you click the inner Publish/Confirm. After publish,
dismiss any share/success modal before reading the final URL.

## What NOT to do with chat attachments

- **Ask the operator a question.** Components are render-only. For
  human-in-the-loop input, call `pace(1h)` and emit a marker like
  `AWAITING_CODE` — the operator's reply arrives as a console-
  injected message which you read on resume.
- **Persistent dashboards.** Belong in the operator panel.
- **Replace a tool call.** Always run the tool first; the component
  summarises what the tool already did.
