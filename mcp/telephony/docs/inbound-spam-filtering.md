# Inbound caller filtering

Telephony evaluates inbound policy after the carrier delivers a new call ID
and before creating an adviser offer. The public IVR number remains assigned
and reachable. A rejected call is stored with `handling_reason=spam_suppressed`
and `error_message=caller_blocked`; it never enters the adviser missed-call
pool.

Set `inbound_spam_blocked_callers` to a comma-separated list of displayed
caller numbers in E.164 format. A rule can apply to the whole project or one
destination:

```text
+33611111111,+33622222222@+33189000001
```

The first rule rejects that displayed caller at any project number. The
second rejects it only when calling the specified destination. Invalid
entries are ignored. An explicit block takes precedence over the trusted
caller exception used by the burst detector. No caller is blocked by default.

The rule is provider independent. Telnyx receives the initial signed
`call.initiated` webhook and Telephony calls `reject_call`; a failed carrier
command yields a retryable webhook response. Twilio returns a Reject response; Plivo and Bandwidth
use their carrier termination response; direct SIP rejects the INVITE. The
original per-caller burst guard still applies to new carrier call IDs. A
destination-wide burst across rotating caller IDs remains alert only, so it
cannot close the public IVR to unrelated callers.

Displayed caller ID is not proof of origin and may be spoofed. Use this rule
only for a specific displayed number whose legitimate traffic can tolerate
being rejected. Carrier signaling data is needed to identify the upstream
source of a rotating or spoofed-number campaign.

## Carrier rejection and evidence (0.7.0)

Telnyx rejection sends `cause=CALL_REJECTED` (documented SIP 603), with a stable
command ID. Failed rejection commands remain in a durable bounded retry queue;
webhook failures also receive a retryable response. This does not establish how
an upstream dialer reacts or prevent new carrier sessions from arriving.

Use `telephony_call_get` to inspect separate carrier leg/session IDs, allowlisted
routing SIP headers and recent command outcomes. These records help correlate
repeated sessions with carrier traces; displayed caller ID alone is insufficient.

## Default protection and termination (local follow-up)

There is no special burst-announcement mode. Default thresholds admit the first
12 distinct carrier call IDs from the same displayed caller to the same
project destination within 60 seconds; attempt 13 and excess attempts during
the cooldown are suppressed before adviser offers. Suppression never starts a
message or AI thread. Duplicate webhooks do not count as new attempts. Explicit
caller blocks apply immediately. Destination-wide rotating-caller bursts stay
alert-only, preserving the chosen public-IVR availability policy.

Normal routing executes configured announcements before ending a call. When
routing or lifecycle cleanup ends an unanswered inbound attempt, the shared
termination helper selects carrier rejection; after an observed answer it uses
hangup. Telnyx supplies `CALL_REJECTED`. Direct SIP sends 603 Decline, and initial
Twilio suppression returns `<Reject reason="rejected"/>`. Other adapters retain
their supported termination operations; identical SIP responses across carriers
are not assumed. An unanswered call can be deliberately rejected before any
burst exists, without classifying it as spam or suppressing its callback
opportunity. This respects explicit no-announcement flows.

Repeated initial webhooks cannot exhaust an accepted answer's retry budget while
its confirmation is pending. Expiry processing locks and reloads the call before
terminating it, so a stale worker snapshot cannot end a call whose claim/routing
transition extended its deadline. Burst threshold queries stop after the limit
plus one indexed rows instead of counting every attempt in a growing flood.

These controls prevent excess detected calls from reaching advisers and remove
avoidable ambiguous termination paths. They cannot prevent an external carrier
from originating a new session or establish who owns a Diversion number. The
final upstream SIP response and retry behavior still require carrier traces and
a controlled call through the actual forwarding path. No production changes or
live calls are included in this local follow-up.
