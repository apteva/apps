package wireguard

import (
	"strconv"
	"strings"

	"github.com/apteva/apps/mcp/vpn/backend"
)

// parseDump turns `wg show wg0 dump` output into PeerStats rows.
//
// The dump format is one line per record, tab-separated:
//
//	First line  (interface): privkey  pubkey  listen_port  fwmark
//	Subsequent lines (peers): pubkey  psk  endpoint  allowed_ips
//	                          latest_handshake  rx  tx  keepalive
//
// We skip the interface line entirely (we already know our own
// state) and read columns 0, 4, 5, 6 of each peer line. `wg` emits
// "(none)" for the PSK when one isn't configured; we don't look at
// that column so it's fine.
//
// latest_handshake is unix seconds, "0" if the peer never handshook.
// rx / tx are bytes, monotonic until restart.
func parseDump(out string) []backend.PeerStats {
	var stats []backend.PeerStats
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if i == 0 {
			// interface line; nothing we want here.
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		ts, _ := strconv.ParseInt(fields[4], 10, 64)
		rx, _ := strconv.ParseInt(fields[5], 10, 64)
		tx, _ := strconv.ParseInt(fields[6], 10, 64)
		stats = append(stats, backend.PeerStats{
			PublicKey:     fields[0],
			LastHandshake: ts,
			RxBytes:       rx,
			TxBytes:       tx,
		})
	}
	return stats
}
