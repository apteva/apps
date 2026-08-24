-- Generic attachment delivery contract for routed apps.
-- provider_ref is stable for inbound MIME/Twilio parts, so the partial
-- unique index makes webhook retries and redispatch reconciliation safe.

ALTER TABLE message_attachments
  ADD COLUMN processing_status TEXT NOT NULL DEFAULT 'ready';

ALTER TABLE message_attachments
  ADD COLUMN processing_error TEXT;

DELETE FROM message_attachments
WHERE provider_ref IS NOT NULL
  AND provider_ref <> ''
  AND id NOT IN (
    SELECT MIN(id)
    FROM message_attachments
    WHERE provider_ref IS NOT NULL AND provider_ref <> ''
    GROUP BY project_id, message_id, provider_ref
  );

CREATE UNIQUE INDEX IF NOT EXISTS ux_msg_attach_provider_ref
  ON message_attachments(project_id, message_id, provider_ref)
  WHERE provider_ref IS NOT NULL AND provider_ref <> '';
