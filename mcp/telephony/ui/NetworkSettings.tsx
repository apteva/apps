import { useEffect, useRef, useState } from "react";
import {
  DEFAULT_NETWORK_POLICY,
  validateNetworkPolicy,
  type NetworkPolicy,
  type NetworkPolicyView,
} from "../frontend/src/network-policy";
import type { TURNIdentity } from "../frontend/src/turn-policy";
import type {
  HeadlessSoftphone,
  SoftphoneSnapshot,
} from "../frontend/src/softphone";

type Request = (args: {
  identity?: TURNIdentity;
  policy?: Partial<NetworkPolicy>;
  inherit?: boolean;
}) => Promise<NetworkPolicyView>;
export default function NetworkSettings({
  request,
  admin = false,
}: {
  request: Request;
  admin?: boolean;
}) {
  const [open, setOpen] = useState(false),
    [identity, setIdentity] = useState("");
  const [view, setView] = useState<NetworkPolicyView>(),
    [policy, setPolicy] = useState<NetworkPolicy>(),
    [users, setUsers] = useState<TURNIdentity[]>([]);
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const generation = useRef(0);
  useEffect(() => {
    if (!open) return;
    const gen = ++generation.current;
    setBusy(true);
    setError("");
    request(identity ? { identity: JSON.parse(identity) } : {})
      .then((v) => {
        if (gen !== generation.current) return;
        setView(v);
        setPolicy(validateNetworkPolicy(v.effective));
        if (v.advisers) setUsers(v.advisers);
      })
      .catch((e) => {
        if (gen === generation.current) setError(e.message);
      })
      .finally(() => {
        if (gen === generation.current) setBusy(false);
      });
    return () => {
      generation.current++;
    };
  }, [open, identity, request]);
  const save = async (inherit = false) => {
    if (!policy) return;
    const gen = ++generation.current;
    setBusy(true);
    setError("");
    try {
      const p = validateNetworkPolicy(policy);
      if (identity || view?.scope === "adviser") p.allow_user_overrides = false;
      const v = await request({
        ...(identity ? { identity: JSON.parse(identity) } : {}),
        ...(inherit ? { inherit: true } : { policy: p }),
      });
      if (gen === generation.current) {
        setView(v);
        setPolicy(v.effective);
      }
    } catch (e) {
      if (gen === generation.current) setError((e as Error).message);
    } finally {
      if (gen === generation.current) setBusy(false);
    }
  };
  const editable =
    admin || view?.scope === "project" || view?.allow_user_overrides;
  return (
    <details
      open={open}
      onToggle={(e) => setOpen(e.currentTarget.open)}
      className="rounded border border-border p-3 text-sm"
    >
      <summary className="cursor-pointer">Browser network settings</summary>
      <div className="mt-3 space-y-3">
        <p className="text-xs text-text-muted">
          Changes apply to new audio connections. Network checks run while idle
          and stop when a call arrives or starts. TURN endpoints and TLS ports
          are configured in the TURN integration.
        </p>
        {admin && (
          <label className="block">
            Applies to
            <select
              value={identity}
              disabled={busy}
              onChange={(e) => setIdentity(e.target.value)}
              className="mt-1 w-full rounded border border-border bg-bg p-2"
            >
              <option value="">Project defaults</option>
              {users.map((u) => (
                <option key={JSON.stringify(u)} value={JSON.stringify(u)}>
                  {u.subject_id} · {u.issuer_app} #{u.issuer_install_id}
                </option>
              ))}
            </select>
          </label>
        )}
        {policy && (
          <fieldset disabled={busy || !editable} className="space-y-3">
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() =>
                  setPolicy({
                    ...DEFAULT_NETWORK_POLICY,
                    allow_user_overrides: policy.allow_user_overrides,
                  })
                }
              >
                Balanced preset
              </button>
              <button
                type="button"
                onClick={() =>
                  setPolicy({
                    ...policy,
                    media_transport: "auto",
                    initial_bitrate_bps: 20000,
                    min_bitrate_bps: 16000,
                    max_bitrate_bps: 20000,
                    playback_target_ms: 80,
                    playback_max_ms: 160,
                    quality_warnings: true,
                  })
                }
              >
                Unstable connection preset
              </button>
            </div>
            <label className="block">
              Media preference
              <select
                value={policy.media_transport}
                onChange={(e) =>
                  setPolicy({
                    ...policy,
                    media_transport: e.target
                      .value as NetworkPolicy["media_transport"],
                  })
                }
                className="mt-1 w-full rounded border border-border bg-bg p-2"
              >
                <option value="inherit">Keep softphone choice</option>
                <option value="auto">
                  Prefer WebRTC, allow WebSocket setup fallback
                </option>
                <option value="webrtc">WebRTC</option>
                <option value="websocket">WebSocket</option>
              </select>
            </label>
            <label className="block">
              Microphone bitrate adaptation
              <select value={policy.upload_bitrate_control}
                onChange={(e)=>setPolicy({...policy,upload_bitrate_control:e.target.value as NetworkPolicy["upload_bitrate_control"]})}
                className="mt-1 w-full rounded border border-border bg-bg p-2">
                <option value="standard">Standard (existing loss / bandwidth control)</option>
                <option value="guarded">Guarded (experimental: fresh feedback and sustained congestion)</option>
              </select>
              <span className="text-xs text-text-muted">Guarded control reacts to delay growth only with upload impairment evidence. It never restarts audio or changes an active route.</span>
            </label>
            {(
              [
                ["min_bitrate_bps", "Minimum Opus bitrate", 16000, 32000, 1000],
                [
                  "initial_bitrate_bps",
                  "Initial Opus bitrate",
                  16000,
                  32000,
                  1000,
                ],
                ["max_bitrate_bps", "Maximum Opus bitrate", 16000, 32000, 1000],
                [
                  "playback_target_ms",
                  "Initial playback reserve (ms)",
                  40,
                  120,
                  10,
                ],
                [
                  "playback_max_ms",
                  "Maximum requested playback reserve (ms)",
                  40,
                  200,
                  10,
                ],
                [
                  "degraded_seconds",
                  "Sustained degradation before notice (seconds)",
                  4,
                  30,
                  1,
                ],
                [
                  "warning_cooldown_seconds",
                  "Notice cooldown (seconds)",
                  30,
                  300,
                  10,
                ],
              ] as const
            ).map(([key, label, min, max, step]) => (
              <label className="block" key={key}>
                {label}
                <input
                  type="number"
                  min={min}
                  max={max}
                  step={step}
                  value={policy[key]}
                  onChange={(e) =>
                    setPolicy({ ...policy, [key]: Number(e.target.value) })
                  }
                  className="mt-1 w-full rounded border border-border bg-bg p-2"
                />
              </label>
            ))}
            <label className="flex gap-2">
              <input
                type="checkbox"
                checked={policy.background_check}
                onChange={(e) =>
                  setPolicy({ ...policy, background_check: e.target.checked })
                }
              />
              Check the network while idle
            </label>
            <label className="flex gap-2">
              <input
                type="checkbox"
                checked={policy.quality_warnings}
                onChange={(e) =>
                  setPolicy({ ...policy, quality_warnings: e.target.checked })
                }
              />
              Report sustained network degradation
            </label>
            {view?.scope === "project" && !identity && (
              <label className="flex gap-2">
                <input
                  type="checkbox"
                  checked={policy.allow_user_overrides}
                  onChange={(e) =>
                    setPolicy({
                      ...policy,
                      allow_user_overrides: e.target.checked,
                    })
                  }
                />
                Allow advisers to edit their own profile
              </label>
            )}
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => void save()}
                className="rounded border border-border px-3 py-2"
              >
                Save network settings
              </button>
              {(identity || view?.scope === "adviser") && (
                <button type="button" onClick={() => void save(true)}>
                  Use project defaults
                </button>
              )}
            </div>
          </fieldset>
        )}
        {view && !editable && <p>Your administrator controls this profile.</p>}
        {error && (
          <p role="alert" className="text-error">
            {error}
          </p>
        )}
      </div>
    </details>
  );
}
export function NetworkCheckControl({
  phone,
  state,
}: {
  phone?: HeadlessSoftphone;
  state: SoftphoneSnapshot;
}) {
  const [error, setError] = useState("");
  const result = state.networkCheck;
  const occupied =
    state.busy || (Boolean(state.callId) && state.phase !== "ended");
  const check = async (compare = false) => {
    setError("");
    try {
      await phone?.checkNetwork({
        paths: compare ? ["direct", "relay"] : ["configured"],
      });
    } catch (e) {
      setError((e as Error).message);
    }
  };
  return (
    <details className="rounded border border-border p-2 text-xs">
      <summary className="cursor-pointer">
        Network:{" "}
        {state.networkChecking
          ? "checking in background"
          : (result?.state ?? "not checked")}
      </summary>
      <p className="mt-2 text-text-muted">
        No microphone or phone call. A quick check normally takes 10–15 seconds;
        comparing paths takes longer. Incoming calls remain available. A check
        result never blocks calling.
      </p>
      <div className="mt-2 flex gap-2">
        <button
          type="button"
          disabled={!phone || occupied || state.networkChecking}
          onClick={() => void check()}
          className="rounded border border-border px-2 py-1 disabled:opacity-50"
        >
          Check network
        </button>
        <button
          type="button"
          disabled={!phone || occupied || state.networkChecking}
          onClick={() => void check(true)}
          className="rounded border border-border px-2 py-1 disabled:opacity-50"
        >
          Compare direct / relay
        </button>
      </div>
      {result?.paths.map((p) => (
        <p key={p.path} className="mt-2">
          {p.path}: {p.state}
          {p.reason ? ` · ${p.reason.replaceAll("_", " ")}` : ""}
          {p.received
            ? ` · loss ↑${p.uploadLossPct.toFixed(1)}% ↓${p.downloadLossPct.toFixed(1)}% · RTT ${p.rttMs?.toFixed(0) ?? "—"} ms · jitter ${p.jitterMs.toFixed(0)} ms`
            : ""}
          {p.candidateType ? ` · ${p.relayProtocol ?? p.candidateType}` : ""}
        </p>
      ))}
      {result?.recommendedPath && result.paths.length > 1 && (
        <p className="mt-2">
          Best measured test path: {result.recommendedPath}. This does not
          change an active call.
        </p>
      )}
      {error && (
        <p role="status" className="mt-2 text-error">
          {error}
        </p>
      )}
    </details>
  );
}
