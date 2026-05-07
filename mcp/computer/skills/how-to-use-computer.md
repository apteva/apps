---
name: how-to-use-computer
triggers:
  - browser_session
  - computer_use
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

## Chat attachments

When you do something visible to the browser, attach a component
instead of describing it in prose. All render-only — never use them
to ask the operator a question.

| When | Component | Key props |
|---|---|---|
| After `browser_session(open)` succeeds | `browser-card` | `instance_id`, `backend`, `url`, `status` |
| After `screenshot` with SoM on | `screenshot-with-som` | `screenshot_url`, `som: [{label,x,y,w,h,kind}]`, `caption` |
| After traversing several pages | `navigation-timeline` | `steps: [{url,title,thumbnail,ts}]` |
| Mid-flow "watch me work" tile | `live-view` | `instance_id`, `height`, `mode` |

Attach via:

```
respond(components=[{ app: "computer", name: "<from-table>", props: {...} }])
```

Default `live-view` to `mode: "thumb"` (polled image). `mode: "live"`
is the full screencast — only when the view IS the message; multiple
live tiles in one transcript get expensive.

## Web-browsing patterns

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
