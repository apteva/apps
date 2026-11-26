/** Set once this page has opened a microphone, so permission is granted. */
let microphoneOpened = false;

export interface CoachTalkStart {
  openMic(): Promise<MediaStream>;
  /** Sends coach.start and resolves on the server's coach.started. */
  requestStart(): Promise<void>;
  /** False once this press was released or replaced. */
  current(): boolean;
  /** Takes the stream as soon as it opens, so a release can stop it at once. */
  adopt(stream: MediaStream): void;
}

/**
 * Opens the microphone and asks the server to start a press.
 *
 * Once this page has opened a microphone, both run at the same time, so the
 * server's reply arrives while the browser opens the device rather than after
 * it. The first press asks in order, so a permission prompt never shows the
 * adviser a supervisor who is not speaking yet.
 *
 * Resolves to undefined when the press was released first. A stream that
 * opens after release is stopped here.
 */
export async function beginCoachTalk(press: CoachTalkStart): Promise<MediaStream | undefined> {
  const parallel = microphoneOpened;
  const mic = press.openMic().then(stream => {
    microphoneOpened = true;
    if (!press.current()) { stream.getTracks().forEach(track => track.stop()); return undefined; }
    press.adopt(stream);
    return stream;
  });
  if (!parallel) {
    const stream = await mic;
    if (!stream) return undefined;
    await press.requestStart();
    return press.current() ? stream : undefined;
  }
  const start = press.requestStart();
  // A refusal is raised below, after the microphone settles; a release
  // rejects it on purpose.
  start.catch(() => {});
  const stream = await mic;
  if (!stream) return undefined;
  await start;
  return press.current() ? stream : undefined;
}

/** For tests: forget that a microphone was opened. */
export function resetCoachTalkForTests(): void { microphoneOpened = false; }
