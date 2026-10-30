import { useScopedRevision } from "./live-events";
import { useEffect, useState } from "react";
import type { StepRun } from "./Workflow";

type API = (path: string, method?: string, body?: unknown) => Promise<any>;
type Criterion = { key: string; label: string; type: "score" | "pass_fail" | "choice"; options?: string[] };
type Quality = { scale: "stars" | "thumbs"; tags: string[]; criteria: Criterion[]; low_score: number };
export type RunRatingSummary = { average: number; count: number; latest: number; step_ratings: number };
type Rating = {
  id: string;
  run_id: string;
  step_id?: string;
  step_key?: string;
  rater: string;
  source: string;
  revision: number;
  score: number;
  criteria: Record<string, unknown>;
  tags: string[];
  comment?: string;
  corrected_output?: string;
  created_at: string;
};
type ReviewItem = {
  id: string;
  run_id: string;
  procedure_version: number;
  reason: "low_score" | "failed" | "sample";
  state: string;
  assignee?: string;
  resolution?: string;
  revision: number;
  created_at: string;
  run_state?: string;
  rating?: RunRatingSummary;
};

const terminal = new Set(["completed", "failed", "cancelled"]);
const reasonLabel: Record<string, string> = { low_score: "Low score", failed: "Failed run", sample: "Sampled" };
const ratingEvent = (topic: string) => /^(run\.rat|review\.)/.test(topic);
const key = () => (globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random()}`);
const when = (s: string) => new Date(s).toLocaleString();
const normalize = (r: any): { ratings: Rating[]; quality: Quality; summary?: RunRatingSummary } => ({
  ratings: Array.isArray(r?.ratings) ? r.ratings : [],
  quality: { scale: r?.quality?.scale === "thumbs" ? "thumbs" : "stars", tags: r?.quality?.tags || [], criteria: r?.quality?.criteria || [], low_score: r?.quality?.low_score ?? 2 },
  summary: r?.summary || undefined,
});

export const qualityStyles = `
.ap-processes .rating-card{display:grid;gap:12px}
.ap-processes .rating-stars{display:inline-flex;gap:2px;align-items:center}
.ap-processes .rating-stars button,.ap-processes .rating-thumbs button{background:transparent;border:1px solid transparent;padding:4px;border-radius:8px;color:var(--pc-muted);cursor:pointer;line-height:0}
.ap-processes .rating-stars button:hover,.ap-processes .rating-thumbs button:hover{border-color:var(--pc-line)}
.ap-processes .rating-stars button[aria-pressed=true],.ap-processes .rating-stars button.lit{color:#e3b86d}
.ap-processes .rating-thumbs{display:inline-flex;gap:8px}
.ap-processes .rating-thumbs button[aria-pressed=true]{color:var(--pc-accent);border-color:var(--pc-accent)}
.ap-processes .rating-tags{display:flex;flex-wrap:wrap;gap:6px}
.ap-processes .rating-tags button{border-radius:999px;padding:3px 10px;font-size:12px}
.ap-processes .rating-tags button[aria-pressed=true]{border-color:var(--pc-accent);color:var(--pc-accent)}
.ap-processes .rating-list{display:grid;gap:8px;margin:0;padding:0;list-style:none}
.ap-processes .rating-list li{border-top:1px solid var(--pc-line);padding-top:8px}
.ap-processes .rating-badge{display:inline-flex;align-items:center;gap:3px;font-size:11px;font-weight:600;color:#e3b86d;white-space:nowrap}
.ap-processes .rating-badge.low{color:#f08b8b}
.ap-processes .quality-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:12px}
.ap-processes .quality-stat strong{display:block;font-size:22px;line-height:1.2}
.ap-processes .quality-table{width:100%;border-collapse:collapse;font-size:13px}
.ap-processes .quality-table th,.ap-processes .quality-table td{text-align:left;padding:6px 8px;border-bottom:1px solid var(--pc-line);vertical-align:middle}
.ap-processes .quality-table th{color:var(--pc-muted);font-weight:500}
.ap-processes .quality-dist{display:flex;align-items:flex-end;gap:2px;height:22px}
.ap-processes .quality-dist span{width:8px;background:var(--pc-accent);opacity:.75;border-radius:2px 2px 0 0;min-height:1px}
.ap-processes .review-item{display:grid;gap:8px;padding:12px 0;border-top:1px solid var(--pc-line)}
`;

function StarIcon({ filled }: { filled: boolean }) {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" aria-hidden="true">
      <path d="M12 3.2l2.7 5.5 6.1.9-4.4 4.3 1 6.1L12 17.1l-5.4 2.9 1-6.1-4.4-4.3 6.1-.9z" fill={filled ? "currentColor" : "none"} stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" />
    </svg>
  );
}
function ThumbIcon({ down }: { down?: boolean }) {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" aria-hidden="true" style={down ? { transform: "rotate(180deg)" } : undefined}>
      <path d="M7 10v10H4V10zm3 10h7.2a2 2 0 0 0 2-1.6l1.2-6A2 2 0 0 0 18.4 10H14l.8-3.9A1.7 1.7 0 0 0 13.1 4L10 10z" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" />
    </svg>
  );
}
export function RatingBadge({ rating }: { rating?: RunRatingSummary | null }) {
  if (!rating || !rating.count) return null;
  const low = rating.average <= 2;
  return (
    <span className={`rating-badge ${low ? "low" : ""}`} title={`${rating.count} run rating${rating.count === 1 ? "" : "s"}`}>
      <svg width="12" height="12" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3.2l2.7 5.5 6.1.9-4.4 4.3 1 6.1L12 17.1l-5.4 2.9 1-6.1-4.4-4.3 6.1-.9z" fill="currentColor" /></svg>
      {rating.average.toFixed(1)}
    </span>
  );
}
function ScoreInput({ scale, value, onChange }: { scale: Quality["scale"]; value: number; onChange: (n: number) => void }) {
  const [hover, setHover] = useState(0);
  if (scale === "thumbs")
    return (
      <div className="rating-thumbs" role="group" aria-label="Rating">
        <button type="button" aria-label="Thumbs up" aria-pressed={value === 5} onClick={() => onChange(5)}><ThumbIcon /></button>
        <button type="button" aria-label="Thumbs down" aria-pressed={value === 1} onClick={() => onChange(1)}><ThumbIcon down /></button>
      </div>
    );
  return (
    <div className="rating-stars" role="group" aria-label="Rating" onMouseLeave={() => setHover(0)}>
      {[1, 2, 3, 4, 5].map((n) => (
        <button key={n} type="button" aria-label={`${n} star${n === 1 ? "" : "s"}`} aria-pressed={value === n} className={(hover || value) >= n ? "lit" : ""} onMouseEnter={() => setHover(n)} onClick={() => onChange(n)}>
          <StarIcon filled={(hover || value) >= n} />
        </button>
      ))}
    </div>
  );
}
function scoreText(scale: Quality["scale"], score: number) {
  if (scale === "thumbs") return score >= 5 ? "Thumbs up" : "Thumbs down";
  return `${score}/5`;
}
function raterName(rater: string, agentName: (id: number) => string) {
  if (rater.startsWith("agent:")) return agentName(Number(rater.slice(6)));
  return rater === "operator" ? "Operator" : rater;
}

export function RunRating({ api, runID, runState, steps, agentName, source = "operator" }: {
  api: API;
  runID: string;
  runState: string;
  steps: StepRun[];
  agentName: (id: number) => string;
  source?: "operator" | "reviewer";
}) {
  const [data, setData] = useState<{ ratings: Rating[]; quality: Quality; summary?: RunRatingSummary } | null>(null);
  const [target, setTarget] = useState("");
  const [score, setScore] = useState(0);
  const [tags, setTags] = useState<string[]>([]);
  const [criteria, setCriteria] = useState<Record<string, unknown>>({});
  const [comment, setComment] = useState("");
  const [corrected, setCorrected] = useState("");
  const [more, setMore] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const live = useScopedRevision((e) => e.data?.run_id === runID && ratingEvent(e.topic));
  const finished = terminal.has(runState);
  useEffect(() => {
    if (!finished) return;
    let alive = true;
    api(`/runs/${encodeURIComponent(runID)}/ratings`).then((r) => alive && setData(normalize(r))).catch((e) => alive && setError(e.message));
    return () => { alive = false; };
  }, [runID, finished, live]);
  const mine = data?.ratings.find((r) => r.rater === "operator" && (r.step_id || "") === target);
  useEffect(() => {
    setScore(mine?.score || 0);
    setTags(mine?.tags || []);
    setCriteria(mine?.criteria || {});
    setComment(mine?.comment || "");
    setCorrected(mine?.corrected_output || "");
    setNotice("");
  }, [target, mine?.id]);
  if (!finished)
    return <p className="small muted">Rating opens when the run finishes.</p>;
  if (!data) return error ? <p className="notice" role="alert">{error}</p> : <p role="status" className="small muted">Loading ratings…</p>;
  const q = data.quality;
  const needsTag = score > 0 && score <= q.low_score && tags.length === 0;
  const save = async () => {
    setBusy(true);
    setError("");
    try {
      const body: Record<string, unknown> = { score, tags, criteria, comment, corrected_output: corrected, source, idempotency_key: key() };
      if (target) body.step_id = target;
      const r = await api(`/runs/${encodeURIComponent(runID)}/ratings`, "POST", body);
      setNotice(r.review_item ? "Rating saved. A review item was opened for this run." : "Rating saved.");
      setData(normalize(await api(`/runs/${encodeURIComponent(runID)}/ratings`)));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const ratable = steps.filter((s) => terminal.has(s.state));
  return (
    <section className="card rating-card" aria-label="Run rating">
      <div className="row between">
        <h2 style={{ margin: 0 }}>{mine ? "Your rating" : data.ratings.length ? "Ratings" : "Rate this run"}</h2>
        {data.summary && data.summary.count > 0 && <span className="small muted">Run average {data.summary.average.toFixed(1)} · {data.summary.count} rating{data.summary.count === 1 ? "" : "s"}</span>}
      </div>
      {ratable.length > 0 && (
        <div className="field">
          <label htmlFor={`rating-target-${runID}`}>Rate</label>
          <select id={`rating-target-${runID}`} value={target} onChange={(e) => setTarget(e.target.value)}>
            <option value="">Whole run</option>
            {ratable.map((s, i) => <option key={s.id} value={s.id}>Step {i + 1}: {s.definition.name || s.key}{s.executor.kind === "agent" && s.executor.agent_id ? ` · ${agentName(s.executor.agent_id)}` : ""}</option>)}
          </select>
        </div>
      )}
      <p className="small muted" style={{ margin: 0 }}>Your review of this outcome as an outside reviewer. Agents that worked on the run cannot rate it.</p>
      <ScoreInput scale={q.scale} value={score} onChange={setScore} />
      <div className="rating-tags" role="group" aria-label="Reason tags">
        {q.tags.map((t) => (
          <button key={t} type="button" aria-pressed={tags.includes(t)} onClick={() => setTags((x) => (x.includes(t) ? x.filter((y) => y !== t) : [...x, t]))}>{t}</button>
        ))}
      </div>
      {needsTag && <p className="small muted">Pick at least one reason for a low rating.</p>}
      {q.criteria.length > 0 && (
        <table className="quality-table" aria-label="Rubric">
          <tbody>
            {q.criteria.map((c) => (
              <tr key={c.key}>
                <th scope="row">{c.label}</th>
                <td>
                  {c.type === "score" ? (
                    <ScoreInput scale="stars" value={Number(criteria[c.key] || 0)} onChange={(n) => setCriteria((x) => ({ ...x, [c.key]: n }))} />
                  ) : c.type === "pass_fail" ? (
                    <select aria-label={c.label} value={criteria[c.key] === undefined ? "" : String(criteria[c.key])} onChange={(e) => setCriteria((x) => { const next = { ...x }; if (e.target.value === "") delete next[c.key]; else next[c.key] = e.target.value === "true"; return next; })}>
                      <option value="">Not rated</option>
                      <option value="true">Pass</option>
                      <option value="false">Fail</option>
                    </select>
                  ) : (
                    <select aria-label={c.label} value={String(criteria[c.key] || "")} onChange={(e) => setCriteria((x) => { const next = { ...x }; if (!e.target.value) delete next[c.key]; else next[c.key] = e.target.value; return next; })}>
                      <option value="">Not rated</option>
                      {c.options?.map((o) => <option key={o}>{o}</option>)}
                    </select>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <div className="field">
        <label htmlFor={`rating-comment-${runID}`}>Comment</label>
        <textarea id={`rating-comment-${runID}`} rows={2} maxLength={2000} value={comment} onChange={(e) => setComment(e.target.value)} placeholder="What was good or wrong about this outcome?" />
      </div>
      {more ? (
        <div className="field">
          <label htmlFor={`rating-corrected-${runID}`}>Corrected output</label>
          <textarea id={`rating-corrected-${runID}`} rows={4} value={corrected} onChange={(e) => setCorrected(e.target.value)} placeholder="What the result should have been" />
        </div>
      ) : (
        <button type="button" className="crumb" style={{ justifySelf: "start", margin: 0 }} onClick={() => setMore(true)}>Add corrected output</button>
      )}
      {error && <p className="notice" role="alert">{error}</p>}
      {notice && <p className="small muted" role="status">{notice}</p>}
      <div className="row">
        <button type="button" className="primary" disabled={busy || !score || needsTag} onClick={save}>{mine ? "Update rating" : "Save rating"}</button>
      </div>
      {data.ratings.length > 0 && (
        <ul className="rating-list" aria-label="Saved ratings">
          {data.ratings.map((r) => (
            <li key={r.id}>
              <div className="row between">
                <strong className="small">{r.step_key ? `Step ${r.step_key}` : "Whole run"} · {scoreText(q.scale, r.score)}</strong>
                <span className="small muted">{raterName(r.rater, agentName)}{r.source === "reviewer" ? " (reviewer)" : ""} · {when(r.created_at)}{r.revision > 1 ? ` · revision ${r.revision}` : ""}</span>
              </div>
              {r.tags.length > 0 && <div className="small muted">{r.tags.join(" · ")}</div>}
              {r.comment && <div className="small">{r.comment}</div>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

export function QualityTab({ api, agentName, currentVersion, onOpenRun }: {
  api: API;
  agentName: (id: number) => string;
  currentVersion: number;
  onOpenRun: (runID: string) => void;
}) {
  const [quality, setQuality] = useState<any>(null);
  const [reviews, setReviews] = useState<{ items: ReviewItem[]; active_count: number } | null>(null);
  const [reviewState, setReviewState] = useState("active");
  const [resolution, setResolution] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const live = useScopedRevision((e) => ratingEvent(e.topic) || e.topic === "run.state_changed");
  const load = async () => {
    const [q, r] = await Promise.all([api("/quality"), api(`/reviews?state=${reviewState}&limit=50`)]);
    setQuality({ versions: [], tags: [], executors: [], criteria: [], worst_runs: [], review: {}, ...q });
    setReviews({ items: Array.isArray(r?.items) ? r.items : [], active_count: r?.active_count || 0 });
  };
  useEffect(() => { load().catch((e) => setError(e.message)); }, [live, reviewState]);
  const update = async (item: ReviewItem, state: string) => {
    setBusy(true);
    setError("");
    try {
      await api(`/reviews/${encodeURIComponent(item.id)}`, "POST", { state, resolution: resolution[item.id] || "", expected_revision: item.revision });
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  if (!quality) return error ? <p className="notice" role="alert">{error}</p> : <p role="status">Loading quality…</p>;
  const current = quality.versions.find((v: any) => v.version === currentVersion) || quality.versions[0];
  const thumbs = quality.scale === "thumbs";
  return (
    <div className="stack" style={{ display: "grid", gap: 16 }}>
      <div className="quality-grid">
        <div className="card quality-stat"><span className="small muted">Average rating{current ? ` · v${current.version}` : ""}</span><strong>{current?.average != null ? (thumbs ? `${Math.round(((current.average - 1) / 4) * 100)}% up` : current.average.toFixed(2)) : "—"}</strong></div>
        <div className="card quality-stat"><span className="small muted">Rating coverage</span><strong>{current ? `${Math.round(current.coverage * 100)}%` : "—"}</strong><span className="small muted">{current ? `${current.rated_runs} of ${current.finished_runs} finished runs` : "No finished runs yet"}</span></div>
        <div className="card quality-stat"><span className="small muted">Needs review</span><strong>{(quality.review.open || 0) + (quality.review.claimed || 0)}</strong><span className="small muted">{quality.review.claimed || 0} claimed</span></div>
      </div>
      {error && <p className="notice" role="alert">{error}</p>}
      <section className="card">
        <h2>Quality by version</h2>
        {quality.versions.length ? (
          <table className="quality-table">
            <thead><tr><th>Version</th><th>Finished</th><th>Rated</th><th>Coverage</th><th>Average</th><th>Distribution (1–5)</th></tr></thead>
            <tbody>
              {quality.versions.map((v: any) => {
                const max = Math.max(1, ...Object.values(v.distribution as Record<string, number>));
                return (
                  <tr key={v.version}>
                    <td>v{v.version}{v.version === quality.current_version ? " (current)" : ""}</td>
                    <td>{v.finished_runs}</td>
                    <td>{v.rated_runs}</td>
                    <td>{Math.round(v.coverage * 100)}%</td>
                    <td>{v.average != null ? v.average.toFixed(2) : "—"}</td>
                    <td><div className="quality-dist" aria-label={`Scores 1 to 5: ${[1, 2, 3, 4, 5].map((n) => v.distribution[n]).join(", ")}`}>{[1, 2, 3, 4, 5].map((n) => <span key={n} title={`${n}: ${v.distribution[n]}`} style={{ height: `${(v.distribution[n] / max) * 100}%` }} />)}</div></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        ) : <p className="small muted">No finished runs yet. Ratings appear here once runs finish and are rated.</p>}
      </section>
      <div className="quality-grid" style={{ gridTemplateColumns: "repeat(auto-fit,minmax(280px,1fr))" }}>
        <section className="card">
          <h2>Top reasons</h2>
          {quality.tags.length ? <table className="quality-table"><tbody>{quality.tags.map((t: any) => <tr key={t.tag}><td>{t.tag}</td><td>{t.count}</td></tr>)}</tbody></table> : <p className="small muted">No reason tags yet.</p>}
        </section>
        <section className="card">
          <h2>Step quality by agent</h2>
          {quality.executors.length ? <table className="quality-table"><thead><tr><th>Agent</th><th>Step ratings</th><th>Average</th></tr></thead><tbody>{quality.executors.map((x: any) => <tr key={x.agent_id}><td>{agentName(x.agent_id)}</td><td>{x.step_ratings}</td><td>{x.average.toFixed(2)}</td></tr>)}</tbody></table> : <p className="small muted">No step ratings yet.</p>}
        </section>
        {quality.criteria.length > 0 && (
          <section className="card">
            <h2>Rubric</h2>
            <table className="quality-table"><tbody>{quality.criteria.map((c: any) => <tr key={c.key}><td>{c.label}</td><td>{c.count ? c.type === "score" ? `${c.average.toFixed(2)} avg` : c.type === "pass_fail" ? `${Math.round(c.pass_rate * 100)}% pass` : Object.entries(c.choices || {}).map(([k, n]) => `${k} ${n}`).join(" · ") : "—"}</td><td className="small muted">{c.count} rated</td></tr>)}</tbody></table>
          </section>
        )}
      </div>
      {quality.worst_runs.length > 0 && (
        <section className="card">
          <h2>Lowest-rated runs</h2>
          <table className="quality-table"><tbody>{quality.worst_runs.map((w: any) => <tr key={w.run_id}><td>{thumbs ? scoreText("thumbs", w.score) : `${w.score}/5`}</td><td>v{w.procedure_version}</td><td className="small muted">{(w.tags || []).join(" · ")}</td><td className="small muted">{when(w.rated_at)}</td><td><button type="button" onClick={() => onOpenRun(w.run_id)}>Open run</button></td></tr>)}</tbody></table>
        </section>
      )}
      <section className="card" aria-label="Review queue">
        <div className="row between">
          <h2 style={{ margin: 0 }}>Review queue</h2>
          <select aria-label="Review state" value={reviewState} onChange={(e) => setReviewState(e.target.value)}>
            <option value="active">Open and claimed</option>
            <option value="resolved">Resolved</option>
            <option value="dismissed">Dismissed</option>
            <option value="all">All</option>
          </select>
        </div>
        {reviews && reviews.items.length === 0 && <p className="small muted">Nothing to review. Low ratings, failed runs and sampled runs land here when the procedure asks for them.</p>}
        {reviews?.items.map((item) => (
          <div className="review-item" key={item.id}>
            <div className="row between">
              <strong>{reasonLabel[item.reason] || item.reason} · v{item.procedure_version}</strong>
              <span className="small muted">{item.state}{item.assignee ? ` · ${raterName(item.assignee, agentName)}` : ""} · {when(item.created_at)}</span>
            </div>
            <div className="row small muted">
              Run {item.run_state}{item.rating?.count ? <> · <RatingBadge rating={item.rating} /></> : " · not rated"}
            </div>
            {item.resolution && <div className="small">{item.resolution}</div>}
            {(item.state === "open" || item.state === "claimed") && (
              <>
                <input aria-label="Resolution" placeholder="What was done (needed to resolve or dismiss)" maxLength={500} value={resolution[item.id] || ""} onChange={(e) => setResolution((x) => ({ ...x, [item.id]: e.target.value }))} />
                <div className="row">
                  <button type="button" onClick={() => onOpenRun(item.run_id)}>Open run</button>
                  {item.state === "open" ? <button type="button" disabled={busy} onClick={() => update(item, "claimed")}>Claim</button> : <button type="button" disabled={busy} onClick={() => update(item, "open")}>Release</button>}
                  <button type="button" className="primary" disabled={busy || !resolution[item.id]?.trim()} onClick={() => update(item, "resolved")}>Resolve</button>
                  <button type="button" disabled={busy || !resolution[item.id]?.trim()} onClick={() => update(item, "dismissed")}>Dismiss</button>
                </div>
              </>
            )}
          </div>
        ))}
      </section>
    </div>
  );
}
