---
name: how-to-use-screenshots
triggers:
  - screenshot
  - "take a screenshot"
  - "capture the page"
  - "what does X look like"
  - "screen capture"
  - "snap a picture of"
---

# Screenshots — capture and attach a browser screenshot

## When to use this app vs. the built-in browser tools

Use **`screenshot_capture`** when the user (or your own plan) needs a
saved, shareable image of a webpage. The capture lands in storage
under `/.screenshots/<yyyy-mm>/…`, gets a registry id, and the
operator can browse all of them in the Screenshots panel.

Do **not** use `screenshot_capture` for quick "I want to look at the
DOM right now" calls during an interactive `computer_use` loop —
that's what `computer_use(action="screenshot")` is for inside an
ongoing session.

Rule of thumb:

| Goal | Tool |
|---|---|
| One-shot "save what this URL looks like, share later" | `screenshot_capture` |
| Mid-session "show me the page so I can decide where to click next" | `computer_use(action="screenshot")` |
| Build a visual changelog of a page over time | `screenshot_capture` with consistent `label` |

## Calling `screenshot_capture`

```
screenshot_capture(
  url="https://example.com/pricing",
  label="Pricing page, post-launch",         # optional, shows in the gallery
  idempotency_key="user-shared-link-42"      # optional; same key within 10 min replays
)
```

Returns `{screenshot_id, storage_id, url, captured_at, label}` — the
`url` is a fresh signed link to the PNG in storage.

### `idempotency_key`

If you're capturing in response to a user request that might fire
twice (button mash, retry, ambient hook), pass an
`idempotency_key` derived from the cause. Within a 10-minute window
the second call returns the same `screenshot_id` without re-opening
a browser. Don't pass one for cases where two captures of the same
URL are intentional ("show me before and after this edit").

## Attach the card to your reply

Every successful `screenshot_capture` should be followed by attaching
the chat component so the operator sees the image inline instead of
just a URL:

```
respond(
  text="Captured. The pricing page shows the new tiers above the fold.",
  components=[{
    app: "screenshots",
    name: "screenshot-card",
    props: {
      screenshot_id: <id from the call>,
      url:           <url from the call>,
      caption:       "Pricing page, post-launch"   # match the label
    }
  }]
)
```

The `url` prop is the fast path — embedding the signed link means the
card renders without a fetch. It expires after a while; the card's
"Open in Gallery" button is the durable fallback.

## Listing and revisiting

`screenshot_list` returns the most recent 50 (or up to 200) captures
with their metadata; `screenshot_get(screenshot_id)` returns one with
a fresh signed URL. Use these when the user asks "what did that page
look like last week" — search by `url_contains` or `label_contains`.

## Cleanup

`screenshot_delete(screenshot_id)` is idempotent: it soft-deletes the
registry row AND removes the underlying storage blob. Use it when
the user explicitly asks to drop a capture. Don't proactively prune —
the operator owns retention policy.

## What this app does NOT do (yet)

- **No Set-of-Mark overlay.** When `computer` adds SoM in a later
  release, this app will too. Today: viewport PNG only.
- **No "screenshot the agent's live browser".** Captures always
  open a fresh, throwaway session. If the user wants a screenshot of
  the page the agent currently has open, that flow lands when
  `computer` exposes session resume — until then, the agent should
  use `computer_use(action="screenshot")` directly.
- **No diff/compare.** Two captures of the same URL are two
  independent rows; the operator compares visually in the gallery.
