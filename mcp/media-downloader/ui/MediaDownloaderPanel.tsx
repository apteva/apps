import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AudioLines,
  ChevronDown,
  ChevronUp,
  Download,
  ExternalLink,
  FileSearch,
  LoaderCircle,
  RefreshCw,
  RotateCcw,
  ShieldCheck,
  Trash2,
  Upload,
  Video,
  X,
} from "lucide-react";

const API = "/api/apps/media-downloader";

function cx(...parts: Array<string | false | null | undefined>) {
  return parts.filter(Boolean).join(" ");
}

async function request(path: string, options: RequestInit = {}, projectId = "") {
  const sep = path.includes("?") ? "&" : "?";
  const url = projectId ? `${API}${path}${sep}project_id=${encodeURIComponent(projectId)}` : `${API}${path}`;
  const res = await fetch(url, {
    ...options,
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const text = await res.text();
  let body: any = {};
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = { error: text };
    }
  }
  if (!res.ok) throw new Error(body.error || `${res.status} ${res.statusText}`);
  return body;
}

function useAppEvents(app: string, projectId: string, onEvent: (event: any) => void) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const bridge = (window as any).__aptevaAppEvents;
    if (bridge) return bridge.subscribe(app, projectId, (event: any) => handlerRef.current(event));
    let lastSeq = 0;
    let stopped = false;
    let source: EventSource | null = null;
    let timer = 0;
    const connect = () => {
      if (stopped) return;
      const since = lastSeq > 0 ? `&since=${lastSeq}` : "";
      source = new EventSource(`/api/app-events/${encodeURIComponent(app)}?project_id=${encodeURIComponent(projectId)}${since}`, { withCredentials: true });
      source.onmessage = (message) => {
        try {
          const event = JSON.parse(message.data);
          if (event.seq && event.seq <= lastSeq) return;
          lastSeq = event.seq || lastSeq;
          handlerRef.current(event);
        } catch {}
      };
      source.onerror = () => {
        source?.close();
        timer = window.setTimeout(connect, 2000);
      };
    };
    connect();
    return () => {
      stopped = true;
      source?.close();
      window.clearTimeout(timer);
    };
  }, [app, projectId]);
}

function Button({ tone = "default", icon: Icon, className, children, ...props }: any) {
  return (
    <button
      {...props}
      className={cx(
        "inline-flex min-h-9 items-center justify-center gap-2 rounded border px-3 text-sm transition-colors disabled:cursor-not-allowed disabled:opacity-50",
        tone === "primary" && "border-accent bg-accent text-bg font-medium",
        tone === "danger" && "border-error/60 text-error hover:bg-error/10",
        tone === "default" && "border-border bg-bg text-text-muted hover:bg-bg-elevated hover:text-text",
        className,
      )}
    >
      {Icon && <Icon size={16} aria-hidden="true" />}
      {children}
    </button>
  );
}

function IconButton({ label, icon: Icon, className, ...props }: any) {
  return (
    <button
      {...props}
      title={label}
      aria-label={label}
      className={cx("inline-flex size-9 shrink-0 items-center justify-center rounded border border-border text-text-muted hover:bg-bg-elevated hover:text-text disabled:opacity-50", className)}
    >
      <Icon size={16} aria-hidden="true" />
    </button>
  );
}

function Field({ label, className, children, group = false }: any) {
  if (group) {
    return (
      <div className={cx("flex min-w-0 flex-col gap-1.5 text-xs text-text-dim", className)}>
        <span>{label}</span>
        {children}
      </div>
    );
  }
  return (
    <label className={cx("flex min-w-0 flex-col gap-1.5 text-xs text-text-dim", className)}>
      <span>{label}</span>
      {children}
    </label>
  );
}

const inputClass = "min-h-9 min-w-0 rounded border border-border bg-bg-input px-2.5 text-sm text-text outline-none focus:border-accent";

