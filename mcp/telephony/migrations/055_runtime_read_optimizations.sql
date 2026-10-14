-- Avoid invalidation when a watched value is assigned its existing value.
DROP TRIGGER telephony_read_calls_update;
CREATE TRIGGER telephony_read_calls_update AFTER UPDATE OF media_error_message,status,media_status,error_message,hold_state,recording_control_state,recording_requested_at,handling_reason,answered_at,ended_at,thread_id,peer_kind,duration_started_at,connected_deadline_at ON calls WHEN NEW.media_error_message IS NOT OLD.media_error_message OR NEW.status IS NOT OLD.status OR NEW.media_status IS NOT OLD.media_status OR NEW.error_message IS NOT OLD.error_message OR NEW.hold_state IS NOT OLD.hold_state OR NEW.recording_control_state IS NOT OLD.recording_control_state OR NEW.recording_requested_at IS NOT OLD.recording_requested_at OR NEW.handling_reason IS NOT OLD.handling_reason OR NEW.answered_at IS NOT OLD.answered_at OR NEW.ended_at IS NOT OLD.ended_at OR NEW.thread_id IS NOT OLD.thread_id OR NEW.peer_kind IS NOT OLD.peer_kind OR NEW.duration_started_at IS NOT OLD.duration_started_at OR NEW.connected_deadline_at IS NOT OLD.connected_deadline_at BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER telephony_read_calls_metadata_update AFTER UPDATE OF id,direction,carrier_slug,carrier_sid,carrier_leg_id,carrier_session_id,ingress_path,to_number,from_number,directive,voice,placed_at,project_id,recording_mode,termination_cause,termination_code,termination_initiator,media_connected_at,media_disconnected_at,media_close_code,media_close_reason,media_close_leg,routing_flow_id,routing_flow_version_id,routing_destination_id,answered_by,termination_reason,routing_resolution,callback_on_ai,machine_detection,control_error,max_duration_sec ON calls WHEN NEW.id IS NOT OLD.id OR NEW.direction IS NOT OLD.direction OR NEW.carrier_slug IS NOT OLD.carrier_slug OR NEW.carrier_sid IS NOT OLD.carrier_sid OR NEW.carrier_leg_id IS NOT OLD.carrier_leg_id OR NEW.carrier_session_id IS NOT OLD.carrier_session_id OR NEW.ingress_path IS NOT OLD.ingress_path OR NEW.to_number IS NOT OLD.to_number OR NEW.from_number IS NOT OLD.from_number OR NEW.directive IS NOT OLD.directive OR NEW.voice IS NOT OLD.voice OR NEW.placed_at IS NOT OLD.placed_at OR NEW.project_id IS NOT OLD.project_id OR NEW.recording_mode IS NOT OLD.recording_mode OR NEW.termination_cause IS NOT OLD.termination_cause OR NEW.termination_code IS NOT OLD.termination_code OR NEW.termination_initiator IS NOT OLD.termination_initiator OR NEW.media_connected_at IS NOT OLD.media_connected_at OR NEW.media_disconnected_at IS NOT OLD.media_disconnected_at OR NEW.media_close_code IS NOT OLD.media_close_code OR NEW.media_close_reason IS NOT OLD.media_close_reason OR NEW.media_close_leg IS NOT OLD.media_close_leg OR NEW.routing_flow_id IS NOT OLD.routing_flow_id OR NEW.routing_flow_version_id IS NOT OLD.routing_flow_version_id OR NEW.routing_destination_id IS NOT OLD.routing_destination_id OR NEW.answered_by IS NOT OLD.answered_by OR NEW.termination_reason IS NOT OLD.termination_reason OR NEW.routing_resolution IS NOT OLD.routing_resolution OR NEW.callback_on_ai IS NOT OLD.callback_on_ai OR NEW.machine_detection IS NOT OLD.machine_detection OR NEW.control_error IS NOT OLD.control_error OR NEW.max_duration_sec IS NOT OLD.max_duration_sec BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
DROP TRIGGER telephony_read_call_offers_update;
CREATE TRIGGER telephony_read_call_offers_update AFTER UPDATE OF project_id,call_id,destination_id,destination_name,kind,agent_id,expires_at,status,run_id,position ON call_offers WHEN NEW.project_id IS NOT OLD.project_id OR NEW.call_id IS NOT OLD.call_id OR NEW.destination_id IS NOT OLD.destination_id OR NEW.destination_name IS NOT OLD.destination_name OR NEW.kind IS NOT OLD.kind OR NEW.agent_id IS NOT OLD.agent_id OR NEW.expires_at IS NOT OLD.expires_at OR NEW.status IS NOT OLD.status OR NEW.run_id IS NOT OLD.run_id OR NEW.position IS NOT OLD.position BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
DROP TRIGGER telephony_read_call_ring_runs_update;
CREATE TRIGGER telephony_read_call_ring_runs_update AFTER UPDATE OF project_id,call_id,status ON call_ring_runs WHEN NEW.project_id IS NOT OLD.project_id OR NEW.call_id IS NOT OLD.call_id OR NEW.status IS NOT OLD.status BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
DROP TRIGGER telephony_read_telephony_call_owners_update;
CREATE TRIGGER telephony_read_telephony_call_owners_update AFTER UPDATE OF project_id,call_id,principal,destination_id ON telephony_call_owners WHEN NEW.project_id IS NOT OLD.project_id OR NEW.call_id IS NOT OLD.call_id OR NEW.principal IS NOT OLD.principal OR NEW.destination_id IS NOT OLD.destination_id BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
DROP TRIGGER telephony_read_routing_destinations_update;
CREATE TRIGGER telephony_read_routing_destinations_update AFTER UPDATE OF project_id,id,config_json ON routing_destinations WHEN NEW.project_id IS NOT OLD.project_id OR NEW.id IS NOT OLD.id OR NEW.config_json IS NOT OLD.config_json BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
DROP TRIGGER telephony_read_telephony_access_policies_update;
CREATE TRIGGER telephony_read_telephony_access_policies_update AFTER UPDATE OF project_id,revision,policy_json ON telephony_access_policies WHEN NEW.project_id IS NOT OLD.project_id OR NEW.revision IS NOT OLD.revision OR NEW.policy_json IS NOT OLD.policy_json BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
DROP TRIGGER telephony_read_recordings_update;
CREATE TRIGGER telephony_read_recordings_update AFTER UPDATE OF project_id,call_id,storage_status,deleted_at ON recordings WHEN NEW.project_id IS NOT OLD.project_id OR NEW.call_id IS NOT OLD.call_id OR NEW.storage_status IS NOT OLD.storage_status OR NEW.deleted_at IS NOT OLD.deleted_at BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
DROP TRIGGER telephony_read_call_control_settings_update;
CREATE TRIGGER telephony_read_call_control_settings_update AFTER UPDATE OF project_id,hold_music_url,hold_music_storage_file_id ON call_control_settings WHEN NEW.project_id IS NOT OLD.project_id OR NEW.hold_music_url IS NOT OLD.hold_music_url OR NEW.hold_music_storage_file_id IS NOT OLD.hold_music_storage_file_id BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT OLD.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 WHERE NEW.project_id<>OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
DROP TRIGGER policy_cache_update;
CREATE TRIGGER policy_cache_update AFTER UPDATE OF policy_json,revision ON telephony_access_policies WHEN NEW.policy_json IS NOT OLD.policy_json OR NEW.revision IS NOT OLD.revision BEGIN
 INSERT INTO telephony_policy_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;

