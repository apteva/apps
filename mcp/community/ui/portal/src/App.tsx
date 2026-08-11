import {
  ArrowRight,
  BadgeCheck,
  BookOpen,
  CheckCircle2,
  CreditCard,
  Home,
  LockKeyhole,
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
import { loadStripe, type Stripe, type StripeElements, type StripePaymentElement } from "@stripe/stripe-js";
import { type CSSProperties, FormEvent, ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, apteva, COMMUNITY_APP, currentProjectId, type AuthResponse, type LessonBundle, type PortalBootstrap, type StorefrontCheckoutSession, useDelegatedToken } from "./api";
import type {
  Community,
  CourseOffer,
  CoursePurchase,
  DMThread,
  DMThreadView,
  EnrollmentRule,
  Lesson,
  Member,
  MemberSubscription,
  MembershipPlan,
  Post,
  PublicOffer,
  PublicProduct,
  Section,
  Space,
  Thread,
} from "./types";

type View = "home" | "spaces" | "courses" | "members" | "messages" | "profile";
type AuthMode = "login" | "signup" | "forgot" | "reset" | "verify_pending";
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

function requestedCommunitySlug(): string {
  if (typeof window === "undefined") return "";
  return new URLSearchParams(window.location.search).get("community")?.trim().toLowerCase() || "";
}

function requestedCourse(): string {
  if (typeof window === "undefined") return "";
  return new URLSearchParams(window.location.search).get("course")?.trim() || "";
}

function requestedProduct(): string {
  if (typeof window === "undefined") return "";
  return new URLSearchParams(window.location.search).get("product")?.trim() || "";
}

function requestedOffer(): number {
  if (typeof window === "undefined") return 0;
  const value = Number(new URLSearchParams(window.location.search).get("offer"));
  return Number.isSafeInteger(value) && value > 0 ? value : 0;
}

function wantsCourseCheckout(): boolean {
  if (typeof window === "undefined") return false;
  const params = new URLSearchParams(window.location.search);
  return !params.has("payment") && params.get("intent") === "buy";
}

function hasPaymentResult(): boolean {
  return typeof window !== "undefined" && new URLSearchParams(window.location.search).has("payment");
}

function initialAuthMode(): AuthMode {
  if (typeof window === "undefined") return "login";
  return new URLSearchParams(window.location.search).get("auth") === "signup" ? "signup" : "login";
}

function authSessionKey(): string {
  return `apteva-community-auth:${requestedCommunitySlug() || "default"}`;
}

function initialAuthSession(): AuthResponse | null {
  if (typeof window === "undefined") return null;
  try {
    const stored = JSON.parse(window.sessionStorage.getItem(authSessionKey()) || "null") as AuthResponse | null;
    if (!stored?.apteva_access_token || !stored.user) return null;
    useDelegatedToken(stored.apteva_access_token);
    return stored;
  } catch {
    window.sessionStorage.removeItem(authSessionKey());
    return null;
  }
}

function initialView(): View {
  if (typeof window === "undefined") return "home";
  const params = new URLSearchParams(window.location.search);
  return params.has("payment") || params.has("course") ? "courses" : "home";
}

function initialAuthForm() {
  return {
    email: "",
    password: "",
    password_confirm: "",
    display_name: "",
  };
}

function friendlyError(error: unknown): string {
  const message = error instanceof Error ? error.message : String(error);
  if (message.includes("not linked to an active member")) {
    return "We could not activate your community access. Please contact support.";
  }
  if (message.includes("requires an existing membership")) return "This community is currently available to approved members only.";
  if (message.includes("different community")) return "This account belongs to a different community. Sign out and use the correct account.";
  if (message.includes("active course enrollment required")) return "Enroll in this course to open its lessons.";
  if (message.includes("lesson is not available yet")) return "This lesson is scheduled for a later date.";
  if (message.includes("email_unverified")) return "Please verify your email before signing in.";
  if (message.includes("invalid_credentials") || message.includes("invalid credentials")) return "The email or password is incorrect.";
  if (message.includes("origin_not_allowed")) return "This course portal is not configured for this address. Please contact support.";
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

function formatMembershipPrice(plan: MembershipPlan): string {
  const amount = (() => {
    try {
      return new Intl.NumberFormat(undefined, { style: "currency", currency: plan.currency })
        .format(plan.unit_amount_cents / 100);
    } catch {
      return `${plan.currency} ${(plan.unit_amount_cents / 100).toFixed(2)}`;
    }
  })();
  const count = plan.interval_count > 1 ? `${plan.interval_count} ` : "";
  return `${amount} / ${count}${plan.interval}${plan.interval_count > 1 ? "s" : ""}`;
}

function formatPublicOffer(offer: PublicOffer): string {
  const amount = (() => {
    try {
      return new Intl.NumberFormat(undefined, { style: "currency", currency: offer.currency })
        .format(offer.unit_amount_cents / 100);
    } catch {
      return `${offer.currency} ${(offer.unit_amount_cents / 100).toFixed(2)}`;
    }
  })();
  if (!offer.interval) return amount;
  const count = (offer.interval_count || 1) > 1 ? `${offer.interval_count} ` : "";
  return `${amount} / ${count}${offer.interval}${(offer.interval_count || 1) > 1 ? "s" : ""}`;
}

export default function App() {
  const [auth, setAuth] = useState<AuthResponse | null>(initialAuthSession);
  const [authMode, setAuthMode] = useState<AuthMode>(initialAuthMode);
  const [authForm, setAuthForm] = useState(initialAuthForm);
  const [portal, setPortal] = useState<PortalBootstrap | null>(null);
  const [portalError, setPortalError] = useState("");
  const [publicProduct, setPublicProduct] = useState<PublicProduct | null>(null);
  const [publicProductLoading, setPublicProductLoading] = useState(Boolean(requestedProduct()));
  const [publicProductError, setPublicProductError] = useState("");
  const [storefrontPriceId, setStorefrontPriceId] = useState(requestedOffer);
  const [storefrontAuthRequested, setStorefrontAuthRequested] = useState(() => requestedOffer() > 0 || wantsCourseCheckout());
  const [storefrontCheckout, setStorefrontCheckout] = useState<StorefrontCheckoutSession | null>(null);
  const [recoveryToken, setRecoveryToken] = useState("");
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
  const [membershipPlans, setMembershipPlans] = useState<MembershipPlan[]>([]);
  const [memberSubscription, setMemberSubscription] = useState<MemberSubscription | null>(null);
  const [comment, setComment] = useState("");

  const [dmId, setDMId] = useState("");
  const [dm, setDM] = useState<DMThreadView | null>(null);
  const [dmRecipient, setDMRecipient] = useState("");
  const [dmBody, setDMBody] = useState("");

  const requestVersion = useRef<Record<string, number>>({});
  const eventTimer = useRef<number | undefined>(undefined);
  const automaticCheckoutStarted = useRef(false);
  const busy = pending > 0;

  const selectedCommunity = communities.find((item) => item.id === communityId);
  const me = memberships.find((item) => item.community_id === communityId);
  const discussionSpaces = spaces.filter((item) => item.kind !== "course");
  const courses = spaces.filter((item) => item.kind === "course");
  const selectedSpace = spaces.find((item) => item.id === spaceId);
  const selectedThread = threads.find((item) => item.id === threadId);
  const selectedCourse = courses.find((item) => item.id === courseId);
  const selectedLesson = lessons.find((item) => item.id === lessonId);
  const selectedStorefrontOffer = publicProduct?.offers.find((offer) => offer.catalog_price_id === storefrontPriceId);
  const memberMap = useMemo(() => new Map(members.map((member) => [member.id, member])), [members]);
  const brandName = portal?.brand.name || "your community";

  useEffect(() => {
    let cancelled = false;
    api.portal.bootstrap(requestedCommunitySlug()).then((value) => {
      if (cancelled) return;
      setPortal(value);
      setPortalError("");
      document.title = `${value.brand.name} courses`;
      document.documentElement.style.setProperty("--brand", value.brand.primary_color);
      document.documentElement.style.setProperty("--brand-dark", value.brand.primary_color);
      document.documentElement.style.setProperty("--brand-accent", value.brand.accent_color);
      if (value.brand.favicon_url) {
        let favicon = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
        if (!favicon) {
          favicon = document.createElement("link");
          favicon.rel = "icon";
          document.head.appendChild(favicon);
        }
        favicon.href = value.brand.favicon_url;
      }
    }).catch((caught) => {
      if (!cancelled) setPortalError(friendlyError(caught));
    });
    return () => { cancelled = true; };
  }, []);

  useEffect(() => {
    const slug = requestedProduct();
    if (!portal || !slug) {
      setPublicProductLoading(false);
      return;
    }
    let cancelled = false;
    setPublicProductLoading(true);
    api.portal.product(portal.community.slug, slug).then((result) => {
      if (cancelled) return;
      setPublicProduct(result.product);
      setPublicProductError("");
      document.title = `${result.product.name} · ${portal.brand.name}`;
    }).catch((caught) => {
      if (!cancelled) setPublicProductError(friendlyError(caught));
    }).finally(() => {
      if (!cancelled) setPublicProductLoading(false);
    });
    return () => { cancelled = true; };
  }, [portal]);

  useEffect(() => {
    if (!publicProduct || !wantsCourseCheckout() || storefrontPriceId > 0 || publicProduct.offers.length === 0) return;
    const preferred = publicProduct.offers.find((offer) => offer.interval === "year") || publicProduct.offers[0];
    const url = new URL(window.location.href);
    url.searchParams.set("offer", String(preferred.catalog_price_id));
    url.searchParams.set("auth", portal?.signup.enabled ? "signup" : "login");
    window.history.replaceState({}, "", url);
    setStorefrontPriceId(preferred.catalog_price_id);
    setStorefrontAuthRequested(true);
    if (portal?.signup.enabled) setAuthMode("signup");
  }, [portal, publicProduct, storefrontPriceId]);

  useEffect(() => {
    if (!portal || typeof window === "undefined") return;
    const fragment = new URLSearchParams(window.location.hash.replace(/^#/, ""));
    const verifyToken = fragment.get("verify");
    const resetToken = fragment.get("reset");
    if (resetToken) {
      setRecoveryToken(resetToken);
      setAuthMode("reset");
      window.history.replaceState({}, "", window.location.pathname + window.location.search);
      return;
    }
    if (!verifyToken) return;
    window.history.replaceState({}, "", window.location.pathname + window.location.search);
    void run(async () => {
      const response = await api.auth.verifyEmail({
        client_id: portal.auth.client_id,
        organization_slug: portal.auth.organization_slug,
        token: verifyToken,
      });
      finishAuthentication(response);
      setNotice("Your email is verified. Welcome!");
    });
  }, [portal]);

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
      const targetID = portal?.community.id || "";
      const targetCommunities = (session.communities || []).filter((item) => item.id === targetID);
      const targetMemberships = (session.memberships || []).filter((item) => item.community_id === targetID);
      setCommunities(targetCommunities);
      setMemberships(targetMemberships);
      setCommunityId(targetCommunities[0]?.id || "");
    });
  }, [latest, portal]);

  const loadCommunity = useCallback(async () => {
    if (!communityId) {
      setMembers([]);
      setSpaces([]);
      setDMThreads([]);
      setMembershipPlans([]);
      setMemberSubscription(null);
      return;
    }
    await latest(
      "community",
      async () => {
        const [memberOut, spaceOut, dmOut, planOut, membershipOut] = await Promise.all([
          api.members.list(communityId),
          api.spaces.list(communityId),
          api.dms.list(communityId),
          api.memberships.plans(communityId),
          api.memberships.status(communityId),
        ]);
        return {
          members: memberOut.members || [],
          spaces: spaceOut.spaces || [],
          dms: dmOut.threads || [],
          plans: planOut.plans || [],
          membership: membershipOut.subscription,
        };
      },
      (result) => {
        setMembers(result.members);
        setSpaces(result.spaces);
        setDMThreads(result.dms);
        setMembershipPlans(result.plans);
        setMemberSubscription(result.membership);
        setSpaceId((current) =>
          result.spaces.some((item) => item.id === current && item.kind !== "course")
            ? current
            : result.spaces.find((item) => item.kind !== "course")?.id || "",
        );
        setCourseId((current) => {
          if (result.spaces.some((item) => item.id === current && item.kind === "course")) return current;
          const requested = requestedCourse();
          return result.spaces.find((item) => item.kind === "course" && (item.id === requested || item.slug === requested))?.id
            || result.spaces.find((item) => item.kind === "course")?.id
            || "";
        });
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
    if (!auth || !portal) return;
    void run(async () => {
      await api.members.ensure(portal.community.id, auth.user.display_name);
      await loadSession();
    }, { preserveNotice: true });
  }, [auth, loadSession, portal, run]);

  useEffect(() => {
    if (!auth?.refresh_token || !portal) return;
    let cancelled = false;
    let timer = 0;
    const refreshSession = async () => {
      try {
        const response = await api.auth.refresh({
          client_id: portal.auth.client_id,
          organization_slug: portal.auth.organization_slug,
          refresh_token: auth.refresh_token!,
        });
        if (!cancelled) finishAuthentication(response);
      } catch {
        if (!cancelled) timer = window.setTimeout(refreshSession, 30_000);
      }
    };
    const ttl = Math.max(60, auth.apteva_expires_in || 3600) * 1000;
    const elapsed = auth.stored_at ? Math.max(0, Date.now() - auth.stored_at) : ttl;
    timer = window.setTimeout(refreshSession, Math.max(1000, Math.floor(ttl * 0.8) - elapsed));
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [auth?.refresh_token, auth?.stored_at, portal]);

  useEffect(() => {
    if (!auth) return;
    void run(loadCommunity, { preserveNotice: true }).then(() => {
      const url = new URL(window.location.href);
      const payment = url.searchParams.get("payment");
      if (!payment) return;
      if (payment === "success") setNotice("Payment submitted. Your course will unlock as soon as it is confirmed.");
      if (payment === "cancelled") setNotice("Checkout was cancelled. You can resume it whenever you are ready.");
      if (payment === "membership-success") setNotice("Payment submitted. Your membership will activate as soon as it is confirmed.");
      if (payment === "membership-cancelled") setNotice("Membership checkout was cancelled. You can resume it whenever you are ready.");
      url.searchParams.delete("payment");
      url.searchParams.delete("intent");
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
    if (!auth || !portal || !wantsCourseCheckout() || automaticCheckoutStarted.current) return;
    if (!courseId || !courseLocked || courseAccessMode !== "paid" || !courseOffer) return;
    if (coursePurchase?.status === "fulfilled" || coursePurchase?.status === "refunded") return;
    automaticCheckoutStarted.current = true;
    void purchaseCourse();
  }, [auth, courseAccessMode, courseId, courseLocked, courseOffer, coursePurchase?.status, portal]);

  useEffect(() => {
    if (!auth || !portal || communityId !== portal.community.id || !selectedStorefrontOffer || automaticCheckoutStarted.current) return;
    automaticCheckoutStarted.current = true;
    void startStorefrontCheckout(selectedStorefrontOffer).then((started) => {
      if (!started) automaticCheckoutStarted.current = false;
    });
  }, [auth, communityId, portal, selectedStorefrontOffer]);

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
            if (event.topic.includes(".member.") || event.topic.includes(".space.") || event.topic.includes(".membership.")) {
              void run(loadCommunity, { quiet: true });
            }
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

  function finishAuthentication(response: AuthResponse) {
    if (!response.apteva_access_token) throw new Error("Sign-in did not complete. Please try again.");
    useDelegatedToken(response.apteva_access_token);
    const next = { ...response, stored_at: Date.now() };
    window.sessionStorage.setItem(authSessionKey(), JSON.stringify(next));
    setAuth(next);
    setAuthMode("login");
    setAuthForm((current) => ({ ...current, password: "" }));
  }

  async function authenticate(event: FormEvent) {
    event.preventDefault();
    if (!portal) return;
    await run(async () => {
      let response: AuthResponse;
      try {
        response = authMode === "login"
          ? await api.auth.login({
            client_id: portal.auth.client_id,
            organization_slug: portal.auth.organization_slug,
            email: authForm.email.trim(),
            password: authForm.password,
          })
          : await api.auth.signup({
            client_id: portal.auth.client_id,
            organization_slug: portal.auth.organization_slug,
            email: authForm.email.trim(),
            password: authForm.password,
            display_name: authForm.display_name.trim() || authForm.email.trim(),
            continue_url: window.location.href.split("#", 1)[0],
          });
      } catch (caught) {
        if (authMode === "login" && String(caught).includes("email_unverified")) {
          setAuthMode("verify_pending");
          setNotice("Please verify your email to continue. You can request a new link below.");
          return;
        }
        throw caught;
      }
      if (!response.apteva_access_token) {
        if (response.verification_required) {
          setNotice(`We sent a verification link to ${authForm.email.trim()}.`);
          setAuthMode("verify_pending");
          return;
        }
        throw new Error("Sign-in did not complete. Please try again.");
      }
      finishAuthentication(response);
    });
  }

  async function requestPasswordReset(event: FormEvent) {
    event.preventDefault();
    if (!portal) return;
    await run(async () => {
      await api.auth.requestPasswordReset({
        client_id: portal.auth.client_id,
        organization_slug: portal.auth.organization_slug,
        email: authForm.email.trim(),
        continue_url: window.location.href.split("#", 1)[0],
      });
      setNotice(`If an account exists for ${authForm.email.trim()}, a reset link is on its way.`);
    });
  }

  async function confirmPasswordReset(event: FormEvent) {
    event.preventDefault();
    if (!portal || !recoveryToken) return;
    if (authForm.password !== authForm.password_confirm) {
      setError("Passwords do not match.");
      return;
    }
    await run(async () => {
      const response = await api.auth.confirmPasswordReset({
        client_id: portal.auth.client_id,
        organization_slug: portal.auth.organization_slug,
        token: recoveryToken,
        password: authForm.password,
      });
      finishAuthentication(response);
      setRecoveryToken("");
      setNotice("Your password has been updated.");
    });
  }

  async function resendVerification() {
    if (!portal || !authForm.email.trim()) return;
    await run(async () => {
      await api.auth.resendVerification({
        client_id: portal.auth.client_id,
        organization_slug: portal.auth.organization_slug,
        email: authForm.email.trim(),
        continue_url: window.location.href.split("#", 1)[0],
      });
      setNotice(`A new verification link was sent to ${authForm.email.trim()}.`);
    });
  }

  function logout() {
    const refreshToken = auth?.refresh_token;
    useDelegatedToken(undefined);
    window.sessionStorage.removeItem(authSessionKey());
    setAuth(null);
    setCommunities([]);
    setMemberships([]);
    setMembers([]);
    setSpaces([]);
    setError("");
    setNotice("Signed out.");
    if (refreshToken) void api.auth.logout(refreshToken).catch(() => undefined);
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
      if (!result.checkout_url) throw new Error("Checkout is temporarily unavailable. Please try again.");
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

  async function startMembership(planId: string) {
    await run(async () => {
      const success = new URL(window.location.href);
      success.searchParams.set("payment", "membership-success");
      const cancel = new URL(window.location.href);
      cancel.searchParams.set("payment", "membership-cancelled");
      const result = await api.memberships.checkout(planId, success.toString(), cancel.toString());
      setMemberSubscription(result.subscription);
      if (result.access_active && !result.checkout_url) {
        setNotice("Membership access is active.");
        await Promise.all([loadCommunity(), loadCourse()]);
        return;
      }
      if (!result.checkout_url) throw new Error("Checkout is temporarily unavailable. Please try again.");
      window.location.assign(result.checkout_url);
    });
  }

  async function startStorefrontCheckout(offer: PublicOffer): Promise<boolean> {
    if (!portal) return false;
    const result = await run(async () => {
      const success = new URL(window.location.href);
      success.searchParams.set("payment", offer.kind === "recurring" ? "membership-success" : "success");
      const cancel = new URL(window.location.href);
      cancel.searchParams.set("payment", offer.kind === "recurring" ? "membership-cancelled" : "cancelled");
      const returnURL = new URL(success);
      returnURL.searchParams.delete("intent");
      return api.storefront.checkout(portal.community.id, offer.catalog_price_id, success.toString(), cancel.toString(), returnURL.toString());
    });
    if (!result) return false;
    if ((result.enrolled || result.access_active) && !result.checkout_url) {
      setNotice("Your access is active.");
      await Promise.all([loadCommunity(), loadCourse()]);
      return true;
    }
    if (result.presentation === "elements" && result.client_secret && result.publishable_key) {
      setStorefrontCheckout(result);
      return true;
    }
    if (!result.checkout_url) {
      setError("Checkout is temporarily unavailable. Please try again.");
      return false;
    }
    window.location.assign(result.checkout_url);
    return true;
  }

  function selectStorefrontOffer(offer: PublicOffer) {
    const url = new URL(window.location.href);
    url.searchParams.set("product", publicProduct?.slug || requestedProduct());
    url.searchParams.set("offer", String(offer.catalog_price_id));
    window.history.replaceState({}, "", url);
    automaticCheckoutStarted.current = false;
    setStorefrontCheckout(null);
    setStorefrontPriceId(offer.catalog_price_id);
  }

  function chooseStorefrontOffer(offer: PublicOffer) {
    selectStorefrontOffer(offer);
    const url = new URL(window.location.href);
    url.searchParams.set("intent", "buy");
    url.searchParams.set("auth", portal?.signup.enabled ? "signup" : "login");
    window.history.replaceState({}, "", url);
    setStorefrontAuthRequested(true);
    if (portal?.signup.enabled) setAuthMode("signup");
  }

  function chooseStorefrontAuthMode(mode: "login" | "signup") {
    const url = new URL(window.location.href);
    url.searchParams.set("auth", mode);
    window.history.replaceState({}, "", url);
    setAuthMode(mode);
    setStorefrontAuthRequested(true);
  }

  async function cancelMembership() {
    if (!memberSubscription) return;
    await run(async () => {
      const result = await api.memberships.cancel(memberSubscription.id, true);
      setMemberSubscription(result.subscription);
      setNotice("Your membership will remain active until the current period ends.");
    });
  }

  async function resumeMembership() {
    if (!memberSubscription) return;
    await run(async () => {
      const result = await api.memberships.resume(memberSubscription.id);
      setMemberSubscription(result.subscription);
      setNotice("Membership renewal resumed.");
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

  if (!portal && !portalError) {
    return <main className="auth-shell"><section className="auth-card"><p className="muted">Loading your courses…</p></section></main>;
  }

  if (portalError) {
    return <main className="auth-shell"><section className="auth-card"><h1>Courses unavailable</h1><div className="alert error" role="alert">{portalError}</div></section></main>;
  }

  if (publicProductLoading && portal) {
    return <main className="auth-shell"><section className="auth-card"><p className="muted">Loading product…</p></section></main>;
  }

  if (publicProductError) {
    return <main className="auth-shell"><section className="auth-card"><h1>Product unavailable</h1><div className="alert error" role="alert">{publicProductError}</div></section></main>;
  }

  if (portal && publicProduct && !hasPaymentResult()) {
    const accountPanel = !auth && storefrontAuthRequested && (authMode === "login" || authMode === "signup") ? (
      <CheckoutAccountForm
        mode={authMode}
        form={authForm}
        busy={busy}
        error={error}
        notice={notice}
        signupEnabled={portal.signup.enabled}
        brandName={brandName}
        onChange={setAuthForm}
        onSubmit={authenticate}
        onModeChange={chooseStorefrontAuthMode}
        onForgot={() => setAuthMode("forgot")}
      />
    ) : undefined;
    const paymentPanel = auth && wantsCourseCheckout() && selectedStorefrontOffer ? (
      storefrontCheckout?.client_secret && storefrontCheckout.publishable_key ? (
        <StripePaymentForm
          session={storefrontCheckout}
          returnURL={(() => {
            const url = new URL(window.location.href);
            url.searchParams.set("payment", selectedStorefrontOffer.kind === "recurring" ? "membership-success" : "success");
            url.searchParams.delete("intent");
            url.searchParams.delete("auth");
            return url.toString();
          })()}
          busy={busy}
          error={error}
          onError={setError}
        />
      ) : <CheckoutPreparing />
    ) : undefined;
    if (!auth || wantsCourseCheckout()) {
      return <PublicStorefront
        product={publicProduct}
        portal={portal}
        checkout={wantsCourseCheckout()}
        signedIn={Boolean(auth)}
        selectedOffer={selectedStorefrontOffer}
        accountPanel={accountPanel}
        paymentPanel={paymentPanel}
        onSelect={selectStorefrontOffer}
        onChoose={chooseStorefrontOffer}
        onSignIn={() => chooseStorefrontAuthMode("login")}
      />;
    }
    return <PublicStorefront
      product={publicProduct}
      portal={portal}
      checkout={false}
      signedIn
      selectedOffer={selectedStorefrontOffer}
      onSelect={selectStorefrontOffer}
      onChoose={chooseStorefrontOffer}
      onSignIn={() => undefined}
    />;
  }

  if (!auth && portal) {
    const brand = <>{portal.brand.logo_url ? <img className="brand-logo" src={portal.brand.logo_url} alt={portal.brand.name} /> : <div className="brand-mark"><BookOpen aria-hidden="true" /></div>}</>;
    if (authMode === "verify_pending") {
      return (
        <main className="auth-shell"><section className="auth-card" aria-labelledby="auth-title">
          {brand}<p className="eyebrow">{brandName}</p><h1 id="auth-title">Check your email</h1>
          <p className="muted">Open the verification link we sent to <strong>{authForm.email}</strong>. You’ll return here automatically.</p>
          {error && <div className="alert error" role="alert">{error}</div>}
          {notice && <div className="alert success" role="status">{notice}</div>}
          <button className="primary" disabled={busy} onClick={resendVerification}>Send another link</button>
          <button className="text-button" onClick={() => setAuthMode("login")}>Back to sign in</button>
        </section></main>
      );
    }
    if (authMode === "forgot") {
      return (
        <main className="auth-shell"><section className="auth-card" aria-labelledby="auth-title">
          {brand}<p className="eyebrow">{brandName}</p><h1 id="auth-title">Reset your password</h1>
          <p className="muted">Enter your email and we’ll send you a secure reset link.</p>
          {error && <div className="alert error" role="alert">{error}</div>}
          {notice && <div className="alert success" role="status">{notice}</div>}
          <form className="stack" onSubmit={requestPasswordReset}>
            <Field label="Email" id="email"><input id="email" type="email" value={authForm.email} onChange={(event) => setAuthForm({ ...authForm, email: event.target.value })} autoComplete="email" required /></Field>
            <button className="primary" disabled={busy}>{busy ? "Please wait…" : "Send reset link"}</button>
          </form>
          <button className="text-button" onClick={() => setAuthMode("login")}>Back to sign in</button>
        </section></main>
      );
    }
    if (authMode === "reset") {
      return (
        <main className="auth-shell"><section className="auth-card" aria-labelledby="auth-title">
          {brand}<p className="eyebrow">{brandName}</p><h1 id="auth-title">Choose a new password</h1>
          {error && <div className="alert error" role="alert">{error}</div>}
          <form className="stack" onSubmit={confirmPasswordReset}>
            <Field label="New password" id="password"><input id="password" type="password" value={authForm.password} onChange={(event) => setAuthForm({ ...authForm, password: event.target.value })} autoComplete="new-password" required /></Field>
            <Field label="Confirm password" id="password-confirm"><input id="password-confirm" type="password" value={authForm.password_confirm} onChange={(event) => setAuthForm({ ...authForm, password_confirm: event.target.value })} autoComplete="new-password" required /></Field>
            <button className="primary" disabled={busy}>{busy ? "Please wait…" : "Update password"}</button>
          </form>
        </section></main>
      );
    }
    return (
      <main className="auth-shell">
        <section className="auth-card" aria-labelledby="auth-title">
          {brand}
          <p className="eyebrow">{brandName}</p>
          <h1 id="auth-title">{authMode === "login" ? `Sign in to your ${brandName} courses` : `Create your ${brandName} account`}</h1>
          <p className="muted">{selectedStorefrontOffer ? "Sign in or create your account to continue securely to payment." : authMode === "login" ? "Continue learning where you left off." : "Create an account to access your courses and community."}</p>
          {selectedStorefrontOffer && publicProduct && <div className="order-summary"><span>{publicProduct.name}</span><strong>{formatPublicOffer(selectedStorefrontOffer)}</strong></div>}
          {error && <div className="alert error" role="alert">{error}</div>}
          {notice && <div className="alert success" role="status">{notice}</div>}
          <form className="stack" onSubmit={authenticate}>
            {authMode === "signup" && (
              <Field label="Your name (optional)" id="display-name">
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
          {authMode === "login" && <button className="text-button" onClick={() => setAuthMode("forgot")}>Forgot your password?</button>}
          {portal.signup.enabled && <button className="text-button" onClick={() => setAuthMode(authMode === "login" ? "signup" : "login")}>
            {authMode === "login" ? "New here? Create an account" : "Already have an account? Sign in"}
          </button>}
        </section>
      </main>
    );
  }

  if (!busy && communities.length === 0) {
    return (
      <main className="auth-shell">
      <section className="auth-card">
        {portal?.brand.logo_url ? <img className="brand-logo" src={portal.brand.logo_url} alt={brandName} /> : <UserRound size={34} aria-hidden="true" />}
        <h1>Access unavailable</h1>
        <p className="muted">We couldn’t activate your {brandName} access.</p>
        {error && <div className="alert error" role="alert">{error}</div>}
          {portal?.brand.support_email && <p className="muted">Contact <a href={`mailto:${portal.brand.support_email}`}>{portal.brand.support_email}</a> for help.</p>}
          <button className="secondary" onClick={logout}><LogOut size={16} /> Sign out</button>
        </section>
      </main>
    );
  }

  return (
    <div className="app-shell">
      <header className="mobile-header">
        <button className="icon-button" aria-label="Open navigation" onClick={() => setMenuOpen(true)}><Menu /></button>
        <strong>{brandName}</strong>
        <span className="avatar">{displayName(me).slice(0, 1).toUpperCase()}</span>
      </header>

      {menuOpen && <button className="nav-scrim" aria-label="Close navigation" onClick={() => setMenuOpen(false)} />}
      <aside className={`sidebar ${menuOpen ? "open" : ""}`}>
        <div className="brand">
          {portal?.brand.logo_url ? <img className="brand-logo sidebar-logo" src={portal.brand.logo_url} alt="" /> : <div className="brand-mark"><BookOpen aria-hidden="true" /></div>}
          <div><strong>{brandName}</strong><span>Courses & community</span></div>
          <button className="icon-button mobile-close" aria-label="Close navigation" onClick={() => setMenuOpen(false)}><X /></button>
        </div>
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
              {memberSubscription ? (
                <article className="membership-summary">
                  <span className={`status-pill ${memberSubscription.status}`}>{memberSubscription.status.replaceAll("_", " ")}</span>
                  <strong>{memberSubscription.plan?.name || "Course membership"}</strong>
                  {memberSubscription.plan && <span>{formatMembershipPrice(memberSubscription.plan)}</span>}
                  {memberSubscription.cancel_at ? (
                    <button className="secondary" onClick={resumeMembership} disabled={busy}>Resume renewal</button>
                  ) : memberSubscription.status !== "cancelled" && memberSubscription.status !== "ended" ? (
                    <button className="text-button" onClick={cancelMembership} disabled={busy}>Cancel at period end</button>
                  ) : null}
                </article>
              ) : membershipPlans.length > 0 ? (
                <div className="membership-plans">
                  <p className="eyebrow">Memberships</p>
                  {membershipPlans.map((plan) => (
                    <article key={plan.id}>
                      <strong>{plan.name}</strong>
                      <span>{formatMembershipPrice(plan)}</span>
                      <p>{plan.description || (plan.scope_type === "all_courses" ? "Access every course." : "Access included courses.")}</p>
                      <button className="primary" onClick={() => startMembership(plan.id)} disabled={busy}>
                        Join membership
                      </button>
                    </article>
                  ))}
                </div>
              ) : null}
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
                          : "Your course unlocks automatically after payment is confirmed."}
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

        {view === "profile" && <section className="profile-card"><span className="avatar xlarge">{displayName(me).slice(0, 1).toUpperCase()}</span><div><p className="eyebrow">Member profile</p><h2>{displayName(me)}</h2><p className="muted">@{me?.handle}</p><p>{me?.bio || "No bio yet."}</p><p className="muted">Signed in as {auth?.user.email}</p></div><button className="secondary" onClick={logout}><LogOut size={16} /> Sign out</button></section>}
      </main>
    </div>
  );
}

function storagePublicURL(fileID?: number): string {
  if (!fileID) return "";
  const project = currentProjectId();
  return `/api/apps/storage/public/files/${fileID}/content${project ? `?project_id=${encodeURIComponent(project)}` : ""}`;
}

function offerLabel(offer: PublicOffer): string {
  if (!offer.interval) return "One-time access";
  const count = offer.interval_count || 1;
  if (offer.interval === "month" && count === 1) return "Monthly";
  if (offer.interval === "year" && count === 1) return "Annual";
  return `Every ${count > 1 ? `${count} ` : ""}${offer.interval}${count > 1 ? "s" : ""}`;
}

function offerNote(offer: PublicOffer): string {
  if (!offer.interval) return "Pay once. No recurring charges.";
  const trial = (offer.trial_days || 0) > 0 ? `${offer.trial_days}-day trial · ` : "";
  return `${trial}Renews automatically until cancelled.`;
}

function annualSaving(product: PublicProduct, offer: PublicOffer): number {
  if (offer.interval !== "year" || (offer.interval_count || 1) !== 1) return 0;
  const monthly = product.offers.find((candidate) => candidate.currency === offer.currency && candidate.interval === "month" && (candidate.interval_count || 1) === 1);
  if (!monthly || monthly.unit_amount_cents <= 0) return 0;
  return Math.max(0, Math.round((1 - offer.unit_amount_cents / (monthly.unit_amount_cents * 12)) * 100));
}

function storefrontBenefits(product: PublicProduct): Array<{ title: string; description?: string }> {
  if (product.storefront?.included?.length) return product.storefront.included;
  const recurring = product.offers.some((offer) => offer.kind === "recurring");
  if (recurring && product.courses.length > 1) {
    return product.courses.slice(0, 4).map((course) => ({ title: course.name, description: "Included with your active membership." }));
  }
  if (recurring) {
    return [
      { title: "Complete learning access", description: "Open every course included in this membership." },
      { title: "New material as it is published", description: "Your access stays current while your membership is active." },
      { title: "Progress that stays with you", description: "Pick up each lesson where you left off." },
    ];
  }
  return [
    { title: "Complete course access", description: "Work through every published lesson at your own pace." },
    { title: "A clear learning path", description: "Follow the course from the first lesson through completion." },
    { title: "Progress tracking", description: "Return anytime and continue where you left off." },
  ];
}

function PublicStorefront({ product, portal, checkout, signedIn, selectedOffer, accountPanel, paymentPanel, onSelect, onChoose, onSignIn }: {
  product: PublicProduct;
  portal: PortalBootstrap;
  checkout: boolean;
  signedIn: boolean;
  selectedOffer?: PublicOffer;
  accountPanel?: ReactNode;
  paymentPanel?: ReactNode;
  onSelect: (offer: PublicOffer) => void;
  onChoose: (offer: PublicOffer) => void;
  onSignIn: () => void;
}) {
  const offers = product.offers;
  const activeOffer = selectedOffer || offers.find((offer) => offer.interval === "year") || offers[0];
  const benefits = storefrontBenefits(product);
  const imageURL = storagePublicURL(product.image_file_id);
  const headline = product.storefront?.headline || product.name;
  const eyebrow = product.storefront?.eyebrow || product.category || (offers.some((offer) => offer.kind === "recurring") ? "Membership" : "Online course");
  const stage = paymentPanel ? "payment" : accountPanel ? "account" : "offer";
  return (
    <main className="storefront-shell">
      <header className="storefront-header">
        <div className="storefront-brand">
          {portal.brand.logo_url ? <img src={portal.brand.logo_url} alt={portal.brand.name} /> : <div className="brand-mark"><BookOpen aria-hidden="true" /></div>}
          <strong>{portal.brand.name}</strong>
        </div>
        {!signedIn && <button className="storefront-signin" onClick={onSignIn}>Already a member? <strong>Sign in</strong></button>}
        {signedIn && <span className="storefront-secure"><LockKeyhole size={15} /> Secure checkout</span>}
      </header>
      <section className="storefront-frame">
        <div className="storefront-story">
          <div className="storefront-art" style={{ "--product-color": product.color || portal.brand.primary_color } as CSSProperties}>
            {imageURL && <img src={imageURL} alt="" onError={(event) => { event.currentTarget.hidden = true; }} />}
            <div className="storefront-art-copy"><span>{portal.brand.name}</span><strong>{product.name}</strong><BadgeCheck aria-hidden="true" /></div>
          </div>
          <div className="storefront-copy">
            <p className="storefront-eyebrow">{eyebrow}</p>
            <h1>{headline}</h1>
            {product.description && <p className="storefront-description">{product.description}</p>}
          </div>
          <section className="storefront-included" aria-labelledby="included-title">
            <h2 id="included-title">What’s included</h2>
            <div className="storefront-benefits">
              {benefits.map((benefit) => <article key={benefit.title}><CheckCircle2 aria-hidden="true" /><div><strong>{benefit.title}</strong>{benefit.description && <p>{benefit.description}</p>}</div></article>)}
            </div>
          </section>
          {product.storefront?.testimonial && <figure className="storefront-testimonial">
            <blockquote>“{product.storefront.testimonial.quote}”</blockquote>
            <figcaption>
              {product.storefront.testimonial.avatar_file_id && <img src={storagePublicURL(product.storefront.testimonial.avatar_file_id)} alt="" />}
              <span><strong>{product.storefront.testimonial.name}</strong>{product.storefront.testimonial.role && <small>{product.storefront.testimonial.role}</small>}</span>
            </figcaption>
          </figure>}
        </div>
        <aside className="storefront-checkout" aria-label="Checkout">
          <div className="checkout-heading">
            <p>{stage === "payment" ? "Payment details" : stage === "account" ? "Your account" : checkout ? "Complete your enrollment" : "Choose your access"}</p>
            {activeOffer && <strong>{formatPublicOffer(activeOffer)}</strong>}
          </div>
          {stage !== "payment" && <div className={`offer-options ${offers.length === 1 ? "single" : ""}`} role={offers.length > 1 ? "radiogroup" : undefined} aria-label="Purchase options">
            {offers.map((offer) => {
              const selected = activeOffer?.catalog_price_id === offer.catalog_price_id;
              const saving = annualSaving(product, offer);
              return <button type="button" role={offers.length > 1 ? "radio" : undefined} aria-checked={offers.length > 1 ? selected : undefined} className={`offer-option ${selected ? "selected" : ""}`} key={offer.catalog_price_id} onClick={() => onSelect(offer)}>
                {offers.length > 1 && <span className="offer-radio" aria-hidden="true" />}
                <span className="offer-copy"><strong>{offerLabel(offer)}</strong><small>{offerNote(offer)}</small></span>
                <span className="offer-price"><strong>{formatPublicOffer(offer)}</strong>{saving > 0 && <small>Save {saving}%</small>}</span>
              </button>;
            })}
            {offers.length === 0 && <div className="checkout-unavailable">This product is not currently available for purchase.</div>}
          </div>}
          {stage === "offer" && activeOffer && <button className="checkout-continue" onClick={() => onChoose(activeOffer)}>Continue <ArrowRight size={18} /></button>}
          {accountPanel}
          {paymentPanel}
          <div className="checkout-trust"><LockKeyhole size={15} /><span><strong>Secure checkout</strong><small>Payments are encrypted and processed by Stripe.</small></span></div>
          {activeOffer?.kind === "recurring" && <p className="checkout-terms">Your subscription renews automatically. You can cancel future renewals from your account.</p>}
        </aside>
      </section>
      <footer className="storefront-footer"><span>© {new Date().getFullYear()} {portal.brand.name}</span>{portal.brand.support_email && <a href={`mailto:${portal.brand.support_email}`}>Need help?</a>}</footer>
    </main>
  );
}

function CheckoutAccountForm({ mode, form, busy, error, notice, signupEnabled, brandName, onChange, onSubmit, onModeChange, onForgot }: {
  mode: "login" | "signup";
  form: ReturnType<typeof initialAuthForm>;
  busy: boolean;
  error: string;
  notice: string;
  signupEnabled: boolean;
  brandName: string;
  onChange: (form: ReturnType<typeof initialAuthForm>) => void;
  onSubmit: (event: FormEvent) => void;
  onModeChange: (mode: "login" | "signup") => void;
  onForgot: () => void;
}) {
  return <section className="checkout-account">
    <div className="checkout-section-title"><UserRound size={18} /><div><strong>{mode === "signup" ? `Create your ${brandName} account` : "Sign in to continue"}</strong><small>Your purchase will be connected to this account.</small></div></div>
    {error && <div className="alert error" role="alert">{error}</div>}
    {notice && <div className="alert success" role="status">{notice}</div>}
    <form className="checkout-form" onSubmit={onSubmit}>
      {mode === "signup" && <Field label="Name" id="checkout-name"><input id="checkout-name" value={form.display_name} onChange={(event) => onChange({ ...form, display_name: event.target.value })} autoComplete="name" placeholder="Your name" /></Field>}
      <Field label="Email address" id="checkout-email"><input id="checkout-email" type="email" value={form.email} onChange={(event) => onChange({ ...form, email: event.target.value })} autoComplete="email" placeholder="you@example.com" required /></Field>
      <Field label="Password" id="checkout-password"><input id="checkout-password" type="password" value={form.password} onChange={(event) => onChange({ ...form, password: event.target.value })} autoComplete={mode === "login" ? "current-password" : "new-password"} minLength={8} required /></Field>
      <button className="checkout-continue" disabled={busy}>{busy ? "Please wait…" : mode === "login" ? "Sign in and continue" : "Create account and continue"}<ArrowRight size={18} /></button>
    </form>
    {mode === "login" && <button className="checkout-link" onClick={onForgot}>Forgot your password?</button>}
    {signupEnabled && <button className="checkout-switch" onClick={() => onModeChange(mode === "login" ? "signup" : "login")}>{mode === "login" ? "New here? Create an account" : "Already have an account? Sign in"}</button>}
  </section>;
}

function CheckoutPreparing() {
  return <div className="checkout-preparing" role="status"><RefreshCcw className="spin" aria-hidden="true" /><div><strong>Preparing secure payment</strong><small>This usually takes only a moment.</small></div></div>;
}

function StripePaymentForm({ session, returnURL, busy, error, onError }: {
  session: StorefrontCheckoutSession;
  returnURL: string;
  busy: boolean;
  error: string;
  onError: (message: string) => void;
}) {
  const mountRef = useRef<HTMLDivElement>(null);
  const stripeRef = useRef<Stripe | null>(null);
  const elementsRef = useRef<StripeElements | null>(null);
  const [ready, setReady] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    let cancelled = false;
    let paymentElement: StripePaymentElement | undefined;
    void loadStripe(session.publishable_key || "").then((stripe) => {
      if (cancelled || !stripe || !mountRef.current || !session.client_secret) return;
      const elements = stripe.elements({
        clientSecret: session.client_secret,
        appearance: { theme: "stripe", variables: { colorPrimary: "#176b57", borderRadius: "10px", fontFamily: "Inter, ui-sans-serif, system-ui, sans-serif" } },
      });
      paymentElement = elements.create("payment", { layout: "tabs" });
      paymentElement.on("ready", () => setReady(true));
      paymentElement.mount(mountRef.current);
      stripeRef.current = stripe;
      elementsRef.current = elements;
    }).catch(() => onError("Secure payment could not be loaded. Please refresh and try again."));
    return () => {
      cancelled = true;
      paymentElement?.destroy();
    };
  }, [onError, session.client_secret, session.publishable_key]);

  async function submitPayment(event: FormEvent) {
    event.preventDefault();
    if (!stripeRef.current || !elementsRef.current) return;
    setSubmitting(true);
    onError("");
    const result = await stripeRef.current.confirmPayment({
      elements: elementsRef.current,
      confirmParams: { return_url: returnURL },
      redirect: "if_required",
    });
    if (result.error) {
      onError(result.error.message || "Payment could not be completed. Please check your details and try again.");
      setSubmitting(false);
      return;
    }
    window.location.assign(returnURL);
  }

  return <form className="stripe-payment" onSubmit={submitPayment}>
    <div className="checkout-section-title"><CreditCard size={18} /><div><strong>Card and billing details</strong><small>Processed securely by Stripe.</small></div></div>
    <div ref={mountRef} className="stripe-element" />
    {error && <div className="alert error" role="alert">{error}</div>}
    {!ready && <div className="stripe-loading"><RefreshCcw className="spin" size={16} /> Loading secure payment…</div>}
    <button className="checkout-pay" disabled={!ready || submitting || busy}>{submitting ? "Processing…" : "Complete secure payment"}<LockKeyhole size={17} /></button>
  </form>;
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
