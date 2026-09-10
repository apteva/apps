import { afterEach, expect, spyOn, test } from "bun:test";
import { listMicrophones, createMicrophonePreview } from "../src/audio";
import { MicrophoneTestSession } from "../../ui/softphone-audio";

const restore: Array<() => void> = [];
afterEach(() => { while (restore.length) restore.pop()!(); });
function mediaDevices(value: unknown) {
 const prior = Object.getOwnPropertyDescriptor(globalThis, "navigator");
 Object.defineProperty(globalThis, "navigator", { configurable: true, value: { mediaDevices: value } });
 restore.push(() => { if (prior) Object.defineProperty(globalThis, "navigator", prior); else Reflect.deleteProperty(globalThis, "navigator"); });
}

test("microphone enumeration filters inputs without requesting permission", async () => {
 let opened = false;
 mediaDevices({ enumerateDevices: async () => [
  { kind: "audioinput", deviceId: "usb", label: "USB microphone" },
  { kind: "audiooutput", deviceId: "speaker", label: "Speaker" },
  { kind: "videoinput", deviceId: "camera", label: "Camera" },
  { kind: "audioinput", deviceId: "unlabeled", label: "" },
 ], getUserMedia: () => { opened = true; throw new Error("unexpected permission"); } });
 expect(await listMicrophones()).toEqual([{ deviceId: "usb", label: "USB microphone" }, { deviceId: "unlabeled", label: "Microphone 2" }]);
 expect(opened).toBe(false);
});

test("preview closes a microphone arriving after stop during a permission prompt", async () => {
 let grant!: (value: MediaStream) => void;
 let stopped = 0;
 mediaDevices({ getUserMedia: () => new Promise<MediaStream>(resolve => { grant = resolve; }) });
 const revoke = spyOn(URL, "revokeObjectURL"); restore.push(() => revoke.mockRestore());
 const preview = createMicrophonePreview();
 const started = preview.start({ inputDeviceId: "usb" }).catch(error => error);
 await preview.stop();
 const track = { stop() { stopped++; } };
 grant({ getAudioTracks: () => [track], getTracks: () => [track] } as unknown as MediaStream);
 expect((await started).message).toContain("cancelled");
 await preview.stop();
 expect(stopped).toBe(1);
 expect(revoke).toHaveBeenCalledTimes(1);
 await expect(preview.start()).rejects.toThrow("already used");
});

test("permission failure revokes the worklet URL and leaves preview stopped", async () => {
 mediaDevices({ getUserMedia: async () => { throw new Error("permission denied"); } });
 const revoke = spyOn(URL, "revokeObjectURL"); restore.push(() => revoke.mockRestore());
 const preview = createMicrophonePreview();
 await expect(preview.start()).rejects.toThrow("permission denied");
 await preview.stop();
 expect(revoke).toHaveBeenCalledTimes(1);
});

test("preview releases its URL even when audio cleanup rejects", async () => {
 const start = spyOn(MicrophoneTestSession.prototype, "start").mockResolvedValue({} as any);
 const cancel = spyOn(MicrophoneTestSession.prototype, "cancel").mockRejectedValue(new Error("context close failed"));
 const revoke = spyOn(URL, "revokeObjectURL");
 restore.push(() => { start.mockRestore(); cancel.mockRestore(); revoke.mockRestore(); });
 const preview = createMicrophonePreview();
 await preview.start();
 await expect(preview.stop()).rejects.toThrow("context close failed");
 expect(revoke).toHaveBeenCalledTimes(1);
});
