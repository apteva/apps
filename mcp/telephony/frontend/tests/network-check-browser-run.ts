import { chromium } from "@playwright/test";
const origin = process.env.TELEPHONY_NETWORK_BROWSER_ORIGIN!;
if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin))
  throw Error("Only local network test fixtures are allowed");
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage();
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(origin);
  await page.waitForFunction(
    () => typeof (window as any).runNetworkReadiness === "function",
  );
  const impaired = process.env.TELEPHONY_NETWORK_EXPECTED === "degraded-relay";
  if (impaired)
    await page.evaluate(() => {
      (window as any).impairNetworkProbe = true;
    });
  const { result, requests } = await page.evaluate(() =>
    (window as any).runNetworkReadiness("configured"),
  );
  // Severe real loss can throttle even an unreliable SCTP datachannel below
  // the readiness sample minimum. Inconclusive is the correct safe result;
  // never demand a confident classification from insufficient observations.
  const classificationOK=impaired
    ? result.state==="degraded" || result.state==="inconclusive" && result.paths[0]?.reason==="insufficient_samples"
    : result.state==="ready";
  if (
    errors.length ||
    !classificationOK ||
    result.paths[0]?.candidateType !== (impaired ? "relay" : "host") ||
    result.paths[0]?.sent < 200 ||
    result.paths[0]?.received < (impaired ? 30 : 200) ||
    (!impaired &&
      (result.paths[0]?.uploadLossPct > 1 ||
        result.paths[0]?.downloadLossPct > 1)) ||
    (impaired &&
      Math.max(
        result.paths[0]?.uploadLossPct ?? 0,
        result.paths[0]?.downloadLossPct ?? 0,
      ) < 10)
  )
    throw Error(JSON.stringify({ errors, result }));
  // No microphone permission or getUserMedia was involved.
  const microphone = await page.evaluate(() =>
    navigator.permissions
      .query({ name: "microphone" as PermissionName })
      .then((v) => v.state),
  );
  if (microphone !== "prompt")
    throw Error("Network check requested microphone access");
  console.log(
    JSON.stringify({
      browser: browser.version(),
      result,
      requests,
      microphone,
    }),
  );
} finally {
  await browser.close();
}
