export interface SpeechResult {
  isFinal: boolean;
  [index: number]: { transcript: string };
}

export interface SpeechEvent {
  resultIndex: number;
  results: ArrayLike<SpeechResult>;
}

export interface BrowserSpeechRecognition {
  lang: string;
  continuous: boolean;
  interimResults: boolean;
  onresult: ((event: SpeechEvent) => void) | null;
  onerror: ((event: { error?: string }) => void) | null;
  onend: (() => void) | null;
  start(): void;
  stop(): void;
  abort(): void;
}

type RecognitionConstructor = new () => BrowserSpeechRecognition;

export function speechRecognitionConstructor(): RecognitionConstructor | undefined {
  const browser = window as Window & { SpeechRecognition?: RecognitionConstructor; webkitSpeechRecognition?: RecognitionConstructor };
  return browser.SpeechRecognition ?? browser.webkitSpeechRecognition;
}

// Browser events may repeat earlier final results. Only append each final
// result index once; never submit a speech result to the agent automatically.
export function finalSpeechText(event: SpeechEvent, committed: Set<number>): string {
  const parts: string[] = [];
  for (let index = event.resultIndex; index < event.results.length; index++) {
    const result = event.results[index];
    if (!result?.isFinal || committed.has(index)) continue;
    committed.add(index);
    const text = result[0]?.transcript?.trim();
    if (text) parts.push(text);
  }
  return parts.join(" ");
}
