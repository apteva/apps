// CommunityPanel — project.page operator surface for the community app.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent, KeyboardEvent, ReactNode } from "react";

const API = "/api/apps/community";

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
    if (bridge) return bridge.subscribe(app, projectId, handler);

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

interface Member {
  id: string;
  community_id: string;
  auth_user_id?: string;
  handle: string;
  display_name: string;
  bio: string;
  status: string;
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

interface CourseOffer {
  space_id: string;
  catalog_product_id: number;
  catalog_price_id: number;
  product_name: string;
  price_nickname?: string;
  unit_amount_cents: number;
  currency: string;
  active: boolean;
}

interface CoursePurchase {
  id: string;
  member_id: string;
  customer_email: string;
  status: string;
  unit_amount_cents: number;
  currency: string;
  created_at: string;
}

interface MembershipPlan {
  id: string;
  name: string;
  description: string;
  catalog_price_id: number;
  unit_amount_cents: number;
  currency: string;
  interval: string;
  interval_count: number;
  scope_type: "all_courses" | "selected_courses" | "course_tags";
  active: boolean;
}

interface MemberSubscription {
  id: string;
  member_id: string;
  plan_id: string;
  status: string;
  cancel_at?: string;
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

type DialogKind =
  | "community"
  | "member"
  | "space"
  | "course"
  | "thread"
  | "section"
  | "lesson"
  | "edit_lesson"
  | "video"
  | "offer"
  | "membership_plan";

async function getJSON<T>(path: string, projectId: string): Promise<T> {
  const separator = path.includes("?") ? "&" : "?";
  const res = await fetch(
    `${API}${path}${separator}project_id=${encodeURIComponent(projectId)}`,
    {
    credentials: "same-origin",
    cache: "no-store",
    },
  );
  if (!res.ok) throw new Error(`${path}: ${res.status}: ${await res.text()}`);
  return res.json() as Promise<T>;
}

function mcpErrorText(v: unknown): string {
  if (!v || typeof v !== "object") return "";
  const obj = v as Record<string, unknown>;
  if (typeof obj.error === "string") return obj.error;
  const err = obj.error;
  if (err && typeof err === "object" && typeof (err as Record<string, unknown>).message === "string") {
    return String((err as Record<string, unknown>).message);
  }
  return "";
}

async function callTool<T>(
  tool: string,
  args: Record<string, unknown>,
  projectId: string,
): Promise<T> {
  const res = await fetch(`${API}/mcp`, {
    method: "POST",
    credentials: "same-origin",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: 1,
      method: "tools/call",
      params: { name: tool, arguments: { ...args, _project_id: projectId } },
    }),
  });
  if (!res.ok) throw new Error(`${tool}: ${res.status}: ${await res.text()}`);
  const j = await res.json();
  if (j.error) throw new Error(j.error.message || tool);
  const text = j.result?.content?.[0]?.text;
  const parsed = text ? JSON.parse(text) : j.result;
  const err = mcpErrorText(parsed);
  if (err) throw new Error(err);
  return parsed as T;
}

function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const m = Math.floor(s / 60);
  const h = Math.floor(m / 60);
  if (h > 0) return `${h}h${(m % 60).toString().padStart(2, "0")}m`;
  if (m > 0) return `${m}m${(s % 60).toString().padStart(2, "0")}s`;
  return `${s}s`;
}

function formatMoney(cents: number, currency: string): string {
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency }).format(cents / 100);
  } catch {
    return `${currency} ${(cents / 100).toFixed(2)}`;
  }
}

function slugify(value: string): string {
  return value
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63);
}

function buttonClass(tone: "primary" | "secondary" | "ghost" = "secondary") {
  if (tone === "primary") return "px-2.5 py-1.5 rounded bg-accent text-bg text-sm font-medium disabled:opacity-50";
  if (tone === "ghost") return "px-2 py-1 rounded text-text-muted hover:bg-bg-secondary text-sm disabled:opacity-50";
  return "px-2.5 py-1.5 rounded border border-border text-sm text-text hover:bg-bg-secondary disabled:opacity-50";
}

