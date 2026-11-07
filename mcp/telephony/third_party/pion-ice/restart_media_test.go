// SPDX-License-Identifier: MIT
package ice

import (
	"bytes"
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/stretchr/testify/require"
)

func TestRestartKeepsBidirectionalMediaUntilNewPairAndReapsSockets(t *testing.T) {
	a, b := pipe(t, &AgentConfig{NetworkTypes: []NetworkType{NetworkTypeUDP4}, CandidateTypes: []CandidateType{CandidateTypeHost}})
	defer closePipe(t, a, b)
	require.NoError(t, a.agent.OnConnectionStateChange(func(ConnectionState) {}))
	require.NoError(t, b.agent.OnConnectionStateChange(func(ConnectionState) {}))
	old, err := a.agent.GetSelectedCandidatePair()
	require.NoError(t, err)
	require.NoError(t, a.agent.Restart("", ""))
	require.NotNil(t, a.agent.restartMedia.Load())
	// Withhold gathering/signaling entirely: old media must continue in both
	// directions without a new DTLS session or a second audio encoder.
	for i := 0; i < 25; i++ {
		p := []byte{0x80, byte(i), 3, 4}
		_, err = a.Write(p)
		require.NoError(t, err)
		got := make(chan []byte, 1)
		go func() { v := make([]byte, 64); n, _ := b.Read(v); got <- v[:n] }()
		select {
		case q := <-got:
			require.True(t, bytes.Equal(p, q))
		case <-time.After(time.Second):
			t.Fatal("old send path interrupted")
		}
		_, err = b.Write(p)
		require.NoError(t, err)
		go func() { v := make([]byte, 64); n, _ := a.Read(v); got <- v[:n] }()
		select {
		case q := <-got:
			require.True(t, bytes.Equal(p, q))
		case <-time.After(time.Second):
			t.Fatal("old receive socket closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Repeat before connecting: never retain an unbounded chain of generations.
	held := a.agent.restartMedia.Load()
	require.NoError(t, a.agent.Restart("", ""))
	require.Same(t, held, a.agent.restartMedia.Load())
	require.NoError(t, b.agent.Restart("", ""))
	u, p, err := a.agent.GetLocalUserCredentials()
	require.NoError(t, err)
	require.NoError(t, b.agent.SetRemoteCredentials(u, p))
	u, p, err = b.agent.GetLocalUserCredentials()
	require.NoError(t, err)
	require.NoError(t, a.agent.SetRemoteCredentials(u, p))
	gatherAndExchangeCandidates(t, a.agent, b.agent)
	require.Eventually(t, func() bool {
		pair, _ := a.agent.GetSelectedCandidatePair()
		return pair != nil && pair.Local.Port() != old.Local.Port()
	}, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return a.agent.restartMedia.Load() == nil }, time.Second, 10*time.Millisecond)
}

func TestRestartMediaRejectsForgedConsentAndUnknownSource(t *testing.T) {
	a, b := pipe(t, &AgentConfig{NetworkTypes: []NetworkType{NetworkTypeUDP4}, CandidateTypes: []CandidateType{CandidateTypeHost}})
	defer closePipe(t, a, b)
	require.NoError(t, a.agent.Restart("", ""))
	held := a.agent.restartMedia.Load()
	require.NotNil(t, held)
	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest, stun.NewUsername(held.ufrag+":"+held.remoteUfrag), stun.NewShortTermIntegrity("incorrect-password"), stun.Fingerprint)
	require.NoError(t, a.agent.loop.Run(a.agent.loop, func(context.Context) {
		last := held.pair.Remote.LastReceived()
		require.True(t, a.agent.handleRestartMediaBinding(msg, held.pair.Local, held.pair.Remote.addrPort()))
		require.Equal(t, last, held.pair.Remote.LastReceived(), "forged request renewed consent")
	}))
	_, valid := a.agent.validateNonSTUNTraffic(held.pair.Local, netip.MustParseAddrPort("127.0.0.1:1"))
	require.False(t, valid)
	require.NoError(t, a.agent.Close())
	require.Nil(t, a.agent.restartMedia.Load())
}
