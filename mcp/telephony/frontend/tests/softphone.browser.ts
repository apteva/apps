import { test, expect } from "@playwright/test";
test("installed headless client talks through real Telephony with host-owned UI", async ({ page }) => {
  const gateway = process.env.TELEPHONY_TEST_GATEWAY;
  if (!gateway) throw new Error("Run via TestTier2HeadlessBrowser; a compiled sidecar gateway is required");
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(process.env.TELEPHONY_TEST_SURFACE === "application-user" ? "/?application-user" : "/");
  await page.waitForFunction(() => typeof (window as any).loadPhone === "function");
  await page.evaluate(({ url, token }) => (window as any).loadPhone(url, token), { url: gateway, token: process.env.TELEPHONY_TEST_USER_TOKEN });
  expect(await page.evaluate(() => Object.keys((window as any).loaded.components))).toEqual([]);
  expect(await page.evaluate(() => (window as any).calls[0].status)).toBe("pending");
  // Contract used by the host's selector and Tester UI, checked on the
  // integrity-verified app-served client rather than a mocked client interface.
  const contract = await page.evaluate(() => {
    const w = window as any;
    return {
      client: ["hangup", "createSoftphone", "watchCalls", "incomingCalls", "listMicrophones", "createMicrophonePreview"].every(key => typeof w.client[key] === "function"),
      phone: ["getSnapshot", "subscribe", "attach", "answer", "hangup", "reconnect", "configureAudio", "setMuted", "sendDTMF", "setOutputVolume", "dispose"].every(key => typeof w.phone[key] === "function"),
    };
  });
  expect(contract).toEqual({ client: true, phone: true });
  let previewSockets = 0;
  const countSocket = () => { previewSockets++; };
  page.on("websocket", countSocket);
  await page.evaluate(async () => {
    const w = window as any;
    w.previewStreams = []; w.previewPeak = 0;
    w.originalGetUserMedia = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
    navigator.mediaDevices.getUserMedia = async constraints => {
      const stream = await w.originalGetUserMedia(constraints);
      w.previewStreams.push(stream); return stream;
    };
    const devices = await w.client.listMicrophones();
    if (!devices.length) throw new Error("No microphone enumerated");
    w.selectedMic = devices[0].deviceId;
    w.preview = w.client.createMicrophonePreview((level: number) => { w.previewPeak = Math.max(w.previewPeak, level); w.previewLevel = level; });
    await w.preview.start({ inputDeviceId: w.selectedMic });
  });
  await expect.poll(() => page.evaluate(() => (window as any).previewPeak)).toBeGreaterThan(0.01);
  const previewResult = await page.evaluate(async () => {
    const w = window as any;
    const devices = await w.client.listMicrophones();
    const selected = w.previewStreams[0].getAudioTracks()[0].getSettings().deviceId;
    await w.preview.stop(); await w.preview.stop();
    navigator.mediaDevices.getUserMedia = w.originalGetUserMedia;
    return { selected: selected === w.selectedMic, labeled: devices.some((d: any) => d.deviceId === selected && d.label), stopped: w.previewStreams.every((s: MediaStream) => s.getTracks().every(t => t.readyState === "ended")), level: w.previewLevel, callId: w.phone.getSnapshot().callId ?? null };
  });
  page.off("websocket", countSocket);
  expect(previewSockets).toBe(0);
  expect(previewResult).toEqual({ selected: true, labeled: true, stopped: true, level: 0, callId: null });
  await page.click("#answer");
  await expect.poll(() => page.evaluate(() => (window as any).phone.getSnapshot().audioState)).toBe("live");
  await expect.poll(() => page.evaluate(() => (window as any).maxSpeaker), { timeout: 15000 }).toBeGreaterThan(0.01);
  await expect.poll(() => page.evaluate(() => (window as any).maxMic), { timeout: 15000 }).toBeGreaterThan(0.01);
  await expect.poll(async () => (await page.request.get(gateway + "/fixture/audio-ready")).json()).toEqual({ ready: true });
  await page.evaluate(() => { const w = window as any; w.phone.setMuted(true); w.phone.sendDTMF("12#"); });
  await expect.poll(() => page.evaluate(() => (window as any).notices)).toContain("Keypad tone sent");
  await expect.poll(() => page.evaluate(() => (window as any).diagnostics?.micInputGainDb)).toBe(0);
  await page.evaluate(() => (window as any).phone.reconnect({ inputGainDB: -6, playbackTargetMs: 80 }));
  await expect.poll(() => page.evaluate(() => (window as any).phone.getSnapshot().audioState)).toBe("live");
  expect(await page.evaluate(() => (window as any).phone.getSnapshot().muted)).toBe(true);
  await expect.poll(() => page.evaluate(() => (window as any).diagnostics?.micInputGainDb)).toBe(-6);
  await expect.poll(() => page.evaluate(() => (window as any).diagnostics?.targetMs)).toBeGreaterThanOrEqual(80);
  await page.evaluate(() => (window as any).phone.setMuted(false));
  await page.evaluate(() => (window as any).phone.hangup());
  expect(await page.evaluate(() => (window as any).phone.getSnapshot().callId)).toBeUndefined();
  await page.evaluate(() => { const w = window as any; w.phone.dispose(); w.loaded.dispose(); });
  if (process.env.TELEPHONY_TEST_SURFACE === "application-user") {
    expect((await page.request.post(gateway + "/fixture/logout")).status()).toBe(204);
    const denied = await page.evaluate(async () => {
      try { await (window as any).client.listCalls(); return false; }
      catch (error: any) { return error.status === 401; }
    });
    expect(denied).toBe(true);
  }
  expect(errors).toEqual([]);
  expect(await page.evaluate(() => (window as any).answerError)).toBeUndefined();
});
