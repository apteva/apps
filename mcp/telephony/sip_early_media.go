package main

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Outbound calls play local ringback. Consume pre-bridge datagrams from socket
// allocation onward, without decoding, forwarding, or attributing them to live
// conversation loss. In particular, provisional SRTP keys must never seed the
// final answer's decryptor. One owner hands the socket to the live reader.
type sipEarlyPacket struct {
	data   []byte
	source *net.UDPAddr
	at     time.Time
}
type sipEarlyReceiver struct {
	answered atomic.Bool
	pending  chan sipEarlyPacket
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

func (m *sipRTPMedia) startEarlyReceiver() {
	e := &sipEarlyReceiver{stop: make(chan struct{}), done: make(chan struct{}), pending: make(chan sipEarlyPacket, 8)}
	m.early = e
	go func() {
		defer close(e.done)
		buffer := make([]byte, 4096)
		for {
			select {
			case <-e.stop:
				return
			default:
			}
			_ = m.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			n, source, err := m.readSocketPacket(buffer)
			if err != nil {
				if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
					continue
				}
				return
			}
			if e.answered.Load() {
				packet := sipEarlyPacket{data: append([]byte(nil), buffer[:n]...), source: source, at: time.Now()}
				select {
				case e.pending <- packet:
				default:
					// Keep the most recent bounded reserve until the bridge owns
					// reception. This queue is only used during activation.
					select {
					case <-e.pending:
						m.observations.activationDropped.Add(1)
					default:
					}
					select {
					case e.pending <- packet:
					default:
						m.observations.activationDropped.Add(1)
					}
				}
				continue
			}
			m.observations.earlyPackets.Add(1)
			m.observations.earlyBytes.Add(int64(n))
		}
	}()
}

func (m *sipRTPMedia) finishEarlyReceiver() {
	if m.early == nil {
		return
	}
	// The bounded deadline avoids racing SetReadDeadline with the old reader.
	m.early.once.Do(func() { close(m.early.stop) })
	<-m.early.done
	m.observations.earlyKernelDrops.Store(m.observations.kernelDrops.Load())
	_ = m.conn.SetReadDeadline(time.Time{})
	m.lastPacket.Store(time.Now().UnixNano())
}

// Once the owner has stopped, the bridge consumes a small fresh reserve before
// returning to direct socket reads. Never carry old activation audio forward.
func (m *sipRTPMedia) readPacket(buffer []byte) (int, *net.UDPAddr, error) {
	if m.early != nil {
		for {
			select {
			case p := <-m.early.pending:
				if time.Since(p.at) > 200*time.Millisecond {
					m.observations.activationDropped.Add(1)
					continue
				}
				m.readAt = p.at
				return copy(buffer, p.data), p.source, nil
			default:
				goto socket
			}
		}
	}
socket:
	n, source, err := m.readSocketPacket(buffer)
	m.readAt = time.Now()
	return n, source, err
}
