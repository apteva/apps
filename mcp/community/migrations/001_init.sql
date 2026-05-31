-- community v0.1: multi-tenant community platform (Circle/Skool-shaped).
--
-- Tenancy: `communities` is the top-level table. Every other row carries
-- `community_id` directly so event-bus consumers and panel queries can
-- filter without joins. `project_id` (apteva scope) is the second axis
-- — communities live inside an apteva project; one project can host
-- multiple communities (one creator, many products / cohorts).
--
-- 0.1 surface: communities, members, spaces (feed|forum|chat), threads,
-- posts, reactions, and DMs. Courses, events, tiers, moderation,
-- billing, and notifications land in 0.2+.
--
-- IDs are TEXT (slug-like or UUID-shaped). The pattern across the
-- codebase is mixed, but TEXT ids make denormalisation cheap and avoid
-- the "did I forget to JOIN?" foot-gun on cross-app references.

CREATE TABLE IF NOT EXISTS communities (
    id           TEXT    PRIMARY KEY,                 -- "c_<random>"
    project_id   TEXT    NOT NULL,                    -- apteva project scope
    slug         TEXT    NOT NULL,                    -- url-safe identifier within (project_id)
    name         TEXT    NOT NULL,
    description  TEXT    NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at  TIMESTAMP,
    UNIQUE(project_id, slug)
);
CREATE INDEX IF NOT EXISTS idx_communities_project ON communities(project_id, archived_at);

CREATE TABLE IF NOT EXISTS members (
    id            TEXT    PRIMARY KEY,                -- "m_<random>"
    community_id  TEXT    NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    contact_id    TEXT,                               -- optional crm link
    handle        TEXT    NOT NULL,                   -- unique within community
    display_name  TEXT    NOT NULL DEFAULT '',
    bio           TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL DEFAULT 'active'
                  CHECK(status IN ('active','suspended','left')),
    joined_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at  TIMESTAMP,
    UNIQUE(community_id, handle)
);
CREATE INDEX IF NOT EXISTS idx_members_community ON members(community_id, status);

CREATE TABLE IF NOT EXISTS spaces (
    id            TEXT    PRIMARY KEY,                -- "s_<random>"
    community_id  TEXT    NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    slug          TEXT    NOT NULL,                   -- unique within community
    name          TEXT    NOT NULL,
    kind          TEXT    NOT NULL DEFAULT 'feed'
                  CHECK(kind IN ('feed','forum','chat')),
    visibility    TEXT    NOT NULL DEFAULT 'members'
                  CHECK(visibility IN ('public','members')),
    sort_order    INTEGER NOT NULL DEFAULT 0,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at   TIMESTAMP,
    UNIQUE(community_id, slug)
);
CREATE INDEX IF NOT EXISTS idx_spaces_community ON spaces(community_id, archived_at);

CREATE TABLE IF NOT EXISTS space_members (
    space_id   TEXT NOT NULL REFERENCES spaces(id)  ON DELETE CASCADE,
    member_id  TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    role       TEXT NOT NULL DEFAULT 'member'
               CHECK(role IN ('moderator','member')),
    joined_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(space_id, member_id)
);
CREATE INDEX IF NOT EXISTS idx_space_members_member ON space_members(member_id);

CREATE TABLE IF NOT EXISTS threads (
    id            TEXT    PRIMARY KEY,                -- "t_<random>"
    community_id  TEXT    NOT NULL,                   -- denormalised for fast filter
    space_id      TEXT    NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    author_id     TEXT    NOT NULL REFERENCES members(id),
    title         TEXT    NOT NULL DEFAULT '',
    pinned        INTEGER NOT NULL DEFAULT 0,
    locked        INTEGER NOT NULL DEFAULT 0,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_post_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    post_count    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_threads_space_active ON threads(space_id, pinned DESC, last_post_at DESC);
CREATE INDEX IF NOT EXISTS idx_threads_community   ON threads(community_id, last_post_at DESC);

CREATE TABLE IF NOT EXISTS posts (
    id            TEXT    PRIMARY KEY,                -- "p_<random>"
    community_id  TEXT    NOT NULL,                   -- denormalised
    thread_id     TEXT    NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    author_id     TEXT    NOT NULL REFERENCES members(id),
    body          TEXT    NOT NULL DEFAULT '',        -- markdown
    reply_to_id   TEXT    REFERENCES posts(id),
    removed_at    TIMESTAMP,                          -- soft-delete; body cleared
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    edited_at     TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_posts_thread   ON posts(thread_id, created_at);
CREATE INDEX IF NOT EXISTS idx_posts_author   ON posts(author_id, created_at DESC);

CREATE TABLE IF NOT EXISTS reactions (
    post_id    TEXT NOT NULL REFERENCES posts(id)   ON DELETE CASCADE,
    member_id  TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    emoji      TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(post_id, member_id, emoji)
);
CREATE INDEX IF NOT EXISTS idx_reactions_post ON reactions(post_id);

-- DMs are owned by the community app — no `messaging` dep required.
-- One thread can have 2+ participants (group DMs).
CREATE TABLE IF NOT EXISTS dm_threads (
    id              TEXT    PRIMARY KEY,              -- "dm_<random>"
    community_id    TEXT    NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_message_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_dm_threads_community ON dm_threads(community_id, last_message_at DESC);

CREATE TABLE IF NOT EXISTS dm_participants (
    dm_thread_id  TEXT NOT NULL REFERENCES dm_threads(id) ON DELETE CASCADE,
    member_id     TEXT NOT NULL REFERENCES members(id)   ON DELETE CASCADE,
    last_read_at  TIMESTAMP,
    PRIMARY KEY(dm_thread_id, member_id)
);
CREATE INDEX IF NOT EXISTS idx_dm_participants_member ON dm_participants(member_id);

CREATE TABLE IF NOT EXISTS dm_messages (
    id            TEXT    PRIMARY KEY,                -- "dmm_<random>"
    community_id  TEXT    NOT NULL,                   -- denormalised
    dm_thread_id  TEXT    NOT NULL REFERENCES dm_threads(id) ON DELETE CASCADE,
    author_id     TEXT    NOT NULL REFERENCES members(id),
    body          TEXT    NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_dm_messages_thread ON dm_messages(dm_thread_id, created_at);
