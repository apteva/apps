import { ChangeEvent, FormEvent, useEffect, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { api, type LessonBundle } from "./api";
import type {
  Assignment,
  AssignmentReviewItem,
  AssignmentSubmission,
  IssuedCertificate,
  LearningStatus,
  Member,
  MemberMilestone,
  Quiz,
  QuizAttempt,
  QuizQuestion,
} from "./types";

export function Markdown({ body }: { body: string }) {
  return (
    <div className="markdown">
      <ReactMarkdown skipHtml remarkPlugins={[remarkGfm]}>
        {body}
      </ReactMarkdown>
    </div>
  );
}

function message(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

function FileLink({
  lessonId,
  fileId,
  label,
}: {
  lessonId: string;
  fileId: string;
  label: string;
}) {
  const [url, setURL] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function prepare() {
    setBusy(true);
    setError("");
    try {
      const result = await api.courses.fileURL(lessonId, fileId);
      setURL(result.url);
    } catch (caught) {
      setError(message(caught));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div>
      {url ? (
        <a href={url} target="_blank" rel="noopener noreferrer">
          Open {label}
        </a>
      ) : (
        <button className="secondary" disabled={busy} onClick={prepare}>
          {busy ? "Preparing…" : `Open ${label}`}
        </button>
      )}
      {error && (
        <p role="alert">
          {error}{" "}
          <button className="text-button" onClick={prepare}>
            Retry
          </button>
        </p>
      )}
    </div>
  );
}

export function SubmissionFileLink({ fileId, target, memberId }: { fileId: string; target: { assignment_id?: string; definition_id?: string }; memberId?: string }) {
  const [url, setURL] = useState(""); const [error, setError] = useState("");
  async function open() { try { setURL((await api.courses.submissionFileURL(fileId, target, memberId)).url); } catch (e) { setError(message(e)); } }
  return <span className="file-chip">{url ? <a href={url} target="_blank" rel="noopener noreferrer">Download file</a> : <button type="button" className="text-button" onClick={() => void open()}>Open file</button>}{error && <small role="alert">{error}</small>}</span>;
}

async function encodeFile(file: File): Promise<string> {
  const bytes = new Uint8Array(await file.arrayBuffer()); let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}

function LessonVideo({
  lessonId,
  fileId,
}: {
  lessonId: string;
  fileId: string;
}) {
  const [url, setURL] = useState("");
  const [error, setError] = useState("");
  const [version, setVersion] = useState(0);
  useEffect(() => {
    let active = true;
    setURL("");
    setError("");
    api.courses
      .fileURL(lessonId, fileId)
      .then((result) => {
        if (active) setURL(result.url);
      })
      .catch((caught) => {
        if (active) setError(message(caught));
      });
    return () => {
      active = false;
    };
  }, [lessonId, fileId, version]);
  return (
    <section aria-label="Lesson video">
      {url && (
        <video
          controls
          preload="metadata"
          src={url}
          onError={() =>
            setError("Video could not be loaded. Renew the link to try again.")
          }
        />
      )}
      {!url && !error && <p role="status">Loading video…</p>}
      {error && (
        <p role="alert">
          {error}{" "}
          <button
            className="secondary"
            onClick={() => setVersion((v) => v + 1)}
          >
            Retry video
          </button>
        </p>
      )}
    </section>
  );
}

export function QuizForm({
  quiz,
  previous,
  onSaved,
}: {
  quiz: Quiz;
  previous?: QuizAttempt;
  onSaved: () => void;
}) {
  const questions = Array.isArray(quiz.questions)
    ? (quiz.questions as QuizQuestion[])
    : [];
  const valid =
    questions.length > 0 &&
    questions.every(
      (q) =>
        q != null &&
        typeof q.prompt === "string" &&
        Array.isArray(q.options) &&
        q.options.length >= 2 &&
        q.options.every((option) => typeof option === "string"),
    );
  const [answers, setAnswers] = useState<Record<number, number>>({});
  const [result, setResult] = useState(previous);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    setResult(previous);
  }, [previous]);
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      setResult(
        await api.courses.submitQuiz(
          quiz.id,
          questions.map((_, i) => answers[i]),
        ),
      );
      onSaved();
    } catch (caught) {
      setError(message(caught));
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="card stack" onSubmit={submit}>
      <h4>{quiz.title}</h4>
      <p>Pass mark {quiz.passing_score}%</p>
      {valid ? (
        <>
          {questions.map((q, i) => (
            <fieldset key={i}>
              <legend>{q.prompt}</legend>
              {q.options.map((option, j) => (
                <label className="quiz-option" key={j}>
                  <input
                    type="radio"
                    name={`${quiz.id}-${i}`}
                    value={j}
                    checked={answers[i] === j}
                    required
                    onChange={() =>
                      setAnswers((current) => ({ ...current, [i]: j }))
                    }
                  />
                  {option}
                </label>
              ))}
            </fieldset>
          ))}
          <button
            className="primary"
            disabled={busy || Object.keys(answers).length !== questions.length}
          >
            {busy ? "Submitting…" : "Submit answers"}
          </button>
        </>
      ) : (
        <p>This quiz needs to be configured by the course operator.</p>
      )}
      {result && (
        <p role="status">
          Score: {result.score}% · {result.passed ? "Passed" : "Try again"}
        </p>
      )}
      {error && <p role="alert">{error}</p>}
    </form>
  );
}

