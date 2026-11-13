import React from "react";
import { createRoot } from "react-dom/client";
import BrowserViewCard, { type BrowserViewProps } from "../BrowserViewCard";
const root = createRoot(document.getElementById("root")!);
(window as any).renderView = (props: BrowserViewProps) => {
  const host = document.getElementById("root")!;
  host.style.width = `${props.presentation?.width ?? 640}px`;
  host.style.height = props.presentation?.mode === "inline" || !props.presentation ? "auto" : `${props.presentation.height}px`;
  root.render(<BrowserViewCard {...props} />);
};
(window as any).renderView({ session_id: "br_one", projectId: "p1", installId: 55 });
