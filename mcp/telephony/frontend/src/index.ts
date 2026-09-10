export { TelephonyClient, telephonyExtension, isTerminalCall, isIncomingBrowserCall } from "./client";
export type { TelephonyClientOptions, Call, CallSession, DialRequest, AnswerRequest, WatchCallsOptions } from "./client";
export { HeadlessSoftphone } from "./softphone";
export type { SoftphoneSnapshot, SoftphoneOptions } from "./softphone";
export type { AudioRuntime, AudioConnection, MicrophoneDevice, MicrophonePreview } from "./audio";
export { DEFAULT_SOFTPHONE_AUDIO_OPTIONS } from "../../ui/softphone-audio";
export type { SoftphoneAudioOptions, SoftphoneDiagnostics, SoftphoneCallbacks } from "../../ui/softphone-audio";
