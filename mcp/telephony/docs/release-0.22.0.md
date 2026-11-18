# Telephony 0.22.0

- Automatic pickup no longer switches itself off after one failed check-in.
  An available browser checks in every 10 s to keep its 30 s claim. Before,
  any failed check-in (a network blip, a timeout or a 503) stopped automatic
  pickup for good, while the server still held the claim for up to 20 s more.
  Now a check-in that gets no answer, times out or gets a server error is
  retried after about 2 s, then about 5 s, while the claim still has time.
  Offers, the call watcher and the microphone stay as they are during
  retries, so a call in its countdown or warning is not dropped.
- Every block says why, in `snapshot.autoAnswer.code`:
  `heartbeat_failed` (the server could not be reached before the claim ran
  out), `moved_to_another_browser`, `session_rejected` (the server refused the
  check-in) or `prepare_failed` (microphone or playback). After
  `heartbeat_failed` the softphone registers again every 30–60 s while no
  call is active, so a temporary block fixes itself without a reload. The
  other codes wait for the adviser. The Calls panel shows **Retry now** for a
  temporary block and **Use this browser** for the others.
- Check-ins no longer wait on the install-wide auto-answer lock. That lock is
  held by call reservations, policy reads and automatic pick-ups, and in
  versions before 0.21.0 by every manual pick-up too; a check-in queued behind
  them could miss its 5 s browser timeout. A check-in is now one database
  statement instead of two: it renews this browser's claim, replaces a claim
  that has lapsed, or reports that another browser holds it. Housekeeping
  deletes lapsed claims every minute.
- The server logs every check-in it refuses (`auto_answer checkin
  not_designated`), fails (`auto_answer checkin failed`, with the statement
  and error) or holds for 1 s or more (`auto_answer checkin slow`), with the
  elapsed time and a hashed principal and session. Before, these were silent.
