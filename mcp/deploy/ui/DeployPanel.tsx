// DeployPanel — control surface for the deploy app. Two-pane:
// left = deployment list + create button, right = detail (status,
// build/release history, env editor, log tail). Mirrors the Code
// panel's interaction model so users moving between them feel
// consistent.

import { useCallback, useEffect, useRef, useState } from "react";

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
    // Cross-bundle multiplexer: the dashboard publishes a shared
    // (app, project) channel pool on window.__aptevaAppEvents. Every
    // panel mounted in the same realm reuses one EventSource per
    // (app, project) instead of opening its own. Without this, a few
    // panels mounted in the agent detail page burn the browser's
    // per-origin HTTP/1.1 connection budget and stuck POSTs follow.
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
    // Fallback: panel running outside the dashboard (or before its
    // hook module loaded). Open an EventSource directly.
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
  instanceId?: number;
}

interface Deployment {
  id: number;
  name: string;
  target_kind: "service" | "android" | "ios";
  description?: string;
  source_kind: string;
  source_ref: string;
  framework: string;
  build_cmd: string;
  build_backend: "local" | "runner" | "codemagic" | "github_actions";
  build_backend_config_json: string;
  start_cmd: string;
  port_hint: number;
  env_json: string;
  target_config_json: string;
  domain: string;
  domain_record_id?: string;
  domain_attached_at?: string;
  current_release_id?: number | null;
  archived_at?: string;
  created_at: string;
  updated_at: string;
}

interface CertStatus {
  status: "pending" | "issuing" | "live" | "failed" | "revoked";
  expires_at?: string;
  error?: string;
}
interface MetaInfo {
  domains_available: boolean;
  certs_available: boolean;
  domains: { name: string }[];
  public_host: string;
  certs: Record<string, CertStatus>;
}

interface Build {
  id: number;
  deployment_id: number;
  source_sha: string;
  framework: string;
  build_backend: "local" | "runner" | "codemagic" | "github_actions";
  external_job_id: string;
  external_status: string;
  status: "pending" | "running" | "succeeded" | "failed" | "cancelled";
  duration_ms: number;
  exit_code: number;
  artifact_path: string;
  artifact_size: number;
  artifact_manifest_json: string;
  artifact_download_url?: string;
  target_config_json?: string;
  log_path: string;
  error: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
}

interface Release {
  id: number;
  deployment_id: number;
  build_id: number;
  status: "starting" | "live" | "stopped" | "crashed" | "failed";
  port: number;
  pid: number;
  started_at?: string;
  stopped_at?: string;
  restart_count: number;
  log_path: string;
  error: string;
  channel?: string;
  provider?: string;
  external_id?: string;
  external_status?: string;
  release_meta_json?: string;
  created_at: string;
}

interface MobileReviewOutcome {
  provider: string;
  submission_id?: string;
  submission_state?: string;
  submitted_at?: string;
  item_id?: string;
  item_state?: string;
  version_id?: string;
  version_name?: string;
  submitted_artifact_id?: string;
  submitted_artifact_version?: string;
  latest_artifact_id?: string;
  latest_artifact_version?: string;
  details_available: boolean;
  details_source?: string;
  provider_console_url?: string;
  action_required?: string;
  sync_error?: string;
  synced_at: string;
}

interface DeploymentDetail {
  deployment: Deployment;
  builds: Build[];
  releases: Release[];
  current_release: Release | null;
  url: string;
}

interface MobileSigningSetup {
  id: number;
  deployment_id: number;
  environment_id?: number;
  platform: string;
  provider: string;
	identity_id?: number;
	prepared_revision?: number;
  bundle_id: string;
  status: "pending" | "provisioning" | "action_required" | "ready" | "failed";
  app_store_app_id?: string;
  apple_bundle_resource_id?: string;
  apple_certificate_id?: string;
  apple_profile_id?: string;
  provider_secret_ref?: string;
  provider_config_json: string;
  key_fingerprint?: string;
  required_features_json: string;
  provisioned_features_json: string;
  managed_features_json: string;
  requirements_hash?: string;
  platform_state_json: string;
  last_error?: string;
  updated_at: string;
}

interface MobileSigningIdentity {
  id: number;
  platform: "android" | "ios";
  application_identifier: string;
  format: string;
  revision: number;
  source: "generated" | "imported";
  key_alias?: string;
	certificate_pem?: string;
  certificate_sha1?: string;
  certificate_sha256?: string;
  expires_at?: string;
}

interface DistributionAudienceMember {
  kind: "individual" | "group";
  email: string;
  first_name?: string;
  last_name?: string;
  state?: string;
}

interface MobileDistributionState {
  platform: "android" | "ios";
  provider: string;
  channel: string;
  group_id?: string;
  group_name?: string;
  configured: boolean;
  audience: DistributionAudienceMember[];
  count: number;
  desired_configured: boolean;
  desired_audience: DistributionAudienceMember[];
  desired_count: number;
  synced: boolean;
  tester_access: "configured" | "not_configured" | "sync_required" | "sync_error";
  install_url?: string;
  install_url_source?: string;
  console_url?: string;
  last_synced_at?: string;
}

interface StoreFinding {
  code: string;
  severity: "error" | "warning" | "info";
  scope: string;
  media_kind?: string;
  asset_id?: string;
  verification?: "provider_read" | "provider_apply" | "provider_commit" | "manual_attestation";
  locale?: string;
  field?: string;
  message: string;
  automatable: boolean;
  action?: string;
}

interface StorePreflight {
  ready: boolean;
  errors: number;
  warnings: number;
  findings: StoreFinding[];
}

interface StoreLocalization {
  title?: string;
  subtitle?: string;
  short_description?: string;
  description?: string;
  keywords?: string[];
  support_url?: string;
  marketing_url?: string;
  promotional_text?: string;
  whats_new?: string;
  video_url?: string;
}

interface StoreAsset {
  id: string;
  locale: string;
  kind: string;
  display_target?: string;
  path: string;
  sha256: string;
  order?: number;
  mime?: string;
  width?: number;
  height?: number;
}

type StoreRatingLevel = "NONE" | "INFREQUENT" | "FREQUENT";

interface StoreContentRating {
  violence?: StoreRatingLevel;
  sexual_content?: StoreRatingLevel;
  profanity?: StoreRatingLevel;
  drugs?: StoreRatingLevel;
  gambling_simulation?: StoreRatingLevel;
  contests?: StoreRatingLevel;
  weapons?: StoreRatingLevel;
  horror_fear?: StoreRatingLevel;
  medical_information?: StoreRatingLevel;
  health_wellness?: StoreRatingLevel;
  mature_themes?: StoreRatingLevel;
  unrestricted_web_access?: boolean;
  real_money_gambling?: boolean;
  loot_boxes?: boolean;
  advertising?: boolean;
  messaging_chat?: boolean;
  user_generated_content?: boolean;
  parental_controls?: boolean;
  age_assurance?: boolean;
  social_media?: boolean;
  social_media_age_gate?: boolean;
}

interface StoreDocument {
  schema_version: number;
  version_name: string;
  default_locale: string;
  release_mode: string;
  earliest_release_at?: string;
  copyright?: string;
  uses_idfa?: boolean;
  content_rights?: {
    uses_third_party_content?: boolean;
    rights_confirmed?: boolean;
  };
  localizations: Record<string, StoreLocalization>;
  assets: StoreAsset[];
  review: {
    first_name?: string;
    last_name?: string;
    email?: string;
    phone?: string;
    notes?: string;
    access_instructions?: string;
    access_confirmed?: boolean;
    credentials_confirmed?: boolean;
    demo_account_required?: boolean;
    demo_username?: string;
    demo_password_set?: boolean;
  };
  classification: {
    primary_category?: string;
    secondary_category?: string;
    content_rating?: StoreContentRating;
    age_declaration?: Record<string, unknown>;
  };
  distribution: {
    territories?: string[];
    availability?: {
      mode?: "all" | "all_except" | "only";
      included_territories?: string[];
      excluded_territories?: string[];
      available_in_new_territories?: boolean;
    };
    price_tier?: string;
    phased_release?: boolean;
    rollout_fraction?: number;
    provider?: Record<string, unknown>;
  };
  testing?: {
    channels?: Record<string, {
      audience?: { kind: "individual" | "group"; identifier: string; first_name?: string; last_name?: string }[];
      install_url?: string;
    }>;
  };
  privacy: {
    policy_url?: string;
    choices_url?: string;
    data_safety_csv?: string;
    declarations?: Record<string, unknown>;
    manual_attestations?: Record<string, boolean>;
  };
  manual_requirements?: { code: string; label: string; completed: boolean; url?: string }[];
  provider_extensions?: Record<string, unknown>;
}

interface StoreConfigState {
  config: {
    id: number;
    provider: string;
    status: string;
    desired_hash: string;
    applied_hash: string;
    observed_json?: string;
    last_error?: string;
  } | null;
  desired: StoreDocument;
  preflight: StorePreflight;
}

interface StoreApplyResult {
  status: string;
  applied: boolean;
  applied_scopes: string[];
  applied_assets?: string[];
  scope_results?: { scope: string; status: string; message?: string }[];
  blocked: { scope: string; media_kind?: string; asset_id?: string; locale?: string; code?: string; message: string }[];
  failed: { scope: string; media_kind?: string; asset_id?: string; locale?: string; code?: string; message: string }[];
}

interface MobilePromotionValidation {
  valid: boolean;
  platform: string;
  provider: string;
  target_channel: string;
  provider_validated: boolean;
  production_access: string;
  commit_performed: boolean;
  preflight: StorePreflight;
  provider_error?: string;
}

const APPLE_CATEGORIES = [
  "BOOKS", "BUSINESS", "DEVELOPER_TOOLS", "EDUCATION", "ENTERTAINMENT", "FINANCE",
  "FOOD_AND_DRINK", "GAMES", "GRAPHICS_AND_DESIGN", "HEALTH_AND_FITNESS", "LIFESTYLE",
  "MAGAZINES_AND_NEWSPAPERS", "MEDICAL", "MUSIC", "NAVIGATION", "NEWS", "PHOTO_AND_VIDEO",
  "PRODUCTIVITY", "REFERENCE", "SHOPPING", "SOCIAL_NETWORKING", "SPORTS", "TRAVEL", "UTILITIES", "WEATHER",
] as const;

const GOOGLE_CATEGORIES = [
  "ART_AND_DESIGN", "AUTO_AND_VEHICLES", "BEAUTY", "BOOKS_AND_REFERENCE", "BUSINESS", "COMICS",
  "COMMUNICATION", "DATING", "EDUCATION", "ENTERTAINMENT", "EVENTS", "FINANCE", "FOOD_AND_DRINK",
  "HEALTH_AND_FITNESS", "HOUSE_AND_HOME", "LIBRARIES_AND_DEMO", "LIFESTYLE", "MAPS_AND_NAVIGATION",
  "MEDICAL", "MUSIC_AND_AUDIO", "NEWS_AND_MAGAZINES", "PARENTING", "PERSONALIZATION", "PHOTOGRAPHY",
  "PRODUCTIVITY", "SHOPPING", "SOCIAL", "SPORTS", "TOOLS", "TRAVEL_AND_LOCAL", "VIDEO_PLAYERS", "WEATHER", "GAME",
] as const;

const RATING_FIELDS: { key: keyof StoreContentRating; label: string }[] = [
  { key: "violence", label: "Violence" },
  { key: "sexual_content", label: "Sexual content or nudity" },
  { key: "profanity", label: "Profanity or crude humor" },
  { key: "drugs", label: "Alcohol, tobacco, or drugs" },
  { key: "gambling_simulation", label: "Simulated gambling" },
  { key: "contests", label: "Contests" },
  { key: "weapons", label: "Guns or other weapons" },
  { key: "horror_fear", label: "Horror or fear" },
  { key: "medical_information", label: "Medical information" },
  { key: "health_wellness", label: "Health or wellness topics" },
  { key: "mature_themes", label: "Mature or suggestive themes" },
];

function currentRatingLevel(value: unknown): string {
  if (value === "INFREQUENT_OR_MILD") return "INFREQUENT";
  if (value === "FREQUENT_OR_INTENSE") return "FREQUENT";
  return String(value || "");
}

function storeReadiness(raw?: string): Record<string, { status?: string; source?: string; message?: string }> {
  if (!raw) return {};
  try {
    return (JSON.parse(raw) as { readiness?: Record<string, { status?: string; source?: string; message?: string }> }).readiness || {};
  } catch {
    return {};
  }
}

interface AutoRestartInfo {
  Attempts: number;
  LastAt: string;
  Paused: boolean;
}

interface UnhealthyEntry {
  deployment_id: number;
  name: string;
  domain?: string;
  status: "crashed" | "failed" | "starting_stuck" | "auto_restart_paused";
  release_id?: number;
  reason?: string;
  unhealthy_for_s: number;
  auto_restart: AutoRestartInfo;
}

const API = "/api/apps/deploy/api";

const FRAMEWORKS = ["", "go", "node", "bun", "static", "blank", "android", "ios"] as const;
const SOURCE_KINDS = ["code", "local"] as const;
const TARGET_KINDS = ["service", "android", "ios"] as const;
const BUILD_BACKENDS = ["local", "runner", "codemagic", "github_actions"] as const;

function statusColor(s: string): string {
  if (s === "live" || s === "succeeded") return "text-green";
  if (s === "running" || s === "starting" || s === "pending") return "text-blue";
  if (s === "stopped") return "text-text-dim";
  return "text-red";
}

function formatSize(n: number): string {
  if (!n) return "—";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} kB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

