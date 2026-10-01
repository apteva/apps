-- Keep the five-minute retry worker on the small set of unfinished
-- soft-bounce actions instead of scanning every delivery-state row.
CREATE INDEX ix_channel_delivery_pending_suppression
  ON contact_channel_delivery_state(project_id, channel_id, transport)
  WHERE (quarantined = 1 AND suppressed = 0)
     OR (quarantined = 0 AND consecutive_soft_bounces = 0 AND suppressed = 1
         AND suppression_source = 'crm' AND suppression_reason = 'soft-bounce-threshold');
