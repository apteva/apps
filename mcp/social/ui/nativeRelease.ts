// Native release state of a delivered target, as computed server-side
// (youtube_release.go). A successful upload is not the same as public.
export interface NativeRelease {
  state: "public" | "unlisted" | "private" | "scheduled" | "release_due" | string;
  visibility: string;
  release_at?: string;
  source: "observed" | "requested" | string;
  observed_at?: string;
}

export interface ReleaseLabel {
  label: string;
  tone: string;
  title: string;
}

const when = (iso?: string) => (iso ? new Date(iso).toLocaleString() : "");

function sourceNote(release: NativeRelease): string {
  return release.source === "observed"
    ? `Read from YouTube ${when(release.observed_at)}.`
    : "As requested by Social; refresh from YouTube to confirm.";
}

// Returns null when the target is simply public, so callers keep the
// ordinary "published" status.
export function nativeReleaseLabel(release?: NativeRelease | null): ReleaseLabel | null {
  if (!release || release.state === "public") return null;
  const note = sourceNote(release);
  switch (release.state) {
    case "scheduled":
      return { label: `Scheduled on YouTube · ${when(release.release_at)}`, tone: "text-info", title: `Uploaded privately; YouTube makes it public at ${when(release.release_at)}. ${note}` };
    case "release_due":
      return { label: `Release time passed · ${when(release.release_at)}`, tone: "text-warn", title: `YouTube should have made it public at ${when(release.release_at)}. ${note}` };
    case "unlisted":
      return { label: "Uploaded · unlisted", tone: "text-warn", title: `Not public: anyone with the link can view it. ${note}` };
    case "private":
      return { label: "Uploaded · private", tone: "text-warn", title: `Not public and no release time is set. ${note}` };
    default:
      return { label: `Uploaded · ${release.visibility || release.state}`, tone: "text-warn", title: note };
  }
}

// Post-level label: only replaces "published" when no delivered target is
// actually public. Mixed posts keep "published" and show per-target detail.
export function postReleaseLabel(post: { status: string; targets?: { status: string; native_release?: NativeRelease | null }[] }): ReleaseLabel | null {
  if (post.status !== "published" && post.status !== "partial") return null;
  const delivered = (post.targets || []).filter(t => t.status === "published");
  if (delivered.length === 0) return null;
  const labels = delivered.map(t => nativeReleaseLabel(t.native_release));
  if (labels.some(l => l === null)) return null;
  if (delivered.length === 1) return labels[0];
  const states = new Set(delivered.map(t => t.native_release!.state));
  if (states.size === 1 && states.has("scheduled")) {
    const first = delivered.map(t => t.native_release!.release_at || "").sort()[0];
    return { label: `Scheduled on YouTube · ${when(first)}`, tone: "text-info", title: "Every delivered video is scheduled on YouTube and not public yet." };
  }
  return { label: "Uploaded · not public", tone: "text-warn", title: "Every delivered video is private, unlisted or scheduled; none is public yet." };
}
