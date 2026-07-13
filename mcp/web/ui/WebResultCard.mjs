import React from "react";

function parseHTTPURL(value) {
  try {
    const parsed = new URL(String(value || ""));
    return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed : null;
  } catch {
    return null;
  }
}

export default function WebResultCard(props) {
  const title = String(props.title || "Web result");
  const parsedURL = parseHTTPURL(props.url);
  const source = props.source ? String(props.source) : "";
  return React.createElement("article", {
    className: "w-full max-w-xl rounded border border-border bg-bg-card p-3 text-text"
  },
    React.createElement("div", { className: "flex min-w-0 items-start gap-2" },
      parsedURL ? React.createElement("a", {
        href: parsedURL.href,
        target: "_blank",
        rel: "noopener noreferrer",
        className: "min-w-0 flex-1 text-sm font-semibold text-text hover:text-accent break-words"
      }, title) : React.createElement("div", { className: "min-w-0 flex-1 text-sm font-semibold break-words" }, title),
      source && React.createElement("span", { className: "shrink-0 rounded border border-border bg-bg-input px-1.5 py-0.5 text-[11px] text-text-muted" }, source)
    ),
    props.snippet && React.createElement("p", { className: "mt-1.5 text-sm text-text-muted break-words" }, String(props.snippet)),
    parsedURL ? React.createElement("div", { className: "mt-2 truncate text-xs text-text-dim", title: parsedURL.href }, `${parsedURL.hostname}${parsedURL.pathname}`) :
      React.createElement("div", { className: "mt-2 text-xs text-red" }, "Invalid or missing web URL")
  );
}
