# Persistent app component views

Conversations 0.26.0 can keep any installed, declared message card in a side
panel or an in-page floating view. The user controls placement. This uses the
existing app SDK's `provides.ui_components`, message attachment slot and
`AppResourceRef`; there is no new dock/floating manifest slot or required SDK
release. Existing cards get controls automatically. Identical legacy references
share a renderer; resource references also retain identity when props change.

## Producer declaration

For example, Computer can declare its existing renderer with the resource type
it represents:

```yaml
provides:
  ui_components:
    - name: browser-view
      entry: /ui/BrowserViewCard.mjs
      slots: [chat.message_attachment]
      resource_types: [computer.browser_session]
      props_schema:
        type: object
        required: [session_id]
        properties:
          session_id: { type: string }
          caption: { type: string }
```

`resource_types` is existing SDK metadata. Explicit `app/name/props` references
remain supported on hosts whose older catalog does not expose that metadata.
When the metadata is exposed, the reference's resource type must match it.

## Return one stable reference from tools

Return a ready-to-copy component from open/status/update results:

```json
{
  "view": {
    "app": "computer",
    "name": "browser-view",
    "resource": {
      "app_install_id": 164,
      "resource_type": "computer.browser_session",
      "resource_id": "br_example"
    },
    "props": {
      "session_id": "br_example",
      "caption": "Browser"
    }
  }
}
```

The IDs above are illustrative. The producer derives its real installation ID
from trusted runtime identity (`ctx.PlatformAPI().WhoAmI().InstallID`) or the
platform-provided `APTEVA_INSTALL_ID`. In Go, the resource object is
`sdk.AppResourceRef{InstallID: installID, ResourceType:
"computer.browser_session", ResourceID: sessionID}`. Do not ask the model to
invent an installation ID or derive resource identity from changing labels.

The agent copies `view` into `conversations_send.components` once. The send
receipt returns `view_refs`. Ordinary follow-ups use text and update the existing
resource through its producer. If an explicit refresh needs new display props,
reuse the exact resource locator. A new browser session gets a new resource ID.

Resource references are locators, never grants. Conversations resolves the
declared renderer in the message's project and checks its installation against
the reference. The producer must authorize every data fetch and action using
the existing platform/app APIs. Interactive question/form components keep their
message-owned action identity and do not share resource previews.

## Optional responsive renderer

Conversations injects a trusted `presentation` prop after message props:

```ts
interface Presentation {
  mode: "inline" | "side-panel" | "floating";
  width: number;
  height: number;
  active: boolean;
}
```

This is an optional Conversations host contract, also exported as
`CardPresentation` from the React frontend. Other hosts and older releases may
omit it. Existing renderers can ignore it. A responsive browser renderer can
use the available space:

```tsx
function BrowserView({ presentation, ...props }) {
  const height = !presentation || presentation.mode === "inline"
    ? 360
    : Math.max(160, presentation.height - 80);
  // Keep stream/screenshot selection and session ownership unchanged.
  return <BrowserContent {...props} height={height} />;
}
```

`width`/`height` are available host content dimensions; inline dimensions may be
zero. Clamp computed sizes. A producer can pause UI polling when `active` is
false, but the property does not authorize closing or extending a resource.
Use injected `projectId`, `installId` and `appName` for authenticated requests;
message props cannot override those host fields or `presentation`.

The host moves one stable portal container between placements. React state
survives those moves. Chromium hosts with `Element.moveBefore` also retain
iframe browsing state; older engines may reload an iframe while preserving the
producer resource/session. Keep durable work state in the producer, not an
iframe's local memory.

## Producer tools and skills

Computer's own tool/skill guidance can say:

> Present the returned view once when a useful session opens. If work remains,
> include it with a concise Conversations progress message so the user can keep
> watching. Reuse that resource reference through updates; do not attach another
> card to every progress/final reply. Complete short work with one final card.
> Closing the panel changes presentation only. Close the browser normally when
> the work ends unless the user asked to keep it open; its existing view then
> follows the saved final frame.

Conversations' send description and skill already enforce this presentation
guidance. This release does not modify or release Computer. Computer's current
session-only cards can already be pinned; its producer can add the resource
declaration and returned locator above to support identity across prop changes.

## Scope and restoration

Views are scoped to the authenticated frontend instance, project, conversation
and installation. Switching conversations hides the previous workspace and
restores the selected reference for the newly selected conversation. The
session preference stores only an opaque key and placement mode; authorized
history must recreate the actual card after reload. Props, credentials and
screenshots are not stored in preferences. App catalog changes discard pinned
renderers and resolve declarations again.

The floating view stays inside the browser page; it is not an OS-level
always-on-top window. Narrow containers use floating placement, with minimize
and expand controls. Closing/returning inline never invokes a producer mutation.
