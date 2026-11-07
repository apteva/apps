# Instances 0.14.0

Adds the generic Apteva resource-policy contract to the native runtime backend.
Validated CPUWeight, CPUQuota, MemoryHigh/Max, IOWeight and Nice settings persist
in the deployment systemd unit. Inspection reports effective process settings
and bounded CPU, IO and memory pressure evidence. Unspecified controls retain
host defaults. No realtime scheduler or host sysctl changes are made.

Uses app-sdk 0.108.0 and retains existing deployment/state-transfer behavior.
Validated with the full Instances Go suite and systemd-rendering/readback tests.
