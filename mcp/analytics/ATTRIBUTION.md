# Acquisition attribution and identity

Analytics 0.18 measures where people come from and joins that to what they
later do in your product. It takes one tag on each site, one backend call per
product, and no changes to the apps that emit lifecycle events.

## 1. The tag

```html
<script async src="https://YOUR-APTEVA/api/apps/analytics/ui/tag.js"
        data-key="wk_live_..."
        data-cookie="true"></script>
```

On every page the tag records:

- **Touches.** `utm_source`, `utm_medium`, `utm_campaign`, `utm_term`,
  `utm_content` and external referrers. The tag keeps the visitor's first touch
  and latest touch, and each one expires after `data-attribution-days` (default
  90). A direct visit never overwrites a touch. Referrers from your own host,
  from the `data-cookie-domain` and from `data-linker` hosts don't count.
- **Event props.** Every event carries `utm_*`, `touch_referrer`,
  `touch_landing` and `touch_ts` for the latest touch, plus `first_utm_*`,
  `first_referrer`, `first_landing` and `first_touch_ts` for the first. Your own
  `apa(event, props)` values take precedence.
- **Delivery.** If a send fails at the network level, the tag queues it (up to
  50 events, for 24 hours). It resends on the next page load or when the browser
  is back online, along with how long the event waited, so the server dates it
  correctly. The server drops duplicates by event ID.

| Attribute | Effect |
|---|---|
| `data-consent="required"` | Nothing is stored on the device until `apa("consent", "granted")`. Before that, events go out with per-page identifiers only. `apa("consent", "denied")` deletes everything the tag stored. |
| `data-attribution-days="N"` | Lifetime of a touch, from 1 to 730 days. |
| `data-cookie="true"` | Copies the visitor ID into a first-party `apa_vid` cookie, so your backend can read it. |
| `data-cookie-domain=".example.com"` | Cookie scope; implies `data-cookie`. |
| `data-linker="app.example.com,shop.example.org"` | Carries the visitor and session across these domains (see below). |

`apa.visitorId()` returns the visitor ID, and `apa.attribution()` returns the
touches stored on this device.

### Cross-domain journeys

With `data-linker`, a click on a link to an approved host adds
`_apa=<visitor>.<session>.<time>` to that link. The receiving site's tag accepts
the parameter only if it is less than 5 minutes old and the referrer is either
absent or an approved host. It then removes `_apa` from the address bar. A
shared link therefore never merges two people. Touches stay in each domain's own
storage, but because both sites now share the visitor ID, the server combines
their histories once that visitor is identified.

## 2. The authentication hook (once per product)

The browser never states who a user is: the write key is public, so a
browser-supplied user ID can't be trusted. After sign-up or sign-in, your
**backend** reads the visitor ID (the `apa_vid` cookie, or the value of
`apa.visitorId()` sent along with the form) and calls:

```go
var out struct {
    Backfilled  int64 `json:"backfilled"`
    Attribution struct {
        FirstTouch map[string]any `json:"first_touch"`
    } `json:"attribution"`
}
err := ctx.PlatformAPI().CallAppResult("analytics", "analytics_identify", map[string]any{
    "user_id":    user.ID,
    "visitor_id": visitorIDFromCookie,
    "aliases":    []string{billingCustomerID}, // optional
}, &out)
// Ignore err: analytics is optional and must never block sign-in.
```

The caller needs the `identity.manage` permission.

`analytics_identify`:

- links the visitor to the user, backfills the visitor's anonymous tag events
  with that user ID, and gives every later event from the visitor the same user;
- stores `aliases`, the IDs this person has in other apps (a billing customer
  ID, a CRM contact ID);
- returns the user's first and latest touch, combined across every linked
  visitor (devices, browsers, domains).

If a device changes hands and the visitor is linked to someone else, the result
reports `previous_user_id`. Events the previous user already had stay theirs.

## 3. Lifecycle outcomes

Apps such as billing or saas already emit events like `subscription.active` or
`invoice.paid`. Analytics records them through bus auto-capture (Capture tab),
or the app calls `analytics_track` itself. Report on them by acquisition:

```json
{"tool": "analytics_attribution", "args": {
  "topic": "subscription.active",
  "actor_key": "props.customer_id",
  "touch": "first",
  "by": "campaign"
}}
```

`actor_key` says where to find the actor on each event: the `user_id` column (the
default) or any `props.<key>` holding a user ID or an alias. Rows come back as
`{value, events, users}`. `(none)` means the user has no known touch (a direct
visitor, or someone never identified); `(not set)` means the touch exists but
lacks that field. To look up one user, call `analytics_user_attribution`.

Analytics doesn't create CRM contacts and doesn't imply marketing consent.
Syncing to a CRM remains the CRM integration's job.
