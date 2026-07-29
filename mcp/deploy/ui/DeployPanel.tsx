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
}

interface StoreFinding {
  code: string;
  severity: "error" | "warning" | "info";
  scope: string;
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
}

interface StoreDocument {
  schema_version: number;
  version_name: string;
  default_locale: string;
  release_mode: string;
  earliest_release_at?: string;
  copyright?: string;
  uses_idfa?: boolean;
  localizations: Record<string, StoreLocalization>;
  assets: StoreAsset[];
  review: {
    first_name?: string;
    last_name?: string;
    email?: string;
    phone?: string;
    notes?: string;
    demo_account_required?: boolean;
    demo_username?: string;
    demo_password_set?: boolean;
  };
  classification: {
    primary_category?: string;
    secondary_category?: string;
    age_declaration?: Record<string, unknown>;
  };
  distribution: {
    territories?: string[];
    price_tier?: string;
    phased_release?: boolean;
    rollout_fraction?: number;
    provider?: Record<string, unknown>;
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
    last_error?: string;
  } | null;
  desired: StoreDocument;
  preflight: StorePreflight;
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
  const [signingBusy, setSigningBusy] = useState(false);
  const [distribution, setDistribution] = useState<MobileDistributionState | null>(null);
  const [distributionError, setDistributionError] = useState("");
  const [audienceEmail, setAudienceEmail] = useState("");
  const [audienceBusy, setAudienceBusy] = useState(false);
  const [storeState, setStoreState] = useState<StoreConfigState | null>(null);
  const [showStoreListing, setShowStoreListing] = useState(false);

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
        if (d.deployment.target_kind === "ios") {
          const signing = await api<{ setups?: MobileSigningSetup[] }>("GET", `/deployments/${id}/mobile-signing`);
          setMobileSigning(
            (signing.setups || []).find((setup) => setup.provider === d.deployment.build_backend)
              || signing.setups?.[0]
              || null,
          );
          setStoreState(await api<StoreConfigState>("GET", `/deployments/${id}/store-config`));
        } else if (d.deployment.target_kind === "android") {
          setMobileSigning(null);
          setStoreState(await api<StoreConfigState>("GET", `/deployments/${id}/store-config`));
        } else {
          setMobileSigning(null);
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
    if (!deployment || deployment.target_kind === "service" || mobileChannel === "production") {
      setDistribution(null);
      setDistributionError("");
      return;
    }
    let cancelled = false;
    setDistribution(null);
    setDistributionError("");
    api<MobileDistributionState>(
      "GET",
      `/deployments/${deployment.id}/distribution`,
      undefined,
      { channel: mobileChannel },
    ).then((state) => {
      if (!cancelled) {
        setDistribution(state);
        setDistributionError("");
      }
    }).catch((e) => {
      if (!cancelled) {
        setDistribution(null);
        setDistributionError((e as Error).message);
      }
    });
    return () => { cancelled = true; };
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

  const [confirmState, setConfirmState] = useState<ConfirmRequest | null>(null);

  const runMobileSigningSetup = async (rotate: boolean) => {
    if (!detail) return;
    setSigningBusy(true);
    try {
      const result = await api<{ setup: MobileSigningSetup; ready: boolean; manual_actions?: string[] }>(
        "POST",
        `/deployments/${detail.deployment.id}/mobile-signing/setup`,
        { provider: detail.deployment.build_backend, rotate },
      );
      setMobileSigning(result.setup);
      if (!result.ready && result.manual_actions?.length) {
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
    setConfirmState({
      title: "Rotate iOS signing",
      body: "Create a replacement Apple distribution certificate and profile, update the build provider secrets, then revoke the previous resources?",
      confirmLabel: "Rotate",
      tone: "warning",
      onConfirm: () => runMobileSigningSetup(true),
    });
  };

  const handleAddAudience = async () => {
    if (!detail || !audienceEmail.trim() || mobileChannel === "production") return;
    setAudienceBusy(true);
    try {
      const state = await api<MobileDistributionState>(
        "POST",
        `/deployments/${detail.deployment.id}/distribution`,
        {
          channel: mobileChannel,
          audience: [{
            kind: detail.deployment.target_kind === "android" ? "group" : "individual",
            email: audienceEmail.trim(),
          }],
        },
      );
      setDistribution(state);
      setDistributionError("");
      setAudienceEmail("");
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
                <select
                  value={mobileChannel}
                  onChange={(e) => {
                    setMobileChannel(e.target.value);
                    if (e.target.value !== "production") setSubmitForReview(false);
                  }}
                  className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
                  title="Store release channel"
                >
                  {detail.deployment.target_kind === "android" ? (
                    <>
                      <option value="internal">Internal</option>
                      <option value="alpha">Alpha</option>
                      <option value="beta">Beta</option>
                      <option value="production">Production</option>
                    </>
                  ) : (
                    <>
                      <option value="internal">TestFlight internal</option>
                      <option value="external">TestFlight external</option>
                      <option value="production">App Store</option>
                    </>
                  )}
                </select>
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
            {detail.deployment.target_kind === "ios" && (
              <section className="px-4 py-2 border-b border-border flex items-center gap-3 text-xs flex-wrap">
                <span className="text-text-dim uppercase">iOS signing</span>
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
                </span>
                {mobileSigning?.last_error && (
                  <span className="text-yellow truncate" title={mobileSigning.last_error}>
                    {mobileSigning.last_error}
                  </span>
                )}
                <button
                  type="button"
                  onClick={handleMobileSigningSetup}
                  disabled={signingBusy || detail.deployment.build_backend !== "codemagic"}
                  className="ml-auto px-2 py-0.5 border border-border rounded hover:bg-bg-input disabled:opacity-40 shrink-0"
                  title={
                    detail.deployment.build_backend === "codemagic"
                      ? "Provision Apple distribution signing and store credentials securely at the build provider."
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
                    title="Replace the Apple distribution certificate, private key, and provisioning profile."
                  >
                    Rotate certificate
                  </button>
                )}
              </section>
            )}
            {mobile && mobileChannel !== "production" && (
              <section className="px-4 py-2 border-b border-border flex items-center gap-3 text-xs flex-wrap">
                <span className="text-text-dim uppercase">Test audience</span>
                <span className="text-text-muted">
                  {distribution
                    ? `${distribution.count} ${detail.deployment.target_kind === "android" ? "Google group" : "tester"}${distribution.count === 1 ? "" : "s"}`
                    : "loading"}
                </span>
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
                        void handleAddAudience();
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
                    disabled={audienceBusy || !audienceEmail.trim()}
                    className="px-2 py-1 border border-border rounded hover:bg-bg-input disabled:opacity-40"
                  >
                    {audienceBusy ? "Adding..." : "Add"}
                  </button>
                </div>
                {distributionError && (
                  <span className="basis-full text-red truncate" title={distributionError}>
                    {distributionError}
                  </span>
                )}
                {distribution && distribution.audience.length > 0 && (
                  <div className="basis-full flex items-center gap-2 overflow-x-auto text-text-dim">
                    {distribution.audience.map((member) => (
                      <span key={`${member.kind}:${member.email}`} className="whitespace-nowrap">
                        {member.email}{member.state ? ` (${member.state})` : ""}
                      </span>
                    ))}
                  </div>
                )}
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
                      {detail.builds[0].status === "succeeded"
                        && (mobile || detail.current_release?.build_id !== detail.builds[0].id) && (
                        <button
                          type="button"
                          onClick={() => handleReleaseBuild(detail.builds[0].id)}
                          className="ml-auto px-2 py-0.5 text-[11px] border border-accent text-accent rounded hover:bg-accent hover:text-bg"
                          title="Promote this build to a live release"
                        >{mobile ? `Publish to ${mobileChannel}` : "Release this build →"}</button>
                      )}
                    </div>
                    <div className="text-text-dim truncate">
                      built as: {detail.builds[0].framework}
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
                      <th className="text-left font-normal">Build</th>
                      <th className="text-right font-normal">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detail.releases.map((rel) => (
                      <tr key={rel.id} className="border-t border-border/40">
                        <td className="py-1">{rel.id}</td>
                        <td>{rel.channel || "-"}</td>
                        <td className={statusColor(rel.status)}>{rel.status}</td>
                        <td className="text-text-dim">{rel.external_status || "-"}</td>
                        <td>{rel.build_id}</td>
                        <td className="text-right space-x-2">
                          <button type="button" onClick={() => { setLogKind("release"); setLogTargetId(rel.id); }} className="text-text-dim hover:text-text">log</button>
                          {rel.status !== "failed" && rel.channel !== mobileChannel && (
                            <button type="button" onClick={() => handlePromoteMobile(rel.id)} className="text-accent hover:underline">promote</button>
                          )}
                          {(rel.external_status === "approved_pending_release" || rel.external_status === "pending_apple_release") && (
                            <button
                              type="button"
                              disabled={busy}
                              onClick={() => handleReleaseApproved(rel.id)}
                              className="text-green hover:underline disabled:opacity-40"
                            >release</button>
                          )}
                        </td>
                      </tr>
                    ))}
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
                      <td>{formatDuration(b.duration_ms)}</td>
                      <td>{formatSize(b.artifact_size)}</td>
                      <td className="text-text-dim truncate">{b.created_at}</td>
                      <td className="text-right space-x-2">
                        <button
                          type="button"
                          onClick={() => { setLogKind("build"); setLogTargetId(b.id); }}
                          className="text-text-dim hover:text-text"
                        >log</button>
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
        target_config_json: targetConfig.trim() || "{}",
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
        className="w-[520px] max-h-[90vh] overflow-y-auto bg-bg border border-border rounded p-5 space-y-4"
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
              <label className="text-xs text-text-muted block mb-1">Mobile target config (JSON)</label>
              <textarea
                value={targetConfig}
                onChange={(e) => setTargetConfig(e.target.value)}
                placeholder={targetKind === "android"
                  ? '{"package_name":"com.example.app","module":"app","variant":"release"}'
                  : '{"bundle_id":"com.example.app","scheme":"App","team_id":"TEAMID"}'}
                rows={3}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
              />
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
  const [preflight, setPreflight] = useState(initial.preflight);
  const [tab, setTab] = useState<"listing" | "media" | "review" | "compliance" | "distribution">("listing");
  const [locale, setLocale] = useState(initial.desired.default_locale || "en-US");
  const [newLocale, setNewLocale] = useState("");
  const [assetKind, setAssetKind] = useState("phone_screenshot");
  const [displayTarget, setDisplayTarget] = useState(deployment.target_kind === "ios" ? "APP_IPHONE_67" : "");
  const [reviewPassword, setReviewPassword] = useState("");
  const [hasConfig, setHasConfig] = useState(Boolean(initial.config));
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
      const blocking = saved.preflight.findings.filter((finding) => finding.severity === "error");
      const passwordUnblocksReview = deployment.target_kind === "ios"
        && reviewPassword.length > 0
        && blocking.length > 0
        && blocking.every((finding) => finding.code === "review_demo.required");
      if (!saved.preflight.ready && !passwordUnblocksReview) {
        setErr(`Resolve ${saved.preflight.errors} blocking issue(s) before applying.`);
        return;
      }
      await api("POST", `/deployments/${deployment.id}/store-apply`, {
        review_demo_password: reviewPassword || undefined,
      });
      setReviewPassword("");
      const next = await api<StoreConfigState>("GET", `/deployments/${deployment.id}/store-config`);
      setDoc(structuredClone(next.desired));
      setPreflight(next.preflight);
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
        <header className="px-5 py-3 border-b border-border flex items-center gap-3">
          <div className="min-w-0 flex-1">
            <h2 className="text-text font-semibold">Store listing</h2>
            <div className="text-xs text-text-dim">
              {deployment.target_kind === "ios" ? "App Store Connect" : "Google Play"}
              {initial.config?.status ? ` · ${initial.config.status}` : " · not configured"}
            </div>
          </div>
          <span className={`text-xs ${preflight.ready ? "text-green" : "text-yellow"}`}>
            {preflight.ready ? "Ready" : `${preflight.errors} blocking · ${preflight.warnings} warning`}
          </span>
          <button type="button" onClick={onClose} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input">
            Close
          </button>
        </header>

        <nav className="px-5 border-b border-border flex items-center gap-1">
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
              <section className="grid grid-cols-3 gap-3">
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
                  Release mode
                  <select
                    value={doc.release_mode}
                    onChange={(e) => setDoc({ ...doc, release_mode: e.target.value })}
                    className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                  >
                    <option value="manual">Manual</option>
                    <option value="after_approval">After approval</option>
                    <option value="scheduled">Scheduled</option>
                    <option value="automatic">Automatic</option>
                  </select>
                </label>
              </section>

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
                <div className="grid grid-cols-2 gap-3">
                  <TextField label="Title" value={localization.title} onChange={(title) => updateLocalization({ title })} />
                  <TextField
                    label={deployment.target_kind === "ios" ? "Subtitle" : "Short description"}
                    value={deployment.target_kind === "ios" ? localization.subtitle : localization.short_description}
                    onChange={(value) => updateLocalization(deployment.target_kind === "ios"
                      ? { subtitle: value }
                      : { short_description: value })}
                  />
                  <label className="col-span-2 text-xs text-text-muted">
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
              <section className="flex items-end gap-3">
                <label className="text-xs text-text-muted">
                  Asset type
                  <select value={assetKind} onChange={(e) => setAssetKind(e.target.value)} className="mt-1 block bg-bg-input border border-border rounded px-2 py-1 text-sm">
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
                  <input
                    value={displayTarget}
                    onChange={(e) => setDisplayTarget(e.target.value)}
                    placeholder={deployment.target_kind === "ios" ? "APP_IPHONE_67" : "tablet_10"}
                    className="mt-1 block w-44 bg-bg-input border border-border rounded px-2 py-1 text-sm"
                  />
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
                      <td className="text-text-dim truncate max-w-64">{asset.path.split("/").at(-1)}</td>
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
            <div className="grid grid-cols-2 gap-3">
              <TextField label="First name" value={doc.review.first_name} onChange={(first_name) => setDoc({ ...doc, review: { ...doc.review, first_name } })} />
              <TextField label="Last name" value={doc.review.last_name} onChange={(last_name) => setDoc({ ...doc, review: { ...doc.review, last_name } })} />
              <TextField label="Email" value={doc.review.email} onChange={(email) => setDoc({ ...doc, review: { ...doc.review, email } })} />
              <TextField label="Phone" value={doc.review.phone} onChange={(phone) => setDoc({ ...doc, review: { ...doc.review, phone } })} />
              <label className="col-span-2 text-xs text-text-muted">
                Review notes
                <textarea
                  rows={5}
                  value={doc.review.notes || ""}
                  onChange={(e) => setDoc({ ...doc, review: { ...doc.review, notes: e.target.value } })}
                  className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                />
              </label>
              {deployment.target_kind === "ios" && (
                <>
                  <label className="col-span-2 flex items-center gap-2 text-xs text-text-muted">
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
                    </>
                  )}
                </>
              )}
            </div>
          )}

          {tab === "compliance" && (
            <div className="space-y-4">
              <div className="grid grid-cols-2 gap-3">
                <TextField label="Privacy policy URL" value={doc.privacy.policy_url} onChange={(policy_url) => setDoc({ ...doc, privacy: { ...doc.privacy, policy_url } })} />
                <TextField label="Privacy choices URL" value={doc.privacy.choices_url} onChange={(choices_url) => setDoc({ ...doc, privacy: { ...doc.privacy, choices_url } })} />
                {deployment.target_kind === "ios" && (
                  <>
                    <TextField label="Primary category ID" value={doc.classification.primary_category} onChange={(primary_category) => setDoc({ ...doc, classification: { ...doc.classification, primary_category } })} />
                    <TextField label="Secondary category ID" value={doc.classification.secondary_category} onChange={(secondary_category) => setDoc({ ...doc, classification: { ...doc.classification, secondary_category } })} />
                  </>
                )}
              </div>
              {deployment.target_kind === "ios" ? (
                <>
                  <label className="text-xs text-text-muted block">
                    Apple age declaration
                    <textarea value={ageJSON} onChange={(e) => setAgeJSON(e.target.value)} rows={8} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono" />
                  </label>
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
                    App Privacy questionnaire reviewed and published
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
              <div className="grid grid-cols-2 gap-3">
                <TextField
                  label="Territories"
                  value={(doc.distribution.territories || []).join(", ")}
                  onChange={(value) => setDoc({
                    ...doc,
                    distribution: {
                      ...doc.distribution,
                      territories: value.split(",").map((item) => item.trim()).filter(Boolean),
                    },
                  })}
                />
                <TextField label="Price point / tier" value={doc.distribution.price_tier} onChange={(price_tier) => setDoc({ ...doc, distribution: { ...doc.distribution, price_tier } })} />
                <label className="flex items-center gap-2 text-xs text-text-muted">
                  <input
                    type="checkbox"
                    checked={Boolean(doc.distribution.phased_release)}
                    onChange={(e) => setDoc({ ...doc, distribution: { ...doc.distribution, phased_release: e.target.checked } })}
                  />
                  Phased release
                </label>
                {doc.release_mode === "scheduled" && (
                  <label className="text-xs text-text-muted">
                    Earliest release
                    <input
                      type="datetime-local"
                      value={doc.earliest_release_at || ""}
                      onChange={(e) => setDoc({ ...doc, earliest_release_at: e.target.value })}
                      className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                    />
                  </label>
                )}
                <label className="flex items-center gap-2 text-xs text-text-muted">
                  <input
                    type="checkbox"
                    checked={Boolean(doc.distribution.provider?.availability_configured)}
                    onChange={(e) => setDoc({
                      ...doc,
                      distribution: {
                        ...doc.distribution,
                        provider: { ...doc.distribution.provider, availability_configured: e.target.checked },
                      },
                    })}
                  />
                  Existing store availability verified
                </label>
                <label className="flex items-center gap-2 text-xs text-text-muted">
                  <input
                    type="checkbox"
                    checked={Boolean(doc.distribution.provider?.pricing_configured)}
                    onChange={(e) => setDoc({
                      ...doc,
                      distribution: {
                        ...doc.distribution,
                        provider: { ...doc.distribution.provider, pricing_configured: e.target.checked },
                      },
                    })}
                  />
                  Existing store pricing verified
                </label>
              </div>
              <label className="text-xs text-text-muted block">
                Provider extensions
                <textarea
                  value={providerExtensionsJSON}
                  onChange={(e) => setProviderExtensionsJSON(e.target.value)}
                  rows={9}
                  className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"
                />
              </label>
            </div>
          )}
        </div>

        <footer className="px-5 py-3 border-t border-border flex items-center gap-2">
          {err && <span className="text-xs text-red flex-1 truncate" title={err}>{err}</span>}
          {!err && <span className="flex-1 text-xs text-text-dim">Saving does not submit or release the app.</span>}
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
        className="w-[520px] max-h-[90vh] overflow-y-auto bg-bg border border-border rounded p-5 space-y-4"
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
              <label className="text-xs text-text-muted block mb-1">Mobile target config</label>
              <textarea
                value={targetConfigJSON}
                onChange={(e) => setTargetConfigJSON(e.target.value)}
                rows={5}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
              />
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
