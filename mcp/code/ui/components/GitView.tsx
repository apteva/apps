import React, { useCallback, useEffect, useState } from "react";

// GitView — the release side of a Git-backed repository: pull requests with
// human approval, branches, tags, command runs (tests and builds from agents
// and workspaces) and the Git operation log. Everything goes through the
// repository's REST routes; refreshTick bumps when the backend emits a
// repo.git.* or repo.command.finished event for this repository.

type API = <T>(method: string, path: string, body?: unknown, extra?: Record<string, string>) => Promise<T>;

interface PullRequest {
  number: number; url: string; state: string; title: string; head: string; head_sha?: string; base: string;
  draft: boolean; merged: boolean; mergeable?: boolean; mergeable_state?: string; author?: string;
  created_at?: string; approved_by?: string[];
}
interface Branch { name: string; current: boolean; remote: boolean; upstream?: string; sha: string }
interface Tag { name: string; sha: string; annotated: boolean; message?: string; created_at?: string; on_remote?: string }
interface Run {
  id: number; runtime: string; workspace_id?: string; command: string; status: string; exit_code: number;
  timed_out: boolean; duration_ms: number; actor?: string; output_tail?: string; error?: string; created_at: string;
}
interface Operation { id: number; operation: string; actor: string; from_sha?: string; to_sha?: string; status: string; error?: string; created_at: string }

type Section = "pulls" | "branches" | "tags" | "runs" | "activity";
const SECTIONS: Array<{ id: Section; label: string }> = [
  { id: "pulls", label: "Pull requests" },
  { id: "branches", label: "Branches" },
  { id: "tags", label: "Tags" },
  { id: "runs", label: "Runs" },
  { id: "activity", label: "Activity" },
];

const short = (sha?: string) => (sha ? sha.slice(0, 9) : "");
const when = (s?: string) => {
  if (!s) return "";
  const d = new Date(s.includes("T") || s.endsWith("Z") ? s : s.replace(" ", "T") + "Z");
  return isNaN(d.getTime()) ? s : d.toLocaleString();
};
const duration = (ms: number) => (ms < 1000 ? `${ms} ms` : ms < 60000 ? `${(ms / 1000).toFixed(1)} s` : `${Math.floor(ms / 60000)} min ${Math.round((ms % 60000) / 1000)} s`);
const who = (actor?: string) => (!actor ? "" : actor.startsWith("agent:") ? `Agent ${actor.slice(6)}` : actor.startsWith("user:") ? `Person ${actor.split(":").pop()}` : actor);

function Pill({ tone, children }: { tone: "green" | "yellow" | "red" | "muted" | "accent"; children: React.ReactNode }) {
  const cls = {
    green: "bg-green/15 text-green", yellow: "bg-yellow/15 text-yellow", red: "bg-red/15 text-red",
    muted: "bg-border text-text-muted", accent: "bg-accent/15 text-accent",
  }[tone];
  return <span className={`text-xs px-1.5 py-0.5 rounded whitespace-nowrap ${cls}`}>{children}</span>;
}

function Btn({ onClick, disabled, danger, children, title }: { onClick: () => void; disabled?: boolean; danger?: boolean; children: React.ReactNode; title?: string }) {
  return (
    <button type="button" onClick={onClick} disabled={disabled} title={title}
      className={`text-xs px-2 py-1 rounded border border-border disabled:opacity-40 ${danger ? "text-red hover:bg-red/10" : "text-text hover:bg-border"}`}>
      {children}
    </button>
  );
}

