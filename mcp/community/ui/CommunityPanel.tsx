// CommunityPanel — the project.page panel for the community app.
//
// Layout (3-pane):
//   ┌─ left ─────┐ ┌─ middle ──────┐ ┌─ right ───────────────┐
//   │ community  │ │ threads list  │ │ posts in selected     │
//   │ switcher   │ │ (ordered by   │ │ thread, oldest-first  │
//   │ ──────     │ │ pinned, then  │ │                       │
//   │ spaces     │ │ last_post)    │ │ [compose] at bottom   │
//   │  · home    │ │               │ │                       │
//   │  · intros  │ │               │ │                       │
//   │ ──────     │ │               │ │                       │
//   │ dms (later)│ │               │ │                       │
//   └────────────┘ └───────────────┘ └───────────────────────┘
//
// Live updates: every mutation in the sidecar emits a typed event on
// the platform bus; this panel subscribes via useAppEvents and refetches
// the slice that changed. No polling.
//
// 0.1 surface: list communities → pick one → list spaces → list threads
// → list posts → react / reply. DMs panel lands in 0.1.1 (the data + bus
// events are already there, just needs a second sidebar tab).
//
// Theme: Tailwind tokens via className only (bg-bg, text-text-muted,
// border-border, etc). No inline style colors, no arbitrary Tailwind.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/community";

// ─── Inline app-events SSE hook ─────────────────────────────────────
// Lifted from the standard cross-app pattern. Multiplexed through
// window.__aptevaAppEvents when the dashboard hosts the panel; falls
// back to opening its own EventSource when the panel runs standalone.

interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

// ─── Types ──────────────────────────────────────────────────────────

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Community {
  id: string;
  slug: string;
  name: string;
  description: string;
  created_at: string;
  archived_at?: string;
}

interface Space {
  id: string;
  community_id: string;
  slug: string;
  name: string;
  kind: "feed" | "forum" | "chat" | "course";
  visibility: "public" | "members";
}

interface Section {
  id: string;
  space_id: string;
  title: string;
  position: number;
}

interface LessonProgress {
  lesson_id: string;
  member_id: string;
  status: "not_started" | "in_progress" | "complete";
  completed_at?: string;
  last_position_seconds?: number;
}

interface Lesson {
  id: string;
  community_id: string;
  section_id: string;
  title: string;
  body: string;
  video_storage_key?: string;
  video_duration_seconds?: number;
  position: number;
  published_at?: string;
  progress?: LessonProgress;
}

interface Thread {
  id: string;
  community_id: string;
  space_id: string;
  author_id: string;
  title: string;
  pinned: boolean;
  locked: boolean;
  last_post_at: string;
  post_count: number;
}

interface ReactionSummary {
  emoji: string;
  count: number;
  by: string[];
}

interface Post {
  id: string;
  thread_id: string;
  author_id: string;
  body: string;
  reply_to_id?: string;
  removed_at?: string;
  created_at: string;
  edited_at?: string;
  reactions?: ReactionSummary[];
}

// ─── helpers ────────────────────────────────────────────────────────

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(`${API}${path}`, { credentials: "include" });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json() as Promise<T>;
}

function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const m = Math.floor(s / 60);
  const h = Math.floor(m / 60);
  if (h > 0) return `${h}h${(m % 60).toString().padStart(2, "0")}m`;
  if (m > 0) return `${m}m${(s % 60).toString().padStart(2, "0")}s`;
  return `${s}s`;
}

// ─── Panel ──────────────────────────────────────────────────────────

