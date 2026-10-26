-- Track only the soft-bounce contribution actually applied to CRM. A delayed
-- correction can undo that one event without clearing other delivery evidence.
CREATE TABLE crm_delivery_event_contributions (
 project_id TEXT NOT NULL,
 source_install_id INTEGER NOT NULL,
 event_id TEXT NOT NULL,
 channel_id INTEGER NOT NULL,
 transport TEXT NOT NULL,
 occurred_at TEXT NOT NULL,
 active_soft INTEGER NOT NULL DEFAULT 0,
 corrected INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(project_id,source_install_id,event_id)
);
CREATE INDEX ix_crm_delivery_contributions_channel
 ON crm_delivery_event_contributions(project_id,channel_id,transport,active_soft);
-- A correction can reduce a quarantined route to one or two genuine soft
-- bounces. Retry removal of the threshold suppression in those cases too.
DROP INDEX ix_channel_delivery_pending_suppression;
CREATE INDEX ix_channel_delivery_pending_suppression
 ON contact_channel_delivery_state(project_id,channel_id,transport)
 WHERE (quarantined=1 AND suppressed=0)
 OR (quarantined=0 AND consecutive_soft_bounces<3 AND suppressed=1
     AND suppression_source='crm' AND suppression_reason='soft-bounce-threshold');
