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
command yields a retryable webhook response. Twilio, Plivo, and Bandwidth
return their carrier hangup response; direct SIP rejects the INVITE. The
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

## Optional completion of suspected retry bursts (local follow-up)

A burst is evidence of repeated attempts, not proof of malicious callers. An
upstream forwarding service may retry unanswered calls. For controlled testing,
`inbound_burst_action=answer_announcement` can answer a suppressed call, wait for
carrier answer confirmation, play a short configured message, then hang up only
after matching speech completion. The policy is generic; this mode currently
supports programmable Telnyx. Other transports retain rejection and report
`announcement_unsupported` in call diagnostics.

The default remains `reject`. Answering may incur carrier charges and may change
Google/tracking-provider call reporting. Explicit caller block rules always
reject. Destination-wide bursts remain alert-only. This mode does not offer the
call to an adviser, start an AI thread, record it, or add it to the missed-call
pool. It preserves `burst_suppressed` classification.

Settings:

| Setting | Default | Bounds / purpose |
| --- | --- | --- |
| `inbound_burst_action` | `reject` | `reject` or `answer_announcement` |
| `inbound_burst_message` | We cannot take your call right now. Goodbye. | At most 240 characters |
| `inbound_burst_language` | `en-US` | Telnyx speech language, e.g. `fr-FR` |
| `inbound_burst_max_seconds` | `20` | 5–60 seconds from admission to deadline cleanup |
| `inbound_burst_max_concurrent` | `3` | 1–50 active suppressed announcements per project |

Disposition, message and language are pinned at admission. Capacity is reserved
transactionally through active call records and survives restart. Excess
suppressed calls use explicit rejection with reason `announcement_capacity`;
this limit never excludes unrelated callers. Migration 034 indexes active burst
announcements so capacity checks do not scan completed call history.

Duplicate initiated webhooks do not reissue an accepted answer while waiting for
its confirmation. Failed commands use the existing bounded retry queue. Missing
answer/speech callbacks trigger hangup at the configured deadline; carrier
outages can still delay physical disconnection. Call diagnostics expose the
selected action, reason and execution stage under `suppression`.

This removes pre-answer rejection from the selected bounded burst path. It does
not promise to stop an independent upstream retry loop, and it cannot establish
whether Google, Telnyx or another provider originates repeats. Verify using one
originating direct call and one through the actual forwarding path, with carrier
SIP traces and a count of new sessions after completion. Local tests cannot
substitute for that network verification. No automatic deployment or live call
is part of this change.
