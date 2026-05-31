import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/channels";

interface NativePanelProps {
  projectId: string;
}

interface Channel {
  id: string;
  mcp_id?: string;
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
  project_id: string;
}

export default function ChannelsPanel({ projectId }: NativePanelProps) {
  const [channels, setChannels] = useState<Channel[]>([]);
  const [selectedId, setSelectedId] = useState("");
  const [status, setStatus] = useState("Loading");
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("ntfy");
  const [agentId, setAgentId] = useState("");
  const [inbound, setInbound] = useState(false);
  const [topic, setTopic] = useState("");
  const [testTitle, setTestTitle] = useState("Apteva");
  const [testMessage, setTestMessage] = useState("Test notification from Channels");

  const appURL = useCallback((path: string) => {
    const sep = path.includes("?") ? "&" : "?";
    return `${API}${path}${sep}project_id=${encodeURIComponent(projectId || "")}`;
  }, [projectId]);

  const load = useCallback(async () => {
    const res = await fetch(appURL("/channels"), { credentials: "same-origin" });
    if (!res.ok) {
      setStatus(`Load failed: ${res.status}`);
      return;
    }
    const data = (await res.json()) as ChannelResponse;
    const rows = data.channels || [];
    setChannels(rows);
    setSelectedId((current) => current && rows.some((ch) => ch.id === current) ? current : rows[0]?.id || "");
    setStatus(`${rows.length} channels`);
  }, [appURL]);

  useEffect(() => {
    load();
  }, [load]);

  const selected = useMemo(
    () => channels.find((ch) => ch.id === selectedId) || channels[0],
    [channels, selectedId],
  );

  const createNtfy = useCallback(async (regenerate = false) => {
    const res = await fetch(appURL("/channels"), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        agent_id: inbound && agentId.trim() ? Number(agentId) : 0,
        project_id: projectId,
        name: name.trim() || "ntfy",
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
    setCreating(false);
    setStatus(regenerate ? "Topic regenerated" : "Channel saved");
    await load();
    setSelectedId(ch.id);
  }, [agentId, appURL, inbound, load, name, projectId, topic]);

  const testNtfy = useCallback(async () => {
    if (!selected || selected.type !== "ntfy" || !selected.topic) {
      setStatus("Select an ntfy channel");
      return;
    }
    const res = await fetch(appURL(`/ntfy/${encodeURIComponent(selected.topic)}`), {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "text/plain",
        "Title": testTitle,
        "Tags": "bell",
      },
      body: testMessage,
    });
    setStatus(res.ok ? "Test sent" : `Test failed: ${res.status}`);
  }, [appURL, selected, testMessage, testTitle]);

  const selectedSubscribeURL = selected?.type === "ntfy" && selected.topic
    ? `${window.location.origin}${API}/ntfy/${selected.topic}`
    : "";
  const selectedStreamURL = selected?.type === "ntfy" && selected.topic
    ? `${window.location.origin}${API}/ntfy/${selected.topic}/json`
    : "";

  return (
    <div className="h-full min-h-0 bg-bg text-text">
      <div className="mx-auto flex h-full max-w-6xl flex-col gap-4 p-5">
        <header className="flex flex-wrap items-center justify-between gap-3 border-b border-border pb-4">
          <div>
            <h1 className="text-xl font-semibold">Channels</h1>
            <p className="mt-1 text-sm text-text-dim">{status}</p>
          </div>
          <div className="flex items-center gap-2">
            <button className="h-9 rounded-md border border-border px-3 text-sm" onClick={load}>
              Refresh
            </button>
            <button className="h-9 rounded-md bg-accent px-3 text-sm font-medium text-accent-contrast" onClick={() => setCreating(true)}>
              New Channel
            </button>
          </div>
        </header>

        <main className="grid min-h-0 flex-1 gap-4 lg:grid-cols-[360px_1fr]">
          <section className="min-h-0 overflow-hidden rounded-lg border border-border bg-surface">
            <div className="border-b border-border px-4 py-3 text-sm font-medium">Project Channels</div>
            <div className="max-h-full overflow-auto">
              {channels.length === 0 ? (
                <div className="p-5 text-sm text-text-dim">No channels yet.</div>
              ) : channels.map((ch) => (
                <button
                  key={ch.id}
                  className={`block w-full border-b border-border p-4 text-left last:border-b-0 ${selectedId === ch.id ? "bg-bg" : ""}`}
                  onClick={() => setSelectedId(ch.id)}
                >
                  <div className="flex items-center justify-between gap-3">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="truncate font-medium">{ch.label || ch.type}</span>
                        <span className="rounded bg-bg px-2 py-0.5 text-xs uppercase text-text-dim">{ch.type}</span>
                      </div>
                      <div className="mt-1 truncate font-mono text-xs text-text-dim">{ch.mcp_id || ch.id}</div>
                    </div>
                    <span className={ch.active ? "text-xs text-success" : "text-xs text-text-dim"}>
                      {ch.active ? "active" : "idle"}
                    </span>
                  </div>
                </button>
              ))}
            </div>
          </section>

          <section className="min-h-0 rounded-lg border border-border bg-surface">
            {creating ? (
              <CreateChannel
                name={name}
                setName={setName}
                agentId={agentId}
                setAgentId={setAgentId}
                inbound={inbound}
                setInbound={setInbound}
                topic={topic}
                setTopic={setTopic}
                onCancel={() => setCreating(false)}
                onSave={() => createNtfy(false)}
                onGenerate={() => createNtfy(true)}
              />
            ) : selected ? (
              <ChannelDetail
                channel={selected}
                subscribeURL={selectedSubscribeURL}
                streamURL={selectedStreamURL}
                testTitle={testTitle}
                setTestTitle={setTestTitle}
                testMessage={testMessage}
                setTestMessage={setTestMessage}
                onTest={testNtfy}
              />
            ) : (
              <div className="p-5 text-sm text-text-dim">Select or create a channel.</div>
            )}
          </section>
        </main>
      </div>
    </div>
  );
}

