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

export interface CodeBlockLabels {
  /** Shown in the header when the fence names no language. */
  text: string;
  copy: string;
  copied: string;
  copyFailed: string;
  wrap: string;
  /** "{count}" is replaced by the block's line count. */
  showAll: string;
  showLess: string;
}

export const DEFAULT_CODE_LABELS: CodeBlockLabels = {
  text: "Text", copy: "Copy", copied: "Copied", copyFailed: "Copy failed",
  wrap: "Wrap lines", showAll: "Show all {count} lines", showLess: "Show less",
};

// Prose and prompts read best wrapped; real code keeps its lines and scrolls.
const WRAPPED_LANGUAGES = new Set(["", "text", "txt", "plain", "plaintext", "markdown", "md", "prompt"]);
// Taller blocks start collapsed behind "Show all".
const COLLAPSE_LINES = 18;
const COLLAPSE_CHARS = 1400;
const ICON_COPY = '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/></svg>';
const ICON_CHECK = '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg>';
const ICON_WRAP = '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 6h18M3 12h15a3 3 0 0 1 0 6h-4m0 0 2-2m-2 2 2 2M3 18h7"/></svg>';

function codeButton(action: string, icon: string, label: string, showLabel: boolean): HTMLButtonElement {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "chat-code-btn";
  button.dataset.chatCode = action;
  button.title = label;
  button.setAttribute("aria-label", label);
  button.innerHTML = icon;
  if (showLabel) {
    const text = document.createElement("span");
    text.className = "chat-code-btn-label";
    text.textContent = label;
    button.append(text);
  }
  return button;
}

// Wraps each fenced block in a header (language, wrap toggle, copy) and, when
// it is tall, a "Show all" footer. Runs on sanitized HTML; the added controls
// carry no handlers and are driven by one delegated listener.
export function decorateCodeBlocks(html: string, labels: CodeBlockLabels = DEFAULT_CODE_LABELS): string {
  if (!html.includes("<pre")) return html;
  const template = document.createElement("template");
  template.innerHTML = html;
  for (const pre of Array.from(template.content.querySelectorAll("pre"))) {
    const code = pre.querySelector(":scope > code");
    const raw = codeText(pre);
    const language = /(?:^|\s)language-([\w+#.-]+)/.exec(code?.className ?? "")?.[1]?.toLowerCase() ?? "";
    const lines = raw.split("\n").length;
    const box = document.createElement("div");
    box.className = "chat-code";
    if (WRAPPED_LANGUAGES.has(language)) box.classList.add("chat-code--wrap");
    const header = document.createElement("div");
    header.className = "chat-code-header";
    const name = document.createElement("span");
    name.className = "chat-code-lang";
    name.textContent = language || labels.text;
    const wrap = codeButton("wrap", ICON_WRAP, labels.wrap, false);
    wrap.setAttribute("aria-pressed", String(box.classList.contains("chat-code--wrap")));
    const copy = codeButton("copy", ICON_COPY, labels.copy, true);
    copy.dataset.copied = labels.copied;
    copy.dataset.failed = labels.copyFailed;
    header.append(name, wrap, copy);
    const body = document.createElement("div");
    body.className = "chat-code-body";
    pre.replaceWith(box);
    body.append(pre);
    box.append(header, body);
    if (lines > COLLAPSE_LINES || raw.length > COLLAPSE_CHARS) {
      box.classList.add("chat-code--collapsible", "chat-code--collapsed");
      const more = document.createElement("div");
      more.className = "chat-code-more";
      const showAll = labels.showAll.replace("{count}", String(lines));
      const expand = codeButton("expand", "", showAll, true);
      expand.dataset.showAll = showAll;
      expand.dataset.showLess = labels.showLess;
      expand.setAttribute("aria-expanded", "false");
      more.append(expand);
      box.append(more);
    }
  }
  installCodeBlockActions();
  return template.innerHTML;
}

// The block's text exactly as the agent wrote it, without the newline marked
// adds before </code>.
function codeText(pre: Element): string {
  return (pre.textContent ?? "").replace(/\n$/, "");
}

let codeActionsInstalled = false;
function installCodeBlockActions() {
  if (codeActionsInstalled || typeof document === "undefined") return;
  codeActionsInstalled = true;
  // composedPath reaches buttons inside a shadow root as well.
  document.addEventListener("click", (event) => {
    const button = event.composedPath().find((node): node is HTMLButtonElement =>
      node instanceof HTMLButtonElement && !!node.dataset.chatCode);
    const box = button?.closest(".chat-code");
    if (!button || !box) return;
    const action = button.dataset.chatCode;
    if (action === "wrap") {
      button.setAttribute("aria-pressed", String(box.classList.toggle("chat-code--wrap")));
    } else if (action === "expand") {
      const collapsed = box.classList.toggle("chat-code--collapsed");
      button.setAttribute("aria-expanded", String(!collapsed));
      setButtonLabel(button, (collapsed ? button.dataset.showAll : button.dataset.showLess) ?? "");
      if (collapsed) box.scrollIntoView({ block: "nearest" });
    } else if (action === "copy") {
      const pre = box.querySelector("pre");
      if (pre) void copyBlock(button, codeText(pre));
    }
  });
}

function setButtonLabel(button: HTMLButtonElement, label: string) {
  button.title = label;
  button.setAttribute("aria-label", label);
  const text = button.querySelector(".chat-code-btn-label");
  if (text) text.textContent = label;
}

const copyResets = new WeakMap<HTMLButtonElement, ReturnType<typeof setTimeout>>();
async function copyBlock(button: HTMLButtonElement, text: string) {
  const original = button.dataset.label ?? button.getAttribute("aria-label") ?? "";
  button.dataset.label = original;
  const ok = await writeClipboard(text);
  button.classList.toggle("is-copied", ok);
  button.classList.toggle("is-failed", !ok);
  button.querySelector("svg")?.replaceWith(template(ok ? ICON_CHECK : ICON_COPY));
  setButtonLabel(button, (ok ? button.dataset.copied : button.dataset.failed) ?? original);
  clearTimeout(copyResets.get(button));
  copyResets.set(button, setTimeout(() => {
    button.classList.remove("is-copied", "is-failed");
    button.querySelector("svg")?.replaceWith(template(ICON_COPY));
    setButtonLabel(button, original);
  }, 1600));
}

function template(svg: string): Element {
  const holder = document.createElement("template");
  holder.innerHTML = svg;
  return holder.content.firstElementChild!;
}

async function writeClipboard(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch { /* fall through to the selection copy */ }
  // Plain-HTTP hosts have no Clipboard API.
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.opacity = "0";
  document.body.append(area);
  area.select();
  let ok = false;
  try { ok = document.execCommand("copy"); } catch { ok = false; }
  area.remove();
  return ok;
}

// Shared by transcript text and Markdown inside Inbox cards. Web links should
// leave the conversation available; relative routes and fragments stay local.
export function renderSafeMarkdown(source: string, codeLabels: CodeBlockLabels = DEFAULT_CODE_LABELS): string {
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
  return purifier.isSupported ? decorateCodeBlocks(purifier.sanitize(parsed, SANITIZE_OPTIONS) as string, codeLabels) : "";
}
