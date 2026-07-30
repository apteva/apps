import {
  BookOpen,
  CheckCircle2,
  Home,
  LogOut,
  Menu,
  MessageCircle,
  MessagesSquare,
  RefreshCcw,
  Send,
  UserRound,
  Users,
  X,
} from "lucide-react";
import { FormEvent, ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, apteva, COMMUNITY_APP, currentProjectId, type AuthResponse, type LessonBundle, useDelegatedToken } from "./api";
import type {
  Community,
  CourseOffer,
  CoursePurchase,
  DMThread,
  DMThreadView,
  EnrollmentRule,
  Lesson,
  Member,
  Post,
  Section,
  Space,
  Thread,
} from "./types";

type View = "home" | "spaces" | "courses" | "members" | "messages" | "profile";
type AuthMode = "login" | "signup";
type AppEvent = {
  topic: string;
  data?: {
    community_id?: string;
    space_id?: string;
    thread_id?: string;
    lesson_id?: string;
    dm_thread_id?: string;
  };
};

const nav: Array<{ id: View; label: string; icon: typeof Home }> = [
  { id: "home", label: "Home", icon: Home },
  { id: "spaces", label: "Spaces", icon: MessagesSquare },
  { id: "courses", label: "Courses", icon: BookOpen },
  { id: "members", label: "Members", icon: Users },
  { id: "messages", label: "Messages", icon: MessageCircle },
  { id: "profile", label: "Profile", icon: UserRound },
];

const authSessionKey = "apteva-community-auth";

function initialAuthSession(): AuthResponse | null {
  if (typeof window === "undefined") return null;
  try {
    const stored = JSON.parse(window.sessionStorage.getItem(authSessionKey) || "null") as AuthResponse | null;
    if (!stored?.apteva_access_token || !stored.user) return null;
    useDelegatedToken(stored.apteva_access_token);
    return stored;
  } catch {
    window.sessionStorage.removeItem(authSessionKey);
    return null;
  }
}

function initialView(): View {
  if (typeof window === "undefined") return "home";
  return new URLSearchParams(window.location.search).has("payment") ? "courses" : "home";
}

function initialAuthForm() {
  const params = typeof window === "undefined" ? new URLSearchParams() : new URLSearchParams(window.location.search);
  return {
    client_id: params.get("client_id") || "",
    organization_slug: params.get("organization_slug") || params.get("organization") || "",
    email: "",
    password: "",
    display_name: "",
  };
}

function friendlyError(error: unknown): string {
  const message = error instanceof Error ? error.message : String(error);
  if (message.includes("not linked to an active member")) {
    return "Your account is not linked to a community membership yet. Ask a community operator to link your Auth user ID.";
  }
  if (message.includes("active course enrollment required")) return "Enroll in this course to open its lessons.";
  if (message.includes("lesson is not available yet")) return "This lesson is scheduled for a later date.";
  const json = message.match(/\{"error":"([^"]+)"\}/);
  if (json?.[1]) return json[1];
  return message.replace(/^HTTP \d+:\s*/, "");
}

function displayName(member?: Member): string {
  return member?.display_name || member?.handle || "Member";
}

function formatCoursePrice(offer: CourseOffer): string {
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: offer.currency,
    }).format(offer.unit_amount_cents / 100);
  } catch {
    return `${offer.currency} ${(offer.unit_amount_cents / 100).toFixed(2)}`;
  }
}

