-- Candidate queues must remain small as completed history grows.
CREATE INDEX idx_calls_credentials_due ON calls(project_id,ended_at,id)
 WHERE ended_at<>'' AND (callback_secret<>'' OR audio_bridge_url<>'');
CREATE INDEX idx_calls_pending_answer ON calls(project_id,placed_at,id)
 WHERE direction='inbound' AND status='pending' AND handling_reason='';
CREATE INDEX idx_ai_handoffs_ready ON ai_handoffs(project_id,call_id) WHERE status='ready';
CREATE INDEX idx_recordings_import_candidates ON recordings(project_id,created_at,id)
 WHERE deleted_at='' AND provider_status='completed' AND storage_status IN ('pending','failed','provider_only');
CREATE INDEX idx_recordings_importing ON recordings(project_id,import_started_at,id) WHERE storage_status='importing';
CREATE INDEX idx_recordings_provider_cleanup ON recordings(project_id,cleanup_next_at,id)
 WHERE deleted_at='' AND storage_status='stored' AND provider_deleted_at='';
CREATE INDEX idx_recordings_retention_due ON recordings(project_id,retention_expires_at,id)
 WHERE deleted_at='' AND retention_expires_at<>'';
CREATE INDEX idx_recordings_deleted_cleanup ON recordings(project_id,cleanup_next_at,retention_expires_at,id)
 WHERE deleted_at<>'' AND (storage_file_id>0 OR provider_deleted_at='');
CREATE INDEX idx_calls_recording_reconcile ON calls(project_id,ended_at,id)
 WHERE carrier_slug IN ('twilio','telnyx','plivo') AND ingress_path<>'sip_direct' AND recording_mode='always'
 AND status IN ('completed','failed') AND carrier_sid<>'' AND ended_at<>'';
CREATE INDEX idx_calls_live_lifecycle ON calls(project_id,placed_at,id)
 WHERE status NOT IN ('completed','failed','no-answer','busy','canceled');
-- Protected calls get a later check, without starving eligible history.
ALTER TABLE calls ADD COLUMN housekeeping_after INTEGER NOT NULL DEFAULT 0;
UPDATE calls SET housekeeping_after=COALESCE(CAST(strftime('%s',ended_at) AS INTEGER)+2592000,0) WHERE ended_at<>'';
CREATE INDEX idx_calls_retention_due ON calls(project_id,housekeeping_after,id) WHERE ended_at<>'';
CREATE TRIGGER calls_retention_insert AFTER INSERT ON calls WHEN NEW.ended_at<>'' BEGIN
 UPDATE calls SET housekeeping_after=COALESCE(CAST(strftime('%s',NEW.ended_at) AS INTEGER)+2592000,0) WHERE id=NEW.id;
END;
CREATE TRIGGER calls_retention_end AFTER UPDATE OF ended_at ON calls WHEN NEW.ended_at<>'' AND NEW.ended_at<>OLD.ended_at BEGIN
 UPDATE calls SET housekeeping_after=COALESCE(CAST(strftime('%s',NEW.ended_at) AS INTEGER)+2592000,0) WHERE id=NEW.id;
END;
CREATE INDEX idx_inbound_burst_retention ON inbound_burst_attempts(project_id,received_at);
CREATE INDEX idx_inbound_cooldown_retention ON inbound_burst_cooldowns(project_id,expires_at);

-- A separate epoch also catches administrative/direct SQL edits that preserve
-- the public policy revision, plus delete/recreate of the same project.
CREATE TABLE telephony_policy_versions(project_id TEXT PRIMARY KEY,revision INTEGER NOT NULL);
INSERT INTO telephony_policy_versions SELECT project_id,1 FROM telephony_access_policies;
CREATE TRIGGER policy_cache_insert AFTER INSERT ON telephony_access_policies BEGIN
 INSERT INTO telephony_policy_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER policy_cache_update AFTER UPDATE OF policy_json,revision ON telephony_access_policies BEGIN
 INSERT INTO telephony_policy_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER policy_cache_delete AFTER DELETE ON telephony_access_policies BEGIN
 INSERT INTO telephony_policy_versions VALUES(OLD.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;

CREATE INDEX idx_calls_legacy_announcement ON calls(project_id,id) WHERE carrier_slug='telnyx' AND status IN ('pending','answered') AND announcement_state IN ('awaiting_answer','speaking','finishing') AND announcement_text<>'';
CREATE INDEX idx_call_legs_active ON call_legs(project_id,started_at,id) WHERE status NOT IN ('finished','canceled');
CREATE INDEX idx_ai_handoffs_work ON ai_handoffs(project_id,next_attempt_at,deadline_at,call_id) WHERE status IN ('running','retry') OR (status='failed' AND fallback_applied=0);
-- A bounded parent-delete batch must not trigger full child-history scans for
-- each foreign-key cascade. Project-leading indexes cannot satisfy call_id.
CREATE INDEX idx_call_offers_retention_fk ON call_offers(call_id);
CREATE INDEX idx_routing_effects_retention_fk ON routing_effects(call_id);
CREATE INDEX idx_carrier_command_events_retention_fk ON carrier_command_events(call_id);
CREATE INDEX idx_listener_sessions_retention_fk ON telephony_listener_sessions(call_id);
CREATE INDEX idx_listener_audit_retention_fk ON telephony_listener_audit(call_id);
CREATE INDEX idx_coaching_audit_retention_fk ON telephony_coaching_audit(call_id);
CREATE INDEX idx_browser_network_retention_fk ON telephony_browser_network_events(call_id);
CREATE INDEX idx_browser_samples_retention_fk ON telephony_browser_transport_samples(call_id);
