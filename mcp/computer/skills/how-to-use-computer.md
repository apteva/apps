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

## What NOT to use these for

- **Asking the operator a question.** Components are render-only.
  For HITL questions, use `pace(1m)` and wait for a console
  message back, the same pattern as the Patreon login flow.
- **Persistent dashboards or settings.** Those belong in the
  operator panel, not in chat.
- **Replacing tool calls.** Always run the tool first; the
  component only summarises what the tool already did.
