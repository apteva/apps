import { DEFAULT_SOFTPHONE_AUDIO_OPTIONS, type SoftphoneAudioOptions } from "./softphone-audio";

export const AUDIO_SETTINGS_KEY = "apteva.telephony.softphone.audio.v4";
export const PREVIOUS_AUDIO_SETTINGS_KEY = "apteva.telephony.softphone.audio.v3";
export const LEGACY_AUDIO_SETTINGS_KEY = "apteva.telephony.softphone.audio.v2";
export const OLDEST_AUDIO_SETTINGS_KEY = "apteva.telephony.softphone.audio.v1";

export function resolveAudioOptions(
  currentRaw: string | null,
  previousRaw: string | null,
  legacyRaw: string | null,
  oldestRaw: string | null = null,
): SoftphoneAudioOptions {
  try {
    const current = JSON.parse(currentRaw || "null") as Partial<SoftphoneAudioOptions> | null;
    if (current) return { ...DEFAULT_SOFTPHONE_AUDIO_OPTIONS, ...current };
    const previous = JSON.parse(previousRaw || "null") as Partial<SoftphoneAudioOptions> | null;
    if (previous) return { ...DEFAULT_SOFTPHONE_AUDIO_OPTIONS, ...previous };
    const legacy = JSON.parse(legacyRaw || "null") as Partial<SoftphoneAudioOptions> | null;
    // v2 enabled automatic gain for every visitor. It cannot distinguish that
    // default from an explicit choice, so migrate once to the safer disabled
    // profile while preserving echo cancellation and noise suppression.
    if (legacy) return { ...DEFAULT_SOFTPHONE_AUDIO_OPTIONS, ...legacy, autoGainControl: false };
    const oldest = JSON.parse(oldestRaw || "null") as Partial<SoftphoneAudioOptions> | null;
    if (oldest) return { ...DEFAULT_SOFTPHONE_AUDIO_OPTIONS, ...oldest, autoGainControl: false };
  } catch {
    // Corrupt local state falls back to the safe recommended profile.
  }
  return { ...DEFAULT_SOFTPHONE_AUDIO_OPTIONS };
}

export function loadAudioOptions(): SoftphoneAudioOptions {
  return resolveAudioOptions(
    localStorage.getItem(AUDIO_SETTINGS_KEY),
    localStorage.getItem(PREVIOUS_AUDIO_SETTINGS_KEY),
    localStorage.getItem(LEGACY_AUDIO_SETTINGS_KEY),
    localStorage.getItem(OLDEST_AUDIO_SETTINGS_KEY),
  );
}

export function persistAudioOptions(value: SoftphoneAudioOptions): void {
  localStorage.setItem(AUDIO_SETTINGS_KEY, JSON.stringify(value));
  localStorage.removeItem(PREVIOUS_AUDIO_SETTINGS_KEY);
  localStorage.removeItem(LEGACY_AUDIO_SETTINGS_KEY);
  localStorage.removeItem(OLDEST_AUDIO_SETTINGS_KEY);
}
