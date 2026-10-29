package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

const groupTopic = "database:project:prod:orders"

func countingRun(calls *atomic.Int32, value string) func(context.Context) (executeResult, error) {
	return func(context.Context) (executeResult, error) {
		calls.Add(1)
		return executeResult{Data: map[string]any{"v": value}}, nil
	}
}

func receive(t *testing.T, member *groupMember) groupDelivery {
	t.Helper()
	select {
	case delivery := <-member.mailbox:
		return delivery
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery")
		return groupDelivery{}
	}
}

func groupKey(t *testing.T, ctx context.Context, variables map[string]any) string {
	t.Helper()
	key, err := subscriptionGroupKey(ctx, "p1", "default", "development", groupTopic, graphqlRequest{Query: "subscription { orderCount }", Variables: variables})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSubscriptionGroupRunsOncePerInvalidation(t *testing.T) {
	a := &App{hub: newSubscriptionHub()}
	defer a.hub.close()
	var shared, other atomic.Int32
	sharedKey := groupKey(t, context.Background(), map[string]any{"region": "eu"})
	first, leaveFirst := a.joinSubscriptionGroup(context.Background(), sharedKey, "p1", "default", groupTopic, countingRun(&shared, "eu"))
	second, leaveSecond := a.joinSubscriptionGroup(context.Background(), sharedKey, "p1", "default", groupTopic, countingRun(&shared, "unused"))
	otherKey := groupKey(t, context.Background(), map[string]any{"region": "us"})
	third, leaveThird := a.joinSubscriptionGroup(context.Background(), otherKey, "p1", "default", groupTopic, countingRun(&other, "us"))

	a.hub.publishAllAPIs("p1", groupTopic, map[string]any{})
	for _, member := range []*groupMember{first, second} {
		if got := receive(t, member).result.Data["v"]; got != "eu" {
			t.Fatalf("shared member got %v", got)
		}
	}
	if got := receive(t, third).result.Data["v"]; got != "us" {
		t.Fatalf("other variables got %v", got)
	}
	if shared.Load() != 1 || other.Load() != 1 {
		t.Fatalf("re-runs per group: shared=%d other=%d", shared.Load(), other.Load())
	}

	leaveFirst()
	leaveSecond()
	leaveThird()
	a.groupMu.Lock()
	groups := len(a.groups)
	a.groupMu.Unlock()
	a.hub.mu.Lock()
	subs := len(a.hub.subs)
	a.hub.mu.Unlock()
	if groups != 0 || subs != 0 {
		t.Fatalf("groups=%d hub subscriptions=%d after everyone left", groups, subs)
	}
}

func TestSubscriptionGroupKeySeparatesCallers(t *testing.T) {
	alice := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Issuer: "auth", Subject: "alice"})
	bob := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Issuer: "auth", Subject: "bob"})
	if groupKey(t, alice, nil) == groupKey(t, bob, nil) {
		t.Fatal("different callers would share one subscription group")
	}
	if groupKey(t, alice, nil) != groupKey(t, alice, nil) {
		t.Fatal("the same caller and query must share a group")
	}
}

func TestSlowSubscriptionGroupMemberKeepsOnlyTheLatestResult(t *testing.T) {
	a := &App{hub: newSubscriptionHub()}
	defer a.hub.close()
	var calls atomic.Int32
	run := func(context.Context) (executeResult, error) {
		n := calls.Add(1)
		return executeResult{Data: map[string]any{"v": int(n)}}, nil
	}
	key := groupKey(t, context.Background(), nil)
	slow, leaveSlow := a.joinSubscriptionGroup(context.Background(), key, "p1", "default", groupTopic, run)
	defer leaveSlow()
	fast, leaveFast := a.joinSubscriptionGroup(context.Background(), key, "p1", "default", groupTopic, run)
	defer leaveFast()
	for want := 1; want <= 3; want++ {
		a.hub.publishAllAPIs("p1", groupTopic, map[string]any{})
		if got := receive(t, fast).result.Data["v"]; got != want {
			t.Fatalf("fast member got %v, want %d", got, want)
		}
	}
	if got := receive(t, slow).result.Data["v"]; got != 3 {
		t.Fatalf("slow member should hold only the latest result, got %v", got)
	}
	select {
	case extra := <-slow.mailbox:
		t.Fatalf("slow member kept a stale result: %v", extra.result.Data)
	default:
	}
}

func TestSubscriptionGroupRetriesFailedReRun(t *testing.T) {
	a := &App{hub: newSubscriptionHub()}
	defer a.hub.close()
	var calls atomic.Int32
	run := func(context.Context) (executeResult, error) {
		if calls.Add(1) == 1 {
			return executeResult{}, errors.New("database.db_count: unavailable")
		}
		return executeResult{Data: map[string]any{"v": "fresh"}}, nil
	}
	member, leave := a.joinSubscriptionGroup(context.Background(), groupKey(t, context.Background(), nil), "p1", "default", groupTopic, run)
	defer leave()
	a.hub.publishAllAPIs("p1", groupTopic, map[string]any{})
	if delivery := receive(t, member); delivery.err == nil {
		t.Fatalf("first re-run should report its failure: %v", delivery.result.Data)
	}
	// No further commit: the retry alone must bring the member up to date.
	if delivery := receive(t, member); delivery.err != nil || delivery.result.Data["v"] != "fresh" {
		t.Fatalf("retry: %v %v", delivery.err, delivery.result.Data)
	}
}
