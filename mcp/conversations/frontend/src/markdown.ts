import createDOMPurify from "dompurify";
import { marked } from "marked";

marked.setOptions({ breaks: true, gfm: true });

const SANITIZE_OPTIONS = {
  USE_PROFILES: { html: true },
  FORBID_TAGS: ["base", "form", "iframe", "object", "embed", "style", "template"],
  FORBID_ATTR: ["style", "srcdoc"],
};

let purifier: ReturnType<typeof createDOMPurify> | null = null;

// Shared by transcript text and Markdown inside Inbox cards. Web links should
// leave the conversation available; relative routes and fragments stay local.
export function renderSafeMarkdown(source: string): string {
  const parsed = marked.parse(source, { async: false }) as string;
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
