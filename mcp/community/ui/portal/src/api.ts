import { AptevaClient, pickBaseURL } from "@apteva/web-sdk";
import type {
  Assignment,
  Community,
  CourseCertificate,
  CourseDetails,
  CourseOffer,
  CoursePurchase,
  DMThread,
  DMThreadView,
  DripSchedule,
  EnrollmentRule,
  Lesson,
  LessonComment,
  LessonResource,
  Member,
  MemberSubscription,
  MembershipPlan,
  Post,
  PublicProduct,
  Quiz,
  Section,
  Space,
  Thread,
} from "./types";

declare const __API_BASE__: string;
declare const __COMMUNITY_APP__: string;
declare const __AUTH_APP__: string;

export const COMMUNITY_APP = __COMMUNITY_APP__ || "community";
export const AUTH_APP = __AUTH_APP__ || "auth";

const portalFetch = ((input: RequestInfo | URL, init?: RequestInit) =>
  globalThis.fetch(input, { ...init, credentials: "omit" })) as typeof fetch;

export const apteva = new AptevaClient({
  baseURL: pickBaseURL(__API_BASE__),
  projectId: currentProjectId(),
  // The portal authenticates with a delegated member token. Do not let a
  // same-origin operator dashboard cookie shadow that explicit identity.
  fetch: portalFetch,
});
const community = apteva.app(COMMUNITY_APP);
const auth = apteva.app(AUTH_APP);

export interface AuthResponse {
  user: { id: number; email: string; display_name?: string };
  refresh_token?: string;
  apteva_access_token?: string;
  apteva_expires_in?: number;
  verification_required?: boolean;
  stored_at?: number;
}

export interface PortalBootstrap {
  community: { id: string; slug: string; name: string; description: string };
  brand: {
    name: string;
    logo_url?: string;
    favicon_url?: string;
    primary_color: string;
    accent_color: string;
    support_email?: string;
  };
  auth: { client_id: string; organization_slug?: string };
  signup: { enabled: boolean };
}

export interface MemberSession {
  communities: Community[];
  memberships: Member[];
}

export interface StorefrontCheckoutSession {
  offer_kind: "one_time" | "recurring";
  presentation?: "hosted" | "elements";
  checkout_url?: string;
  client_secret?: string;
  publishable_key?: string;
  enrolled?: boolean;
  access_active?: boolean;
  purchase?: CoursePurchase;
  subscription?: MemberSubscription;
}

export interface LessonBundle {
  lesson: Lesson;
  resources: LessonResource[];
  quizzes: Quiz[];
  assignments: Assignment[];
  comments: LessonComment[];
}

export function useDelegatedToken(token?: string): void {
  apteva.setApiKey(token);
}

export function currentProjectId(): string {
  if (typeof window === "undefined") return "";
  const fromQuery = new URLSearchParams(window.location.search).get("project_id");
  return fromQuery || window.__APTEVA_APP__?.default_project || "";
}

function projectScopedPath(path: string): string {
  const projectId = currentProjectId();
  if (!projectId) return path;
  return `${path}${path.includes("?") ? "&" : "?"}project_id=${encodeURIComponent(projectId)}`;
}

function communityPublicPath(path: string): string {
  if (typeof window === "undefined") return `/api/apps/${encodeURIComponent(COMMUNITY_APP)}${path}`;
  const escaped = COMMUNITY_APP.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = window.location.pathname.match(new RegExp(`/api/apps/${escaped}/_install/(\\d+)(?:/|$)`));
  const selector = match?.[1] ? `/_install/${encodeURIComponent(match[1])}` : "";
  return `/api/apps/${encodeURIComponent(COMMUNITY_APP)}${selector}${path}`;
}

const self = "self";

