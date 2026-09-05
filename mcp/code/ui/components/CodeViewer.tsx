import { useMemo, type RefObject } from "react";
import hljs from "highlight.js/lib/core";
import bash from "highlight.js/lib/languages/bash";
import css from "highlight.js/lib/languages/css";
import diff from "highlight.js/lib/languages/diff";
import dockerfile from "highlight.js/lib/languages/dockerfile";
import go from "highlight.js/lib/languages/go";
import javascript from "highlight.js/lib/languages/javascript";
import json from "highlight.js/lib/languages/json";
import markdown from "highlight.js/lib/languages/markdown";
import python from "highlight.js/lib/languages/python";
import sql from "highlight.js/lib/languages/sql";
import typescript from "highlight.js/lib/languages/typescript";
import xml from "highlight.js/lib/languages/xml";
import yaml from "highlight.js/lib/languages/yaml";

hljs.registerLanguage("bash", bash);
hljs.registerLanguage("css", css);
hljs.registerLanguage("diff", diff);
hljs.registerLanguage("dockerfile", dockerfile);
hljs.registerLanguage("go", go);
hljs.registerLanguage("javascript", javascript);
hljs.registerLanguage("json", json);
hljs.registerLanguage("markdown", markdown);
hljs.registerLanguage("python", python);
hljs.registerLanguage("sql", sql);
hljs.registerLanguage("typescript", typescript);
hljs.registerLanguage("xml", xml);
hljs.registerLanguage("yaml", yaml);

interface SyntaxLanguage {
  id: string | null;
  label: string;
}

// Keep language selection deterministic. Auto-detection looks clever on tiny
// files but produces distracting false positives (README as CSS, env as SQL).
export function syntaxLanguageForPath(path: string): SyntaxLanguage {
  const name = path.split("/").pop()?.toLowerCase() || "";
  const ext = name.includes(".") ? name.split(".").pop() || "" : "";
  if (name === "dockerfile" || name.startsWith("dockerfile.")) return { id: "dockerfile", label: "Dockerfile" };
  if (name === "makefile") return { id: null, label: "Makefile" };
  switch (ext) {
    case "js": case "jsx": case "mjs": case "cjs":
      return { id: "javascript", label: ext === "jsx" ? "JSX" : "JavaScript" };
    case "ts": case "tsx": case "mts": case "cts":
      return { id: "typescript", label: ext === "tsx" ? "TSX" : "TypeScript" };
    case "json": case "jsonc":
      return { id: "json", label: ext.toUpperCase() };
    case "css": case "scss": case "sass": case "less":
      return { id: "css", label: ext.toUpperCase() };
    case "html": case "htm": case "xml": case "svg":
      return { id: "xml", label: ext === "svg" ? "SVG" : ext.toUpperCase() };
    case "md": case "mdx":
      return { id: "markdown", label: ext.toUpperCase() };
    case "sh": case "bash": case "zsh":
      return { id: "bash", label: "Shell" };
    case "go":
      return { id: "go", label: "Go" };
    case "py": case "pyw":
      return { id: "python", label: "Python" };
    case "yaml": case "yml":
      return { id: "yaml", label: "YAML" };
    case "sql":
      return { id: "sql", label: "SQL" };
    case "diff": case "patch":
      return { id: "diff", label: "Diff" };
    default:
      return { id: null, label: ext ? ext.toUpperCase() : "Plain text" };
  }
}

function escapeHTML(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

const CODE_SYNTAX_THEME = `
  .code-syntax .hljs-comment,
  .code-syntax .hljs-quote { color: var(--text-dim); font-style: italic; }
  .code-syntax .hljs-keyword,
  .code-syntax .hljs-selector-tag,
  .code-syntax .hljs-doctag { color: var(--accent); font-weight: 600; }
  .code-syntax .hljs-string,
  .code-syntax .hljs-regexp,
  .code-syntax .hljs-attr,
  .code-syntax .hljs-template-tag { color: var(--success); }
  .code-syntax .hljs-number,
  .code-syntax .hljs-literal,
  .code-syntax .hljs-symbol,
  .code-syntax .hljs-bullet { color: var(--warn); }
  .code-syntax .hljs-title,
  .code-syntax .hljs-section,
  .code-syntax .hljs-function .hljs-title { color: var(--info); }
  .code-syntax .hljs-built_in,
  .code-syntax .hljs-type,
  .code-syntax .hljs-class .hljs-title { color: color-mix(in srgb, var(--warn), var(--text) 22%); }
  .code-syntax .hljs-variable,
  .code-syntax .hljs-template-variable,
  .code-syntax .hljs-params,
  .code-syntax .hljs-attribute { color: color-mix(in srgb, var(--accent), var(--text) 38%); }
  .code-syntax .hljs-meta,
  .code-syntax .hljs-tag,
  .code-syntax .hljs-name { color: color-mix(in srgb, var(--info), var(--text) 25%); }
  .code-syntax .hljs-property { color: color-mix(in srgb, var(--info), var(--text) 42%); }
  .code-syntax .hljs-addition { color: var(--success); background: color-mix(in srgb, var(--success), transparent 88%); }
  .code-syntax .hljs-deletion { color: var(--error); background: color-mix(in srgb, var(--error), transparent 88%); }
  .code-syntax .hljs-emphasis { font-style: italic; }
  .code-syntax .hljs-strong { font-weight: 700; }
`;

export function CodeViewer({
  path,
  content,
  viewerRef,
}: {
  path: string;
  content: string;
  viewerRef?: RefObject<HTMLDivElement | null>;
}) {
  const oversized=content.length>500_000;
  content=oversized?content.slice(0,500_000)+"\n\n[Preview limited to 500 KB. Download the file to view all content.]":content;
  const language = syntaxLanguageForPath(path);
  const highlighted = useMemo(() => {
    if (!language.id || content.length>200_000) return escapeHTML(content);
    try {
      return hljs.highlight(content, { language: language.id, ignoreIllegals: true }).value;
    } catch {
      return escapeHTML(content);
    }
  }, [content, language.id]);
  const lineNumbers = useMemo(
    () => Array.from({ length: Math.max(1, content.split("\n").length) }, (_, i) => String(i + 1)).join("\n"),
    [content],
  );

  return (
    <div ref={viewerRef} className="code-syntax min-w-full flex items-start bg-bg">
      <style>{CODE_SYNTAX_THEME}</style>
      <pre
        aria-hidden="true"
        className="sticky left-0 z-10 shrink-0 select-none border-r border-border/70 bg-bg-subtle px-3 py-4 text-right font-mono text-[11px] leading-5 text-text-dim"
      >{lineNumbers}</pre>
      <pre className="min-w-max flex-1 bg-bg px-4 py-4 font-mono text-[12px] leading-5 text-text" style={{ tabSize: 2 }}>
        <code className={language.id ? `hljs language-${language.id}` : "hljs"} dangerouslySetInnerHTML={{ __html: highlighted }} />
      </pre>
    </div>
  );
}
