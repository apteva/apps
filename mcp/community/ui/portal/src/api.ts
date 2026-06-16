import { AptevaClient, pickBaseURL, pickKioskKey } from "@apteva/web-sdk";
import type {
  Assignment,
  Community,
  CourseAnalytics,
  CourseCertificate,
  CourseDetails,
  CourseEnrollment,
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

export const apteva = new AptevaClient({
  baseURL: pickBaseURL(__API_BASE__),
  apiKey: pickKioskKey(),
});

const community = apteva.app(COMMUNITY_APP);

export function currentProjectId(): string {
  if (typeof window === "undefined") return "";
  const params = new URLSearchParams(window.location.search);
  const fromQuery = params.get("project_id");
  if (fromQuery) return fromQuery;
  const injected = window.__APTEVA_APP__?.default_project;
  if (injected) return injected;
  return window.localStorage.getItem("apteva.community.project_id") || "";
}

export function setCurrentProjectId(projectId: string): void {
  if (typeof window === "undefined") return;
  if (projectId) window.localStorage.setItem("apteva.community.project_id", projectId);
  else window.localStorage.removeItem("apteva.community.project_id");
}

function withQuery(path: string, params: Record<string, string | number | boolean | undefined> = {}): string {
  const url = new URL(path, "http://community.local");
  const projectId = currentProjectId();
  if (projectId) url.searchParams.set("project_id", projectId);
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") url.searchParams.set(key, String(value));
  }
  return url.pathname + url.search;
}

