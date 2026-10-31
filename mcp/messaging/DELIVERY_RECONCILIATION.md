# SES automatic response reconciliation

Messaging keeps the original provider event, recipient spelling, reason and raw
SES payload. An interpretation links a confirmed automatic response to its
inbound message. Message status, recipient status, event counts and the delivery
timeline use the interpretation (`auto_replied`, with `provider_kind=bounced`).
It does not count as a human reply or a delivery failure.

Only single-recipient SES Transient/General events without DSN failure fields
qualify. Confirmation requires the exact feedback ID in the inbound RFC
Message-ID, an SES mailer-daemon sender, passing SES DKIM/DMARC/virus verdicts,
the original sending mailbox as the owned inbound recipient, and a recorded
delivery to that same canonical recipient. If In-Reply-To is present, it must
match the original message. Other transient bounces, permanent bounces,
complaints and failures on other recipients keep their existing behavior.

## Rollout

Release versions: CRM **0.9.26** and Messaging **0.13.61**.

Install CRM first, then Messaging. CRM records the exact soft-bounce
contribution of each delivery event, so a later correction removes only that
contribution. It preserves other bounces, permanent delivery evidence, manual
suppressions, complaints and unsubscribe state. A correction received before
the original event prevents that late original from adding a false bounce.
CRM corrections without a recorded contribution preserve existing channel state;
pre-upgrade soft-bounce history requires an audited delivery recovery rather than
a blind counter reset. The three reported CRM channels were already active.

The existing Messaging recovery worker checks matching stored inbound evidence
every 30 seconds. Since **0.13.62** it does so incrementally: each new
Transient/General bounce is indexed once by feedback ID
(`auto_response_candidates`), each new SES inbound message marks matching
candidates ready, and only ready candidates are reconsidered. Discovery reads
use the read-only pool in bounded id windows with query deadlines, so the
writer connection stays available to normal requests. (0.13.61 rescanned every
historical bounce with an unindexable `LIKE` on each pass and starved the app.) It repairs historical message and recipient summaries in a
transaction, preserves raw events, and queues a correction with a stable event
ID. Gateway failures leave the correction pending for retry. Reconciliation
never resends mail or releases SDR sequence holds.

Before changing production, back up both app databases. After installation,
verify the affected messages through `message_get`: a single canonical recipient,
`opened` or `delivered` status, a typed `auto_replied` event retaining its raw
Bounce evidence, and no extra suppression. Verify the corresponding CRM channel
state and independently review any SDR hold before resuming a sequence.

## Validation

Run `go test ./... -short` separately in Messaging and CRM with `GOWORK=off`.
`testdata/auto_response_histories.json` contains sanitized versions of the three
reported histories. For a private forensic replay, set
`MESSAGING_INVESTIGATION_CASES` to the captured JSON file and run
`go test -run TestPrivateProductionAutoResponseReplay -v`. Customer captures must
remain outside the repository.
