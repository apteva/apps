import { runNetworkCheck } from "../src/network-check";
(window as any).runNetworkReadiness = async (path: string) => {
  const requests: any[] = [];
  const result = await runNetworkCheck(
    async (body, signal) => {
      requests.push({ action: body.action });
      const response = await fetch("/softphone/network-check", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
        signal,
      });
      if (!response.ok) throw Error(`Network check ${response.status}`);
      const result = await response.json();
      if (body.action === "offer" && (window as any).impairNetworkProbe)
        setTimeout(() => void fetch("/arm", { method: "POST" }), 300);
      return result;
    },
    { paths: [path as any] },
  );
  return { result, requests };
};
