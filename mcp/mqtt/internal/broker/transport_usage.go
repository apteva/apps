package mqtt

import "github.com/mochi-mqtt/server/v2/packets"

// TransportUsage is error-capable and reserves usage before success or writes.
// Failed/partial writes are conservatively charged; TLS framing is excluded.
type TransportUsage interface {
	Open(*Client, packets.Packet) error
	Close(*Client)
	Read(*Client, packets.Packet, int) error
	Write(*Client, packets.Packet, int) error
}

func PacketBytes(pk packets.Packet) int {
	n := pk.FixedHeader.Remaining
	size := n + 2
	for n >= 128 {
		size++
		n /= 128
	}
	return size
}

func (cl *Client) InboundPacketBytes(pk packets.Packet) int {
	if cl.State.headerBytes > 0 {
		return pk.FixedHeader.Remaining + cl.State.headerBytes
	}
	return PacketBytes(pk)
}