function AssignmentForm({
  assignment,
  lessonId,
  previous,
  onSaved,
}: {
  assignment: Assignment;
  lessonId: string;
  previous?: AssignmentSubmission;
  onSaved: () => void;
}) {
  const [body, setBody] = useState(previous?.body || "");
  const [links, setLinks] = useState((previous?.links || []).join("\n"));
  const [files, setFiles] = useState((previous?.files || []).join("\n"));
  const [fileNames, setFileNames] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(!!previous);
  useEffect(() => {
    if (previous) {
      setBody(previous.body);
      setLinks((previous.links || []).join("\n"));
      setFiles((previous.files || []).join("\n"));
      setSaved(true);
    }
  }, [previous]);
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.courses.submitAssignment(
        assignment.id,
        body,
        links.split(/\n|,/).map((value) => value.trim()).filter(Boolean),
        files.split(/\n|,/).map((value) => value.trim()).filter(Boolean),
      );
      setSaved(true);
      onSaved();
    } catch (caught) {
      setError(message(caught));
    } finally {
      setBusy(false);
    }
  }
  async function upload(event: ChangeEvent<HTMLInputElement>) {
    const picked = event.target.files?.[0]; if (!picked) return; setError("");
    if (picked.size > 25 * 1024 * 1024) { setError("Files must be 25 MB or smaller."); return; }
    try { const uploaded = await api.courses.uploadFile(picked.name, await encodeFile(picked), picked.type || "application/octet-stream", { assignment_id: assignment.id }); setFiles((value) => [...value.split(/\n|,/).map((v) => v.trim()).filter(Boolean), uploaded.file_id].join("\n")); setFileNames((value) => [...value, picked.name]); setSaved(false); }
    catch (caught) { setError(message(caught)); }
    event.target.value = "";
  }
  return (
    <form className="card stack" onSubmit={submit}>
      <h4>{assignment.title}</h4>
      <Markdown body={assignment.instructions} />
      {assignment.attachment_storage_file_id && (
        <FileLink
          lessonId={lessonId}
          fileId={assignment.attachment_storage_file_id}
          label="assignment attachment"
        />
      )}
      <label htmlFor={`submission-${assignment.id}`}>Your submission</label>
      <textarea
        id={`submission-${assignment.id}`}
        value={body}
        onChange={(e) => {
          setBody(e.target.value);
          setSaved(false);
        }}
        maxLength={100000}
      />
      <label htmlFor={`links-${assignment.id}`}>Evidence links (one per line)</label>
      <textarea
        id={`links-${assignment.id}`}
        value={links}
        onChange={(e) => { setLinks(e.target.value); setSaved(false); }}
        maxLength={4000}
      />
      <label htmlFor={`upload-${assignment.id}`}>Attachments</label>
      <input id={`upload-${assignment.id}`} type="file" onChange={(e) => void upload(e)} />
      <div className="file-list">{files.split(/\n|,/).map((id) => id.trim()).filter(Boolean).map((id, i) => <SubmissionFileLink key={id} fileId={id} target={{ assignment_id: assignment.id }} />)}{fileNames.map((name) => <span key={name} className="muted">{name}</span>)}</div>
      <button className="primary" disabled={busy || (!body.trim() && !links.trim() && !files.trim())}>
        {busy ? "Submitting…" : previous?.status === "needs_changes" ? "Resubmit for review" : "Save submission"}
      </button>
      {saved && <p role="status">Submission saved. Status: {previous?.status || "submitted"}. {previous?.feedback || ""}</p>}
      {error && <p role="alert">{error}</p>}
    </form>
  );
}