function formatDuration(ms: number): string {
  if (!ms) return "—";
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

function mobileFeatureLabels(raw: string): string {
  try {
    const features = JSON.parse(raw);
    if (!Array.isArray(features) || features.length === 0) return "none";
    return features.map((feature) => {
      if (feature === "ios.push_notifications") return "Push Notifications";
      return String(feature);
    }).join(", ");
  } catch {
    return "unknown";
  }
}

function cloudExecutionLabel(raw: string, backend: string): string {
  try {
    const config = JSON.parse(raw || "{}");
    const versions = config.software_versions ?? {};
    const xcode = versions.xcode ?? config.xcode_version;
    const runner = config.instance_type ??
      (config.machine_class
        ? config.machine_class
        : backend === "codemagic"
          ? "mac_mini_m2"
          : "");
    return [xcode ? `Xcode ${xcode}` : "", runner].filter(Boolean).join(" · ");
  } catch {
    return "";
  }
}

function releaseRolloutFraction(release: Release): number {
  try {
    const value = Number(JSON.parse(release.release_meta_json || "{}").rollout_fraction);
    return value > 0 && value <= 1 ? value : 0.1;
  } catch {
    return 0.1;
  }
}

function releaseTesterAccess(release: Release): { status: string; count: number; installURL: string } {
  try {
    const meta = JSON.parse(release.release_meta_json || "{}");
    return {
      status: String(meta.tester_access || "not_configured"),
      count: Number(meta.tester_count || 0),
      installURL: String(meta.install_url || ""),
    };
  } catch {
    return { status: "not_configured", count: 0, installURL: "" };
  }
}

function releaseReviewOutcome(release: Release): MobileReviewOutcome | null {
  try {
    return JSON.parse(release.release_meta_json || "{}").review_outcome || null;
  } catch {
    return null;
  }
}

function isProductionMobileChannel(platform: Deployment["target_kind"], channel: string): boolean {
  const normalized = channel.trim().toLowerCase();
  return normalized === "production" || (platform === "android" && normalized.endsWith(":production"));
}

function mobileBuildVersion(build: Build): string {
  for (const raw of [build.artifact_manifest_json, build.target_config_json]) {
    try {
      const value = JSON.parse(raw || "{}");
      const name = String(value.version_name || "");
      const number = String(value.build_number || value.version_code || "");
      if (name || number) return [name, number && `(${number})`].filter(Boolean).join(" ");
    } catch {}
  }
  return "-";
}

function artifactDownloadLabel(deployment: Deployment, build: Build): string {
  let format = deployment.target_kind === "android"
    ? "AAB"
    : deployment.target_kind === "ios"
      ? "IPA"
      : "ZIP";
  try {
    const manifest = JSON.parse(build.artifact_manifest_json || "{}");
    const primary = String(manifest.primary || "");
    const extension = primary.includes(".") ? primary.split(".").pop()?.toUpperCase() : "";
    if (extension && ["AAB", "APK", "IPA", "ZIP"].includes(extension)) format = extension;
  } catch {}
  return `Download ${format}`;
}

export default function DeployPanel({ projectId, installId }: NativePanelProps) {
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [selected, setSelected] = useState<number | null>(null);
  const [detail, setDetail] = useState<DeploymentDetail | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [showCreate, setShowCreate] = useState(false);
  const [logs, setLogs] = useState("");
  const [logKind, setLogKind] = useState<"build" | "release">("release");
  const [logTargetId, setLogTargetId] = useState<number | null>(null);
  const [meta, setMeta] = useState<MetaInfo | null>(null);
  const [showAttachDomain, setShowAttachDomain] = useState(false);
  const [showEditConfig, setShowEditConfig] = useState(false);
  const [health, setHealth] = useState<Record<number, UnhealthyEntry>>({});
  const [mobileChannel, setMobileChannel] = useState("internal");
  const [submitForReview, setSubmitForReview] = useState(false);
  const [mobileSigning, setMobileSigning] = useState<MobileSigningSetup | null>(null);
	const [mobileSigningIdentity, setMobileSigningIdentity] = useState<MobileSigningIdentity | null>(null);
  const [signingBusy, setSigningBusy] = useState(false);
	const [signingImportFile, setSigningImportFile] = useState<File | null>(null);
	const [signingStorePassword, setSigningStorePassword] = useState("");
	const [signingKeyPassword, setSigningKeyPassword] = useState("");
	const [signingKeyAlias, setSigningKeyAlias] = useState("");
  const [distribution, setDistribution] = useState<MobileDistributionState | null>(null);
  const [distributionError, setDistributionError] = useState("");
  const [audienceEmail, setAudienceEmail] = useState("");
  const [audienceDraft, setAudienceDraft] = useState<DistributionAudienceMember[]>([]);
  const [installURLDraft, setInstallURLDraft] = useState("");
  const [audienceBusy, setAudienceBusy] = useState(false);
  const [storeState, setStoreState] = useState<StoreConfigState | null>(null);
  const [showStoreListing, setShowStoreListing] = useState(false);
  const [rolloutFractions, setRolloutFractions] = useState<Record<number, number>>({});
  const [promotionValidations, setPromotionValidations] = useState<Record<number, MobilePromotionValidation>>({});

  const withParams = useCallback(
    (extra: Record<string, string> = {}) =>
      new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...extra,
      }).toString(),
    [projectId, installId],
  );

  const api = useCallback(
    async <T,>(method: string, path: string, body?: unknown, extra: Record<string, string> = {}): Promise<T> => {
      const res = await fetch(`${API}${path}?${withParams(extra)}`, {
        method,
        credentials: "same-origin",
        headers: body ? { "Content-Type": "application/json" } : {},
        body: body ? JSON.stringify(body) : undefined,
      });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      return res.json();
    },
    [withParams],
  );

  const apiText = useCallback(
    async (path: string, extra: Record<string, string> = {}): Promise<string> => {
      const res = await fetch(`${API}${path}?${withParams(extra)}`, { credentials: "same-origin" });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      return res.text();
    },
    [withParams],
  );

  const loadDeployments = useCallback(async () => {
    try {
      const r = await api<{ deployments?: Deployment[] }>("GET", "/deployments");
      setDeployments(r.deployments || []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }, [api]);

  const loadDetail = useCallback(
    async (id: number) => {
      try {
        const d = await api<DeploymentDetail>("GET", `/deployments/${id}`);
        setDetail(d);
		if (d.deployment.target_kind === "ios" || d.deployment.target_kind === "android") {
			const signing = await api<{ setups?: MobileSigningSetup[]; identities?: MobileSigningIdentity[] }>("GET", `/deployments/${id}/mobile-signing`);
			setMobileSigning(
            (signing.setups || []).find((setup) => setup.provider === d.deployment.build_backend)
              || signing.setups?.[0]
              || null,
			);
			setMobileSigningIdentity(signing.identities?.[0] || null);
			setStoreState(await api<StoreConfigState>("GET", `/deployments/${id}/store-config`));
		} else {
			setMobileSigning(null);
			setMobileSigningIdentity(null);
          setStoreState(null);
        }
        // Always re-anchor the log pane to THIS deployment, so
        // switching from a deployment with a live release to a
        // deployment without one doesn't leave the previous one's
        // log target stuck in view. Preference: live release > latest
        // build > nothing.
        if (d.current_release) {
          setLogKind("release");
          setLogTargetId(d.current_release.id);
        } else if (d.deployment.target_kind !== "service" && d.releases?.[0]) {
          setLogKind("release");
          setLogTargetId(d.releases[0].id);
        } else if (d.builds && d.builds[0]) {
          setLogKind("build");
          setLogTargetId(d.builds[0].id);
        } else {
          setLogTargetId(null);
        }
      } catch (e) {
        setError((e as Error).message);
      }
    },
    [api],
  );

  useEffect(() => { loadDeployments(); }, [loadDeployments]);

  useEffect(() => {
    const deployment = detail?.deployment;
    if (!deployment || deployment.target_kind === "service" || isProductionMobileChannel(deployment.target_kind, mobileChannel)) {
      setDistribution(null);
      setDistributionError("");
      return;
    }
    let cancelled = false;
    setDistribution(null);
    setDistributionError("");
    const timer = window.setTimeout(() => {
      api<MobileDistributionState>(
        "GET",
        `/deployments/${deployment.id}/distribution`,
        undefined,
        { channel: mobileChannel },
      ).then((state) => {
        if (!cancelled) {
          setDistribution(state);
          setAudienceDraft(state.desired_configured ? state.desired_audience : state.audience);
          setInstallURLDraft(state.install_url || "");
          setDistributionError("");
        }
      }).catch((e) => {
        if (!cancelled) {
          setDistribution(null);
          setDistributionError((e as Error).message);
        }
      });
    }, 250);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [api, detail?.deployment.id, detail?.deployment.target_kind, mobileChannel]);

  // Capabilities: whether the optional Domains app is installed +
  // the registered domains for the picker. Cheap one-shot per mount.
  useEffect(() => {
    let cancelled = false;
    api<MetaInfo>("GET", "/_meta")
      .then((m) => { if (!cancelled) setMeta(m); })
      .catch(() => { if (!cancelled) setMeta({ domains_available: false, certs_available: false, domains: [], public_host: "", certs: {} }); });
    return () => { cancelled = true; };
  }, [api]);

  // Refresh on relevant events.
  useAppEvents<{ deployment_id?: number; build_id?: number; release_id?: number }>("deploy", projectId, (ev) => {
    if (ev.topic.startsWith("deploy.")) {
      loadDeployments();
      if (selected != null && ev.data?.deployment_id === selected) {
        loadDetail(selected);
      }
      // Crash / auto-restart events change the health snapshot — refresh
      // immediately instead of waiting for the 15s poll.
      if (
        ev.topic === "deploy.release.crashed" ||
        ev.topic === "deploy.release.live" ||
        ev.topic === "deploy.release.stopped" ||
        ev.topic === "deploy.release.failed" ||
        ev.topic === "deploy.auto_restart_paused" ||
        ev.topic === "deploy.auto_restart_attempted" ||
        ev.topic === "deploy.auto_restart_failed" ||
        ev.topic === "deploy.restarted"
      ) {
        loadHealth();
      }
    }
  });

  // Health snapshot poll. Unhealthy = crashed/failed/starting_stuck/
  // auto_restart_paused. Cheap (~one query per deployment) so a 15s
  // tick is fine; events bump it sooner when state actually changes.
  const loadHealth = useCallback(async () => {
    try {
      const r = await api<{ unhealthy?: UnhealthyEntry[] }>("GET", "/health");
      const byID: Record<number, UnhealthyEntry> = {};
      (r.unhealthy || []).forEach((u) => { byID[u.deployment_id] = u; });
      setHealth(byID);
    } catch {
      // Backend may be on an older version without /api/health. Surface
      // nothing rather than spamming the user with errors.
    }
  }, [api]);

  useEffect(() => {
    loadHealth();
    const handle = window.setInterval(loadHealth, 15000);
    return () => window.clearInterval(handle);
  }, [loadHealth]);

  // Auto-tail logs every 2s when there's an active build or live release.
  useEffect(() => {
    if (logTargetId == null) return;
    let alive = true;
    const tick = async () => {
      try {
        const txt = await apiText(`/${logKind === "build" ? "builds" : "releases"}/${logTargetId}/log`, { tail: "300" });
        if (alive) setLogs(txt);
      } catch {/* swallow — endpoint may 404 briefly */}
    };
    tick();
    const handle = window.setInterval(tick, 2000);
    return () => { alive = false; window.clearInterval(handle); };
  }, [logKind, logTargetId, apiText]);

  const selectDeployment = (id: number) => {
    // Clear deployment-scoped state synchronously BEFORE the async
    // fetch so the panel renders empty during the load window
    // instead of the previously-selected deployment's data. Without
    // this, switching from a deployment with a live release to
    // another briefly shows the previous deployment's logs / build
    // card / status — visibly confusing for ~1s.
    setSelected(id);
    setDetail(null);
    setLogs("");
    setLogTargetId(null);
    setLogKind("release");
    setMobileSigning(null);
		setMobileSigningIdentity(null);
		setSigningImportFile(null);
    setDistribution(null);
    setDistributionError("");
    setStoreState(null);
    setAudienceEmail("");
    loadDetail(id);
  };

  const handleBuild = async (release: boolean) => {
    if (!detail) return;
    setBusy(true);
    try {
      const r = await api<{ build: Build; release?: Release; release_error?: string }>(
        "POST", `/deployments/${detail.deployment.id}/build`, {
          release,
          channel: detail.deployment.target_kind === "service" ? undefined : mobileChannel,
          submit_for_review: detail.deployment.target_kind === "ios" && mobileChannel === "production" && submitForReview,
        },
      );
      // Switch log target to the freshly-created build first.
      setLogKind("build");
      setLogTargetId(r.build.id);
      if (r.release_error) setError("Release: " + r.release_error);
    } catch (e) {
      setError("Build failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const handleReleaseBuild = async (buildId: number) => {
    if (!detail) return;
    try {
      const r = await api<{ release: Release }>(
        "POST", `/deployments/${detail.deployment.id}/release`, {
          build_id: buildId,
          channel: detail.deployment.target_kind === "service" ? undefined : mobileChannel,
          submit_for_review: detail.deployment.target_kind === "ios" && mobileChannel === "production" && submitForReview,
        },
      );
      setLogKind("release");
      setLogTargetId(r.release.id);
    } catch (e) {
      setError("Release failed: " + (e as Error).message);
    }
  };

  const handleCancelBuild = async (buildId: number) => {
    if (!detail) return;
    try {
      await api("POST", `/builds/${buildId}/cancel`);
      await loadDetail(detail.deployment.id);
    } catch (e) {
      setError("Cancel build failed: " + (e as Error).message);
    }
  };

  const handlePromoteMobile = async (releaseId: number) => {
    if (!detail) return;
    setBusy(true);
    try {
      const r = await api<{ release: Release }>("POST", `/deployments/${detail.deployment.id}/promote`, {
        release_id: releaseId,
        target_channel: mobileChannel,
        submit_for_review: detail.deployment.target_kind === "ios" && mobileChannel === "production" && submitForReview,
      });
      setLogKind("release");
      setLogTargetId(r.release.id);
      loadDetail(detail.deployment.id);
    } catch (e) {
      setError("Promotion failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const handleValidateMobilePromotion = async (releaseId: number) => {
    if (!detail) return;
    setBusy(true);
    try {
      const r = await api<{ validation: MobilePromotionValidation }>("POST", `/deployments/${detail.deployment.id}/promote`, {
        release_id: releaseId,
        target_channel: mobileChannel,
        validate_only: true,
        submit_for_review: detail.deployment.target_kind === "ios" && mobileChannel === "production" && submitForReview,
      });
      setPromotionValidations((current) => ({ ...current, [releaseId]: r.validation }));
      setError("");
    } catch (e) {
      setError("Promotion validation failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const [confirmState, setConfirmState] = useState<ConfirmRequest | null>(null);

  const runMobileSigningSetup = async (rotate: boolean) => {
    if (!detail) return;
    setSigningBusy(true);
    try {
		const result = await api<{ setup: MobileSigningSetup; identity?: MobileSigningIdentity; ready: boolean; manual_actions?: string[] }>(
        "POST",
        `/deployments/${detail.deployment.id}/mobile-signing/setup`,
        { provider: detail.deployment.build_backend, rotate },
      );
		setMobileSigning(result.setup);
		if (result.identity) setMobileSigningIdentity(result.identity);
		if (result.manual_actions?.length) {
        setError(result.manual_actions.join(" "));
      } else {
        setError("");
      }
      await loadDetail(detail.deployment.id);
    } catch (e) {
      setError("Signing setup failed: " + (e as Error).message);
    } finally {
      setSigningBusy(false);
    }
  };

  const handleMobileSigningSetup = () => {
    void runMobileSigningSetup(false);
  };

	const handleMobileSigningRotation = () => {
		const platform = detail?.deployment.target_kind;
		setConfirmState({
			title: platform === "android" ? "Rotate Android upload key" : "Rotate iOS signing",
			body: platform === "android"
				? "Replace the managed Android upload key? Google Play must accept the new upload certificate before builds signed with it can be published."
				: "Create a replacement Apple distribution certificate and profile, update the build provider secrets, then revoke the previous resources?",
      confirmLabel: "Rotate",
      tone: "warning",
      onConfirm: () => runMobileSigningSetup(true),
		});
	};

	const importAndroidSigning = async () => {
		if (!detail || !signingImportFile || !signingStorePassword) return;
		setSigningBusy(true);
		try {
			const submit = async (commit: boolean) => {
				const form = new FormData();
				form.set("keystore", signingImportFile);
				form.set("store_password", signingStorePassword);
				form.set("key_password", signingKeyPassword);
				form.set("key_alias", signingKeyAlias);
				form.set("inspect_only", commit ? "false" : "true");
				form.set("confirm_replace", commit && mobileSigningIdentity ? "true" : "false");
				return fetch(`${API}/deployments/${detail.deployment.id}/mobile-signing/import?${withParams()}`, {
					method: "POST", credentials: "same-origin", body: form,
				});
			};
			const response = await submit(false);
			if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
			const preview = await response.json() as { identity: MobileSigningIdentity; replacement_required: boolean };
			setConfirmState({
				title: preview.replacement_required ? "Replace Android upload key" : "Import Android upload key",
				body: `${preview.identity.application_identifier} · alias ${preview.identity.key_alias || "1"} · SHA-1 ${preview.identity.certificate_sha1 || "-"} · SHA-256 ${preview.identity.certificate_sha256 || "-"}`,
				confirmLabel: preview.replacement_required ? "Replace keystore" : "Import keystore",
				tone: preview.replacement_required ? "warning" : undefined,
				onConfirm: async () => {
					const committed = await submit(true);
					if (!committed.ok) throw new Error(`${committed.status}: ${await committed.text().catch(() => "")}`);
					setSigningImportFile(null);
					setSigningStorePassword("");
					setSigningKeyPassword("");
					setSigningKeyAlias("");
					await loadDetail(detail.deployment.id);
					setError("");
				},
			});
		} catch (e) {
			setError("Signing import failed: " + (e as Error).message);
		} finally {
			setSigningBusy(false);
		}
	};

	const downloadSigningRecovery = () => {
		if (!detail) return;
		setConfirmState({
			title: "Export signing recovery",
			body: "Download a sensitive archive containing the private signing key and recovery credentials?",
			confirmLabel: "Download",
			tone: "warning",
			onConfirm: async () => {
				const response = await fetch(`${API}/deployments/${detail.deployment.id}/mobile-signing/recovery?${withParams()}`, {
					method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" },
					body: JSON.stringify({ confirm: true }),
				});
				if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
				const blob = await response.blob();
				const disposition = response.headers.get("Content-Disposition") || "";
				const filename = disposition.match(/filename="([^"]+)"/)?.[1] || "apteva-signing-recovery.zip";
				const url = URL.createObjectURL(blob);
				const anchor = document.createElement("a");
				anchor.href = url;
				anchor.download = filename;
				anchor.click();
				URL.revokeObjectURL(url);
			},
		});
	};

	const downloadSigningCertificate = () => {
		if (!mobileSigningIdentity?.certificate_pem) return;
		const blob = new Blob([mobileSigningIdentity.certificate_pem], { type: "application/x-pem-file" });
		const url = URL.createObjectURL(blob);
		const anchor = document.createElement("a");
		anchor.href = url;
		anchor.download = `${mobileSigningIdentity.application_identifier}-certificate.pem`;
		anchor.click();
		URL.revokeObjectURL(url);
	};

  const handleAddAudience = () => {
    if (!detail || !audienceEmail.trim() || isProductionMobileChannel(detail.deployment.target_kind, mobileChannel)) return;
		const email = audienceEmail.trim().toLowerCase();
		const kind = detail.deployment.target_kind === "android" ? "group" : "individual";
		if (!audienceDraft.some((member) => member.kind === kind && member.email.toLowerCase() === email)) {
			setAudienceDraft((current) => [...current, { kind, email }]);
		}
		setAudienceEmail("");
	};

	const handleSyncAudience = async () => {
		if (!detail || isProductionMobileChannel(detail.deployment.target_kind, mobileChannel)) return;
    setAudienceBusy(true);
    try {
      const state = await api<MobileDistributionState>(
        "PUT",
        `/deployments/${detail.deployment.id}/distribution`,
        {
          channel: mobileChannel,
			audience: audienceDraft,
			install_url: installURLDraft.trim(),
        },
      );
      setDistribution(state);
		setAudienceDraft(state.desired_audience);
		setInstallURLDraft(state.install_url || "");
      setDistributionError("");
    } catch (e) {
      setDistributionError((e as Error).message);
    } finally {
      setAudienceBusy(false);
    }
  };

  const handleReleaseApproved = async (releaseId: number) => {
    setBusy(true);
    try {
      await api("POST", `/releases/${releaseId}/release-approved`);
      if (detail) await loadDetail(detail.deployment.id);
    } catch (e) {
      setError("Release request failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const handleSyncMobileRelease = async (releaseId: number) => {
    setBusy(true);
    try {
      await api("POST", `/releases/${releaseId}/sync`);
      if (detail) await loadDetail(detail.deployment.id);
      setError("");
    } catch (e) {
      setError("Store sync failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const handleRollout = async (release: Release) => {
    const fraction = rolloutFractions[release.id] ?? releaseRolloutFraction(release);
    setBusy(true);
    try {
      await api("POST", `/releases/${release.id}/rollout`, { fraction });
      if (detail) await loadDetail(detail.deployment.id);
    } catch (e) {
      setError("Rollout update failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const handleHaltMobileRelease = (release: Release) => {
    setConfirmState({
      title: detail?.deployment.target_kind === "android" ? "Halt rollout" : "Expire TestFlight build",
      body: detail?.deployment.target_kind === "android"
        ? "Halt this Google Play production rollout?"
        : "Expire this TestFlight build? Testers will no longer be able to install it.",
      confirmLabel: "Halt",
      tone: "warning",
      onConfirm: async () => {
        await api("POST", `/releases/${release.id}/halt`);
        if (detail) await loadDetail(detail.deployment.id);
      },
    });
  };

  const handleStop = () => {
    if (!detail) return;
    setConfirmState({
      title: "Stop release",
      body: "Stop the live release? The supervised process will be terminated.",
      confirmLabel: "Stop",
      tone: "warning",
      onConfirm: async () => {
        try {
          await api("POST", `/deployments/${detail.deployment.id}/stop`);
        } catch (e) {
          setError("Stop failed: " + (e as Error).message);
        }
      },
    });
  };

  const handleRestart = async () => {
    if (!detail) return;
    setBusy(true);
    try {
      const r = await api<{ release: Release }>(
        "POST", `/deployments/${detail.deployment.id}/restart`,
      );
      setLogKind("release");
      setLogTargetId(r.release.id);
      loadHealth();
    } catch (e) {
      setError("Restart failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const handleDetachDomain = () => {
    if (!detail) return;
    setConfirmState({
      title: "Detach domain",
      body: `Remove the domain "${detail.deployment.domain}" from this deployment? The DNS record will be deleted via the Domains app.`,
      confirmLabel: "Detach",
      tone: "warning",
      onConfirm: async () => {
        try {
          await api("POST", `/deployments/${detail.deployment.id}/detach-domain`);
          loadDetail(detail.deployment.id);
        } catch (e) {
          setError("Detach failed: " + (e as Error).message);
        }
      },
    });
  };

  const handleDestroy = () => {
    if (!detail) return;
    setConfirmState({
      title: "Destroy deployment",
      body: `Destroy deployment "${detail.deployment.name}"? This stops the live release and deletes all builds and artifacts on disk. This can't be undone.`,
      confirmLabel: "Destroy",
      tone: "danger",
      onConfirm: async () => {
        try {
          await api("DELETE", `/deployments/${detail.deployment.id}`);
          setSelected(null);
          setDetail(null);
        } catch (e) {
          setError("Destroy failed: " + (e as Error).message);
        }
      },
    });
  };

  const mobile = detail?.deployment.target_kind === "android" || detail?.deployment.target_kind === "ios";
  const latestMobileRelease = mobile ? detail?.releases[0] : null;

  return (
    <div className="h-full flex">
      {/* Deployments list */}
      <aside className="w-72 border-r border-border flex flex-col">
        <div className="p-3 border-b border-border flex items-center gap-2">
          <span className="text-xs uppercase tracking-wide text-text-dim flex-1">deployments</span>
          <button
            type="button"
            onClick={() => setShowCreate(true)}
            className="px-2 py-0.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >+ New</button>
        </div>
        <div className="flex-1 overflow-auto">
          {error && <div className="p-3 text-red text-xs">{error}</div>}
          {deployments.length === 0 ? (
            <div className="p-3 text-text-muted text-sm">No deployments yet.</div>
          ) : (
            <ul>
              {deployments.map((d) => (
                <li
                  key={d.id}
                  onClick={() => selectDeployment(d.id)}
                  className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${
                    d.id === selected ? "bg-bg-input" : ""
                  }`}
                >
                  <div className="flex items-center gap-1">
                    <span className="text-sm text-text font-medium truncate flex-1">{d.name}</span>
                    {d.framework && (
                      <span className="text-[10px] px-1 py-0.5 rounded bg-blue/15 text-blue">
                        {d.target_kind || d.framework}
                      </span>
                    )}
                    {health[d.id] ? (
                      <span
                        className={`text-[10px] px-1 py-0.5 rounded ${
                          health[d.id].status === "auto_restart_paused"
                            ? "bg-yellow/20 text-yellow"
                            : "bg-red/20 text-red"
                        }`}
                        title={
                          health[d.id].status === "auto_restart_paused"
                            ? `Auto-restart paused after ${health[d.id].auto_restart.Attempts} attempts`
                            : `${health[d.id].status}${health[d.id].reason ? `: ${health[d.id].reason}` : ""}`
                        }
                      >
                        {health[d.id].status === "auto_restart_paused" ? "paused" : "!"}
                      </span>
                    ) : d.current_release_id ? (
                      <span className="text-[10px] px-1 py-0.5 rounded bg-green/15 text-green">live</span>
                    ) : (
                      <span className="text-[10px] px-1 py-0.5 rounded bg-border text-text-dim">idle</span>
                    )}
                  </div>
                  <div className="text-xs text-text-muted truncate">
                    {d.source_kind}:{d.source_ref}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </aside>

      {/* Detail */}
      <main className="flex-1 overflow-hidden flex flex-col">
        {!detail ? (
          <div className="p-8 text-text-muted text-sm text-center mt-12">
            {deployments.length === 0
              ? "Click + New to wire up your first deployment."
              : "Pick a deployment on the left."}
          </div>
        ) : (
          <>
            <header className="p-4 border-b border-border flex items-center gap-3 flex-wrap">
              <div className="flex-1 min-w-0">
                <div className="text-text font-semibold text-lg truncate">
                  {detail.deployment.name}
                </div>
                <div className="text-xs text-text-dim truncate">
                  {detail.deployment.source_kind}:{detail.deployment.source_ref}
                  {detail.deployment.framework ? ` · ${detail.deployment.framework}` : ""}
                </div>
              </div>
              {detail.url && (
                <a
                  href={detail.url}
                  target="_blank"
                  rel="noreferrer"
                  className="text-xs text-accent hover:underline truncate max-w-[260px]"
                >{detail.url} ↗</a>
              )}
              {!mobile && (detail.deployment.domain ? (
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={handleDetachDomain}
                    title={detail.deployment.domain_attached_at ? `Attached ${detail.deployment.domain_attached_at}` : "Free-text domain (no DNS managed)"}
                    className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input"
                  >
                    {detail.deployment.domain_attached_at ? "Detach" : "Clear"} {detail.deployment.domain}
                  </button>
                  {meta?.certs_available && <CertBadge status={meta.certs[detail.deployment.domain]} />}
                </div>
              ) : meta?.domains_available && (
                <button
                  type="button"
                  onClick={() => setShowAttachDomain(true)}
                  className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input"
                >+ Attach domain</button>
              ))}
              {mobile && (
                detail.deployment.target_kind === "android" ? (
                  <>
                    <input
                      list="deploy-android-tracks"
                      value={mobileChannel}
                      onChange={(event) => setMobileChannel(event.target.value.toLowerCase())}
                      className="w-28 bg-bg-input border border-border rounded px-2 py-1 text-xs"
                      title="Google Play track"
                    />
                    <datalist id="deploy-android-tracks">
                      <option value="internal" />
                      <option value="alpha" />
                      <option value="beta" />
                      <option value="production" />
                    </datalist>
                  </>
                ) : (
                  <select
                    value={mobileChannel}
                    onChange={(e) => {
                      setMobileChannel(e.target.value);
                      if (e.target.value !== "production") setSubmitForReview(false);
                    }}
                    className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
                    title="Store release channel"
                  >
                    <>
                      <option value="internal">TestFlight internal</option>
                      <option value="external">TestFlight external</option>
                      <option value="production">App Store</option>
                    </>
                  </select>
                )
              )}
              {mobile && (
                <button
                  type="button"
                  onClick={() => setShowStoreListing(true)}
                  className={`px-2 py-1 text-xs border rounded hover:bg-bg-input ${
                    storeState?.preflight.ready ? "border-green text-green" : "border-yellow text-yellow"
                  }`}
                  title={storeState?.preflight.ready
                    ? "Store listing is ready"
                    : `${storeState?.preflight.errors ?? 0} blocking listing issue(s)`}
                >
                  Store listing
                </button>
              )}
              {detail.deployment.target_kind === "ios" && mobileChannel === "production" && (
                <label className="flex items-center gap-1 text-xs text-text-muted">
                  <input
                    type="checkbox"
                    checked={submitForReview}
                    disabled={!storeState?.preflight.ready}
                    onChange={(e) => setSubmitForReview(e.target.checked)}
                  />
                  Submit review
                </label>
              )}
              <button
                type="button"
                onClick={() => handleBuild(true)}
                disabled={busy}
                className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-40"
              >{busy ? "Building…" : mobile ? "Build & Publish" : "Build & Release"}</button>
              <button
                type="button"
                onClick={() => handleBuild(false)}
                disabled={busy}
                className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-40"
              >Build only</button>
              {!mobile && detail.current_release && (
                <button
                  type="button"
                  onClick={handleRestart}
                  disabled={busy}
                  className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-40"
                  title="Stop and re-spawn the current release with whatever config the deployment row now holds. No rebuild."
                >Restart</button>
              )}
              <button
                type="button"
                onClick={() => setShowEditConfig(true)}
                className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input"
                title="Edit env, build/start commands, framework, port hint. Restart to apply without rebuilding."
              >Edit config</button>
              {!mobile && detail.current_release && detail.current_release.status === "live" && (
                <button
                  type="button"
                  onClick={handleStop}
                  className="px-2 py-1 text-xs border border-red text-red/80 rounded hover:bg-red/10"
                >Stop</button>
              )}
              <button
                type="button"
                onClick={handleDestroy}
                className="px-2 py-1 text-xs border border-red text-red/70 rounded hover:bg-red/10"
              >Destroy</button>
            </header>
            {health[detail.deployment.id] && (
              <UnhealthyBar
                entry={health[detail.deployment.id]}
                onRestart={handleRestart}
                busy={busy}
              />
            )}
            {mobile && (
              <section className="px-4 py-2 border-b border-border flex items-center gap-3 text-xs flex-wrap">
                <span className="text-text-dim uppercase">{detail.deployment.target_kind} signing</span>
                <span className={
                  mobileSigning?.status === "ready"
                    ? "text-green"
                    : mobileSigning?.status === "action_required"
                      ? "text-yellow"
                      : mobileSigning?.status === "failed"
                        ? "text-red"
                        : "text-text-muted"
                }>
                  {mobileSigning?.status || "not configured"}
                </span>
                <span className="text-text-dim truncate min-w-[12rem] flex-1">
                  {detail.deployment.build_backend}
                  {mobileSigning?.bundle_id ? ` · ${mobileSigning.bundle_id}` : ""}
                  {mobileSigning?.required_features_json
                    ? ` · requires ${mobileFeatureLabels(mobileSigning.required_features_json)}`
                    : ""}
                  {mobileSigning?.provisioned_features_json
                    ? ` · Apple ${mobileFeatureLabels(mobileSigning.provisioned_features_json)}`
                    : ""}
                  {cloudExecutionLabel(detail.deployment.build_backend_config_json, detail.deployment.build_backend)
                    ? ` · ${cloudExecutionLabel(detail.deployment.build_backend_config_json, detail.deployment.build_backend)}`
                    : ""}
                  {mobileSigning?.provider_secret_ref ? ` · secret group ${mobileSigning.provider_secret_ref}` : ""}
									{mobileSigningIdentity ? ` · identity r${mobileSigningIdentity.revision}` : ""}
                </span>
								{mobileSigningIdentity?.certificate_sha1 && (
									<span className="text-text-dim font-mono truncate max-w-[15rem]" title={mobileSigningIdentity.certificate_sha1}>
										SHA-1 {mobileSigningIdentity.certificate_sha1}
									</span>
								)}
								{mobileSigningIdentity?.certificate_sha256 && (
									<span className="text-text-dim font-mono truncate max-w-[15rem]" title={mobileSigningIdentity.certificate_sha256}>
										SHA-256 {mobileSigningIdentity.certificate_sha256}
									</span>
								)}
								{mobileSigningIdentity?.expires_at && <span className="text-text-dim">expires {mobileSigningIdentity.expires_at.slice(0, 10)}</span>}
                {mobileSigning?.last_error && (
                  <span className="text-yellow truncate" title={mobileSigning.last_error}>
                    {mobileSigning.last_error}
                  </span>
                )}
                <button
                  type="button"
                  onClick={handleMobileSigningSetup}
								disabled={signingBusy || detail.deployment.build_backend === "github_actions"}
                  className="ml-auto px-2 py-0.5 border border-border rounded hover:bg-bg-input disabled:opacity-40 shrink-0"
                  title={
									detail.deployment.build_backend !== "github_actions"
										? "Prepare the managed signing identity for this build provider."
                      : "This build provider does not yet expose a signing-secret adapter."
                  }
                >
                  {signingBusy
                    ? "Reconciling..."
                    : mobileSigning?.status === "ready"
                      ? "Repair"
                      : "Configure"}
                </button>
                {mobileSigning?.status === "ready" && (
                  <button
                    type="button"
                    onClick={handleMobileSigningRotation}
                    disabled={signingBusy}
                    className="px-2 py-0.5 border border-border rounded hover:bg-bg-input disabled:opacity-40 shrink-0"
									title={detail.deployment.target_kind === "android"
										? "Replace the managed Android upload key."
										: "Replace the Apple distribution certificate, private key, and provisioning profile."}
                  >
									Rotate key
                  </button>
                )}
								{mobileSigningIdentity && (
									<button
										type="button"
										onClick={downloadSigningRecovery}
										disabled={signingBusy}
										className="px-2 py-0.5 border border-border rounded hover:bg-bg-input disabled:opacity-40 shrink-0"
									>
										Recovery
									</button>
								)}
								{mobileSigningIdentity?.certificate_pem && (
									<button type="button" onClick={downloadSigningCertificate} className="px-2 py-0.5 border border-border rounded hover:bg-bg-input shrink-0">
										Certificate
									</button>
								)}
								{detail.deployment.target_kind === "android" && (
									<div className="basis-full flex items-center gap-2 pt-1 flex-wrap">
										<input type="file" accept=".p12,.pfx,application/x-pkcs12" onChange={(event) => setSigningImportFile(event.target.files?.[0] || null)} className="text-xs max-w-[14rem]" />
										<input type="password" value={signingStorePassword} onChange={(event) => setSigningStorePassword(event.target.value)} placeholder="Store password" className="bg-bg-input border border-border rounded px-2 py-1 w-32" />
										<input type="password" value={signingKeyPassword} onChange={(event) => setSigningKeyPassword(event.target.value)} placeholder="Key password" className="bg-bg-input border border-border rounded px-2 py-1 w-32" />
										<input value={signingKeyAlias} onChange={(event) => setSigningKeyAlias(event.target.value)} placeholder="Alias (default 1)" className="bg-bg-input border border-border rounded px-2 py-1 w-32" />
										<button type="button" onClick={() => void importAndroidSigning()} disabled={signingBusy || !signingImportFile || !signingStorePassword} className="px-2 py-1 border border-border rounded hover:bg-bg-input disabled:opacity-40">
											{mobileSigningIdentity ? "Replace keystore" : "Import keystore"}
										</button>
									</div>
								)}
              </section>
            )}
            {mobile && !isProductionMobileChannel(detail.deployment.target_kind, mobileChannel) && (
              <section className="px-4 py-2 border-b border-border flex items-center gap-3 text-xs flex-wrap">
                <span className="text-text-dim uppercase">Test audience</span>
                <span className="text-text-muted">
                  {distribution
					? `${distribution.count} provider ${detail.deployment.target_kind === "android" ? "Google group" : "tester"}${distribution.count === 1 ? "" : "s"}`
                    : "loading"}
                </span>
				{distribution && (
					<span className={distribution.synced ? "text-green" : distribution.tester_access === "sync_error" ? "text-red" : "text-yellow/80"}>
						{distribution.tester_access.replaceAll("_", " ")}
					</span>
				)}
                {distribution?.group_name && (
                  <span className="text-text-dim truncate">{distribution.group_name}</span>
                )}
				<div className="flex items-center gap-2 ml-auto min-w-0">
                  <input
                    type="email"
                    value={audienceEmail}
                    onChange={(e) => setAudienceEmail(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") {
                        e.preventDefault();
									handleAddAudience();
                      }
                    }}
                    placeholder={detail.deployment.target_kind === "android"
                      ? "Google Group email"
                      : "Tester Apple ID email"}
                    className="w-52 bg-bg-input border border-border rounded px-2 py-1 text-xs"
                  />
                  <button
                    type="button"
					onClick={handleAddAudience}
					disabled={!audienceEmail.trim()}
                    className="px-2 py-1 border border-border rounded hover:bg-bg-input disabled:opacity-40"
                  >
					Add
                  </button>
					<button
						type="button"
						onClick={handleSyncAudience}
						disabled={audienceBusy}
						className="px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-40"
					>
						{audienceBusy ? "Syncing..." : "Sync testers"}
					</button>
                </div>
                {distributionError && (
                  <span className="basis-full text-red truncate" title={distributionError}>
                    {distributionError}
                  </span>
                )}
				<div className="basis-full flex items-center gap-2 overflow-x-auto text-text-dim min-h-6">
					<span className="uppercase shrink-0">Desired</span>
					{audienceDraft.map((member) => (
						<span key={`${member.kind}:${member.email}`} className="whitespace-nowrap inline-flex items-center gap-1 border border-border rounded px-1.5 py-0.5">
							{member.email}
							{detail.deployment.target_kind === "android" && (
								<button type="button" onClick={() => setAudienceDraft((current) => current.filter((item) => item.email !== member.email || item.kind !== member.kind))} className="text-red" title="Remove Google Group">x</button>
							)}
						</span>
					))}
					{audienceDraft.length === 0 && <span>No desired testers</span>}
				</div>
				{distribution && distribution.audience.length > 0 && (
					<div className="basis-full flex items-center gap-2 overflow-x-auto text-text-dim min-h-6">
						<span className="uppercase shrink-0">Provider</span>
						{distribution.audience.map((member) => (
							<span key={`${member.kind}:${member.email}`} className="whitespace-nowrap">
								{member.email}{member.state ? ` (${member.state})` : ""}
							</span>
						))}
					</div>
				)}
				<div className="basis-full flex items-center gap-2">
					<input
						type="url"
						value={installURLDraft}
						onChange={(event) => setInstallURLDraft(event.target.value)}
						placeholder="Tester opt-in URL"
						className="flex-1 min-w-52 bg-bg-input border border-border rounded px-2 py-1 text-xs"
					/>
					<button type="button" disabled={!installURLDraft} onClick={() => void navigator.clipboard.writeText(installURLDraft)} className="px-2 py-1 border border-border rounded hover:bg-bg-input disabled:opacity-40">Copy link</button>
					{distribution?.console_url && <a href={distribution.console_url} target="_blank" rel="noreferrer" className="px-2 py-1 border border-border rounded hover:bg-bg-input">Open console</a>}
				</div>
				{detail.deployment.target_kind === "android" && (
					<span className="basis-full text-text-dim">Google Play supports Google Groups only. Individual email tester lists remain managed in Play Console.</span>
				)}
				{distribution?.last_synced_at && <span className="basis-full text-text-dim">Last synced {distribution.last_synced_at}</span>}
              </section>
            )}

            <section className="grid grid-cols-2 gap-4 p-4 border-b border-border text-xs">
              <div>
                <div className="text-text-dim uppercase mb-1">{mobile ? "Latest store release" : "Current release"}</div>
                {(mobile ? latestMobileRelease : detail.current_release) ? (
                  <div className="space-y-1">
                    <div>
                      <span className={statusColor((mobile ? latestMobileRelease! : detail.current_release!).status) + " font-medium"}>
                        {(mobile ? latestMobileRelease! : detail.current_release!).status}
                      </span>
                      {mobile ? (
                        <span className="text-text-dim">
                          {` · ${latestMobileRelease!.provider || detail.deployment.target_kind} · ${latestMobileRelease!.channel || "-"}`}
                          {latestMobileRelease!.external_status ? ` · ${latestMobileRelease!.external_status}` : ""}
                        </span>
                      ) : (
                        <span className="text-text-dim"> · port {detail.current_release!.port} · pid {detail.current_release!.pid}</span>
                      )}
                    </div>
                    <div className="text-text-dim">
                      build #{(mobile ? latestMobileRelease! : detail.current_release!).build_id}
                      {(mobile ? latestMobileRelease! : detail.current_release!).started_at && ` · started ${(mobile ? latestMobileRelease! : detail.current_release!).started_at}`}
                      {!mobile && detail.current_release!.restart_count > 0 && (
                        <span className="text-yellow/80">
                          {" · restarts: "}{detail.current_release!.restart_count}
                        </span>
                      )}
                    </div>
                    {(mobile ? latestMobileRelease! : detail.current_release!).error && (
                      <div className="text-red truncate" title={(mobile ? latestMobileRelease! : detail.current_release!).error}>
                        {(mobile ? latestMobileRelease! : detail.current_release!).error}
                      </div>
                    )}
                  </div>
                ) : (
                  <div className="text-text-muted">{mobile ? "No store release." : "No live release."}</div>
                )}
              </div>
              <div>
                <div className="text-text-dim uppercase mb-1">Latest build</div>
                {detail.builds[0] ? (
                  <div className="space-y-1">
                    <div className="flex items-baseline gap-2">
                      <span className={statusColor(detail.builds[0].status) + " font-medium"}>
                        #{detail.builds[0].id} {detail.builds[0].status}
                      </span>
                      <span className="text-text-dim">
                        · {formatDuration(detail.builds[0].duration_ms)} · {formatSize(detail.builds[0].artifact_size)}
                      </span>
                      {detail.builds[0].status === "succeeded" && (
                        <div className="ml-auto flex items-center gap-2">
                          {detail.builds[0].artifact_download_url && (
                            <a
                              href={detail.builds[0].artifact_download_url}
                              download
                              className="px-2 py-0.5 text-[11px] border border-border text-text-dim rounded hover:text-text hover:border-text-dim"
                              title="Download the retained build artifact"
                            >{artifactDownloadLabel(detail.deployment, detail.builds[0])}</a>
                          )}
                          {(mobile || detail.current_release?.build_id !== detail.builds[0].id) && (
                            <button
                              type="button"
                              onClick={() => handleReleaseBuild(detail.builds[0].id)}
                              className="px-2 py-0.5 text-[11px] border border-accent text-accent rounded hover:bg-accent hover:text-bg"
                              title="Promote this build to a live release"
                            >{mobile ? `Publish to ${mobileChannel}` : "Release this build →"}</button>
                          )}
                        </div>
                      )}
                    </div>
                    <div className="text-text-dim truncate">
                      built as: {detail.builds[0].framework}
                      {mobile && ` · version ${mobileBuildVersion(detail.builds[0])}`}
                      {detail.builds[0].build_backend !== "local"
                        && ` · ${detail.builds[0].build_backend}:${detail.builds[0].external_status || "queued"}`}
                      {detail.builds[0].framework !== detail.deployment.framework
                        && detail.deployment.framework !== ""
                        && (
                          <span className="text-yellow/80" title="Deployment's framework changed since this build — next build will use the deployment's current framework.">
                            {" "}· next build: {detail.deployment.framework}
                          </span>
                        )}
                    </div>
                    {detail.builds[0].error && (
                      <div className="text-red truncate" title={detail.builds[0].error}>
                        {detail.builds[0].error}
                      </div>
                    )}
                  </div>
                ) : (
                  <div className="text-text-muted">No builds yet.</div>
                )}
              </div>
            </section>

            <section className="px-4 py-2 border-b border-border flex items-center gap-2 text-xs">
              <span className="text-text-dim uppercase">logs</span>
              <button
                type="button"
                onClick={() => {
                  const rel = mobile ? latestMobileRelease : detail.current_release;
                  if (rel) { setLogKind("release"); setLogTargetId(rel.id); }
                }}
                className={`px-2 py-0.5 rounded border ${logKind === "release" ? "border-accent text-accent" : "border-border text-text-dim hover:bg-bg-input"}`}
              >{mobile ? "Store" : "Runtime"}</button>
              <button
                type="button"
                onClick={() => detail.builds[0] && (setLogKind("build"), setLogTargetId(detail.builds[0].id))}
                className={`px-2 py-0.5 rounded border ${logKind === "build" ? "border-accent text-accent" : "border-border text-text-dim hover:bg-bg-input"}`}
              >Latest build</button>
              <span className="text-text-dim ml-auto">
                {logKind} #{logTargetId ?? "-"}
              </span>
            </section>

            <div className="flex-1 overflow-auto bg-bg">
              <pre className="text-[11px] font-mono p-4 text-text whitespace-pre">
                {logs || (logTargetId == null ? "(no log target — build something)" : "(empty)")}
              </pre>
            </div>

            {mobile && (
              <section className="border-t border-border p-3 max-h-44 overflow-auto">
                <div className="text-xs text-text-dim uppercase mb-2">Store releases</div>
                <table className="w-full text-xs">
                  <thead className="text-text-dim">
                    <tr>
                      <th className="text-left font-normal">#</th>
                      <th className="text-left font-normal">Channel</th>
                      <th className="text-left font-normal">Status</th>
                      <th className="text-left font-normal">Store state</th>
					  <th className="text-left font-normal">Test access</th>
                      <th className="text-left font-normal">Build</th>
                      <th className="text-right font-normal">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detail.releases.map((rel) => {
                      const review = releaseReviewOutcome(rel);
                      const reviewedVersion = review?.submitted_artifact_version;
                      const latestVersion = review?.latest_artifact_version;
                      const artifactMismatch = reviewedVersion && latestVersion && reviewedVersion !== latestVersion;
                      return <tr key={rel.id} className="border-t border-border/40">
                        <td className="py-1">{rel.id}</td>
                        <td>{rel.channel || "-"}</td>
                        <td className={statusColor(rel.status)}>{rel.status}</td>
						<td className="text-text-dim" title={review?.sync_error || review?.action_required || ""}>
							<div>{review?.item_state || review?.submission_state || rel.external_status || "-"}</div>
							{reviewedVersion && (
								<div className={artifactMismatch ? "text-yellow" : "text-text-dim"}>
									reviewed {reviewedVersion}{artifactMismatch ? ` · latest ${latestVersion}` : ""}
								</div>
							)}
						</td>
						<td className="text-text-dim">
							{isProductionMobileChannel(detail.deployment.target_kind, rel.channel) ? "-" : (() => {
								const access = releaseTesterAccess(rel);
								return access.installURL
									? <a href={access.installURL} target="_blank" rel="noreferrer" className="text-accent hover:underline">{access.count} · install</a>
									: `${access.count} · ${access.status.replaceAll("_", " ")}`;
							})()}
						</td>
                        <td>{rel.build_id}</td>
                        <td className="text-right space-x-2">
                          <button type="button" onClick={() => { setLogKind("release"); setLogTargetId(rel.id); }} className="text-text-dim hover:text-text">log</button>
                          <button type="button" disabled={busy} onClick={() => handleSyncMobileRelease(rel.id)} className="text-text-dim hover:text-text disabled:opacity-40">sync</button>
						  {review?.provider_console_url && (review.action_required || review.sync_error) && (
							<a href={review.provider_console_url} target="_blank" rel="noreferrer" className="text-accent hover:underline">review</a>
						  )}
                          {rel.status !== "failed" && rel.channel !== mobileChannel && (
                            <>
                              <button type="button" disabled={busy} onClick={() => handleValidateMobilePromotion(rel.id)} className="text-text-dim hover:text-text disabled:opacity-40">validate</button>
                              <button type="button" disabled={busy} onClick={() => handlePromoteMobile(rel.id)} className="text-accent hover:underline disabled:opacity-40">promote</button>
                            </>
                          )}
                          {promotionValidations[rel.id] && (
                            <span className={promotionValidations[rel.id].valid ? "text-green" : "text-red"} title={promotionValidations[rel.id].provider_error || `${promotionValidations[rel.id].preflight.errors} blocking findings · ${promotionValidations[rel.id].production_access}`}>
                              {promotionValidations[rel.id].valid ? "ready" : "blocked"}
                            </span>
                          )}
                          {(rel.external_status === "approved_pending_release" || rel.external_status === "pending_apple_release") && (
                            <button
                              type="button"
                              disabled={busy}
                              onClick={() => handleReleaseApproved(rel.id)}
                              className="text-green hover:underline disabled:opacity-40"
                            >release</button>
                          )}
                          {detail.deployment.target_kind === "android" && rel.channel === "production" && rel.status !== "failed" && (
                            <span className="inline-flex items-center gap-1">
                              <input
                                type="range"
                                min={1}
                                max={100}
                                value={Math.round((rolloutFractions[rel.id] ?? releaseRolloutFraction(rel)) * 100)}
                                onChange={(e) => setRolloutFractions((current) => ({
                                  ...current,
                                  [rel.id]: Number(e.target.value) / 100,
                                }))}
                                className="w-20"
                                title="Production rollout percentage"
                              />
                              <button type="button" disabled={busy} onClick={() => handleRollout(rel)} className="text-accent hover:underline disabled:opacity-40">
                                {Math.round((rolloutFractions[rel.id] ?? releaseRolloutFraction(rel)) * 100)}%
                              </button>
                              <button type="button" disabled={busy} onClick={() => handleHaltMobileRelease(rel)} className="text-red hover:underline disabled:opacity-40">halt</button>
                            </span>
                          )}
                          {detail.deployment.target_kind === "ios" && rel.channel !== "production" && rel.status === "live" && (
                            <button type="button" disabled={busy} onClick={() => handleHaltMobileRelease(rel)} className="text-red hover:underline disabled:opacity-40">expire</button>
                          )}
                        </td>
                      </tr>;
                    })}
                  </tbody>
                </table>
              </section>
            )}

            <section className="border-t border-border p-3 max-h-44 overflow-auto">
              <div className="text-xs text-text-dim uppercase mb-2">Builds</div>
              <table className="w-full text-xs">
                <thead className="text-text-dim">
                  <tr>
                    <th className="text-left font-normal">#</th>
                    <th className="text-left font-normal">Status</th>
                    {mobile && <th className="text-left font-normal">Version</th>}
                    <th className="text-left font-normal">Duration</th>
                    <th className="text-left font-normal">Size</th>
                    <th className="text-left font-normal">Created</th>
                    <th className="text-right font-normal">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {detail.builds.map((b) => (
                    <tr key={b.id} className="border-t border-border/40">
                      <td className="py-1">{b.id}</td>
                      <td className={statusColor(b.status)}>{b.status}</td>
                      {mobile && <td className="text-text-dim">{mobileBuildVersion(b)}</td>}
                      <td>{formatDuration(b.duration_ms)}</td>
                      <td>{formatSize(b.artifact_size)}</td>
                      <td className="text-text-dim truncate">{b.created_at}</td>
                      <td className="text-right space-x-2">
                        <button
                          type="button"
                          onClick={() => { setLogKind("build"); setLogTargetId(b.id); }}
                          className="text-text-dim hover:text-text"
                        >log</button>
                        {b.artifact_download_url && (
                          <a
                            href={b.artifact_download_url}
                            download
                            className="text-text-dim hover:text-text"
                            title="Download the retained build artifact"
                          >{artifactDownloadLabel(detail.deployment, b).toLowerCase()}</a>
                        )}
                        {b.status === "succeeded" && (
                          <button
                            type="button"
                            onClick={() => handleReleaseBuild(b.id)}
                            className="text-accent hover:underline"
                          >{mobile ? "publish" : "release"}</button>
                        )}
                        {(b.status === "pending" || b.status === "running") && b.build_backend !== "local" && (
                          <button
                            type="button"
                            onClick={() => handleCancelBuild(b.id)}
                            className="text-red hover:underline"
                          >cancel</button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </section>
          </>
        )}
      </main>

      {showCreate && (
        <CreateDeploymentDialog
          onClose={() => setShowCreate(false)}
          onCreated={(d) => {
            setShowCreate(false);
            loadDeployments().then(() => selectDeployment(d.id));
          }}
          api={api}
          projectId={projectId}
          meta={meta}
        />
      )}

      {showAttachDomain && detail && (
        <AttachDomainDialog
          deployment={detail.deployment}
          meta={meta}
          onClose={() => setShowAttachDomain(false)}
          onAttached={() => {
            setShowAttachDomain(false);
            loadDetail(detail.deployment.id);
          }}
          api={api}
        />
      )}

      {showStoreListing && detail && mobile && storeState && (
        <StoreListingDialog
          deployment={detail.deployment}
          initial={storeState}
          projectId={projectId}
          installId={installId}
          api={api}
          onClose={() => setShowStoreListing(false)}
          onSaved={(next) => {
            setStoreState(next);
            setSubmitForReview((current) => current && next.preflight.ready);
          }}
        />
      )}

      {showEditConfig && detail && (
        <EditConfigDialog
          deployment={detail.deployment}
          hasCurrentRelease={!!detail.current_release}
          onClose={() => setShowEditConfig(false)}
          onSaved={(restarted) => {
            setShowEditConfig(false);
            loadDetail(detail.deployment.id);
            if (restarted) loadHealth();
          }}
          api={api}
        />
      )}

      {confirmState && (
        <ConfirmDialog
          request={confirmState}
          onClose={() => setConfirmState(null)}
        />
      )}
    </div>
  );
}

// ─── ConfirmDialog ─────────────────────────────────────────────────
//
// Drop-in replacement for window.confirm: a centred modal with title,
// body, and two action buttons. The "danger" tone is used for
// destructive actions (Destroy); "warning" for reversible interrupts
// (Stop). Spawned by setting confirmState — the dialog clears its own
// state through onClose so callers don't have to.

interface ConfirmRequest {
  title: string;
  body: string;
  confirmLabel: string;
  tone?: "warning" | "danger";
  onConfirm: () => void | Promise<void>;
}

function ConfirmDialog({ request, onClose }: { request: ConfirmRequest; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const accent =
    request.tone === "danger"
      ? "bg-red text-white hover:bg-red/90"
      : "bg-blue text-white hover:bg-blue/90";

  const submit = async () => {
    setBusy(true);
    try {
      await request.onConfirm();
      onClose();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-[420px] bg-bg border border-border rounded p-5 space-y-4"
        role="dialog"
        aria-modal="true"
      >
        <h2 className="text-text font-semibold">{request.title}</h2>
        <p className="text-text-muted text-sm">{request.body}</p>
        <div className="flex justify-end gap-2 pt-1">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1.5 text-sm rounded border border-border text-text-muted hover:text-text disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={busy}
            autoFocus
            className={`px-3 py-1.5 text-sm rounded ${accent} disabled:opacity-50`}
          >
            {busy ? "Working…" : request.confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

interface CodeRepo {
  slug: string;
  name?: string;
  framework?: string;
  archived?: boolean;
}

function CreateDeploymentDialog({
  onClose,
  onCreated,
  api,
  projectId,
  meta,
}: {
  onClose: () => void;
  onCreated: (d: Deployment) => void;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
  projectId: string;
  meta: MetaInfo | null;
}) {
  const [name, setName] = useState("");
  const [targetKind, setTargetKind] = useState<(typeof TARGET_KINDS)[number]>("service");
  const [sourceKind, setSourceKind] = useState<(typeof SOURCE_KINDS)[number]>("code");
  const [sourceRef, setSourceRef] = useState("");
  const [framework, setFramework] = useState<(typeof FRAMEWORKS)[number]>("");
  const [buildCmd, setBuildCmd] = useState("");
  const [buildBackend, setBuildBackend] = useState<(typeof BUILD_BACKENDS)[number]>("local");
  const [buildBackendConfig, setBuildBackendConfig] = useState("{}");
  const [startCmd, setStartCmd] = useState("");
  const [env, setEnv] = useState("");
  const [targetConfig, setTargetConfig] = useState("");
  const [domainApex, setDomainApex] = useState("");
  const [domainSub, setDomainSub] = useState("");
  const [domainText, setDomainText] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const [repos, setRepos] = useState<CodeRepo[] | null>(null);
  const [reposErr, setReposErr] = useState("");
  useEffect(() => {
    if (sourceKind !== "code") return;
    if (repos !== null) return;
    let cancelled = false;
    fetch(`/api/apps/code/api/repos?project_id=${encodeURIComponent(projectId)}`, {
      credentials: "same-origin",
    })
      .then(async (r) => {
        if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
        return r.json() as Promise<{ repositories?: CodeRepo[] }>;
      })
      .then((j) => {
        if (cancelled) return;
        setRepos((j.repositories || []).filter((r) => !r.archived));
      })
      .catch((e) => {
        if (cancelled) return;
        setReposErr((e as Error).message);
        setRepos([]);
      });
    return () => {
      cancelled = true;
    };
  }, [sourceKind, projectId, repos]);

  const submit = async () => {
    if (!name.trim() || !sourceRef.trim()) {
      setErr("name and source_ref required");
      return;
    }
    const backendConfig = buildBackendConfig.trim() || "{}";
    try {
      const parsed = JSON.parse(backendConfig);
      if (typeof parsed !== "object" || Array.isArray(parsed) || parsed === null) {
        setErr("Build backend config must be a JSON object");
        return;
      }
    } catch (e) {
      setErr("Build backend config: " + (e as Error).message);
      return;
    }
    const targetJSON = targetConfig.trim() || "{}";
    if (targetKind !== "service") {
      try {
        const parsed = JSON.parse(targetJSON) as Record<string, unknown>;
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("must be an object");
        if (targetKind === "android" && !String(parsed.package_name || "").trim()) throw new Error("package_name is required");
        if (targetKind === "ios" && !String(parsed.bundle_id || "").trim()) throw new Error("bundle_id is required");
      } catch (e) {
        setErr("Mobile target config: " + (e as Error).message);
        return;
      }
    }
    setBusy(true);
    try {
      let domain = "";
      if (meta?.domains_available && domainApex) {
        domain = domainSub.trim() ? `${domainSub.trim()}.${domainApex}` : domainApex;
      } else if (!meta?.domains_available && domainText.trim()) {
        domain = domainText.trim();
      }
      const r = await api<{ deployment: Deployment; domain_error?: string }>("POST", "/deployments", {
        name: name.trim(),
        target_kind: targetKind,
        source_kind: sourceKind,
        source_ref: sourceRef.trim(),
        framework,
        build_cmd: buildCmd.trim(),
        build_backend: buildBackend,
        build_backend_config_json: backendConfig,
        start_cmd: startCmd.trim(),
        env_json: env.trim() || "{}",
        target_config_json: targetJSON,
        domain: targetKind === "service" ? domain : "",
      });
      if (r.domain_error) {
        // Deployment was created; only the DNS step failed. Surface
        // it without aborting so the user can retry attach later.
        setErr("Created, but domain attach failed: " + r.domain_error);
        return;
      }
      onCreated(r.deployment);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <form
        onClick={(e) => e.stopPropagation()}
        onSubmit={(e) => { e.preventDefault(); submit(); }}
        className="w-[520px] max-w-[94vw] max-h-[90vh] overflow-y-auto bg-bg border border-border rounded p-5 space-y-4"
      >
        <h2 className="text-text font-semibold">New deployment</h2>
        <div className="grid grid-cols-2 gap-3">
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">Name (slug)</label>
            <input
              autoFocus
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="my-api"
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">Target</label>
            <select
              value={targetKind}
              onChange={(e) => {
                const next = e.target.value as (typeof TARGET_KINDS)[number];
                setTargetKind(next);
                setFramework(next === "service" ? "" : next);
              }}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              {TARGET_KINDS.map((kind) => <option key={kind} value={kind}>{kind}</option>)}
            </select>
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">Source</label>
            <select
              value={sourceKind}
              onChange={(e) => setSourceKind(e.target.value as (typeof SOURCE_KINDS)[number])}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              {SOURCE_KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
            </select>
          </div>
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">Framework</label>
            <select
              value={framework}
              onChange={(e) => setFramework(e.target.value as (typeof FRAMEWORKS)[number])}
              disabled={targetKind !== "service"}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              {FRAMEWORKS.map((f) => (
                <option key={f} value={f}>{f === "" ? "(auto-detect)" : f}</option>
              ))}
            </select>
          </div>
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">Build backend</label>
            <select
              value={buildBackend}
              onChange={(e) => setBuildBackend(e.target.value as (typeof BUILD_BACKENDS)[number])}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              <option value="local">Local</option>
              <option value="runner">Capsule runner</option>
              <option value="codemagic">Codemagic</option>
              <option value="github_actions">GitHub Actions</option>
            </select>
          </div>
          {buildBackend !== "local" && (
            <div className="col-span-2">
              <label className="text-xs text-text-muted block mb-1">Build backend config (JSON)</label>
              <textarea
                value={buildBackendConfig}
                onChange={(e) => setBuildBackendConfig(e.target.value)}
                placeholder={buildBackend === "codemagic"
                  ? '{"app_id":"...","workflow_id":"apteva-mobile-capsule","branch":"main","source_mode":"bundle","artifact_mode":"file"}'
                  : '{"owner":"acme","repo":"app","workflow_id":"build.yml","ref":"main","artifact_mode":"file"}'}
                rows={4}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
              />
            </div>
          )}
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">
              Source ref ({sourceKind === "code" ? "repository from Code app" : "absolute path on host"})
            </label>
            {sourceKind === "code" ? (
              repos === null ? (
                <div className="w-full text-xs text-text-dim px-2 py-1.5">Loading repositories…</div>
              ) : repos.length === 0 ? (
                <div className="text-xs text-text-dim space-y-1">
                  <div>{reposErr ? `Couldn't load repos: ${reposErr}` : "No repositories in this project yet."}</div>
                  <div>Open the Code panel to create one, then return here.</div>
                </div>
              ) : (
                <select
                  value={sourceRef}
                  onChange={(e) => {
                    setSourceRef(e.target.value);
                    if (framework === "") {
                      const r = repos.find((x) => x.slug === e.target.value);
                      const f = (r?.framework || "").toLowerCase();
                      if ((FRAMEWORKS as readonly string[]).includes(f)) {
                        setFramework(f as (typeof FRAMEWORKS)[number]);
                      }
                    }
                  }}
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                >
                  <option value="">— select a repository —</option>
                  {repos.map((r) => (
                    <option key={r.slug} value={r.slug}>
                      {r.slug}
                      {r.framework ? ` · ${r.framework}` : ""}
                      {r.name && r.name !== r.slug ? ` (${r.name})` : ""}
                    </option>
                  ))}
                </select>
              )
            ) : (
              <input
                type="text"
                value={sourceRef}
                onChange={(e) => setSourceRef(e.target.value)}
                placeholder="/abs/path/to/repo"
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
              />
            )}
          </div>
          <div className={targetKind === "service" ? "" : "col-span-2"}>
            <label className="text-xs text-text-muted block mb-1">Build cmd (optional)</label>
            <input
              type="text"
              value={buildCmd}
              onChange={(e) => setBuildCmd(e.target.value)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>
          {targetKind === "service" && <div>
            <label className="text-xs text-text-muted block mb-1">Start cmd (optional)</label>
            <input
              type="text"
              value={startCmd}
              onChange={(e) => setStartCmd(e.target.value)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>}
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">Env (JSON object, optional)</label>
            <textarea
              value={env}
              onChange={(e) => setEnv(e.target.value)}
              placeholder='{"LOG_LEVEL":"info"}'
              rows={2}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>
          {targetKind !== "service" && (
            <div className="col-span-2">
              <MobileTargetFields targetKind={targetKind} value={targetConfig} onChange={setTargetConfig} />
              <details className="mt-3 text-xs text-text-muted">
                <summary className="cursor-pointer">Advanced mobile target JSON</summary>
                <textarea
                  value={targetConfig}
                  onChange={(e) => setTargetConfig(e.target.value)}
                  rows={5}
                  className="mt-2 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                />
              </details>
            </div>
          )}
          {targetKind === "service" && <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">
              Domain (optional)
              {meta?.domains_available
                ? meta.public_host
                  ? <span className="text-text-dim"> · CNAME → {meta.public_host}</span>
                  : <span className="text-yellow"> · public_host not configured</span>
                : <span className="text-text-dim"> · free-text (Domains app not installed)</span>}
            </label>
            {meta?.domains_available ? (
              meta.domains.length === 0 ? (
                <div className="text-xs text-text-dim">No domains registered. Open the Domains panel to add one.</div>
              ) : (
                <div className="flex gap-2">
                  <input
                    type="text"
                    value={domainSub}
                    onChange={(e) => setDomainSub(e.target.value)}
                    placeholder="app (or empty for apex)"
                    className="w-32 bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                  />
                  <span className="self-center text-text-dim">.</span>
                  <select
                    value={domainApex}
                    onChange={(e) => setDomainApex(e.target.value)}
                    className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                  >
                    <option value="">— pick a domain —</option>
                    {meta.domains.map((d) => (
                      <option key={d.name} value={d.name}>{d.name}</option>
                    ))}
                  </select>
                </div>
              )
            ) : (
              <input
                type="text"
                value={domainText}
                onChange={(e) => setDomainText(e.target.value)}
                placeholder="app.acme.com"
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
              />
            )}
          </div>}
        </div>
        {err && <div className="text-red text-xs">{err}</div>}
        <div className="flex items-center justify-end gap-2 pt-2">
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
          >Cancel</button>
          <button
            type="submit"
            disabled={busy}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Creating…" : "Create"}</button>
        </div>
      </form>
    </div>
  );
}

// ─── AttachDomainDialog ───────────────────────────────────────────
//
// Picker for an existing deployment: choose a registered apex +
// subdomain, optionally override target/type, submit. Surfaces the
// same domain_error path as deploy_init when DNS write fails.

function AttachDomainDialog({
  deployment,
  meta,
  onClose,
  onAttached,
  api,
}: {
  deployment: Deployment;
  meta: MetaInfo | null;
  onClose: () => void;
  onAttached: () => void;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
}) {
  const [apex, setApex] = useState("");
  const [sub, setSub] = useState("");
  const [target, setTarget] = useState("");
  const [recordType, setRecordType] = useState<"CNAME" | "A">("CNAME");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const canSubmit = !!apex && (recordType === "CNAME" ? !!sub : true);

  const submit = async () => {
    if (!canSubmit) {
      setErr(recordType === "CNAME" ? "Apex CNAME isn't allowed; pick a subdomain or switch to A." : "Pick a domain.");
      return;
    }
    setBusy(true);
    try {
      const fqdn = sub.trim() ? `${sub.trim()}.${apex}` : apex;
      await api("POST", `/deployments/${deployment.id}/attach-domain`, {
        fqdn,
        target: target.trim(),
        type: recordType,
      });
      onAttached();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <form
        onClick={(e) => e.stopPropagation()}
        onSubmit={(e) => { e.preventDefault(); submit(); }}
        className="w-[480px] bg-bg border border-border rounded p-5 space-y-4"
      >
        <h2 className="text-text font-semibold">Attach domain</h2>
        {(!meta || meta.domains.length === 0) ? (
          <div className="text-xs text-text-dim">No domains registered. Open the Domains panel to add one first.</div>
        ) : (
          <>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="text-xs text-text-muted block mb-1">Type</label>
                <select
                  value={recordType}
                  onChange={(e) => setRecordType(e.target.value as "CNAME" | "A")}
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                >
                  <option value="CNAME">CNAME</option>
                  <option value="A">A</option>
                </select>
              </div>
              <div>
                <label className="text-xs text-text-muted block mb-1">Apex domain</label>
                <select
                  value={apex}
                  onChange={(e) => setApex(e.target.value)}
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                >
                  <option value="">— pick —</option>
                  {meta.domains.map((d) => (
                    <option key={d.name} value={d.name}>{d.name}</option>
                  ))}
                </select>
              </div>
              <div className="col-span-2">
                <label className="text-xs text-text-muted block mb-1">
                  Subdomain {recordType === "CNAME" && <span className="text-text-dim">(required for CNAME)</span>}
                </label>
                <input
                  type="text"
                  value={sub}
                  onChange={(e) => setSub(e.target.value)}
                  placeholder="app"
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                />
              </div>
              <div className="col-span-2">
                <label className="text-xs text-text-muted block mb-1">
                  Target
                  <span className="text-text-dim">
                    {" · "}
                    {meta.public_host
                      ? `defaults to public_host: ${meta.public_host}`
                      : "public_host not configured — required"}
                  </span>
                </label>
                <input
                  type="text"
                  value={target}
                  onChange={(e) => setTarget(e.target.value)}
                  placeholder={recordType === "A" ? "1.2.3.4" : "edge.acme.com"}
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                />
              </div>
            </div>
            {err && <div className="text-red text-xs">{err}</div>}
          </>
        )}
        <div className="flex items-center justify-end gap-2 pt-2">
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
          >Cancel</button>
          <button
            type="submit"
            disabled={busy || !canSubmit || !meta || meta.domains.length === 0}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Attaching…" : "Attach"}</button>
        </div>
      </form>
    </div>
  );
}

function StoreListingDialog({
  deployment,
  initial,
  projectId,
  installId,
  api,
  onClose,
  onSaved,
}: {
  deployment: Deployment;
  initial: StoreConfigState;
  projectId: string;
  installId: number;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
  onClose: () => void;
  onSaved: (next: StoreConfigState) => void;
}) {
  const [doc, setDoc] = useState<StoreDocument>(() => structuredClone(initial.desired));
  const [applyResult, setApplyResult] = useState<StoreApplyResult | null>(null);
  const [preflight, setPreflight] = useState(initial.preflight);
  const [tab, setTab] = useState<"listing" | "media" | "review" | "compliance" | "distribution">("listing");
  const [locale, setLocale] = useState(initial.desired.default_locale || "en-US");
  const [newLocale, setNewLocale] = useState("");
  const [assetKind, setAssetKind] = useState("phone_screenshot");
  const [displayTarget, setDisplayTarget] = useState(deployment.target_kind === "ios" ? "APP_IPHONE_69" : "");
  const [reviewPassword, setReviewPassword] = useState("");
  const [hasConfig, setHasConfig] = useState(Boolean(initial.config));
  const [readiness, setReadiness] = useState(() => storeReadiness(initial.config?.observed_json));
  const [ageJSON, setAgeJSON] = useState(
    JSON.stringify(initial.desired.classification.age_declaration || {}, null, 2),
  );
  const [providerExtensionsJSON, setProviderExtensionsJSON] = useState(
    JSON.stringify(initial.desired.provider_extensions || {}, null, 2),
  );
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const localization = doc.localizations[locale] || {};
  const updateLocalization = (patch: Partial<StoreLocalization>) => {
    setDoc((current) => ({
      ...current,
      localizations: {
        ...current.localizations,
        [locale]: { ...(current.localizations[locale] || {}), ...patch },
      },
    }));
  };

  const normalize = (): StoreDocument => {
    let age: Record<string, unknown>;
    try {
      age = JSON.parse(ageJSON || "{}") as Record<string, unknown>;
      if (!age || typeof age !== "object" || Array.isArray(age)) throw new Error("must be an object");
    } catch (e) {
      throw new Error("Age declaration JSON: " + (e as Error).message);
    }
    let providerExtensions: Record<string, unknown>;
    try {
      providerExtensions = JSON.parse(providerExtensionsJSON || "{}") as Record<string, unknown>;
      if (!providerExtensions || typeof providerExtensions !== "object" || Array.isArray(providerExtensions)) {
        throw new Error("must be an object");
      }
    } catch (e) {
      throw new Error("Provider extensions JSON: " + (e as Error).message);
    }
    return {
      ...doc,
      schema_version: 1,
      version_name: doc.version_name.trim(),
      default_locale: doc.default_locale || locale,
      classification: { ...doc.classification, age_declaration: age },
      provider_extensions: providerExtensions,
    };
  };

  const save = async (): Promise<StoreConfigState> => {
    const desired = normalize();
    const next = await api<StoreConfigState>("PUT", `/deployments/${deployment.id}/store-config`, desired);
    setDoc(structuredClone(next.desired));
    setPreflight(next.preflight);
    setReadiness(storeReadiness(next.config?.observed_json));
    setHasConfig(true);
    onSaved(next);
    return next;
  };

  const handleSave = async () => {
    setBusy(true);
    setErr("");
    try {
      await save();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const handleApply = async () => {
    setBusy(true);
    setErr("");
    try {
      const saved = await save();
      const result = await api<StoreApplyResult>("POST", `/deployments/${deployment.id}/store-apply`, {
        scopes: ["version", "localizations", "media", "review", "classification", "privacy", "distribution", "testing", "compliance"],
        allow_partial: true,
        review_demo_password: reviewPassword || undefined,
      });
      setApplyResult(result);
      setReviewPassword("");
      const next = await api<StoreConfigState>("GET", `/deployments/${deployment.id}/store-config`);
      setDoc(structuredClone(next.desired));
      setPreflight(next.preflight);
      setReadiness(storeReadiness(next.config?.observed_json));
      onSaved(next);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const handleSync = async () => {
    setBusy(true);
    setErr("");
    try {
      await api("POST", `/deployments/${deployment.id}/store-sync`);
      const next = await api<StoreConfigState>("GET", `/deployments/${deployment.id}/store-config`);
      setDoc(structuredClone(next.desired));
      setPreflight(next.preflight);
      setReadiness(storeReadiness(next.config?.observed_json));
      onSaved(next);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const uploadAsset = async (file: File) => {
    setBusy(true);
    setErr("");
    try {
      const form = new FormData();
      form.set("file", file);
      form.set("locale", locale);
      form.set("kind", assetKind);
      form.set("display_target", displayTarget);
      form.set("order", String(doc.assets.length + 1));
      const query = new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
      });
      const response = await fetch(
        `${API}/deployments/${deployment.id}/store-assets?${query.toString()}`,
        { method: "POST", credentials: "same-origin", body: form },
      );
      if (!response.ok) throw new Error(`${response.status}: ${await response.text()}`);
      const result = await response.json() as { asset: StoreAsset };
      setDoc((current) => ({ ...current, assets: [...current.assets, result.asset] }));
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const addLocale = () => {
    const value = newLocale.trim();
    if (!value) return;
    setDoc((current) => ({
      ...current,
      localizations: { ...current.localizations, [value]: current.localizations[value] || {} },
    }));
    setLocale(value);
    setNewLocale("");
  };

  const tabs = [
    ["listing", "Listing"],
    ["media", "Media"],
    ["review", "Review"],
    ["compliance", "Compliance"],
    ["distribution", "Distribution"],
  ] as const;

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-[820px] max-w-[94vw] h-[86vh] bg-bg border border-border rounded flex flex-col overflow-hidden"
      >
        <header className="px-5 py-3 border-b border-border flex flex-wrap items-center gap-3">
          <div className="min-w-0 flex-1">
            <h2 className="text-text font-semibold">Store listing</h2>
            <div className="text-xs text-text-dim">
              {deployment.target_kind === "ios" ? "App Store Connect" : "Google Play"}
              {initial.config?.status
                ? ` · ${initial.config.desired_hash === initial.config.applied_hash && initial.config.applied_hash ? "applied and verified" : initial.config.status}`
                : " · not configured"}
            </div>
          </div>
          <span className={`text-xs ${preflight.ready ? "text-green" : "text-yellow"}`}>
            {preflight.ready ? "Ready" : `${preflight.errors} blocking · ${preflight.warnings} warning`}
          </span>
          <button type="button" onClick={onClose} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input">
            Close
          </button>
        </header>

        <nav className="px-5 border-b border-border flex items-center gap-1 overflow-x-auto shrink-0">
          {tabs.map(([value, label]) => (
            <button
              key={value}
              type="button"
              onClick={() => setTab(value)}
              className={`px-3 py-2 text-xs border-b-2 ${
                tab === value ? "border-accent text-accent" : "border-transparent text-text-dim hover:text-text"
              }`}
            >{label}</button>
          ))}
        </nav>

        <div className="flex-1 overflow-auto p-5">
          {preflight.findings.length > 0 && (
            <section className="mb-5 border-b border-border pb-4 space-y-1">
              {preflight.findings.map((finding) => (
                <div key={`${finding.code}:${finding.locale || ""}`} className="flex gap-2 text-xs">
                  <span className={finding.severity === "error" ? "text-red" : "text-yellow"}>
                    {finding.severity}
                  </span>
                  <span className="text-text">{finding.message}</span>
                  {finding.locale && <span className="text-text-dim">{finding.locale}</span>}
                </div>
              ))}
            </section>
          )}

          {tab === "listing" && (
            <div className="space-y-5">
              <section className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                <label className="text-xs text-text-muted">
                  Store version
                  <input
                    value={doc.version_name}
                    onChange={(e) => setDoc({ ...doc, version_name: e.target.value })}
                    placeholder="1.0"
                    className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                  />
                </label>
                <label className="text-xs text-text-muted">
                  Default locale
                  <select
                    value={doc.default_locale}
                    onChange={(e) => setDoc({ ...doc, default_locale: e.target.value })}
                    className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                  >
                    {Object.keys(doc.localizations).map((value) => <option key={value}>{value}</option>)}
                  </select>
                </label>
                <label className="text-xs text-text-muted">
                  {deployment.target_kind === "ios" ? "Release mode" : "Production rollout"}
                  <select
                    value={doc.release_mode}
                    onChange={(e) => setDoc({ ...doc, release_mode: e.target.value })}
                    className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                  >
                    {deployment.target_kind === "ios" ? (
                      <>
                        <option value="manual">Manual</option>
                        <option value="after_approval">After approval</option>
                        <option value="scheduled">Scheduled</option>
                      </>
                    ) : (
                      <>
                        <option value="immediate">Immediate</option>
                        <option value="staged">Staged rollout</option>
                      </>
                    )}
                  </select>
                </label>
              </section>

              {deployment.target_kind === "ios" && (
                <section className="grid grid-cols-1 sm:grid-cols-3 gap-3 border-t border-border pt-4">
                  <TextField label="Copyright" value={doc.copyright} onChange={(copyright) => setDoc({ ...doc, copyright })} />
                  <label className="flex items-center gap-2 text-xs text-text-muted">
                    <input
                      type="checkbox"
                      checked={Boolean(doc.content_rights?.uses_third_party_content)}
                      onChange={(e) => setDoc({
                        ...doc,
                        content_rights: { ...doc.content_rights, uses_third_party_content: e.target.checked },
                      })}
                    />
                    Uses third-party content
                  </label>
                  <label className="flex items-center gap-2 text-xs text-text-muted">
                    <input
                      type="checkbox"
                      checked={Boolean(doc.content_rights?.rights_confirmed)}
                      onChange={(e) => setDoc({
                        ...doc,
                        content_rights: { ...doc.content_rights, rights_confirmed: e.target.checked },
                      })}
                    />
                    Content rights confirmed
                  </label>
                </section>
              )}

              <section className="border-t border-border pt-4">
                <div className="flex items-center gap-2 mb-3">
                  <select
                    value={locale}
                    onChange={(e) => setLocale(e.target.value)}
                    className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
                  >
                    {Object.keys(doc.localizations).map((value) => <option key={value}>{value}</option>)}
                  </select>
                  <input
                    value={newLocale}
                    onChange={(e) => setNewLocale(e.target.value)}
                    placeholder="fr-FR"
                    className="w-24 bg-bg-input border border-border rounded px-2 py-1 text-xs"
                  />
                  <button type="button" onClick={addLocale} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input">
                    Add locale
                  </button>
                </div>
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <TextField label="Title" value={localization.title} onChange={(title) => updateLocalization({ title })} />
                  <TextField
                    label={deployment.target_kind === "ios" ? "Subtitle" : "Short description"}
                    value={deployment.target_kind === "ios" ? localization.subtitle : localization.short_description}
                    onChange={(value) => updateLocalization(deployment.target_kind === "ios"
                      ? { subtitle: value }
                      : { short_description: value })}
                  />
                  <label className="sm:col-span-2 text-xs text-text-muted">
                    Description
                    <textarea
                      rows={6}
                      value={localization.description || ""}
                      onChange={(e) => updateLocalization({ description: e.target.value })}
                      className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                    />
                  </label>
                  {deployment.target_kind === "ios" && (
                    <TextField
                      label="Keywords"
                      value={(localization.keywords || []).join(", ")}
                      onChange={(value) => updateLocalization({ keywords: value.split(",").map((item) => item.trim()).filter(Boolean) })}
                    />
                  )}
                  <TextField label="What's new" value={localization.whats_new} onChange={(whats_new) => updateLocalization({ whats_new })} />
                  <TextField label="Support URL" value={localization.support_url} onChange={(support_url) => updateLocalization({ support_url })} />
                  <TextField label="Marketing URL" value={localization.marketing_url} onChange={(marketing_url) => updateLocalization({ marketing_url })} />
                  <TextField label="Promotional text" value={localization.promotional_text} onChange={(promotional_text) => updateLocalization({ promotional_text })} />
                  {deployment.target_kind === "android" && (
                    <TextField label="Promo video URL" value={localization.video_url} onChange={(video_url) => updateLocalization({ video_url })} />
                  )}
                </div>
              </section>
            </div>
          )}

          {tab === "media" && (
            <div className="space-y-4">
              <section className="flex flex-wrap items-end gap-3">
                <label className="text-xs text-text-muted">
                  Asset type
                  <select
                    value={assetKind}
                    onChange={(e) => {
                      const kind = e.target.value;
                      setAssetKind(kind);
                      if (deployment.target_kind === "ios") {
                        setDisplayTarget(kind === "tablet_screenshot" ? "APP_IPAD_PRO_13" : kind === "app_preview" ? "IPHONE_67" : "APP_IPHONE_69");
                      } else {
                        setDisplayTarget(kind === "tablet_screenshot" ? "tablet_7" : "");
                      }
                    }}
                    className="mt-1 block bg-bg-input border border-border rounded px-2 py-1 text-sm"
                  >
                    <option value="phone_screenshot">Phone screenshot</option>
                    <option value="tablet_screenshot">Tablet screenshot</option>
                    {deployment.target_kind === "ios" && <option value="app_preview">App preview</option>}
                    {deployment.target_kind === "ios" && <option value="review_attachment">Review attachment</option>}
                    {deployment.target_kind === "android" && <option value="icon">App icon</option>}
                    {deployment.target_kind === "android" && <option value="feature_graphic">Feature graphic</option>}
                    {deployment.target_kind === "android" && <option value="tv_screenshot">TV screenshot</option>}
                    {deployment.target_kind === "android" && <option value="wear_screenshot">Wear screenshot</option>}
                  </select>
                </label>
                <label className="text-xs text-text-muted">
                  Display target
                  {deployment.target_kind === "ios" && assetKind !== "review_attachment" ? (
                    <select
                      value={displayTarget}
                      onChange={(e) => setDisplayTarget(e.target.value)}
                      className="mt-1 block w-52 bg-bg-input border border-border rounded px-2 py-1 text-sm"
                    >
                      {assetKind === "tablet_screenshot" ? (
                        <>
                          <option value="APP_IPAD_PRO_13">iPad 13-inch</option>
                          <option value="APP_IPAD_PRO_3GEN_129">iPad Pro 12.9-inch</option>
                        </>
                      ) : assetKind === "app_preview" ? (
                        <option value="IPHONE_67">iPhone 6.7-inch preview</option>
                      ) : (
                        <>
                          <option value="APP_IPHONE_69">iPhone 6.9-inch</option>
                          <option value="APP_IPHONE_67">iPhone 6.7-inch</option>
                          <option value="APP_IPHONE_65">iPhone 6.5-inch</option>
                          <option value="APP_IPHONE_63">iPhone 6.3-inch</option>
                          <option value="APP_IPHONE_61">iPhone 6.1-inch</option>
                        </>
                      )}
                    </select>
                  ) : deployment.target_kind === "android" && assetKind === "tablet_screenshot" ? (
                    <select
                      value={displayTarget || "tablet_7"}
                      onChange={(e) => setDisplayTarget(e.target.value)}
                      className="mt-1 block w-44 bg-bg-input border border-border rounded px-2 py-1 text-sm"
                    >
                      <option value="tablet_7">7-inch tablet</option>
                      <option value="tablet_10">10-inch tablet</option>
                    </select>
                  ) : (
                    <span className="mt-1 block text-xs text-text-dim">Selected automatically</span>
                  )}
                </label>
                <label className="px-3 py-1.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg cursor-pointer">
                  Upload
                  <input
                    type="file"
                    accept={assetKind === "app_preview"
                      ? "video/mp4,video/quicktime"
                      : assetKind === "review_attachment"
                        ? "image/png,image/jpeg,application/pdf"
                        : "image/png,image/jpeg"}
                    multiple
                    className="hidden"
                    onChange={(e) => {
                      for (const file of Array.from(e.target.files || [])) void uploadAsset(file);
                      e.currentTarget.value = "";
                    }}
                  />
                </label>
              </section>
              <table className="w-full text-xs">
                <thead className="text-text-dim">
                  <tr>
                    <th className="text-left font-normal">Locale</th>
                    <th className="text-left font-normal">Type</th>
                    <th className="text-left font-normal">Target</th>
                    <th className="text-left font-normal">File</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {doc.assets.map((asset) => (
                    <tr key={asset.id} className="border-t border-border">
                      <td className="py-2">{asset.locale}</td>
                      <td>{asset.kind}</td>
                      <td className="text-text-dim">{asset.display_target || "-"}</td>
                      <td className="text-text-dim truncate max-w-64">
                        {asset.path.split("/").at(-1)}
                        {asset.width && asset.height ? ` · ${asset.width} × ${asset.height}` : ""}
                      </td>
                      <td className="text-right">
                        <button
                          type="button"
                          onClick={() => setDoc({ ...doc, assets: doc.assets.filter((candidate) => candidate.id !== asset.id) })}
                          className="text-red hover:underline"
                        >remove</button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {tab === "review" && (
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <TextField label="First name" value={doc.review.first_name} onChange={(first_name) => setDoc({ ...doc, review: { ...doc.review, first_name } })} />
              <TextField label="Last name" value={doc.review.last_name} onChange={(last_name) => setDoc({ ...doc, review: { ...doc.review, last_name } })} />
              <TextField label="Email" value={doc.review.email} onChange={(email) => setDoc({ ...doc, review: { ...doc.review, email } })} />
              <TextField label="Phone" value={doc.review.phone} onChange={(phone) => setDoc({ ...doc, review: { ...doc.review, phone } })} />
              <label className="sm:col-span-2 text-xs text-text-muted">
                Review notes
                <textarea
                  rows={5}
                  value={doc.review.notes || ""}
                  onChange={(e) => setDoc({ ...doc, review: { ...doc.review, notes: e.target.value } })}
                  className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                />
              </label>
              <label className="sm:col-span-2 flex items-center gap-2 text-xs text-text-muted">
                <input
                  type="checkbox"
                  checked={Boolean(doc.review.demo_account_required)}
                  onChange={(e) => setDoc({ ...doc, review: { ...doc.review, demo_account_required: e.target.checked } })}
                />
                Review requires login
              </label>
              {doc.review.demo_account_required && (
                <>
                  <TextField label="Demo username" value={doc.review.demo_username} onChange={(demo_username) => setDoc({ ...doc, review: { ...doc.review, demo_username } })} />
                  {deployment.target_kind === "ios" ? (
                      <label className="text-xs text-text-muted">
                        Demo password {doc.review.demo_password_set && <span className="text-green">configured</span>}
                        <input
                          type="password"
                          value={reviewPassword}
                          onChange={(e) => setReviewPassword(e.target.value)}
                          placeholder={doc.review.demo_password_set ? "Leave blank to keep" : "Sent once during Apply"}
                          className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                        />
                      </label>
                  ) : (
                    <label className="flex items-center gap-2 text-xs text-text-muted">
                      <input
                        type="checkbox"
                        checked={Boolean(doc.review.credentials_confirmed)}
                        onChange={(e) => setDoc({ ...doc, review: { ...doc.review, credentials_confirmed: e.target.checked } })}
                      />
                      Reusable credentials configured
                    </label>
                  )}
                </>
              )}
              {deployment.target_kind === "android" && (
                <>
                  <label className="sm:col-span-2 text-xs text-text-muted">
                    App access instructions
                    <textarea
                      rows={4}
                      value={doc.review.access_instructions || ""}
                      onChange={(e) => setDoc({ ...doc, review: { ...doc.review, access_instructions: e.target.value } })}
                      className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                    />
                  </label>
                  <label className="sm:col-span-2 flex items-center gap-2 text-xs text-text-muted">
                    <input
                      type="checkbox"
                      checked={Boolean(doc.review.access_confirmed)}
                      onChange={(e) => setDoc({ ...doc, review: { ...doc.review, access_confirmed: e.target.checked } })}
                    />
                    App access completed in Play Console
                  </label>
                </>
              )}
            </div>
          )}

          {tab === "compliance" && (
            <div className="space-y-4">
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                <TextField label="Privacy policy URL" value={doc.privacy.policy_url} onChange={(policy_url) => setDoc({ ...doc, privacy: { ...doc.privacy, policy_url } })} />
                <TextField label="Privacy choices URL" value={doc.privacy.choices_url} onChange={(choices_url) => setDoc({ ...doc, privacy: { ...doc.privacy, choices_url } })} />
                <label className="text-xs text-text-muted">
                  Primary category
                  <select
                    value={doc.classification.primary_category || ""}
                    onChange={(e) => setDoc({ ...doc, classification: { ...doc.classification, primary_category: e.target.value } })}
                    className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                  >
                    <option value="">Select category</option>
                    {(deployment.target_kind === "ios" ? APPLE_CATEGORIES : GOOGLE_CATEGORIES).map((category) => (
                      <option key={category} value={category}>{category.replaceAll("_", " ")}</option>
                    ))}
                  </select>
                </label>
                {deployment.target_kind === "ios" && (
                  <label className="text-xs text-text-muted">
                    Secondary category
                    <select
                      value={doc.classification.secondary_category || ""}
                      onChange={(e) => setDoc({ ...doc, classification: { ...doc.classification, secondary_category: e.target.value } })}
                      className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                    >
                      <option value="">None</option>
                      {APPLE_CATEGORIES.map((category) => <option key={category} value={category}>{category.replaceAll("_", " ")}</option>)}
                    </select>
                  </label>
                )}
              </div>
              <section className="border-t border-border pt-4 space-y-3">
                <div className="text-xs uppercase text-text-dim">Content rating</div>
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  {RATING_FIELDS.map(({ key, label }) => (
                    <label key={key} className="text-xs text-text-muted">
                      {label}
                      <select
                        value={currentRatingLevel(doc.classification.content_rating?.[key])}
                        onChange={(e) => setDoc({
                          ...doc,
                          classification: {
                            ...doc.classification,
                            content_rating: {
                              ...doc.classification.content_rating,
                              [key]: e.target.value as StoreRatingLevel,
                            },
                          },
                        })}
                        className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                      >
                        <option value="">Select</option>
                        <option value="NONE">None</option>
                        <option value="INFREQUENT">Infrequent</option>
                        <option value="FREQUENT">Frequent</option>
                      </select>
                    </label>
                  ))}
                </div>
                <div className="flex flex-wrap gap-4">
                  {([
                    ["unrestricted_web_access", "Unrestricted web access"],
                    ["real_money_gambling", "Real-money gambling"],
                    ["loot_boxes", "Loot boxes"],
                    ["advertising", "Advertising"],
                    ["messaging_chat", "Messaging and chat"],
                    ["user_generated_content", "User-generated content"],
                    ["parental_controls", "Parental controls"],
                    ["age_assurance", "Age assurance"],
                    ["social_media", "Social media"],
                    ["social_media_age_gate", "Social media restricted under 13"],
                  ] as const).map(([key, label]) => (
                    <label key={key} className="flex items-center gap-2 text-xs text-text-muted">
                      <input
                        type="checkbox"
                        checked={Boolean(doc.classification.content_rating?.[key])}
                        onChange={(e) => setDoc({
                          ...doc,
                          classification: {
                            ...doc.classification,
                            content_rating: { ...doc.classification.content_rating, [key]: e.target.checked },
                          },
                        })}
                      />
                      {label}
                    </label>
                  ))}
                </div>
                {deployment.target_kind === "ios" && (
                  <details className="text-xs text-text-muted">
                    <summary className="cursor-pointer">Advanced Apple declaration fields</summary>
                    <textarea value={ageJSON} onChange={(e) => setAgeJSON(e.target.value)} rows={6} className="mt-2 w-full bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono" />
                  </details>
                )}
              </section>
              {deployment.target_kind === "ios" ? (
                <>
                  <label className="flex items-center gap-2 text-xs text-text-muted">
                    <input
                      type="checkbox"
                      checked={Boolean(doc.privacy.manual_attestations?.apple_privacy_published)}
                      onChange={(e) => setDoc({
                        ...doc,
                        privacy: {
                          ...doc.privacy,
                          manual_attestations: { ...doc.privacy.manual_attestations, apple_privacy_published: e.target.checked },
                        },
                      })}
                    />
                    App Privacy already reviewed and published (optional confirmation)
                  </label>
                </>
              ) : (
                <>
                  <label className="text-xs text-text-muted block">
                    Google Data Safety CSV
                    <textarea
                      value={doc.privacy.data_safety_csv || ""}
                      onChange={(e) => setDoc({ ...doc, privacy: { ...doc.privacy, data_safety_csv: e.target.value } })}
                      rows={8}
                      className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"
                    />
                  </label>
                  <label className="flex items-center gap-2 text-xs text-text-muted">
                    <input
                      type="checkbox"
                      checked={Boolean(doc.privacy.manual_attestations?.google_data_safety_published)}
                      onChange={(e) => setDoc({
                        ...doc,
                        privacy: {
                          ...doc.privacy,
                          manual_attestations: { ...doc.privacy.manual_attestations, google_data_safety_published: e.target.checked },
                        },
                      })}
                    />
                    Existing Play Data Safety declaration verified
                  </label>
                  <label className="flex items-center gap-2 text-xs text-text-muted">
                    <input
                      type="checkbox"
                      checked={Boolean(doc.privacy.manual_attestations?.google_app_content_complete)}
                      onChange={(e) => setDoc({
                        ...doc,
                        privacy: {
                          ...doc.privacy,
                          manual_attestations: { ...doc.privacy.manual_attestations, google_app_content_complete: e.target.checked },
                        },
                      })}
                    />
                    Play category, content rating, and App Content complete
                  </label>
                </>
              )}
            </div>
          )}

          {tab === "distribution" && (
            <div className="space-y-4">
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                <label className="text-xs text-text-muted">
                  Availability
                  <select
                    value={doc.distribution.availability?.mode || (doc.distribution.territories?.length ? "only" : "")}
                    onChange={(e) => setDoc({ ...doc, distribution: {
                      ...doc.distribution,
                      availability: { ...doc.distribution.availability, mode: e.target.value as "all" | "all_except" | "only" },
                    } })}
                    className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                  >
                    <option value="">Select availability</option>
                    <option value="all">All storefronts</option>
                    <option value="all_except">All except selected</option>
                    <option value="only">Only selected</option>
                  </select>
                </label>
                {(doc.distribution.availability?.mode === "all_except" || doc.distribution.availability?.mode === "only" || (!doc.distribution.availability?.mode && doc.distribution.territories?.length)) && (
                  <TextField
                    label={doc.distribution.availability?.mode === "all_except" ? "Excluded territories" : "Included territories"}
                    value={(doc.distribution.availability?.mode === "all_except"
                      ? doc.distribution.availability?.excluded_territories
                      : doc.distribution.availability?.included_territories || doc.distribution.territories || []).join(", ")}
                    onChange={(value) => {
                      const territories = value.split(",").map((item) => item.trim().toUpperCase()).filter(Boolean);
                      const mode = doc.distribution.availability?.mode || "only";
                      setDoc({ ...doc, distribution: { ...doc.distribution, territories: undefined, availability: {
                        ...doc.distribution.availability,
                        mode,
                        ...(mode === "all_except" ? { excluded_territories: territories } : { included_territories: territories }),
                      } } });
                    }}
                  />
                )}
                <TextField label="Price point / tier" value={doc.distribution.price_tier} onChange={(price_tier) => setDoc({ ...doc, distribution: { ...doc.distribution, price_tier } })} />
                <label className="flex items-center gap-2 text-xs text-text-muted">
                  <input
                    type="checkbox"
                    checked={Boolean(doc.distribution.phased_release)}
                    onChange={(e) => setDoc({ ...doc, distribution: { ...doc.distribution, phased_release: e.target.checked } })}
                  />
                  Phased release
                </label>
                {deployment.target_kind === "android" && doc.release_mode === "staged" && (
                  <label className="text-xs text-text-muted">
                    Initial rollout percent
                    <input
                      type="number"
                      min={1}
                      max={99}
                      value={Math.round((doc.distribution.rollout_fraction || 0.1) * 100)}
                      onChange={(e) => setDoc({
                        ...doc,
                        distribution: { ...doc.distribution, rollout_fraction: Number(e.target.value) / 100 },
                      })}
                      className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                    />
                  </label>
                )}
                {doc.release_mode === "scheduled" && (
                  <label className="text-xs text-text-muted">
                    Earliest release
                    <input
                      type="datetime-local"
                      value={(doc.earliest_release_at || "").slice(0, 16)}
                      onChange={(e) => setDoc({ ...doc, earliest_release_at: e.target.value ? new Date(e.target.value).toISOString() : "" })}
                      className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                    />
                  </label>
                )}
                <label className="flex items-center gap-2 text-xs text-text-muted" title={readiness.availability?.message}>
                  <input
                    type="checkbox"
                    disabled={readiness.availability?.status === "verified"}
                    checked={readiness.availability?.status === "verified" || Boolean(doc.distribution.provider?.availability_configured)}
                    onChange={(e) => setDoc({
                      ...doc,
                      distribution: {
                        ...doc.distribution,
                        provider: { ...doc.distribution.provider, availability_configured: e.target.checked },
                      },
                    })}
                  />
                  {readiness.availability?.status === "verified" ? "Availability verified by provider" : "Existing store availability verified manually"}
                </label>
                <label className="flex items-center gap-2 text-xs text-text-muted" title={readiness.pricing?.message}>
                  <input
                    type="checkbox"
                    disabled={readiness.pricing?.status === "verified"}
                    checked={readiness.pricing?.status === "verified" || Boolean(doc.distribution.provider?.pricing_configured)}
                    onChange={(e) => setDoc({
                      ...doc,
                      distribution: {
                        ...doc.distribution,
                        provider: { ...doc.distribution.provider, pricing_configured: e.target.checked },
                      },
                    })}
                  />
                  {readiness.pricing?.status === "verified" ? "Pricing verified by provider" : "Existing store pricing verified manually"}
                </label>
              </div>
              <details className="text-xs text-text-muted">
                <summary className="cursor-pointer">Advanced provider extensions</summary>
                <textarea
                  value={providerExtensionsJSON}
                  onChange={(e) => setProviderExtensionsJSON(e.target.value)}
                  rows={9}
                  className="mt-2 w-full bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"
                />
              </details>
            </div>
          )}
        </div>

        <footer className="px-5 py-3 border-t border-border flex flex-wrap items-center gap-2">
          {err && <span className="text-xs text-red flex-1 truncate" title={err}>{err}</span>}
          {!err && applyResult && <span className="flex-1 text-xs text-text-dim">
            {applyResult.applied_scopes.length} scopes applied
            {(applyResult.applied_assets?.length || 0) > 0 ? ` · ${applyResult.applied_assets!.join(", ")}` : ""}
            {` · ${applyResult.blocked.length} blocked · ${applyResult.failed.length} failed`}
          </span>}
          {!err && !applyResult && <span className="flex-1 text-xs text-text-dim">Saving does not submit or release the app.</span>}
          <button type="button" onClick={handleSync} disabled={busy || !hasConfig} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-40">
            Sync
          </button>
          <button type="button" onClick={handleSave} disabled={busy} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-40">
            Save draft
          </button>
          <button type="button" onClick={handleApply} disabled={busy} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-40">
            {busy ? "Working..." : "Apply to store"}
          </button>
        </footer>
      </div>
    </div>
  );
}

function TextField({ label, value, onChange }: { label: string; value?: string; onChange: (value: string) => void }) {
  return (
    <label className="text-xs text-text-muted">
      {label}
      <input
        value={value || ""}
        onChange={(e) => onChange(e.target.value)}
        className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
      />
    </label>
  );
}

function parseMobileTargetJSON(raw: string): Record<string, unknown> {
  try {
    const value = JSON.parse(raw.trim() || "{}");
    return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
  } catch {
    return {};
  }
}

function patchMobileTargetJSON(raw: string, patch: Record<string, unknown>): string {
  const value = parseMobileTargetJSON(raw);
  if (!value.version_strategy) value.version_strategy = "auto";
  for (const [key, next] of Object.entries(patch)) {
    if (next === "" || next === undefined || next === null || (Array.isArray(next) && next.length === 0)) {
      delete value[key];
    } else {
      value[key] = next;
    }
  }
  return JSON.stringify(value, null, 2);
}

function MobileTargetFields({
  targetKind,
  value,
  onChange,
}: {
  targetKind: "android" | "ios";
  value: string;
  onChange: (value: string) => void;
}) {
  const config = parseMobileTargetJSON(value);
  const strategy = String(config.version_strategy || "auto");
  const set = (patch: Record<string, unknown>) => onChange(patchMobileTargetJSON(value, patch));
  const families = Array.isArray(config.device_families) ? config.device_families.map(String) : [];
  const toggleFamily = (family: string, checked: boolean) => {
    const next = checked ? [...new Set([...families, family])] : families.filter((value) => value !== family);
    set({ device_families: next });
  };

  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
      {targetKind === "android" ? (
        <>
          <TextField label="Package name" value={String(config.package_name || "")} onChange={(package_name) => set({ package_name })} />
          <TextField label="Marketing version" value={String(config.version_name || "")} onChange={(version_name) => set({ version_name })} />
          <TextField label="Gradle module" value={String(config.module || "app")} onChange={(module) => set({ module })} />
          <TextField label="Build variant" value={String(config.variant || "release")} onChange={(variant) => set({ variant })} />
        </>
      ) : (
        <>
          <TextField label="Bundle ID" value={String(config.bundle_id || "")} onChange={(bundle_id) => set({ bundle_id })} />
          <TextField label="Xcode scheme" value={String(config.scheme || "")} onChange={(scheme) => set({ scheme })} />
          <TextField label="Marketing version" value={String(config.version_name || "")} onChange={(version_name) => set({ version_name })} />
          <TextField label="Apple Team ID" value={String(config.team_id || "")} onChange={(team_id) => set({ team_id })} />
          <TextField label="App Store app ID" value={String(config.app_store_app_id || "")} onChange={(app_store_app_id) => set({ app_store_app_id })} />
          <div className="flex items-end gap-4 pb-1">
            {(["iphone", "ipad"] as const).map((family) => (
              <label key={family} className="flex items-center gap-2 text-xs text-text-muted">
                <input type="checkbox" checked={families.includes(family)} onChange={(e) => toggleFamily(family, e.target.checked)} />
                {family === "iphone" ? "iPhone" : "iPad"}
              </label>
            ))}
          </div>
        </>
      )}
      <label className="text-xs text-text-muted">
        Version allocation
        <select value={strategy} onChange={(e) => set({ version_strategy: e.target.value })} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm">
          <option value="auto">Automatic</option>
          <option value="manual">Manual</option>
        </select>
      </label>
      {strategy === "manual" && (
        <TextField
          label={targetKind === "ios" ? "Build number" : "Version code"}
          value={String(config[targetKind === "ios" ? "build_number" : "version_code"] || "")}
          onChange={(next) => set({ [targetKind === "ios" ? "build_number" : "version_code"]: next })}
        />
      )}
    </div>
  );
}

// ─── CertBadge ────────────────────────────────────────────────────
//
// One-glance cert status next to an attached domain. The Certs app
// owns issuance state — this is purely a render of the meta payload.

function CertBadge({ status }: { status?: CertStatus }) {
  if (!status) {
    return <span className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text-dim">no cert</span>;
  }
  const cls =
    status.status === "live"     ? "bg-green/15 text-green" :
    status.status === "issuing"  ? "bg-blue/15 text-blue" :
    status.status === "pending"  ? "bg-blue/15 text-blue" :
    status.status === "failed"   ? "bg-red/15 text-red" :
                                   "bg-border text-text-dim";
  const label =
    status.status === "live"    ? `cert · expires ${formatExpires(status.expires_at)}` :
    status.status === "issuing" ? "cert · issuing…" :
    status.status === "pending" ? "cert · pending" :
    status.status === "failed"  ? "cert · failed" :
                                  `cert · ${status.status}`;
  return (
    <span
      className={`text-[10px] px-1.5 py-0.5 rounded ${cls}`}
      title={status.error || (status.expires_at ? `expires ${status.expires_at}` : "")}
    >
      {label}
    </span>
  );
}

function formatExpires(iso?: string): string {
  if (!iso) return "?";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "?";
  return d.toISOString().slice(0, 10);
}

// ─── UnhealthyBar ─────────────────────────────────────────────────
//
// Inline warning rendered above the detail body when /api/health
// flagged the currently-selected deployment. Yellow for "paused"
// (operator decision needed — auto-restart gave up), red for the
// active failure modes.

function UnhealthyBar({
  entry, onRestart, busy,
}: {
  entry: UnhealthyEntry;
  onRestart: () => void;
  busy: boolean;
}) {
  const paused = entry.status === "auto_restart_paused";
  const tone = paused
    ? "bg-yellow/10 border-yellow/40 text-yellow"
    : "bg-red/10 border-red/40 text-red";
  const label =
    entry.status === "auto_restart_paused"
      ? `Auto-restart paused after ${entry.auto_restart.Attempts} attempt${entry.auto_restart.Attempts === 1 ? "" : "s"}. Fix the config and restart manually to clear.`
      : entry.status === "starting_stuck"
        ? `Release stuck in "starting" for ${formatDurationSeconds(entry.unhealthy_for_s)} — pid never owned the port.`
        : `Release ${entry.status}${entry.reason ? `: ${entry.reason}` : ""} (${formatDurationSeconds(entry.unhealthy_for_s)} ago).`;
  return (
    <div className={`px-4 py-2 text-xs border-b flex items-center gap-3 ${tone}`}>
      <span className="font-medium">⚠</span>
      <span className="flex-1">{label}</span>
      {entry.auto_restart.Attempts > 0 && (
        <span className="text-text-dim">
          attempts: {entry.auto_restart.Attempts}
        </span>
      )}
      <button
        type="button"
        onClick={onRestart}
        disabled={busy}
        className="px-2 py-0.5 text-[11px] border border-current rounded hover:bg-current/10 disabled:opacity-40"
      >Restart now</button>
    </div>
  );
}

function formatDurationSeconds(s: number): string {
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

// ─── EditConfigDialog ─────────────────────────────────────────────
//
// PATCH /deployments/:id for the allowlisted mutable fields. Two
// submit modes: "Save" updates the row only (applies on next build);
// "Save & restart" PATCHes then POSTs /restart so the new env /
// start_cmd / port_hint takes effect immediately without rebuilding.
// env_json is a raw JSON textarea — same shape the create dialog
// uses — to keep parity with the agent-facing tool.

function EditConfigDialog({
  deployment,
  hasCurrentRelease,
  onClose,
  onSaved,
  api,
}: {
  deployment: Deployment;
  hasCurrentRelease: boolean;
  onClose: () => void;
  onSaved: (restarted: boolean) => void;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
}) {
  const [description, setDescription] = useState(deployment.description ?? "");
  const [framework, setFramework] = useState<(typeof FRAMEWORKS)[number]>(
    (FRAMEWORKS as readonly string[]).includes(deployment.framework)
      ? (deployment.framework as (typeof FRAMEWORKS)[number])
      : "",
  );
  const [buildCmd, setBuildCmd] = useState(deployment.build_cmd ?? "");
  const [buildBackend, setBuildBackend] = useState<(typeof BUILD_BACKENDS)[number]>(
    deployment.build_backend ?? "local",
  );
  const [buildBackendConfig, setBuildBackendConfig] = useState(
    deployment.build_backend_config_json ?? "{}",
  );
  const [startCmd, setStartCmd] = useState(deployment.start_cmd ?? "");
  const [portHint, setPortHint] = useState(
    deployment.port_hint ? String(deployment.port_hint) : "",
  );
  const [envJSON, setEnvJSON] = useState(deployment.env_json ?? "");
  const [targetConfigJSON, setTargetConfigJSON] = useState(deployment.target_config_json ?? "{}");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (restart: boolean) => {
    setErr("");
    // Validate env_json client-side — server allowlist accepts the
    // string verbatim and the running process gets it next release.
    // A typo in the textarea silently means "no env"; bail early.
    const env = envJSON.trim();
    if (env !== "" && env !== "{}") {
      try {
        const parsed = JSON.parse(env);
        if (typeof parsed !== "object" || Array.isArray(parsed) || parsed === null) {
          setErr("env_json must be a JSON object");
          return;
        }
      } catch (e) {
        setErr("env_json: " + (e as Error).message);
        return;
      }
    }
    const port = portHint.trim();
    let portN = 0;
    if (port !== "") {
      portN = Number(port);
      if (!Number.isFinite(portN) || portN < 0 || portN > 65535 || Math.floor(portN) !== portN) {
        setErr("port_hint must be an integer 0-65535");
        return;
      }
    }
    const backendConfig = buildBackendConfig.trim() || "{}";
    try {
      const parsed = JSON.parse(backendConfig);
      if (typeof parsed !== "object" || Array.isArray(parsed) || parsed === null) {
        setErr("build_backend_config_json must be a JSON object");
        return;
      }
    } catch (e) {
      setErr("build_backend_config_json: " + (e as Error).message);
      return;
    }
    setBusy(true);
    try {
      // Diff against current values — sending only-changed keys keeps
      // the deploy.updated event payload accurate (which downstream
      // observers may key off).
      const body: Record<string, string | number> = {};
      if (description !== (deployment.description ?? "")) body.description = description;
      if (framework !== deployment.framework) body.framework = framework;
      if (buildCmd !== (deployment.build_cmd ?? "")) body.build_cmd = buildCmd;
      if (buildBackend !== (deployment.build_backend ?? "local")) body.build_backend = buildBackend;
      if (backendConfig !== (deployment.build_backend_config_json ?? "{}")) {
        body.build_backend_config_json = backendConfig;
      }
      if (startCmd !== (deployment.start_cmd ?? "")) body.start_cmd = startCmd;
      if (portN !== deployment.port_hint) body.port_hint = portN;
      if (env !== (deployment.env_json ?? "")) body.env_json = env;
      const targetConfig = targetConfigJSON.trim() || "{}";
      try {
        const parsed = JSON.parse(targetConfig);
        if (typeof parsed !== "object" || Array.isArray(parsed) || parsed === null) {
          setErr("target_config_json must be a JSON object");
          return;
        }
      } catch (e) {
        setErr("target_config_json: " + (e as Error).message);
        return;
      }
      if (targetConfig !== (deployment.target_config_json ?? "{}")) body.target_config_json = targetConfig;

      if (Object.keys(body).length > 0) {
        await api("PATCH", `/deployments/${deployment.id}`, body);
      }
      if (restart && hasCurrentRelease) {
        await api("POST", `/deployments/${deployment.id}/restart`);
      }
      onSaved(restart && hasCurrentRelease);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <form
        onClick={(e) => e.stopPropagation()}
        onSubmit={(e) => { e.preventDefault(); submit(false); }}
        className="w-[520px] max-w-[94vw] max-h-[90vh] overflow-y-auto bg-bg border border-border rounded p-5 space-y-4"
      >
        <h2 className="text-text font-semibold">Edit deployment config</h2>
        <p className="text-xs text-text-dim">
          Changes apply on the next build/release.
          {hasCurrentRelease && " Use “Save & restart” to apply immediately without rebuilding."}
        </p>
        <div className="grid grid-cols-2 gap-3">
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">Description</label>
            <input
              type="text"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">Framework</label>
            <select
              value={framework}
              onChange={(e) => setFramework(e.target.value as (typeof FRAMEWORKS)[number])}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              {FRAMEWORKS.map((f) => (
                <option key={f} value={f}>{f === "" ? "(auto-detect)" : f}</option>
              ))}
            </select>
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">
              Port hint <span className="text-text-dim">(0 = auto)</span>
            </label>
            <input
              type="number"
              min={0}
              max={65535}
              value={portHint}
              onChange={(e) => setPortHint(e.target.value)}
              placeholder="0"
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">Build cmd</label>
            <input
              type="text"
              value={buildCmd}
              onChange={(e) => setBuildCmd(e.target.value)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">Build backend</label>
            <select
              value={buildBackend}
              onChange={(e) => setBuildBackend(e.target.value as (typeof BUILD_BACKENDS)[number])}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              <option value="local">Local</option>
              <option value="runner">Capsule runner</option>
              <option value="codemagic">Codemagic</option>
              <option value="github_actions">GitHub Actions</option>
            </select>
          </div>
          {buildBackend !== "local" && (
            <div className="col-span-2">
              <label className="text-xs text-text-muted block mb-1">Build backend config (JSON)</label>
              <textarea
                value={buildBackendConfig}
                onChange={(e) => setBuildBackendConfig(e.target.value)}
                rows={4}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
              />
            </div>
          )}
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">Start cmd</label>
            <input
              type="text"
              value={startCmd}
              onChange={(e) => setStartCmd(e.target.value)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">
              Env <span className="text-text-dim">(JSON object)</span>
            </label>
            <textarea
              value={envJSON}
              onChange={(e) => setEnvJSON(e.target.value)}
              placeholder='{"LOG_LEVEL":"info"}'
              rows={5}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>
          {deployment.target_kind !== "service" && (
            <div className="col-span-2">
              <MobileTargetFields targetKind={deployment.target_kind} value={targetConfigJSON} onChange={setTargetConfigJSON} />
              <details className="mt-3 text-xs text-text-muted">
                <summary className="cursor-pointer">Advanced mobile target JSON</summary>
                <textarea
                  value={targetConfigJSON}
                  onChange={(e) => setTargetConfigJSON(e.target.value)}
                  rows={6}
                  className="mt-2 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                />
              </details>
            </div>
          )}
        </div>
        {err && <div className="text-red text-xs">{err}</div>}
        <div className="flex items-center justify-end gap-2 pt-2">
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
          >Cancel</button>
          <button
            type="submit"
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
          >{busy ? "Saving…" : "Save"}</button>
          {hasCurrentRelease && (
            <button
              type="button"
              onClick={() => submit(true)}
              disabled={busy}
              className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
            >{busy ? "Working…" : "Save & restart"}</button>
          )}
        </div>
      </form>
    </div>
  );
}
