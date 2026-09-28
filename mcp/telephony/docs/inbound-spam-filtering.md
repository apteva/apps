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
