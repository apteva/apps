# Telephony 0.6.4

This release adds generic softphone call controls, with Telnyx as the first
supported carrier. It does not install Telephony into a project or change a
partner application.

- Authorized call owners can hold and resume a connected human call without
  creating a new call. Telnyx plays configured MP3/WAV hold music to the
  customer; playback webhooks confirm the state. Browser/carrier audio is
  gated while held, including after a browser reconnect.
- Hold music can be a direct public HTTPS URL or an Apteva Storage file in the
  same project. Storage files are validated and resolved to a fresh signed,
  externally reachable HTTPS proxy URL for each hold; signed URLs are never
  stored in settings or exposed in call reads. Hold is unavailable until music
  is configured.
- Authorized call owners can pause and resume source-side recording on
  recording-enabled Telnyx calls. A successful Telnyx API response is treated
  as confirmation; failed or uncertain responses are not reported as paused.
- Call reads, live status, the Telephony client, and the headless softphone
  expose durable hold/recording states and carrier capability flags. Other
  carriers explicitly report unsupported controls.
- New delegated actions `call.hold` and `call.recording.control` must be granted
  to users who need them. Existing user grants remain unchanged.

Go and frontend tests cover authorization, repeated actions, carrier failures,
reconnect, hangup, and Storage validation. A live Telnyx call and inspection of
the saved recording are still required before claiming that a sensitive-data
pause excludes speech end to end; unit tests cannot prove carrier behavior.