export function LearningContent({ bundle }: { bundle: LessonBundle }) {
  const [status, setStatus] = useState<LearningStatus>({
    attempts: [],
    submissions: [],
  });
  const [error, setError] = useState("");
  const [version, setVersion] = useState(0);
  useEffect(() => {
    let active = true;
    api.courses
      .learningStatus(bundle.lesson.id)
      .then((value) => {
        if (active) {
          setStatus(value);
          setError("");
        }
      })
      .catch((caught) => {
        if (active) setError(message(caught));
      });
    return () => {
      active = false;
    };
  }, [bundle.lesson.id, version]);
  return (
    <>
      {bundle.lesson.video_storage_key && (
        <LessonVideo
          lessonId={bundle.lesson.id}
          fileId={bundle.lesson.video_storage_key}
        />
      )}
      <Markdown body={bundle.lesson.body} />
      {bundle.resources.length > 0 && (
        <section>
          <h3>Resources</h3>
          <ul>
            {bundle.resources.map((resource) => (
              <li key={resource.id}>
                <FileLink
                  lessonId={bundle.lesson.id}
                  fileId={resource.storage_file_id}
                  label={resource.name || "resource"}
                />
              </li>
            ))}
          </ul>
        </section>
      )}
      {error && (
        <p role="alert">
          Learning results could not be loaded. {error}{" "}
          <button onClick={() => setVersion((v) => v + 1)}>Retry</button>
        </p>
      )}
      {bundle.assignments.length > 0 && (
        <section>
          <h3>Assignments</h3>
          {bundle.assignments.map((assignment) => (
            <AssignmentForm
              key={assignment.id}
              lessonId={bundle.lesson.id}
              assignment={assignment}
              previous={status.submissions.find(
                (s) => s.assignment_id === assignment.id,
              )}
              onSaved={() => setVersion((v) => v + 1)}
            />
          ))}
        </section>
      )}
      {bundle.quizzes.length > 0 && (
        <section>
          <h3>Quizzes</h3>
          {bundle.quizzes.map((quiz) => (
            <QuizForm
              key={quiz.id}
              quiz={quiz}
              previous={status.attempts.find((a) => a.quiz_id === quiz.id)}
              onSaved={() => setVersion((v) => v + 1)}
            />
          ))}
        </section>
      )}
    </>
  );
}

export function CertificateCard({
  certificate,
}: {
  certificate: IssuedCertificate;
}) {
  return (
    <section className="certificate" aria-label="Earned certificate">
      <h3>{certificate.title}</h3>
      <Markdown body={certificate.body} />
      <p>
        Issued {new Date(certificate.issued_at).toLocaleDateString()} ·
        Certificate {certificate.id}
      </p>
      <button className="secondary" onClick={() => window.print()}>
        Print certificate
      </button>
    </section>
  );
}

