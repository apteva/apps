-- Audience class (0.6.0): who a conversation's human side is.
--   operator — dashboard users, agent-created topic conversations,
--              operator-bound external channels. The inbox surface.
--   public   — end users of a deployed product (site chatbot visitors
--              behind a gateway app, and similar). Inbox-kind items
--              (alert / report / approval) are structurally refused
--              here; agents reply with conversations_send only and
--              escalate via an operator conversation.
ALTER TABLE conversations ADD COLUMN audience TEXT NOT NULL DEFAULT 'operator';
