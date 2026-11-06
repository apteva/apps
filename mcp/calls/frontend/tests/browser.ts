import { chromium, expect } from "@playwright/test";

const [gateway, externalOrigin, room, invite, token] = process.argv.slice(2);
const browser = await chromium.launch({
  args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream", "--autoplay-policy=no-user-gesture-required", "--disable-features=WebRtcHideLocalIpsWithMdns"],
});
try {
  const hosted = await browser.newPage();
  const external = await browser.newPage();
  const panel = await browser.newPage();
  for (const page of [hosted, external, panel]) {
    page.on("pageerror", error => console.error(error));
    page.on("console", event => { if (event.type() === "error") console.error(event.text()); });
  }
  await hosted.goto(gateway + invite);
  await hosted.getByRole("button", { name: "Join room" }).click();
  await expect(hosted.getByRole("button", { name: "Joined", exact: true })).toBeVisible();
  const externalQuery = new URLSearchParams({ gateway, token });
  await external.goto(externalOrigin + "/?" + externalQuery);
  await expect(external.locator("#root")).toHaveText("joined");
  await panel.goto(gateway + "/panel?room=" + room);
  await panel.getByRole("button", { name: "Join as host" }).click();
  await expect(panel.getByRole("button", { name: "Turn camera on" })).toBeVisible();

  await hosted.getByRole("button", { name: "Mic", exact: true }).click();
  await expect(hosted.getByRole("button", { name: "Mic on" })).toBeVisible();
  await hosted.getByRole("button", { name: "Cam", exact: true }).click();
  await external.evaluate(() => window.calls.setCamera(true));
  await panel.getByRole("button", { name: "Turn camera on" }).click();
  await expect.poll(() => external.evaluate(() => window.calls.getSnapshot().remoteStreams.length), { timeout: 15000, message: "External client receives two remote streams" }).toBe(2);
  await expect.poll(() => hosted.locator(".tile:not(.primary) video").evaluateAll(
    videos => videos.filter(video => (video as HTMLVideoElement).videoWidth > 0).length,
  ), { timeout: 15000, message: "Hosted page receives two playable videos" }).toBe(2);
  await expect.poll(() => panel.locator("video:not([muted])").evaluateAll(
    videos => videos.filter(video => !(video as HTMLVideoElement).muted && (video as HTMLVideoElement).videoWidth > 0).length,
  ), { timeout: 15000, message: "Panel receives two playable videos" }).toBe(2);

  await external.evaluate(() => window.calls.sendMessage("Hello from an external UI"));
  await expect(hosted.getByText("Hello from an external UI")).toBeVisible();
  await expect(panel.getByText("Hello from an external UI")).toBeVisible();

  const ownedTracks = await external.evaluateHandle(() => window.calls.getSnapshot().localStream!.getTracks());
  panel.on("dialog", dialog => void dialog.accept());
  await panel.getByRole("button", { name: "End for everyone" }).click();
  await expect(external.locator("#root")).toHaveText("ended");
  await expect(hosted.getByRole("button", { name: "Room closed" })).toBeVisible();
  expect(await ownedTracks.evaluate(tracks => tracks.every(track => track.readyState === "ended"))).toBe(true);
  expect(await external.evaluate(() => window.calls.getSnapshot().remoteStreams.length)).toBe(0);
} catch (error) {
  for (const page of browser.contexts().flatMap(context => context.pages())) {
    console.error(new URL(page.url()).pathname, await page.locator("body").innerText());
    if (await page.evaluate(() => !!window.calls)) {
      console.error(await page.evaluate(() => {
        const { error, participants, remoteStreams, camera } = window.calls.getSnapshot();
        return { error, participants: participants.map(p => p.id), streams: remoteStreams.map(item => item.stream.getTracks().map(track => track.kind)), camera };
      }));
    }
  }
  throw error;
} finally {
  await browser.close();
}
