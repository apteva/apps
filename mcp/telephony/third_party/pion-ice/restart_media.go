// SPDX-License-Identifier: MIT
// Telephony compatibility patch to Pion ICE v4.4.4. See TELEPHONY-PATCH.md.
package ice

import (
	"context"
	"net/netip"
	"time"

	stunx "github.com/pion/ice/v4/internal/stun"
	"github.com/pion/stun/v4"
)

// One previous generation, never a second encoder/DTLS/SRTP pipeline. The
// selector sees only the new checklist; application writes retain the last
// nominated pair until a replacement is nominated. Existing ICE failure and
// application setup deadlines still bound an unsuccessful negotiation.
type restartMediaPath struct {
	pair                    *CandidatePair
	locals, remotes         map[NetworkType][]Candidate
	ufrag, pwd, remoteUfrag string
	cleanup                 *time.Timer // task-loop owner only
}

// Task-loop owner. Keep the old sockets out of the new gathering/checklist,
// preserving their receive caches and the already authenticated remote path.
func (a *Agent) retainRestartMedia() bool {
	pair := a.getSelectedPair()
	if pair == nil {
		return false
	}
	a.closeRestartMedia()
	a.restartMedia.Store(&restartMediaPath{pair: pair, locals: a.localCandidates, remotes: a.remoteCandidates, ufrag: a.localUfrag, pwd: a.localPwd, remoteUfrag: a.remoteUfrag})
	a.localCandidates = make(map[NetworkType][]Candidate)
	a.remoteCandidates = make(map[NetworkType][]Candidate)
	return true
}

func (a *Agent) closeRestartMedia() {
	held := a.restartMedia.Swap(nil)
	if held == nil {
		return
	}
	if held.cleanup != nil {
		held.cleanup.Stop()
	}
	a.removeUfragValueFromMux(held.ufrag)
	for _, group := range []map[NetworkType][]Candidate{held.locals, held.remotes} {
		for _, candidates := range group {
			for _, candidate := range candidates {
				_ = candidate.close()
			}
		}
	}
}

func (a *Agent) finishRestartMedia() {
	held := a.restartMedia.Load()
	if held == nil || held.cleanup != nil {
		return
	}
	// Allow in-flight packets on the nominated old path to drain after the new
	// pair is selected. No audio is buffered/replayed and no per-packet timer runs.
	held.cleanup = time.AfterFunc(250*time.Millisecond, func() {
		_ = a.loop.Run(a.loop, func(context.Context) {
			if a.restartMedia.Load() == held {
				a.closeRestartMedia()
			}
		})
	})
}

// Before the peer has applied the answer it can still send authenticated
// consent requests for the previous generation. Reply only for its nominated
// address and original username/integrity; never nominate it into the new ICE
// checklist or accept old credentials on a newly gathered socket.
func (a *Agent) handleRestartMediaBinding(msg *stun.Message, local Candidate, remote netip.AddrPort) bool {
	held := a.restartMedia.Load()
	if held == nil || !held.pair.Local.Equal(local) || !addrPortEqual(held.pair.Remote.addrPort(), remote) || msg.Type.Class != stun.ClassRequest || stunx.AssertUsername(msg, held.ufrag+":"+held.remoteUfrag) != nil {
		return false
	}
	if stun.MessageIntegrity([]byte(held.pwd)).Check(msg) == nil {
		response, err := stun.Build(msg, stun.BindingSuccess, &stun.XORMappedAddress{IP: remote.Addr().AsSlice(), Port: int(remote.Port())}, stun.NewShortTermIntegrity(held.pwd), stun.Fingerprint)
		if err == nil {
			a.sendSTUN(response, local, held.pair.Remote)
			held.pair.Remote.seen(false)
		}
	}
	return true
}