function fmtBytes(value: number) {
  if (!value) return "";
  const units = ["B", "KB", "MB", "GB"];
  let size = Number(value);
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${size.toFixed(unit ? 1 : 0)} ${units[unit]}`;
}

function fmtDate(value: string) {
  if (!value) return "";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function shortError(value: string) {
  const line = String(value || "").split("\n").find(Boolean) || "";
  return line.length > 180 ? `${line.slice(0, 177)}...` : line;
}

function statusTone(status: string) {
  if (status === "completed" || status === "valid") return "text-success";
  if (status === "failed" || status === "invalid") return "text-error";
  if (status === "canceled") return "text-warn";
  if (status === "running") return "text-accent";
  return "text-text-dim";
}

const stageLabels: Record<string, string> = {
  queued: "Queued",
  downloading: "Downloading",
  postprocessing: "Processing",
  preparing: "Preparing upload",
  uploading: "Uploading",
  completed: "Completed",
  failed: "Failed",
  canceled: "Canceled",
};

const initialDownload = {
  url: "",
  mode: "video",
  quality: "best",
  audio_format: "mp3",
  folder: "/.downloads/media",
  visibility: "private",
  source_profile_id: "",
};

export default function MediaDownloaderPanel({ projectId = "" }: { projectId?: string }) {
  const [tab, setTab] = useState("downloads");
  const [jobs, setJobs] = useState<any[]>([]);
  const [profiles, setProfiles] = useState<any[]>([]);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [downloadDraft, setDownloadDraft] = useState(initialDownload);
  const refreshSeq = useRef(0);

  const refresh = useCallback(async () => {
    const seq = ++refreshSeq.current;
    setBusy(true);
    try {
      const [jobData, profileData] = await Promise.all([
        request("/jobs?limit=50", {}, projectId),
        request("/profiles", {}, projectId),
      ]);
      if (seq !== refreshSeq.current) return;
      setJobs(jobData.jobs || []);
      setProfiles(profileData.profiles || []);
      setNotice("");
    } catch (error: any) {
      if (seq === refreshSeq.current) setNotice(error.message);
    } finally {
      if (seq === refreshSeq.current) setBusy(false);
    }
  }, [projectId]);

  useEffect(() => {
    setJobs([]);
    setProfiles([]);
    refresh();
    const timer = window.setInterval(refresh, 60000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  useAppEvents("media-downloader", projectId, (event) => {
    const data = event?.data;
    if (!data?.id) return;
    if (event.topic?.startsWith("download.")) {
      setJobs((current) => {
        const index = current.findIndex((job) => job.id === data.id);
        if (index < 0) return event.topic === "download.created" ? [data, ...current] : current;
        const next = current.slice();
        next[index] = { ...next[index], ...data };
        return next;
      });
    }
    if (event.topic === "profile.deleted") {
      setProfiles((current) => current.filter((profile) => profile.id !== data.id));
    } else if (event.topic?.startsWith("profile.")) {
      setProfiles((current) => {
        const index = current.findIndex((profile) => profile.id === data.id);
        if (index < 0) return event.topic === "profile.created" ? [data, ...current] : current;
        const next = current.slice();
        next[index] = { ...next[index], ...data };
        return next;
      });
    }
  });

  return (
    <div className="flex h-full min-h-0 flex-col bg-bg text-text">
      <header className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2 sm:px-4">
        <div className="mr-1 flex min-w-0 items-center gap-2 font-medium">
          <Download size={18} className="shrink-0 text-accent" aria-hidden="true" />
          <span className="truncate">Media Downloader</span>
        </div>
        <div className="flex rounded border border-border p-0.5" role="tablist" aria-label="Media Downloader views">
          {[
            ["downloads", "Downloads"],
            ["profiles", "Profiles"],
          ].map(([value, label]) => (
            <button
              key={value}
              role="tab"
              aria-selected={tab === value}
              onClick={() => setTab(value)}
              className={cx("min-h-8 rounded px-3 text-sm", tab === value ? "bg-bg-elevated text-text" : "text-text-dim hover:text-text")}
            >
              {label}
            </button>
          ))}
        </div>
        <div className="ml-auto flex min-w-0 items-center gap-2">
          <div className="min-w-0 text-right text-xs text-text-dim" aria-live="polite">
            {notice ? <span className="text-error">{shortError(notice)}</span> : busy ? "Refreshing" : ""}
          </div>
          <IconButton label="Refresh" icon={busy ? LoaderCircle : RefreshCw} className={busy ? "[&>svg]:animate-spin" : ""} onClick={refresh} disabled={busy} />
        </div>
      </header>
      <main className="min-h-0 min-w-0 flex-1 overflow-x-hidden overflow-y-auto" role="tabpanel">
        {tab === "downloads" ? (
          <DownloadsTab
            jobs={jobs}
            profiles={profiles}
            projectId={projectId}
            draft={downloadDraft}
            setDraft={setDownloadDraft}
            onRefresh={refresh}
            setNotice={setNotice}
          />
        ) : (
          <ProfilesTab profiles={profiles} projectId={projectId} onRefresh={refresh} setNotice={setNotice} />
        )}
      </main>
    </div>
  );
}

function DownloadsTab({ jobs, profiles, projectId, draft, setDraft, onRefresh, setNotice }: any) {
  const [submitting, setSubmitting] = useState(false);
  const [probing, setProbing] = useState(false);
  const [metadata, setMetadata] = useState<any>(null);
  const active = useMemo(() => jobs.filter((job: any) => job.status === "running" || job.status === "queued").length, [jobs]);
  const update = (key: string, value: string) => setDraft((current: any) => ({ ...current, [key]: value }));

  const probe = async () => {
    if (!draft.url.trim()) return;
    setProbing(true);
    setNotice("");
    try {
      const result = await request("/probe", { method: "POST", body: JSON.stringify({ url: draft.url, source_profile_id: draft.source_profile_id }) }, projectId);
      setMetadata(result.metadata || null);
    } catch (error: any) {
      setMetadata(null);
      setNotice(error.message);
    } finally {
      setProbing(false);
    }
  };

  const submit = async (event: any) => {
    event.preventDefault();
    if (!draft.url.trim()) return;
    setSubmitting(true);
    setNotice("");
    try {
      await request("/jobs", { method: "POST", body: JSON.stringify(draft) }, projectId);
      setDraft((current: any) => ({ ...current, url: "" }));
      setMetadata(null);
      await onRefresh();
    } catch (error: any) {
      setNotice(error.message);
    } finally {
      setSubmitting(false);
    }
  };

  const cancel = async (id: string) => {
    try {
      await request(`/jobs/${encodeURIComponent(id)}/cancel`, { method: "POST", body: "{}" }, projectId);
    } catch (error: any) {
      setNotice(error.message);
    }
  };

  const retry = (job: any) => {
    setDraft({
      ...initialDownload,
      url: job.url,
      mode: job.mode || "video",
      quality: job.quality || "best",
      folder: job.storage_folder || initialDownload.folder,
      visibility: job.storage_visibility || "private",
      source_profile_id: job.source_profile_id || "",
    });
    window.setTimeout(() => document.getElementById("media-download-url")?.focus(), 0);
  };

  return (
    <div className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-4 p-3 sm:p-4">
      <form onSubmit={submit} className="grid w-full min-w-0 max-w-full gap-3 border-b border-border pb-4">
        <div className="flex min-w-0 flex-col gap-2 sm:flex-row">
          <Field label="Source URL" className="flex-1">
            <input id="media-download-url" className={inputClass} type="url" required value={draft.url} onChange={(event) => update("url", event.target.value)} placeholder="https://..." />
          </Field>
          <div className="flex items-end gap-2">
            <Button type="button" icon={probing ? LoaderCircle : FileSearch} className={probing ? "[&>svg]:animate-spin" : ""} onClick={probe} disabled={!draft.url.trim() || probing}>
              Probe
            </Button>
            <Button type="submit" tone="primary" icon={submitting ? LoaderCircle : Download} className={submitting ? "[&>svg]:animate-spin" : ""} disabled={!draft.url.trim() || submitting}>
              Download
            </Button>
          </div>
        </div>
        <div className="grid min-w-0 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Field label="Mode" group>
            <div className="grid min-h-9 grid-cols-2 rounded border border-border p-0.5">
              {[["video", "Video", Video], ["audio", "Audio", AudioLines]].map(([value, label, Icon]: any) => (
                <button key={value} type="button" onClick={() => setDraft((current: any) => ({ ...current, mode: value, quality: value === "audio" ? "best" : current.quality }))} className={cx("flex items-center justify-center gap-2 rounded px-2 text-sm", draft.mode === value ? "bg-bg-elevated text-text" : "text-text-dim")}>
                  <Icon size={15} aria-hidden="true" /> {label}
                </button>
              ))}
            </div>
          </Field>
          {draft.mode === "video" ? (
            <Field label="Quality">
              <select className={inputClass} value={draft.quality} onChange={(event) => update("quality", event.target.value)}>
                {["best", "1080p", "720p", "480p", "360p", "worst"].map((value) => <option key={value}>{value}</option>)}
              </select>
            </Field>
          ) : (
            <Field label="Audio format">
              <select className={inputClass} value={draft.audio_format} onChange={(event) => update("audio_format", event.target.value)}>
                {["mp3", "m4a", "opus", "wav"].map((value) => <option key={value}>{value}</option>)}
              </select>
            </Field>
          )}
          <Field label="Source profile">
            <select className={inputClass} value={draft.source_profile_id} onChange={(event) => update("source_profile_id", event.target.value)}>
              <option value="">None</option>
              {profiles.map((profile: any) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
            </select>
          </Field>
          <Field label="Visibility">
            <select className={inputClass} value={draft.visibility} onChange={(event) => update("visibility", event.target.value)}>
              {["private", "signed", "public"].map((value) => <option key={value}>{value}</option>)}
            </select>
          </Field>
          <Field label="Storage folder" className="sm:col-span-2 lg:col-span-4">
            <input className={inputClass} value={draft.folder} onChange={(event) => update("folder", event.target.value)} />
          </Field>
        </div>
        {metadata && (
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-l-2 border-accent px-3 py-1 text-xs text-text-muted">
            <strong className="max-w-full truncate text-text">{metadata.title || metadata.id}</strong>
            {metadata.uploader && <span>{metadata.uploader}</span>}
            {metadata.duration != null && <span>{Math.round(Number(metadata.duration) / 60)} min</span>}
            {metadata.age_limit > 0 && <span>Age {metadata.age_limit}+</span>}
            {metadata.format_count != null && <span>{metadata.format_count} formats</span>}
            <IconButton label="Clear probe result" icon={X} className="ml-auto size-7 border-0" onClick={() => setMetadata(null)} />
          </div>
        )}
      </form>

      <div className="flex items-center justify-between">
        <h2 className="text-sm font-medium">Recent downloads</h2>
        <span className="text-xs text-text-dim">{active} active</span>
      </div>
      <div className="border-y border-border">
        {jobs.length === 0 ? (
          <div className="p-8 text-center text-sm text-text-dim">No downloads yet.</div>
        ) : jobs.map((job: any) => (
          <JobRow key={job.id} job={job} projectId={projectId} onCancel={cancel} onRetry={retry} setNotice={setNotice} />
        ))}
      </div>
    </div>
  );
}

function JobRow({ job, projectId, onCancel, onRetry, setNotice }: any) {
  const [expanded, setExpanded] = useState(false);
  const [details, setDetails] = useState<any>(null);
  const [loading, setLoading] = useState(false);
  const active = job.status === "running" || job.status === "queued";
  const progress = Math.max(0, Math.min(100, Number(job.progress || 0)));
  const stage = stageLabels[job.stage] || stageLabels[job.status] || job.status;

  const toggle = async () => {
    const next = !expanded;
    setExpanded(next);
    if (!next) return;
    setLoading(true);
    try {
      setDetails(await request(`/jobs/${encodeURIComponent(job.id)}`, {}, projectId));
    } catch (error: any) {
      setNotice(error.message);
    } finally {
      setLoading(false);
    }
  };

  return (
    <article className="border-b border-border py-3 last:border-b-0">
      <div className="flex min-w-0 flex-wrap items-start gap-2 px-3 sm:flex-nowrap">
        <button className="min-w-0 basis-full text-left sm:basis-auto sm:flex-1" onClick={toggle} aria-expanded={expanded}>
          <div className="truncate text-sm font-medium" title={job.title || job.url}>{job.title || job.url}</div>
          <div className="mt-0.5 flex flex-wrap gap-x-2 text-xs text-text-dim">
            <span>{job.mode} / {job.quality}</span>
            <span className="max-w-full truncate">{job.storage_folder}</span>
          </div>
        </button>
        <span className={cx("ml-auto shrink-0 pt-1.5 text-xs font-medium sm:ml-0 sm:pt-0.5", statusTone(job.status))}>{stage}</span>
        {active ? (
          <Button className="min-h-8 px-2" onClick={() => onCancel(job.id)}>Cancel</Button>
        ) : (
          <IconButton label="Retry download" icon={RotateCcw} className="size-8" onClick={() => onRetry(job)} />
        )}
        <IconButton label={expanded ? "Hide details" : "Show details"} icon={expanded ? ChevronUp : ChevronDown} className="size-8" onClick={toggle} />
      </div>
      <div className="mt-2 flex items-center gap-3 px-3">
        <div className="h-1.5 flex-1 overflow-hidden rounded bg-bg-input" role="progressbar" aria-label={`${stage} progress`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(progress)}>
          <div className={cx("h-full transition-[width]", job.status === "failed" ? "bg-error" : job.status === "canceled" ? "bg-warn" : "bg-accent")} style={{ width: `${progress}%` }} />
        </div>
        <span className="w-10 text-right text-xs tabular-nums text-text-dim">{Math.round(progress)}%</span>
      </div>
      <div className="mt-2 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 px-3 text-xs text-text-dim">
        {job.output_name && <span className="max-w-full truncate">{job.output_name}</span>}
        {job.output_bytes > 0 && <span>{fmtBytes(job.output_bytes)}</span>}
        {job.storage_url && (
          <a href={job.storage_url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-accent hover:underline">
            Open file <ExternalLink size={12} aria-hidden="true" />
          </a>
        )}
        {job.error && <span className="min-w-0 flex-1 truncate text-error" title={job.error}>{shortError(job.error)}</span>}
      </div>
      {expanded && (
        <div className="mt-3 border-t border-border bg-bg-elevated/40 px-3 py-3 text-xs">
          {loading ? (
            <div className="flex items-center gap-2 text-text-dim"><LoaderCircle size={14} className="animate-spin" /> Loading</div>
          ) : (
            <div className="grid gap-3">
              <dl className="grid gap-x-4 gap-y-1 text-text-muted sm:grid-cols-2">
                <div><dt className="inline text-text-dim">Created: </dt><dd className="inline">{fmtDate(job.created_at)}</dd></div>
                <div><dt className="inline text-text-dim">Updated: </dt><dd className="inline">{fmtDate(job.updated_at)}</dd></div>
                {job.extractor && <div><dt className="inline text-text-dim">Extractor: </dt><dd className="inline">{job.extractor}</dd></div>}
                <div><dt className="inline text-text-dim">Job: </dt><dd className="inline font-mono">{job.id}</dd></div>
              </dl>
              {details?.logs?.length > 0 && (
                <div className="max-h-48 overflow-auto border-l-2 border-border pl-3 font-mono text-[11px] leading-5 text-text-muted">
                  {details.logs.map((log: any, index: number) => <div key={`${log.created_at}-${index}`} className={log.level === "error" || log.level === "stderr" ? "text-error" : ""}>{log.message}</div>)}
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </article>
  );
}

const providers: any = {
  youtube: { label: "YouTube", name: "YouTube account", test: "https://www.youtube.com/watch?v=...", cookie: ".youtube.com\tTRUE\t/\tTRUE\t1893456000\tSID\t..." },
  patreon: { label: "Patreon", name: "Patreon account", test: "https://www.patreon.com/posts/...", cookie: ".patreon.com\tTRUE\t/\tTRUE\t1893456000\tsession_id\t..." },
};

function ProfilesTab({ profiles, projectId, onRefresh, setNotice }: any) {
  const [form, setForm] = useState({ name: "", provider: "youtube", test_url: "", cookies_netscape: "" });
  const [validating, setValidating] = useState("");
  const [testURLs, setTestURLs] = useState<Record<string, string>>({});
  const [deleteTarget, setDeleteTarget] = useState<any>(null);
  const [busy, setBusy] = useState(false);
  const meta = providers[form.provider];
  const update = (key: string, value: string) => setForm((current) => ({ ...current, [key]: value }));

  const submit = async (event: any) => {
    event.preventDefault();
    setBusy(true);
    setNotice("");
    try {
      const result = await request("/profiles", { method: "POST", body: JSON.stringify(form) }, projectId);
      if (result.validation_error) setNotice(result.validation_error);
      setForm({ name: "", provider: form.provider, test_url: "", cookies_netscape: "" });
      await onRefresh();
    } catch (error: any) {
      setNotice(error.message);
    } finally {
      setBusy(false);
    }
  };

  const importCookies = async (file?: File) => {
    if (!file) return;
    try {
      update("cookies_netscape", await file.text());
    } catch (error: any) {
      setNotice(error.message);
    }
  };

  const validate = async (profile: any) => {
    const url = (testURLs[profile.id] || "").trim();
    if (!url) return;
    setValidating(profile.id);
    setNotice("");
    try {
      const result = await request(`/profiles/${encodeURIComponent(profile.id)}/validate`, { method: "POST", body: JSON.stringify({ url }) }, projectId);
      if (!result.valid) setNotice(result.error || "Profile validation failed");
      await onRefresh();
    } catch (error: any) {
      setNotice(error.message);
    } finally {
      setValidating("");
    }
  };

  const remove = async () => {
    if (!deleteTarget) return;
    setBusy(true);
    try {
      await request(`/profiles/${encodeURIComponent(deleteTarget.id)}`, { method: "DELETE" }, projectId);
      setDeleteTarget(null);
    } catch (error: any) {
      setNotice(error.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-4 p-3 sm:p-4">
      <form onSubmit={submit} className="grid w-full min-w-0 max-w-full gap-3 border-b border-border pb-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Name">
            <input className={inputClass} required value={form.name} onChange={(event) => update("name", event.target.value)} placeholder={meta.name} />
          </Field>
          <Field label="Provider">
            <select className={inputClass} value={form.provider} onChange={(event) => update("provider", event.target.value)}>
              {Object.entries(providers).map(([value, provider]: any) => <option key={value} value={value}>{provider.label}</option>)}
            </select>
          </Field>
          <Field label="Validation URL">
            <input className={inputClass} type="url" value={form.test_url} onChange={(event) => update("test_url", event.target.value)} placeholder={meta.test} />
          </Field>
          <Field label="Import cookies.txt" group>
            <label className="inline-flex min-h-9 cursor-pointer items-center justify-center gap-2 rounded border border-border bg-bg-input px-3 text-sm text-text-muted hover:text-text">
              <Upload size={16} aria-hidden="true" /> Choose file
              <input className="sr-only" type="file" accept=".txt,text/plain" onChange={(event) => importCookies(event.target.files?.[0])} />
            </label>
          </Field>
        </div>
        <Field label="Netscape cookies">
          <textarea className={`${inputClass} min-h-32 resize-y py-2 font-mono`} required value={form.cookies_netscape} onChange={(event) => update("cookies_netscape", event.target.value)} placeholder={meta.cookie} />
        </Field>
        <div className="flex justify-end">
          <Button type="submit" tone="primary" icon={busy ? LoaderCircle : ShieldCheck} className={busy ? "[&>svg]:animate-spin" : ""} disabled={busy || !form.name.trim() || !form.cookies_netscape.trim()}>
            Save profile
          </Button>
        </div>
      </form>

      <h2 className="text-sm font-medium">Source profiles</h2>
      <div className="border-y border-border">
        {profiles.length === 0 ? (
          <div className="p-8 text-center text-sm text-text-dim">No source profiles.</div>
        ) : profiles.map((profile: any) => (
          <div key={profile.id} className="grid gap-2 border-b border-border p-3 last:border-b-0">
            <div className="flex min-w-0 items-center gap-2">
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">{profile.name}</div>
                <div className="text-xs text-text-dim">{providers[profile.provider]?.label || profile.provider} / {profile.auth_type}</div>
              </div>
              <span className={cx("text-xs font-medium capitalize", statusTone(profile.status))}>{profile.status}</span>
              <IconButton label="Delete profile" icon={Trash2} className="size-8 text-error" onClick={() => setDeleteTarget(profile)} />
            </div>
            <div className="flex min-w-0 flex-col gap-2 sm:flex-row">
              <input
                className={cx(inputClass, "flex-1")}
                type="url"
                value={testURLs[profile.id] || ""}
                onChange={(event) => setTestURLs((current) => ({ ...current, [profile.id]: event.target.value }))}
                placeholder={providers[profile.provider]?.test || "https://..."}
                aria-label={`Validation URL for ${profile.name}`}
              />
              <Button icon={validating === profile.id ? LoaderCircle : ShieldCheck} className={validating === profile.id ? "[&>svg]:animate-spin" : ""} onClick={() => validate(profile)} disabled={validating === profile.id || !(testURLs[profile.id] || "").trim()}>
                Validate
              </Button>
            </div>
            {profile.last_validated_at && <div className="text-xs text-text-dim">Checked {fmtDate(profile.last_validated_at)}</div>}
            {profile.last_error && <div className="truncate text-xs text-error" title={profile.last_error}>{shortError(profile.last_error)}</div>}
          </div>
        ))}
      </div>

      {deleteTarget && (
        <div className="fixed inset-0 z-50 grid place-items-center bg-black/50 p-4" role="dialog" aria-modal="true" aria-labelledby="delete-profile-title">
          <div className="w-full max-w-sm rounded border border-border bg-bg-elevated p-4 shadow-xl">
            <div id="delete-profile-title" className="font-medium">Delete {deleteTarget.name}?</div>
            <div className="mt-4 flex justify-end gap-2">
              <Button onClick={() => setDeleteTarget(null)}>Cancel</Button>
              <Button tone="danger" icon={Trash2} onClick={remove} disabled={busy}>Delete</Button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
