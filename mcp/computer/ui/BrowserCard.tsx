import { useEffect, useState } from "react";
import { Card, CardHeader, DataList } from "@apteva/ui-kit";
import { BACKEND_LABEL, computerVendor, hostFor, panelURL, sessionURL } from "./shared";

interface SessionInfo {
  session_id: string;
  backend: string;
  current_url: string;
  status: string;
}

interface Props {
  session_id?: string;
  /** v0.7.57 compatibility */
  instance_id?: string;
  backend?: string;
  url?: string;
  status?: string;
  projectId?: string;
  preview?: boolean;
}

export default function BrowserCard(props: Props) {
  const id = props.session_id ?? props.instance_id ?? "";
  const [remote, setRemote] = useState<SessionInfo | null>(null);

  useEffect(() => {
    if (props.preview || !id) return;
    let active = true;
    fetch(sessionURL(id, "presentation", props.projectId), { credentials: "include" })
      .then((response) => {
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        return response.json();
      })
      .then((body) => active && setRemote(body.session))
      .catch(() => {});
    return () => {
      active = false;
    };
  }, [id, props.preview, props.projectId]);

  const session = remote ?? {
    session_id: id || "br_example",
    backend: props.backend ?? "local",
    current_url: props.url ?? "https://example.com/",
    status: props.status ?? "active",
  };

  return (
    <Card>
      <CardHeader
        vendor={computerVendor}
        title={hostFor(session.current_url) || "Browser session"}
        subtitle={BACKEND_LABEL[session.backend] ?? session.backend}
        status={{
          label: session.status,
          variant: session.status === "active" ? "active" : "muted",
        }}
        action={props.preview ? undefined : { label: "Open", href: panelURL(session.session_id) }}
      />
      <div className="px-4 py-3 border-t border-border">
        <DataList
          items={[
            { label: "URL", value: session.current_url },
            { label: "Session", value: session.session_id },
          ]}
        />
      </div>
    </Card>
  );
}
