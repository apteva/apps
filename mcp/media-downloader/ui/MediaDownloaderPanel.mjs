import { useCallback, useEffect, useMemo, useState } from "react";
import { Fragment, jsx, jsxs } from "react/jsx-runtime";

const API = "/api/apps/media-downloader";

function cx(...parts) {
  return parts.filter(Boolean).join(" ");
}

async function request(path, options = {}, projectId = "") {
  const sep = path.includes("?") ? "&" : "?";
  const url = projectId ? `${API}${path}${sep}project_id=${encodeURIComponent(projectId)}` : `${API}${path}`;
  const res = await fetch(url, {
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
    ...options,
  });
  const text = await res.text();
  const body = text ? JSON.parse(text) : {};
  if (!res.ok) throw new Error(body.error || `${res.status} ${res.statusText}`);
  return body;
}

function fmtBytes(n) {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let v = Number(n);
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function statusClass(status) {
  if (status === "completed") return "text-success";
  if (status === "failed") return "text-error";
  if (status === "canceled") return "text-warn";
  if (status === "running") return "text-accent";
  return "text-text-dim";
}

function Field({ label, children }) {
  return jsxs("label", {
    className: "flex flex-col gap-1 text-xs text-text-dim",
    children: [
      jsx("span", { children: label }),
      children,
    ],
  });
}

function Input(props) {
  return jsx("input", {
    ...props,
    className: cx("bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text", props.className),
  });
}

function Select(props) {
  return jsx("select", {
    ...props,
    className: cx("bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text", props.className),
  });
}

function Button({ tone = "default", className = "", ...props }) {
  return jsx("button", {
    ...props,
    className: cx(
      "inline-flex items-center justify-center gap-1 rounded border px-3 py-1.5 text-sm disabled:opacity-50",
      tone === "primary" && "border-accent bg-accent text-bg font-medium",
      tone === "danger" && "border-error text-error hover:bg-error/10",
      tone === "default" && "border-border text-text-muted hover:text-text",
      className,
    ),
  });
}

export default function MediaDownloaderPanel({ projectId }) {
  const [tab, setTab] = useState("downloads");
  const [jobs, setJobs] = useState([]);
  const [profiles, setProfiles] = useState([]);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  const refresh = useCallback(async () => {
    setBusy(true);
    try {
      const [j, p] = await Promise.all([
        request("/jobs?limit=50", {}, projectId),
        request("/profiles", {}, projectId),
      ]);
      setJobs(j.jobs || []);
      setProfiles(p.profiles || []);
      setMessage("");
    } catch (err) {
      setMessage(err.message);
    } finally {
      setBusy(false);
    }
  }, [projectId]);

  useEffect(() => {
    refresh();
    const t = window.setInterval(refresh, 3000);
    return () => window.clearInterval(t);
  }, [refresh]);

  return jsxs("div", {
    className: "h-full flex flex-col bg-bg text-text",
    children: [
      jsxs("header", {
        className: "flex items-center gap-3 border-b border-border px-4 py-2",
        children: [
          jsx("div", { className: "font-medium", children: "Media Downloader" }),
          jsxs("div", {
            className: "flex border border-border rounded overflow-hidden",
            children: ["downloads", "profiles"].map((name) =>
              jsx("button", {
                onClick: () => setTab(name),
                className: cx(
                  "px-3 py-1.5 text-sm capitalize",
                  tab === name ? "bg-bg-elevated text-text" : "text-text-dim hover:text-text",
                ),
                children: name,
              }, name),
            ),
          }),
          jsx("div", { className: "ml-auto text-xs text-text-dim", children: message || (busy ? "refreshing" : "") }),
          jsx(Button, { onClick: refresh, disabled: busy, children: "Refresh" }),
        ],
      }),
      jsx("main", {
        className: "flex-1 overflow-auto",
        children: tab === "downloads"
          ? jsx(DownloadsTab, { jobs, profiles, projectId, onRefresh: refresh, setMessage })
          : jsx(ProfilesTab, { profiles, projectId, onRefresh: refresh, setMessage }),
      }),
    ],
  });
}

function DownloadsTab({ jobs, profiles, projectId, onRefresh, setMessage }) {
  const [form, setForm] = useState({
    url: "",
    mode: "video",
    quality: "best",
    audio_format: "mp3",
    folder: "/.downloads/media",
    visibility: "private",
    source_profile_id: "",
  });
  const running = useMemo(() => jobs.filter((j) => j.status === "running" || j.status === "queued").length, [jobs]);

  const update = (key, value) => setForm((f) => ({ ...f, [key]: value }));
  const submit = async (e) => {
    e.preventDefault();
    if (!form.url.trim()) return;
    try {
      await request("/jobs", { method: "POST", body: JSON.stringify(form) }, projectId);
      setForm((f) => ({ ...f, url: "" }));
      await onRefresh();
    } catch (err) {
      setMessage(err.message);
    }
  };
  const cancel = async (id) => {
    try {
      await request(`/jobs/${encodeURIComponent(id)}/cancel`, { method: "POST", body: "{}" }, projectId);
      await onRefresh();
    } catch (err) {
      setMessage(err.message);
    }
  };

  return jsxs("div", {
    className: "p-4 grid gap-4",
    children: [
      jsxs("form", {
        onSubmit: submit,
        className: "border border-border rounded p-3 grid gap-3",
        children: [
          jsx(Field, { label: "URL", children: jsx(Input, { value: form.url, onChange: (e) => update("url", e.target.value), placeholder: "https://www.youtube.com/watch?v=...", className: "w-full" }) }),
          jsxs("div", {
            className: "grid gap-3 md:grid-cols-6",
            children: [
              jsx(Field, { label: "Mode", children: jsx(Select, { value: form.mode, onChange: (e) => update("mode", e.target.value), children: [jsx("option", { value: "video", children: "Video" }), jsx("option", { value: "audio", children: "Audio" })] }) }),
              jsx(Field, { label: "Quality", children: jsx(Select, { value: form.quality, onChange: (e) => update("quality", e.target.value), children: ["best", "1080p", "720p", "480p", "360p", "worst"].map((q) => jsx("option", { value: q, children: q }, q)) }) }),
              jsx(Field, { label: "Audio", children: jsx(Select, { value: form.audio_format, onChange: (e) => update("audio_format", e.target.value), children: ["mp3", "m4a", "opus", "wav"].map((q) => jsx("option", { value: q, children: q }, q)) }) }),
              jsx(Field, { label: "Profile", children: jsx(Select, { value: form.source_profile_id, onChange: (e) => update("source_profile_id", e.target.value), children: [jsx("option", { value: "", children: "None" }), ...profiles.map((p) => jsx("option", { value: p.id, children: p.name }, p.id))] }) }),
              jsx(Field, { label: "Visibility", children: jsx(Select, { value: form.visibility, onChange: (e) => update("visibility", e.target.value), children: ["private", "signed", "public"].map((q) => jsx("option", { value: q, children: q }, q)) }) }),
              jsx(Field, { label: "Folder", children: jsx(Input, { value: form.folder, onChange: (e) => update("folder", e.target.value) }) }),
            ],
          }),
          jsxs("div", {
            className: "flex items-center justify-between gap-3",
            children: [
              jsx("div", { className: "text-xs text-text-dim", children: `${running} active` }),
              jsx(Button, { tone: "primary", disabled: !form.url.trim(), children: "Download" }),
            ],
          }),
        ],
      }),
      jsx("div", {
        className: "border border-border rounded overflow-hidden",
        children: jobs.length === 0
          ? jsx("div", { className: "p-6 text-center text-sm text-text-dim", children: "No downloads yet." })
          : jobs.map((job) => jsx(JobRow, { job, onCancel: cancel }, job.id)),
      }),
    ],
  });
}

function JobRow({ job, onCancel }) {
  const active = job.status === "running" || job.status === "queued";
  return jsxs("div", {
    className: "grid gap-2 border-b border-border last:border-b-0 p-3",
    children: [
      jsxs("div", {
        className: "flex items-start gap-3",
        children: [
          jsxs("div", {
            className: "min-w-0 flex-1",
            children: [
              jsx("div", { className: "text-sm font-medium truncate", title: job.title || job.url, children: job.title || job.url }),
              jsxs("div", { className: "text-xs text-text-dim truncate", children: [job.mode, " / ", job.quality, " / ", job.storage_folder] }),
            ],
          }),
          jsx("div", { className: cx("text-xs uppercase", statusClass(job.status)), children: job.status }),
          active && jsx(Button, { onClick: () => onCancel(job.id), children: "Cancel" }),
        ],
      }),
      jsxs("div", {
        className: "flex items-center gap-3",
        children: [
          jsx("div", { className: "h-2 flex-1 bg-bg-input rounded overflow-hidden", children: jsx("div", { className: "h-full bg-accent", style: { width: `${Math.max(0, Math.min(100, job.progress || 0))}%` } }) }),
          jsx("div", { className: "text-xs text-text-dim w-12 text-right", children: `${Math.round(job.progress || 0)}%` }),
        ],
      }),
      jsxs("div", {
        className: "flex flex-wrap gap-3 text-xs text-text-dim",
        children: [
          job.output_name && jsx("span", { children: job.output_name }),
          !!job.output_bytes && jsx("span", { children: fmtBytes(job.output_bytes) }),
          !!job.storage_file_id && jsx("span", { children: `file ${job.storage_file_id}` }),
          job.error && jsx("span", { className: "text-error", children: job.error }),
        ],
      }),
    ],
  });
}

function ProfilesTab({ profiles, projectId, onRefresh, setMessage }) {
  const [form, setForm] = useState({ name: "", test_url: "", cookies_netscape: "" });
  const update = (key, value) => setForm((f) => ({ ...f, [key]: value }));
  const submit = async (e) => {
    e.preventDefault();
    if (!form.name.trim() || !form.cookies_netscape.trim()) return;
    try {
      await request("/profiles", { method: "POST", body: JSON.stringify(form) }, projectId);
      setForm({ name: "", test_url: "", cookies_netscape: "" });
      await onRefresh();
    } catch (err) {
      setMessage(err.message);
    }
  };
  const remove = async (id) => {
    if (!window.confirm("Delete this profile?")) return;
    try {
      await request(`/profiles/${encodeURIComponent(id)}`, { method: "DELETE" }, projectId);
      await onRefresh();
    } catch (err) {
      setMessage(err.message);
    }
  };

  return jsxs("div", {
    className: "p-4 grid gap-4",
    children: [
      jsxs("form", {
        onSubmit: submit,
        className: "border border-border rounded p-3 grid gap-3",
        children: [
          jsxs("div", {
            className: "grid gap-3 md:grid-cols-2",
            children: [
              jsx(Field, { label: "Name", children: jsx(Input, { value: form.name, onChange: (e) => update("name", e.target.value), placeholder: "YouTube account" }) }),
              jsx(Field, { label: "Test URL", children: jsx(Input, { value: form.test_url, onChange: (e) => update("test_url", e.target.value), placeholder: "Optional" }) }),
            ],
          }),
          jsx(Field, {
            label: "Cookies",
            children: jsx("textarea", {
              value: form.cookies_netscape,
              onChange: (e) => update("cookies_netscape", e.target.value),
              className: "bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text font-mono min-h-32",
              placeholder: ".youtube.com\tTRUE\t/\tTRUE\t1893456000\tSID\t...",
            }),
          }),
          jsx("div", { className: "flex justify-end", children: jsx(Button, { tone: "primary", disabled: !form.name.trim() || !form.cookies_netscape.trim(), children: "Save Profile" }) }),
        ],
      }),
      jsx("div", {
        className: "border border-border rounded overflow-hidden",
        children: profiles.length === 0
          ? jsx("div", { className: "p-6 text-center text-sm text-text-dim", children: "No source profiles." })
          : profiles.map((profile) => jsxs("div", {
              className: "flex items-center gap-3 border-b border-border last:border-b-0 p-3",
              children: [
                jsxs("div", {
                  className: "min-w-0 flex-1",
                  children: [
                    jsx("div", { className: "text-sm font-medium truncate", children: profile.name }),
                    jsxs("div", { className: "text-xs text-text-dim", children: [profile.provider, " / ", profile.auth_type, profile.last_error ? ` / ${profile.last_error}` : ""] }),
                  ],
                }),
                jsx("div", { className: "text-xs text-text-dim", children: profile.last_validated_at ? "validated" : profile.status }),
                jsx(Button, { tone: "danger", onClick: () => remove(profile.id), children: "Delete" }),
              ],
            }, profile.id)),
      }),
    ],
  });
}
