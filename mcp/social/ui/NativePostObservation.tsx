export interface NativePostMetadata {
  native_post_id: string; observed_at: string;
  uploaded_at?: string | null;
  title: string | null; body: string | null; published_at: string | null;
  visibility: string | null; scheduled_publish_at?: string | null; audience: string | null; processing_state: string | null;
  original_filename: string | null; bytes: number | null; duration_seconds: number | null;
}
export function NativePostObservation({ metadata }: { metadata?: NativePostMetadata }) {
  if (!metadata) return null;
  return <section className="mt-2 border-t border-border pt-2 text-xs text-text-dim" aria-label="Native history observation">
    <p>Provider metadata observed {metadata.observed_at}; this is not independent public access verification.</p>
    <dl className="mt-1 grid grid-cols-2 gap-x-3">
      <dt>Native title</dt><dd>{metadata.title ?? "Unknown"}</dd>
      <dt>Native visibility</dt><dd>{metadata.visibility ?? "Unknown"}</dd>
      {metadata.scheduled_publish_at && <><dt>Scheduled release</dt><dd>{metadata.scheduled_publish_at}</dd></>}
      <dt>Membership audience</dt><dd>{metadata.audience ?? "Unknown"}</dd>
      <dt>Processing state</dt><dd>{metadata.processing_state ?? "Unknown"}</dd>
      <dt>Native publication time</dt><dd>{metadata.published_at ?? "Unknown"}</dd>
      <dt>Native upload time</dt><dd>{metadata.uploaded_at ?? "Unknown"}</dd>
      <dt>Original filename</dt><dd>{metadata.original_filename ?? "Unknown"}</dd>
      <dt>Original bytes</dt><dd>{metadata.bytes == null ? "Unknown" : metadata.bytes.toLocaleString()}</dd>
      <dt>Duration</dt><dd>{metadata.duration_seconds == null ? "Unknown" : `${metadata.duration_seconds} seconds`}</dd>
    </dl>
  </section>;
}
