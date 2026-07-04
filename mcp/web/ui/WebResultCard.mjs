import React from "react";

export default function WebResultCard(props) {
  const title = props.title || "Web result";
  const url = props.url || "https://example.com";
  return React.createElement("div", {
    style: { border: "1px solid var(--border, #d0d7de)", borderRadius: 8, padding: 12, maxWidth: 520, background: "var(--bg, white)" }
  },
    React.createElement("a", { href: url, target: "_blank", rel: "noreferrer", style: { fontWeight: 600, color: "inherit", textDecoration: "none" } }, title),
    props.snippet && React.createElement("p", { style: { fontSize: 13, margin: "6px 0 0", color: "var(--text-muted, #64748b)" } }, props.snippet),
    React.createElement("div", { style: { fontSize: 12, marginTop: 8, color: "var(--text-muted, #64748b)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } },
      props.source ? `${props.source} - ${url}` : url)
  );
}