export const api = {
  communities: {
    list: () => community.get<{ communities: Community[] }>(withQuery("/communities")),
    create: (body: { slug: string; name: string; description?: string }) =>
      community.tool<Community>("communities_create", body),
  },
  members: {
    list: (communityId: string) =>
      community.get<{ members: Member[] }>(withQuery("/members", { community_id: communityId, status: "active" })),
    create: (body: { community_id: string; handle: string; display_name?: string; bio?: string }) =>
      community.tool<Member>("members_create", body),
  },
  spaces: {
    list: (communityId: string) =>
      community.get<{ spaces: Space[] }>(withQuery("/spaces", { community_id: communityId })),
    create: (body: { community_id: string; slug: string; name: string; kind: string; visibility: string }) =>
      community.tool<Space>("spaces_create", body),
    createCourse: (body: { community_id: string; slug: string; name: string; visibility: string }) =>
      community.tool<Space>("courses_create", body),
  },
  threads: {
    list: (spaceId: string) => community.get<{ threads: Thread[] }>(withQuery("/threads", { space_id: spaceId })),
    create: (body: { space_id: string; author_id: string; title?: string; body?: string }) =>
      community.tool<{ thread: Thread; first_post?: Post }>("threads_create", body),
  },
  posts: {
    list: (threadId: string) => community.get<{ posts: Post[] }>(withQuery("/posts", { thread_id: threadId })),
    create: (body: { thread_id: string; author_id: string; body: string; reply_to_id?: string }) =>
      community.tool<Post>("posts_create", body),
  },
  courses: {
    sections: (spaceId: string) => community.get<{ sections: Section[] }>(withQuery("/sections", { space_id: spaceId })),
    createSection: (body: { space_id: string; title: string; position?: number }) =>
      community.tool<Section>("sections_create", body),
    updateSection: (body: { id: string; title?: string; position?: number }) =>
      community.tool<Section>("sections_update", body),
    deleteSection: (id: string) => community.tool<{ ok: boolean }>("sections_delete", { id }),
    reorderSections: (space_id: string, order: string[]) => community.tool("sections_reorder", { space_id, order }),
    lessons: (spaceId: string, memberId?: string, includeDrafts = true) =>
      community.get<{ lessons: Lesson[] }>(
        withQuery("/lessons", { space_id: spaceId, member_id: memberId, include_drafts: includeDrafts }),
      ),
    lesson: (id: string, memberId?: string) =>
      community.get<Lesson>(withQuery("/lesson", { id, member_id: memberId })),
    createLesson: (body: { section_id: string; title: string; body?: string; position?: number }) =>
      community.tool<Lesson>("lessons_create", body),
    updateLesson: (body: { id: string; title?: string; body?: string }) =>
      community.tool<Lesson>("lessons_update", body),
    deleteLesson: (id: string) => community.tool<{ ok: boolean }>("lessons_delete", { id }),
    reorderLessons: (section_id: string, order: string[]) => community.tool("lessons_reorder", { section_id, order }),
    publishLesson: (id: string, published: boolean) =>
      community.tool<Lesson>("lessons_publish", { id, published }),
    attachVideo: (id: string, storage_key: string, duration_seconds?: number) =>
      community.tool<Lesson>("lessons_attach_video", { id, storage_key, duration_seconds }),
    markLesson: (lesson_id: string, member_id: string, status: "in_progress" | "complete") =>
      community.tool("lessons_mark_complete", { lesson_id, member_id, status }),
    details: (space_id: string) =>
      community.tool<{ details: CourseDetails; enrollment_rules: EnrollmentRule; certificate: CourseCertificate }>(
        "courses_get_details",
        { space_id },
      ),
    updateDetails: (body: Partial<CourseDetails> & { space_id: string }) =>
      community.tool<CourseDetails>("courses_update_details", body),
    resources: (lesson_id: string) =>
      community.tool<{ resources: LessonResource[] }>("lesson_resources_list", { lesson_id }),
    addResource: (body: { lesson_id: string; storage_file_id: string; name?: string; kind?: string; content_type?: string; size_bytes?: number }) =>
      community.tool<LessonResource>("lesson_resources_add", body),
    deleteResource: (id: string) => community.tool<{ ok: boolean }>("lesson_resources_delete", { id }),
    quizzes: (lesson_id: string) => community.tool<{ quizzes: Quiz[] }>("quizzes_list", { lesson_id }),
    createQuiz: (body: { lesson_id: string; title: string; questions?: unknown[]; passing_score?: number; position?: number }) =>
      community.tool<Quiz>("quizzes_create", body),
    updateQuiz: (body: { id: string; title?: string; questions?: unknown[]; passing_score?: number; position?: number }) =>
      community.tool<Quiz>("quizzes_update", body),
    deleteQuiz: (id: string) => community.tool<{ ok: boolean }>("quizzes_delete", { id }),
    assignments: (lesson_id: string) => community.tool<{ assignments: Assignment[] }>("assignments_list", { lesson_id }),
    createAssignment: (body: { lesson_id: string; title: string; instructions?: string; due_after_days?: number; attachment_storage_file_id?: string }) =>
      community.tool<Assignment>("assignments_create", body),
    updateAssignment: (body: { id: string; title?: string; instructions?: string; due_after_days?: number; attachment_storage_file_id?: string }) =>
      community.tool<Assignment>("assignments_update", body),
    deleteAssignment: (id: string) => community.tool<{ ok: boolean }>("assignments_delete", { id }),
    comments: (lesson_id: string) => community.tool<{ comments: LessonComment[] }>("lesson_comments_list", { lesson_id }),
    postComment: (body: { lesson_id: string; member_id: string; body: string }) =>
      community.tool<LessonComment>("lesson_comments_post", body),
    configureCertificate: (body: Partial<CourseCertificate> & { space_id: string }) =>
      community.tool<CourseCertificate>("certificates_configure", body),
    setDrip: (body: { lesson_id: string; release_at?: string; release_after_days?: number }) =>
      community.tool<DripSchedule>("drip_schedule_set", body),
    drips: (space_id: string) => community.tool<{ schedules: DripSchedule[] }>("drip_schedule_list", { space_id }),
    setEnrollmentRules: (body: Partial<EnrollmentRule> & { space_id: string }) =>
      community.tool<EnrollmentRule>("enrollment_rules_set", body),
    enroll: (body: { space_id: string; member_id: string; status?: "pending" | "active" }) =>
      community.tool<CourseEnrollment>("course_enroll", body),
    enrollments: (space_id: string, status?: string) =>
      community.tool<{ enrollments: CourseEnrollment[] }>("course_enrollments_list", { space_id, status }),
    analytics: (space_id: string) => community.tool<CourseAnalytics>("course_analytics", { space_id }),
  },
  auth: {
    login: (body: { client_id: string; email: string; password: string; organization_slug?: string }) =>
      apteva.app(AUTH_APP).post("/login", body),
    signup: (body: { client_id: string; email: string; password: string; display_name?: string; organization_slug?: string }) =>
      apteva.app(AUTH_APP).post("/signup", body),
    me: () => apteva.app(AUTH_APP).get("/me"),
  },
};