export function GitView({ slug, api, refreshTick, gitBacked, defaultBranch }: { slug: string; api: API; refreshTick: number; gitBacked: boolean; defaultBranch?: string }) {
  // A repository without a Git remote still has command runs.
  // The repository's Git status can arrive after this view mounts, so the
  // section is derived: Runs only until it is known to be Git-backed, then
  // the person's pick (Pull requests by default).
  const sections = gitBacked ? SECTIONS : SECTIONS.filter((s) => s.id === "runs");
  const [picked, setSection] = useState<Section | null>(null);
  const section: Section = gitBacked ? (picked ?? "pulls") : "runs";
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");
  const base = `/repos/${encodeURIComponent(slug)}`;

  const [prState, setPrState] = useState<"open" | "closed" | "all">("open");
  const [pulls, setPulls] = useState<PullRequest[] | null>(null);
  const [branches, setBranches] = useState<Branch[] | null>(null);
  const [tags, setTags] = useState<Tag[] | null>(null);
  const [runs, setRuns] = useState<Run[] | null>(null);
  const [ops, setOps] = useState<Operation[] | null>(null);
  const [openRun, setOpenRun] = useState<Run | null>(null);
  const [confirm, setConfirm] = useState("");
  const [method, setMethod] = useState<"merge" | "squash" | "rebase">("squash");
  const [tagName, setTagName] = useState("");
  const [tagMessage, setTagMessage] = useState("");

  const load = useCallback(async () => {
    setError("");
    try {
      if (section === "pulls") setPulls((await api<{ pull_requests?: PullRequest[] }>("GET", `${base}/git/pull-requests`, undefined, { state: prState, limit: "50" })).pull_requests ?? []);
      if (section === "branches") setBranches((await api<{ branches: Branch[] }>("GET", `${base}/git/branches`)).branches);
      if (section === "tags") setTags((await api<{ tags?: Tag[] }>("GET", `${base}/git/tags`, undefined, { compare_remote: "1", limit: "100" })).tags ?? []);
      if (section === "runs") setRuns((await api<{ runs: Run[] }>("GET", `${base}/runs`, undefined, { limit: "50" })).runs);
      if (section === "activity") setOps((await api<{ operations: Operation[] }>("GET", `${base}/git/operations`, undefined, { limit: "200" })).operations);
    } catch (e) { setError((e as Error).message); }
  }, [api, base, section, prState]);

  useEffect(() => { void load(); }, [load, refreshTick]);
  useEffect(() => { setConfirm(""); setNotice(""); setOpenRun(null); }, [section, slug]);

  const act = async (key: string, fn: () => Promise<unknown>, done: string) => {
    setBusy(key); setError(""); setNotice(""); setConfirm("");
    try { await fn(); setNotice(done); await load(); }
    catch (e) { setError((e as Error).message.replace(/^\d+: /, "")); }
    finally { setBusy(""); }
  };
  const pr = (body: Record<string, unknown>) => api("POST", `${base}/git/pull-requests`, body);

  return (
    <div className="flex-1 min-h-0 flex flex-col">
      <div className="px-4 border-b border-border flex items-center gap-4 shrink-0">
        {sections.map((s) => (
          <button key={s.id} type="button" onClick={() => setSection(s.id)}
            className={`py-2 text-sm border-b-2 ${section === s.id ? "border-accent text-text" : "border-transparent text-text-muted hover:text-text"}`}>
            {s.label}
          </button>
        ))}
        <span className="flex-1" />
        <button type="button" onClick={() => void load()} className="text-xs text-text-muted hover:text-text">Refresh</button>
      </div>
      {(error || notice) && (
        <div className={`px-4 py-2 text-xs border-b border-border ${error ? "text-red" : "text-green"}`}>{error || notice}</div>
      )}
      <div className="flex-1 min-h-0 overflow-auto p-4">
        {section === "pulls" && (
          <div className="space-y-3">
            <div className="flex items-center gap-2 text-xs text-text-muted">
              {(["open", "closed", "all"] as const).map((s) => (
                <button key={s} type="button" onClick={() => setPrState(s)} className={`px-2 py-0.5 rounded ${prState === s ? "bg-border text-text" : "hover:text-text"}`}>{s}</button>
              ))}
              <span className="flex-1" />
              <span>Merge method</span>
              <select value={method} onChange={(e) => setMethod(e.target.value as typeof method)} className="bg-bg border border-border rounded px-1 py-0.5 text-xs">
                <option value="squash">squash</option><option value="merge">merge</option><option value="rebase">rebase</option>
              </select>
            </div>
            {pulls === null ? <div className="text-sm text-text-muted">Loading…</div> : pulls.length === 0 ? (
              <div className="text-sm text-text-muted">No {prState === "all" ? "" : prState + " "}pull requests.</div>
            ) : pulls.map((p) => {
              const approved = (p.approved_by ?? []).length > 0;
              const open = p.state === "open";
              return (
                <div key={p.number} className="border border-border rounded p-3 space-y-2">
                  <div className="flex items-start gap-2">
                    <div className="flex-1 min-w-0">
                      <a href={p.url} target="_blank" rel="noreferrer" className="text-sm text-text hover:underline">#{p.number} {p.title}</a>
                      <div className="text-xs text-text-muted font-mono truncate">{p.head} → {p.base} · {short(p.head_sha)}{p.author ? ` · ${p.author}` : ""}{p.created_at ? ` · ${when(p.created_at)}` : ""}</div>
                    </div>
                    <div className="flex items-center gap-1 flex-wrap justify-end">
                      {p.draft && <Pill tone="muted">draft</Pill>}
                      {p.merged ? <Pill tone="accent">merged</Pill> : !open ? <Pill tone="muted">closed</Pill> : approved ? (
                        <Pill tone="green">approved by {(p.approved_by ?? []).map(who).join(", ")}</Pill>
                      ) : <Pill tone="yellow">needs approval</Pill>}
                      {open && p.mergeable === false && <Pill tone="red">conflicts</Pill>}
                    </div>
                  </div>
                  {open && (
                    <div className="flex items-center gap-2">
                      <Btn disabled={!!busy || approved} title="Approve the current head; an agent can then merge it"
                        onClick={() => act(`a${p.number}`, () => pr({ action: "approve", number: p.number, expected_head_sha: p.head_sha }), `Approved #${p.number} at ${short(p.head_sha)}.`)}>
                        {busy === `a${p.number}` ? "Approving…" : approved ? "Approved" : "Approve"}
                      </Btn>
                      {confirm === `m${p.number}` ? (
                        <Btn disabled={!!busy} onClick={() => act(`m${p.number}`, () => pr({ action: "merge", number: p.number, method, expected_head_sha: p.head_sha }), `Merged #${p.number}.`)}>
                          {busy === `m${p.number}` ? "Merging…" : `Confirm ${method} into ${p.base}`}
                        </Btn>
                      ) : <Btn disabled={!!busy} onClick={() => setConfirm(`m${p.number}`)}>Merge</Btn>}
                      {confirm === `c${p.number}` ? (
                        <Btn danger disabled={!!busy} onClick={() => act(`c${p.number}`, () => pr({ action: "close", number: p.number }), `Closed #${p.number}.`)}>Confirm close</Btn>
                      ) : <Btn danger disabled={!!busy} onClick={() => setConfirm(`c${p.number}`)}>Close</Btn>}
                      {confirm && confirm.endsWith(String(p.number)) && <button type="button" className="text-xs text-text-muted hover:text-text" onClick={() => setConfirm("")}>Cancel</button>}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}

        {section === "branches" && (
          branches === null ? <div className="text-sm text-text-muted">Loading…</div> : (
            <table className="w-full text-sm">
              <thead><tr className="text-xs text-text-dim text-left"><th className="py-1">Branch</th><th>Commit</th><th>Upstream</th><th /></tr></thead>
              <tbody>
                {branches.map((b) => {
                  const protectedBranch = b.current || (b.remote && b.name.split("/").slice(1).join("/") === (defaultBranch || "main"));
                  const name = b.remote ? b.name.split("/").slice(1).join("/") : b.name;
                  const where = b.remote ? "remote" : "local";
                  const key = `${where}:${b.name}`;
                  const del = (force: boolean) => act(key, () => api("POST", `${base}/git/branches/delete`, { name, where, force }), `Deleted ${b.name}.`);
                  return (
                    <tr key={key} className="border-t border-border">
                      <td className="py-1.5 font-mono">{b.name}{b.current && <span className="ml-2"><Pill tone="green">checked out</Pill></span>}{b.remote && <span className="ml-2"><Pill tone="muted">origin</Pill></span>}</td>
                      <td className="font-mono text-xs text-text-muted">{short(b.sha)}</td>
                      <td className="font-mono text-xs text-text-muted">{b.upstream}</td>
                      <td className="text-right">
                        {protectedBranch ? null : confirm === key ? (
                          <span className="inline-flex gap-1">
                            <Btn danger disabled={!!busy} onClick={() => del(false)}>Delete</Btn>
                            <Btn danger disabled={!!busy} title="Delete even if its commits are not merged anywhere" onClick={() => del(true)}>Force</Btn>
                            <button type="button" className="text-xs text-text-muted hover:text-text" onClick={() => setConfirm("")}>Cancel</button>
                          </span>
                        ) : <Btn danger disabled={!!busy} onClick={() => setConfirm(key)}>Delete…</Btn>}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )
        )}

        {section === "tags" && (
          <div className="space-y-3">
            <div className="flex items-center gap-2">
              <input value={tagName} onChange={(e) => setTagName(e.target.value)} placeholder="app/v1.2.3" className="bg-bg border border-border rounded px-2 py-1 text-sm font-mono w-48" />
              <input value={tagMessage} onChange={(e) => setTagMessage(e.target.value)} placeholder="Message (optional)" className="bg-bg border border-border rounded px-2 py-1 text-sm flex-1" />
              <Btn disabled={!!busy || !tagName.trim()} onClick={() => act("tag", async () => { await api("POST", `${base}/git/tags`, { action: "create", name: tagName.trim(), message: tagMessage }); setTagName(""); setTagMessage(""); }, `Tagged HEAD as ${tagName.trim()}.`)}>Tag HEAD</Btn>
            </div>
            {tags === null ? <div className="text-sm text-text-muted">Loading…</div> : tags.length === 0 ? <div className="text-sm text-text-muted">No tags.</div> : (
              <table className="w-full text-sm">
                <thead><tr className="text-xs text-text-dim text-left"><th className="py-1">Tag</th><th>Commit</th><th>On origin</th><th>Created</th><th /></tr></thead>
                <tbody>
                  {tags.map((t) => (
                    <tr key={t.name} className="border-t border-border">
                      <td className="py-1.5 font-mono" title={t.message}>{t.name}</td>
                      <td className="font-mono text-xs text-text-muted">{short(t.sha)}</td>
                      <td>{t.on_remote === "same" ? <Pill tone="green">published</Pill> : t.on_remote === "different" ? <Pill tone="red">differs on origin</Pill> : <Pill tone="muted">local only</Pill>}</td>
                      <td className="text-xs text-text-muted">{when(t.created_at)}</td>
                      <td className="text-right">
                        <span className="inline-flex gap-1">
                          {t.on_remote !== "same" && <Btn disabled={!!busy} onClick={() => act(`p${t.name}`, () => api("POST", `${base}/git/tags`, { action: "push", name: t.name }), `Pushed ${t.name}.`)}>Push</Btn>}
                          {t.on_remote !== "same" && <Btn danger disabled={!!busy} onClick={() => act(`d${t.name}`, () => api("POST", `${base}/git/tags`, { action: "delete", name: t.name }), `Deleted local tag ${t.name}.`)}>Delete</Btn>}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        )}

        {section === "runs" && (
          runs === null ? <div className="text-sm text-text-muted">Loading…</div> : runs.length === 0 ? (
            <div className="text-sm text-text-muted">No commands have run in this repository yet. Tests and builds started with repos_run_command show up here.</div>
          ) : (
            <div className="space-y-2">
              {runs.map((r) => {
                const ok = r.status !== "error" && !r.timed_out && r.exit_code === 0 && !r.error;
                const expanded = openRun?.id === r.id;
                return (
                  <div key={r.id} className="border border-border rounded">
                    <button type="button" className="w-full text-left p-2 flex items-center gap-2 hover:bg-border/40"
                      onClick={async () => {
                        if (expanded) { setOpenRun(null); return; }
                        try { setOpenRun(await api<Run>("GET", `${base}/runs/${r.id}`)); } catch (e) { setError((e as Error).message); }
                      }}>
                      {ok ? <Pill tone="green">passed</Pill> : r.timed_out ? <Pill tone="red">timed out</Pill> : <Pill tone="red">{r.error && r.exit_code === 0 ? "error" : `exit ${r.exit_code}`}</Pill>}
                      <span className="font-mono text-sm truncate flex-1">{r.command}</span>
                      <span className="text-xs text-text-muted whitespace-nowrap">{r.runtime}{r.workspace_id ? ` ${r.workspace_id}` : ""} · {duration(r.duration_ms)} · {who(r.actor)} · {when(r.created_at)}</span>
                    </button>
                    {expanded && openRun && (
                      <div className="border-t border-border p-2 space-y-1">
                        {openRun.error && <div className="text-xs text-red">{openRun.error}</div>}
                        <pre className="text-xs font-mono whitespace-pre-wrap max-h-96 overflow-auto text-text-muted">{openRun.output_tail || "(no output)"}</pre>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          )
        )}

        {section === "activity" && (
          ops === null ? <div className="text-sm text-text-muted">Loading…</div> : ops.length === 0 ? <div className="text-sm text-text-muted">No Git activity yet.</div> : (
            <table className="w-full text-sm">
              <thead><tr className="text-xs text-text-dim text-left"><th className="py-1">When</th><th>Operation</th><th>By</th><th>Commits</th><th>Result</th></tr></thead>
              <tbody>
                {ops.map((o) => (
                  <tr key={o.id} className="border-t border-border align-top">
                    <td className="py-1.5 text-xs text-text-muted whitespace-nowrap pr-2">{when(o.created_at)}</td>
                    <td className="font-mono text-xs pr-2 whitespace-nowrap">{o.operation.replace(/_/g, " ")}</td>
                    <td className="text-xs pr-2 whitespace-nowrap">{who(o.actor)}</td>
                    <td className="font-mono text-xs text-text-muted pr-2 whitespace-nowrap">{short(o.from_sha)}{o.from_sha && o.to_sha && o.from_sha !== o.to_sha ? " → " : ""}{o.from_sha !== o.to_sha ? short(o.to_sha) : ""}</td>
                    <td className="text-xs">{o.status === "ok" ? <Pill tone="green">ok</Pill> : <span className="text-red">{o.error || o.status}</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )
        )}
      </div>
    </div>
  );
}
