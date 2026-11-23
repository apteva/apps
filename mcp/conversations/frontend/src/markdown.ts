import createDOMPurify from "dompurify";
import { marked } from "marked";

marked.setOptions({ breaks: true, gfm: true });

const SANITIZE_OPTIONS = {
  USE_PROFILES: { html: true },
  FORBID_TAGS: ["base", "form", "iframe", "object", "embed", "style", "template"],
  FORBID_ATTR: ["style", "srcdoc"],
};

let purifier: ReturnType<typeof createDOMPurify> | null = null;

// A list item whose text starts with a number ("- 01. Define", "1. 01. Define")
// is, to CommonMark, a nested ordered list: it shows "1." in place of the
// item's own "01.". In messages that number is part of the item's title (todo
// sequence numbers, years), so it is escaped and shown as written. Fenced code
// is left alone.
const NUMBERED_ITEM_TEXT = /^((?:[ \t]*>)*[ \t]*(?:[-*+]|\d{1,9}[.)])[ \t]+)(\d{1,9})([.)])(?=[ \t]|$)/;
const FENCE = /^(?:[ \t]*>)*[ \t]{0,3}(`{3,}|~{3,})/;

export function keepListItemNumbers(source: string): string {
  let fence = "";
  return source.split("\n").map((line) => {
    const marker = FENCE.exec(line)?.[1] ?? "";
    if (fence) {
      if (marker && marker[0] === fence[0] && marker.length >= fence.length && line.trimEnd().endsWith(marker)) fence = "";
      return line;
    }
    if (marker) {
      fence = marker;
      return line;
    }
    return line.replace(NUMBERED_ITEM_TEXT, "$1$2\\$3");
  }).join("\n");
}

// Shared by transcript text and Markdown inside Inbox cards. Web links should
// leave the conversation available; relative routes and fragments stay local.
export function renderSafeMarkdown(source: string): string {
  const parsed = marked.parse(keepListItemNumbers(source), { async: false }) as string;
  if (!purifier) {
    purifier = createDOMPurify(window);
    if (purifier.isSupported) purifier.addHook("afterSanitizeAttributes", (node) => {
      if (node.tagName !== "A" || !/^(?:https?:)?\/\//i.test((node.getAttribute("href") ?? "").trim())) return;
      node.setAttribute("target", "_blank");
      const rel = new Set((node.getAttribute("rel") ?? "").split(/\s+/).filter(Boolean));
      rel.add("noopener");
      rel.add("noreferrer");
      node.setAttribute("rel", [...rel].join(" "));
    });
  }
  return purifier.isSupported ? purifier.sanitize(parsed, SANITIZE_OPTIONS) : "";
}