export default function App() {
  const [auth, setAuth] = useState<AuthResponse | null>(initialAuthSession);
  const [authMode, setAuthMode] = useState<AuthMode>("login");
  const [authForm, setAuthForm] = useState(initialAuthForm);
  const [view, setView] = useState<View>(initialView);
  const [menuOpen, setMenuOpen] = useState(false);
  const [pending, setPending] = useState(0);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const [communities, setCommunities] = useState<Community[]>([]);
  const [memberships, setMemberships] = useState<Member[]>([]);
  const [communityId, setCommunityId] = useState("");
  const [members, setMembers] = useState<Member[]>([]);
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [dmThreads, setDMThreads] = useState<DMThread[]>([]);

  const [spaceId, setSpaceId] = useState("");
  const [threads, setThreads] = useState<Thread[]>([]);
  const [threadId, setThreadId] = useState("");
  const [posts, setPosts] = useState<Post[]>([]);
  const [threadForm, setThreadForm] = useState({ title: "", body: "" });
  const [reply, setReply] = useState("");

  const [courseId, setCourseId] = useState("");
  const [sections, setSections] = useState<Section[]>([]);
  const [lessons, setLessons] = useState<Lesson[]>([]);
  const [lessonId, setLessonId] = useState("");
  const [bundle, setBundle] = useState<LessonBundle | null>(null);
  const [courseSummary, setCourseSummary] = useState("");
  const [courseLocked, setCourseLocked] = useState(false);
  const [courseAccessMode, setCourseAccessMode] = useState<EnrollmentRule["access_mode"]>("free");
  const [courseOffer, setCourseOffer] = useState<CourseOffer | null>(null);
  const [coursePurchase, setCoursePurchase] = useState<CoursePurchase | null>(null);
  const [comment, setComment] = useState("");

  const [dmId, setDMId] = useState("");
  const [dm, setDM] = useState<DMThreadView | null>(null);
  const [dmRecipient, setDMRecipient] = useState("");
  const [dmBody, setDMBody] = useState("");

  const requestVersion = useRef<Record<string, number>>({});
  const eventTimer = useRef<number | undefined>(undefined);
  const busy = pending > 0;

  const selectedCommunity = communities.find((item) => item.id === communityId);
  const me = memberships.find((item) => item.community_id === communityId);
  const discussionSpaces = spaces.filter((item) => item.kind !== "course");
  const courses = spaces.filter((item) => item.kind === "course");
  const selectedSpace = spaces.find((item) => item.id === spaceId);
  const selectedThread = threads.find((item) => item.id === threadId);
  const selectedCourse = courses.find((item) => item.id === courseId);
  const selectedLesson = lessons.find((item) => item.id === lessonId);
  const memberMap = useMemo(() => new Map(members.map((member) => [member.id, member])), [members]);

  const run = useCallback(async <T,>(
    task: () => Promise<T>,
    options: { quiet?: boolean; preserveNotice?: boolean } = {},
  ): Promise<T | undefined> => {
    setPending((value) => value + 1);
    if (!options.quiet) {
      setError("");
      if (!options.preserveNotice) setNotice("");
    }
    try {
      return await task();
    } catch (caught) {
      if (!options.quiet) setError(friendlyError(caught));
      return undefined;
    } finally {
      setPending((value) => Math.max(0, value - 1));
    }
  }, []);

  const latest = useCallback(async <T,>(key: string, task: () => Promise<T>, apply: (value: T) => void) => {
    const version = (requestVersion.current[key] || 0) + 1;
    requestVersion.current[key] = version;
    const value = await task();
    if (requestVersion.current[key] === version) apply(value);
  }, []);

  const loadSession = useCallback(async () => {
    await latest("session", api.session, (session) => {
      setCommunities(session.communities || []);
      setMemberships(session.memberships || []);
      setCommunityId((current) =>
        session.communities.some((item) => item.id === current) ? current : session.communities[0]?.id || "",
      );
    });
  }, [latest]);

  const loadCommunity = useCallback(async () => {
    if (!communityId) {
      setMembers([]);
      setSpaces([]);
      setDMThreads([]);
      return;
    }
    await latest(
      "community",
      async () => {
        const [memberOut, spaceOut, dmOut] = await Promise.all([
          api.members.list(communityId),
          api.spaces.list(communityId),
          api.dms.list(communityId),
        ]);
        return { members: memberOut.members || [], spaces: spaceOut.spaces || [], dms: dmOut.threads || [] };
      },
      (result) => {
        setMembers(result.members);
        setSpaces(result.spaces);
        setDMThreads(result.dms);
        setSpaceId((current) =>
          result.spaces.some((item) => item.id === current && item.kind !== "course")
            ? current
            : result.spaces.find((item) => item.kind !== "course")?.id || "",
        );
        setCourseId((current) =>
          result.spaces.some((item) => item.id === current && item.kind === "course")
            ? current
            : result.spaces.find((item) => item.kind === "course")?.id || "",
        );
      },
    );
  }, [communityId, latest]);

  const loadThreads = useCallback(async () => {
    if (!spaceId) {
      setThreads([]);
      setPosts([]);
      return;
    }
    await latest("threads", () => api.threads.list(spaceId), (result) => {
      const next = result.threads || [];
      setThreads(next);
      setThreadId((current) => next.some((item) => item.id === current) ? current : next[0]?.id || "");
    });
  }, [latest, spaceId]);

  const loadPosts = useCallback(async () => {
    if (!threadId) {
      setPosts([]);
      return;
    }
    await latest("posts", () => api.posts.list(threadId), (result) => setPosts(result.posts || []));
  }, [latest, threadId]);

  const loadCourse = useCallback(async () => {
    if (!courseId) {
      setSections([]);
      setLessons([]);
      setBundle(null);
      setCourseOffer(null);
      setCoursePurchase(null);
      setCourseAccessMode("free");
      return;
    }
    await latest(
      "course",
      async () => {
        const [details, sectionOut, offerOut, purchaseOut] = await Promise.all([
          api.courses.details(courseId),
          api.courses.sections(courseId),
          api.courses.offer(courseId),
          api.courses.purchase(courseId),
        ]);
        let lessonOut: { lessons: Lesson[] } | undefined;
        let locked = false;
        try {
          lessonOut = await api.courses.lessons(courseId);
        } catch (caught) {
          const message = caught instanceof Error ? caught.message : String(caught);
          if (!message.includes("active course enrollment required")) throw caught;
          locked = true;
        }
        return {
          details,
          sections: sectionOut.sections || [],
          lessons: lessonOut?.lessons || [],
          offer: offerOut.offer,
          purchase: purchaseOut.purchase,
          locked,
        };
      },
      (result) => {
        setSections(result.sections);
        setLessons(result.lessons);
        setCourseLocked(result.locked);
        setCourseOffer(result.offer);
        setCoursePurchase(result.purchase);
        setCourseAccessMode(result.details.enrollment_rules?.access_mode || "free");
        setCourseSummary(result.details.details?.summary || result.details.details?.description || "");
        setLessonId((current) =>
          result.lessons.some((item) => item.id === current) ? current : result.lessons[0]?.id || "",
        );
      },
    );
  }, [courseId, latest]);

  const loadBundle = useCallback(async () => {
    if (!lessonId) {
      setBundle(null);
      return;
    }
    await latest("lesson", () => api.courses.bundle(lessonId), setBundle);
  }, [latest, lessonId]);

  const loadDM = useCallback(async () => {
    if (!dmId) {
      setDM(null);
      return;
    }
    await latest("dm", () => api.dms.get(dmId), (result) => {
      setDM(result);
      void api.dms.markRead(dmId).catch(() => undefined);
    });
  }, [dmId, latest]);

  useEffect(() => {
    if (!auth) return;
    void run(loadSession, { preserveNotice: true });
  }, [auth, loadSession, run]);

  useEffect(() => {
    if (!auth) return;
    void run(loadCommunity, { preserveNotice: true }).then(() => {
      const url = new URL(window.location.href);
      const payment = url.searchParams.get("payment");
      if (!payment) return;
      if (payment === "success") setNotice("Payment submitted. Course access activates as soon as Billing confirms it.");
      if (payment === "cancelled") setNotice("Checkout was cancelled. You can resume it whenever you are ready.");
      url.searchParams.delete("payment");
      window.history.replaceState({}, "", url);
    });
  }, [auth, loadCommunity, run]);

  useEffect(() => {
    if (view === "spaces") void run(loadThreads, { quiet: true });
  }, [loadThreads, run, view]);

  useEffect(() => {
    if (view === "spaces") void run(loadPosts, { quiet: true });
  }, [loadPosts, run, view]);

  useEffect(() => {
    if (view === "courses") void run(loadCourse, { preserveNotice: true });
  }, [loadCourse, run, view]);

  useEffect(() => {
    if (view === "courses") void run(loadBundle, { quiet: true });
  }, [loadBundle, run, view]);

  useEffect(() => {
    if (!auth || !courseId || coursePurchase?.status !== "awaiting_payment") return;
    const timer = window.setInterval(() => {
      void api.courses.purchase(courseId).then((result) => {
        setCoursePurchase(result.purchase);
        if (result.purchase?.status === "fulfilled") void run(loadCourse, { quiet: true });
      }).catch(() => undefined);
    }, 5000);
    return () => window.clearInterval(timer);
  }, [auth, courseId, coursePurchase?.status, loadCourse, run]);

  useEffect(() => {
    if (view === "messages") void run(loadDM, { quiet: true });
  }, [loadDM, run, view]);

  useEffect(() => {
    if (!auth || !currentProjectId()) return;
    try {
      const subscription = apteva.subscribe<AppEvent>(
        `/api/app-events/${encodeURIComponent(COMMUNITY_APP)}`,
        { project_id: currentProjectId() },
        (event) => {
          const data = event.data ?? {};
          if (data.community_id && data.community_id !== communityId) return;
          if (eventTimer.current) window.clearTimeout(eventTimer.current);
          eventTimer.current = window.setTimeout(() => {
            if (event.topic.includes(".member.") || event.topic.includes(".space.")) void run(loadCommunity, { quiet: true });
            if (view === "spaces" && (!data.space_id || data.space_id === spaceId)) void run(loadThreads, { quiet: true });
            if (view === "spaces" && data.thread_id === threadId) void run(loadPosts, { quiet: true });
            if (view === "courses" && (!data.space_id || data.space_id === courseId)) void run(loadCourse, { quiet: true });
            if (view === "courses" && data.lesson_id === lessonId) void run(loadBundle, { quiet: true });
            if (view === "messages") void run(loadCommunity, { quiet: true });
          }, 250);
        },
      );
      return () => {
        if (eventTimer.current) window.clearTimeout(eventTimer.current);
        subscription.close();
      };
    } catch {
      return undefined;
    }
  }, [auth, communityId, courseId, lessonId, loadBundle, loadCommunity, loadCourse, loadPosts, loadThreads, run, spaceId, threadId, view]);

  async function authenticate(event: FormEvent) {
    event.preventDefault();
    await run(async () => {
      const organization_slug = authForm.organization_slug.trim() || undefined;
      const response = authMode === "login"
        ? await api.auth.login({
          client_id: authForm.client_id.trim(),
          organization_slug,
          email: authForm.email.trim(),
          password: authForm.password,
        })
        : await api.auth.signup({
          client_id: authForm.client_id.trim(),
          organization_slug,
          email: authForm.email.trim(),
          password: authForm.password,
          display_name: authForm.display_name.trim() || authForm.email.trim(),
        });
      if (!response.apteva_access_token) {
        if (response.verification_required) {
          setNotice("Account created. Verify your email, then log in.");
          setAuthMode("login");
          return;
        }
        throw new Error("Auth did not return a delegated access token.");
      }
      useDelegatedToken(response.apteva_access_token);
      window.sessionStorage.setItem(authSessionKey, JSON.stringify(response));
      setAuth(response);
      setAuthForm((current) => ({ ...current, password: "" }));
    });
  }

  function logout() {
    useDelegatedToken(undefined);
    window.sessionStorage.removeItem(authSessionKey);
    setAuth(null);
    setCommunities([]);
    setMemberships([]);
    setMembers([]);
    setSpaces([]);
    setError("");
    setNotice("Signed out.");
  }

  async function createThread(event: FormEvent) {
    event.preventDefault();
    if (!spaceId || !threadForm.body.trim()) return;
    await run(async () => {
      const created = await api.threads.create(spaceId, threadForm.title.trim(), threadForm.body.trim());
      setThreadForm({ title: "", body: "" });
      await loadThreads();
      setThreadId(created.thread.id);
    });
  }

  async function createPost(event: FormEvent) {
    event.preventDefault();
    if (!threadId || !reply.trim()) return;
    await run(async () => {
      await api.posts.create(threadId, reply.trim());
      setReply("");
      await Promise.all([loadPosts(), loadThreads()]);
    });
  }

  async function enroll() {
    if (!courseId) return;
    await run(async () => {
      await api.courses.enroll(courseId);
      setNotice("Enrollment submitted.");
      await loadCourse();
    });
  }

  async function purchaseCourse() {
    if (!courseId) return;
    await run(async () => {
      const success = new URL(window.location.href);
      success.searchParams.set("payment", "success");
      const cancel = new URL(window.location.href);
      cancel.searchParams.set("payment", "cancelled");
      const result = await api.courses.purchaseStart(courseId, success.toString(), cancel.toString());
      setCoursePurchase(result.purchase);
      if (result.enrolled) {
        setNotice("Course access is active.");
        await loadCourse();
        return;
      }
      if (!result.checkout_url) throw new Error("Billing did not return a checkout URL.");
      window.location.assign(result.checkout_url);
    });
  }

  async function cancelPurchase() {
    if (!courseId) return;
    await run(async () => {
      const result = await api.courses.purchaseCancel(courseId);
      setCoursePurchase(result.purchase);
      setNotice("Checkout cancelled.");
    });
  }

  async function completeLesson() {
    if (!lessonId) return;
    await run(async () => {
      await api.courses.mark(lessonId, "complete");
      await Promise.all([loadCourse(), loadBundle()]);
    });
  }

  async function postComment(event: FormEvent) {
    event.preventDefault();
    if (!lessonId || !comment.trim()) return;
    await run(async () => {
      await api.courses.comment(lessonId, comment.trim());
      setComment("");
      await loadBundle();
    });
  }

  async function openDM(event: FormEvent) {
    event.preventDefault();
    if (!communityId || !dmRecipient) return;
    await run(async () => {
      const opened = await api.dms.open(communityId, dmRecipient);
      setDMId(opened.id);
      setDMRecipient("");
      const listed = await api.dms.list(communityId);
      setDMThreads(listed.threads || []);
    });
  }

  async function sendDM(event: FormEvent) {
    event.preventDefault();
    if (!dmId || !dmBody.trim()) return;
    await run(async () => {
      await api.dms.send(dmId, dmBody.trim());
      setDMBody("");
      await loadDM();
    });
  }

  if (!auth) {
    return (
      <main className="auth-shell">
        <section className="auth-card" aria-labelledby="auth-title">
          <div className="brand-mark"><MessagesSquare aria-hidden="true" /></div>
          <p className="eyebrow">Community</p>
          <h1 id="auth-title">{authMode === "login" ? "Welcome back" : "Create your account"}</h1>
          <p className="muted">Sign in through Auth to reach your verified community memberships.</p>
          {error && <div className="alert error" role="alert">{error}</div>}
          {notice && <div className="alert success" role="status">{notice}</div>}
          <form className="stack" onSubmit={authenticate}>
            <Field label="Auth client ID" id="client-id">
              <input id="client-id" value={authForm.client_id} onChange={(event) => setAuthForm({ ...authForm, client_id: event.target.value })} autoComplete="username" required />
            </Field>
            <Field label="Organization (optional)" id="organization">
              <input id="organization" value={authForm.organization_slug} onChange={(event) => setAuthForm({ ...authForm, organization_slug: event.target.value })} />
            </Field>
            {authMode === "signup" && (
              <Field label="Display name" id="display-name">
                <input id="display-name" value={authForm.display_name} onChange={(event) => setAuthForm({ ...authForm, display_name: event.target.value })} autoComplete="name" />
              </Field>
            )}
            <Field label="Email" id="email">
              <input id="email" type="email" value={authForm.email} onChange={(event) => setAuthForm({ ...authForm, email: event.target.value })} autoComplete="email" required />
            </Field>
            <Field label="Password" id="password">
              <input id="password" type="password" value={authForm.password} onChange={(event) => setAuthForm({ ...authForm, password: event.target.value })} autoComplete={authMode === "login" ? "current-password" : "new-password"} required />
            </Field>
            <button className="primary" disabled={busy}>{busy ? "Please wait…" : authMode === "login" ? "Sign in" : "Sign up"}</button>
          </form>
          <button className="text-button" onClick={() => setAuthMode(authMode === "login" ? "signup" : "login")}>
            {authMode === "login" ? "Need an account? Sign up" : "Already registered? Sign in"}
          </button>
        </section>
      </main>
    );
  }

  if (!busy && communities.length === 0) {
    return (
      <main className="auth-shell">
        <section className="auth-card">
          <UserRound size={34} aria-hidden="true" />
          <h1>Membership pending</h1>
          <p className="muted">Your Auth account is valid, but it is not linked to an active Community member.</p>
          <div className="identity-box">
            <span>Auth user ID</span>
            <code>{auth.user.id}</code>
          </div>
          <p className="muted">Give this ID to a community operator so they can link it to your member profile.</p>
          <button className="secondary" onClick={logout}><LogOut size={16} /> Sign out</button>
        </section>
      </main>
    );
  }

  return (
    <div className="app-shell">
      <header className="mobile-header">
        <button className="icon-button" aria-label="Open navigation" onClick={() => setMenuOpen(true)}><Menu /></button>
        <strong>{selectedCommunity?.name || "Community"}</strong>
        <span className="avatar">{displayName(me).slice(0, 1).toUpperCase()}</span>
      </header>

      {menuOpen && <button className="nav-scrim" aria-label="Close navigation" onClick={() => setMenuOpen(false)} />}
      <aside className={`sidebar ${menuOpen ? "open" : ""}`}>
        <div className="brand">
          <div className="brand-mark"><MessagesSquare aria-hidden="true" /></div>
          <div><strong>Community</strong><span>Member portal</span></div>
          <button className="icon-button mobile-close" aria-label="Close navigation" onClick={() => setMenuOpen(false)}><X /></button>
        </div>
        <Field label="Community" id="community-select">
          <select id="community-select" value={communityId} onChange={(event) => setCommunityId(event.target.value)}>
            {communities.map((community) => <option key={community.id} value={community.id}>{community.name}</option>)}
          </select>
        </Field>
        <nav aria-label="Community navigation">
          {nav.map((item) => {
            const Icon = item.icon;
            return (
              <button key={item.id} className={view === item.id ? "active" : ""} aria-current={view === item.id ? "page" : undefined} onClick={() => { setView(item.id); setMenuOpen(false); }}>
                <Icon size={18} aria-hidden="true" />{item.label}
                {item.id === "messages" && dmThreads.some((thread) => (thread.unread_count || 0) > 0) && <span className="unread-dot" />}
              </button>
            );
          })}
        </nav>
        <div className="sidebar-user">
          <span className="avatar">{displayName(me).slice(0, 1).toUpperCase()}</span>
          <div><strong>{displayName(me)}</strong><span>@{me?.handle}</span></div>
          <button className="icon-button" onClick={logout} aria-label="Sign out"><LogOut size={17} /></button>
        </div>
      </aside>

      <main className="content">
        <header className="topbar">
          <div><p className="eyebrow">{selectedCommunity?.slug}</p><h1>{nav.find((item) => item.id === view)?.label}</h1></div>
          <button className="secondary" onClick={() => void run(loadCommunity)} disabled={busy}><RefreshCcw size={16} className={busy ? "spin" : ""} /> Refresh</button>
        </header>
        {error && <div className="alert error" role="alert">{error}</div>}
        {notice && <div className="alert success" role="status">{notice}</div>}

        {view === "home" && (
          <div className="stack">
            <section className="hero">
              <div><p className="eyebrow">Welcome back</p><h2>{displayName(me)}</h2><p>{selectedCommunity?.description || "Your community conversations, courses, and people in one place."}</p></div>
              <div className="hero-icon"><MessagesSquare aria-hidden="true" /></div>
            </section>
            <div className="metrics">
              <Metric label="Members" value={members.length} />
              <Metric label="Spaces" value={discussionSpaces.length} />
              <Metric label="Courses" value={courses.length} />
              <Metric label="Unread messages" value={dmThreads.reduce((sum, thread) => sum + (thread.unread_count || 0), 0)} />
            </div>
          </div>
        )}

        {view === "spaces" && (
          <div className="three-pane">
            <section className="pane">
              <h2>Spaces</h2>
              <ItemList empty="No discussion spaces yet.">
                {discussionSpaces.map((space) => <button key={space.id} className={space.id === spaceId ? "selected" : ""} onClick={() => setSpaceId(space.id)}><strong>{space.name}</strong><span>{space.kind}</span></button>)}
              </ItemList>
            </section>
            <section className="pane">
              <h2>{selectedSpace?.name || "Threads"}</h2>
              <ItemList empty="Start the first conversation.">
                {threads.map((thread) => <button key={thread.id} className={thread.id === threadId ? "selected" : ""} onClick={() => setThreadId(thread.id)}><strong>{thread.title || "Conversation"}</strong><span>{thread.post_count} posts</span></button>)}
              </ItemList>
              {spaceId && <form className="composer" onSubmit={createThread}>
                <Field label="Thread title" id="thread-title"><input id="thread-title" value={threadForm.title} onChange={(event) => setThreadForm({ ...threadForm, title: event.target.value })} /></Field>
                <Field label="Opening message" id="thread-body"><textarea id="thread-body" value={threadForm.body} onChange={(event) => setThreadForm({ ...threadForm, body: event.target.value })} required /></Field>
                <button className="primary" disabled={busy}>Start thread</button>
              </form>}
            </section>
            <section className="pane conversation-pane">
              <h2>{selectedThread?.title || "Conversation"}</h2>
              <div className="messages">
                {posts.map((post) => <article className={`message ${post.author_id === me?.id ? "mine" : ""}`} key={post.id}>
                  <header><strong>{displayName(memberMap.get(post.author_id))}</strong><time>{new Date(post.created_at).toLocaleString()}</time></header>
                  <p>{post.body}</p>
                  <div className="message-actions">
                    {["👍", "❤️", "🎉"].map((emoji) => <button key={emoji} aria-label={`React ${emoji}`} onClick={() => void run(async () => { await api.posts.react(post.id, emoji); await loadPosts(); }, { quiet: true })}>{emoji} {post.reactions?.find((item) => item.emoji === emoji)?.count || ""}</button>)}
                    {post.author_id === me?.id && <button onClick={() => {
                      const body = window.prompt("Edit your post", post.body);
                      if (body?.trim()) void run(async () => { await api.posts.edit(post.id, body.trim()); await loadPosts(); });
                    }}>Edit</button>}
                    {post.author_id === me?.id && <button onClick={() => {
                      if (window.confirm("Remove this post?")) void run(async () => { await api.posts.remove(post.id); await loadPosts(); });
                    }}>Remove</button>}
                  </div>
                </article>)}
                {!threadId && <Empty>Select a thread to read the conversation.</Empty>}
              </div>
              {threadId && <form className="reply" onSubmit={createPost}><Field label="Reply" id="reply"><textarea id="reply" value={reply} onChange={(event) => setReply(event.target.value)} required /></Field><button className="primary" disabled={busy}><Send size={16} /> Send</button></form>}
            </section>
          </div>
        )}

        {view === "courses" && (
          <div className="course-layout">
            <section className="pane">
              <h2>Courses</h2>
              <ItemList empty="No courses available.">
                {courses.map((course) => <button key={course.id} className={course.id === courseId ? "selected" : ""} onClick={() => setCourseId(course.id)}><strong>{course.name}</strong><span>{course.visibility}</span></button>)}
              </ItemList>
            </section>
            <section className="course-content">
              <div className="course-heading">
                <div>
                  <p className="eyebrow">Course</p>
                  <h2>{selectedCourse?.name || "Choose a course"}</h2>
                  <p className="muted">{courseSummary}</p>
                  {courseOffer && <span className="course-price">{formatCoursePrice(courseOffer)}</span>}
                </div>
                {courseLocked && courseId && courseAccessMode === "free" && <button className="primary" onClick={enroll}>Enroll</button>}
                {courseLocked && courseId && courseAccessMode === "paid" && courseOffer && (
                  <div className="purchase-actions">
                    <button className="primary" onClick={purchaseCourse} disabled={busy || coursePurchase?.status === "refund_pending"}>
                      {coursePurchase?.status === "awaiting_payment" || coursePurchase?.status === "payment_failed"
                        ? "Continue checkout"
                        : `Buy course · ${formatCoursePrice(courseOffer)}`}
                    </button>
                    {coursePurchase?.status === "awaiting_payment" && (
                      <button className="secondary" onClick={cancelPurchase} disabled={busy}>Cancel</button>
                    )}
                  </div>
                )}
              </div>
              {courseLocked ? (
                <div className="purchase-card">
                  {courseAccessMode === "paid" && !courseOffer && <Empty>This course is not currently for sale.</Empty>}
                  {courseAccessMode === "paid" && courseOffer && (
                    <>
                      <strong>{coursePurchase?.status === "awaiting_payment" ? "Checkout ready" : "Purchase to unlock this course"}</strong>
                      <p className="muted">
                        {coursePurchase?.status === "payment_failed"
                          ? "The last payment attempt did not complete. You can safely retry."
                          : "Payment is handled by Billing. Access activates only after the payment is confirmed."}
                      </p>
                    </>
                  )}
                  {courseAccessMode === "free" && <Empty>Enroll to unlock the published lessons.</Empty>}
                  {(courseAccessMode === "invite" || courseAccessMode === "manual") && <Empty>Contact a community operator for access to this course.</Empty>}
                </div>
              ) : (
                <div className="lesson-layout">
                  <aside className="lesson-list">
                    {sections.map((section) => <div key={section.id}><h3>{section.title}</h3>{lessons.filter((lesson) => lesson.section_id === section.id).map((lesson) => <button key={lesson.id} className={lesson.id === lessonId ? "selected" : ""} onClick={() => setLessonId(lesson.id)}><span>{lesson.title}</span>{lesson.progress?.status === "complete" && <CheckCircle2 size={16} aria-label="Completed" />}</button>)}</div>)}
                  </aside>
                  <article className="lesson-reader">
                    {bundle ? <>
                      <div className="lesson-title"><div><p className="eyebrow">Lesson</p><h2>{bundle.lesson.title}</h2></div><button className="secondary" onClick={completeLesson} disabled={bundle.lesson.progress?.status === "complete"}><CheckCircle2 size={16} />{bundle.lesson.progress?.status === "complete" ? "Completed" : "Mark complete"}</button></div>
                      <div className="lesson-body">{bundle.lesson.body}</div>
                      {bundle.resources.length > 0 && <section><h3>Resources</h3><ul>{bundle.resources.map((resource) => <li key={resource.id}>{resource.name || resource.storage_file_id}</li>)}</ul></section>}
                      {bundle.assignments.length > 0 && <section><h3>Assignments</h3>{bundle.assignments.map((assignment) => <article className="card" key={assignment.id}><strong>{assignment.title}</strong><p>{assignment.instructions}</p></article>)}</section>}
                      {bundle.quizzes.length > 0 && <section><h3>Quizzes</h3>{bundle.quizzes.map((quiz) => <article className="card" key={quiz.id}><strong>{quiz.title}</strong><span>Pass mark {quiz.passing_score}%</span></article>)}</section>}
                      <section><h3>Discussion</h3><div className="comment-list">{bundle.comments.map((item) => <article key={item.id}><strong>{displayName(memberMap.get(item.member_id))}</strong><p>{item.body}</p></article>)}</div><form className="reply" onSubmit={postComment}><Field label="Add a comment" id="lesson-comment"><textarea id="lesson-comment" value={comment} onChange={(event) => setComment(event.target.value)} required /></Field><button className="primary" disabled={busy}>Comment</button></form></section>
                    </> : <Empty>{lessonId ? "Loading lesson…" : "Choose a lesson."}</Empty>}
                  </article>
                </div>
              )}
            </section>
          </div>
        )}

        {view === "members" && <section className="member-grid" aria-label="Member directory">{members.map((member) => <article className="member-card" key={member.id}><span className="avatar large">{displayName(member).slice(0, 1).toUpperCase()}</span><div><h2>{displayName(member)}</h2><p>@{member.handle}</p><p>{member.bio}</p></div>{member.id !== me?.id && <button className="secondary" onClick={() => { setDMRecipient(member.id); setView("messages"); }}>Message</button>}</article>)}</section>}

        {view === "messages" && (
          <div className="message-layout">
            <section className="pane">
              <h2>Messages</h2>
              <form className="composer" onSubmit={openDM}><Field label="Start a conversation" id="dm-recipient"><select id="dm-recipient" value={dmRecipient} onChange={(event) => setDMRecipient(event.target.value)}><option value="">Choose a member</option>{members.filter((member) => member.id !== me?.id).map((member) => <option key={member.id} value={member.id}>{displayName(member)}</option>)}</select></Field><button className="primary" disabled={!dmRecipient || busy}>Open</button></form>
              <ItemList empty="No direct messages yet.">{dmThreads.map((thread) => {
                const other = thread.participants.find((id) => id !== me?.id);
                return <button key={thread.id} className={thread.id === dmId ? "selected" : ""} onClick={() => setDMId(thread.id)}><strong>{displayName(memberMap.get(other || ""))}</strong><span>{thread.unread_count ? `${thread.unread_count} unread` : "Up to date"}</span></button>;
              })}</ItemList>
            </section>
            <section className="pane conversation-pane">
              <h2>{dm ? displayName(memberMap.get(dm.participants.find((id) => id !== me?.id) || "")) : "Conversation"}</h2>
              <div className="messages">{dm?.messages.map((message) => <article key={message.id} className={`message ${message.author_id === me?.id ? "mine" : ""}`}><header><strong>{displayName(memberMap.get(message.author_id))}</strong><time>{new Date(message.created_at).toLocaleString()}</time></header><p>{message.body}</p></article>)}{!dm && <Empty>Choose a conversation.</Empty>}</div>
              {dm && <form className="reply" onSubmit={sendDM}><Field label="Message" id="dm-body"><textarea id="dm-body" value={dmBody} onChange={(event) => setDMBody(event.target.value)} required /></Field><button className="primary" disabled={busy}><Send size={16} /> Send</button></form>}
            </section>
          </div>
        )}

        {view === "profile" && <section className="profile-card"><span className="avatar xlarge">{displayName(me).slice(0, 1).toUpperCase()}</span><div><p className="eyebrow">Member profile</p><h2>{displayName(me)}</h2><p className="muted">@{me?.handle}</p><p>{me?.bio || "No bio yet."}</p><p className="muted">Signed in as {auth.user.email}</p></div><button className="secondary" onClick={logout}><LogOut size={16} /> Sign out</button></section>}
      </main>
    </div>
  );
}

function Field({ label, id, children }: { label: string; id: string; children: ReactNode }) {
  return <label className="field" htmlFor={id}><span>{label}</span>{children}</label>;
}

function ItemList({ children, empty }: { children: ReactNode; empty: string }) {
  return <div className="item-list">{children || <Empty>{empty}</Empty>}</div>;
}

function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

function Metric({ label, value }: { label: string; value: number }) {
  return <article className="metric"><strong>{value}</strong><span>{label}</span></article>;
}
