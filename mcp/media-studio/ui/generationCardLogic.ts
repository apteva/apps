export type GenerationLike = {
  kind?: string;
  storage_urls?: string[];
  upstream_urls?: string[];
  local_cache_url?: string;
  thumbnail_b64?: string;
};

export function generationMediaURLs(generation: GenerationLike): string[] {
  const urls = [
    ...(generation.storage_urls || []),
    generation.local_cache_url || "",
    ...(generation.upstream_urls || []),
  ].filter((url) => isSafeMediaURL(url));
  return Array.from(new Set(urls));
}

function isSafeMediaURL(url: string): boolean {
  return url.startsWith("/") || url.startsWith("https://") || url.startsWith("http://") || url.startsWith("blob:");
}

export function generationPoster(generation: GenerationLike): string | undefined {
  return generation.thumbnail_b64
    ? `data:image/jpeg;base64,${generation.thumbnail_b64}`
    : undefined;
}

export function isVideoKind(kind = ""): boolean {
  return kind === "video" || kind === "avatar";
}

export function isAudioKind(kind = ""): boolean {
  return kind === "audio_tts" || kind === "audio_sfx" || kind === "music";
}

export function mediaKindLabel(kind = ""): string {
  switch (kind) {
    case "image": return "Generated image";
    case "video": return "Generated video";
    case "avatar": return "Avatar video";
    case "audio_tts": return "Voiceover";
    case "audio_sfx": return "Sound effect";
    case "music": return "Generated music";
    default: return "Generated media";
  }
}

export function formatGenerationCost(value = 0): string {
  if (!Number.isFinite(value) || value <= 0) return "";
  if (value < 0.01) return `$${value.toFixed(4)}`;
  return `$${value.toFixed(2)}`;
}

export function formatGenerationDuration(seconds = 0): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "";
  const total = Math.round(seconds);
  const minutes = Math.floor(total / 60);
  const remainder = total % 60;
  return minutes > 0 ? `${minutes}:${String(remainder).padStart(2, "0")}` : `${remainder}s`;
}

export function isTerminalJobStatus(status = ""): boolean {
  return status === "complete" || status === "failed" || status === "cancelled";
}

export function shouldRefreshGenerationCard(
  event: { topic?: string; data?: { generation_id?: number | string; job_id?: number | string } },
  generationId?: number,
  jobId?: number,
): boolean {
  if (!event.topic?.startsWith("media.") && !event.topic?.endsWith(".queued")) return false;
  const eventGeneration = Number(event.data?.generation_id || 0);
  const eventJob = Number(event.data?.job_id || 0);
  return Boolean(
    (generationId && eventGeneration === generationId) ||
    (jobId && eventJob === jobId),
  );
}
