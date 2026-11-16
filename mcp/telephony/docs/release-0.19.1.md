# Telephony 0.19.1

- Run the listener's once-a-second access check apart from its audio, so a
  slow database read no longer fills the 120 ms listener queue and drops
  frames. A 600 ms check lost 108 of 320 frames before and none now.
- Revocation still takes effect within about a second. If no check has started
  for 5 seconds, listener audio pauses until one completes
  (`unchecked_frames`).
- The periodic check and the policy-write sweep use the cached phone policy,
  narrowed to `call.listen` or `call.coach`, the same as the listen request.
  Supervisors admitted through a resource-grant number are no longer cut off.
- Listener diagnostics add `max_check_ms`, `slow_checks`, `max_queue_ms` and,
  for WebRTC listeners, `max_adapter_ms`. A healthy call pays one compare per
  frame and one timing per second.
