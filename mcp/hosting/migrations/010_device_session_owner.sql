-- Which tenant a phone's Hosting session belongs to. Only that tenant is
-- shown the session id; a session Hosting opened to inspect or clean the
-- phone has no owner and is never handed out.
ALTER TABLE hosting_machines ADD COLUMN device_session_owner TEXT NOT NULL DEFAULT '';
-- A phone rented under 2.5.0 holds its tenant's session while assigned.
UPDATE hosting_machines SET device_session_owner = tenant_id
  WHERE device_session != '' AND tenant_id IS NOT NULL AND state = 'assigned';
