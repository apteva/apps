-- Scoped summaries use a covering partial index; deleted rows are excluded.
CREATE INDEX idx_recordings_call_summary ON recordings(project_id,call_id,storage_status) WHERE deleted_at='';
CREATE INDEX idx_call_offers_call_active ON call_offers(project_id,call_id,expires_at,run_id,position) WHERE status='offered';

-- Read epochs invalidate bounded list/SSE caches even for direct SQL writes.
-- Audio diagnostic/heartbeat writes must not add cache churn on the media path.
CREATE TABLE telephony_read_versions(project_id TEXT PRIMARY KEY, revision INTEGER NOT NULL);
CREATE TRIGGER telephony_read_calls_insert AFTER INSERT ON calls BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_calls_update AFTER UPDATE OF id,thread_id,direction,carrier_slug,carrier_sid,carrier_leg_id,carrier_session_id,ingress_path,to_number,from_number,directive,voice,status,placed_at,answered_at,ended_at,project_id,error_message,recording_mode,termination_cause,termination_code,termination_initiator,media_status,media_error_message,media_connected_at,media_disconnected_at,media_close_code,media_close_reason,media_close_leg,peer_kind,routing_flow_id,routing_flow_version_id,routing_destination_id,answered_by,termination_reason,handling_reason,routing_resolution,callback_on_ai,machine_detection,hold_state,recording_control_state,control_error,recording_requested_at,max_duration_sec,duration_started_at,connected_deadline_at ON calls BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_calls_delete AFTER DELETE ON calls BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_call_offers_insert AFTER INSERT ON call_offers BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_call_offers_update AFTER UPDATE OF project_id,call_id,destination_id,destination_name,kind,agent_id,expires_at,status,run_id,position ON call_offers BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_call_offers_delete AFTER DELETE ON call_offers BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_call_ring_runs_insert AFTER INSERT ON call_ring_runs BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_call_ring_runs_update AFTER UPDATE OF project_id,call_id,status ON call_ring_runs BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_call_ring_runs_delete AFTER DELETE ON call_ring_runs BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_telephony_call_owners_insert AFTER INSERT ON telephony_call_owners BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_telephony_call_owners_update AFTER UPDATE OF project_id,call_id,principal,destination_id ON telephony_call_owners BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_telephony_call_owners_delete AFTER DELETE ON telephony_call_owners BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_routing_destinations_insert AFTER INSERT ON routing_destinations BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_routing_destinations_update AFTER UPDATE OF project_id,id,config_json ON routing_destinations BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_routing_destinations_delete AFTER DELETE ON routing_destinations BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_telephony_access_policies_insert AFTER INSERT ON telephony_access_policies BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_telephony_access_policies_update AFTER UPDATE OF project_id,revision,policy_json ON telephony_access_policies BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_telephony_access_policies_delete AFTER DELETE ON telephony_access_policies BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_recordings_insert AFTER INSERT ON recordings BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_recordings_update AFTER UPDATE OF project_id,call_id,storage_status,deleted_at ON recordings BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_recordings_delete AFTER DELETE ON recordings BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_call_control_settings_insert AFTER INSERT ON call_control_settings BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_call_control_settings_update AFTER UPDATE OF project_id,hold_music_url,hold_music_storage_file_id ON call_control_settings BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_call_control_settings_delete AFTER DELETE ON call_control_settings BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
