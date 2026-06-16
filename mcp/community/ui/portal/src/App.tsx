import {
  ArrowDown,
  ArrowUp,
  BarChart3,
  BookOpen,
  CheckCircle2,
  Globe2,
  Hash,
  LayoutDashboard,
  Lock,
  MessageSquare,
  Paperclip,
  Plus,
  RefreshCcw,
  Save,
  Send,
  Trash2,
  UploadCloud,
  UserRound,
  Users,
} from "lucide-react";
import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { api, apteva, COMMUNITY_APP, currentProjectId, setCurrentProjectId } from "./api";
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

type View = "home" | "spaces" | "courses" | "members" | "auth";

const emptyCourseDetails: CourseDetails = {
  space_id: "",
  summary: "",
  description: "",
  instructor_name: "",
  level: "",
  tags: [],
  price_cents: 0,
  currency: "USD",
  prerequisites: [],
  outcomes: [],
};

const emptyEnrollmentRule: EnrollmentRule = {
  space_id: "",
  access_mode: "free",
  requires_approval: false,
};

const emptyCertificate: CourseCertificate = {
  space_id: "",
  enabled: false,
  title: "",
  body: "",
  issue_on_completion: true,
};

const views: Array<{ id: View; label: string; icon: typeof LayoutDashboard }> = [
  { id: "home", label: "Home", icon: LayoutDashboard },
  { id: "spaces", label: "Spaces", icon: Hash },
  { id: "courses", label: "Courses", icon: BookOpen },
  { id: "members", label: "Members", icon: Users },
  { id: "auth", label: "Auth", icon: Lock },
];

function routeFromHash(): View {
  const raw = window.location.hash.replace(/^#\/?/, "");
  if (raw === "spaces" || raw === "courses" || raw === "members" || raw === "auth") return raw;
  return "home";
}

function slugify(value: string): string {
  return value
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 62);
}

function handleFromName(value: string): string {
  return value
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9_]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .slice(0, 30);
}

