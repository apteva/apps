-- A realtime voice thread is temporary, but its owner is an existing chat.
-- Keep ended bindings for authorization/audit; only one live session per chat.
CREATE TABLE conversation_voice_sessions (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    user_id INTEGER NOT NULL,
    agent_id INTEGER NOT NULL,
    thread_id TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'starting',
    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_heartbeat_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at DATETIME,
    last_event_at DATETIME
);
CREATE UNIQUE INDEX conversation_voice_active_chat ON conversation_voice_sessions(conversation_id)
    WHERE status IN ('starting', 'active');
-- Once a voice child ends it must never regain the broad, unbound-thread
-- participant access that legacy agent tools grant other opaque threads.
CREATE TRIGGER conversation_voice_retired AFTER UPDATE OF status ON conversation_voice_sessions
WHEN NEW.status = 'ended' BEGIN
    INSERT OR IGNORE INTO retired_conversation_threads(project_id,agent_id,thread_id)
    VALUES(NEW.project_id,NEW.agent_id,NEW.thread_id);
END;
CREATE TRIGGER conversation_voice_deleted BEFORE DELETE ON conversation_voice_sessions BEGIN
    INSERT OR IGNORE INTO retired_conversation_threads(project_id,agent_id,thread_id)
    VALUES(OLD.project_id,OLD.agent_id,OLD.thread_id);
END;
