import { useEffect, useRef, useState } from "react";
import { scopedAppURL } from "./panelScope";
import { mcpEnvelopeError } from "./accountFlow";
interface UploadOutcome {
  at?: string;
  started_at?: string;
  http_status?: number;
  response_excerpt?: string;
  request_id?: string;
  chunk?: number;
  content_range?: string;
  result?: string;
}
export interface DeliveryDiagnostics {
  correlation_id?: string;
  verified_source_bytes?: number | null;
  attempted_upload_bytes?: number | null;
  acknowledged_upload_bytes?: number | null;
  legacy_transferred_bytes?: number;
  provider_status?: string;
  provider_observation?: {
    observed_at: string;
    uploaded_bytes?: number | null;
    downloaded_bytes?: number | null;
    query_succeeded: boolean;
  };
  first_upload_error?: UploadOutcome;
  latest_upload_error?: UploadOutcome;
  upload_acknowledgement_resolution?: UploadOutcome;
  initialization?: UploadOutcome;
  last_upload?: UploadOutcome;
  local_diagnostic?: string;
  last_progress_at?: string;
  automatic_polling_stopped?: boolean;
  next_poll_at?: string;
}
const bytes = (n: number | null | undefined) =>
  n == null ? "Unknown" : `${n.toLocaleString()} bytes`;
export function TargetDelivery({
  postId,
  targetId,
  projectId,
  diagnostics,
  onChanged,
}: {
  postId: number;
  targetId: number;
  projectId?: string | null;
  diagnostics?: DeliveryDiagnostics;
  onChanged?: () => void;
}) {
  const [local, setLocal] = useState<DeliveryDiagnostics | undefined>(
    diagnostics,
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [decision, setDecision] = useState<{
    decision: string;
    reason?: string;
    recovery_id?: string;
  }>();
  const identity = `${projectId}:${postId}:${targetId}`;
  const current = useRef(identity);
  current.current = identity;
  useEffect(() => {
    current.current = identity;
    setLocal(diagnostics);
    setBusy(false);
    setDecision(undefined);
    setError("");
    return () => {
      current.current = "";
    };
  }, [identity]);
  useEffect(() => {
    setLocal(diagnostics);
  }, [diagnostics]);
  const run = async (action: "inspect" | "revalidate" | "recover") => {
    const captured = identity;
    setBusy(true);
    setError("");
    try {
      const response = await fetch(
        scopedAppURL(
          "/api/apps/social",
          `/posts/${postId}/delivery/${targetId}/${action}`,
          projectId,
        ),
        {
          method: action === "inspect" ? "GET" : "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body:
            action === "recover"
              ? JSON.stringify({
                  recovery_id: decision?.recovery_id,
                  confirm: true,
                })
              : undefined,
        },
      );
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json();
      const issue = mcpEnvelopeError(data);
      if (issue) throw new Error(issue);
      if (current.current !== captured) return;
      const target = data.targets?.find(
        (item: { id: number }) => item.id === targetId,
      );
      if (data.delivery_diagnostics || target?.delivery_diagnostics)
        setLocal(data.delivery_diagnostics || target.delivery_diagnostics);
      if (action === "revalidate") setDecision(data);
      else if (action === "recover") {
        setDecision(undefined);
        onChanged?.();
      } else setDecision(undefined);
    } catch (error) {
      if (current.current === captured) setError((error as Error).message);
    } finally {
      if (current.current === captured) setBusy(false);
    }
  };
  const d = local;
  const observation = d?.provider_observation;
  const acknowledgementResolved = !!d?.upload_acknowledgement_resolution;
  return (
    <section
      className="mt-2 rounded border border-border p-2 text-xs"
      aria-label="Delivery status"
    >
      <div className="font-medium text-text">Upload and publication</div>
      {d?.local_diagnostic === "stalled_upload" && !acknowledgementResolved && (
        <p role="status" className="mt-1 text-error">
          Upload stalled: no confirmed progress after an upload error. TikTok
          status: {d.provider_status || "Unknown"}. A replacement upload is
          blocked until nonpublication is established.
        </p>
      )}
      {d?.provider_status && <p>Provider status: {d.provider_status}</p>}
      <dl className="mt-1 grid grid-cols-2 gap-x-2">
        <dt>Source verified</dt>
        <dd>{bytes(d?.verified_source_bytes)}</dd>
        <dt>Upload payload attempted</dt>
        <dd>{bytes(d?.attempted_upload_bytes)}</dd>
        <dt>Upload acknowledged</dt>
        <dd>{bytes(d?.acknowledged_upload_bytes)}</dd>
        <dt>TikTok uploaded</dt>
        <dd>{bytes(observation?.uploaded_bytes)}</dd>
        <dt>TikTok downloaded</dt>
        <dd>{bytes(observation?.downloaded_bytes)}</dd>
      </dl>
      {observation && (
        <p className="mt-1 text-text-dim">
          Observed: {observation.observed_at}
          {observation.query_succeeded
            ? " · Status retrieved; this alone does not confirm publication."
            : " · Status request failed."}
        </p>
      )}
      {d?.last_progress_at && (
        <p>Last confirmed progress: {d.last_progress_at}</p>
      )}
      {d?.legacy_transferred_bytes != null && (
        <p className="text-text-dim">
          Earlier transfer estimate was unverified and is excluded from
          acknowledged progress.
        </p>
      )}
      {acknowledgementResolved && (
        <p className="mt-1 text-text-dim">
          Upload acknowledgement reconciled from the saved successful response.
          Original warning retained; the existing operation is checked without
          uploading again.
        </p>
      )}
      {d?.latest_upload_error && (
        <details
          className={
            acknowledgementResolved ? "mt-2 text-text-dim" : "mt-2 text-error"
          }
        >
          <summary>
            {acknowledgementResolved
              ? "Previous acknowledgement warning (resolved)"
              : "Upload error"}{" "}
            · HTTP {d.latest_upload_error.http_status || "unknown"}
          </summary>
          <p>
            First recorded error:{" "}
            {d.first_upload_error?.at || "Original time unavailable"}
          </p>
          <p>
            Latest error:{" "}
            {d.latest_upload_error.at || "Original time unavailable"}
          </p>
          <p>
            Chunk {d.latest_upload_error.chunk || "unknown"} ·{" "}
            {d.latest_upload_error.content_range || "Range unavailable"}
          </p>
          <p>{d.latest_upload_error.response_excerpt}</p>
          <p>Request: {d.latest_upload_error.request_id || "Unavailable"}</p>
        </details>
      )}
      {d?.automatic_polling_stopped && (
        <p className="mt-1 text-text-dim">
          Automatic checks paused after repeated checks without progress. Check
          this target manually; it will not be resubmitted automatically.
        </p>
      )}
      {d?.next_poll_at && (
        <p className="text-text-dim">Next automatic check: {d.next_poll_at}</p>
      )}
      <div className="mt-2 flex flex-wrap gap-2">
        <button type="button" disabled={busy} onClick={() => run("inspect")}>
          Check status
        </button>
        <button type="button" disabled={busy} onClick={() => run("revalidate")}>
          Check recovery eligibility
        </button>
        {decision?.recovery_id &&
          (decision.decision === "retry" || decision.decision === "resume") && (
            <button
              type="button"
              disabled={busy}
              onClick={() => run("recover")}
            >
              Recover this target
            </button>
          )}
      </div>
      {decision && (
        <p role="status" className="mt-1">
          {decision.decision}: {decision.reason}
        </p>
      )}
      {error && (
        <p role="alert" className="text-error">
          {error}
        </p>
      )}
    </section>
  );
}
