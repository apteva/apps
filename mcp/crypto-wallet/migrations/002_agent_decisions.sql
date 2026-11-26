-- 0.1.1: approvals come from the panel or from agents holding the
-- approvals.decide grant (no more Conversations cards). Record which agent
-- decided; approval_message_id columns stay for 0.1.0 rows but are unused.
ALTER TABLE approvals ADD COLUMN decided_by_agent INTEGER NOT NULL DEFAULT 0;
ALTER TABLE allowlist ADD COLUMN decided_by_agent INTEGER NOT NULL DEFAULT 0;
