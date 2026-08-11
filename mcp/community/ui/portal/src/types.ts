export type SpaceKind = "feed" | "forum" | "chat" | "course";
export type SpaceVisibility = "public" | "members";

export interface Community {
  id: string;
  project_id: string;
  slug: string;
  name: string;
  description: string;
  created_at: string;
  archived_at?: string;
}

export interface Member {
  id: string;
  community_id: string;
  contact_id?: string;
  auth_user_id?: string;
  handle: string;
  display_name: string;
  bio: string;
  status: string;
  joined_at: string;
  last_seen_at?: string;
}

export interface DMThread {
  id: string;
  community_id: string;
  created_at: string;
  last_message_at: string;
  participants: string[];
  unread_count?: number;
}

export interface DMMessage {
  id: string;
  community_id: string;
  dm_thread_id: string;
  author_id: string;
  body: string;
  created_at: string;
}

export interface DMThreadView extends DMThread {
  messages: DMMessage[];
}

export interface Space {
  id: string;
  community_id: string;
  slug: string;
  name: string;
  kind: SpaceKind;
  visibility: SpaceVisibility;
  sort_order: number;
  created_at: string;
  archived_at?: string;
}

export interface Thread {
  id: string;
  community_id: string;
  space_id: string;
  author_id: string;
  title: string;
  pinned: boolean;
  locked: boolean;
  created_at: string;
  last_post_at: string;
  post_count: number;
}

export interface ReactionSummary {
  emoji: string;
  count: number;
  by: string[];
}

export interface Post {
  id: string;
  community_id: string;
  thread_id: string;
  author_id: string;
  body: string;
  reply_to_id?: string;
  removed_at?: string;
  created_at: string;
  edited_at?: string;
  reactions?: ReactionSummary[];
}

export interface Section {
  id: string;
  space_id: string;
  title: string;
  position: number;
  created_at: string;
}

export interface LessonProgress {
  lesson_id: string;
  member_id: string;
  status: "in_progress" | "complete";
  completed_at?: string;
  last_position_seconds?: number;
  updated_at: string;
}

export interface Lesson {
  id: string;
  community_id: string;
  section_id: string;
  title: string;
  body: string;
  video_storage_key?: string;
  video_duration_seconds?: number;
  position: number;
  published_at?: string;
  created_at: string;
  updated_at: string;
  progress?: LessonProgress;
}

export interface CourseDetails {
  space_id: string;
  summary: string;
  description: string;
  instructor_member_id?: string;
  instructor_name: string;
  level: string;
  tags: string[];
  price_cents: number;
  currency: string;
  prerequisites: string[];
  outcomes: string[];
  cover_storage_file_id?: string;
  updated_at?: string;
}

export interface LessonResource {
  id: string;
  lesson_id: string;
  storage_file_id: string;
  name: string;
  kind: string;
  content_type: string;
  size_bytes?: number;
  position: number;
  created_at: string;
}

export interface Quiz {
  id: string;
  lesson_id: string;
  title: string;
  questions: unknown;
  passing_score: number;
  position: number;
  created_at: string;
  updated_at: string;
}

export interface Assignment {
  id: string;
  lesson_id: string;
  title: string;
  instructions: string;
  due_after_days?: number;
  attachment_storage_file_id?: string;
  created_at: string;
  updated_at: string;
}

export interface LessonComment {
  id: string;
  lesson_id: string;
  member_id: string;
  body: string;
  created_at: string;
}

export interface CourseCertificate {
  space_id: string;
  enabled: boolean;
  title: string;
  body: string;
  template_storage_file_id?: string;
  issue_on_completion: boolean;
  updated_at?: string;
}

export interface DripSchedule {
  id: string;
  space_id: string;
  lesson_id: string;
  release_at?: string;
  release_after_days?: number;
  created_at: string;
  updated_at: string;
}

export interface EnrollmentRule {
  space_id: string;
  access_mode: "free" | "paid" | "invite" | "manual";
  requires_approval: boolean;
  max_enrollments?: number;
  starts_at?: string;
  ends_at?: string;
  updated_at?: string;
}

