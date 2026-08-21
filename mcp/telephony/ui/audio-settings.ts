import { DEFAULT_SOFTPHONE_AUDIO_OPTIONS, type SoftphoneAudioOptions } from "./softphone-audio";

export const AUDIO_SETTINGS_KEY = "apteva.telephony.softphone.audio.v2";
export const LEGACY_AUDIO_SETTINGS_KEY = "apteva.telephony.softphone.audio.v1";

export function resolveAudioOptions(currentRaw: string | null, legacyRaw: string | null): SoftphoneAudioOptions {
  try {
    const current = JSON.parse(currentRaw || "null") as Partial<SoftphoneAudioOptions> | null;
    if (current) return { ...DEFAULT_SOFTPHONE_AUDIO_OPTIONS, ...current };
    const legacy = JSON.parse(legacyRaw || "null") as Partial<SoftphoneAudioOptions> | null;
    // v1 wrote automatic gain=false for every visitor, so it cannot tell an
    // explicit choice from the old default. Migrate once to the recommended
    // human-softphone profile while preserving the other two choices.
    if (legacy) return { ...DEFAULT_SOFTPHONE_AUDIO_OPTIONS, ...legacy, autoGainControl: true };
  } catch {
    // Corrupt local state falls back to the safe recommended profile.
  }
  return { ...DEFAULT_SOFTPHONE_AUDIO_OPTIONS };
}

export function loadAudioOptions(): SoftphoneAudioOptions {
  return resolveAudioOptions(
    localStorage.getItem(AUDIO_SETTINGS_KEY),
    localStorage.getItem(LEGACY_AUDIO_SETTINGS_KEY),
  );
}

export function persistAudioOptions(value: SoftphoneAudioOptions): void {
  localStorage.setItem(AUDIO_SETTINGS_KEY, JSON.stringify(value));
  localStorage.removeItem(LEGACY_AUDIO_SETTINGS_KEY);
}