export function InstructorReviewQueue({ items, onReviewed }: { items: AssignmentReviewItem[]; onReviewed: () => void }) {
  const [feedback, setFeedback] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  async function review(item: AssignmentReviewItem, status: "approved" | "needs_changes") {
    const key = `${item.assignment_id}:${item.member_id}`;
    setBusy(key); setError("");
    try { await api.courses.reviewAssignment(item.assignment_id, item.member_id, status, feedback[key] || ""); onReviewed(); }
    catch (caught) { setError(message(caught)); }
    finally { setBusy(""); }
  }
  return <section className="review-queue" aria-label="Instructor review queue"><div className="section-heading"><div><p className="eyebrow">Instructor workspace</p><h3>Assignment review queue</h3></div><span className="muted">{items.length} awaiting review</span></div>{items.map((item) => { const key = `${item.assignment_id}:${item.member_id}`; return <article className="review-card" key={key}><div><strong>{item.assignment_title}</strong><p className="muted">Student {item.member_id} · submission v{item.version}</p><Markdown body={item.body} />{item.links.length > 0 && <p>{item.links.map((link) => <a key={link} href={link} target="_blank" rel="noopener noreferrer">Evidence link</a>)}</p>}{item.files.map((file) => <SubmissionFileLink key={file} fileId={file} target={{ assignment_id: item.assignment_id }} memberId={item.member_id} />)}</div><textarea aria-label={`Feedback for ${item.assignment_title}`} value={feedback[key] || ""} onChange={(event) => setFeedback((current) => ({ ...current, [key]: event.target.value }))} placeholder="Feedback" maxLength={4000} /><div className="review-actions"><button className="secondary" disabled={busy === key} onClick={() => void review(item, "needs_changes")}>Request changes</button><button className="primary" disabled={busy === key} onClick={() => void review(item, "approved")}>Approve</button></div></article>})}{error && <p role="alert">{error}</p>}</section>;
}

export function MilestoneForm({ milestone, onSaved }: { milestone: MemberMilestone; onSaved: () => void }) {
  const [text, setText] = useState(milestone.evidence_text || ""); const [links, setLinks] = useState((milestone.evidence_links || []).join("\n")); const [files, setFiles] = useState(milestone.evidence_files || []); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  async function upload(event: ChangeEvent<HTMLInputElement>) { const file = event.target.files?.[0]; if (!file) return; try { const f = await api.courses.uploadFile(file.name, await encodeFile(file), file.type || "application/octet-stream", { definition_id: milestone.definition.id }); setFiles((v) => [...v, f.file_id]); } catch (e) { setError(message(e)); } event.target.value = ""; }
  async function submit(event: FormEvent) { event.preventDefault(); setBusy(true); setError(""); try { await api.courses.submitMilestone(milestone.definition.id, text, links.split(/\n|,/).map((v) => v.trim()).filter(Boolean), files); onSaved(); } catch (e) { setError(message(e)); } finally { setBusy(false); } }
  return <form className="card stack milestone-form" onSubmit={submit}><h4>{milestone.definition.title}</h4><p className="muted">{milestone.definition.description}</p>{milestone.feedback && <p role="alert">Feedback: {milestone.feedback}</p>}<label>Evidence</label><textarea value={text} onChange={(e) => setText(e.target.value)} placeholder="Describe what you achieved" /><label>Evidence links</label><textarea value={links} onChange={(e) => setLinks(e.target.value)} placeholder="One link per line" /><label>Attachments</label><input type="file" onChange={(e) => void upload(e)} />{files.map((file) => <SubmissionFileLink key={file} fileId={file} target={{ definition_id: milestone.definition.id }} />)}<button className="primary" disabled={busy || (!text.trim() && !links.trim() && files.length === 0)}>{busy ? "Submitting…" : milestone.status === "needs_changes" ? "Resubmit evidence" : "Submit evidence"}</button>{error && <p role="alert">{error}</p>}</form>;
}

export function ProfileForm({
  member,
  onSaved,
}: {
  member: Member;
  onSaved: () => void;
}) {
  const [name, setName] = useState(member.display_name);
  const [bio, setBio] = useState(member.bio);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    setSaved(false);
    try {
      await api.members.update(member.community_id, {
        display_name: name.trim(),
        bio,
      });
      setSaved(true);
      onSaved();
    } catch (caught) {
      setError(message(caught));
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="stack profile-form" onSubmit={submit}>
      <label htmlFor="profile-name">Display name</label>
      <input
        id="profile-name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        required
        maxLength={300}
      />
      <label htmlFor="profile-bio">Bio</label>
      <textarea
        id="profile-bio"
        value={bio}
        onChange={(e) => setBio(e.target.value)}
        maxLength={4000}
      />
      <button className="primary" disabled={busy || !name.trim()}>
        {busy ? "Saving…" : "Save profile"}
      </button>
      {saved && <p role="status">Profile saved.</p>}
      {error && <p role="alert">{error}</p>}
    </form>
  );
}
