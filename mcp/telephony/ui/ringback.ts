// Locally synthesized ringback tones.
//
// Carriers do not send early media to a browser softphone, so an operator
// hears silence between dialing and answer. These patterns reproduce the
// national ringback cadences through the softphone's own AudioContext, which
// means the tone follows the operator's chosen output device and volume.
// Cadence values alternate on/off durations in seconds.

export interface RingbackPattern {
  readonly country: string;
  readonly frequencies: readonly number[];
  readonly cadence: readonly number[];
}

export const DEFAULT_RINGBACK_COUNTRY = "FR";

export const RINGBACK_PATTERNS: Readonly<Record<string, RingbackPattern>> = Object.freeze({
  FR: { country: "FR", frequencies: [440], cadence: [1.5, 3.5] },
  BE: { country: "BE", frequencies: [425], cadence: [1, 3] },
  CH: { country: "CH", frequencies: [425], cadence: [1, 4] },
  DE: { country: "DE", frequencies: [425], cadence: [1, 4] },
  AT: { country: "AT", frequencies: [425], cadence: [1, 5] },
  NL: { country: "NL", frequencies: [425], cadence: [1, 4] },
  ES: { country: "ES", frequencies: [425], cadence: [1.5, 3] },
  IT: { country: "IT", frequencies: [425], cadence: [1, 4] },
  PT: { country: "PT", frequencies: [425], cadence: [1, 5] },
  GB: { country: "GB", frequencies: [400, 450], cadence: [0.4, 0.2, 0.4, 2] },
  IE: { country: "IE", frequencies: [400, 450], cadence: [0.4, 0.2, 0.4, 2] },
  AU: { country: "AU", frequencies: [400, 425], cadence: [0.4, 0.2, 0.4, 2] },
  US: { country: "US", frequencies: [440, 480], cadence: [2, 4] },
  CA: { country: "CA", frequencies: [440, 480], cadence: [2, 4] },
  JP: { country: "JP", frequencies: [400], cadence: [1, 2] },
});

/** Resolves a country code (case-insensitive) to a pattern, defaulting to France. */
export function ringbackPattern(country?: string): RingbackPattern {
  const code = (country ?? DEFAULT_RINGBACK_COUNTRY).trim().toUpperCase();
  return RINGBACK_PATTERNS[code] ?? RINGBACK_PATTERNS[DEFAULT_RINGBACK_COUNTRY];
}

/** On-segments (seconds from start) produced by repeating the cadence up to `seconds`. */
export function ringbackTimeline(pattern: RingbackPattern, seconds: number): Array<{ start: number; end: number }> {
  const segments: Array<{ start: number; end: number }> = [];
  const cycle = pattern.cadence.reduce((sum, value) => sum + value, 0);
  if (!(cycle > 0) || !(seconds > 0)) return segments;
  let t = 0;
  while (t < seconds) {
    for (let i = 0; i < pattern.cadence.length; i += 2) {
      const on = pattern.cadence[i] ?? 0;
      const off = pattern.cadence[i + 1] ?? 0;
      if (t >= seconds) break;
      if (on > 0) segments.push({ start: t, end: Math.min(t + on, seconds) });
      t += on + off;
    }
  }
  return segments;
}

/**
 * Plays the pattern through `destination` until the returned stop function
 * runs. Scheduling happens in ten-second windows against the context clock so
 * the cadence stays accurate even when the page is throttled.
 */
export function playRingback(ctx: AudioContext, destination: AudioNode, pattern: RingbackPattern, level = 0.12): () => void {
  const gain = ctx.createGain();
  gain.gain.value = 0;
  gain.connect(destination);
  const perTone = level / Math.max(1, pattern.frequencies.length);
  const oscillators = pattern.frequencies.map((frequency) => {
    const oscillator = ctx.createOscillator();
    oscillator.type = "sine";
    oscillator.frequency.value = frequency;
    oscillator.connect(gain);
    oscillator.start();
    return oscillator;
  });
  const origin = ctx.currentTime + 0.05;
  let scheduled = 0; // seconds of timeline already scheduled
  const ramp = 0.01;
  const schedule = () => {
    const horizon = ctx.currentTime - origin + 10;
    if (horizon <= scheduled) return;
    for (const segment of ringbackTimeline(pattern, horizon)) {
      if (segment.end <= scheduled) continue;
      const start = origin + Math.max(segment.start, scheduled);
      const end = origin + segment.end;
      gain.gain.setValueAtTime(0, start);
      gain.gain.linearRampToValueAtTime(perTone, start + ramp);
      gain.gain.setValueAtTime(perTone, Math.max(start + ramp, end - ramp));
      gain.gain.linearRampToValueAtTime(0, end);
    }
    scheduled = horizon;
  };
  schedule();
  const timer = setInterval(schedule, 4000);
  let stopped = false;
  return () => {
    if (stopped) return;
    stopped = true;
    clearInterval(timer);
    try { gain.gain.cancelScheduledValues(0); } catch { /* context may be closing */ }
    gain.gain.value = 0;
    for (const oscillator of oscillators) {
      try { oscillator.stop(); } catch { /* already stopped */ }
      oscillator.disconnect();
    }
    gain.disconnect();
  };
}