function listFromText(value: string): string[] {
  return value
    .split(/\n|,/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function textFromList(value?: string[]): string {
  return (value || []).join("\n");
}

function parseQuestions(value: string): unknown[] {
  if (!value.trim()) return [];
  const parsed = JSON.parse(value);
  return Array.isArray(parsed) ? parsed : [parsed];
}

function maybeNumber(value: string): number | undefined {
  if (!value.trim()) return undefined;
  const n = Number(value);
  return Number.isFinite(n) ? n : undefined;
}

export function App() {
  const [view, setView] = useState<View>(() => routeFromHash());
  const [projectId, setProjectId] = useState(() => currentProjectId());
  const [communities, setCommunities] = useState<Community[]>([]);
  const [members, setMembers] = useState<Member[]>([]);
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [threads, setThreads] = useState<Thread[]>([]);
  const [posts, setPosts] = useState<Post[]>([]);
  const [sections, setSections] = useState<Section[]>([]);
  const [lessons, setLessons] = useState<Lesson[]>([]);
  const [courseDetails, setCourseDetails] = useState<CourseDetails>(emptyCourseDetails);
  const [enrollmentRule, setEnrollmentRule] = useState<EnrollmentRule>(emptyEnrollmentRule);
  const [certificate, setCertificate] = useState<CourseCertificate>(emptyCertificate);
  const [drips, setDrips] = useState<DripSchedule[]>([]);
  const [enrollments, setEnrollments] = useState<CourseEnrollment[]>([]);
  const [analytics, setAnalytics] = useState<CourseAnalytics>();
  const [resources, setResources] = useState<LessonResource[]>([]);
  const [quizzes, setQuizzes] = useState<Quiz[]>([]);
  const [assignments, setAssignments] = useState<Assignment[]>([]);
  const [comments, setComments] = useState<LessonComment[]>([]);
  const [communityId, setCommunityId] = useState("");
  const [memberId, setMemberId] = useState("");
  const [spaceId, setSpaceId] = useState("");
  const [threadId, setThreadId] = useState("");
  const [courseId, setCourseId] = useState("");
  const [lessonId, setLessonId] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const [communityForm, setCommunityForm] = useState({ name: "", slug: "", description: "" });
  const [memberForm, setMemberForm] = useState({ display_name: "", handle: "", bio: "" });
  const [spaceForm, setSpaceForm] = useState({ name: "", slug: "", kind: "forum", visibility: "members" });
  const [threadForm, setThreadForm] = useState({ title: "", body: "" });
  const [replyBody, setReplyBody] = useState("");
  const [courseForm, setCourseForm] = useState({ name: "", slug: "", visibility: "members" });
  const [detailsForm, setDetailsForm] = useState({
    summary: "",
    description: "",
    instructor_member_id: "",
    instructor_name: "",
    level: "",
    tags: "",
    price_cents: "0",
    currency: "USD",
    prerequisites: "",
    outcomes: "",
    cover_storage_file_id: "",
  });
  const [sectionTitle, setSectionTitle] = useState("");
  const [sectionEdit, setSectionEdit] = useState<Record<string, string>>({});
  const [lessonForm, setLessonForm] = useState({ section_id: "", title: "", body: "" });
  const [lessonEdit, setLessonEdit] = useState({ title: "", body: "" });
  const [videoForm, setVideoForm] = useState({ storage_key: "", duration_seconds: "" });
  const [resourceForm, setResourceForm] = useState({ storage_file_id: "", name: "", kind: "file", content_type: "", size_bytes: "" });
  const [quizForm, setQuizForm] = useState({ title: "", questions: "", passing_score: "70" });
  const [assignmentForm, setAssignmentForm] = useState({ title: "", instructions: "", due_after_days: "", attachment_storage_file_id: "" });
  const [certificateForm, setCertificateForm] = useState({ enabled: false, title: "", body: "", template_storage_file_id: "", issue_on_completion: true });
  const [dripForm, setDripForm] = useState({ release_at: "", release_after_days: "" });
  const [enrollmentForm, setEnrollmentForm] = useState({ access_mode: "free", requires_approval: false, max_enrollments: "", starts_at: "", ends_at: "" });
  const [commentBody, setCommentBody] = useState("");
  const [authForm, setAuthForm] = useState({ client_id: "", email: "", password: "", organization_slug: "" });
  const [authResult, setAuthResult] = useState("");

  const selectedCommunity = communities.find((c) => c.id === communityId);
  const selectedMember = members.find((m) => m.id === memberId);
  const selectedSpace = spaces.find((s) => s.id === spaceId);
  const selectedThread = threads.find((t) => t.id === threadId);
  const courses = spaces.filter((s) => s.kind === "course");
  const discussionSpaces = spaces.filter((s) => s.kind !== "course");
  const selectedCourse = spaces.find((s) => s.id === courseId);
  const selectedLesson = lessons.find((l) => l.id === lessonId);

  const lessonBySection = useMemo(() => {
    const map = new Map<string, Lesson[]>();
    for (const lesson of lessons) {
      const list = map.get(lesson.section_id) || [];
      list.push(lesson);
      map.set(lesson.section_id, list);
    }
    return map;
  }, [lessons]);

  useEffect(() => {
    const onHash = () => setView(routeFromHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  const run = useCallback(async (fn: () => Promise<void>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }, []);

  const loadCommunities = useCallback(async () => {
    const out = await api.communities.list();
    setCommunities(out.communities || []);
    if (!communityId && out.communities?.[0]) setCommunityId(out.communities[0].id);
  }, [communityId]);

  const loadCommunityData = useCallback(async () => {
    if (!communityId) return;
    const [memberOut, spaceOut] = await Promise.all([api.members.list(communityId), api.spaces.list(communityId)]);
    setMembers(memberOut.members || []);
    setSpaces(spaceOut.spaces || []);
    const storedMember = window.localStorage.getItem(`apteva.community.${communityId}.member_id`) || "";
    const nextMember = memberOut.members.find((m) => m.id === storedMember)?.id || memberOut.members[0]?.id || "";
    setMemberId((cur) => memberOut.members.find((m) => m.id === cur)?.id || nextMember);
    const nextSpace = spaceOut.spaces.find((s) => s.id === spaceId)?.id || spaceOut.spaces.find((s) => s.kind !== "course")?.id || "";
    setSpaceId(nextSpace);
    const nextCourse = spaceOut.spaces.find((s) => s.id === courseId)?.id || spaceOut.spaces.find((s) => s.kind === "course")?.id || "";
    setCourseId(nextCourse);
  }, [communityId, courseId, spaceId]);

  const loadThreads = useCallback(async () => {
    if (!spaceId) {
      setThreads([]);
      setPosts([]);
      return;
    }
    const out = await api.threads.list(spaceId);
    setThreads(out.threads || []);
    setThreadId((cur) => out.threads.find((t) => t.id === cur)?.id || out.threads[0]?.id || "");
  }, [spaceId]);

  const loadPosts = useCallback(async () => {
    if (!threadId) {
      setPosts([]);
      return;
    }
    const out = await api.posts.list(threadId);
    setPosts(out.posts || []);
  }, [threadId]);

  const loadCourse = useCallback(async () => {
    if (!courseId) {
      setSections([]);
      setLessons([]);
      setCourseDetails(emptyCourseDetails);
      setEnrollmentRule(emptyEnrollmentRule);
      setCertificate(emptyCertificate);
      setDrips([]);
      setEnrollments([]);
      setAnalytics(undefined);
      return;
    }
    const [sectionOut, lessonOut, detailsOut, dripOut, enrollmentOut, analyticsOut] = await Promise.all([
      api.courses.sections(courseId),
      api.courses.lessons(courseId, memberId || undefined, true),
      api.courses.details(courseId),
      api.courses.drips(courseId),
      api.courses.enrollments(courseId),
      api.courses.analytics(courseId),
    ]);
    setSections(sectionOut.sections || []);
    setLessons(lessonOut.lessons || []);
    setCourseDetails(detailsOut.details || { ...emptyCourseDetails, space_id: courseId });
    setEnrollmentRule(detailsOut.enrollment_rules || { ...emptyEnrollmentRule, space_id: courseId });
    setCertificate(detailsOut.certificate || { ...emptyCertificate, space_id: courseId });
    setDrips(dripOut.schedules || []);
    setEnrollments(enrollmentOut.enrollments || []);
    setAnalytics(analyticsOut);
    setDetailsForm({
      summary: detailsOut.details?.summary || "",
      description: detailsOut.details?.description || "",
      instructor_member_id: detailsOut.details?.instructor_member_id || "",
      instructor_name: detailsOut.details?.instructor_name || "",
      level: detailsOut.details?.level || "",
      tags: textFromList(detailsOut.details?.tags),
      price_cents: String(detailsOut.details?.price_cents ?? 0),
      currency: detailsOut.details?.currency || "USD",
      prerequisites: textFromList(detailsOut.details?.prerequisites),
      outcomes: textFromList(detailsOut.details?.outcomes),
      cover_storage_file_id: detailsOut.details?.cover_storage_file_id || "",
    });
    setCertificateForm({
      enabled: Boolean(detailsOut.certificate?.enabled),
      title: detailsOut.certificate?.title || "",
      body: detailsOut.certificate?.body || "",
      template_storage_file_id: detailsOut.certificate?.template_storage_file_id || "",
      issue_on_completion: detailsOut.certificate?.issue_on_completion ?? true,
    });
    setEnrollmentForm({
      access_mode: detailsOut.enrollment_rules?.access_mode || "free",
      requires_approval: Boolean(detailsOut.enrollment_rules?.requires_approval),
      max_enrollments: detailsOut.enrollment_rules?.max_enrollments ? String(detailsOut.enrollment_rules.max_enrollments) : "",
      starts_at: detailsOut.enrollment_rules?.starts_at || "",
      ends_at: detailsOut.enrollment_rules?.ends_at || "",
    });
    setLessonId((cur) => lessonOut.lessons.find((l) => l.id === cur)?.id || lessonOut.lessons[0]?.id || "");
  }, [courseId, memberId]);

  const loadLessonExtras = useCallback(async () => {
    if (!lessonId) {
      setResources([]);
      setQuizzes([]);
      setAssignments([]);
      setComments([]);
      setLessonEdit({ title: "", body: "" });
      setVideoForm({ storage_key: "", duration_seconds: "" });
      return;
    }
    const lesson = lessons.find((l) => l.id === lessonId);
    setLessonEdit({ title: lesson?.title || "", body: lesson?.body || "" });
    setVideoForm({
      storage_key: lesson?.video_storage_key || "",
      duration_seconds: lesson?.video_duration_seconds ? String(lesson.video_duration_seconds) : "",
    });
    const [resourceOut, quizOut, assignmentOut, commentOut] = await Promise.all([
      api.courses.resources(lessonId),
      api.courses.quizzes(lessonId),
      api.courses.assignments(lessonId),
      api.courses.comments(lessonId),
    ]);
    setResources(resourceOut.resources || []);
    setQuizzes(quizOut.quizzes || []);
    setAssignments(assignmentOut.assignments || []);
    setComments(commentOut.comments || []);
  }, [lessonId, lessons]);

  useEffect(() => {
    void run(loadCommunities);
  }, [loadCommunities, run]);

  useEffect(() => {
    void run(loadCommunityData);
  }, [loadCommunityData, run]);

  useEffect(() => {
    void run(loadThreads);
  }, [loadThreads, run]);

  useEffect(() => {
    void run(loadPosts);
  }, [loadPosts, run]);

  useEffect(() => {
    void run(loadCourse);
  }, [loadCourse, run]);

  useEffect(() => {
    void run(loadLessonExtras);
  }, [loadLessonExtras, run]);

  useEffect(() => {
    if (memberId && communityId) {
      window.localStorage.setItem(`apteva.community.${communityId}.member_id`, memberId);
    }
  }, [communityId, memberId]);

  useEffect(() => {
    if (!projectId) return;
    try {
      const sub = apteva.subscribe(
        `/api/app-events/${encodeURIComponent(COMMUNITY_APP)}`,
        { project_id: projectId },
        () => {
          void Promise.allSettled([loadCommunities(), loadCommunityData(), loadThreads(), loadPosts(), loadCourse()]);
        },
      );
      return () => sub.close();
    } catch {
      return undefined;
    }
  }, [loadCommunities, loadCommunityData, loadCourse, loadPosts, loadThreads, projectId]);

  function navigate(next: View) {
    window.location.hash = next === "home" ? "#/" : `#/${next}`;
    setView(next);
  }

  function saveProject(e: FormEvent) {
    e.preventDefault();
    setCurrentProjectId(projectId.trim());
    window.location.reload();
  }

  function refreshAll() {
    void run(async () => {
      await Promise.all([loadCommunities(), loadCommunityData(), loadThreads(), loadPosts(), loadCourse()]);
    });
  }

  function createCommunity(e: FormEvent) {
    e.preventDefault();
    void run(async () => {
      const created = await api.communities.create(communityForm);
      setCommunityForm({ name: "", slug: "", description: "" });
      setCommunityId(created.id);
      await loadCommunities();
    });
  }

  function createMember(e: FormEvent) {
    e.preventDefault();
    if (!communityId) return;
    void run(async () => {
      const created = await api.members.create({ community_id: communityId, ...memberForm });
      setMemberForm({ display_name: "", handle: "", bio: "" });
      setMemberId(created.id);
      await loadCommunityData();
    });
  }

  function createSpace(e: FormEvent) {
    e.preventDefault();
    if (!communityId) return;
    void run(async () => {
      const created = await api.spaces.create({ community_id: communityId, ...spaceForm });
      setSpaceForm({ name: "", slug: "", kind: "forum", visibility: "members" });
      setSpaceId(created.id);
      await loadCommunityData();
    });
  }

  function createThread(e: FormEvent) {
    e.preventDefault();
    if (!spaceId || !memberId) return;
    void run(async () => {
      const out = await api.threads.create({ space_id: spaceId, author_id: memberId, ...threadForm });
      setThreadForm({ title: "", body: "" });
      setThreadId(out.thread.id);
      await loadThreads();
      await loadPosts();
    });
  }

  function createPost(e: FormEvent) {
    e.preventDefault();
    if (!threadId || !memberId || !replyBody.trim()) return;
    void run(async () => {
      await api.posts.create({ thread_id: threadId, author_id: memberId, body: replyBody });
      setReplyBody("");
      await Promise.all([loadThreads(), loadPosts()]);
    });
  }

  function createCourse(e: FormEvent) {
    e.preventDefault();
    if (!communityId) return;
    void run(async () => {
      const created = await api.spaces.createCourse({ community_id: communityId, ...courseForm });
      setCourseForm({ name: "", slug: "", visibility: "members" });
      setCourseId(created.id);
      await loadCommunityData();
    });
  }

  function createSection(e: FormEvent) {
    e.preventDefault();
    if (!courseId || !sectionTitle.trim()) return;
    void run(async () => {
      await api.courses.createSection({ space_id: courseId, title: sectionTitle, position: sections.length });
      setSectionTitle("");
      await loadCourse();
    });
  }

  function createLesson(e: FormEvent) {
    e.preventDefault();
    const targetSection = lessonForm.section_id || sections[0]?.id;
    if (!targetSection || !lessonForm.title.trim()) return;
    void run(async () => {
      const created = await api.courses.createLesson({ section_id: targetSection, title: lessonForm.title, body: lessonForm.body });
      setLessonForm({ section_id: targetSection, title: "", body: "" });
      setLessonId(created.id);
      await loadCourse();
    });
  }

  function publishLesson(published: boolean) {
    if (!selectedLesson) return;
    void run(async () => {
      await api.courses.publishLesson(selectedLesson.id, published);
      await loadCourse();
    });
  }

  function markLessonComplete() {
    if (!selectedLesson || !memberId) return;
    void run(async () => {
      await api.courses.markLesson(selectedLesson.id, memberId, "complete");
      await loadCourse();
    });
  }

  function saveCourseDetails(e: FormEvent) {
    e.preventDefault();
    if (!courseId) return;
    void run(async () => {
      await api.courses.updateDetails({
        space_id: courseId,
        summary: detailsForm.summary,
        description: detailsForm.description,
        instructor_member_id: detailsForm.instructor_member_id,
        instructor_name: detailsForm.instructor_name,
        level: detailsForm.level,
        tags: listFromText(detailsForm.tags),
        price_cents: maybeNumber(detailsForm.price_cents) || 0,
        currency: detailsForm.currency || "USD",
        prerequisites: listFromText(detailsForm.prerequisites),
        outcomes: listFromText(detailsForm.outcomes),
        cover_storage_file_id: detailsForm.cover_storage_file_id,
      });
      await loadCourse();
    });
  }

  function saveEnrollmentRules(e: FormEvent) {
    e.preventDefault();
    if (!courseId) return;
    void run(async () => {
      await api.courses.setEnrollmentRules({
        space_id: courseId,
        access_mode: enrollmentForm.access_mode as EnrollmentRule["access_mode"],
        requires_approval: enrollmentForm.requires_approval,
        max_enrollments: maybeNumber(enrollmentForm.max_enrollments),
        starts_at: enrollmentForm.starts_at,
        ends_at: enrollmentForm.ends_at,
      });
      await loadCourse();
    });
  }

  function enrollSelectedMember() {
    if (!courseId || !memberId) return;
    void run(async () => {
      await api.courses.enroll({ space_id: courseId, member_id: memberId });
      await loadCourse();
    });
  }

  function saveCertificate(e: FormEvent) {
    e.preventDefault();
    if (!courseId) return;
    void run(async () => {
      await api.courses.configureCertificate({ space_id: courseId, ...certificateForm });
      await loadCourse();
    });
  }

  function updateSection(section: Section) {
    const title = sectionEdit[section.id] ?? section.title;
    void run(async () => {
      await api.courses.updateSection({ id: section.id, title });
      await loadCourse();
    });
  }

  function deleteSection(section: Section) {
    void run(async () => {
      await api.courses.deleteSection(section.id);
      await loadCourse();
    });
  }

  function moveSection(section: Section, direction: -1 | 1) {
    const index = sections.findIndex((s) => s.id === section.id);
    const next = [...sections];
    const swap = index + direction;
    if (index < 0 || swap < 0 || swap >= next.length) return;
    [next[index], next[swap]] = [next[swap], next[index]];
    void run(async () => {
      await api.courses.reorderSections(courseId, next.map((s) => s.id));
      await loadCourse();
    });
  }

  function updateLesson(e: FormEvent) {
    e.preventDefault();
    if (!selectedLesson) return;
    void run(async () => {
      await api.courses.updateLesson({ id: selectedLesson.id, ...lessonEdit });
      await loadCourse();
    });
  }

  function deleteLesson() {
    if (!selectedLesson) return;
    void run(async () => {
      await api.courses.deleteLesson(selectedLesson.id);
      setLessonId("");
      await loadCourse();
    });
  }

  function moveLesson(lesson: Lesson, direction: -1 | 1) {
    const sectionLessons = (lessonBySection.get(lesson.section_id) || []).slice();
    const index = sectionLessons.findIndex((l) => l.id === lesson.id);
    const swap = index + direction;
    if (index < 0 || swap < 0 || swap >= sectionLessons.length) return;
    [sectionLessons[index], sectionLessons[swap]] = [sectionLessons[swap], sectionLessons[index]];
    void run(async () => {
      await api.courses.reorderLessons(lesson.section_id, sectionLessons.map((l) => l.id));
      await loadCourse();
    });
  }

  function attachVideo(e: FormEvent) {
    e.preventDefault();
    if (!selectedLesson || !videoForm.storage_key.trim()) return;
    void run(async () => {
      await api.courses.attachVideo(selectedLesson.id, videoForm.storage_key, maybeNumber(videoForm.duration_seconds));
      await loadCourse();
    });
  }

  function addResource(e: FormEvent) {
    e.preventDefault();
    if (!selectedLesson || !resourceForm.storage_file_id.trim()) return;
    void run(async () => {
      await api.courses.addResource({
        lesson_id: selectedLesson.id,
        storage_file_id: resourceForm.storage_file_id,
        name: resourceForm.name,
        kind: resourceForm.kind,
        content_type: resourceForm.content_type,
        size_bytes: maybeNumber(resourceForm.size_bytes),
      });
      setResourceForm({ storage_file_id: "", name: "", kind: "file", content_type: "", size_bytes: "" });
      await loadLessonExtras();
      await loadCourse();
    });
  }

  function addQuiz(e: FormEvent) {
    e.preventDefault();
    if (!selectedLesson || !quizForm.title.trim()) return;
    void run(async () => {
      await api.courses.createQuiz({
        lesson_id: selectedLesson.id,
        title: quizForm.title,
        questions: parseQuestions(quizForm.questions),
        passing_score: maybeNumber(quizForm.passing_score) || 70,
      });
      setQuizForm({ title: "", questions: "", passing_score: "70" });
      await loadLessonExtras();
      await loadCourse();
    });
  }

  function addAssignment(e: FormEvent) {
    e.preventDefault();
    if (!selectedLesson || !assignmentForm.title.trim()) return;
    void run(async () => {
      await api.courses.createAssignment({
        lesson_id: selectedLesson.id,
        title: assignmentForm.title,
        instructions: assignmentForm.instructions,
        due_after_days: maybeNumber(assignmentForm.due_after_days),
        attachment_storage_file_id: assignmentForm.attachment_storage_file_id,
      });
      setAssignmentForm({ title: "", instructions: "", due_after_days: "", attachment_storage_file_id: "" });
      await loadLessonExtras();
      await loadCourse();
    });
  }

  function setDrip(e: FormEvent) {
    e.preventDefault();
    if (!selectedLesson) return;
    void run(async () => {
      await api.courses.setDrip({
        lesson_id: selectedLesson.id,
        release_at: dripForm.release_at,
        release_after_days: maybeNumber(dripForm.release_after_days),
      });
      await loadCourse();
    });
  }

  function postLessonComment(e: FormEvent) {
    e.preventDefault();
    if (!selectedLesson || !memberId || !commentBody.trim()) return;
    void run(async () => {
      await api.courses.postComment({ lesson_id: selectedLesson.id, member_id: memberId, body: commentBody });
      setCommentBody("");
      await loadLessonExtras();
      await loadCourse();
    });
  }

  function deleteResource(id: string) {
    void run(async () => {
      await api.courses.deleteResource(id);
      await loadLessonExtras();
      await loadCourse();
    });
  }

  function deleteQuiz(id: string) {
    void run(async () => {
      await api.courses.deleteQuiz(id);
      await loadLessonExtras();
      await loadCourse();
    });
  }

  function deleteAssignment(id: string) {
    void run(async () => {
      await api.courses.deleteAssignment(id);
      await loadLessonExtras();
      await loadCourse();
    });
  }

  function authSubmit(e: FormEvent, mode: "login" | "signup") {
    e.preventDefault();
    void run(async () => {
      const body = { ...authForm, organization_slug: authForm.organization_slug || undefined };
      const out = mode === "login" ? await api.auth.login(body) : await api.auth.signup({ ...body, display_name: authForm.email });
      setAuthResult(JSON.stringify(out, null, 2));
    });
  }

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <Users size={22} />
          <div>
            <strong>Community</strong>
            <span>Member portal</span>
          </div>
        </div>

        <form className="project-form" onSubmit={saveProject}>
          <label>Project</label>
          <div className="inline">
            <input value={projectId} onChange={(e) => setProjectId(e.target.value)} placeholder="project_id" />
            <button type="submit" className="icon-button" aria-label="Save project">
              <CheckCircle2 size={16} />
            </button>
          </div>
        </form>

        <nav>
          {views.map((item) => {
            const Icon = item.icon;
            return (
              <button key={item.id} className={view === item.id ? "active" : ""} onClick={() => navigate(item.id)}>
                <Icon size={17} />
                {item.label}
              </button>
            );
          })}
        </nav>

        <section className="side-section">
          <div className="section-title">Communities</div>
          <select value={communityId} onChange={(e) => setCommunityId(e.target.value)}>
            <option value="">Select community</option>
            {communities.map((community) => (
              <option key={community.id} value={community.id}>
                {community.name}
              </option>
            ))}
          </select>
        </section>

        <section className="side-section">
          <div className="section-title">Acting As</div>
          <select value={memberId} onChange={(e) => setMemberId(e.target.value)}>
            <option value="">Select member</option>
            {members.map((member) => (
              <option key={member.id} value={member.id}>
                {member.display_name || member.handle}
              </option>
            ))}
          </select>
          {selectedMember ? <p className="muted">@{selectedMember.handle}</p> : <p className="muted">Create a member to post and track lessons.</p>}
        </section>
      </aside>

      <main>
        <header className="topbar">
          <div>
            <p className="eyebrow">{selectedCommunity?.slug || "setup"}</p>
            <h1>{selectedCommunity?.name || "Create your community"}</h1>
          </div>
          <button className="secondary" onClick={refreshAll} disabled={busy}>
            <RefreshCcw size={16} />
            Refresh
          </button>
        </header>

        {error ? <div className="error">{error}</div> : null}

        {view === "home" ? (
          <HomeView
            communities={communities}
            selectedCommunity={selectedCommunity}
            discussionSpaces={discussionSpaces}
            courses={courses}
            members={members}
            communityForm={communityForm}
            setCommunityForm={setCommunityForm}
            createCommunity={createCommunity}
          />
        ) : null}

        {view === "spaces" ? (
          <SpacesView
            spaces={discussionSpaces}
            selectedSpace={selectedSpace?.kind === "course" ? undefined : selectedSpace}
            setSpaceId={setSpaceId}
            threads={threads}
            selectedThread={selectedThread}
            setThreadId={setThreadId}
            posts={posts}
            members={members}
            memberId={memberId}
            spaceForm={spaceForm}
            setSpaceForm={setSpaceForm}
            createSpace={createSpace}
            threadForm={threadForm}
            setThreadForm={setThreadForm}
            createThread={createThread}
            replyBody={replyBody}
            setReplyBody={setReplyBody}
            createPost={createPost}
          />
        ) : null}

        {view === "courses" ? (
          <CoursesView
            courses={courses}
            selectedCourse={selectedCourse}
            setCourseId={setCourseId}
            courseDetails={courseDetails}
            detailsForm={detailsForm}
            setDetailsForm={setDetailsForm}
            saveCourseDetails={saveCourseDetails}
            enrollmentRule={enrollmentRule}
            enrollmentForm={enrollmentForm}
            setEnrollmentForm={setEnrollmentForm}
            saveEnrollmentRules={saveEnrollmentRules}
            enrollSelectedMember={enrollSelectedMember}
            enrollments={enrollments}
            certificate={certificate}
            certificateForm={certificateForm}
            setCertificateForm={setCertificateForm}
            saveCertificate={saveCertificate}
            analytics={analytics}
            sections={sections}
            lessons={lessons}
            lessonBySection={lessonBySection}
            selectedLesson={selectedLesson}
            setLessonId={setLessonId}
            memberId={memberId}
            courseForm={courseForm}
            setCourseForm={setCourseForm}
            createCourse={createCourse}
            sectionTitle={sectionTitle}
            setSectionTitle={setSectionTitle}
            createSection={createSection}
            sectionEdit={sectionEdit}
            setSectionEdit={setSectionEdit}
            updateSection={updateSection}
            deleteSection={deleteSection}
            moveSection={moveSection}
            lessonForm={lessonForm}
            setLessonForm={setLessonForm}
            createLesson={createLesson}
            lessonEdit={lessonEdit}
            setLessonEdit={setLessonEdit}
            updateLesson={updateLesson}
            deleteLesson={deleteLesson}
            moveLesson={moveLesson}
            publishLesson={publishLesson}
            markLessonComplete={markLessonComplete}
            videoForm={videoForm}
            setVideoForm={setVideoForm}
            attachVideo={attachVideo}
            resources={resources}
            resourceForm={resourceForm}
            setResourceForm={setResourceForm}
            addResource={addResource}
            deleteResource={deleteResource}
            quizzes={quizzes}
            quizForm={quizForm}
            setQuizForm={setQuizForm}
            addQuiz={addQuiz}
            deleteQuiz={deleteQuiz}
            assignments={assignments}
            assignmentForm={assignmentForm}
            setAssignmentForm={setAssignmentForm}
            addAssignment={addAssignment}
            deleteAssignment={deleteAssignment}
            drips={drips}
            dripForm={dripForm}
            setDripForm={setDripForm}
            setDrip={setDrip}
            comments={comments}
            commentBody={commentBody}
            setCommentBody={setCommentBody}
            postLessonComment={postLessonComment}
          />
        ) : null}

        {view === "members" ? (
          <MembersView
            members={members}
            memberForm={memberForm}
            setMemberForm={setMemberForm}
            createMember={createMember}
          />
        ) : null}

        {view === "auth" ? (
          <AuthView
            authForm={authForm}
            setAuthForm={setAuthForm}
            authResult={authResult}
            authSubmit={authSubmit}
          />
        ) : null}
      </main>
    </div>
  );
}

function HomeView(props: {
  communities: Community[];
  selectedCommunity?: Community;
  discussionSpaces: Space[];
  courses: Space[];
  members: Member[];
  communityForm: { name: string; slug: string; description: string };
  setCommunityForm: (v: { name: string; slug: string; description: string }) => void;
  createCommunity: (e: FormEvent) => void;
}) {
  return (
    <div className="grid two">
      <section className="panel hero-panel">
        <div className="stat-row">
          <Metric label="Spaces" value={props.discussionSpaces.length} />
          <Metric label="Courses" value={props.courses.length} />
          <Metric label="Members" value={props.members.length} />
        </div>
        <h2>{props.selectedCommunity?.name || "Community workspace"}</h2>
        <p>{props.selectedCommunity?.description || "Set up a community, add spaces, publish courses, and let members participate from one portal."}</p>
      </section>

      <section className="panel">
        <h3>Create Community</h3>
        <form className="stack" onSubmit={props.createCommunity}>
          <input
            value={props.communityForm.name}
            onChange={(e) => props.setCommunityForm({ ...props.communityForm, name: e.target.value, slug: props.communityForm.slug || slugify(e.target.value) })}
            placeholder="Name"
            required
          />
          <input
            value={props.communityForm.slug}
            onChange={(e) => props.setCommunityForm({ ...props.communityForm, slug: slugify(e.target.value) })}
            placeholder="slug"
            required
          />
          <textarea
            value={props.communityForm.description}
            onChange={(e) => props.setCommunityForm({ ...props.communityForm, description: e.target.value })}
            placeholder="Description"
          />
          <button type="submit">
            <Plus size={16} />
            Create community
          </button>
        </form>
      </section>

      <section className="panel span">
        <h3>Current Surface</h3>
        <div className="feature-grid">
          <Feature icon={Hash} title="Spaces" body="Feeds, forums, and chat-style areas for member conversations." />
          <Feature icon={BookOpen} title="Courses" body="Course spaces with sections, lessons, publishing, and progress." />
          <Feature icon={MessageSquare} title="Threads" body="Threaded posts, replies, reactions, pinning, and locking through Community tools." />
          <Feature icon={UserRound} title="Members" body="Member directory and local member identity until Auth mapping is promoted server-side." />
        </div>
      </section>
    </div>
  );
}

function SpacesView(props: {
  spaces: Space[];
  selectedSpace?: Space;
  setSpaceId: (id: string) => void;
  threads: Thread[];
  selectedThread?: Thread;
  setThreadId: (id: string) => void;
  posts: Post[];
  members: Member[];
  memberId: string;
  spaceForm: { name: string; slug: string; kind: string; visibility: string };
  setSpaceForm: (v: { name: string; slug: string; kind: string; visibility: string }) => void;
  createSpace: (e: FormEvent) => void;
  threadForm: { title: string; body: string };
  setThreadForm: (v: { title: string; body: string }) => void;
  createThread: (e: FormEvent) => void;
  replyBody: string;
  setReplyBody: (v: string) => void;
  createPost: (e: FormEvent) => void;
}) {
  return (
    <div className="grid layout">
      <section className="panel list-panel">
        <h3>Spaces</h3>
        <div className="list">
          {props.spaces.map((space) => (
            <button key={space.id} className={props.selectedSpace?.id === space.id ? "list-item active" : "list-item"} onClick={() => props.setSpaceId(space.id)}>
              <span>{space.name}</span>
              <small>{space.kind} · {space.visibility}</small>
            </button>
          ))}
        </div>
        <form className="stack compact" onSubmit={props.createSpace}>
          <input
            value={props.spaceForm.name}
            onChange={(e) => props.setSpaceForm({ ...props.spaceForm, name: e.target.value, slug: props.spaceForm.slug || slugify(e.target.value) })}
            placeholder="New space"
            required
          />
          <div className="inline">
            <input value={props.spaceForm.slug} onChange={(e) => props.setSpaceForm({ ...props.spaceForm, slug: slugify(e.target.value) })} placeholder="slug" required />
            <select value={props.spaceForm.kind} onChange={(e) => props.setSpaceForm({ ...props.spaceForm, kind: e.target.value })}>
              <option value="forum">Forum</option>
              <option value="feed">Feed</option>
              <option value="chat">Chat</option>
            </select>
          </div>
          <button type="submit">
            <Plus size={16} />
            Add space
          </button>
        </form>
      </section>

      <section className="panel list-panel">
        <h3>{props.selectedSpace?.name || "Threads"}</h3>
        <div className="list">
          {props.threads.map((thread) => (
            <button key={thread.id} className={props.selectedThread?.id === thread.id ? "list-item active" : "list-item"} onClick={() => props.setThreadId(thread.id)}>
              <span>{thread.title || "Untitled thread"}</span>
              <small>{thread.post_count} posts</small>
            </button>
          ))}
        </div>
        <form className="stack compact" onSubmit={props.createThread}>
          <input value={props.threadForm.title} onChange={(e) => props.setThreadForm({ ...props.threadForm, title: e.target.value })} placeholder="Thread title" />
          <textarea value={props.threadForm.body} onChange={(e) => props.setThreadForm({ ...props.threadForm, body: e.target.value })} placeholder="Start the discussion" />
          <button type="submit" disabled={!props.memberId || !props.selectedSpace}>
            <Plus size={16} />
            New thread
          </button>
        </form>
      </section>

      <section className="panel conversation">
        <h3>{props.selectedThread?.title || "Select a thread"}</h3>
        <div className="posts">
          {props.posts.map((post) => {
            const author = props.members.find((m) => m.id === post.author_id);
            return (
              <article key={post.id} className="post">
                <strong>{author?.display_name || author?.handle || "Member"}</strong>
                <p>{post.removed_at ? "This post was removed." : post.body}</p>
                {post.reactions?.length ? <small>{post.reactions.map((r) => `${r.emoji} ${r.count}`).join(" · ")}</small> : null}
              </article>
            );
          })}
        </div>
        <form className="composer" onSubmit={props.createPost}>
          <textarea value={props.replyBody} onChange={(e) => props.setReplyBody(e.target.value)} placeholder="Reply to the thread" />
          <button type="submit" disabled={!props.memberId || !props.selectedThread}>
            <Send size={16} />
            Send
          </button>
        </form>
      </section>
    </div>
  );
}

function CoursesView(props: {
  courses: Space[];
  selectedCourse?: Space;
  setCourseId: (id: string) => void;
  courseDetails: CourseDetails;
  detailsForm: {
    summary: string;
    description: string;
    instructor_member_id: string;
    instructor_name: string;
    level: string;
    tags: string;
    price_cents: string;
    currency: string;
    prerequisites: string;
    outcomes: string;
    cover_storage_file_id: string;
  };
  setDetailsForm: (v: {
    summary: string;
    description: string;
    instructor_member_id: string;
    instructor_name: string;
    level: string;
    tags: string;
    price_cents: string;
    currency: string;
    prerequisites: string;
    outcomes: string;
    cover_storage_file_id: string;
  }) => void;
  saveCourseDetails: (e: FormEvent) => void;
  enrollmentRule: EnrollmentRule;
  enrollmentForm: { access_mode: string; requires_approval: boolean; max_enrollments: string; starts_at: string; ends_at: string };
  setEnrollmentForm: (v: { access_mode: string; requires_approval: boolean; max_enrollments: string; starts_at: string; ends_at: string }) => void;
  saveEnrollmentRules: (e: FormEvent) => void;
  enrollSelectedMember: () => void;
  enrollments: CourseEnrollment[];
  certificate: CourseCertificate;
  certificateForm: { enabled: boolean; title: string; body: string; template_storage_file_id: string; issue_on_completion: boolean };
  setCertificateForm: (v: { enabled: boolean; title: string; body: string; template_storage_file_id: string; issue_on_completion: boolean }) => void;
  saveCertificate: (e: FormEvent) => void;
  analytics?: CourseAnalytics;
  sections: Section[];
  lessons: Lesson[];
  lessonBySection: Map<string, Lesson[]>;
  selectedLesson?: Lesson;
  setLessonId: (id: string) => void;
  memberId: string;
  courseForm: { name: string; slug: string; visibility: string };
  setCourseForm: (v: { name: string; slug: string; visibility: string }) => void;
  createCourse: (e: FormEvent) => void;
  sectionTitle: string;
  setSectionTitle: (v: string) => void;
  createSection: (e: FormEvent) => void;
  sectionEdit: Record<string, string>;
  setSectionEdit: (v: Record<string, string>) => void;
  updateSection: (section: Section) => void;
  deleteSection: (section: Section) => void;
  moveSection: (section: Section, direction: -1 | 1) => void;
  lessonForm: { section_id: string; title: string; body: string };
  setLessonForm: (v: { section_id: string; title: string; body: string }) => void;
  createLesson: (e: FormEvent) => void;
  lessonEdit: { title: string; body: string };
  setLessonEdit: (v: { title: string; body: string }) => void;
  updateLesson: (e: FormEvent) => void;
  deleteLesson: () => void;
  moveLesson: (lesson: Lesson, direction: -1 | 1) => void;
  publishLesson: (published: boolean) => void;
  markLessonComplete: () => void;
  videoForm: { storage_key: string; duration_seconds: string };
  setVideoForm: (v: { storage_key: string; duration_seconds: string }) => void;
  attachVideo: (e: FormEvent) => void;
  resources: LessonResource[];
  resourceForm: { storage_file_id: string; name: string; kind: string; content_type: string; size_bytes: string };
  setResourceForm: (v: { storage_file_id: string; name: string; kind: string; content_type: string; size_bytes: string }) => void;
  addResource: (e: FormEvent) => void;
  deleteResource: (id: string) => void;
  quizzes: Quiz[];
  quizForm: { title: string; questions: string; passing_score: string };
  setQuizForm: (v: { title: string; questions: string; passing_score: string }) => void;
  addQuiz: (e: FormEvent) => void;
  deleteQuiz: (id: string) => void;
  assignments: Assignment[];
  assignmentForm: { title: string; instructions: string; due_after_days: string; attachment_storage_file_id: string };
  setAssignmentForm: (v: { title: string; instructions: string; due_after_days: string; attachment_storage_file_id: string }) => void;
  addAssignment: (e: FormEvent) => void;
  deleteAssignment: (id: string) => void;
  drips: DripSchedule[];
  dripForm: { release_at: string; release_after_days: string };
  setDripForm: (v: { release_at: string; release_after_days: string }) => void;
  setDrip: (e: FormEvent) => void;
  comments: LessonComment[];
  commentBody: string;
  setCommentBody: (v: string) => void;
  postLessonComment: (e: FormEvent) => void;
}) {
  const currentDrip = props.drips.find((d) => d.lesson_id === props.selectedLesson?.id);
  return (
    <div className="course-builder">
      <section className="panel list-panel">
        <h3>Courses</h3>
        <div className="list">
          {props.courses.map((course) => (
            <button key={course.id} className={props.selectedCourse?.id === course.id ? "list-item active" : "list-item"} onClick={() => props.setCourseId(course.id)}>
              <span>{course.name}</span>
              <small>{course.visibility}</small>
            </button>
          ))}
        </div>
        <form className="stack compact" onSubmit={props.createCourse}>
          <input
            value={props.courseForm.name}
            onChange={(e) => props.setCourseForm({ ...props.courseForm, name: e.target.value, slug: props.courseForm.slug || slugify(e.target.value) })}
            placeholder="Course name"
            required
          />
          <input value={props.courseForm.slug} onChange={(e) => props.setCourseForm({ ...props.courseForm, slug: slugify(e.target.value) })} placeholder="slug" required />
          <button type="submit">
            <Plus size={16} />
            Add course
          </button>
        </form>
      </section>

      <section className="panel builder-wide">
        <div className="reader-actions">
          <div>
            <h3>{props.selectedCourse?.name || "Course Setup"}</h3>
            <p className="muted">{props.courseDetails.summary || "Define the public course offer, files, pricing, and access rules."}</p>
          </div>
          <button onClick={props.enrollSelectedMember} disabled={!props.selectedCourse || !props.memberId}>
            <Users size={16} />
            Enroll
          </button>
        </div>
        <div className="builder-grid">
          <form className="stack" onSubmit={props.saveCourseDetails}>
            <h4>Definition</h4>
            <input value={props.detailsForm.summary} onChange={(e) => props.setDetailsForm({ ...props.detailsForm, summary: e.target.value })} placeholder="Summary" />
            <textarea value={props.detailsForm.description} onChange={(e) => props.setDetailsForm({ ...props.detailsForm, description: e.target.value })} placeholder="Course description" />
            <div className="inline">
              <input value={props.detailsForm.instructor_name} onChange={(e) => props.setDetailsForm({ ...props.detailsForm, instructor_name: e.target.value })} placeholder="Instructor name" />
              <input value={props.detailsForm.instructor_member_id} onChange={(e) => props.setDetailsForm({ ...props.detailsForm, instructor_member_id: e.target.value })} placeholder="Instructor member id" />
            </div>
            <div className="inline">
              <input value={props.detailsForm.level} onChange={(e) => props.setDetailsForm({ ...props.detailsForm, level: e.target.value })} placeholder="Level" />
              <input value={props.detailsForm.cover_storage_file_id} onChange={(e) => props.setDetailsForm({ ...props.detailsForm, cover_storage_file_id: e.target.value })} placeholder="Cover storage file id" />
            </div>
            <div className="inline">
              <input value={props.detailsForm.price_cents} onChange={(e) => props.setDetailsForm({ ...props.detailsForm, price_cents: e.target.value })} placeholder="Price cents" />
              <input value={props.detailsForm.currency} onChange={(e) => props.setDetailsForm({ ...props.detailsForm, currency: e.target.value.toUpperCase() })} placeholder="Currency" />
            </div>
            <textarea value={props.detailsForm.tags} onChange={(e) => props.setDetailsForm({ ...props.detailsForm, tags: e.target.value })} placeholder="Tags, one per line" />
            <textarea value={props.detailsForm.prerequisites} onChange={(e) => props.setDetailsForm({ ...props.detailsForm, prerequisites: e.target.value })} placeholder="Prerequisites, one per line" />
            <textarea value={props.detailsForm.outcomes} onChange={(e) => props.setDetailsForm({ ...props.detailsForm, outcomes: e.target.value })} placeholder="Outcomes, one per line" />
            <button type="submit" disabled={!props.selectedCourse}>
              <Save size={16} />
              Save definition
            </button>
          </form>

          <form className="stack" onSubmit={props.saveEnrollmentRules}>
            <h4>Access</h4>
            <select value={props.enrollmentForm.access_mode} onChange={(e) => props.setEnrollmentForm({ ...props.enrollmentForm, access_mode: e.target.value })}>
              <option value="free">Free</option>
              <option value="paid">Paid</option>
              <option value="invite">Invite</option>
              <option value="manual">Manual</option>
            </select>
            <label className="check-row">
              <input type="checkbox" checked={props.enrollmentForm.requires_approval} onChange={(e) => props.setEnrollmentForm({ ...props.enrollmentForm, requires_approval: e.target.checked })} />
              Approval required
            </label>
            <input value={props.enrollmentForm.max_enrollments} onChange={(e) => props.setEnrollmentForm({ ...props.enrollmentForm, max_enrollments: e.target.value })} placeholder="Max enrollments" />
            <input value={props.enrollmentForm.starts_at} onChange={(e) => props.setEnrollmentForm({ ...props.enrollmentForm, starts_at: e.target.value })} placeholder="Starts at" />
            <input value={props.enrollmentForm.ends_at} onChange={(e) => props.setEnrollmentForm({ ...props.enrollmentForm, ends_at: e.target.value })} placeholder="Ends at" />
            <button type="submit" disabled={!props.selectedCourse}>
              <Save size={16} />
              Save access
            </button>
            <div className="pill-row">
              <span>{props.enrollmentRule.access_mode}</span>
              <span>{props.enrollments.length} enrollments</span>
            </div>
          </form>

          <form className="stack" onSubmit={props.saveCertificate}>
            <h4>Certificate</h4>
            <label className="check-row">
              <input type="checkbox" checked={props.certificateForm.enabled} onChange={(e) => props.setCertificateForm({ ...props.certificateForm, enabled: e.target.checked })} />
              Enabled
            </label>
            <input value={props.certificateForm.title} onChange={(e) => props.setCertificateForm({ ...props.certificateForm, title: e.target.value })} placeholder="Certificate title" />
            <textarea value={props.certificateForm.body} onChange={(e) => props.setCertificateForm({ ...props.certificateForm, body: e.target.value })} placeholder="Certificate body" />
            <input value={props.certificateForm.template_storage_file_id} onChange={(e) => props.setCertificateForm({ ...props.certificateForm, template_storage_file_id: e.target.value })} placeholder="Template storage file id" />
            <label className="check-row">
              <input type="checkbox" checked={props.certificateForm.issue_on_completion} onChange={(e) => props.setCertificateForm({ ...props.certificateForm, issue_on_completion: e.target.checked })} />
              Issue on completion
            </label>
            <button type="submit" disabled={!props.selectedCourse}>
              <Save size={16} />
              Save certificate
            </button>
          </form>
        </div>
      </section>

      <section className="panel list-panel">
        <h3>{props.selectedCourse?.name || "Curriculum"}</h3>
        <div className="curriculum">
          {props.sections.map((section) => (
            <div key={section.id} className="section-block">
              <div className="section-tools">
                <input value={props.sectionEdit[section.id] ?? section.title} onChange={(e) => props.setSectionEdit({ ...props.sectionEdit, [section.id]: e.target.value })} />
                <button className="icon-button secondary" onClick={() => props.moveSection(section, -1)} aria-label="Move section up">
                  <ArrowUp size={15} />
                </button>
                <button className="icon-button secondary" onClick={() => props.moveSection(section, 1)} aria-label="Move section down">
                  <ArrowDown size={15} />
                </button>
                <button className="icon-button secondary" onClick={() => props.updateSection(section)} aria-label="Save section">
                  <Save size={15} />
                </button>
                <button className="icon-button danger" onClick={() => props.deleteSection(section)} aria-label="Delete section">
                  <Trash2 size={15} />
                </button>
              </div>
              {(props.lessonBySection.get(section.id) || []).map((lesson) => (
                <div key={lesson.id} className={props.selectedLesson?.id === lesson.id ? "lesson-row active" : "lesson-row"}>
                  <button className="lesson pick" onClick={() => props.setLessonId(lesson.id)}>
                    <span>{lesson.title}</span>
                    <small>{lesson.published_at ? "Published" : "Draft"}</small>
                  </button>
                  <button className="icon-button secondary" onClick={() => props.moveLesson(lesson, -1)} aria-label="Move lesson up">
                    <ArrowUp size={14} />
                  </button>
                  <button className="icon-button secondary" onClick={() => props.moveLesson(lesson, 1)} aria-label="Move lesson down">
                    <ArrowDown size={14} />
                  </button>
                </div>
              ))}
            </div>
          ))}
        </div>
        <form className="inline-form" onSubmit={props.createSection}>
          <input value={props.sectionTitle} onChange={(e) => props.setSectionTitle(e.target.value)} placeholder="Section title" />
          <button type="submit" disabled={!props.selectedCourse}>
            <Plus size={16} />
          </button>
        </form>
        <form className="stack compact" onSubmit={props.createLesson}>
          <select value={props.lessonForm.section_id} onChange={(e) => props.setLessonForm({ ...props.lessonForm, section_id: e.target.value })}>
            <option value="">First section</option>
            {props.sections.map((section) => (
              <option key={section.id} value={section.id}>
                {section.title}
              </option>
            ))}
          </select>
          <input value={props.lessonForm.title} onChange={(e) => props.setLessonForm({ ...props.lessonForm, title: e.target.value })} placeholder="Lesson title" />
          <textarea value={props.lessonForm.body} onChange={(e) => props.setLessonForm({ ...props.lessonForm, body: e.target.value })} placeholder="Lesson body" />
          <button type="submit" disabled={!props.sections.length}>
            <Plus size={16} />
            Add lesson
          </button>
        </form>
      </section>

      <section className="panel reader">
        <div className="reader-actions">
          <h3>{props.selectedLesson?.title || "Select a lesson"}</h3>
          {props.selectedLesson ? (
            <div className="inline">
              <button className="secondary" onClick={() => props.publishLesson(!props.selectedLesson?.published_at)}>
                {props.selectedLesson.published_at ? "Unpublish" : "Publish"}
              </button>
              <button onClick={props.markLessonComplete} disabled={!props.memberId}>
                <CheckCircle2 size={16} />
                Complete
              </button>
            </div>
          ) : null}
        </div>
        {props.selectedLesson ? (
          <>
            <div className="lesson-meta">
              {props.selectedLesson.published_at ? (
                <span>
                  <Globe2 size={14} /> Published
                </span>
              ) : (
                <span>
                  <Lock size={14} /> Draft
                </span>
              )}
              {props.selectedLesson.progress?.status ? (
                <span>
                  <CheckCircle2 size={14} /> {props.selectedLesson.progress.status}
                </span>
              ) : null}
              {props.selectedLesson.video_storage_key ? (
                <span>
                  <UploadCloud size={14} /> video {props.selectedLesson.video_storage_key}
                </span>
              ) : null}
              {currentDrip ? <span>drip {currentDrip.release_at || `${currentDrip.release_after_days || 0} days`}</span> : null}
            </div>
            <form className="stack" onSubmit={props.updateLesson}>
              <input value={props.lessonEdit.title} onChange={(e) => props.setLessonEdit({ ...props.lessonEdit, title: e.target.value })} placeholder="Lesson title" />
              <textarea value={props.lessonEdit.body} onChange={(e) => props.setLessonEdit({ ...props.lessonEdit, body: e.target.value })} placeholder="Lesson body" />
              <div className="inline">
                <button type="submit">
                  <Save size={16} />
                  Save lesson
                </button>
                <button type="button" className="danger" onClick={props.deleteLesson}>
                  <Trash2 size={16} />
                  Delete
                </button>
              </div>
            </form>

            <form className="inline compact" onSubmit={props.attachVideo}>
              <input value={props.videoForm.storage_key} onChange={(e) => props.setVideoForm({ ...props.videoForm, storage_key: e.target.value })} placeholder="Video storage file id" />
              <input value={props.videoForm.duration_seconds} onChange={(e) => props.setVideoForm({ ...props.videoForm, duration_seconds: e.target.value })} placeholder="Duration seconds" />
              <button type="submit">
                <UploadCloud size={16} />
                Video
              </button>
            </form>

            <form className="inline compact" onSubmit={props.setDrip}>
              <input value={props.dripForm.release_at} onChange={(e) => props.setDripForm({ ...props.dripForm, release_at: e.target.value })} placeholder="Release at" />
              <input value={props.dripForm.release_after_days} onChange={(e) => props.setDripForm({ ...props.dripForm, release_after_days: e.target.value })} placeholder="Release after days" />
              <button type="submit">Drip</button>
            </form>

            <div className="builder-grid lesson-extras">
              <div className="stack">
                <h4>Resources</h4>
                {props.resources.map((resource) => (
                  <div key={resource.id} className="mini-row">
                    <span>{resource.name || resource.storage_file_id}</span>
                    <small>{resource.kind}</small>
                    <button className="icon-button danger" onClick={() => props.deleteResource(resource.id)} aria-label="Delete resource">
                      <Trash2 size={14} />
                    </button>
                  </div>
                ))}
                <form className="stack compact" onSubmit={props.addResource}>
                  <input value={props.resourceForm.storage_file_id} onChange={(e) => props.setResourceForm({ ...props.resourceForm, storage_file_id: e.target.value })} placeholder="Storage file id" />
                  <input value={props.resourceForm.name} onChange={(e) => props.setResourceForm({ ...props.resourceForm, name: e.target.value })} placeholder="Resource name" />
                  <div className="inline">
                    <input value={props.resourceForm.kind} onChange={(e) => props.setResourceForm({ ...props.resourceForm, kind: e.target.value })} placeholder="Kind" />
                    <input value={props.resourceForm.content_type} onChange={(e) => props.setResourceForm({ ...props.resourceForm, content_type: e.target.value })} placeholder="Content type" />
                  </div>
                  <button type="submit">
                    <Paperclip size={16} />
                    Add resource
                  </button>
                </form>
              </div>

              <div className="stack">
                <h4>Quizzes</h4>
                {props.quizzes.map((quiz) => (
                  <div key={quiz.id} className="mini-row">
                    <span>{quiz.title}</span>
                    <small>{quiz.passing_score}%</small>
                    <button className="icon-button danger" onClick={() => props.deleteQuiz(quiz.id)} aria-label="Delete quiz">
                      <Trash2 size={14} />
                    </button>
                  </div>
                ))}
                <form className="stack compact" onSubmit={props.addQuiz}>
                  <input value={props.quizForm.title} onChange={(e) => props.setQuizForm({ ...props.quizForm, title: e.target.value })} placeholder="Quiz title" />
                  <textarea value={props.quizForm.questions} onChange={(e) => props.setQuizForm({ ...props.quizForm, questions: e.target.value })} placeholder="Questions JSON array" />
                  <input value={props.quizForm.passing_score} onChange={(e) => props.setQuizForm({ ...props.quizForm, passing_score: e.target.value })} placeholder="Passing score" />
                  <button type="submit">Add quiz</button>
                </form>
              </div>

              <div className="stack">
                <h4>Assignments</h4>
                {props.assignments.map((assignment) => (
                  <div key={assignment.id} className="mini-row">
                    <span>{assignment.title}</span>
                    <small>{assignment.due_after_days ? `${assignment.due_after_days} days` : "open"}</small>
                    <button className="icon-button danger" onClick={() => props.deleteAssignment(assignment.id)} aria-label="Delete assignment">
                      <Trash2 size={14} />
                    </button>
                  </div>
                ))}
                <form className="stack compact" onSubmit={props.addAssignment}>
                  <input value={props.assignmentForm.title} onChange={(e) => props.setAssignmentForm({ ...props.assignmentForm, title: e.target.value })} placeholder="Assignment title" />
                  <textarea value={props.assignmentForm.instructions} onChange={(e) => props.setAssignmentForm({ ...props.assignmentForm, instructions: e.target.value })} placeholder="Instructions" />
                  <div className="inline">
                    <input value={props.assignmentForm.due_after_days} onChange={(e) => props.setAssignmentForm({ ...props.assignmentForm, due_after_days: e.target.value })} placeholder="Due days" />
                    <input value={props.assignmentForm.attachment_storage_file_id} onChange={(e) => props.setAssignmentForm({ ...props.assignmentForm, attachment_storage_file_id: e.target.value })} placeholder="Storage file id" />
                  </div>
                  <button type="submit">Add assignment</button>
                </form>
              </div>
            </div>

            <section className="stack compact">
              <h4>Comments</h4>
              {props.comments.map((comment) => (
                <article key={comment.id} className="post">
                  <strong>{comment.member_id}</strong>
                  <p>{comment.body}</p>
                </article>
              ))}
              <form className="composer" onSubmit={props.postLessonComment}>
                <textarea value={props.commentBody} onChange={(e) => props.setCommentBody(e.target.value)} placeholder="Comment on this lesson" />
                <button type="submit" disabled={!props.memberId}>
                  <Send size={16} />
                  Comment
                </button>
              </form>
            </section>
          </>
        ) : (
          <p className="muted">Create sections and lessons to build the course.</p>
        )}
      </section>

      <section className="panel builder-wide">
        <h3>
          <BarChart3 size={18} /> Analytics
        </h3>
        <div className="stat-row analytics-row">
          <Metric label="Sections" value={props.analytics?.sections || 0} />
          <Metric label="Lessons" value={props.analytics?.lessons || 0} />
          <Metric label="Published" value={props.analytics?.published_lessons || 0} />
          <Metric label="Resources" value={props.analytics?.resources || 0} />
          <Metric label="Quizzes" value={props.analytics?.quizzes || 0} />
          <Metric label="Assignments" value={props.analytics?.assignments || 0} />
          <Metric label="Comments" value={props.analytics?.comments || 0} />
          <Metric label="Enrollments" value={props.analytics?.active_enrollments || 0} />
          <Metric label="Completion %" value={props.analytics?.progress_completion_percent || 0} />
        </div>
      </section>
    </div>
  );
}

function MembersView(props: {
  members: Member[];
  memberForm: { display_name: string; handle: string; bio: string };
  setMemberForm: (v: { display_name: string; handle: string; bio: string }) => void;
  createMember: (e: FormEvent) => void;
}) {
  return (
    <div className="grid two">
      <section className="panel">
        <h3>Members</h3>
        <div className="member-grid">
          {props.members.map((member) => (
            <article key={member.id} className="member">
              <div className="avatar">{(member.display_name || member.handle).slice(0, 1).toUpperCase()}</div>
              <div>
                <strong>{member.display_name || member.handle}</strong>
                <span>@{member.handle}</span>
                <p>{member.bio || "No bio yet."}</p>
              </div>
            </article>
          ))}
        </div>
      </section>
      <section className="panel">
        <h3>Create Member</h3>
        <form className="stack" onSubmit={props.createMember}>
          <input
            value={props.memberForm.display_name}
            onChange={(e) => props.setMemberForm({ ...props.memberForm, display_name: e.target.value, handle: props.memberForm.handle || handleFromName(e.target.value) })}
            placeholder="Display name"
          />
          <input value={props.memberForm.handle} onChange={(e) => props.setMemberForm({ ...props.memberForm, handle: handleFromName(e.target.value) })} placeholder="handle" required />
          <textarea value={props.memberForm.bio} onChange={(e) => props.setMemberForm({ ...props.memberForm, bio: e.target.value })} placeholder="Bio" />
          <button type="submit">
            <Plus size={16} />
            Add member
          </button>
        </form>
      </section>
    </div>
  );
}

function AuthView(props: {
  authForm: { client_id: string; email: string; password: string; organization_slug: string };
  setAuthForm: (v: { client_id: string; email: string; password: string; organization_slug: string }) => void;
  authResult: string;
  authSubmit: (e: FormEvent, mode: "login" | "signup") => void;
}) {
  return (
    <div className="grid two">
      <section className="panel">
        <h3>Auth App Connection</h3>
        <p className="muted">
          This portal uses the web SDK and can call the Auth app public endpoints. The next server pass should map Auth users to Community members and enforce member identity on portal routes.
        </p>
        <form className="stack" onSubmit={(e) => props.authSubmit(e, "login")}>
          <input value={props.authForm.client_id} onChange={(e) => props.setAuthForm({ ...props.authForm, client_id: e.target.value })} placeholder="Auth client_id" required />
          <input value={props.authForm.organization_slug} onChange={(e) => props.setAuthForm({ ...props.authForm, organization_slug: e.target.value })} placeholder="organization_slug optional" />
          <input type="email" value={props.authForm.email} onChange={(e) => props.setAuthForm({ ...props.authForm, email: e.target.value })} placeholder="Email" required />
          <input type="password" value={props.authForm.password} onChange={(e) => props.setAuthForm({ ...props.authForm, password: e.target.value })} placeholder="Password" required />
          <div className="inline">
            <button type="submit">Login</button>
            <button type="button" className="secondary" onClick={(e) => props.authSubmit(e as unknown as FormEvent, "signup")}>
              Signup
            </button>
          </div>
        </form>
      </section>
      <section className="panel">
        <h3>Result</h3>
        <pre className="result">{props.authResult || "No auth call yet."}</pre>
      </section>
    </div>
  );
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="metric">
      <strong>{value}</strong>
      <span>{label}</span>
    </div>
  );
}

function Feature({ icon: Icon, title, body }: { icon: typeof Hash; title: string; body: string }) {
  return (
    <article className="feature">
      <Icon size={18} />
      <strong>{title}</strong>
      <p>{body}</p>
    </article>
  );
}
