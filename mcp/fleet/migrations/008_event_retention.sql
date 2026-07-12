-- fleet 008: retain the newest 1000 audit events per tenant.

CREATE TRIGGER IF NOT EXISTS fleet_events_retain_recent
AFTER INSERT ON fleet_events
BEGIN
    DELETE FROM fleet_events
    WHERE tenant_id = NEW.tenant_id
      AND id < COALESCE((
          SELECT id
          FROM fleet_events
          WHERE tenant_id = NEW.tenant_id
          ORDER BY id DESC
          LIMIT 1 OFFSET 999
      ), 0);
END;