export interface CourseEnrollment {
  space_id: string;
  member_id: string;
  status: "pending" | "active" | "rejected" | "cancelled" | "completed";
  source: "manual" | "community_purchase" | string;
  source_ref?: string;
  access_expires_at?: string;
  access_revoked_at?: string;
  enrolled_at: string;
  completed_at?: string;
}

export interface CourseOffer {
  space_id: string;
  catalog_product_id: number;
  catalog_price_id: number;
  product_name: string;
  price_nickname?: string;
  unit_amount_cents: number;
  currency: string;
  active: boolean;
  created_at: string;
  updated_at: string;
}

export type CoursePurchaseStatus =
  | "creating"
  | "awaiting_payment"
  | "payment_failed"
  | "paid"
  | "fulfilled"
  | "cancelled"
  | "failed"
  | "refund_pending"
  | "partially_refunded"
  | "refunded";

export interface CoursePurchase {
  id: string;
  community_id: string;
  space_id: string;
  member_id: string;
  catalog_product_id: number;
  catalog_price_id: number;
  product_name: string;
  unit_amount_cents: number;
  currency: string;
  customer_email: string;
  billing_customer_id?: number;
  billing_invoice_id?: number;
  billing_session_id?: string;
  checkout_url?: string;
  status: CoursePurchaseStatus;
  refunded_cents: number;
  last_error?: string;
  created_at: string;
  updated_at: string;
  paid_at?: string;
  fulfilled_at?: string;
  cancelled_at?: string;
  refunded_at?: string;
}

export type MembershipStatus =
  | "creating"
  | "trialing"
  | "past_due"
  | "active"
  | "paused"
  | "cancelled"
  | "ended"
  | "failed";

export interface MembershipPlan {
  id: string;
  community_id: string;
  name: string;
  description: string;
  catalog_product_id: number;
  catalog_price_id: number;
  product_name: string;
  price_nickname?: string;
  unit_amount_cents: number;
  currency: string;
  interval: "day" | "week" | "month" | "year";
  interval_count: number;
  scope_type: "all_courses" | "selected_courses" | "course_tags";
  collection_method: "automatic" | "send_invoice";
  trial_days: number;
  grace_days: number;
  active: boolean;
  course_ids: string[];
  tags: string[];
}

export interface MemberSubscription {
  id: string;
  community_id: string;
  member_id: string;
  plan_id: string;
  billing_customer_id?: number;
  subscription_id?: number;
  status: MembershipStatus;
  current_period_start?: string;
  current_period_end?: string;
  next_renewal_at?: string;
  cancel_at?: string;
  checkout_url?: string;
  last_error?: string;
  plan?: MembershipPlan;
}

export interface PublicSection {
  title: string;
  lessons: string[];
}

export interface PublicCourse {
  slug: string;
  name: string;
  summary?: string;
  description?: string;
  instructor?: string;
  level?: string;
  outcomes: string[];
  cover_file_id?: string;
  curriculum: PublicSection[];
}

export interface PublicOffer {
  catalog_price_id: number;
  kind: "one_time" | "recurring";
  name: string;
  unit_amount_cents: number;
  currency: string;
  interval?: "day" | "week" | "month" | "year";
  interval_count?: number;
  trial_days?: number;
  scope_type: "all_courses" | "selected_courses" | "course_tags";
  course_slugs: string[];
}

export interface PublicProduct {
  catalog_product_id: number;
  slug: string;
  name: string;
  description?: string;
  type: string;
  category?: string;
  color?: string;
  image_file_id?: number;
  offers: PublicOffer[];
  courses: PublicCourse[];
}

export interface CourseAnalytics {
  space_id: string;
  sections: number;
  lessons: number;
  published_lessons: number;
  resources: number;
  quizzes: number;
  assignments: number;
  comments: number;
  active_enrollments: number;
  progress_rows: number;
  completed_progress_rows: number;
  progress_completion_percent: number;
}