CREATE TABLE telephony_work_versions(project_id TEXT PRIMARY KEY,revision INTEGER NOT NULL);
CREATE TABLE telephony_carrier_history_cursors (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, evidence_id TEXT NOT NULL UNIQUE,
 project_id TEXT NOT NULL, call_id TEXT NOT NULL
);
INSERT INTO telephony_carrier_history_cursors(evidence_id,project_id,call_id)
 SELECT id,project_id,call_id FROM telephony_carrier_transport_history ORDER BY occurred_at,id;
CREATE INDEX telephony_carrier_cursor_call ON telephony_carrier_history_cursors(project_id,call_id,seq);
CREATE TRIGGER carrier_history_cursor_insert AFTER INSERT ON telephony_carrier_transport_history BEGIN
 INSERT INTO telephony_carrier_history_cursors(evidence_id,project_id,call_id) VALUES(NEW.id,NEW.project_id,NEW.call_id);
END;
CREATE TRIGGER carrier_history_cursor_delete AFTER DELETE ON telephony_carrier_transport_history BEGIN
 DELETE FROM telephony_carrier_history_cursors WHERE evidence_id=OLD.id;
END;
CREATE INDEX idx_calls_read_order ON calls(project_id,placed_at DESC,id DESC);
CREATE TRIGGER work_epoch_calls_insert AFTER INSERT ON calls BEGIN
 INSERT INTO telephony_work_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_calls_delete AFTER DELETE ON calls BEGIN
 INSERT INTO telephony_work_versions VALUES(OLD.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_calls_update AFTER UPDATE OF status,media_status,media_generation,media_deadline_at,thread_id,peer_kind,carrier_slug,direction,audio_bridge_url,carrier_answered_at,media_connected_at,agent_id,route_id,project_id ON calls WHEN NEW.project_id IS NOT OLD.project_id OR NEW.status IS NOT OLD.status OR NEW.media_status IS NOT OLD.media_status OR NEW.media_generation IS NOT OLD.media_generation OR NEW.media_deadline_at IS NOT OLD.media_deadline_at OR NEW.thread_id IS NOT OLD.thread_id OR NEW.peer_kind IS NOT OLD.peer_kind OR NEW.carrier_slug IS NOT OLD.carrier_slug OR NEW.direction IS NOT OLD.direction OR NEW.audio_bridge_url IS NOT OLD.audio_bridge_url OR NEW.carrier_answered_at IS NOT OLD.carrier_answered_at OR NEW.media_connected_at IS NOT OLD.media_connected_at OR NEW.agent_id IS NOT OLD.agent_id OR NEW.route_id IS NOT OLD.route_id BEGIN
 INSERT INTO telephony_work_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_work_versions SELECT OLD.project_id,1 WHERE OLD.project_id IS NOT NEW.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_ai_call_policies_insert AFTER INSERT ON ai_call_policies BEGIN
 INSERT INTO telephony_work_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_ai_call_policies_delete AFTER DELETE ON ai_call_policies BEGIN
 INSERT INTO telephony_work_versions VALUES(OLD.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_ai_call_policies_update AFTER UPDATE OF stage,project_id ON ai_call_policies WHEN NEW.project_id IS NOT OLD.project_id OR NEW.stage IS NOT OLD.stage BEGIN
 INSERT INTO telephony_work_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_work_versions SELECT OLD.project_id,1 WHERE OLD.project_id IS NOT NEW.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_ai_handoffs_insert AFTER INSERT ON ai_handoffs BEGIN
 INSERT INTO telephony_work_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_ai_handoffs_delete AFTER DELETE ON ai_handoffs BEGIN
 INSERT INTO telephony_work_versions VALUES(OLD.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_ai_handoffs_update AFTER UPDATE OF status,next_attempt_at,deadline_at,fallback_applied,project_id ON ai_handoffs WHEN NEW.project_id IS NOT OLD.project_id OR NEW.status IS NOT OLD.status OR NEW.next_attempt_at IS NOT OLD.next_attempt_at OR NEW.deadline_at IS NOT OLD.deadline_at OR NEW.fallback_applied IS NOT OLD.fallback_applied BEGIN
 INSERT INTO telephony_work_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_work_versions SELECT OLD.project_id,1 WHERE OLD.project_id IS NOT NEW.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_carrier_media_bridges_insert AFTER INSERT ON carrier_media_bridges BEGIN
 INSERT INTO telephony_work_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_carrier_media_bridges_delete AFTER DELETE ON carrier_media_bridges BEGIN
 INSERT INTO telephony_work_versions VALUES(OLD.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_carrier_media_bridges_update AFTER UPDATE OF state,next_attempt_at,deadline_at,project_id ON carrier_media_bridges WHEN NEW.project_id IS NOT OLD.project_id OR NEW.state IS NOT OLD.state OR NEW.next_attempt_at IS NOT OLD.next_attempt_at OR NEW.deadline_at IS NOT OLD.deadline_at BEGIN
 INSERT INTO telephony_work_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_work_versions SELECT OLD.project_id,1 WHERE OLD.project_id IS NOT NEW.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_carrier_activations_insert AFTER INSERT ON carrier_activations BEGIN
 INSERT INTO telephony_work_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_carrier_activations_delete AFTER DELETE ON carrier_activations BEGIN
 INSERT INTO telephony_work_versions VALUES(OLD.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER work_epoch_carrier_activations_update AFTER UPDATE OF status,next_attempt_at,deadline_at,project_id ON carrier_activations WHEN NEW.project_id IS NOT OLD.project_id OR NEW.status IS NOT OLD.status OR NEW.next_attempt_at IS NOT OLD.next_attempt_at OR NEW.deadline_at IS NOT OLD.deadline_at BEGIN
 INSERT INTO telephony_work_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_work_versions SELECT OLD.project_id,1 WHERE OLD.project_id IS NOT NEW.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;

CREATE TABLE telephony_audio_versions(project_id TEXT PRIMARY KEY,revision INTEGER NOT NULL);
CREATE TRIGGER audio_report_epoch_insert AFTER INSERT ON telephony_audio_reports BEGIN
 INSERT INTO telephony_audio_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER audio_report_epoch_update AFTER UPDATE ON telephony_audio_reports BEGIN
 INSERT INTO telephony_audio_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
 INSERT INTO telephony_audio_versions SELECT OLD.project_id,1 WHERE NEW.project_id IS NOT OLD.project_id ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER audio_report_epoch_delete AFTER DELETE ON telephony_audio_reports BEGIN
 INSERT INTO telephony_audio_versions VALUES(OLD.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