export default function CommunityPanel({ projectId }: NativePanelProps) {
  const [communities, setCommunities] = useState<Community[]>([]);
  const [communityId, setCommunityId] = useState<string | null>(null);
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [spaceId, setSpaceId] = useState<string | null>(null);
  const [threads, setThreads] = useState<Thread[]>([]);
  const [threadId, setThreadId] = useState<string | null>(null);
  const [posts, setPosts] = useState<Post[]>([]);
  // Course state: parallel to threads/posts but for kind=course spaces.
  const [sections, setSections] = useState<Section[]>([]);
  const [lessons, setLessons] = useState<Lesson[]>([]);
  const [lessonId, setLessonId] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  // Load communities once on mount.
  useEffect(() => {
    getJSON<{ communities: Community[] }>("/communities")
      .then((d) => {
        setCommunities(d.communities);
        if (d.communities.length > 0 && !communityId) {
          setCommunityId(d.communities[0].id);
        }
      })
      .catch((e) => setLoadError(String(e)));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Load spaces whenever the selected community changes.
  const refreshSpaces = useCallback((cid: string) => {
    getJSON<{ spaces: Space[] }>(
      `/spaces?community_id=${encodeURIComponent(cid)}`,
    )
      .then((d) => {
        setSpaces(d.spaces);
        if (d.spaces.length > 0) {
          setSpaceId((prev) =>
            prev && d.spaces.some((s) => s.id === prev) ? prev : d.spaces[0].id,
          );
        } else {
          setSpaceId(null);
        }
      })
      .catch((e) => setLoadError(String(e)));
  }, []);
  useEffect(() => {
    if (!communityId) return;
    refreshSpaces(communityId);
  }, [communityId, refreshSpaces]);

  const activeSpace = useMemo(
    () => spaces.find((s) => s.id === spaceId) ?? null,
    [spaces, spaceId],
  );
  const isCourseSpace = activeSpace?.kind === "course";

  // Load threads when the space changes (only when it's not a course).
  const refreshThreads = useCallback((sid: string) => {
    getJSON<{ threads: Thread[] }>(
      `/threads?space_id=${encodeURIComponent(sid)}`,
    )
      .then((d) => {
        setThreads(d.threads);
        setThreadId((prev) =>
          prev && d.threads.some((t) => t.id === prev) ? prev : d.threads[0]?.id ?? null,
        );
      })
      .catch((e) => setLoadError(String(e)));
  }, []);
  useEffect(() => {
    if (!spaceId || isCourseSpace) {
      setThreads([]);
      setThreadId(null);
      return;
    }
    refreshThreads(spaceId);
  }, [spaceId, isCourseSpace, refreshThreads]);

  // Load course content when the selected space is a course.
  const refreshCourse = useCallback((sid: string) => {
    Promise.all([
      getJSON<{ sections: Section[] }>(
        `/sections?space_id=${encodeURIComponent(sid)}`,
      ),
      getJSON<{ lessons: Lesson[] }>(
        `/lessons?space_id=${encodeURIComponent(sid)}&include_drafts=true`,
      ),
    ])
      .then(([s, l]) => {
        setSections(s.sections);
        setLessons(l.lessons);
        setLessonId((prev) =>
          prev && l.lessons.some((x) => x.id === prev) ? prev : l.lessons[0]?.id ?? null,
        );
      })
      .catch((e) => setLoadError(String(e)));
  }, []);
  useEffect(() => {
    if (!spaceId || !isCourseSpace) {
      setSections([]);
      setLessons([]);
      setLessonId(null);
      return;
    }
    refreshCourse(spaceId);
  }, [spaceId, isCourseSpace, refreshCourse]);

  // Load posts when the thread changes.
  const refreshPosts = useCallback((tid: string) => {
    getJSON<{ posts: Post[] }>(
      `/posts?thread_id=${encodeURIComponent(tid)}`,
    )
      .then((d) => setPosts(d.posts))
      .catch((e) => setLoadError(String(e)));
  }, []);
  useEffect(() => {
    if (!threadId) {
      setPosts([]);
      return;
    }
    refreshPosts(threadId);
  }, [threadId, refreshPosts]);

  // Live event subscription. Filter on community_id at the payload
  // level, then route by topic to the slice that needs to refresh.
  useAppEvents<{ community_id?: string; space_id?: string; thread_id?: string }>(
    "community",
    projectId,
    (ev) => {
      const t = ev.topic;
      const d = ev.data ?? {};
      if (t === "community.community.created") {
        // New community in this project — refetch the list.
        getJSON<{ communities: Community[] }>("/communities")
          .then((res) => setCommunities(res.communities))
          .catch(() => {});
        return;
      }
      if (!communityId || (d.community_id && d.community_id !== communityId)) {
        return;
      }
      if (t === "community.space.created" || t === "community.space.member_added") {
        refreshSpaces(communityId);
        return;
      }
      if (spaceId && (t === "community.thread.created")) {
        if (!d.space_id || d.space_id === spaceId) refreshThreads(spaceId);
        return;
      }
      if (
        threadId &&
        d.thread_id === threadId &&
        (t === "community.post.created" ||
          t === "community.post.edited" ||
          t === "community.post.reacted" ||
          t === "community.post.removed")
      ) {
        refreshPosts(threadId);
        return;
      }
      // A new post anywhere in the current space bumps last_post_at →
      // refresh the threads list so ordering stays correct.
      if (t === "community.post.created" && spaceId) {
        refreshThreads(spaceId);
      }
      // Course events: anything that mutates sections or lessons in
      // the current course refreshes the outline.
      if (
        spaceId &&
        isCourseSpace &&
        (t === "community.section.created" ||
          t === "community.sections.reordered" ||
          t === "community.lesson.created" ||
          t === "community.lesson.updated" ||
          t === "community.lesson.published" ||
          t === "community.lesson.unpublished" ||
          t === "community.lesson.video_attached")
      ) {
        refreshCourse(spaceId);
      }
    },
  );

  const activeCommunity = useMemo(
    () => communities.find((c) => c.id === communityId) ?? null,
    [communities, communityId],
  );
  const activeThread = useMemo(
    () => threads.find((t) => t.id === threadId) ?? null,
    [threads, threadId],
  );
  const activeLesson = useMemo(
    () => lessons.find((l) => l.id === lessonId) ?? null,
    [lessons, lessonId],
  );
  const lessonsBySection = useMemo(() => {
    const m: Record<string, Lesson[]> = {};
    for (const l of lessons) {
      (m[l.section_id] ??= []).push(l);
    }
    for (const arr of Object.values(m)) arr.sort((a, b) => a.position - b.position);
    return m;
  }, [lessons]);

  return (
    <div className="flex h-full w-full bg-bg text-text">
      {/* Left rail */}
      <aside className="w-60 shrink-0 border-r border-border flex flex-col">
        <div className="p-3 border-b border-border">
          <label className="text-text-muted text-xs uppercase tracking-wide">
            Community
          </label>
          <select
            className="w-full mt-1 bg-bg-secondary border border-border rounded px-2 py-1"
            value={communityId ?? ""}
            onChange={(e) => setCommunityId(e.target.value || null)}
          >
            {communities.length === 0 ? (
              <option value="">— no communities yet —</option>
            ) : null}
            {communities.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        </div>
        <div className="p-3">
          <div className="text-text-muted text-xs uppercase tracking-wide mb-2">
            Spaces
          </div>
          {spaces.length === 0 ? (
            <div className="text-text-muted text-sm">No spaces yet.</div>
          ) : (
            <ul className="space-y-0.5">
              {spaces.map((s) => (
                <li key={s.id}>
                  <button
                    onClick={() => setSpaceId(s.id)}
                    className={
                      "w-full text-left px-2 py-1 rounded " +
                      (s.id === spaceId
                        ? "bg-bg-secondary text-text"
                        : "text-text-muted hover:bg-bg-secondary")
                    }
                  >
                    <span className="text-text-muted">#</span> {s.name}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </aside>

      {/* Middle pane: threads OR course outline */}
      <section className="w-80 shrink-0 border-r border-border flex flex-col">
        <header className="p-3 border-b border-border flex items-center justify-between">
          <div className="font-medium">
            {activeSpace?.name ?? (isCourseSpace ? "Lessons" : "Threads")}
          </div>
        </header>
        <div className="flex-1 overflow-auto">
          {isCourseSpace ? (
            sections.length === 0 ? (
              <div className="p-4 text-text-muted text-sm">
                No sections yet.
              </div>
            ) : (
              <ul>
                {sections.map((s) => (
                  <li key={s.id} className="border-b border-border">
                    <div className="px-3 py-2 text-xs uppercase tracking-wide text-text-muted">
                      {s.title}
                    </div>
                    <ul>
                      {(lessonsBySection[s.id] ?? []).map((l) => {
                        const status = l.progress?.status ?? "not_started";
                        return (
                          <li key={l.id}>
                            <button
                              onClick={() => setLessonId(l.id)}
                              className={
                                "w-full text-left px-3 py-2 flex items-center gap-2 " +
                                (l.id === lessonId
                                  ? "bg-bg-secondary"
                                  : "hover:bg-bg-secondary")
                              }
                            >
                              <span
                                className={
                                  "inline-block w-2 h-2 rounded-full shrink-0 border " +
                                  (status === "complete"
                                    ? "bg-text border-text"
                                    : status === "in_progress"
                                    ? "bg-text-muted border-text-muted"
                                    : "border-border")
                                }
                                aria-label={status}
                              />
                              <span className="text-sm truncate flex-1">
                                {l.title}
                                {!l.published_at ? (
                                  <span className="text-text-muted">
                                    {" "}(draft)
                                  </span>
                                ) : null}
                              </span>
                              {l.video_duration_seconds ? (
                                <span className="text-xs text-text-muted shrink-0">
                                  {formatDuration(l.video_duration_seconds)}
                                </span>
                              ) : null}
                            </button>
                          </li>
                        );
                      })}
                    </ul>
                  </li>
                ))}
              </ul>
            )
          ) : threads.length === 0 ? (
            <div className="p-4 text-text-muted text-sm">
              {spaceId ? "No threads yet." : "Pick a space."}
            </div>
          ) : (
            <ul>
              {threads.map((t) => (
                <li key={t.id}>
                  <button
                    onClick={() => setThreadId(t.id)}
                    className={
                      "w-full text-left px-3 py-2 border-b border-border " +
                      (t.id === threadId
                        ? "bg-bg-secondary"
                        : "hover:bg-bg-secondary")
                    }
                  >
                    <div className="text-sm font-medium truncate">
                      {t.pinned ? "★ " : ""}
                      {t.title || "(untitled thread)"}
                    </div>
                    <div className="text-xs text-text-muted">
                      {t.post_count} posts ·{" "}
                      {new Date(t.last_post_at).toLocaleString()}
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </section>

      {/* Right pane: thread posts OR lesson body */}
      <main className="flex-1 flex flex-col min-w-0">
        <header className="p-3 border-b border-border">
          <div className="font-medium truncate">
            {isCourseSpace
              ? activeLesson?.title || "Select a lesson"
              : activeThread?.title || "Select a thread"}
          </div>
          {activeCommunity ? (
            <div className="text-text-muted text-xs">
              {activeCommunity.name}
            </div>
          ) : null}
        </header>
        <div className="flex-1 overflow-auto p-3 space-y-3">
          {isCourseSpace ? (
            !activeLesson ? (
              <div className="text-text-muted text-sm">
                {lessons.length > 0 ? "Pick a lesson." : "No lessons in this course yet."}
              </div>
            ) : (
              <>
                {activeLesson.video_storage_key ? (
                  <div className="border border-border rounded bg-bg-secondary p-3">
                    <div className="text-xs uppercase tracking-wide text-text-muted mb-2">
                      Video
                    </div>
                    <div className="text-sm text-text-muted">
                      storage_key: <code>{activeLesson.video_storage_key}</code>
                      {activeLesson.video_duration_seconds ? (
                        <> · {formatDuration(activeLesson.video_duration_seconds)}</>
                      ) : null}
                    </div>
                  </div>
                ) : null}
                <article className="border border-border rounded p-3 bg-bg-secondary">
                  <div className="whitespace-pre-wrap text-sm">
                    {activeLesson.body || "(empty)"}
                  </div>
                </article>
                {activeLesson.progress ? (
                  <div className="text-xs text-text-muted">
                    progress: {activeLesson.progress.status}
                    {activeLesson.progress.completed_at
                      ? ` · completed ${new Date(activeLesson.progress.completed_at).toLocaleString()}`
                      : null}
                  </div>
                ) : null}
              </>
            )
          ) : threadId === null ? (
            <div className="text-text-muted text-sm">
              {threads.length > 0
                ? "Pick a thread to read it."
                : "No threads in this space yet."}
            </div>
          ) : posts.length === 0 ? (
            <div className="text-text-muted text-sm">No posts yet.</div>
          ) : (
            posts.map((p) => (
              <article
                key={p.id}
                className="border border-border rounded p-3 bg-bg-secondary"
              >
                <header className="text-xs text-text-muted mb-1">
                  {p.author_id} · {new Date(p.created_at).toLocaleString()}
                  {p.edited_at ? " · edited" : ""}
                  {p.removed_at ? " · removed" : ""}
                </header>
                <div className="whitespace-pre-wrap text-sm">{p.body}</div>
                {p.reactions && p.reactions.length > 0 ? (
                  <div className="flex gap-1 mt-2">
                    {p.reactions.map((r) => (
                      <span
                        key={r.emoji}
                        className="text-xs px-1.5 py-0.5 rounded border border-border text-text-muted"
                      >
                        {r.emoji} {r.count}
                      </span>
                    ))}
                  </div>
                ) : null}
              </article>
            ))
          )}
        </div>
      </main>
      {loadError ? (
        <div className="absolute bottom-2 right-2 text-xs px-2 py-1 rounded border border-border bg-bg text-text-muted">
          {loadError}
        </div>
      ) : null}
    </div>
  );
}
