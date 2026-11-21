-- conversations v0.27: main routing and conversation thread lifecycle.
--
-- An agent's first qualifying operator chat is answered by the agent's main
-- thread. The binding row (thread_id='main') is the only record of that
-- routing; at most one conversation per agent may hold it.

-- DeleteConversation could leave bindings behind when SQLite foreign keys
-- were off. Their threads were already retired by the delete trigger.
DELETE FROM conversation_agent_threads
 WHERE conversation_id NOT IN (SELECT id FROM conversations);
-- Earlier releases never bound a conversation to main. Clear any stray row
-- defensively so the unique index below cannot fail on upgrade.
DELETE FROM conversation_agent_threads
 WHERE thread_id = 'main'
   AND rowid NOT IN (SELECT MIN(rowid) FROM conversation_agent_threads WHERE thread_id = 'main' GROUP BY agent_id);
CREATE UNIQUE INDEX conversation_agent_threads_main
    ON conversation_agent_threads(agent_id) WHERE thread_id = 'main';

-- Lifecycle of chat-* threads. 'dormant' means Conversations stopped the Core
-- thread after inactivity; the next inbound message recreates it.
ALTER TABLE conversation_agent_threads ADD COLUMN status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE conversation_agent_threads ADD COLUMN idle_check_at DATETIME;
ALTER TABLE conversation_agent_threads ADD COLUMN keep_until DATETIME;
ALTER TABLE conversation_agent_threads ADD COLUMN keep_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_agent_threads ADD COLUMN dormant_at DATETIME;

-- Retired threads are stopped in Core, not only refused here. Rows retired by
-- earlier releases are swept too, which cleans up threads leaked before v0.27.
ALTER TABLE retired_conversation_threads ADD COLUMN killed_at DATETIME;
ALTER TABLE retired_conversation_threads ADD COLUMN kill_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE retired_conversation_threads ADD COLUMN kill_error TEXT NOT NULL DEFAULT '';
ALTER TABLE retired_conversation_threads ADD COLUMN next_kill_at DATETIME;

-- Main is the agent's own thread. Deleting the conversation it answers must
-- release the routing without retiring (or later killing) main itself.
DELETE FROM retired_conversation_threads WHERE thread_id = 'main';
DROP TRIGGER IF EXISTS conversations_retired;
CREATE TRIGGER conversations_retired BEFORE DELETE ON conversations BEGIN
    INSERT OR IGNORE INTO retired_conversation_threads(project_id,agent_id,thread_id)
        SELECT OLD.project_id,agent_id,thread_id FROM conversation_agent_threads
        WHERE conversation_id=OLD.id AND thread_id!='main';
    INSERT OR IGNORE INTO retired_conversation_threads(project_id,agent_id,thread_id)
        SELECT OLD.project_id,OLD.lead_agent_id,OLD.thread_id WHERE OLD.thread_id!='' AND OLD.thread_id!='main';
END;

-- Archiving releases main routing so the agent's next new chat can take it.
-- Archived chat-* bindings stay authoritative owners until deletion.
CREATE TRIGGER conversations_archived_release_main AFTER UPDATE OF archived_at ON conversations
WHEN NEW.archived_at IS NOT NULL BEGIN
    DELETE FROM conversation_agent_threads WHERE conversation_id=NEW.id AND thread_id='main';
END;

-- Bridged conversations never run in main. Linking a transport or adding an
-- external participant releases main; the next message gets a chat thread.
CREATE TRIGGER telegram_binding_release_main AFTER INSERT ON telegram_bindings BEGIN
    DELETE FROM conversation_agent_threads WHERE conversation_id=NEW.conversation_id AND thread_id='main';
END;
CREATE TRIGGER external_participant_release_main AFTER INSERT ON participants
WHEN NEW.external_identity != '' BEGIN
    DELETE FROM conversation_agent_threads WHERE conversation_id=NEW.conversation_id AND thread_id='main';
END;
