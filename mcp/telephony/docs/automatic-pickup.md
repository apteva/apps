# Human softphone automatic pickup

Automatic answering is opt-in. The default project mode is manual. Automatic mode defaults to `delay_ms: 0`, meaning no deliberate countdown after the server reserves the offer; browser media establishment still takes time. A delay of 0–10000 ms is optional. AI immediate-answer routing remains independent.

The same `HeadlessSoftphone` implementation serves the Telephony panel and embedding apps. The panel exposes availability above the calls list, personal settings in the audio/settings pane, and administrator settings under Connected numbers → Automatic pickup.

## Policies

`POST /softphone/auto-answer-policy` reads or updates policies using the authenticated project. Scopes are `project`, `route`, `destination` (browser only), and `adviser`.

```json
{"scope":"project","policy":{"mode":"manual","delay_ms":0,"allow_adviser_override":true}}
```

```json
{"scope":"route","scope_id":"route-id","policy":{"mode":"automatic","delay_ms":0,"allow_adviser_override":false}}
```

An administrator supplies the adviser's complete authenticated `identity` tuple to configure an adviser. Application users can read/write only their own preference, and only when project policy permits it. Platform operators are keyed by verified platform user identity when available; machine/API contexts without an operator identity share their trusted operator context.

Precedence: project → route → browser destination → adviser. `mode: inherit` preserves the earlier mode/delay. An administrator's `allow_adviser_override: false` at any applicable scope prevents the adviser preference from overriding it. Project restrictions cannot be loosened by lower scopes. Route/destination policies are rechecked at placement.

## Availability and browser coordination

From an explicit user gesture:

```ts
await phone.setAutoAnswerAvailable(true);
```

This prepares microphone and playback access and registers a 30-second identity-bound availability lease. A 10-second heartbeat maintains it. The microphone is open while waiting, but idle audio is not transmitted to a carrier. Disable availability to release prepared devices:

```ts
await phone.setAutoAnswerAvailable(false);
```

One browser is designated per adviser and project. A second browser cannot silently replace it. Explicitly selecting **Use this browser** calls `setAutoAnswerAvailable(true, {takeover:true})`. Server checks reject the old browser's reservation even before it notices its lease was replaced. Closing/unloading a browser attempts release; expiry handles crashes and interrupted cleanup. Heartbeat failure disables future automatic pickup and leaves existing call audio intact.

Embedding hosts using the default polling configuration get permission-scoped call push/poll updates automatically while available. Hosts using `pollIntervalMs: 0` must supply complete current lists through `phone.observeCalls(calls)`. Feed withdrawn/terminal updates too. Existing single-call `observeCall` behavior is unchanged.

The server reserves only one call for an adviser and only one adviser for an automatic offer. It does not answer the carrier independently. The browser invokes the existing answer path, which repeats current offer, permission, reservation, ownership and capacity checks. Busy/connecting calls block another automatic pickup. Normal manual answer, IVR deadlines and fallback rules remain authoritative.

Delayed offers expose `snapshot.autoAnswer` with state, source, deadline and timing. `phone.answerAutoAnswerNow()` skips the remaining countdown; `phone.cancelAutoAnswer()` suppresses automatic action on that offer without declining it. Failed readiness or placement leaves existing manual/recovery behavior available; an HTTP answer response alone is not a live-audio success.

Server logs retain reservation/attempt time, call, policy source and configured delay, with a hashed principal. Browser session diagnostics retain offer receipt, answer start and actual audio-connected timing. Credentials are excluded.

## Verification

Go regressions cover policy defaults/precedence/locks, full identity tuples, project isolation, occupied advisers, competing calls/advisers, browser lease expiry/takeover, policy and permission revocation, withdrawn/terminal offers and normal human placement. TypeScript tests cover immediate action, countdown/cancel/answer-now, readiness failure, late responses, busy state, duplicate notifications and teardown.

An opt-in local Chromium test exercises real microphone/playback preparation and the HTTP answer/media path against a fake carrier, for zero and delayed pickup. It places no external calls:

```sh
env GOWORK=off go test -tags integration -run '^TestAutoAnswerRealBrowserPickup$' -count=1 -timeout 90s -v .
```
