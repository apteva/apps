# Telephony 0.16.3

Ends direct SIP calls that a restart or crash left unfinished.

Direct SIP dialogs live only in the Telephony process. When it stops, every
direct SIP caller is dropped, but their calls stayed `answering` or
`in-progress` with armed deadlines, so the CRM showed them as still active and
the expiry worker later tried to end dialogs that no longer existed.

- At startup, after ownership is established, every unfinished direct SIP call
  (inbound or outbound) is marked `failed` with cause `telephony_restart`,
  initiator `telephony` and the error "call ended when Telephony restarted".
  A connected call is never recorded as a successful conversation.
- Its deadlines are cleared, so the expiry worker has nothing left to end.
- The change is published through the normal lifecycle update: one
  `call.failed` event per call, pending routing work canceled.
- No carrier command is sent. The call's Core thread is ended, best effort.
- Calls held by carrier call control (Twilio, Telnyx programmable) are left
  alone; the carrier keeps them alive and media recovery reconnects them.
- Calls already finished, and any dialog the gateway still holds live (a
  managed runtime starts its gateway before initialization, and a failed
  initialization can be retried in the same process), are left alone.
- Running it again changes nothing.
