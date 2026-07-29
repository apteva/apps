export type InboxAttachmentKind = "image" | "video" | "audio" | "file";

export interface InboxAttachment {
  kind: InboxAttachmentKind;
  url: string;
  mime?: string;
  name?: string;
  thumbnail_url?: string;
  storage_id?: number;
}

export interface InboxAttachmentCapabilities {
  types: InboxAttachmentKind[];
  max: number;
}

const VALID_KINDS = new Set<InboxAttachmentKind>(["image", "video", "audio", "file"]);

function attachmentKind(value: unknown, mime = "", name = ""): InboxAttachmentKind {
  const explicit = String(value || "").toLowerCase() as InboxAttachmentKind;
  if (VALID_KINDS.has(explicit)) return explicit;
  const normalizedMime = mime.toLowerCase();
  if (normalizedMime.startsWith("image/")) return "image";
  if (normalizedMime.startsWith("video/")) return "video";
  if (normalizedMime.startsWith("audio/")) return "audio";
  const extension = name.toLowerCase().split(/[?#]/, 1)[0].split(".").pop() || "";
  if (["jpg", "jpeg", "png", "gif", "webp", "avif"].includes(extension)) return "image";
  if (["mp4", "mov", "webm", "m4v"].includes(extension)) return "video";
  if (["mp3", "m4a", "aac", "wav", "ogg", "opus", "flac"].includes(extension)) return "audio";
  return "file";
}

function attachmentArray(value: unknown): unknown[] {
  if (Array.isArray(value)) return value;
  if (!value || typeof value !== "object") return [];
  const record = value as Record<string, unknown>;
  for (const key of ["attachments", "data", "items", "media"]) {
    if (Array.isArray(record[key])) return record[key] as unknown[];
  }
  return [record];
}

function deepString(value: unknown, keys: string[]): string {
  if (!value || typeof value !== "object") return "";
  if (Array.isArray(value)) {
    for (const item of value) {
      const found = deepString(item, keys);
      if (found) return found;
    }
    return "";
  }
  const record = value as Record<string, unknown>;
  for (const key of keys) {
    if (typeof record[key] === "string" && String(record[key]).trim()) {
      return String(record[key]).trim();
    }
  }
  for (const nested of Object.values(record)) {
    const found = deepString(nested, keys);
    if (found) return found;
  }
  return "";
}

export function parseInboxAttachments(value: unknown): InboxAttachment[] {
  let decoded = value;
  if (typeof decoded === "string") {
    try {
      decoded = JSON.parse(decoded);
    } catch {
      return [];
    }
  }
  return attachmentArray(decoded).flatMap((raw) => {
    if (typeof raw === "string" && raw.trim()) {
      const url = raw.trim();
      return [{
        kind: attachmentKind("", "", url),
        url,
      }];
    }
    if (!raw || typeof raw !== "object") return [];
    const item = raw as Record<string, unknown>;
    const url = deepString(item, ["url", "file_url", "video_url", "image_url", "src"]);
    if (!url) return [];
    const mime = deepString(item, ["mime", "mime_type", "content_type"]);
    const name = deepString(item, ["name", "filename", "title"]);
    return [{
      kind: attachmentKind(deepString(item, ["kind", "type"]), mime, name),
      url,
      mime: mime || undefined,
      name: name || undefined,
      thumbnail_url: deepString(item, ["thumbnail_url", "preview_url"]) || undefined,
      storage_id: Number(item.storage_id) || undefined,
    }];
  });
}

export function inboxAttachmentCapabilities(
  accountCapabilities: Record<string, unknown> | null | undefined,
  platformInbox: Record<string, unknown> | null | undefined,
  kind: string,
): InboxAttachmentCapabilities {
  if (kind !== "dm") return { types: [], max: 0 };
  const providerTypes = accountCapabilities?.inbox_attachment_types;
  const platformTypes = platformInbox?.dm_attachment_types;
  const rawTypes = Array.isArray(providerTypes) ? providerTypes : Array.isArray(platformTypes) ? platformTypes : [];
  const types = rawTypes
    .map((value) => String(value).toLowerCase() as InboxAttachmentKind)
    .filter((value): value is InboxAttachmentKind => VALID_KINDS.has(value));
  const providerMax = Number(accountCapabilities?.inbox_max_attachments);
  const platformMax = Number(platformInbox?.dm_max_attachments);
  const max = Number.isFinite(providerMax) && providerMax > 0
    ? providerMax
    : Number.isFinite(platformMax) && platformMax > 0 ? platformMax : 0;
  return { types, max: types.length > 0 ? max : 0 };
}

export function storageAttachmentKind(contentType: string): InboxAttachmentKind {
  return attachmentKind("", contentType, "");
}
