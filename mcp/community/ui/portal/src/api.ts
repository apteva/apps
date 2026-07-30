import { AptevaClient, pickBaseURL } from "@apteva/web-sdk";
import type {
  Assignment,
  Community,
  CourseCertificate,
  CourseDetails,
  DMThread,
  DMThreadView,
  DripSchedule,
  EnrollmentRule,
  Lesson,
  LessonComment,
  LessonResource,
  Member,
  Post,
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

export const apteva = new AptevaClient({ baseURL: pickBaseURL(__API_BASE__) });
const community = apteva.app(COMMUNITY_APP);
const auth = apteva.app(AUTH_APP);

export interface AuthResponse {
  user: { id: number; email: string; display_name?: string };
  apteva_access_token?: string;
  apteva_expires_in?: number;
  verification_required?: boolean;
}

export interface MemberSession {
  communities: Community[];
  memberships: Member[];
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

const self = "self";

export const api = {
  auth: {
    login: (body: { client_id: string; email: string; password: string; organization_slug?: string }) =>
      auth.post<AuthResponse>("/login", { ...body, project_id: currentProjectId() }),
    signup: (body: { client_id: string; email: string; password: string; display_name: string; organization_slug?: string }) =>
      auth.post<AuthResponse>("/signup", { ...body, project_id: currentProjectId() }),
  },
  session: () => community.tool<MemberSession>("members_me"),
  communities: {
    list: () => community.tool<MemberSession>("communities_list"),
  },
  members: {
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
    progress: (space_id: string) =>
      community.tool("lessons_progress", { space_id, member_id: self }),
    mark: (lesson_id: string, status: "in_progress" | "complete", last_position_seconds?: number) =>
      community.tool("lessons_mark_complete", { lesson_id, member_id: self, status, last_position_seconds }),
    comment: (lesson_id: string, body: string) =>
      community.tool<LessonComment>("lesson_comments_post", { lesson_id, member_id: self, body }),
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
