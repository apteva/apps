# Telephony 0.30.0

Connected calls can be transferred to advisers' browsers. The carrier leg never
changes: Telephony moves the call between its own peers. See
[call-transfers.md](call-transfers.md).

- **AI to adviser.** New agent tools `telephony_transfer_call` and
  `telephony_transfer_targets`.
  - The caller hears hold music, the AI hears silence, and the target
    destination's or ring group's browsers ring.
  - When an adviser answers, the call becomes a softphone call. Telnyx calls
    restart their media stream; direct SIP calls start a new bridge on the same
    RTP socket. The AI thread then ends.
  - When nobody answers, the AI gets the caller back, or the call ends with
    `if_no_answer: "hangup"`.
  - Works on Telnyx calls and inbound direct SIP calls.
  - The new config `ai_transfer_targets` limits the targets. Empty allows every
    enabled browser destination and ring group.
- **Adviser to colleague (blind).** A Transfer button in the Calls panel, and
  `transfer()` / `cancelTransfer()` in the SDK.
  - The caller is on hold until a colleague answers, and the first adviser can
    cancel until then.
  - When nobody answers, the call comes back to the first adviser.
  - Needs the new `call.transfer` action and a `transfer_targets` list on the
    user's or group's grant.
- **Answering a transfer.** It uses the usual answer route. Transfers show as
  incoming calls (`transfer`, `answerable`, `ring_offers`), so ringing and
  automatic pick-up work for them. A ring group rings all its members at once.
- **New events.** `call.transfer_started`, `call.transfer_answered`,
  `call.transfer_no_answer`, `call.transfer_canceled`, `call.transfer_failed`
  and `call.transfer_abandoned`. Each carries the call payload plus `transfer`.
- **Migration 077** adds the `call_transfers` table. Transfers ringing when
  Telephony restarts end as `failed`, and the call goes back to whoever
  transferred it.
- **No added cost on normal calls.** The only per-call work is one flag check
  per caller audio frame. Nothing is read from the database unless a transfer
  is ringing.
