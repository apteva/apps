import { useCallback, useMemo, useState } from "react";

const API = "/api/apps/channels";

interface NativePanelProps {
  projectId: string;
}

interface Channel {
  id: string;
  type: string;
  label: string;
  active: boolean;
  agent_id: number;
  project_id: string;
  chat_id: string;
  topic?: string;
  subscribe_path?: string;
  stream_json?: string;
  stream_sse?: string;
  capabilities?: string[];
}

interface ChannelResponse {
  channels: Channel[];
  agent_id: number;
  project_id: string;
}

export default function ChannelsPanel({ projectId }: NativePanelProps) {
  const [agentId, setAgentId] = useState("");
  const [channels, setChannels] = useState<Channel[]>([]);
  const [status, setStatus] = useState("Enter an agent id");
  const [topic, setTopic] = useState("");
  const [testTitle, setTestTitle] = useState("Apteva");
  const [testMessage, setTestMessage] = useState("Test notification from Channels");

  const qs = useMemo(() => {
    const p = new URLSearchParams();
    if (projectId) p.set("project_id", projectId);
    if (agentId.trim()) p.set("agent_id", agentId.trim());
    return p.toString();
  }, [agentId, projectId]);

  const appURL = useCallback((path: string) => {
    const sep = path.includes("?") ? "&" : "?";
    return `${API}${path}${sep}project_id=${encodeURIComponent(projectId || "")}`;
  }, [projectId]);

  const load = useCallback(async () => {
    if (!agentId.trim()) {
      setStatus("Agent id required");
      return;
    }
    const res = await fetch(`${API}/channels?${qs}`, { credentials: "same-origin" });
    if (!res.ok) {
      setStatus(`Load failed: ${res.status}`);
      return;
    }
    const data = (await res.json()) as ChannelResponse;
    setChannels(data.channels || []);
    const ntfy = (data.channels || []).find((c) => c.type === "ntfy");
    if (ntfy?.topic) setTopic(ntfy.topic);
    setStatus(`${data.channels?.length || 0} channels`);
  }, [agentId, qs]);

  const saveNtfy = useCallback(async (regenerate = false) => {
    if (!agentId.trim()) {
      setStatus("Agent id required");
      return;
    }
    const res = await fetch(appURL("/channels"), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        agent_id: Number(agentId),
        project_id: projectId,
        type: "ntfy",
        topic: regenerate ? "" : topic.trim(),
        regenerate,
      }),
    });
    if (!res.ok) {
      setStatus(`Save failed: ${res.status}`);
      return;
    }
    const ch = (await res.json()) as Channel;
    setTopic(ch.topic || "");
    setChannels((prev) => {
      const rest = prev.filter((item) => item.type !== "ntfy");
      return [...rest, ch].sort((a, b) => a.type.localeCompare(b.type));
    });
    setStatus(regenerate ? "Topic regenerated" : "ntfy channel saved");
  }, [agentId, appURL, projectId, topic]);

  const testNtfy = useCallback(async () => {
    const ntfy = channels.find((c) => c.type === "ntfy");
    if (!ntfy?.topic) {
      setStatus("Create ntfy first");
      return;
    }
    const res = await fetch(appURL(`/ntfy/${encodeURIComponent(ntfy.topic)}`), {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "text/plain",
        "Title": testTitle,
        "Tags": "bell",
      },
      body: testMessage,
    });
    setStatus(res.ok ? "Test notification sent" : `Test failed: ${res.status}`);
  }, [appURL, channels, testMessage, testTitle]);

  const ntfy = channels.find((c) => c.type === "ntfy");
  const subscribeURL = ntfy?.topic ? `${window.location.origin}${API}/ntfy/${ntfy.topic}` : "";
  const streamJSON = ntfy?.topic ? `${window.location.origin}${API}/ntfy/${ntfy.topic}/json` : "";

  return (
    <div className="h-full min-h-0 bg-bg text-text">
      <div className="mx-auto flex h-full max-w-6xl flex-col gap-4 p-5">
        <header className="flex flex-wrap items-end justify-between gap-3 border-b border-border pb-4">
          <div>
            <h1 className="text-xl font-semibold">Channels</h1>
            <p className="mt-1 text-sm text-text-dim">{status}</p>
          </div>
          <div className="flex items-center gap-2">
            <input
              className="h-9 w-36 rounded-md border border-border bg-surface px-3 text-sm outline-none focus:border-accent"
              value={agentId}
              onChange={(e) => setAgentId(e.target.value)}
              placeholder="Agent id"
              inputMode="numeric"
            />
            <button className="h-9 rounded-md bg-accent px-3 text-sm font-medium text-accent-contrast" onClick={load}>
              Load
            </button>
          </div>
        </header>

        <main className="grid min-h-0 flex-1 gap-4 lg:grid-cols-[1fr_420px]">
          <section className="min-h-0 rounded-lg border border-border bg-surface">
            <div className="border-b border-border px-4 py-3 text-sm font-medium">Available Channels</div>
            <div className="divide-y divide-border">
              {channels.length === 0 ? (
                <div className="p-5 text-sm text-text-dim">No channels loaded.</div>
              ) : channels.map((ch) => (
                <div key={ch.id} className="flex items-center justify-between gap-3 p-4">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="font-medium">{ch.label || ch.type}</span>
                      <span className="rounded bg-bg px-2 py-0.5 text-xs uppercase text-text-dim">{ch.type}</span>
                      <span className={ch.active ? "text-xs text-success" : "text-xs text-text-dim"}>
                        {ch.active ? "active" : "inactive"}
                      </span>
                    </div>
                    <div className="mt-1 truncate font-mono text-xs text-text-dim">{ch.id}</div>
                  </div>
                  <div className="hidden shrink-0 gap-1 md:flex">
                    {(ch.capabilities || []).map((cap) => (
                      <span key={cap} className="rounded border border-border px-2 py-1 text-xs text-text-dim">{cap}</span>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          </section>

          <aside className="rounded-lg border border-border bg-surface">
            <div className="border-b border-border px-4 py-3 text-sm font-medium">ntfy</div>
            <div className="space-y-4 p-4">
              <label className="block text-sm">
                <span className="mb-1 block text-text-dim">Topic</span>
                <input
                  className="h-9 w-full rounded-md border border-border bg-bg px-3 font-mono text-sm outline-none focus:border-accent"
                  value={topic}
                  onChange={(e) => setTopic(e.target.value)}
                  placeholder="Generated on save"
                />
              </label>
              <div className="flex flex-wrap gap-2">
                <button className="h-9 rounded-md bg-accent px-3 text-sm font-medium text-accent-contrast" onClick={() => saveNtfy(false)}>
                  Save ntfy
                </button>
                <button className="h-9 rounded-md border border-border px-3 text-sm" onClick={() => saveNtfy(true)}>
                  Regenerate
                </button>
              </div>

              <div className="space-y-2 text-sm">
                <ReadOnly label="Subscribe URL" value={subscribeURL} />
                <ReadOnly label="JSON Stream" value={streamJSON} />
                <ReadOnly label="Agent Channel" value={ntfy?.id || ""} />
              </div>

              <div className="space-y-2 border-t border-border pt-4">
                <input
                  className="h-9 w-full rounded-md border border-border bg-bg px-3 text-sm outline-none focus:border-accent"
                  value={testTitle}
                  onChange={(e) => setTestTitle(e.target.value)}
                  placeholder="Title"
                />
                <textarea
                  className="min-h-20 w-full resize-none rounded-md border border-border bg-bg px-3 py-2 text-sm outline-none focus:border-accent"
                  value={testMessage}
                  onChange={(e) => setTestMessage(e.target.value)}
                />
                <button className="h-9 rounded-md border border-border px-3 text-sm" onClick={testNtfy}>
                  Send Test
                </button>
              </div>
            </div>
          </aside>
        </main>
      </div>
    </div>
  );
}

function ReadOnly({ label, value }: { label: string; value: string }) {
  return (
    <label className="block">
      <span className="mb-1 block text-text-dim">{label}</span>
      <input
        className="h-9 w-full rounded-md border border-border bg-bg px-3 font-mono text-xs text-text-dim"
        value={value}
        readOnly
        onFocus={(e) => e.currentTarget.select()}
      />
    </label>
  );
}
