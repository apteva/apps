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
  server_url?: string;
  subscribe_url?: string;
  provider_url?: string;
  provider_topic_url?: string;
  telegram_chat_id?: string;
  connection_id?: number;
  parse_mode?: string;
  webhook_url?: string;
  stream_json?: string;
  stream_sse?: string;
  stream_json_url?: string;
  stream_sse_url?: string;
  local_stream_json_url?: string;
  capabilities?: string[];
}

interface Conversation {
  id: string;
  agent_id: number;
  instance_id: number;
  project_id: string;
  title: string;
  channel: string;
  thread_id?: string;
  updated_at: string;
}

interface Message {
  id: number;
  chat_id: string;
  role: "user" | "agent" | "system";
  content: string;
  status: string;
  created_at: string;
}

interface ChannelResponse {
  channels: Channel[];
  project_id: string;
}

type View = "inbox" | "channels";
type ChannelType = "ntfy" | "telegram";

export default function ChannelsPanel({ projectId }: NativePanelProps) {
  const [view, setView] = useState<View>("inbox");
  const [status, setStatus] = useState("Loading");

  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [selectedConversationId, setSelectedConversationId] = useState("");
  const [messages, setMessages] = useState<Message[]>([]);
  const [draft, setDraft] = useState("");

  const [channels, setChannels] = useState<Channel[]>([]);
  const [selectedId, setSelectedId] = useState("");
  const [creating, setCreating] = useState(false);
  const [channelType, setChannelType] = useState<ChannelType>("ntfy");
  const [name, setName] = useState("ntfy");
  const [agentId, setAgentId] = useState("");
  const [inbound, setInbound] = useState(false);
  const [topic, setTopic] = useState("");
  const [telegramChatId, setTelegramChatId] = useState("");
  const [telegramBotToken, setTelegramBotToken] = useState("");
  const [telegramParseMode, setTelegramParseMode] = useState("");
  const [testTitle, setTestTitle] = useState("Apteva");
  const [testMessage, setTestMessage] = useState("Test notification from Channels");

  const appURL = useCallback((path: string) => {
    const sep = path.includes("?") ? "&" : "?";
    return `${API}${path}${sep}project_id=${encodeURIComponent(projectId || "")}`;
  }, [projectId]);

  const loadConversations = useCallback(async () => {
    const res = await fetch(appURL("/chats"), { credentials: "same-origin" });
    if (!res.ok) {
      setStatus(`Inbox load failed: ${res.status}`);
      return;
    }
    const rows = ((await res.json()) || []) as Conversation[];
    setConversations(rows);
    setSelectedConversationId((current) => current && rows.some((row) => row.id === current) ? current : rows[0]?.id || "");
    setStatus(`${rows.length} conversations`);
  }, [appURL]);

  const loadMessages = useCallback(async (chatId: string) => {
    if (!chatId) {
      setMessages([]);
      return;
    }
    const res = await fetch(appURL(`/messages?chat_id=${encodeURIComponent(chatId)}`), { credentials: "same-origin" });
    if (!res.ok) {
      setStatus(`Messages failed: ${res.status}`);
      return;
    }
    setMessages(((await res.json()) || []) as Message[]);
  }, [appURL]);

  const loadChannels = useCallback(async () => {
    const res = await fetch(appURL("/channels"), { credentials: "same-origin" });
    if (!res.ok) {
      setStatus(`Channels load failed: ${res.status}`);
      return;
    }
    const data = (await res.json()) as ChannelResponse;
    const rows = data.channels || [];
    setChannels(rows);
    setSelectedId((current) => current && rows.some((ch) => ch.id === current) ? current : rows[0]?.id || "");
    setStatus(`${rows.length} channels`);
  }, [appURL]);

  const reload = useCallback(async () => {
    await Promise.all([loadConversations(), loadChannels()]);
  }, [loadChannels, loadConversations]);

  useEffect(() => {
    reload();
  }, [reload]);

  const selectedConversation = useMemo(
    () => conversations.find((row) => row.id === selectedConversationId) || conversations[0],
    [conversations, selectedConversationId],
  );

  useEffect(() => {
    if (!selectedConversation?.id) {
      setMessages([]);
      return;
    }
    loadMessages(selectedConversation.id);
  }, [loadMessages, selectedConversation?.id]);

  useEffect(() => {
    if (!selectedConversation?.id) return;
    const url = appURL(`/stream?chat_id=${encodeURIComponent(selectedConversation.id)}&since=0`);
    const es = new EventSource(url, { withCredentials: true });
    es.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data) as Message;
        setMessages((prev) => prev.some((m) => m.id === msg.id) ? prev : [...prev, msg]);
      } catch {}
    };
    return () => es.close();
  }, [appURL, selectedConversation?.id]);

  const selected = useMemo(
    () => channels.find((ch) => ch.id === selectedId) || channels[0],
    [channels, selectedId],
  );

  const createChannel = useCallback(async (regenerate = false) => {
    if (channelType === "telegram" && !telegramChatId.trim()) {
      setStatus("Telegram chat id required");
      return;
    }
    if (channelType === "telegram" && !telegramBotToken.trim()) {
      setStatus("Telegram bot token required");
      return;
    }
    const res = await fetch(appURL("/channels"), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        agent_id: inbound && agentId.trim() ? Number(agentId) : 0,
        project_id: projectId,
        name: name.trim() || (channelType === "telegram" ? "Telegram" : "ntfy"),
        type: channelType,
        topic: regenerate ? "" : topic.trim(),
        regenerate,
        chat_id: channelType === "telegram" ? telegramChatId.trim() : undefined,
        bot_token: channelType === "telegram" ? telegramBotToken.trim() : undefined,
        parse_mode: channelType === "telegram" ? telegramParseMode.trim() : undefined,
      }),
    });
    if (!res.ok) {
      setStatus(`Save failed: ${res.status}`);
      return;
    }
    const ch = (await res.json()) as Channel;
    setTopic(ch.topic || "");
    if (channelType === "telegram") setTelegramBotToken("");
    setCreating(false);
    setStatus(regenerate ? "Topic generated" : "Channel created");
    await reload();
    setSelectedId(ch.id);
    setView("channels");
  }, [agentId, appURL, channelType, inbound, name, projectId, reload, telegramBotToken, telegramChatId, telegramParseMode, topic]);

  const sendChatMessage = useCallback(async () => {
    if (!selectedConversation?.id || !draft.trim()) return;
    const res = await fetch(appURL(`/messages?chat_id=${encodeURIComponent(selectedConversation.id)}`), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ content: draft.trim() }),
    });
    if (!res.ok) {
      setStatus(`Send failed: ${res.status}`);
      return;
    }
    setDraft("");
    await loadMessages(selectedConversation.id);
  }, [appURL, draft, loadMessages, selectedConversation?.id]);

  const testChannel = useCallback(async () => {
    if (!selected || !selected.topic) {
      setStatus("Select a channel");
      return;
    }
    let res: Response;
    if (selected.type === "telegram") {
      res = await fetch(appURL("/telegram/test"), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          topic: selected.topic,
          title: testTitle,
          message: testMessage,
          actions: [
            { type: "callback", label: "Approve", value: "approve_test" },
            { type: "callback", label: "Reject", value: "reject_test" },
          ],
        }),
      });
    } else {
      res = await fetch(appURL(`/ntfy/${encodeURIComponent(selected.topic)}`), {
        method: "POST",
        credentials: "same-origin",
        headers: {
          "Content-Type": "text/plain",
          "Title": testTitle,
          "Tags": "bell",
        },
        body: testMessage,
      });
    }
    setStatus(res.ok ? "Test sent" : `Test failed: ${res.status}`);
    if (res.ok) await loadConversations();
  }, [appURL, loadConversations, selected, testMessage, testTitle]);

  const registerTelegramWebhook = useCallback(async () => {
    if (!selected || selected.type !== "telegram" || !selected.topic) {
      setStatus("Select a Telegram channel");
      return;
    }
    const res = await fetch(appURL("/telegram/register-webhook"), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ topic: selected.topic }),
    });
    setStatus(res.ok ? "Telegram webhook registered" : `Webhook failed: ${res.status}`);
  }, [appURL, selected]);

  const deleteChannel = useCallback(async () => {
    if (!selected) return;
    if (!window.confirm(`Delete ${selected.label || selected.id}?`)) return;
    const res = await fetch(appURL(`/channels?channel_id=${encodeURIComponent(selected.id)}`), {
      method: "DELETE",
      credentials: "same-origin",
    });
    if (!res.ok) {
      setStatus(`Delete failed: ${res.status}`);
      return;
    }
    setStatus("Channel deleted");
    await reload();
  }, [appURL, reload, selected]);

  const selectedSubscribeURL = selected?.type === "ntfy" && selected.topic
    ? selected.provider_topic_url || selected.subscribe_url || `https://ntfy.sh/${selected.topic}`
    : "";
  const selectedServerURL = selected?.type === "ntfy" && selected.topic
    ? selected.provider_url || selected.server_url || "https://ntfy.sh"
    : "";
  const selectedStreamURL = selected?.type === "ntfy" && selected.topic
    ? selected.local_stream_json_url || selected.stream_json_url || `${API}/ntfy/${selected.topic}/json`
    : "";

  return (
    <div className="h-full min-h-0 bg-bg text-text">
      <div className="flex h-full w-full flex-col gap-4 p-5">
        <header className="flex flex-wrap items-center justify-between gap-3 border-b border-border pb-4">
          <div>
            <h1 className="text-xl font-semibold">Channels</h1>
            <p className="mt-1 text-sm text-text-dim">{status}</p>
          </div>
          <div className="flex items-center gap-2">
            <div className="flex h-9 rounded-md border border-border bg-surface p-1">
              <button className={`rounded px-3 text-sm ${view === "inbox" ? "bg-bg" : "text-text-dim"}`} onClick={() => setView("inbox")}>Inbox</button>
              <button className={`rounded px-3 text-sm ${view === "channels" ? "bg-bg" : "text-text-dim"}`} onClick={() => setView("channels")}>Channels</button>
            </div>
            <button className="h-9 rounded-md border border-border px-3 text-sm" onClick={reload}>Refresh</button>
            <button className="h-9 rounded-md bg-accent px-3 text-sm font-medium text-accent-contrast" onClick={() => { setCreating(true); setView("channels"); }}>
              New Channel
            </button>
          </div>
        </header>

        {view === "inbox" ? (
          <InboxView
            conversations={conversations}
            selectedId={selectedConversation?.id || ""}
            onSelect={setSelectedConversationId}
            messages={messages}
            draft={draft}
            setDraft={setDraft}
            onSend={sendChatMessage}
            selected={selectedConversation}
          />
        ) : (
          <ChannelsView
            channels={channels}
            selectedId={selectedId}
            onSelect={setSelectedId}
            creating={creating}
            selected={selected}
            channelType={channelType}
            setChannelType={(v) => {
              setChannelType(v);
              setName((current) => current === "ntfy" || current === "Telegram" ? (v === "telegram" ? "Telegram" : "ntfy") : current);
            }}
            name={name}
            setName={setName}
            agentId={agentId}
            setAgentId={setAgentId}
            inbound={inbound}
            setInbound={setInbound}
            topic={topic}
            setTopic={setTopic}
            telegramChatId={telegramChatId}
            setTelegramChatId={setTelegramChatId}
            telegramBotToken={telegramBotToken}
            setTelegramBotToken={setTelegramBotToken}
            telegramParseMode={telegramParseMode}
            setTelegramParseMode={setTelegramParseMode}
            onCancel={() => setCreating(false)}
            onSave={() => createChannel(false)}
            onGenerate={() => createChannel(true)}
            subscribeURL={selectedSubscribeURL}
            serverURL={selectedServerURL}
            streamURL={selectedStreamURL}
            testTitle={testTitle}
            setTestTitle={setTestTitle}
            testMessage={testMessage}
            setTestMessage={setTestMessage}
            onTest={testChannel}
            onRegisterTelegramWebhook={registerTelegramWebhook}
            onDelete={deleteChannel}
          />
        )}
      </div>
    </div>
  );
}

