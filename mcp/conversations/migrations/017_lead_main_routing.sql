-- conversations v0.27.8: some lead agents never answer chats from main.
--
-- The Apteva Helper has no project, and its main thread is not a chat thread,
-- so its chats always get their own chat-* thread. The create handler records
-- this from the agent list it already reads; main routing checks the column
-- inside the message transaction without asking the platform. Existing
-- conversations keep the previous behavior.
ALTER TABLE conversations ADD COLUMN main_routing INTEGER NOT NULL DEFAULT 1;