export default function CommunityPanel({ projectId }: NativePanelProps) {
  const [communities, setCommunities] = useState<Community[]>([]);
  const [communityId, setCommunityId] = useState<string | null>(null);
  const [members, setMembers] = useState<Member[]>([]);
  const [memberId, setMemberId] = useState<string>("");
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [spaceId, setSpaceId] = useState<string | null>(null);
  const [threads, setThreads] = useState<Thread[]>([]);
  const [threadId, setThreadId] = useState<string | null>(null);
  const [posts, setPosts] = useState<Post[]>([]);
  const [sections, setSections] = useState<Section[]>([]);
  const [lessons, setLessons] = useState<Lesson[]>([]);
  const [courseOffer, setCourseOffer] = useState<CourseOffer | null>(null);
  const [coursePurchases, setCoursePurchases] = useState<CoursePurchase[]>([]);
  const [membershipPlans, setMembershipPlans] = useState<MembershipPlan[]>([]);
  const [memberSubscriptions, setMemberSubscriptions] = useState<MemberSubscription[]>([]);
  const [lessonId, setLessonId] = useState<string | null>(null);
  const [newLessonSectionId, setNewLessonSectionId] = useState("");
  const [dialog, setDialog] = useState<DialogKind | null>(null);
  const [status, setStatus] = useState("Loading");
  const [postBody, setPostBody] = useState("");

  const fail = useCallback((prefix: string, err: unknown) => {
    setStatus(`${prefix}: ${(err as Error).message || String(err)}`);
  }, []);

  const refreshCommunities = useCallback(async () => {
    const d = await getJSON<{ communities: Community[] }>("/communities", projectId);
    setCommunities(d.communities || []);
    setCommunityId((prev) =>
      prev && d.communities.some((c) => c.id === prev) ? prev : d.communities[0]?.id ?? null,
    );
    setStatus(`${d.communities.length} communities`);
    return d.communities || [];
  }, [projectId]);

  const refreshMembers = useCallback(async (cid: string) => {
    const d = await getJSON<{ members: Member[] }>(
      `/members?community_id=${encodeURIComponent(cid)}&status=active`,
      projectId,
    );
    setMembers(d.members || []);
    setMemberId((prev) =>
      prev && d.members.some((m) => m.id === prev) ? prev : d.members[0]?.id ?? "",
    );
    return d.members || [];
  }, [projectId]);

  const refreshSpaces = useCallback(async (cid: string) => {
    const d = await getJSON<{ spaces: Space[] }>(
      `/spaces?community_id=${encodeURIComponent(cid)}`,
      projectId,
    );
    setSpaces(d.spaces || []);
    setSpaceId((prev) =>
      prev && d.spaces.some((s) => s.id === prev) ? prev : d.spaces[0]?.id ?? null,
    );
    return d.spaces || [];
  }, [projectId]);

  const refreshMemberships = useCallback(async (cid: string) => {
    const [plansOut, subscriptionsOut] = await Promise.all([
      callTool<{ plans: MembershipPlan[] }>("membership_plans_list", { community_id: cid }, projectId),
      callTool<{ subscriptions: MemberSubscription[] }>("membership_subscriptions_list", { community_id: cid, limit: 100 }, projectId),
    ]);
    setMembershipPlans(plansOut.plans || []);
    setMemberSubscriptions(subscriptionsOut.subscriptions || []);
  }, [projectId]);

  const refreshThreads = useCallback(async (sid: string) => {
    const d = await getJSON<{ threads: Thread[] }>(
      `/threads?space_id=${encodeURIComponent(sid)}`,
      projectId,
    );
    setThreads(d.threads || []);
    setThreadId((prev) =>
      prev && d.threads.some((t) => t.id === prev) ? prev : d.threads[0]?.id ?? null,
    );
    return d.threads || [];
  }, [projectId]);

  const refreshCourse = useCallback(async (sid: string) => {
    const [s, l, offerOut, purchasesOut] = await Promise.all([
      getJSON<{ sections: Section[] }>(
        `/sections?space_id=${encodeURIComponent(sid)}`,
        projectId,
      ),
      getJSON<{ lessons: Lesson[] }>(
        `/lessons?space_id=${encodeURIComponent(sid)}&include_drafts=true`,
        projectId,
      ),
      callTool<{ offer: CourseOffer | null }>("course_offer_get", { space_id: sid }, projectId),
      callTool<{ purchases: CoursePurchase[] }>("course_purchases_list", { space_id: sid, limit: 100 }, projectId),
    ]);
    setSections(s.sections || []);
    setLessons(l.lessons || []);
    setCourseOffer(offerOut.offer || null);
    setCoursePurchases(purchasesOut.purchases || []);
    setLessonId((prev) =>
      prev && l.lessons.some((x) => x.id === prev) ? prev : l.lessons[0]?.id ?? null,
    );
    return l.lessons || [];
  }, [projectId]);

  const refreshPosts = useCallback(async (tid: string) => {
    const d = await getJSON<{ posts: Post[] }>(
      `/posts?thread_id=${encodeURIComponent(tid)}`,
      projectId,
    );
    setPosts(d.posts || []);
    return d.posts || [];
  }, [projectId]);

  useEffect(() => {
    refreshCommunities().catch((e) => fail("Load communities", e));
  }, [refreshCommunities, fail]);

  useEffect(() => {
    if (!communityId) {
      setMembers([]);
      setSpaces([]);
      setMembershipPlans([]);
      setMemberSubscriptions([]);
      setSpaceId(null);
      return;
    }
    Promise.all([refreshMembers(communityId), refreshSpaces(communityId), refreshMemberships(communityId)]).catch((e) =>
      fail("Load community", e),
    );
  }, [communityId, refreshMembers, refreshSpaces, refreshMemberships, fail]);

  const activeCommunity = useMemo(
    () => communities.find((c) => c.id === communityId) ?? null,
    [communities, communityId],
  );
  const activeSpace = useMemo(
    () => spaces.find((s) => s.id === spaceId) ?? null,
    [spaces, spaceId],
  );
  const isCourseSpace = activeSpace?.kind === "course";

  useEffect(() => {
    if (!spaceId || isCourseSpace) {
      setThreads([]);
      setThreadId(null);
      return;
    }
    refreshThreads(spaceId).catch((e) => fail("Load threads", e));
  }, [spaceId, isCourseSpace, refreshThreads, fail]);

  useEffect(() => {
    if (!spaceId || !isCourseSpace) {
      setSections([]);
      setLessons([]);
      setCourseOffer(null);
      setCoursePurchases([]);
      setLessonId(null);
      return;
    }
    refreshCourse(spaceId).catch((e) => fail("Load course", e));
  }, [spaceId, isCourseSpace, refreshCourse, fail]);

  useEffect(() => {
    if (!threadId) {
      setPosts([]);
      return;
    }
    refreshPosts(threadId).catch((e) => fail("Load posts", e));
  }, [threadId, refreshPosts, fail]);

  useAppEvents<{ community_id?: string; space_id?: string; thread_id?: string }>(
    "community",
    projectId,
    (ev) => {
      const t = ev.topic;
      const d = ev.data ?? {};
      if (
        t === "community.community.created" ||
        t === "community.community.updated" ||
        t === "community.community.archived"
      ) {
        refreshCommunities().catch(() => {});
        return;
      }
      if (!communityId || (d.community_id && d.community_id !== communityId)) return;
      if (t.startsWith("community.member.")) {
        refreshMembers(communityId).catch(() => {});
      }
      if (t.startsWith("community.membership.")) {
        refreshMemberships(communityId).catch(() => {});
      }
      if (
        t === "community.space.created" ||
        t === "community.space.updated" ||
        t === "community.space.archived" ||
        t === "community.space.member_added"
      ) {
        refreshSpaces(communityId).catch(() => {});
        return;
      }
      if (spaceId && t === "community.thread.created") {
        if (!d.space_id || d.space_id === spaceId) refreshThreads(spaceId).catch(() => {});
        return;
      }
      if (threadId && d.thread_id === threadId && t.startsWith("community.post.")) {
        refreshPosts(threadId).catch(() => {});
        return;
      }
      if (t === "community.post.created" && spaceId) refreshThreads(spaceId).catch(() => {});
      if (
        spaceId &&
        isCourseSpace &&
        (t.startsWith("community.section.") ||
          t.startsWith("community.sections.") ||
          t.startsWith("community.lesson.") ||
          t.startsWith("community.course.offer") ||
          t.startsWith("community.course.purchase") ||
          t.startsWith("community.course.refund") ||
          t.startsWith("community.course.access"))
      ) {
        refreshCourse(spaceId).catch(() => {});
      }
    },
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
    for (const l of lessons) (m[l.section_id] ??= []).push(l);
    for (const arr of Object.values(m)) arr.sort((a, b) => a.position - b.position);
    return m;
  }, [lessons]);

  const runTool = useCallback(
    async <T,>(label: string, tool: string, args: Record<string, unknown>): Promise<T> => {
      setStatus(`${label}...`);
      try {
        const out = await callTool<T>(tool, args, projectId);
        setStatus(`${label} saved`);
        return out;
      } catch (e) {
        fail(label, e);
        throw e;
      }
    },
    [projectId, fail],
  );

  const sendPost = async () => {
    if (!threadId || !memberId || !postBody.trim()) return;
    await runTool("Post", "posts_create", {
      thread_id: threadId,
      author_id: memberId,
      body: postBody.trim(),
    });
    setPostBody("");
    await refreshPosts(threadId);
    if (spaceId) await refreshThreads(spaceId);
  };

  return (
    <div className="relative flex h-full w-full flex-col overflow-auto bg-bg text-text lg:flex-row lg:overflow-hidden">
      <aside className="flex w-full shrink-0 flex-col border-b border-border min-h-0 max-h-80 lg:h-full lg:w-72 lg:max-h-none lg:border-b-0 lg:border-r">
        <div className="p-3 border-b border-border space-y-3">
          <div className="flex items-center justify-between gap-2">
            <div className="text-text-muted text-xs uppercase tracking-wide">Community</div>
            <button className={buttonClass("ghost")} onClick={() => setDialog("community")}>
              + Community
            </button>
          </div>
          <select
            className="w-full bg-bg-secondary border border-border rounded px-2 py-1.5 text-sm"
            value={communityId ?? ""}
            onChange={(e) => setCommunityId(e.target.value || null)}
          >
            {communities.length === 0 ? <option value="">No communities</option> : null}
            {communities.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
          {activeCommunity?.description ? (
            <div className="text-xs text-text-muted line-clamp-3">{activeCommunity.description}</div>
          ) : null}
        </div>

        <div className="p-3 border-b border-border space-y-2">
          <div className="flex items-center justify-between gap-2">
            <div className="text-text-muted text-xs uppercase tracking-wide">Members</div>
            <button
              className={buttonClass("ghost")}
              disabled={!communityId}
              onClick={() => setDialog("member")}
            >
              + Member
            </button>
          </div>
          <select
            className="w-full bg-bg-secondary border border-border rounded px-2 py-1.5 text-sm"
            value={memberId}
            onChange={(e) => setMemberId(e.target.value)}
            disabled={members.length === 0}
          >
            {members.length === 0 ? <option value="">No members</option> : null}
            {members.map((m) => (
              <option key={m.id} value={m.id}>
                {m.display_name || m.handle}
              </option>
            ))}
          </select>
        </div>

        <div className="p-3 border-b border-border space-y-2">
          <div className="flex items-center justify-between gap-2">
            <div>
              <div className="text-text-muted text-xs uppercase tracking-wide">Memberships</div>
              <div className="text-xs text-text-muted">{memberSubscriptions.filter((s) => s.status === "active" || s.status === "trialing").length} active</div>
            </div>
            <button className={buttonClass("ghost")} disabled={!communityId} onClick={() => setDialog("membership_plan")}>
              + Plan
            </button>
          </div>
          {membershipPlans.length === 0 ? (
            <div className="text-xs text-text-muted">No recurring plans.</div>
          ) : (
            <div className="space-y-1">
              {membershipPlans.slice(0, 3).map((plan) => (
                <div key={plan.id} className="rounded border border-border px-2 py-1.5 text-xs">
                  <div className="font-medium">{plan.name}</div>
                  <div className="text-text-muted">{formatMoney(plan.unit_amount_cents, plan.currency)} / {plan.interval}</div>
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="p-3 flex-1 overflow-auto min-h-0">
          <div className="flex items-center justify-between gap-2 mb-2">
            <div className="text-text-muted text-xs uppercase tracking-wide">Spaces</div>
            <div className="flex gap-1">
              <button
                className={buttonClass("ghost")}
                disabled={!communityId}
                onClick={() => setDialog("space")}
              >
                + Space
              </button>
              <button
                className={buttonClass("ghost")}
                disabled={!communityId}
                onClick={() => setDialog("course")}
              >
                + Course
              </button>
            </div>
          </div>
          {spaces.length === 0 ? (
            <EmptyState label={communityId ? "No spaces yet." : "Create or pick a community."} />
          ) : (
            <ul className="space-y-1">
              {spaces.map((s) => (
                <li key={s.id}>
                  <button
                    onClick={() => setSpaceId(s.id)}
                    className={
                      "w-full text-left px-2.5 py-2 rounded border " +
                      (s.id === spaceId
                        ? "bg-bg-secondary border-border text-text"
                        : "border-transparent text-text-muted hover:bg-bg-secondary")
                    }
                  >
                    <div className="flex items-center gap-2">
                      <span className="text-xs text-text-muted">{s.kind === "course" ? "course" : "#"}</span>
                      <span className="truncate text-sm">{s.name}</span>
                    </div>
                    <div className="text-xs text-text-muted">{s.visibility}</div>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </aside>

      <section className="flex w-full shrink-0 flex-col border-b border-border min-h-0 max-h-96 lg:h-full lg:w-96 lg:max-h-none lg:border-b-0 lg:border-r">
        <header className="p-3 border-b border-border flex items-center justify-between gap-2">
          <div className="min-w-0">
            <div className="font-medium truncate">{activeSpace?.name ?? "No space selected"}</div>
            <div className="text-xs text-text-muted">
              {activeSpace ? `${activeSpace.kind} · ${activeSpace.visibility}` : "Spaces and courses live here."}
            </div>
          </div>
          {isCourseSpace ? (
            <div className="flex gap-2">
              <button className={buttonClass()} onClick={() => setDialog("offer")}>
                {courseOffer?.active ? "Edit sale" : "Configure sale"}
              </button>
              <button className={buttonClass("primary")} onClick={() => setDialog("section")}>
                + Section
              </button>
            </div>
          ) : (
            <button
              className={buttonClass("primary")}
              disabled={!spaceId || !memberId}
              onClick={() => setDialog("thread")}
            >
              + Thread
            </button>
          )}
        </header>
        <div className="flex-1 overflow-auto min-h-0">
          {isCourseSpace ? (
            sections.length === 0 ? (
              <EmptyState label="No sections yet. Add a section to start the course outline." />
            ) : (
              <ul>
                {sections.map((s) => (
                  <li key={s.id} className="border-b border-border">
                    <div className="px-3 py-2 flex items-center justify-between gap-2">
                      <div className="text-xs uppercase tracking-wide text-text-muted truncate">{s.title}</div>
                      <button
                        className={buttonClass("ghost")}
                        onClick={() => {
                          setNewLessonSectionId(s.id);
                          setDialog("lesson");
                        }}
                      >
                        + Lesson
                      </button>
                    </div>
                    <ul className="pb-1">
                      {(lessonsBySection[s.id] ?? []).length === 0 ? (
                        <li className="px-3 py-2 text-xs text-text-muted">No lessons in this section.</li>
                      ) : null}
                      {(lessonsBySection[s.id] ?? []).map((l) => (
                        <li key={l.id}>
                          <button
                            onClick={() => setLessonId(l.id)}
                            className={
                              "w-full text-left px-3 py-2 flex items-center gap-2 " +
                              (l.id === lessonId ? "bg-bg-secondary" : "hover:bg-bg-secondary")
                            }
                          >
                            <span
                              className={
                                "inline-block w-2 h-2 rounded-full shrink-0 " +
                                (l.published_at ? "bg-success" : "bg-text-dim")
                              }
                            />
                            <span className="text-sm truncate flex-1">{l.title}</span>
                            {!l.published_at ? <span className="text-xs text-text-muted">draft</span> : null}
                            {l.video_duration_seconds ? (
                              <span className="text-xs text-text-muted">{formatDuration(l.video_duration_seconds)}</span>
                            ) : null}
                          </button>
                        </li>
                      ))}
                    </ul>
                  </li>
                ))}
              </ul>
            )
          ) : threads.length === 0 ? (
            <EmptyState label={spaceId ? "No threads yet." : "Pick a space."} />
          ) : (
            <ul>
              {threads.map((t) => (
                <li key={t.id}>
                  <button
                    onClick={() => setThreadId(t.id)}
                    className={
                      "w-full text-left px-3 py-2 border-b border-border " +
                      (t.id === threadId ? "bg-bg-secondary" : "hover:bg-bg-secondary")
                    }
                  >
                    <div className="text-sm font-medium truncate">
                      {t.pinned ? "Pinned · " : ""}
                      {t.title || "(untitled thread)"}
                    </div>
                    <div className="text-xs text-text-muted">
                      {t.post_count} posts · {new Date(t.last_post_at).toLocaleString()}
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </section>

      <main className="flex min-h-[28rem] min-w-0 flex-1 flex-col lg:min-h-0">
        <header className="p-3 border-b border-border flex items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="font-medium truncate">
              {isCourseSpace
                ? activeLesson?.title || "Select a lesson"
                : activeThread?.title || "Select a thread"}
            </div>
            <div className="text-xs text-text-muted truncate">
              {activeCommunity?.name ?? "No community"} {activeSpace ? `/ ${activeSpace.name}` : ""}
            </div>
          </div>
          {isCourseSpace && activeLesson ? (
            <div className="flex gap-2">
              <button className={buttonClass()} onClick={() => setDialog("video")}>
                Attach video
              </button>
              <button className={buttonClass("primary")} onClick={() => setDialog("edit_lesson")}>
                Edit lesson
              </button>
            </div>
          ) : null}
        </header>
        <div className="flex-1 overflow-auto p-4 space-y-3 min-h-0">
          {isCourseSpace ? (
            <section className="border border-border rounded bg-bg-secondary p-3">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <div className="text-xs uppercase tracking-wide text-text-muted">Course sales</div>
                  {courseOffer?.active ? (
                    <>
                      <div className="font-medium mt-1">{formatMoney(courseOffer.unit_amount_cents, courseOffer.currency)}</div>
                      <div className="text-xs text-text-muted">
                        Catalog price #{courseOffer.catalog_price_id} · {coursePurchases.length} purchases
                      </div>
                    </>
                  ) : (
                    <div className="text-sm text-text-muted mt-1">No active offer. This course cannot be purchased.</div>
                  )}
                </div>
                <div className="flex gap-2">
                  {courseOffer?.active ? (
                    <button
                      className={buttonClass("ghost")}
                      onClick={() => {
                        runTool("Archive sale", "course_offer_archive", { space_id: spaceId })
                          .then(async () => {
                            if (spaceId) await refreshCourse(spaceId);
                          })
                          .catch(() => {});
                      }}
                    >
                      Stop sales
                    </button>
                  ) : null}
                  <button className={buttonClass()} onClick={() => setDialog("offer")}>
                    {courseOffer?.active ? "Change price" : "Set Catalog price"}
                  </button>
                </div>
              </div>
              {coursePurchases.length > 0 ? (
                <div className="mt-3 grid gap-1 border-t border-border pt-3">
                  {coursePurchases.slice(0, 4).map((purchase) => (
                    <div key={purchase.id} className="flex items-center justify-between gap-3 text-xs">
                      <span className="truncate">{purchase.customer_email}</span>
                      <span className="text-text-muted">{purchase.status.replaceAll("_", " ")}</span>
                    </div>
                  ))}
                </div>
              ) : null}
            </section>
          ) : null}
          {isCourseSpace ? (
            !activeLesson ? (
              <EmptyState label={lessons.length > 0 ? "Pick a lesson." : "No lessons in this course yet."} />
            ) : (
              <>
                <div className="flex flex-wrap items-center gap-2 text-xs">
                  <span className="px-2 py-1 rounded border border-border text-text-muted">
                    {activeLesson.published_at ? "Published" : "Draft"}
                  </span>
                  {activeLesson.video_storage_key ? (
                    <span className="px-2 py-1 rounded border border-border text-text-muted">
                      Video {activeLesson.video_duration_seconds ? formatDuration(activeLesson.video_duration_seconds) : "attached"}
                    </span>
                  ) : null}
                </div>
                {activeLesson.video_storage_key ? (
                  <div className="border border-border rounded bg-bg-secondary p-3">
                    <div className="text-xs uppercase tracking-wide text-text-muted mb-2">Video</div>
                    <div className="text-sm text-text-muted break-all">
                      {activeLesson.video_storage_key}
                      {activeLesson.video_duration_seconds ? ` · ${formatDuration(activeLesson.video_duration_seconds)}` : ""}
                    </div>
                  </div>
                ) : null}
                <article className="border border-border rounded p-4 bg-bg-secondary">
                  <div className="whitespace-pre-wrap text-sm leading-relaxed">
                    {activeLesson.body || "(empty lesson body)"}
                  </div>
                </article>
              </>
            )
          ) : threadId === null ? (
            <EmptyState label={threads.length > 0 ? "Pick a thread to read it." : "No threads in this space yet."} />
          ) : posts.length === 0 ? (
            <EmptyState label="No posts yet." />
          ) : (
            posts.map((p) => (
              <article key={p.id} className="border border-border rounded p-3 bg-bg-secondary">
                <header className="text-xs text-text-muted mb-1">
                  {memberLabel(members, p.author_id)} · {new Date(p.created_at).toLocaleString()}
                  {p.edited_at ? " · edited" : ""}
                  {p.removed_at ? " · removed" : ""}
                </header>
                <div className="whitespace-pre-wrap text-sm">{p.body}</div>
                {p.reactions && p.reactions.length > 0 ? (
                  <div className="flex gap-1 mt-2">
                    {p.reactions.map((r) => (
                      <span key={r.emoji} className="text-xs px-1.5 py-0.5 rounded border border-border text-text-muted">
                        {r.emoji} {r.count}
                      </span>
                    ))}
                  </div>
                ) : null}
              </article>
            ))
          )}
        </div>
        {!isCourseSpace && activeThread ? (
          <div className="border-t border-border p-3 flex items-end gap-2">
            <textarea
              value={postBody}
              onChange={(e) => setPostBody(e.target.value)}
              placeholder={memberId ? "Write a reply" : "Create a member before posting"}
              className="flex-1 min-h-[76px] bg-bg-secondary border border-border rounded px-3 py-2 text-sm resize-y"
              disabled={!memberId}
            />
            <button
              className={buttonClass("primary")}
              disabled={!memberId || !postBody.trim()}
              onClick={() => sendPost().catch(() => {})}
            >
              Send
            </button>
          </div>
        ) : null}
      </main>

      <div className="absolute bottom-2 right-2 text-xs px-2 py-1 rounded border border-border bg-bg text-text-muted max-w-[520px] truncate">
        {status}
      </div>

      {dialog ? (
        <CommunityDialog
          kind={dialog}
          community={activeCommunity}
          space={activeSpace}
          sections={sections}
          spaces={spaces}
          lesson={activeLesson}
          offer={courseOffer}
          initialSectionId={newLessonSectionId}
          memberId={memberId}
          projectId={projectId}
          onClose={() => setDialog(null)}
          onCreated={async (target) => {
            setDialog(null);
            if (target.communityId) setCommunityId(target.communityId);
            if (target.memberId) setMemberId(target.memberId);
            if (target.spaceId) setSpaceId(target.spaceId);
            if (target.threadId) setThreadId(target.threadId);
            if (target.lessonId) setLessonId(target.lessonId);
            if (communityId) {
              await Promise.all([refreshMembers(communityId), refreshSpaces(communityId), refreshMemberships(communityId)]);
            } else {
              await refreshCommunities();
            }
            const createdCourseContent =
              dialog === "course" ||
              dialog === "section" ||
              dialog === "lesson" ||
              dialog === "edit_lesson" ||
              dialog === "video" ||
              dialog === "offer";
            if (createdCourseContent && (target.spaceId || spaceId)) {
              await refreshCourse(target.spaceId || spaceId!);
            } else if (target.spaceId || spaceId) {
              await refreshThreads(target.spaceId || spaceId!);
            }
            if (target.communityId) setCommunityId(target.communityId);
            if (target.memberId) setMemberId(target.memberId);
            if (target.spaceId) setSpaceId(target.spaceId);
            if (target.threadId) setThreadId(target.threadId);
            if (target.lessonId) setLessonId(target.lessonId);
          }}
          runTool={runTool}
        />
      ) : null}
    </div>
  );
}

function memberLabel(members: Member[], id: string): string {
  const m = members.find((x) => x.id === id);
  return m ? m.display_name || m.handle : id;
}

function EmptyState({ label }: { label: string }) {
  return <div className="p-4 text-text-muted text-sm">{label}</div>;
}

function DialogShell({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog.focus();
    return () => previous?.focus();
  }, []);

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      onClose();
      return;
    }
    if (event.key !== "Tab") return;
    const focusable = Array.from(
      event.currentTarget.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [href], [tabindex]:not([tabindex="-1"])',
      ),
    );
    if (focusable.length === 0) {
      event.preventDefault();
      return;
    }
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  };

  return (
    <div
      className="absolute inset-0 bg-black/40 flex items-center justify-center p-4 z-20"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="community-dialog-title"
        tabIndex={-1}
        onKeyDown={handleKeyDown}
        className="w-full max-w-xl bg-bg border border-border rounded shadow-xl outline-none"
      >
        <header className="px-4 py-3 border-b border-border flex items-center justify-between">
          <div id="community-dialog-title" className="font-medium">{title}</div>
          <button className={buttonClass("ghost")} onClick={onClose}>
            Close
          </button>
        </header>
        <div className="p-4">{children}</div>
      </div>
    </div>
  );
}

function Field({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-xs uppercase tracking-wide text-text-muted">{label}</span>
      {children}
    </label>
  );
}

const inputClass = "w-full bg-bg-secondary border border-border rounded px-3 py-2 text-sm";

function CommunityDialog({
  kind,
  community,
  space,
  sections,
  spaces,
  lesson,
  offer,
  initialSectionId,
  memberId,
  projectId,
  runTool,
  onClose,
  onCreated,
}: {
  kind: DialogKind;
  community: Community | null;
  space: Space | null;
  sections: Section[];
  spaces: Space[];
  lesson: Lesson | null;
  offer: CourseOffer | null;
  initialSectionId: string;
  memberId: string;
  projectId: string;
  runTool: <T,>(label: string, tool: string, args: Record<string, unknown>) => Promise<T>;
  onClose: () => void;
  onCreated: (target: { communityId?: string; memberId?: string; spaceId?: string; threadId?: string; lessonId?: string }) => Promise<void>;
}) {
  const [name, setName] = useState(kind === "edit_lesson" ? lesson?.title || "" : "");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [authUserId, setAuthUserId] = useState("");
  const [visibility, setVisibility] = useState<"members" | "public">("members");
  const [spaceKind, setSpaceKind] = useState<Space["kind"]>("feed");
  const [sectionId, setSectionId] = useState(initialSectionId || sections[0]?.id || "");
  const [body, setBody] = useState(kind === "edit_lesson" ? lesson?.body || "" : "");
  const [published, setPublished] = useState(Boolean(lesson?.published_at));
  const [storageKey, setStorageKey] = useState("");
  const [duration, setDuration] = useState("");
  const [catalogPriceId, setCatalogPriceId] = useState(offer?.catalog_price_id ? String(offer.catalog_price_id) : "");
  const [planScope, setPlanScope] = useState<MembershipPlan["scope_type"]>("all_courses");
  const [collectionMethod, setCollectionMethod] = useState("automatic");
  const [trialDays, setTrialDays] = useState("0");
  const [graceDays, setGraceDays] = useState("7");
  const [selectedCourseIds, setSelectedCourseIds] = useState<string[]>([]);
  const [planTags, setPlanTags] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const title =
    kind === "community"
      ? "New community"
      : kind === "member"
      ? "New member"
      : kind === "course"
      ? "New course"
      : kind === "space"
      ? "New space"
      : kind === "section"
      ? "New section"
      : kind === "lesson"
      ? "New lesson"
      : kind === "edit_lesson"
      ? "Edit lesson"
      : kind === "video"
      ? "Attach lesson video"
      : kind === "offer"
      ? "Configure course sale"
      : kind === "membership_plan"
      ? "New membership plan"
      : "New thread";

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      if (kind === "community") {
        const out = await runTool<Community>("Create community", "communities_create", {
          slug: slug || slugify(name),
          name,
          description,
          _project_id: projectId,
        });
        await onCreated({ communityId: out.id });
      } else if (kind === "member") {
        const out = await runTool<Member>("Create member", "members_create", {
          community_id: community?.id,
          handle: slug || slugify(name).replace(/-/g, "_"),
          display_name: name,
          bio: description,
          auth_user_id: authUserId.trim() || undefined,
        });
        await onCreated({ memberId: out.id });
      } else if (kind === "space" || kind === "course") {
        const tool = kind === "course" ? "courses_create" : "spaces_create";
        const out = await runTool<Space>(kind === "course" ? "Create course" : "Create space", tool, {
          community_id: community?.id,
          slug: slug || slugify(name),
          name,
          kind: kind === "course" ? undefined : spaceKind,
          visibility,
        });
        await onCreated({ spaceId: out.id });
      } else if (kind === "section") {
        await runTool<Section>("Create section", "sections_create", {
          space_id: space?.id,
          title: name,
        });
        await onCreated({ spaceId: space?.id });
      } else if (kind === "lesson") {
        const out = await runTool<Lesson>("Create lesson", "lessons_create", {
          section_id: sectionId,
          title: name,
          body,
        });
        if (published) {
          await runTool<Lesson>("Publish lesson", "lessons_publish", {
            id: out.id,
            published: true,
          });
        }
        await onCreated({ spaceId: space?.id, lessonId: out.id });
      } else if (kind === "edit_lesson" && lesson) {
        await runTool<Lesson>("Update lesson", "lessons_update", {
          id: lesson.id,
          title: name,
          body,
        });
        if (published !== Boolean(lesson.published_at)) {
          await runTool<Lesson>("Update publish state", "lessons_publish", {
            id: lesson.id,
            published,
          });
        }
        await onCreated({ spaceId: space?.id, lessonId: lesson.id });
      } else if (kind === "video" && lesson) {
        const args: Record<string, unknown> = { id: lesson.id, storage_key: storageKey };
        if (duration.trim()) args.duration_seconds = Number(duration);
        await runTool<Lesson>("Attach video", "lessons_attach_video", args);
        await onCreated({ spaceId: space?.id, lessonId: lesson.id });
      } else if (kind === "offer") {
        await runTool<{ offer: CourseOffer }>("Save course sale", "course_offer_upsert", {
          space_id: space?.id,
          catalog_price_id: Number(catalogPriceId),
        });
        await onCreated({ spaceId: space?.id });
      } else if (kind === "membership_plan") {
        const out = await runTool<{ plan: MembershipPlan }>("Create membership plan", "membership_plans_upsert", {
          community_id: community?.id,
          name,
          description,
          catalog_price_id: Number(catalogPriceId),
          scope_type: planScope,
          collection_method: collectionMethod,
          trial_days: Number(trialDays || 0),
          grace_days: Number(graceDays || 0),
        });
        if (planScope === "selected_courses") {
          await runTool("Set included courses", "membership_plan_courses_set", {
            id: out.plan.id,
            space_ids: selectedCourseIds,
          });
        }
        if (planScope === "course_tags") {
          await runTool("Set included tags", "membership_plan_tags_set", {
            id: out.plan.id,
            tags: planTags.split(",").map((tag) => tag.trim()).filter(Boolean),
          });
        }
        await onCreated({});
      } else if (kind === "thread") {
        const out = await runTool<{ thread: Thread }>("Create thread", "threads_create", {
          space_id: space?.id,
          author_id: memberId,
          title: name,
          body,
        });
        await onCreated({ spaceId: space?.id, threadId: out.thread?.id });
      }
    } catch (err) {
      setError((err as Error).message || String(err));
    } finally {
      setBusy(false);
    }
  };

  const needsSlug = kind === "community" || kind === "member" || kind === "space" || kind === "course";
  const canSubmit =
    kind === "offer" || kind === "membership_plan"
      ? Number.isInteger(Number(catalogPriceId)) && Number(catalogPriceId) > 0
      : kind === "video"
      ? Boolean(storageKey.trim())
      : kind === "lesson"
      ? Boolean(name.trim() && sectionId)
      : kind === "edit_lesson"
      ? Boolean(name.trim())
      : kind === "thread"
      ? Boolean(memberId && (name.trim() || body.trim()))
      : Boolean(name.trim());

  return (
    <DialogShell title={title} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        {kind === "space" ? (
          <Field label="Type">
            <select className={inputClass} value={spaceKind} onChange={(e) => setSpaceKind(e.target.value as Space["kind"])}>
              <option value="feed">Feed</option>
              <option value="forum">Forum</option>
              <option value="chat">Chat</option>
            </select>
          </Field>
        ) : null}

        {kind !== "video" && kind !== "offer" ? (
          <Field label={kind === "member" ? "Display name" : kind === "section" ? "Section title" : kind === "thread" ? "Thread title" : "Name"}>
            <input
              className={inputClass}
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                if (needsSlug && !slug) setSlug(slugify(e.target.value));
              }}
              autoFocus
            />
          </Field>
        ) : null}

        {needsSlug ? (
          <Field label={kind === "member" ? "Handle" : "Slug"}>
            <input className={inputClass} value={slug} onChange={(e) => setSlug(e.target.value)} />
          </Field>
        ) : null}

        {kind === "space" || kind === "course" ? (
          <Field label="Visibility">
            <select className={inputClass} value={visibility} onChange={(e) => setVisibility(e.target.value as "members" | "public")}>
              <option value="members">Members</option>
              <option value="public">Public</option>
            </select>
          </Field>
        ) : null}

        {kind === "lesson" ? (
          <Field label="Section">
            <select className={inputClass} value={sectionId} onChange={(e) => setSectionId(e.target.value)}>
              {sections.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.title}
                </option>
              ))}
            </select>
          </Field>
        ) : null}

        {kind === "community" || kind === "member" || kind === "membership_plan" ? (
          <Field label={kind === "community" ? "Description" : "Bio"}>
            <textarea className={`${inputClass} min-h-[92px]`} value={description} onChange={(e) => setDescription(e.target.value)} />
          </Field>
        ) : null}

        {kind === "member" ? (
          <Field label="Auth user ID">
            <input
              className={inputClass}
              value={authUserId}
              onChange={(e) => setAuthUserId(e.target.value)}
              placeholder="Link the ID shown by the member portal"
            />
          </Field>
        ) : null}

        {kind === "lesson" || kind === "edit_lesson" || kind === "thread" ? (
          <Field label={kind === "thread" ? "First post" : "Lesson body"}>
            <textarea className={`${inputClass} min-h-[180px]`} value={body} onChange={(e) => setBody(e.target.value)} />
          </Field>
        ) : null}

        {kind === "lesson" || kind === "edit_lesson" ? (
          <label className="flex items-center gap-2 text-sm text-text-muted">
            <input type="checkbox" checked={published} onChange={(e) => setPublished(e.target.checked)} />
            Published
          </label>
        ) : null}

        {kind === "video" ? (
          <>
            <Field label="Storage key">
              <input className={inputClass} value={storageKey} onChange={(e) => setStorageKey(e.target.value)} autoFocus />
            </Field>
            <Field label="Duration seconds">
              <input className={inputClass} type="number" min="0" value={duration} onChange={(e) => setDuration(e.target.value)} />
            </Field>
          </>
        ) : null}

        {kind === "offer" ? (
          <>
            <Field label="Catalog price ID">
              <input
                className={inputClass}
                type="number"
                min="1"
                step="1"
                value={catalogPriceId}
                onChange={(e) => setCatalogPriceId(e.target.value)}
                autoFocus
              />
            </Field>
            <div className="text-sm text-text-muted">
              Community validates that this is an active, flat, one-time Catalog price. Billing uses it for every new checkout.
            </div>
          </>
        ) : null}

        {kind === "membership_plan" ? (
          <>
            <Field label="Recurring Catalog price ID">
              <input className={inputClass} type="number" min="1" step="1" value={catalogPriceId} onChange={(e) => setCatalogPriceId(e.target.value)} />
            </Field>
            <Field label="Course access">
              <select className={inputClass} value={planScope} onChange={(e) => setPlanScope(e.target.value as MembershipPlan["scope_type"])}>
                <option value="all_courses">All courses</option>
                <option value="selected_courses">Selected courses</option>
                <option value="course_tags">Courses with tags</option>
              </select>
            </Field>
            {planScope === "selected_courses" ? (
              <Field label="Included courses">
                <div className="space-y-2 rounded border border-border p-3">
                  {spaces.filter((item) => item.kind === "course").map((course) => (
                    <label key={course.id} className="flex items-center gap-2 text-sm">
                      <input type="checkbox" checked={selectedCourseIds.includes(course.id)} onChange={(e) => setSelectedCourseIds((current) => e.target.checked ? [...current, course.id] : current.filter((id) => id !== course.id))} />
                      {course.name}
                    </label>
                  ))}
                </div>
              </Field>
            ) : null}
            {planScope === "course_tags" ? (
              <Field label="Course tags (comma-separated)">
                <input className={inputClass} value={planTags} onChange={(e) => setPlanTags(e.target.value)} placeholder="beginner, premium" />
              </Field>
            ) : null}
            <Field label="Renewal collection">
              <select className={inputClass} value={collectionMethod} onChange={(e) => setCollectionMethod(e.target.value)}>
                <option value="automatic">Automatic saved-card collection</option>
                <option value="send_invoice">Send hosted payment link</option>
              </select>
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Trial days"><input className={inputClass} type="number" min="0" value={trialDays} onChange={(e) => setTrialDays(e.target.value)} /></Field>
              <Field label="Grace days"><input className={inputClass} type="number" min="0" value={graceDays} onChange={(e) => setGraceDays(e.target.value)} /></Field>
            </div>
            <div className="text-sm text-text-muted">
              Community validates the recurring Catalog price and orchestrates Billing plus Subscriptions directly.
            </div>
          </>
        ) : null}

        {kind === "thread" && !memberId ? (
          <div className="text-sm text-warn">Create or select a member before opening a thread.</div>
        ) : null}
        {kind === "lesson" && sections.length === 0 ? (
          <div className="text-sm text-warn">Create a section before adding lessons.</div>
        ) : null}
        {error ? <div className="text-sm text-warn">{error}</div> : null}

        <div className="flex justify-end gap-2">
          <button type="button" className={buttonClass()} onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className={buttonClass("primary")} disabled={busy || !canSubmit}>
            {busy ? "Saving..." : "Save"}
          </button>
        </div>
      </form>
    </DialogShell>
  );
}
