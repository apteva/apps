-- A helper is titled by the reason its spawn call gave ("Archiving fan care
-- assignments"), not by the first line of its instructions. tokens and
-- tool_steps are summed from the helper's activity once it has ended.
ALTER TABLE conversation_workers ADD COLUMN label TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_workers ADD COLUMN tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE conversation_workers ADD COLUMN tool_steps INTEGER NOT NULL DEFAULT 0;
UPDATE conversation_workers SET label=COALESCE((
 SELECT t.reason FROM conversation_tool_activity t
 WHERE t.conversation_id=conversation_workers.conversation_id
   AND t.agent_id=conversation_workers.agent_id
   AND t.call_id=conversation_workers.spawn_call_id
   AND t.reason<>''
 ORDER BY t.id DESC LIMIT 1),'')
WHERE spawn_call_id<>'';
