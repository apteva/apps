export interface PublicationFactsData {
  submitted_at?: string | null;
  provider_accepted_at?: string | null;
  provider_reported_published_at?: string | null;
  delivery_recorded_at?: string | null;
  native_published_at?: string | null;
  native_uploaded_at?: string | null;
  observed_at?: string | null;
  publication_time_source?: string;
  legacy_published_at?: string | null;
}
export function PublicationFacts({ facts }: { facts?: PublicationFactsData }) {
  if (!facts) return null;
  return (
    <section
      className="mt-2 border-t border-border pt-2 text-xs text-text-dim"
      aria-label="Publication times"
    >
      <dl className="grid grid-cols-2 gap-x-3">
        <dt>Submitted</dt>
        <dd>{facts.submitted_at ?? "Unknown"}</dd>
        <dt>Provider accepted</dt>
        <dd>{facts.provider_accepted_at ?? "Unknown"}</dd>
        <dt>Delivery recorded</dt>
        <dd>{facts.delivery_recorded_at ?? "Unknown"}</dd>
        <dt>Native publication</dt>
        <dd>{facts.native_published_at ?? "Unknown"}</dd>
        <dt>Native upload</dt>
        <dd>{facts.native_uploaded_at ?? "Unknown"}</dd>
        <dt>Observed</dt>
        <dd>{facts.observed_at ?? "Unknown"}</dd>
        {facts.provider_reported_published_at && (
          <>
            <dt>Provider-reported publication</dt>
            <dd>{facts.provider_reported_published_at}</dd>
          </>
        )}
      </dl>
      <p>
        Upload, delivery recording and audience confirmation do not establish a
        native publication time.
      </p>
      {facts.legacy_published_at && (
        <details>
          <summary>Previous timestamp retained for review</summary>
          <p>{facts.legacy_published_at} · Original meaning unconfirmed.</p>
        </details>
      )}
    </section>
  );
}
