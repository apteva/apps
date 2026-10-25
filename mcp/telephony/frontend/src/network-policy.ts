export interface NetworkPolicy {
  upload_bitrate_control: "standard" | "guarded";
  media_transport: "inherit" | "auto" | "webrtc" | "websocket";
  initial_bitrate_bps: number;
  min_bitrate_bps: number;
  max_bitrate_bps: number;
  playback_target_ms: number;
  playback_max_ms: number;
  quality_warnings: boolean;
  degraded_seconds: number;
  warning_cooldown_seconds: number;
  background_check: boolean;
  allow_user_overrides: boolean;
}
export const DEFAULT_NETWORK_POLICY: Readonly<NetworkPolicy> = Object.freeze({
  upload_bitrate_control: "standard",
  media_transport: "inherit",
  initial_bitrate_bps: 32000,
  min_bitrate_bps: 16000,
  max_bitrate_bps: 32000,
  playback_target_ms: 60,
  playback_max_ms: 160,
  quality_warnings: false,
  degraded_seconds: 6,
  warning_cooldown_seconds: 60,
  background_check: false,
  allow_user_overrides: false,
});
export interface NetworkPolicyView {
  configured?:boolean;
  policy: NetworkPolicy;
  effective: NetworkPolicy;
  override: boolean;
  allow_user_overrides: boolean;
  scope?: "project" | "adviser";
  advisers?: import("./turn-policy").TURNIdentity[];
}
export function validateNetworkPolicy(input: unknown): NetworkPolicy {
  if (!input || typeof input !== "object" || Array.isArray(input))
    throw Error("Invalid network policy");
  const p = { ...DEFAULT_NETWORK_POLICY, ...input } as NetworkPolicy;
  for (const key of [
    "initial_bitrate_bps",
    "min_bitrate_bps",
    "max_bitrate_bps",
    "playback_target_ms",
    "playback_max_ms",
    "degraded_seconds",
    "warning_cooldown_seconds",
  ] as const)
    if (!Number.isInteger(p[key])) throw Error("Invalid network policy bounds");
  for (const key of [
    "quality_warnings",
    "background_check",
    "allow_user_overrides",
  ] as const)
    if (typeof p[key] !== "boolean")
      throw Error("Invalid network policy switch");
  if (
    !["standard", "guarded"].includes(p.upload_bitrate_control) ||
    !["inherit", "auto", "webrtc", "websocket"].includes(p.media_transport) ||
    p.min_bitrate_bps < 16000 ||
    p.max_bitrate_bps > 32000 ||
    p.initial_bitrate_bps < p.min_bitrate_bps ||
    p.initial_bitrate_bps > p.max_bitrate_bps ||
    p.playback_target_ms < 40 ||
    p.playback_target_ms > 120 ||
    p.playback_max_ms < p.playback_target_ms ||
    p.playback_max_ms > 200 ||
    p.degraded_seconds < 4 ||
    p.degraded_seconds > 30 ||
    p.warning_cooldown_seconds < 30 ||
    p.warning_cooldown_seconds > 300
  )
    throw Error("Invalid network policy bounds");
  return p;
}
