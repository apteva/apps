import { useEffect, useId, useRef, type ReactNode } from "react";
import { marked } from "marked";
import createDOMPurify from "dompurify";
import styles from "./result-content.css" with { type: "text" };

function Markdown({ text }: { text: string }) {
  const html = createDOMPurify(window).sanitize(marked.parse(text, { async: false, breaks: true }), {
    ALLOWED_TAGS: ["p", "br", "strong", "em", "del", "a", "ul", "ol", "li", "blockquote", "h1", "h2", "h3", "h4", "h5", "h6", "pre", "code", "hr", "table", "thead", "tbody", "tr", "th", "td"],
    ALLOWED_ATTR: ["href", "title"],
  });
  return <div className="result-markdown" dangerouslySetInnerHTML={{ __html: html }} />;
}

function StructuredValue({ value }: { value: unknown }) {
  if (typeof value === "string") return <Markdown text={value} />;
  if (Array.isArray(value)) return value.length
    ? <ol className="result-items">{value.map((item, i) => <li key={i}><StructuredValue value={item} /></li>)}</ol>
    : <p className="muted">No items</p>;
  if (value && typeof value === "object") return <dl className="result-fields">{Object.entries(value).map(([key, item]) => (
    <div key={key}><dt>{key.replace(/_/g, " ")}</dt><dd><StructuredValue value={item} /></dd></div>
  ))}</dl>;
  return <span>{value === null ? "Not provided" : typeof value === "boolean" ? value ? "Yes" : "No" : String(value)}</span>;
}

export default function ResultContent({ content }: { content: string }) {
  let value: unknown = content;
  try {
    // Preserve numeric receipt IDs and decimal text without floating-point rounding.
    // Quoted strings are matched first and remain unchanged.
    const exactNumbers = content.replace(/"(?:\\.|[^"\\])*"|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g,
      (token, number) => number ? JSON.stringify(number) : token);
    value = JSON.parse(exactNumbers);
  } catch { /* Plain text and Markdown are valid outputs too. */ }
  return <div className="result-content"><style>{styles}</style><StructuredValue value={value} /></div>;
}

export function ContentModal({ title, children, onClose }: { title: string; children: ReactNode; onClose: () => void }) {
  const titleID = useId();
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const previousFocus = document.activeElement as HTMLElement | null;
    const previousOverflow = document.body.style.overflow;
    dialog.current?.showModal();
    document.body.style.overflow = "hidden";
    return () => {
      document.body.style.overflow = previousOverflow;
      previousFocus?.focus();
    };
  }, []);
  return <dialog ref={dialog} className="run-result-modal" aria-labelledby={titleID} onCancel={onClose} onClose={onClose}>
    <style>{styles}</style>
    <header><h2 id={titleID}>{title}</h2><button type="button" onClick={onClose} autoFocus>Close</button></header>
    <div className="run-result-body">{children}</div>
  </dialog>;
}

export function ResultModal({ content, onClose }: { content: string; onClose: () => void }) {
  return <ContentModal title="Process result" onClose={onClose}><ResultContent content={content} /></ContentModal>;
}
