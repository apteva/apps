#!/usr/bin/env python3
"""HTTPS-only sensor uploader using a device write credential, never an admin key."""
import json
import os
import ssl
import time
import urllib.error
import urllib.request
from urllib.parse import urlparse
from device_runtime import DeviceRuntime

URL = os.environ["APTEVA_DEVICE_INGEST_URL"]
TOKEN = os.environ["APTEVA_DEVICE_WRITE_TOKEN"]
parsed = urlparse(URL)
if parsed.scheme != "https" and not (parsed.scheme == "http" and parsed.hostname in ("127.0.0.1", "::1", "localhost")):
    raise ValueError("HTTPS is required except for local loopback tests")
CONTEXT = ssl.create_default_context(cafile=os.environ.get("APTEVA_HTTPS_CA_FILE") or None)
RUNTIME = DeviceRuntime(os.environ.get("APTEVA_DEVICE_JOURNAL", "https-device-journal.sqlite"))


def submit(kind, payload):
    # Configure URL for /state; use /telemetry for telemetry-only samples.
    target = URL.rsplit("/", 1)[0] + "/" + kind
    request = urllib.request.Request(target, data=payload.encode(), method="POST",
                                     headers={"X-Device-Token": TOKEN, "Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=15, context=CONTEXT) as response:
        receipt = json.load(response)
    if receipt.get("accepted") and receipt.get("message_id") == json.loads(payload)["message_id"]:
        RUNTIME.receipt(json.dumps({"status": "accepted", "message_id": receipt["message_id"]}))


def main():
    next_sample = 0.0
    try:
        while True:
            if time.monotonic() >= next_sample:
                RUNTIME.sample("state", {"temperature": 22.5}, {"simulated": True})
                next_sample = time.monotonic() + 10
            try:
                RUNTIME.resend(submit)
            except urllib.error.HTTPError as error:
                if error.code in (401, 403):
                    raise RuntimeError("device write credential was revoked, expired, or denied") from None
                if error.code in (400, 409, 413):
                    raise RuntimeError("submission requires operator correction; journal retained") from None
                print(f"Ingestion temporarily unavailable (HTTP {error.code}); retrying", flush=True)
                time.sleep(max(1, int(error.headers.get("Retry-After", "2"))))
            except (urllib.error.URLError, TimeoutError):
                print("Ingestion unavailable; journal retained for retry", flush=True)
            time.sleep(2)
    finally:
        RUNTIME.close()


if __name__ == "__main__":
    main()
