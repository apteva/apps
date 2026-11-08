"""Verified MQTT TLS, configured from enrollment metadata."""
import os
import ssl


def configure_tls(client):
    value = os.environ.get("APTEVA_MQTT_TLS", "true").strip().lower()
    if value not in ("true", "false"):
        raise ValueError("APTEVA_MQTT_TLS must be true or false")
    if value == "true":
        # Default system trust or the provisioned private CA. Hostname and
        # chain verification stay enabled; there is no insecure fallback.
        context = ssl.create_default_context(cafile=os.environ.get("APTEVA_MQTT_CA_FILE") or None)
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        client.tls_set_context(context)