export const api = {
  portal: {
    bootstrap: (community: string) =>
      apteva.get<PortalBootstrap>(projectScopedPath(`${communityPublicPath("/portal/bootstrap")}?community=${encodeURIComponent(community)}`)),
    products: (community: string) =>
      apteva.get<{ products: PublicProduct[]; count: number }>(projectScopedPath(`${communityPublicPath("/portal/products")}?community=${encodeURIComponent(community)}`)),
    product: (community: string, slug: string) =>
      apteva.get<{ product: PublicProduct }>(projectScopedPath(`${communityPublicPath(`/portal/products/${encodeURIComponent(slug)}`)}?community=${encodeURIComponent(community)}`)),
  },
  auth: {
    login: (body: { client_id: string; email: string; password: string; organization_slug?: string }) =>
      auth.post<AuthResponse>(projectScopedPath("/login"), { ...body, project_id: currentProjectId() }),
    signup: (body: { client_id: string; email: string; password: string; display_name: string; organization_slug?: string; continue_url?: string }) =>
      auth.post<AuthResponse>(projectScopedPath("/signup"), { ...body, project_id: currentProjectId() }),
    requestPasswordReset: (body: { client_id: string; email: string; organization_slug?: string; continue_url?: string }) =>
      auth.post<{ ok: boolean }>(projectScopedPath("/password/reset/request"), { ...body, project_id: currentProjectId() }),
    confirmPasswordReset: (body: { client_id: string; token: string; password: string; organization_slug?: string }) =>
      auth.post<AuthResponse>(projectScopedPath("/password/reset/confirm"), { ...body, project_id: currentProjectId() }),
    verifyEmail: (body: { client_id: string; token: string; organization_slug?: string }) =>
      auth.post<AuthResponse>(projectScopedPath("/email/verify"), { ...body, project_id: currentProjectId() }),
    resendVerification: (body: { client_id: string; email: string; organization_slug?: string; continue_url?: string }) =>
      auth.post<{ ok: boolean }>(projectScopedPath("/email/verification/resend"), { ...body, project_id: currentProjectId() }),
    refresh: (body: { client_id: string; refresh_token: string; organization_slug?: string }) =>
      auth.post<AuthResponse>(projectScopedPath("/refresh"), { ...body, project_id: currentProjectId() }),
    logout: (refresh_token?: string) =>
      auth.post<void>(projectScopedPath("/logout"), { refresh_token, project_id: currentProjectId() }),
  },
  session: () => community.tool<MemberSession>("members_me"),
  communities: {
    list: () => community.tool<MemberSession>("communities_list"),
  },
  members: {
    ensure: (community_id: string, display_name?: string) =>
      community.tool<{ member: Member; created: boolean }>("members_ensure", { community_id, display_name }),
    list: (community_id: string) =>
      community.tool<{ members: Member[] }>("members_list", { community_id, status: "active", limit: 500 }),
    me: (community_id: string) =>
      community.tool<{ member: Member }>("members_me", { community_id }),
    update: (community_id: string, body: { display_name?: string; bio?: string }) =>
      community.tool<Member>("members_update", { community_id, id: self, ...body }),
  },
  spaces: {
    list: (community_id: string) =>
      community.tool<{ spaces: Space[] }>("spaces_list", { community_id }),
  },
  threads: {
    list: (space_id: string) =>
      community.tool<{ threads: Thread[] }>("threads_list", { space_id, limit: 100 }),
    create: (space_id: string, title: string, body: string) =>
      community.tool<{ thread: Thread; first_post?: Post }>("threads_create", {
        space_id,
        author_id: self,
        title,
        body,
      }),
  },
  posts: {
    list: (thread_id: string) =>
      community.tool<{ posts: Post[] }>("posts_list", { thread_id, limit: 300 }),
    create: (thread_id: string, body: string, reply_to_id?: string) =>
      community.tool<Post>("posts_create", { thread_id, author_id: self, body, reply_to_id }),
    react: (post_id: string, emoji: string) =>
      community.tool<{ action: string }>("posts_react", { post_id, member_id: self, emoji }),
    edit: (id: string, body: string) =>
      community.tool<Post>("posts_edit", { id, caller_member_id: self, body }),
    remove: (id: string) =>
      community.tool<{ ok: boolean }>("posts_remove", { id, caller_member_id: self }),
  },
  courses: {
    details: (space_id: string) =>
      community.tool<{ details: CourseDetails; enrollment_rules: EnrollmentRule; certificate: CourseCertificate }>(
        "courses_get_details",
        { space_id },
      ),
    sections: (space_id: string) =>
      community.tool<{ sections: Section[] }>("sections_list", { space_id }),
    lessons: (space_id: string) =>
      community.tool<{ lessons: Lesson[] }>("lessons_list", {
        space_id,
        member_id: self,
        include_drafts: false,
      }),
    bundle: (id: string) =>
      community.tool<LessonBundle>("lesson_bundle_get", { id, member_id: self }),
    enroll: (space_id: string) =>
      community.tool("course_enroll", { space_id, member_id: self }),
    offer: (space_id: string) =>
      community.tool<{ offer: CourseOffer | null }>("course_offer_get", { space_id }),
    purchase: (space_id: string) =>
      community.tool<{ purchase: CoursePurchase | null }>("course_purchase_status", {
        space_id,
        member_id: self,
      }),
    purchaseStart: (space_id: string, success_url: string, cancel_url: string) =>
      community.tool<{ purchase: CoursePurchase | null; checkout_url?: string; enrolled?: boolean }>(
        "course_purchase_start",
        { space_id, member_id: self, success_url, cancel_url },
      ),
    purchaseCancel: (space_id: string) =>
      community.tool<{ purchase: CoursePurchase }>("course_purchase_cancel", {
        space_id,
        member_id: self,
      }),
    progress: (space_id: string) =>
      community.tool("lessons_progress", { space_id, member_id: self }),
    mark: (lesson_id: string, status: "in_progress" | "complete", last_position_seconds?: number) =>
      community.tool("lessons_mark_complete", { lesson_id, member_id: self, status, last_position_seconds }),
    comment: (lesson_id: string, body: string) =>
      community.tool<LessonComment>("lesson_comments_post", { lesson_id, member_id: self, body }),
  },
  memberships: {
    plans: (community_id: string) =>
      community.tool<{ plans: MembershipPlan[] }>("membership_plans_list", { community_id }),
    status: (community_id: string) =>
      community.tool<{ subscription: MemberSubscription | null; has_membership: boolean; access_active?: boolean }>(
        "membership_status",
        { community_id, member_id: self },
      ),
    checkout: (plan_id: string, success_url: string, cancel_url: string) =>
      community.tool<{ subscription: MemberSubscription; checkout_url?: string; access_active?: boolean }>(
        "membership_checkout_start",
        { plan_id, member_id: self, success_url, cancel_url },
      ),
    cancel: (id: string, at_period_end = true) =>
      community.tool<{ subscription: MemberSubscription }>("membership_cancel", {
        id, member_id: self, at_period_end,
      }),
    resume: (id: string) =>
      community.tool<{ subscription: MemberSubscription }>("membership_resume", { id, member_id: self }),
  },
  storefront: {
    checkout: (community_id: string, catalog_price_id: number, success_url: string, cancel_url: string, return_url: string) =>
      community.tool<StorefrontCheckoutSession>("storefront_checkout_start", {
        community_id, catalog_price_id, member_id: self, success_url, cancel_url,
        return_url, presentation: "elements",
      }),
  },
  dms: {
    list: (community_id: string) =>
      community.tool<{ threads: DMThread[] }>("dms_list_threads", {
        community_id,
        member_id: self,
        limit: 100,
      }),
    get: (id: string) =>
      community.tool<DMThreadView>("dms_get_thread", { id, caller_member_id: self, limit: 300 }),
    open: (community_id: string, participant: string) =>
      community.tool<DMThread>("dms_open", { community_id, participants: [self, participant] }),
    send: (dm_thread_id: string, body: string) =>
      community.tool("dms_send", { dm_thread_id, author_id: self, body }),
    markRead: (dm_thread_id: string) =>
      community.tool("dms_mark_read", { dm_thread_id, member_id: self }),
  },
};
