# Telephony 0.28.0

- Carrier callback URLs name their installation. Inbound route webhooks
  (Telnyx `webhook_event_url`, Twilio/Plivo/Bandwidth voice and status URLs),
  per-call status, recording, IVR and XML callbacks now use
  `<public_url>/api/apps/telephony/_install/<id>/...` instead of the name-only
  `/api/apps/telephony/...?project_id=`. The platform resolves the name-only
  form to the project's default install, so a second telephony install in the
  same project used to receive none of its own carrier callbacks.
- Routes configured before 0.28.0 keep the name-only URL at the carrier until
  they are configured again. Those requests still work on the project's default
  install, and Twilio/Plivo signatures are accepted over either URL form.
- Media stream URLs were already install-scoped and are unchanged.
