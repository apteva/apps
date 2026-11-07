import { chromium } from "@playwright/test";
const origin = process.env.TELEPHONY_NETWORK_BROWSER_ORIGIN!;
if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin))
  throw Error("Only local fixtures allowed");
const browser = await chromium.launch({
  headless: true,
  args: [
    "--autoplay-policy=no-user-gesture-required",
    "--use-fake-device-for-media-stream",
    "--use-fake-ui-for-media-stream",
  ],
});
try {
  const page = await browser.newPage({ permissions: ["microphone"] });
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(origin);
  await page.waitForFunction(
    () => typeof (window as any).runNetworkFault === "function",
  );
  const result = await page.evaluate(async () => {
    const c = await (await fetch("/config")).json();
    return await (window as any).runNetworkFault(c.media_url, c.enabled);
  });
  if (errors.length) throw Error(errors.join(","));
  console.log(JSON.stringify({ browser: browser.version(), ...result }));
} finally {
  await browser.close();
}
