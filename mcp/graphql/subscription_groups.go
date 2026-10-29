package main

// Subscription groups fan one re-run out to every subscriber of the same
// invalidation-driven subscription. Members share a group only when the
// document, operation, variables, environment, topic and caller identity all
// match, so a result is never delivered to a caller who could not have run the
// query themselves. Each member has a single-slot mailbox holding the latest
// result: a slow connection only delays itself.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

type groupDelivery struct {
	result executeResult
	err    error
}

type groupMember struct {
	mailbox chan groupDelivery
}

type subscriptionGroup struct {
	key         string
	ctx         context.Context
	cancel      context.CancelFunc
	run         func(context.Context) (executeResult, error)
	unsubscribe func()
	mu          sync.Mutex
	members     map[*groupMember]struct{}
}

func subscriptionGroupKey(ctx context.Context, project, api, environment, topic string, req graphqlRequest) (string, error) {
	raw, err := json.Marshal([]any{project, normalizeAPISlug(api), normalizeEnvironment(environment), topic, req.Query, req.OperationName, req.Variables, executionIdentity(ctx, project)})
	if err != nil {
		return "", invalid("cannot form subscription identity")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// joinSubscriptionGroup adds a member, creating the group (and its single hub
// subscription) on first use. Join before the member's initial execution so a
// change committed meanwhile still reaches it. The group runs with the first
// member's identity, which every member shares by construction of the key.
func (a *App) joinSubscriptionGroup(ctx context.Context, key, project, api, topic string, run func(context.Context) (executeResult, error)) (*groupMember, func()) {
	member := &groupMember{mailbox: make(chan groupDelivery, 1)}
	a.groupMu.Lock()
	if a.groups == nil {
		a.groups = map[string]*subscriptionGroup{}
	}
	g := a.groups[key]
	if g == nil {
		groupCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		sub, unsubscribe := a.hub.subscribeCoalescedForAPI(project, api, topic)
		g = &subscriptionGroup{key: key, ctx: groupCtx, cancel: cancel, run: run, unsubscribe: unsubscribe, members: map[*groupMember]struct{}{}}
		a.groups[key] = g
		go g.loop(sub)
	}
	g.mu.Lock()
	g.members[member] = struct{}{}
	g.mu.Unlock()
	a.groupMu.Unlock()
	var once sync.Once
	leave := func() {
		once.Do(func() {
			a.groupMu.Lock()
			defer a.groupMu.Unlock()
			g.mu.Lock()
			delete(g.members, member)
			empty := len(g.members) == 0
			g.mu.Unlock()
			if empty {
				if a.groups[key] == g {
					delete(a.groups, key)
				}
				g.cancel()
				g.unsubscribe()
			}
		})
	}
	return member, leave
}

func (g *subscriptionGroup) loop(sub *subscription) {
	retry := time.Duration(0)
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-sub.done:
			return
		case <-sub.events:
			// Events are invalidations: one re-run covers every queued one, and
			// it may only share an execution that started afterwards.
			received := time.Now()
			for drained := false; !drained; {
				select {
				case <-sub.events:
				default:
					drained = true
				}
			}
			result, err := g.run(context.WithValue(g.ctx, sharedAfterKey{}, received))
			if g.ctx.Err() != nil {
				return
			}
			// A failed re-run would leave every member stale until the next
			// commit, so it is retried with backoff.
			if err != nil || len(result.Errors) > 0 {
				retry = min(max(2*retry, 250*time.Millisecond), 5*time.Second)
				time.AfterFunc(retry, func() {
					select {
					case sub.events <- nil:
					default:
					}
				})
			} else {
				retry = 0
			}
			g.deliver(groupDelivery{result: result, err: err})
		}
	}
}

// deliver replaces each member's pending result with the newest one.
func (g *subscriptionGroup) deliver(d groupDelivery) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for member := range g.members {
		select {
		case <-member.mailbox:
		default:
		}
		select {
		case member.mailbox <- d:
		default:
		}
	}
}
