// SimulatorPanel — standalone device-farm UI for the Simulator app.
//
// Boot iOS/Android sims, see them live (DeviceFrame), drive input, tail
// logs, take screenshots. Code's panel embeds the same DeviceFrame for
// a repo's dev run; this panel is the direct interface for working with
// devices outside the editor (testing a pre-built flow, hand-driving a
// device, checking capabilities).

import { useCallback, useEffect, useState } from "react";
import { DeviceFrame } from "./components/DeviceFrame";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface ToolProbe {
  name: string;
  found: boolean;
  path?: string;
  version?: string;
  note?: string;
}
interface PlatformCapability {
  available: boolean;
  reasons: string[];
  streaming_available?: boolean;
  streaming_reasons?: string[];
  tools: Record<string, ToolProbe>;
}
interface Capabilities {
  android: PlatformCapability;
  ios: PlatformCapability;
}

interface Sim {
  id: string;
  platform: string;
  runtime: string;
  device_type: string;
  status: string;
  serial: string;
  booted_at?: string;
}

const API = "/api/apps/simulator/api";
const DEFAULT_ANDROID_IMAGE = "system-images;android-34;google_apis;x86_64";
const DEFAULT_ANDROID_DEVICE_TYPE = "pixel_6";
const DEFAULT_IOS_RUNTIME = "";
const DEFAULT_IOS_DEVICE_TYPE = "iPhone-15-Pro";