function InboxView({
  conversations,
  selectedId,
  onSelect,
  messages,
  draft,
  setDraft,
  onSend,
  selected,
}: {
  conversations: Conversation[];
  selectedId: string;
  onSelect: (id: string) => void;
  messages: Message[];
  draft: string;
  setDraft: (v: string) => void;
  onSend: () => void;
  selected?: Conversation;
}) {
  const canSend = selected?.channel === "chat" && !!selected.agent_id;
  return (
    <main className="grid min-h-0 flex-1 gap-4 lg:grid-cols-[360px_1fr]">
      <section className="min-h-0 overflow-hidden rounded-lg border border-border bg-surface">
        <div className="border-b border-border px-4 py-3 text-sm font-medium">Conversations</div>
        <div className="max-h-full overflow-auto">
          {conversations.length === 0 ? (
            <div className="p-5 text-sm text-text-dim">No conversations yet.</div>
          ) : conversations.map((row) => (
            <button
              key={row.id}
              className={`block w-full border-b border-border p-4 text-left last:border-b-0 ${selectedId === row.id ? "bg-bg" : ""}`}
              onClick={() => onSelect(row.id)}
            >
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="truncate font-medium">{row.title || row.channel}</span>
                    <span className="rounded bg-bg px-2 py-0.5 text-xs uppercase text-text-dim">{row.channel}</span>
                  </div>
                  <div className="mt-1 truncate font-mono text-xs text-text-dim">
                    {row.channel === "ntfy" || row.channel === "telegram" ? row.thread_id : `agent:${row.agent_id || "none"}`}
                  </div>
                </div>
              </div>
            </button>
          ))}
        </div>
      </section>

      <section className="flex min-h-0 flex-col rounded-lg border border-border bg-surface">
        <div className="border-b border-border px-4 py-3">
          <div className="text-sm font-medium">{selected?.title || "Conversation"}</div>
          <div className="mt-1 font-mono text-xs text-text-dim">{selected?.id || ""}</div>
        </div>
        <div className="min-h-0 flex-1 space-y-3 overflow-auto p-4">
          {messages.length === 0 ? (
            <div className="text-sm text-text-dim">No messages.</div>
          ) : messages.map((m) => (
            <div key={m.id} className={`max-w-[80%] rounded-lg border border-border p-3 ${m.role === "agent" ? "ml-auto bg-bg" : "bg-surface"}`}>
              <div className="mb-1 text-xs uppercase text-text-dim">{m.role}</div>
              <div className="whitespace-pre-wrap text-sm">{m.content}</div>
            </div>
          ))}
        </div>
        <div className="border-t border-border p-3">
          {canSend ? (
            <div className="flex gap-2">
              <input
                className="h-10 min-w-0 flex-1 rounded-md border border-border bg-bg px-3 text-sm outline-none focus:border-accent"
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => { if (e.key === "Enter") onSend(); }}
                placeholder="Message"
              />
              <button className="h-10 rounded-md bg-accent px-4 text-sm font-medium text-accent-contrast" onClick={onSend}>Send</button>
            </div>
          ) : (
            <div className="text-sm text-text-dim">This conversation is read-only here.</div>
          )}
        </div>
      </section>
    </main>
  );
}

