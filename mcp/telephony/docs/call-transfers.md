# Call transfers (Telephony 0.30.0)

A connected call can ring advisers' browsers and move to whoever answers. The
carrier leg never changes: no SIP REFER, no carrier transfer and no second
call. Telephony moves the call between its own peers.

There are two ways to start a transfer:

- **AI to adviser.** The call's AI calls `telephony_transfer_call`.
- **Adviser to colleague (blind).** The adviser on the call presses Transfer in
  the softphone, or calls `transfer()` in the SDK.

## Targets

A transfer is aimed at a target:

- a **browser destination**, or
- a **ring group**. A ring group rings all of its enabled browser members at
  once, whatever its own strategy.

The adviser who started the transfer is never offered their own transfer.

| Who transfers | Allowed targets |
|---|---|
| AI | The config `ai_transfer_targets`, a comma-separated list of IDs. Empty allows every enabled browser destination and ring group in the project. `telephony_transfer_targets` lists them. |
| Application user | The `transfer_targets` field of their user or group grant in the access policy. The provider or scopes must include `call.transfer`. |
| Operator (panel or API key) | Any target. |

Example group grant:

```json
{ "id": "sales-team", "role": "user", "destinations": ["sales"], "transfer_targets": ["support", "team-ring-group"] }
```

Each entry in `transfer_targets` must be a browser destination or ring group in
the project. Policy writes that name anything else are refused.
`GET /softphone/access` reports the user's `transfer_targets`.

## While it rings

- **The caller is put on hold.** They hear the project's hold music, or
  silence if none is configured. If the adviser had already put the caller on
  hold, that hold stays as it is.
- **Targets see the call.** Their call list shows the connected call with a
  `transfer` object (source, target, note, deadline) and one browser
  `ring_offers` entry per destination they can answer through.
  - `answerable` is true for them.
  - The SDK's `isIncomingBrowserCall` treats the call as incoming, so the ring
    UI, ringtone and automatic pick-up work as they do for a new call.
  - Automatic pick-up follows the target adviser's own pick-up policy.
- **An AI transfer** keeps the AI connected but feeds it silence instead of the
  caller, and the AI's call policy is paused by the hold. The AI's last
  sentence (for example "please hold") finishes playing before the hold music
  starts, up to 5 seconds.
- **An adviser transfer** keeps the first adviser's browser on the call, on
  hold, until a colleague answers. They can cancel with
  `POST /softphone/transfer-cancel/{call_id}` or `cancelTransfer()`.

The ring lasts `timeout_seconds`: 10 to 90 seconds, 30 by default. Only one
transfer rings per call at a time.

## When someone answers

The colleague answers with the usual `POST /softphone/answer/{call_id}`, or the
SDK `answer()`. The answer is atomic: the first answer wins and later answers
get `offer_expired`.

| Transfer from | What happens on answer |
|---|---|
| Adviser | Ownership and the browser media grant move to the colleague, as with a supervisor takeover. The first browser receives `call.transferred` and stops, and the SDK reports `transferred: true`. |
| AI | The call becomes a softphone call: `peer_kind` changes to `human` and the carrier bridge restarts with the softphone hub as its peer. On Telnyx this is a media stream restart, announced as a planned handover, so media recovery covers a lost restart. On direct SIP it is a new local bridge on the same RTP socket. The AI thread is then ended, and the AI can no longer hang up the call. |

AI transfers need a Telnyx call or an inbound direct SIP call. Other carriers
return `transfer_unsupported`.

## When nobody answers

| Outcome | What happens |
|---|---|
| No answer (deadline passed) | The call returns to whoever transferred it. The hold ends (unless the adviser had held the caller before transferring) and an AI is unparked. An AI may instead pass `if_no_answer: "hangup"` to end the call. |
| Canceled | The call returns to the adviser or operator who canceled. |
| Abandoned | The caller hung up while it rang. |
| Failed | Telephony restarted while it rang. Ringing transfers are ended at startup and the call returns as for no answer. |

## Agent tools

- `telephony_transfer_targets`: the targets the calling AI may use.
- `telephony_transfer_call`
  - Arguments: `call_id`, `target_id`, `note?` (at most 500 characters, shown
    to the adviser), `timeout_seconds?`, `if_no_answer?` (`return` or
    `hangup`).
  - It waits for the outcome and returns the transfer with a short instruction
    for the AI. For example, after an answer: "do not speak, and do not hang
    up".

## Softphone API

| Route | Permission | Body |
|---|---|---|
| `GET /softphone/transfer-targets` | `call.transfer` | none |
| `POST /softphone/transfer/{call_id}` | `call.transfer`, and the caller must own the call | `{target_id, note?, timeout_seconds?}` |
| `POST /softphone/transfer-cancel/{call_id}` | `call.transfer`, and only the adviser who started the transfer (or an operator) | none |

Refusals return JSON `{code, error}`. The codes are:

- `target_not_allowed`
- `target_unavailable`
- `not_call_owner`
- `transfer_in_progress`
- `call_not_connected`
- `transfer_unsupported`
- `transfer_not_found`

SDK:

- `TelephonyClient.transferTargets()`, `transfer(id, {...})`,
  `cancelTransfer(id)`.
- `HeadlessSoftphone.transfer(targetId, {note})` and `cancelTransfer()`. The
  snapshot exposes `transfer` while it rings, and `transferred` once a
  colleague has the call.

## Events

Every event carries the call payload plus a `transfer` object:

- `call.transfer_started`
- `call.transfer_answered`
- `call.transfer_no_answer`
- `call.transfer_canceled`
- `call.transfer_failed`
- `call.transfer_abandoned`

## Cost on normal calls

- **Calls that are not being transferred:** one atomic load per caller audio
  frame (the parked check). Call lists and the transfer worker read nothing
  while no transfer rings.
- **Calls with a transfer ringing:** one indexed query per call-list rebuild.