function CreateChannel({
  name,
  setName,
  agentId,
  setAgentId,
  inbound,
  setInbound,
  topic,
  setTopic,
  onCancel,
  onSave,
  onGenerate,
}: {
  name: string;
  setName: (v: string) => void;
  agentId: string;
  setAgentId: (v: string) => void;
  inbound: boolean;
  setInbound: (v: boolean) => void;
  topic: string;
  setTopic: (v: string) => void;
  onCancel: () => void;
  onSave: () => void;
  onGenerate: () => void;
}) {
  return (
    <div className="space-y-4 p-5">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-base font-semibold">New Channel</h2>
        <button className="h-8 rounded-md border border-border px-3 text-sm" onClick={onCancel}>Cancel</button>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="block text-sm">
          <span className="mb-1 block text-text-dim">Name</span>
          <input
            className="h-9 w-full rounded-md border border-border bg-bg px-3 text-sm outline-none focus:border-accent"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Marco Phone"
          />
        </label>
        <label className="block text-sm">
          <span className="mb-1 block text-text-dim">Type</span>
          <select className="h-9 w-full rounded-md border border-border bg-bg px-3 text-sm outline-none">
            <option>ntfy</option>
          </select>
        </label>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={inbound}
          onChange={(e) => setInbound(e.target.checked)}
        />
        <span>Route inbound messages to an agent</span>
      </label>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="block text-sm">
          <span className="mb-1 block text-text-dim">Inbound Agent</span>
          <input
            className="h-9 w-full rounded-md border border-border bg-bg px-3 text-sm outline-none focus:border-accent"
            value={agentId}
            onChange={(e) => setAgentId(e.target.value)}
            placeholder={inbound ? "Agent id" : "Optional"}
            inputMode="numeric"
            disabled={!inbound}
          />
        </label>
      </div>
      <label className="block text-sm">
        <span className="mb-1 block text-text-dim">Topic</span>
        <input
          className="h-9 w-full rounded-md border border-border bg-bg px-3 font-mono text-sm outline-none focus:border-accent"
          value={topic}
          onChange={(e) => setTopic(e.target.value)}
          placeholder="Generated if blank"
        />
      </label>
      <div className="flex flex-wrap gap-2">
        <button className="h-9 rounded-md bg-accent px-3 text-sm font-medium text-accent-contrast" onClick={onSave}>Create</button>
        <button className="h-9 rounded-md border border-border px-3 text-sm" onClick={onGenerate}>Generate Topic</button>
      </div>
    </div>
  );
}

function ChannelDetail({
  channel,
  subscribeURL,
  streamURL,
  testTitle,
  setTestTitle,
  testMessage,
  setTestMessage,
  onTest,
}: {
  channel: Channel;
  subscribeURL: string;
  streamURL: string;
  testTitle: string;
  setTestTitle: (v: string) => void;
  testMessage: string;
  setTestMessage: (v: string) => void;
  onTest: () => void;
}) {
  return (
    <div className="space-y-5 p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold">{channel.label || channel.type}</h2>
          <div className="mt-1 font-mono text-xs text-text-dim">{channel.mcp_id || channel.id}</div>
        </div>
        <span className="rounded bg-bg px-2 py-1 text-xs uppercase text-text-dim">{channel.type}</span>
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <ReadOnly label="Inbound Agent" value={channel.agent_id ? String(channel.agent_id) : "None"} />
        <ReadOnly label="Project" value={channel.project_id || ""} />
        {channel.type === "ntfy" && <ReadOnly label="Subscribe URL" value={subscribeURL} />}
        {channel.type === "ntfy" && <ReadOnly label="JSON Stream" value={streamURL} />}
      </div>

      <div className="flex flex-wrap gap-1">
        {(channel.capabilities || []).map((cap) => (
          <span key={cap} className="rounded border border-border px-2 py-1 text-xs text-text-dim">{cap}</span>
        ))}
      </div>

      {channel.type === "ntfy" && (
        <div className="space-y-2 border-t border-border pt-4">
          <div className="text-sm font-medium">Test</div>
          <div className="grid gap-2 md:grid-cols-[220px_1fr_auto]">
            <input
              className="h-9 rounded-md border border-border bg-bg px-3 text-sm outline-none focus:border-accent"
              value={testTitle}
              onChange={(e) => setTestTitle(e.target.value)}
              placeholder="Title"
            />
            <input
              className="h-9 rounded-md border border-border bg-bg px-3 text-sm outline-none focus:border-accent"
              value={testMessage}
              onChange={(e) => setTestMessage(e.target.value)}
              placeholder="Message"
            />
            <button className="h-9 rounded-md border border-border px-3 text-sm" onClick={onTest}>Send</button>
          </div>
        </div>
      )}
    </div>
  );
}

function ReadOnly({ label, value }: { label: string; value: string }) {
  return (
    <label className="block min-w-0 text-sm">
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
