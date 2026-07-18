import type { CalendarPostLike } from "./postCalendar";

const SOCIAL_API = "/api/apps/social";

export interface SocialPostTarget {
  id: number;
  social_account_id: number;
  platform: string;
  display_name: string;
  avatar_url?: string;
  status: string;
  platform_post_id?: string;
  platform_url?: string;
  attempts?: number;
  last_error?: string;
  published_at?: string;
}

export interface SocialPost extends CalendarPostLike {
  id: number;
  body: string;
  media_storage_ids: number[];
  external_media_urls?: string[];
  schedule_at: string;
  status: string;
  created_at: string;
  published_at: string;
  profile_id?: number;
  targets: SocialPostTarget[];
}

export interface SocialAppEvent {
  topic: string;
  app?: string;
  project_id?: string;
  data?: Record<string, unknown>;
}

export function socialURL(path: string, projectId?: string): string {
  const separator = path.includes("?") ? "&" : "?";
  return `${SOCIAL_API}${path}${projectId ? `${separator}project_id=${encodeURIComponent(projectId)}` : ""}`;
}

export async function fetchSocialPost(postID: number, projectId?: string): Promise<SocialPost> {
  const response = await fetch(socialURL(`/posts/${postID}`, projectId), { credentials: "same-origin" });
  if (!response.ok) throw new Error(response.status === 404 ? "Post not found" : await response.text());
  const data = await response.json() as { post?: SocialPost } | SocialPost;
  const post = "post" in data ? data.post : data;
  if (!post) throw new Error("Post response was empty");
  return post;
}

export async function fetchSocialCalendar(input: {
  start: Date;
  end: Date;
  projectId?: string;
  profileID?: number;
}): Promise<SocialPost[]> {
  const params = new URLSearchParams({
    from: input.start.toISOString(),
    to: input.end.toISOString(),
    limit: "1000",
  });
  if (input.profileID) params.set("profile_id", String(input.profileID));
  const response = await fetch(socialURL(`/posts?${params.toString()}`, input.projectId), {
    credentials: "same-origin",
  });
  if (!response.ok) throw new Error(await response.text());
  const data = await response.json() as { posts?: SocialPost[] };
  return data.posts || [];
}

export function subscribeSocialEvents(
  projectId: string | undefined,
  onEvent: (event: SocialAppEvent) => void,
): () => void {
  if (!projectId) return () => undefined;
  const bridge = (window as unknown as {
    __aptevaAppEvents?: {
      subscribe(app: string, project: string, fn: (event: SocialAppEvent) => void): () => void;
    };
  }).__aptevaAppEvents;
  if (bridge) return bridge.subscribe("social", projectId, onEvent);

  const source = new EventSource(`/api/app-events/social?project_id=${encodeURIComponent(projectId)}`, {
    withCredentials: true,
  });
  source.onmessage = (message) => {
    try {
      onEvent(JSON.parse(message.data));
    } catch {
      // Ignore malformed event frames; the polling fallback will recover.
    }
  };
  return () => source.close();
}

export function isPostLifecycleEvent(topic: string): boolean {
  return topic.startsWith("post.") || topic.startsWith("target.");
}

export function postTitle(post: Pick<SocialPost, "body">): string {
  const first = post.body.split(/\r?\n/).map((line) => line.trim()).find(Boolean);
  if (!first) return "Untitled post";
  return first.length > 96 ? `${first.slice(0, 95)}…` : first;
}

export function postStatusVariant(status: string): "muted" | "ok" | "warn" | "err" {
  switch (status) {
    case "published": return "ok";
    case "failed": return "err";
    case "partial": return "warn";
    default: return "muted";
  }
}

export function postStatusColor(status: string): string {
  switch (status) {
    case "published": return "#22C55E";
    case "failed": return "#EF4444";
    case "partial": return "#F59E0B";
    case "scheduled": return "#38BDF8";
    default: return "#A1A1AA";
  }
}

export const previewCalendarPosts: SocialPost[] = [
  previewPost(1, 0, "scheduled", "Product walkthrough for the new automation flow", "linkedin"),
  previewPost(2, 1, "scheduled", "A practical lesson from this week's customer work", "twitter"),
  previewPost(3, 3, "scheduled", "Two-minute focus reset before an important meeting", "youtube"),
];

export const previewSinglePost = previewCalendarPosts[0];

function previewPost(id: number, dayOffset: number, status: string, body: string, platform: string): SocialPost {
  const date = new Date();
  date.setHours(10 + id * 2, 0, 0, 0);
  date.setDate(date.getDate() + dayOffset);
  return {
    id,
    body,
    media_storage_ids: [],
    external_media_urls: [],
    schedule_at: date.toISOString(),
    status,
    created_at: new Date().toISOString(),
    published_at: "",
    targets: [{
      id,
      social_account_id: id,
      platform,
      display_name: platform === "youtube" ? "Inner Edge Audio" : "Marco Schwartz",
      status: "pending",
    }],
  };
}