export default function SimulatorPanel({ projectId }: NativePanelProps) {
  const [caps, setCaps] = useState<Capabilities | null>(null);
  const [sims, setSims] = useState<Sim[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [streamUrl, setStreamUrl] = useState<string | null>(null);
  const [screenshotUrl, setScreenshotUrl] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const withParams = useCallback(
    (extra?: Record<string, string>) =>
      new URLSearchParams({ project_id: projectId, ...(extra ?? {}) }).toString(),
    [projectId],
  );

  const loadCaps = useCallback(async () => {
    try {
      const r = await fetch(`${API}/capabilities?${withParams()}`, { credentials: "same-origin" });
      setCaps(await r.json());
    } catch (e) {
      setError("Failed to load capabilities: " + (e as Error).message);
    }
  }, [withParams]);

  const loadSims = useCallback(async () => {
    try {
      const r = await fetch(`${API}/sims?${withParams()}`, { credentials: "same-origin" });
      const j = await r.json();
      const next = j.sims ?? [];
      setSims(next);
    } catch {
      /* background poll — ignore */
    }
  }, [withParams]);

  useEffect(() => {
    loadCaps();
    loadSims();
    const t = setInterval(loadSims, 3000);
    return () => clearInterval(t);
  }, [loadCaps, loadSims]);

  const boot = async (platform: string) => {
    setBusy(true);
    setError("");
    setScreenshotUrl(null);
    try {
      const payload =
        platform === "ios"
          ? { platform, image: DEFAULT_IOS_RUNTIME, device_type: DEFAULT_IOS_DEVICE_TYPE }
          : {
              platform,
              image: DEFAULT_ANDROID_IMAGE,
              device_type: DEFAULT_ANDROID_DEVICE_TYPE,
            };
      const r = await fetch(`${API}/sims/boot?${withParams()}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      if (!r.ok) throw new Error((await r.json()).error ?? r.statusText);
      const sim: Sim = await r.json();
      await loadSims();
      setSelected(sim.id);
    } catch (e) {
      setError("Boot failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const shutdown = async (id: string) => {
    setBusy(true);
    try {
      await fetch(`${API}/sims/${encodeURIComponent(id)}/shutdown?${withParams()}`, {
        method: "POST",
        credentials: "same-origin",
      });
      if (selected === id) {
        setSelected(null);
        setStreamUrl(null);
        setScreenshotUrl(null);
      }
      await loadSims();
    } catch (e) {
      setError("Shutdown failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const openStream = useCallback(async (id: string) => {
    setSelected(id);
    setStreamUrl(null);
    setScreenshotUrl(null);
    const sim = sims.find((s) => s.id === id);
    if (sim && sim.status !== "booted") {
      setError("");
      return;
    }
    const platformCaps = sim?.platform === "ios" ? caps?.ios : caps?.android;
    if (platformCaps && platformCaps.streaming_available === false) {
      setError("Live view unavailable: " + (platformCaps.streaming_reasons ?? []).join("; "));
      return;
    }
    try {
      const r = await fetch(`${API}/sims/${encodeURIComponent(id)}/stream-url?${withParams()}`, {
        method: "POST",
        credentials: "same-origin",
      });
      if (!r.ok) throw new Error((await r.json()).error ?? r.statusText);
      const j = await r.json();
      setStreamUrl(j.stream_url);
    } catch (e) {
      setError("Stream failed: " + (e as Error).message);
    }
  }, [caps, sims, withParams]);

  const showScreenshot = (id: string) => {
    setStreamUrl(null);
    setError("");
    setScreenshotUrl(`${API}/sims/${encodeURIComponent(id)}/screenshot?${withParams({ t: String(Date.now()) })}`);
  };

  const selectedSim = sims.find((s) => s.id === selected) ?? null;
  const selectedCaps = selectedSim?.platform === "ios" ? caps?.ios : caps?.android;
  const canStreamSelected = selectedCaps?.streaming_available !== false;

  useEffect(() => {
    if (selectedSim?.status === "booted" && canStreamSelected && !streamUrl && !busy) {
      void openStream(selectedSim.id);
    }
  }, [selectedSim?.id, selectedSim?.status, canStreamSelected, streamUrl, busy, openStream]);

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <header className="px-4 py-3 border-b border-border flex items-center gap-3">
        <h1 className="text-sm font-semibold">Simulator</h1>
        <span className="flex-1" />
        <BootButton
          label="Boot Android"
          enabled={!!caps?.android.available && !busy}
          reasons={caps?.android.reasons ?? []}
          onClick={() => boot("android")}
        />
        <BootButton
          label="Boot iOS"
          enabled={!!caps?.ios.available && !busy}
          reasons={caps?.ios.reasons ?? []}
          onClick={() => boot("ios")}
        />
      </header>

      {error && (
        <div className="px-4 py-2 text-xs text-red border-b border-border bg-red/10">{error}</div>
      )}

      <div className="flex-1 flex overflow-hidden">
        {/* Device list */}
        <aside className="w-64 border-r border-border overflow-y-auto">
          {sims.length === 0 ? (
            <div className="p-4 text-xs text-text-muted">No sims yet. Boot one to begin.</div>
          ) : (
            <ul>
              {sims.map((s) => (
                <li key={s.id}>
                  <button
                    type="button"
                    onClick={() => openStream(s.id)}
                    className={`w-full text-left px-3 py-2 border-b border-border hover:bg-bg-input/40 ${
                      selected === s.id ? "bg-bg-input/60" : ""
                    }`}
                  >
                    <div className="flex items-center gap-2">
                      <StatusDot status={s.status} />
                      <span className="text-xs font-medium truncate">{s.device_type || s.id}</span>
                    </div>
                    <div className="text-[11px] text-text-muted mt-0.5">
                      {s.platform} · {s.status}
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </aside>

        {/* Selected device */}
        <main className="flex-1 overflow-y-auto p-4">
          {!selectedSim ? (
            <div className="text-sm text-text-muted text-center mt-12">
              Select a device, or boot one from the header.
            </div>
          ) : (
            <div className="flex flex-col items-center gap-4">
              <div className="w-full flex items-center gap-2">
                <span className="text-xs text-text-muted">
                  {selectedSim.device_type} · {selectedSim.platform} · {selectedSim.status}
                </span>
                <span className="flex-1" />
                {selectedSim.status === "shutdown" ? (
                  <button
                    type="button"
                    onClick={() => boot(selectedSim.platform)}
                    disabled={busy}
                    className="px-2 py-0.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
                  >
                    Boot
                  </button>
                ) : (
                  <button
                    type="button"
                    onClick={() => shutdown(selectedSim.id)}
                    disabled={busy}
                    className="px-2 py-0.5 text-xs border border-red text-red rounded hover:bg-red hover:text-white disabled:opacity-50"
                  >
                    Shut down
                  </button>
                )}
              </div>

              {selectedSim.status === "booted" && streamUrl ? (
                <DeviceFrame streamUrl={streamUrl} platform={selectedSim.platform} />
              ) : selectedSim.status === "booted" && !canStreamSelected ? (
                <div className="flex flex-col items-center gap-3">
                  <div className="max-w-md text-center text-xs text-text-muted">
                    Live view needs idb. The simulator is booted; install idb to stream and send input, or use a screenshot here.
                  </div>
                  <button
                    type="button"
                    onClick={() => showScreenshot(selectedSim.id)}
                    className="px-3 py-1.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg"
                  >
                    Refresh screenshot
                  </button>
                  {screenshotUrl && (
                    <img
                      src={screenshotUrl}
                      alt="Simulator screenshot"
                      className="max-w-[360px] max-h-[70vh] rounded-lg border border-border bg-black object-contain"
                    />
                  )}
                </div>
              ) : selectedSim.status === "booted" ? (
                <button
                  type="button"
                  onClick={() => openStream(selectedSim.id)}
                  className="px-3 py-1.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg"
                >
                  Start live view
                </button>
              ) : (
                <div className="text-xs text-text-muted">Device is {selectedSim.status}.</div>
              )}
            </div>
          )}
        </main>
      </div>
    </div>
  );
}

function BootButton({
  label,
  enabled,
  reasons,
  onClick,
}: {
  label: string;
  enabled: boolean;
  reasons: string[];
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={!enabled}
      title={enabled ? "" : reasons.join("\n")}
      className="px-2.5 py-1 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-40 disabled:cursor-not-allowed"
    >
      {label}
    </button>
  );
}

function StatusDot({ status }: { status: string }) {
  const cls =
    status === "booted"
      ? "text-green"
      : status === "booting"
        ? "text-blue"
        : status === "crashed"
          ? "text-red"
          : "text-text-dim";
  return <span className={`text-xs ${cls}`}>●</span>;
}
