-- v0.13.37: make provider retries idempotent for inbound messages.
--
-- Remove duplicate retry rows before installing uniqueness constraints.
DELETE FROM messages
 WHERE direction = 'in' AND s3_key IS NOT NULL
   AND id NOT IN (
     SELECT MIN(id) FROM messages
      WHERE direction = 'in' AND s3_key IS NOT NULL
      GROUP BY project_id, s3_key
   );

DELETE FROM messages
 WHERE direction = 'in' AND message_id_header IS NOT NULL
   AND id NOT IN (
     SELECT MIN(id) FROM messages
      WHERE direction = 'in' AND message_id_header IS NOT NULL
      GROUP BY project_id, message_id_header
   );

DELETE FROM messages
 WHERE direction = 'in' AND provider_message_id IS NOT NULL
   AND id NOT IN (
     SELECT MIN(id) FROM messages
      WHERE direction = 'in' AND provider_message_id IS NOT NULL
      GROUP BY project_id, provider_message_id
   );

CREATE UNIQUE INDEX IF NOT EXISTS ux_msg_inbound_s3_key
  ON messages(project_id, s3_key)
  WHERE direction = 'in' AND s3_key IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS ux_msg_inbound_msgid
  ON messages(project_id, message_id_header)
  WHERE direction = 'in' AND message_id_header IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS ux_msg_inbound_provider_id
  ON messages(project_id, provider_message_id)
  WHERE direction = 'in' AND provider_message_id IS NOT NULL;
