package main

// Subscription groups fan one re-run out to every subscriber of the same
// invalidation-driven subscription. Members share a group only when the
// document, operation, variables, environment, topic and caller identity all
// match, so a result is never delivered to a caller who could not have run the
// query themselves. Each member has a single-slot mailbox holding the latest
// result: a slow connection only delays itself.
//
// A result is only delivered while it is the newest run, so a result read under the
// old authority can never follow one that reflects the revocation, however late its
// read comes back. A commit replaces a running refresh, up to a limit, so constant
// writes can't keep every result from being delivered. Grant and profile sources
// have a higher limit than other data.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

const (
	// maxGroupDataReplacements is how many refreshes in a row a change that isn't
	// about authority may replace. After that a refresh finishes, is delivered, and
	// the next one covers what arrived meanwhile.
	maxGroupDataReplacements = 3
	// maxGroupGrantReplacements is the same limit for changes to grant and profile
	// sources. A busy profile table must not silence a subscriber, and each result
	// already matches the grants in its own snapshot. A result finishing at this limit
	// can predate the latest change, but nothing newer has been delivered, so no row
	// can come back after being removed, and the next refresh follows at once.
	maxGroupGrantReplacements = 16
	// maxGroupLiveRuns bounds replaced reads that are still unwinding. Past it the
	// newest change waits for one of them; nothing old is delivered meanwhile.
	maxGroupLiveRuns = 8
)

type groupDelivery struct {
	result executeResult
	err    error
}

type groupMember struct {
	mailbox chan groupDelivery
	// after is the newest run already started when this member joined. Its own first
	// read starts later, so results of those runs could only take it backwards.
	after uint64
	group *subscriptionGroup
}

// overtaken reports whether a refresh has started since this member joined. Its
// result will reach the mailbox, so the member's own first read is no longer needed.
func (m *groupMember) overtaken() bool {
	m.group.mu.Lock()
	defer m.group.mu.Unlock()
	return m.group.generation > m.after
}

type subscriptionGroup struct {
	key         string
	ctx         context.Context
	cancel      context.CancelFunc
	run         func(context.Context) (executeResult, error)
	unsubscribe func()
	authority   map[string]bool
	mu          sync.Mutex
	members     map[*groupMember]struct{}
	generation  uint64
}

