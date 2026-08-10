-- Community v0.5: verified Auth membership and concurrency-safe DM identity.

ALTER TABLE members ADD COLUMN auth_user_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS ux_members_community_auth_user
    ON members(community_id, auth_user_id)
    WHERE auth_user_id IS NOT NULL;

ALTER TABLE dm_threads ADD COLUMN participant_key TEXT;
UPDATE dm_threads
   SET participant_key = (
       SELECT group_concat(member_id, '|')
         FROM (
             SELECT member_id
               FROM dm_participants
              WHERE dm_thread_id = dm_threads.id
              ORDER BY member_id
         )
   )
 WHERE participant_key IS NULL;

-- Older versions avoided duplicate participant sets with an application-level
-- lookup, which could still race. Merge any duplicates before adding the
-- unique index so existing installs upgrade without losing messages.
UPDATE dm_messages
   SET dm_thread_id = (
       SELECT keep.id
         FROM dm_threads keep
        WHERE keep.community_id = dm_messages.community_id
          AND keep.participant_key = (
              SELECT old.participant_key
                FROM dm_threads old
               WHERE old.id = dm_messages.dm_thread_id
          )
        ORDER BY keep.created_at, keep.id
        LIMIT 1
   )
 WHERE dm_thread_id <> (
       SELECT keep.id
         FROM dm_threads keep
        WHERE keep.community_id = dm_messages.community_id
          AND keep.participant_key = (
              SELECT old.participant_key
                FROM dm_threads old
               WHERE old.id = dm_messages.dm_thread_id
          )
        ORDER BY keep.created_at, keep.id
        LIMIT 1
   );

UPDATE dm_participants AS keep
   SET last_read_at = (
       SELECT MAX(other_part.last_read_at)
         FROM dm_participants other_part
         JOIN dm_threads other_thread ON other_thread.id = other_part.dm_thread_id
         JOIN dm_threads keep_thread ON keep_thread.id = keep.dm_thread_id
        WHERE other_thread.community_id = keep_thread.community_id
          AND other_thread.participant_key = keep_thread.participant_key
          AND other_part.member_id = keep.member_id
   )
 WHERE keep.dm_thread_id = (
       SELECT canonical.id
         FROM dm_threads canonical
         JOIN dm_threads current ON current.id = keep.dm_thread_id
        WHERE canonical.community_id = current.community_id
          AND canonical.participant_key = current.participant_key
        ORDER BY canonical.created_at, canonical.id
        LIMIT 1
   );

UPDATE dm_threads AS keep
   SET last_message_at = (
       SELECT MAX(other.last_message_at)
         FROM dm_threads other
        WHERE other.community_id = keep.community_id
          AND other.participant_key = keep.participant_key
   )
 WHERE keep.id = (
       SELECT canonical.id
         FROM dm_threads canonical
        WHERE canonical.community_id = keep.community_id
          AND canonical.participant_key = keep.participant_key
        ORDER BY canonical.created_at, canonical.id
        LIMIT 1
   );

DELETE FROM dm_threads
 WHERE id <> (
       SELECT canonical.id
         FROM dm_threads canonical
        WHERE canonical.community_id = dm_threads.community_id
          AND canonical.participant_key = dm_threads.participant_key
        ORDER BY canonical.created_at, canonical.id
        LIMIT 1
   );

CREATE UNIQUE INDEX IF NOT EXISTS ux_dm_threads_participant_set
    ON dm_threads(community_id, participant_key)
    WHERE participant_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS issued_certificates (
    id          TEXT PRIMARY KEY,
    space_id    TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    member_id   TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    body        TEXT NOT NULL DEFAULT '',
    issued_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(space_id, member_id)
);