function ChannelsView(props: {
  channels: Channel[];
  selectedId: string;
  onSelect: (id: string) => void;
  creating: boolean;
  selected?: Channel;
  channelType: ChannelType;
  setChannelType: (v: ChannelType) => void;
  name: string;
  setName: (v: string) => void;
  agentId: string;
  setAgentId: (v: string) => void;
  inbound: boolean;
  setInbound: (v: boolean) => void;
  topic: string;
  setTopic: (v: string) => void;
  telegramChatId: string;
  setTelegramChatId: (v: string) => void;
  telegramBotToken: string;
  setTelegramBotToken: (v: string) => void;
  telegramParseMode: string;
  setTelegramParseMode: (v: string) => void;
  onCancel: () => void;
  onSave: () => void;
  onGenerate: () => void;
  subscribeURL: string;
  serverURL: string;
  streamURL: string;
  testTitle: string;
  setTestTitle: (v: string) => void;
  testMessage: string;
  setTestMessage: (v: string) => void;
  onTest: () => void;
  onRegisterTelegramWebhook: () => void;
  onDelete: () => void;
}) {
  return (
    <main className="grid min-h-0 flex-1 gap-4 lg:grid-cols-[360px_1fr]">
      <section className="min-h-0 overflow-hidden rounded-lg border border-border bg-surface">
        <div className="border-b border-border px-4 py-3 text-sm font-medium">Project Channels</div>
        <div className="max-h-full overflow-auto">
          {props.channels.length === 0 ? (
            <div className="p-5 text-sm text-text-dim">No channels yet.</div>
          ) : props.channels.map((ch) => (
            <button
              key={ch.id}
              className={`block w-full border-b border-border p-4 text-left last:border-b-0 ${props.selectedId === ch.id ? "bg-bg" : ""}`}
              onClick={() => props.onSelect(ch.id)}
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
        {props.creating ? (
          <CreateChannel {...props} />
        ) : props.selected ? (
          <ChannelDetail
            channel={props.selected}
            subscribeURL={props.subscribeURL}
            serverURL={props.serverURL}
            streamURL={props.streamURL}
            testTitle={props.testTitle}
            setTestTitle={props.setTestTitle}
            testMessage={props.testMessage}
            setTestMessage={props.setTestMessage}
            onTest={props.onTest}
            onRegisterTelegramWebhook={props.onRegisterTelegramWebhook}
            onDelete={props.onDelete}
          />
        ) : (
          <div className="p-5 text-sm text-text-dim">Select or create a channel.</div>
        )}
      </section>
    </main>
  );
}

function CreateChannel({
  channelType,
  setChannelType,
  name,
  setName,
  agentId,
  setAgentId,
  inbound,
  setInbound,
  topic,
  setTopic,
  telegramChatId,
  setTelegramChatId,
  telegramBotToken,
  setTelegramBotToken,
  telegramParseMode,
  setTelegramParseMode,
  onCancel,
  onSave,
  onGenerate,
}: {
  channelType: ChannelType;
  setChannelType: (v: ChannelType) => void;
  name: string;
  setName: (v: string) => void;
  agentId: string;
  setAgentId: (v: string) => void;
  inbound: boolean;
  setInbound: (v: boolean) => void;
  topic: string;
  setTopic: (v: string) => void;
  telegramChatId: string;
  setTelegramChatId: (v: string) => void;
  telegramBotToken: string;
  setTelegramBotToken: (v: string) => void;
  telegramParseMode: string;
  setTelegramParseMode: (v: string) => void;
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
          <select
            className="h-9 w-full rounded-md border border-border bg-bg px-3 text-sm outline-none"
            value={channelType}
            onChange={(e) => setChannelType(e.target.value as ChannelType)}
          >
            <option value="ntfy">ntfy</option>
            <option value="telegram">Telegram</option>
          </select>
        </label>
      </div>
      {channelType === "telegram" && (
        <div className="rounded-md border border-border bg-bg p-3 text-sm">
          <div className="font-medium">Telegram setup</div>
          <ol className="mt-2 list-decimal space-y-1 pl-5 text-text-dim">
            <li>Create a bot with BotFather and paste the bot token below.</li>
            <li>Add the bot to your Telegram group, or open a direct chat with it.</li>
            <li>Send any message to the bot/group, then open <code className="font-mono">https://api.telegram.org/botTOKEN/getUpdates</code> and copy the chat id.</li>
            <li>Create the channel, then select it and register the webhook.</li>
          </ol>
        </div>
      )}
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
      {channelType === "telegram" && (
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="block text-sm">
            <span className="mb-1 block text-text-dim">Bot token</span>
            <input
              className="h-9 w-full rounded-md border border-border bg-bg px-3 font-mono text-xs outline-none focus:border-accent"
              value={telegramBotToken}
              onChange={(e) => setTelegramBotToken(e.target.value)}
              placeholder="123456:ABC..."
              type="password"
            />
          </label>
          <label className="block text-sm">
            <span className="mb-1 block text-text-dim">Telegram chat id</span>
            <input
              className="h-9 w-full rounded-md border border-border bg-bg px-3 font-mono text-sm outline-none focus:border-accent"
              value={telegramChatId}
              onChange={(e) => setTelegramChatId(e.target.value)}
              placeholder="-1001234567890"
            />
          </label>
          <label className="block text-sm">
            <span className="mb-1 block text-text-dim">Parse mode</span>
            <select
              className="h-9 w-full rounded-md border border-border bg-bg px-3 text-sm outline-none"
              value={telegramParseMode}
              onChange={(e) => setTelegramParseMode(e.target.value)}
            >
              <option value="">None</option>
              <option value="MarkdownV2">MarkdownV2</option>
              <option value="HTML">HTML</option>
            </select>
          </label>
        </div>
      )}
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
  serverURL,
  streamURL,
  testTitle,
  setTestTitle,
  testMessage,
  setTestMessage,
  onTest,
  onRegisterTelegramWebhook,
  onDelete,
}: {
  channel: Channel;
  subscribeURL: string;
  serverURL: string;
  streamURL: string;
  testTitle: string;
  setTestTitle: (v: string) => void;
  testMessage: string;
  setTestMessage: (v: string) => void;
  onTest: () => void;
  onRegisterTelegramWebhook: () => void;
  onDelete: () => void;
}) {
  return (
    <div className="space-y-5 p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold">{channel.label || channel.type}</h2>
          <div className="mt-1 font-mono text-xs text-text-dim">{channel.mcp_id || channel.id}</div>
        </div>
        <div className="flex items-center gap-2">
          <span className="rounded bg-bg px-2 py-1 text-xs uppercase text-text-dim">{channel.type}</span>
          {channel.type !== "chat" && (
            <button className="h-8 rounded-md border border-border px-3 text-sm text-error" onClick={onDelete}>Delete</button>
          )}
        </div>
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <ReadOnly label="Inbound Agent" value={channel.agent_id ? String(channel.agent_id) : "None"} />
        <ReadOnly label="Project" value={channel.project_id || ""} />
        {channel.type === "ntfy" && <ReadOnly label="Server URL" value={serverURL} />}
        {channel.type === "ntfy" && <ReadOnly label="Topic" value={channel.topic || ""} />}
        {channel.type === "ntfy" && <ReadOnly label="Topic URL" value={subscribeURL} />}
        {channel.type === "ntfy" && <ReadOnly label="Local JSON Stream" value={streamURL} />}
        {channel.type === "telegram" && <ReadOnly label="Topic" value={channel.topic || ""} />}
        {channel.type === "telegram" && <ReadOnly label="Telegram Chat ID" value={channel.telegram_chat_id || ""} />}
        {channel.type === "telegram" && <ReadOnly label="Webhook URL" value={channel.webhook_url || ""} />}
        {channel.type === "telegram" && <ReadOnly label="Parse Mode" value={channel.parse_mode || "None"} />}
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
      {channel.type === "telegram" && (
        <div className="space-y-4 border-t border-border pt-4">
          <div className="rounded-md border border-border bg-bg p-3 text-sm">
            <div className="font-medium">Telegram configuration</div>
            <ol className="mt-2 list-decimal space-y-1 pl-5 text-text-dim">
              <li>In Telegram, send a message to the bot or the group where the bot was added.</li>
              <li>Use BotFather token + <code className="font-mono">getUpdates</code> to confirm the chat id matches this channel.</li>
              <li>Register the webhook after the channel is created so inbound messages and button taps return here.</li>
              <li>Agents send with <code className="font-mono">{`respond(channel="${channel.mcp_id || channel.id}", text="...")`}</code>.</li>
            </ol>
          </div>
          <div className="flex flex-wrap gap-2">
            <button className="h-9 rounded-md border border-border px-3 text-sm" onClick={onRegisterTelegramWebhook}>Register Webhook</button>
            <button className="h-9 rounded-md border border-border px-3 text-sm" onClick={onTest}>Send Approve/Reject Test</button>
          </div>
          <div className="grid gap-2 md:grid-cols-[220px_1fr]">
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
