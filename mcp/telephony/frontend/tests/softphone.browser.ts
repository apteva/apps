import { test, expect } from "@playwright/test";
test("installed headless client talks through real Telephony with host-owned UI", async ({ page, context }) => {
  const gateway = process.env.TELEPHONY_TEST_GATEWAY;
  if (!gateway) throw new Error("Run via TestTier2HeadlessBrowser; a compiled sidecar gateway is required");
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  let renewalFailures=0;
  // Accelerate only the fixture's advertised client lease; the server still
  // enforces its real lease. Inject one genuine HTTP 503 response.
  const shorterLease=async(route:any)=>{
    const response=await route.fetch();
    if(response.ok()) {const body=await response.json();await route.fulfill({response,json:body.lease_seconds?{...body,lease_seconds:10}:body});}
    else await route.fulfill({response});
  };
  await page.route('**/softphone/answer/**',shorterLease);
  await page.route('**/softphone/attach/**',shorterLease);
  await page.route('**/softphone/renew/**',async route=>{
    if(renewalFailures++===0)await route.fulfill({status:503,contentType:'application/json',body:'{"code":"fixture_transient_failure"}'});
    else await route.continue();
  });
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
  await page.evaluate(() => {
    const w = window as any;
    w.watchSamples = []; w.watchCalls = [];
    w.watcher = w.client.watchCalls((calls: any[]) => { w.watchCalls = calls; }, {
      intervalMs: 60000, onTiming: (sample: any) => w.watchSamples.push(sample),
    });
  });
  // Wait for the initial stream reconciliation, then require a later change.
  await expect.poll(() => page.evaluate(() => (window as any).watchSamples.some((s: any) => s.trigger === "push"))).toBe(true);
  await page.click("#answer");
  await expect.poll(() => page.evaluate(() => (window as any).watchCalls[0]?.status), { timeout: 1500 }).not.toBe("pending");
  await expect.poll(() => page.evaluate(() => (window as any).phone.getSnapshot().audioState)).toBe("live");
  await expect.poll(() => page.evaluate(() => (window as any).maxSpeaker), { timeout: 15000 }).toBeGreaterThan(0.01);
  await expect.poll(() => page.evaluate(() => (window as any).maxMic), { timeout: 15000 }).toBeGreaterThan(0.01);
  await expect.poll(async () => (await page.request.get(gateway + "/fixture/audio-ready")).json()).toEqual({ ready: true });
  // Legacy trusted-operator Answer has no lease; explicit reconnect upgrades
  // it to a managed, authorized attach session without changing the carrier leg.
  if(await page.evaluate(()=>(window as any).phone.session.lease_seconds===undefined)) {
    await page.evaluate(()=>(window as any).phone.reconnect());
    await expect.poll(()=>page.evaluate(()=>(window as any).phone.getSnapshot().audioState)).toBe('live');
  }
  const mediaBeforeRenewal=await (await page.request.get(gateway+'/fixture/media-connections')).json();
  await expect.poll(()=>renewalFailures,{timeout:10000}).toBeGreaterThanOrEqual(2);
  const renewalSnapshot=await page.evaluate(()=>(window as any).phone.getSnapshot());
  expect(renewalSnapshot.audioState,JSON.stringify(renewalSnapshot)).toBe('live');
  expect(await (await page.request.get(gateway+'/fixture/media-connections')).json()).toEqual(mediaBeforeRenewal); // No audio teardown on the renewal failure.
  if (process.env.TELEPHONY_TEST_SURFACE === "headless") {
    const observer = await context.newPage();
    const observerErrors: string[] = [];
    observer.on("pageerror", error => observerErrors.push(error.message));
    await observer.goto("/listener?strict");
    await observer.waitForFunction(() => typeof (window as any).loadListener === "function");
    await observer.evaluate(async () => {
      const w = window as any;
      navigator.mediaDevices.getUserMedia = async () => { throw new Error("Passive listener requested microphone access"); };
      await w.loadListener();
    });
    await observer.click("#listen");
    await observer.evaluate(() => (window as any).listenPromise);
    await expect.poll(() => observer.evaluate(() => (window as any).callListener.getSnapshot().state)).toBe("listening");
    await expect.poll(() => observer.evaluate(() => (window as any).listenerDiagnostics?.played_ms?.every((value: number) => value > 100))).toBe(true);
    expect(await observer.evaluate(() => (window as any).listenerDiagnostics.max_queue_ms)).toBeLessThanOrEqual(160);
    await observer.evaluate(() => (window as any).callListener.stop());
    expect(await observer.evaluate(() => (window as any).callListener.getSnapshot().state)).toBe("idle");
    expect(observerErrors).toEqual([]);
    // The general SDK module loader requires script-src blob:, as documented
    // by the SDK. The strict page above tests bundled host integration separately.
    await observer.goto('/listener');
    await observer.waitForFunction(()=>typeof(window as any).loadListener==='function');
    await observer.evaluate(async()=>{navigator.mediaDevices.getUserMedia=async()=>{throw new Error('Join requested a microphone');};await (window as any).loadListener();});
    expect(await observer.evaluate(()=>typeof(window as any).client.createCallListener)).toBe('function');
    // Real packaged headless client, capture, worker and adviser overlay.
    await observer.evaluate(()=>{
      const w=window as any;
      w.micRequests=0;w.coachTracks=[];
      navigator.mediaDevices.getUserMedia=async()=>{
        w.micRequests++;
        const ctx=new AudioContext(),tone=ctx.createOscillator(),dest=ctx.createMediaStreamDestination();
        tone.frequency.value=1700;tone.connect(dest);tone.start();
        w.coachContext=ctx;w.coachTone=tone;w.coachTracks.push(...dest.stream.getTracks());
        return dest.stream;
      };
    });
    await observer.click('#coach');await observer.evaluate(()=>(window as any).coachPromise);
    expect(await observer.evaluate(()=>(window as any).micRequests)).toBe(0);
    // Release during a delayed microphone permission prompt must stop late tracks.
    await observer.evaluate(()=>{
      const w=window as any;w.realMic=navigator.mediaDevices.getUserMedia;
      navigator.mediaDevices.getUserMedia=()=>new Promise(resolve=>{w.resolveMic=resolve;});
      w.cancelledTalk=w.callListener.startTalking();
    });
    await observer.evaluate(async()=>{
      const w=window as any;w.callListener.stopTalking();const stream=await w.realMic();w.resolveMic(stream);await w.cancelledTalk;
      w.lateTrackStopped=stream.getTracks().every((t:MediaStreamTrack)=>t.readyState==='ended');
      await w.coachContext.close();navigator.mediaDevices.getUserMedia=w.realMic;
    });
    expect(await observer.evaluate(()=>(window as any).lateTrackStopped)).toBe(true);
    await observer.locator('#talk').dispatchEvent('pointerdown');await observer.evaluate(()=>(window as any).talkPromise);
    await expect.poll(()=>observer.evaluate(()=>(window as any).callListener.getSnapshot().talking)).toBe(true);
    await expect.poll(()=>page.evaluate(()=>(window as any).diagnostics?.coachingPlayedMs),{timeout:15000}).toBeGreaterThan(100);
    expect(await page.evaluate(()=>(window as any).diagnostics.coachingMaxQueueMs)).toBeLessThanOrEqual(120);
    await observer.locator('#talk').dispatchEvent('pointerup');
    expect(await observer.evaluate(()=>(window as any).callListener.getSnapshot().talking)).toBe(false);
    expect(await observer.evaluate(()=>(window as any).coachTracks.every((t:MediaStreamTrack)=>t.readyState==='ended'))).toBe(true);
    // Blur/background cannot leave the microphone active.
    await observer.locator('#talk').dispatchEvent('pointerdown');await observer.evaluate(()=>(window as any).talkPromise);
    await observer.evaluate(()=>window.dispatchEvent(new Event('blur')));
    expect(await observer.evaluate(()=>(window as any).callListener.getSnapshot().talking)).toBe(false);
    await observer.evaluate(async()=>{const w=window as any;await w.callListener.stop();await w.coachContext.close();});
    expect(observerErrors).toEqual([]);
    await observer.close();
    expect(await page.evaluate(() => (window as any).phone.getSnapshot().audioState)).toBe("live");
  }
  await page.evaluate(() => { const w = window as any; w.phone.setMuted(true); w.phone.sendDTMF("12#"); });
  await expect.poll(() => page.evaluate(() => (window as any).notices)).toContain("Keypad tone sent");
  await expect.poll(() => page.evaluate(() => (window as any).diagnostics?.micInputGainDb)).toBe(0);
  const originalMedia=await page.evaluate(()=>(window as any).phone.session.media_url);
  // Force an actual transport close: the packaged worker must request a fresh
  // authorized token, reconnect the same call and preserve the mute gate.
  expect(await (await page.request.post(gateway+'/fixture/drop-browser')).json()).toEqual({ok:true});
  await expect.poll(()=>page.evaluate(()=>(window as any).phone.session.media_url),{timeout:15000}).not.toBe(originalMedia);
  await expect.poll(()=>page.evaluate(()=>(window as any).phone.getSnapshot().audioState),{timeout:15000}).toBe('live');
  expect(await page.evaluate(()=>(window as any).phone.getSnapshot().muted)).toBe(true);
  await expect.poll(()=>page.evaluate(()=>(window as any).diagnostics?.sessionEvents?.some((e:any)=>e.action==='websocket'&&e.code==='1006'))).toBe(true);
  await page.evaluate(() => (window as any).phone.reconnect({ inputGainDB: -6, playbackTargetMs: 80 }));
  await expect.poll(() => page.evaluate(() => (window as any).phone.getSnapshot().audioState)).toBe("live");
  expect(await page.evaluate(() => (window as any).phone.getSnapshot().muted)).toBe(true);
  await expect.poll(() => page.evaluate(() => (window as any).diagnostics?.micInputGainDb)).toBe(-6);
  await expect.poll(() => page.evaluate(() => (window as any).diagnostics?.targetMs)).toBeGreaterThanOrEqual(80);
  await page.evaluate(() => (window as any).phone.setMuted(false));
  await page.evaluate(() => (window as any).phone.hangup());
  expect(await page.evaluate(() => (window as any).phone.getSnapshot().callId)).toBeUndefined();
  await page.evaluate(() => { const w = window as any; w.watcher.close(); w.phone.dispose(); w.loaded.dispose(); });
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
