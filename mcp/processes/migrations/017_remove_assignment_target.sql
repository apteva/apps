-- Procedure parameters are the only assignment-specific execution inputs.
-- Remove only the obsolete top-level label; a declared parameter named target
-- remains valid. Immutable delivery envelopes keep their original bytes so a
-- retry cannot change content for an event already accepted by the platform.
UPDATE process_assignments SET body_json=json_remove(body_json,'$.target')
 WHERE json_type(body_json,'$.target') IS NOT NULL;
UPDATE process_runs SET assignment_json=json_remove(assignment_json,'$.target')
 WHERE json_type(assignment_json,'$.target') IS NOT NULL;
