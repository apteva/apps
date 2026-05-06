---
name: how-to-use-computer
triggers:
  - browser_session
  - computer_use
  - "navigated through"
  - "browsed"
---

# Computer — chat attachment guide

When you do something visible to a browser, attach the matching
chat component so the operator sees it instead of reading a wall
of text. All four are render-only — never use them to ask the
operator a question.

## Patterns

### After `browser_session(open, url=X)` succeeds

Attach a `browser-card` instead of writing "I opened …":

```
respond(components=[{
  app: "computer",
  name: "browser-card",
  props: {
    instance_id: "<the instance you're on>",
    backend: "local" | "browserbase" | "steel",
    url: "<the URL you opened>",
    status: "active"
  }
}])
```

The card carries the URL, backend, status, and a "watch live" button
that deep-links to the operator panel. Don't also describe the
session in prose; the card is the description.

### After `computer_use(screenshot)` when SoM is on

Attach `screenshot-with-som` so the badges render over the image
in chat:

```
respond(components=[{
  app: "computer",
  name: "screenshot-with-som",
  props: {
    screenshot_url: "<the screenshot URL the tool returned>",
    som: [
      { label: 1, x: 120, y: 84, w: 200, h: 32, kind: "input" },
      { label: 2, x: 120, y: 130, w: 80, h: 32, kind: "button" }
    ],
    caption: "Login form — email field is label=1, submit is label=2"
  }
}])
```

Skip the component when SoM is off; for a plain screenshot, just
reference it inline as you would any image.

### Summarising a multi-page browse

After traversing several pages (research, reading docs, clicking
through a flow), attach `navigation-timeline` instead of bulleting
URLs in prose:

```
respond(components=[{
  app: "computer",
  name: "navigation-timeline",
  props: {
    steps: [
      { url: "https://...", title: "...", thumbnail: "...", ts: "10:14" },
      { url: "https://...", title: "...", thumbnail: "...", ts: "10:15" },
      ...
    ]
  }
}])
```

### Announcing "I'm working on this now" mid-flow

When the next step will take a while and the operator should be
able to watch, attach a small live-view tile:

```
respond(components=[{
  app: "computer",
  name: "live-view",
  props: {
    instance_id: "<this instance>",
    height: 320,
    mode: "thumb"     // "live" only when this view is the focus
                      // of the message; thumbs are cheaper for
                      // long transcripts.
  }
}])
```

Default to `mode: "thumb"` (polled image, ~3s refresh). Use
`mode: "live"` only when the live screencast is the point of the
message — three live tiles in a long transcript get expensive.

## SaaS web composers (Patreon, Substack, Twitter, Notion, …)

Three traps that consistently break agent flows in rich-text editors:

1. **/new allocates server state.** URLs like `/posts/new`, `/compose`,
   `/create` create a draft on the server before the editor renders,
   then redirect to `/posts/<id>/edit`. Each visit spawns a duplicate
   draft. If the editor seems broken, do NOT navigate to /new again
   as a reset — recover in-place by pressing Escape and re-clicking
   the field.

2. **Inline pickers steal focus.** Most editors expose a "+" or
   "Add ..." button INSIDE the empty body area. Clicking it opens
   a content-type picker menu (image/video/poll/embed) instead of
   focusing the text editor. Press Escape, then click an empty
   area inside the body region.

3. **Recognise the body field by badge color and tier:**
   - **Body editor**: ORANGE badge (input/textbox tier), label text
     shows the placeholder — "Start writing…", "Type here", "Tell
     your story…". This is a `textbox`-role contenteditable. Always
     click this first when typing post content.
   - **Picker buttons**: GREEN badge (button tier), labels read
     "Click to add", "Add image", "+", icon-only. These open menus,
     don't accept text.
   - **Publish is usually two clicks**: first opens a confirmation
     modal; the post is NOT live until you click the inner
     Publish/Confirm button. Dismiss any post-publish share/success
     modal before reading the URL.

## What NOT to use these for

- **Asking the operator a question.** Components are render-only.
  For HITL questions, use `pace(1m)` and wait for a console
  message back, the same pattern as the Patreon login flow.
- **Persistent dashboards or settings.** Those belong in the
  operator panel, not in chat.
- **Replacing tool calls.** Always run the tool first; the
  component only summarises what the tool already did.
