# Media v0.15.5 — component video pose sampling

Video pose requests embedded an absolute temporary source path inside encoded
JSON. Executor placement relocated the surrounding command, leaving FFmpeg
looking for the cached video at the old location. Encoded model and recovery
paths had the same weakness. Sampling could fail before the first pose result
and fall back to legacy cropping.

Remote requests now carry workspace-relative source, model and recovery paths.
The pose runtime resolves these after decoding against the actual request
location and the runtime directory passed by the relocated command. Escaping
workspace paths are rejected. Direct source URLs and local execution retain
their existing behavior. Executor v0.15.1 and its exact source pin are unchanged.

Frame failures retain FFmpeg exit codes, startup errno, timeout/empty-output
state and up to four allowlisted decoder messages. Both sides sanitize the
protocol before saving or exposing it; private paths, URLs and arbitrary stderr
are excluded. Compact crop receipts retain these details. Existing historical
stderr cannot be reconstructed.

Validation includes a regression that reproduced zero samples through the real
component before the fix, then passed cold and warm cache frame sampling using
real FFmpeg. Invalid-video diagnostics, retry/deadline/resource handling,
workspace escape rejection, receipt sanitization and actual MediaPipe landmark
inference at three timestamps on cold and warm component runs are covered.
Media uses App SDK v0.111.0, verified as the latest descendant tag.

No masters or existing reel outputs are modified or automatically rerendered.
