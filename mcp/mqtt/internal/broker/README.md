Broker core copied from github.com/mochi-mqtt/server/v2 v2.7.9 (MIT license in
LICENSE.md). Codecs, listeners, storage types and system types stay pinned upstream.

App-local changes add error-capable atomic persistence, serialize protocol state
changes, commit acceptance plus delivery obligations before network success,
separate inbound/outbound QoS2 identifiers and restore sessions before listening.
Review these boundaries when updating the upstream core.