func subscriptionGroupKey(ctx context.Context, project, api, environment, topic string, req graphqlRequest) (string, error) {
	raw, err := json.Marshal([]any{project, normalizeAPISlug(api), normalizeEnvironment(environment), topic, req.Query, req.OperationName, req.Variables, executionIdentity(ctx, project)})
	if err != nil {
		return "", invalid("cannot form subscription identity")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// joinSubscriptionGroup joins a group with no known grant or profile sources.
func (a *App) joinSubscriptionGroup(ctx context.Context, key, project, api, topic string, run func(context.Context) (executeResult, error)) (*groupMember, func()) {
	return a.joinSubscriptionGroupWatching(ctx, key, project, api, topic, nil, run)
}

// joinSubscriptionGroupWatching adds a member, creating the group (and its single
// hub subscription) on first use. Join before the member's initial execution so a
// change committed meanwhile still reaches it. The group runs with the first
// member's identity, which every member shares by construction of the key.
func (a *App) joinSubscriptionGroupWatching(ctx context.Context, key, project, api, topic string, authority []string, run func(context.Context) (executeResult, error)) (*groupMember, func()) {
	member := &groupMember{mailbox: make(chan groupDelivery, 1)}
	a.groupMu.Lock()
	if a.groups == nil {
		a.groups = map[string]*subscriptionGroup{}
	}
	g := a.groups[key]
	if g == nil {
		groupCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		sub, unsubscribe := a.hub.subscribeCoalescedForAPI(project, api, topic)
		g = &subscriptionGroup{key: key, ctx: groupCtx, cancel: cancel, run: run, unsubscribe: unsubscribe, authority: map[string]bool{}, members: map[*groupMember]struct{}{}}
		for _, name := range authority {
			g.authority[name] = true
		}
		a.groups[key] = g
		go g.loop(sub)
	}
	g.mu.Lock()
	member.group = g
	member.after = g.generation
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

// firstSubscriptionRead runs a subscriber's initial execution and says whether to
// send its result. A refresh started since the subscriber joined covers what this
// read saw, so the read gives way to it: sending the read first could show a row a
// revocation has already removed, however late the read comes back.
func firstSubscriptionRead(ctx context.Context, member *groupMember, read func(context.Context) (executeResult, error)) (executeResult, bool, error) {
	if member == nil {
		result, err := read(ctx)
		return result, true, err
	}
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan groupDelivery, 1)
	go func() {
		result, err := read(readCtx)
		finished <- groupDelivery{result: result, err: err}
	}()
	for {
		select {
		case first := <-finished:
			if first.err != nil {
				return first.result, false, first.err
			}
			return first.result, !member.overtaken(), nil
		case delivery := <-member.mailbox:
			// A failed refresh says nothing newer, and the group retries it.
			if delivery.err != nil {
				continue
			}
			return delivery.result, true, nil
		}
	}
}

type groupOutcome struct {
	generation uint64
	result     executeResult
	err        error
}

// touchesAuthority reports whether an invalidation names a grant or profile source.
// An event that can't say what changed counts, since it might have.
func (g *subscriptionGroup) touchesAuthority(event map[string]any) bool {
	if len(g.authority) == 0 || event == nil {
		return false
	}
	switch names := event["collections"].(type) {
	case []string:
		for _, name := range names {
			if g.authority[name] {
				return true
			}
		}
		return false
	case []any:
		for _, name := range names {
			if text, ok := name.(string); ok && g.authority[text] {
				return true
			}
		}
		return false
	}
	return true
}

func (g *subscriptionGroup) loop(sub *subscription) {
	finished := make(chan groupOutcome)
	stopped := make(chan struct{})
	defer close(stopped)
	var current uint64 // the run whose result may be delivered; 0 when none is
	var cancelCurrent context.CancelFunc
	var pendingAt time.Time // a change no run has covered yet
	live, replacements := 0, 0
	retry := time.Duration(0)
	abandon := func() {
		if cancelCurrent != nil {
			cancelCurrent()
		}
		current, cancelCurrent = 0, nil
	}
	defer abandon()
	start := func(received time.Time) {
		g.mu.Lock()
		g.generation++
		generation := g.generation
		g.mu.Unlock()
		// A re-run may only share an execution that started after this change.
		runCtx, cancel := context.WithCancel(context.WithValue(g.ctx, sharedAfterKey{}, received))
		current, cancelCurrent, pendingAt = generation, cancel, time.Time{}
		live++
		go func() {
			result, err := g.run(runCtx)
			cancel()
			select {
			case finished <- groupOutcome{generation: generation, result: result, err: err}:
			case <-stopped:
			}
		}()
	}
	startPending := func() {
		if current == 0 && !pendingAt.IsZero() && live < maxGroupLiveRuns {
			start(pendingAt)
		}
	}
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-sub.done:
			return
		case event := <-sub.events:
			// Events are invalidations: one re-run covers every queued one, and it may
			// only share an execution that started afterwards.
			received := time.Now()
			authority := g.touchesAuthority(event)
			for drained := false; !drained; {
				select {
				case queued := <-sub.events:
					authority = authority || g.touchesAuthority(queued)
				default:
					drained = true
				}
			}
			// A lost event might have named a grant source.
			if sub.dropped.Swap(false) && len(g.authority) > 0 {
				authority = true
			}
			// The next run may only share an execution newer than this change.
			pendingAt = received
			limit := maxGroupDataReplacements
			if authority {
				limit = maxGroupGrantReplacements
			}
			if current != 0 && replacements < limit {
				replacements++
				abandon()
			}
			startPending()
		case outcome := <-finished:
			live--
			if outcome.generation != current {
				startPending()
				continue // replaced: a newer run decides what members see
			}
			current, cancelCurrent = 0, nil
			if g.ctx.Err() != nil {
				return
			}
			replacements = 0
			// A failed re-run would leave every member stale until the next
			// commit, so it is retried with backoff.
			if outcome.err != nil || len(outcome.result.Errors) > 0 {
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
			g.deliver(outcome.generation, groupDelivery{result: outcome.result, err: outcome.err})
			startPending()
		}
	}
}

// deliver replaces each member's pending result with the newest one. A member that
// joined after this run started has its own newer read, so it is left out.
func (g *subscriptionGroup) deliver(generation uint64, d groupDelivery) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for member := range g.members {
		if generation <= member.after {
			continue
		}
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
