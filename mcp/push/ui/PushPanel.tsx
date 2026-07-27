import { useCallback, useEffect, useState } from "react";

const API = "/api/apps/push";

interface PanelProps {
  projectId?: string;
}

interface Device {
  id: string;
  platform: string;
  bundle_id: string;
  environment: "sandbox" | "production";
  user_ref?: string;
  app_version?: string;
  status: "active" | "invalid" | "revoked";
  last_seen_at: string;
}

interface Delivery {
  id: string;
  device_id: string;
  type: "approval" | "alert" | "report" | "test";
  status: "pending" | "sent" | "failed";
  provider_id?: string;
  error?: string;
  created_at: string;
}

interface Stats {
  provider_ready: boolean;
  active_devices: number;
  sent_today: number;
  failed_today: number;
  recent: Delivery[];
}

const emptyStats: Stats = {
  provider_ready: false,
  active_devices: 0,
  sent_today: 0,
  failed_today: 0,
  recent: [],
};

export default function PushPanel({ projectId = "" }: PanelProps) {
  const [stats, setStats] = useState<Stats>(emptyStats);
  const [devices, setDevices] = useState<Device[]>([]);
  const [loading, setLoading] = useState(true);
  const [message, setMessage] = useState("");

  const appURL = useCallback((path: string) => {
    if (!projectId) return `${API}${path}`;
    const separator = path.includes("?") ? "&" : "?";
    return `${API}${path}${separator}project_id=${encodeURIComponent(projectId)}`;
  }, [projectId]);

  const reload = useCallback(async () => {
    setLoading(true);
    try {
      const [statsResponse, devicesResponse] = await Promise.all([
        fetch(appURL("/stats"), { credentials: "same-origin" }),
        fetch(appURL("/devices?limit=20"), { credentials: "same-origin" }),
      ]);
      if (!statsResponse.ok || !devicesResponse.ok) {
        throw new Error(`Push returned ${statsResponse.ok ? devicesResponse.status : statsResponse.status}`);
      }
      setStats((await statsResponse.json()) as Stats);
      const body = (await devicesResponse.json()) as { devices?: Device[] };
      setDevices(body.devices || []);
      setMessage("");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Could not load Push");
    } finally {
      setLoading(false);
    }
  }, [appURL]);

  useEffect(() => {
    reload();
  }, [reload]);

  const testDevice = useCallback(async (id: string) => {
    setMessage("Sending test…");
    const response = await fetch(appURL(`/admin/devices/${encodeURIComponent(id)}/test`), {
      method: "POST",
      credentials: "same-origin",
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({})) as { error?: string };
      setMessage(body.error || `Test failed (${response.status})`);
      return;
    }
    setMessage("Test notification sent");
    await reload();
  }, [appURL, reload]);

  const revokeDevice = useCallback(async (id: string) => {
    const response = await fetch(appURL(`/admin/devices/${encodeURIComponent(id)}`), {
      method: "DELETE",
      credentials: "same-origin",
    });
    if (!response.ok) {
      setMessage(`Could not revoke device (${response.status})`);
      return;
    }
    setMessage("Device revoked");
    await reload();
  }, [appURL, reload]);

  return (
    <div className="push-panel">
      <style>{styles}</style>

      <header className="push-header">
        <div>
          <div className="push-eyebrow">MOBILE DELIVERY</div>
          <h1>Push</h1>
          <p>Private notifications for connected Apteva devices.</p>
        </div>
        <button className="push-refresh" onClick={reload} disabled={loading}>
          {loading ? "Loading…" : "Refresh"}
        </button>
      </header>

      <section className="push-metrics" aria-label="Push status">
        <Metric
          label="APNs"
          value={stats.provider_ready ? "Connected" : "Not connected"}
          tone={stats.provider_ready ? "good" : "muted"}
        />
        <Metric label="Active devices" value={String(stats.active_devices)} />
        <Metric label="Sent today" value={String(stats.sent_today)} />
        <Metric label="Failed today" value={String(stats.failed_today)} tone={stats.failed_today ? "warn" : "muted"} />
      </section>

      {message && <div className="push-message" role="status">{message}</div>}

      <div className="push-grid">
        <section className="push-card">
          <div className="push-card-heading">
            <div>
              <h2>Devices</h2>
              <p>iPhones registered with this relay.</p>
            </div>
            <span className="push-count">{devices.length}</span>
          </div>

          <div className="push-list">
            {devices.length === 0 && (
              <Empty title="No devices yet" detail="A device appears here after the iOS app enables notifications." />
            )}
            {devices.map((device) => (
              <div className="push-device" key={device.id}>
                <div className="push-device-icon" aria-hidden="true"><span /></div>
                <div className="push-device-copy">
                  <strong>{device.user_ref || "iOS device"}</strong>
                  <span>
                    {device.bundle_id} · {device.environment}
                    {device.app_version ? ` · v${device.app_version}` : ""}
                    {" · "}
                    {relativeTime(device.last_seen_at)}
                  </span>
                </div>
                <span className={`push-status ${device.status}`}>{device.status}</span>
                {device.status === "active" && (
                  <div className="push-actions">
                    <button onClick={() => testDevice(device.id)}>Test</button>
                    <button className="danger" onClick={() => revokeDevice(device.id)}>Revoke</button>
                  </div>
                )}
              </div>
            ))}
          </div>
        </section>

        <section className="push-card">
          <div className="push-card-heading">
            <div>
              <h2>Recent delivery</h2>
              <p>Latest messages accepted or rejected by APNs.</p>
            </div>
          </div>

          <div className="push-list">
            {stats.recent.length === 0 && (
              <Empty title="Nothing sent yet" detail="Delivery history will appear here." />
            )}
            {stats.recent.map((delivery) => (
              <div className="push-delivery" key={delivery.id}>
                <div className={`push-dot ${delivery.status}`} />
                <div className="push-delivery-copy">
                  <strong>{labelForType(delivery.type)}</strong>
                  <span>{relativeTime(delivery.created_at)}</span>
                  {delivery.error && <small title={delivery.error}>{delivery.error}</small>}
                </div>
                <span className={`push-status ${delivery.status}`}>{delivery.status}</span>
              </div>
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}

function Metric({ label, value, tone = "" }: { label: string; value: string; tone?: string }) {
  return (
    <div className={`push-metric ${tone}`}>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function Empty({ title, detail }: { title: string; detail: string }) {
  return (
    <div className="push-empty">
      <strong>{title}</strong>
      <span>{detail}</span>
    </div>
  );
}

function labelForType(type: Delivery["type"]) {
  if (type === "approval") return "Approval";
  if (type === "alert") return "Alert";
  if (type === "report") return "Report";
  return "Test notification";
}

function relativeTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Recently";
  const seconds = Math.max(0, Math.floor((Date.now() - date.getTime()) / 1000));
  if (seconds < 60) return "Just now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

const styles = `
  .push-panel {
    --push-orange: #ff6b1a;
    --push-border: color-mix(in srgb, currentColor 12%, transparent);
    --push-muted: color-mix(in srgb, currentColor 58%, transparent);
    color: var(--text-primary, inherit);
    max-width: 1120px;
    margin: 0 auto;
    padding: 28px;
  }
  .push-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    margin-bottom: 24px;
  }
  .push-eyebrow {
    color: var(--push-orange);
    font: 700 11px/1.2 ui-monospace, SFMono-Regular, Menlo, monospace;
    letter-spacing: .13em;
  }
  .push-header h1 {
    margin: 5px 0 4px;
    font-size: 32px;
    letter-spacing: -.04em;
  }
  .push-header p, .push-card-heading p {
    color: var(--push-muted);
    margin: 0;
    font-size: 14px;
  }
  .push-refresh, .push-actions button {
    appearance: none;
    border: 1px solid var(--push-border);
    background: color-mix(in srgb, currentColor 5%, transparent);
    color: inherit;
    border-radius: 10px;
    padding: 9px 13px;
    font: inherit;
    font-size: 13px;
    cursor: pointer;
  }
  .push-refresh:hover, .push-actions button:hover {
    border-color: color-mix(in srgb, var(--push-orange) 55%, transparent);
  }
  .push-refresh:disabled { opacity: .5; cursor: default; }
  .push-metrics {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 12px;
    margin-bottom: 16px;
  }
  .push-metric {
    border: 1px solid var(--push-border);
    background: color-mix(in srgb, currentColor 3%, transparent);
    border-radius: 14px;
    padding: 15px 16px;
  }
  .push-metric span {
    color: var(--push-muted);
    display: block;
    font-size: 12px;
    margin-bottom: 6px;
  }
  .push-metric strong { font-size: 17px; }
  .push-metric.good strong { color: #24b477; }
  .push-metric.warn strong { color: var(--push-orange); }
  .push-metric.muted strong { color: var(--push-muted); }
  .push-message {
    border: 1px solid color-mix(in srgb, var(--push-orange) 30%, transparent);
    background: color-mix(in srgb, var(--push-orange) 9%, transparent);
    border-radius: 11px;
    color: color-mix(in srgb, var(--push-orange) 85%, currentColor);
    font-size: 13px;
    margin: 0 0 16px;
    padding: 10px 13px;
  }
  .push-grid {
    display: grid;
    grid-template-columns: minmax(0, 1.15fr) minmax(0, .85fr);
    gap: 16px;
  }
  .push-card {
    border: 1px solid var(--push-border);
    background: color-mix(in srgb, currentColor 2.5%, transparent);
    border-radius: 18px;
    min-width: 0;
    overflow: hidden;
  }
  .push-card-heading {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 18px 19px 15px;
  }
  .push-card-heading h2 {
    font-size: 16px;
    margin: 0 0 4px;
  }
  .push-count {
    background: color-mix(in srgb, currentColor 7%, transparent);
    border-radius: 999px;
    color: var(--push-muted);
    font-size: 12px;
    padding: 4px 8px;
  }
  .push-list { border-top: 1px solid var(--push-border); }
  .push-device, .push-delivery {
    display: flex;
    align-items: center;
    gap: 12px;
    min-height: 66px;
    padding: 10px 18px;
  }
  .push-device + .push-device, .push-delivery + .push-delivery {
    border-top: 1px solid var(--push-border);
  }
  .push-device-icon {
    align-items: center;
    background: color-mix(in srgb, var(--push-orange) 14%, transparent);
    border-radius: 11px;
    color: var(--push-orange);
    display: flex;
    height: 38px;
    justify-content: center;
    width: 38px;
  }
  .push-device-icon span {
    border: 1.7px solid currentColor;
    border-radius: 3px;
    box-sizing: border-box;
    display: block;
    height: 20px;
    position: relative;
    width: 12px;
  }
  .push-device-icon span::after {
    background: currentColor;
    border-radius: 50%;
    bottom: 1.5px;
    content: "";
    height: 1.5px;
    left: 50%;
    position: absolute;
    transform: translateX(-50%);
    width: 1.5px;
  }
  .push-device-copy, .push-delivery-copy {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-width: 0;
  }
  .push-device-copy strong, .push-delivery-copy strong { font-size: 14px; }
  .push-device-copy span, .push-delivery-copy span, .push-delivery-copy small {
    color: var(--push-muted);
    font-size: 12px;
    margin-top: 3px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .push-status {
    border-radius: 999px;
    color: var(--push-muted);
    font-size: 11px;
    padding: 4px 7px;
    text-transform: capitalize;
  }
  .push-status.active, .push-status.sent {
    background: color-mix(in srgb, #24b477 12%, transparent);
    color: #24b477;
  }
  .push-status.invalid, .push-status.failed {
    background: color-mix(in srgb, var(--push-orange) 12%, transparent);
    color: var(--push-orange);
  }
  .push-actions { display: flex; gap: 6px; }
  .push-actions button { padding: 6px 9px; }
  .push-actions .danger { color: var(--push-orange); }
  .push-dot {
    background: var(--push-muted);
    border-radius: 50%;
    height: 8px;
    width: 8px;
  }
  .push-dot.sent { background: #24b477; }
  .push-dot.failed { background: var(--push-orange); }
  .push-empty {
    align-items: center;
    color: var(--push-muted);
    display: flex;
    flex-direction: column;
    gap: 5px;
    padding: 36px 22px;
    text-align: center;
  }
  .push-empty strong { color: inherit; font-size: 14px; }
  .push-empty span { font-size: 13px; }
  @media (max-width: 800px) {
    .push-panel { padding: 18px; }
    .push-metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    .push-grid { grid-template-columns: 1fr; }
  }
  @media (max-width: 540px) {
    .push-header { align-items: flex-start; flex-direction: column; }
    .push-device { align-items: flex-start; flex-wrap: wrap; }
    .push-device-copy { min-width: 160px; }
    .push-actions { margin-left: 50px; width: 100%; }
  }
`;
