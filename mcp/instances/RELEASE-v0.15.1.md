# Instances v0.15.1

Native deployment target inspection now tolerates Debian SONAMEs containing `~`
by omitting those unrepresentable optional names from its library inventory.
Previously, an unrelated distro-versioned library made an otherwise valid host
fail the entire target probe.

The wire contract remains unchanged. Malformed names and paths still fail, and
all declared package library requirements must match the advertised inventory.
SSH, host identity, systemd, gateway, ABI and capacity validation remain enforced.

Includes a Debian SONAME regression and malformed/overlong inventory cases.
