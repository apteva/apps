package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// heldRead is one Database read that has its answer and hasn't returned it.
type heldRead struct {
	paused  chan struct{}
	release chan struct{}
	done    chan struct{}
}

// liveFixture serves a published Subscription over the real realtime handler. Only
// Database is fake, so the whole path from a commit to a client's socket runs.
type liveFixture struct {
	t       *testing.T
	fixture *joinedFixture
	server  *httptest.Server
	revoked atomic.Bool
	mu      sync.Mutex
	holds   []*heldRead
	reads   atomic.Int32
}

const liveSDL = `
type Query { ping: Int }
type Subscription { live: [LiveCall!]! }
type LiveCall { id: ID prospect: String }
`

// The row depends on a call, its linked prospect, sale and offer. The viewer's
// centres come from a saved profile, and the offers from a saved grant.
const livePlan = `{"database": "crm",
  "params": {"user": {"from": "$identity.subject", "type": "string", "required": true}},
  "access": {"values": {
    "centres": {"optional": true, "otherwise": {"collection": "profiles", "field": "centre_id", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}},
    "offers": {"optional": true, "otherwise": {"collection": "grants", "field": "offer_ids", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}}
  }},
  "steps": [{"id": "page", "op": "joined_page", "max_rows": 10, "args": {
    "collection": "calls",
    "where": {"field": "centre_id", "op": "in", "value": "$scope.centres"},
    "joins": [
      {"as": "p", "collection": "prospects", "kind": "left", "on": [{"local": "root.prospect_id", "foreign": "id"}]},
      {"as": "s", "collection": "sales", "kind": "left", "on": [{"local": "root.sale_id", "foreign": "id"}]},
      {"as": "o", "collection": "offers", "kind": "left", "on": [{"local": "s.offer_id", "foreign": "id"}]}],
    "select": ["root.id", "p.name"]},
   "shape": {"rename": {"root.id": "id", "p.name": "prospect"}}}],
  "result": "$steps.page"}`

func newLiveFixture(t *testing.T) *liveFixture {
	t.Helper()
	f := newJoinedFixture(t, nil)
	l := &liveFixture{t: t, fixture: f}
	db := f.app.ctx.AppDB()
	schema, issues, err := createSchemaForAPI(db, "p1", "default", "production", liveSDL, 0)
	if err != nil || len(issues) > 0 {
		t.Fatal(issues, err)
	}
	f.schema = schema.Version
	if _, err := upsertResolverForAPI(db, "p1", "default", "Subscription", "live", databasePipelineOperation, f.source, decodeConfig(t, livePlan)); err != nil {
		t.Fatal(err)
	}
	f.db.respond = func(_ context.Context, op string, args map[string]any) (any, error) {
		switch args["collection"] {
		case "profiles":
			if l.revoked.Load() {
				return map[string]any{"records": []any{}, "hasMore": false}, nil
			}
			return map[string]any{"records": []any{map[string]any{"centre_id": "a"}}, "hasMore": false}, nil
		case "grants":
			return map[string]any{"records": []any{}, "hasMore": false}, nil
		}
		if op == joinedPage {
			if filterMatchesNothing(args["where"]) {
				return map[string]any{"records": []any{}, "hasMore": false, "total": "0"}, nil
			}
			return map[string]any{"records": []any{map[string]any{"root.id": "7", "p.name": fmt.Sprintf("read %d", l.reads.Add(1))}}, "hasMore": false, "total": "1"}, nil
		}
		return emptyAnswer(op), nil
	}
	f.db.afterRead = func(operations []map[string]any) {
		joined := false
		for _, operation := range operations {
			if args, _ := operation["args"].(map[string]any); operation["op"] == joinedPage && !filterMatchesNothing(args["where"]) {
				joined = true
			}
		}
		if !joined {
			return
		}
		l.mu.Lock()
		var hold *heldRead
		if len(l.holds) > 0 {
			hold, l.holds = l.holds[0], l.holds[1:]
		}
		l.mu.Unlock()
		if hold != nil {
			close(hold.paused)
			<-hold.release
			defer close(hold.done)
		}
	}
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	identity := &requestIdentity{Subject: "21", Permissions: []string{"manager"}, Expires: time.Now().Add(time.Hour)}
	l.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.app.handleRealtime(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, identity)))
	}))
	t.Cleanup(l.server.Close)
	return l
}

// holdNextRead pauses the next read that can return a visible row.
func (l *liveFixture) holdNextRead() *heldRead {
	hold := &heldRead{paused: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	l.mu.Lock()
	l.holds = append(l.holds, hold)
	l.mu.Unlock()
	return hold
}

func (l *liveFixture) commit(collections ...string) {
	l.fixture.db.commits <- collections
}

type liveClient struct {
	t        *testing.T
	conn     *websocket.Conn
	messages chan wsMessage
}

func (l *liveFixture) connect() *liveClient {
	l.t.Helper()
	header := http.Header{}
	header.Set("Sec-WebSocket-Protocol", "graphql-transport-ws")
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(l.server.URL, "http")+"/realtime/default", header)
	if err != nil {
		l.t.Fatal(err)
	}
	l.t.Cleanup(func() { conn.Close() })
	client := &liveClient{t: l.t, conn: conn, messages: make(chan wsMessage, 64)}
	if err := conn.WriteJSON(map[string]any{"type": "connection_init"}); err != nil {
		l.t.Fatal(err)
	}
	var ack wsMessage
	if err := conn.ReadJSON(&ack); err != nil || ack.Type != "connection_ack" {
		l.t.Fatal(ack, err)
	}
	go func() {
		defer close(client.messages)
		for {
			var message wsMessage
			if conn.ReadJSON(&message) != nil {
				return
			}
			client.messages <- message
		}
	}()
	if err := conn.WriteJSON(map[string]any{"id": "s", "type": "subscribe", "payload": map[string]any{"query": `subscription {live {id prospect}}`, "environment": "production"}}); err != nil {
		l.t.Fatal(err)
	}
	return client
}

// next returns the rows of the next result.
func (c *liveClient) next() string {
	c.t.Helper()
	select {
	case message, open := <-c.messages:
		if !open {
			c.t.Fatal("the connection closed")
		}
		if message.Type != "next" {
			c.t.Fatalf("got %s: %s", message.Type, message.Payload)
		}
		var payload struct {
			Data struct {
				Live []map[string]any `json:"live"`
			} `json:"data"`
			Errors []map[string]any `json:"errors"`
		}
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			c.t.Fatal(err)
		}
		if len(payload.Errors) > 0 {
			c.t.Fatalf("errors: %s", message.Payload)
		}
		raw, _ := json.Marshal(payload.Data.Live)
		return string(raw)
	case <-time.After(3 * time.Second):
		c.t.Fatal("no result arrived")
		return ""
	}
}

func (c *liveClient) quiet(what string) {
	c.t.Helper()
	select {
	case message, open := <-c.messages:
		if open {
			c.t.Fatalf("%s: %s %s", what, message.Type, message.Payload)
		}
	case <-time.After(250 * time.Millisecond):
	}
}

func waitFor(t *testing.T, what string, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal(what)
	}
}

// The row a paused refresh holds never reappears once a newer result has removed it.
func TestRevocationDiscardsAPausedRefresh(t *testing.T) {
	l := newLiveFixture(t)
	client := l.connect()
	if got := client.next(); !strings.Contains(got, `"id":"7"`) {
		t.Fatalf("first read: %s", got)
	}
	// A data change starts a refresh that reads the visible row and stalls.
	hold := l.holdNextRead()
	l.commit("calls")
	waitFor(t, "the refresh never reached Database", hold.paused)

	// Access is revoked. The new result reaches the client while the old read waits.
	l.revoked.Store(true)
	l.commit("profiles")
	if got := client.next(); got != `[]` {
		t.Fatalf("after revocation: %s", got)
	}

	close(hold.release)
	waitFor(t, "the old read never returned", hold.done)
	client.quiet("the old read was delivered")

	// A later refresh re-authorises too.
	l.commit("calls")
	if got := client.next(); got != `[]` {
		t.Fatalf("a later refresh: %s", got)
	}
}

// The first read after a reconnect can stall the same way.
func TestRevocationDiscardsAPausedReadOnReconnect(t *testing.T) {
	l := newLiveFixture(t)
	first := l.connect()
	if got := first.next(); !strings.Contains(got, `"id":"7"`) {
		t.Fatalf("first read: %s", got)
	}
	first.conn.Close()

	hold := l.holdNextRead()
	second := l.connect()
	waitFor(t, "the reconnect never read", hold.paused)

	l.revoked.Store(true)
	l.commit("profiles")
	if got := second.next(); got != `[]` {
		t.Fatalf("after revocation: %s", got)
	}
	close(hold.release)
	waitFor(t, "the old read never returned", hold.done)
	second.quiet("the reconnect's old read was delivered")
}

// A reconnect also re-reads the grants instead of trusting the old connection.
func TestReconnectReauthorises(t *testing.T) {
	l := newLiveFixture(t)
	first := l.connect()
	if got := first.next(); !strings.Contains(got, `"id":"7"`) {
		t.Fatalf("first read: %s", got)
	}
	first.conn.Close()
	l.revoked.Store(true)
	if got := l.connect().next(); got != `[]` {
		t.Fatalf("reconnect after revocation: %s", got)
	}
}

// One connection's stale read can't reach a connection that joined after it started.
func TestReconnectIgnoresARefreshOlderThanIt(t *testing.T) {
	l := newLiveFixture(t)
	first := l.connect()
	first.next()
	hold := l.holdNextRead()
	l.commit("calls")
	waitFor(t, "the refresh never reached Database", hold.paused)

	l.revoked.Store(true)
	second := l.connect()
	if got := second.next(); got != `[]` {
		t.Fatalf("second connection: %s", got)
	}
	// The commit is delivered late, after the older read has finished.
	close(hold.release)
	waitFor(t, "the old read never returned", hold.done)
	second.quiet("the older refresh reached the newer connection")
	// The older connection may see that older read, then the revocation, in order.
	if got := first.next(); !strings.Contains(got, `"id":"7"`) {
		t.Fatalf("first connection: %s", got)
	}
	l.commit("profiles")
	if got := first.next(); got != `[]` {
		t.Fatalf("first connection after revocation: %s", got)
	}
	if got := second.next(); got != `[]` {
		t.Fatalf("second connection after revocation: %s", got)
	}
}

// Linked data refreshes the row, and Database is asked about every source.
func TestLinkedDataAndGrantSourcesTriggerARefresh(t *testing.T) {
	l := newLiveFixture(t)
	client := l.connect()
	before := client.next()
	for _, collection := range []string{"prospects", "sales", "offers", "grants", "profiles", "calls"} {
		l.commit(collection)
		got := client.next()
		if got == before {
			t.Errorf("%s did not refresh: %s", collection, got)
		}
		before = got
	}
	l.fixture.db.mu.Lock()
	defer l.fixture.db.mu.Unlock()
	if len(l.fixture.db.watches) == 0 {
		t.Fatal("no watch opened")
	}
	if got := fmt.Sprint(l.fixture.db.watches[0]["collections"]); got != "[calls grants offers profiles prospects sales]" {
		t.Errorf("watched: %s", got)
	}
}

// ---- how the group orders runs ----

// scriptedRuns hands each run to the test, which decides when it finishes.
type scriptedRuns struct {
	mu      sync.Mutex
	started chan *scriptedRun
}

type scriptedRun struct {
	number int
	ctx    context.Context
	finish chan executeResult
}

func newScriptedRuns() *scriptedRuns {
	return &scriptedRuns{started: make(chan *scriptedRun, 64)}
}

func (s *scriptedRuns) run(ctx context.Context) (executeResult, error) {
	s.mu.Lock()
	run := &scriptedRun{number: len(s.started) + 1, ctx: ctx, finish: make(chan executeResult, 1)}
	s.mu.Unlock()
	s.started <- run
	return <-run.finish, nil
}

func (s *scriptedRuns) next(t *testing.T) *scriptedRun {
	t.Helper()
	select {
	case run := <-s.started:
		return run
	case <-time.After(2 * time.Second):
		t.Fatal("no run started")
		return nil
	}
}

func (s *scriptedRuns) none(t *testing.T, what string) {
	t.Helper()
	select {
	case <-s.started:
		t.Fatal(what)
	case <-time.After(100 * time.Millisecond):
	}
}

func groupResult(value string) executeResult {
	return executeResult{Data: map[string]any{"v": value}}
}

func noDelivery(t *testing.T, member *groupMember, what string) {
	t.Helper()
	select {
	case delivery := <-member.mailbox:
		t.Fatalf("%s: %v", what, delivery.result.Data)
	case <-time.After(100 * time.Millisecond):
	}
}

func joinScripted(t *testing.T, authority ...string) (*App, *scriptedRuns, *groupMember) {
	t.Helper()
	a := &App{hub: newSubscriptionHub()}
	t.Cleanup(a.hub.close)
	runs := newScriptedRuns()
	member, leave := a.joinSubscriptionGroupWatching(context.Background(), "k", "p1", "default", groupTopic, authority, runs.run)
	t.Cleanup(leave)
	return a, runs, member
}

func changed(a *App, collections ...string) {
	a.hub.publishAllAPIs("p1", groupTopic, map[string]any{"collections": collections})
}

// Writes to a busy table replace a running refresh only so many times, then a
// result gets out, so constant writes can't starve delivery.
func TestConstantWritesCannotStarveDelivery(t *testing.T) {
	a, runs, member := joinScripted(t, "profiles")
	changed(a, "calls")
	run := runs.next(t)
	for range maxGroupDataReplacements {
		changed(a, "calls")
		replacement := runs.next(t)
		select {
		case <-run.ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("the replaced run was not cancelled")
		}
		run.finish <- groupResult("replaced")
		run = replacement
	}
	noDelivery(t, member, "a replaced run was delivered")

	// The limit is reached: this write waits for the running refresh.
	changed(a, "calls")
	runs.none(t, "a data write replaced the refresh past the limit")
	if run.ctx.Err() != nil {
		t.Fatal("the refresh was cancelled past the limit")
	}
	run.finish <- groupResult("kept")
	if got := receive(t, member).result.Data["v"]; got != "kept" {
		t.Fatalf("delivered %v", got)
	}
	// The change that waited now gets its own refresh, and the count starts over.
	run = runs.next(t)
	changed(a, "calls")
	replacement := runs.next(t)
	run.finish <- groupResult("replaced")
	replacement.finish <- groupResult("latest")
	if got := receive(t, member).result.Data["v"]; got != "latest" {
		t.Fatalf("delivered %v", got)
	}
}

// A grant or profile write replaces a refresh whatever the limit says.
func TestGrantWritesReplaceARefreshPastTheDataLimit(t *testing.T) {
	a, runs, member := joinScripted(t, "profiles", "grants")
	changed(a, "calls")
	run := runs.next(t)
	for range maxGroupDataReplacements {
		changed(a, "calls")
		replacement := runs.next(t)
		run.finish <- groupResult("old")
		run = replacement
	}
	for i := range 5 {
		changed(a, "calls", "grants")
		replacement := runs.next(t)
		select {
		case <-run.ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("the stale run was not cancelled")
		}
		run.finish <- groupResult(fmt.Sprint("stale ", i))
		run = replacement
	}
	noDelivery(t, member, "a run read under old grants was delivered")
	run.finish <- groupResult("after the grant change")
	if got := receive(t, member).result.Data["v"]; got != "after the grant change" {
		t.Fatalf("delivered %v", got)
	}
}

// A refresh finishing while the first read gives way must not be lost: whichever way
// they interleave, the subscriber's last result is the newest.
func TestFirstReadNeverLosesANewerResult(t *testing.T) {
	older, newer := groupDelivery{result: groupResult("older")}, groupDelivery{result: groupResult("newer")}
	for range 3000 {
		g := &subscriptionGroup{members: map[*groupMember]struct{}{}}
		m := &groupMember{mailbox: make(chan groupDelivery, 1), group: g}
		g.members[m] = struct{}{}
		m.mailbox <- older
		first := make(chan executeResult, 1)
		start := make(chan struct{})
		go func() {
			<-start
			result, _, _ := firstSubscriptionRead(context.Background(), m, func(ctx context.Context) (executeResult, error) {
				<-ctx.Done()
				return executeResult{}, ctx.Err()
			})
			first <- result
		}()
		delivered := make(chan struct{})
		go func() {
			<-start
			g.deliver(1, newer)
			close(delivered)
		}()
		close(start)
		last := (<-first).Data["v"]
		<-delivered
		select {
		case delivery := <-m.mailbox:
			last = delivery.result.Data["v"]
		default:
		}
		if last != "newer" {
			t.Fatalf("the subscriber ended on %v", last)
		}
	}
}

// A busy profile table can't silence a subscriber: at the limit a refresh gets out,
// and the change it may have missed starts another one at once.
func TestGrantWritesCannotStarveDelivery(t *testing.T) {
	a, runs, member := joinScripted(t, "profiles")
	changed(a, "profiles")
	run := runs.next(t)
	for range maxGroupGrantReplacements {
		changed(a, "profiles")
		replacement := runs.next(t)
		select {
		case <-run.ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("the replaced run was not cancelled")
		}
		run.finish <- groupResult("replaced")
		run = replacement
	}
	changed(a, "profiles")
	select {
	case <-run.ctx.Done():
		t.Fatal("the limit did not hold")
	case <-time.After(100 * time.Millisecond):
	}
	noDelivery(t, member, "nothing finished yet")
	run.finish <- groupResult("at the limit")
	if got := receive(t, member).result.Data["v"]; got != "at the limit" {
		t.Fatalf("delivered %v", got)
	}
	next := runs.next(t)
	next.finish <- groupResult("covers the last change")
	if got := receive(t, member).result.Data["v"]; got != "covers the last change" {
		t.Fatalf("delivered %v", got)
	}
}

// A refresh started for several changes may only share an execution newer than the
// last of them. The oldest would let it reuse a read from before that change.
func TestARefreshOnlySharesReadsNewerThanItsLastChange(t *testing.T) {
	a, runs, member := joinScripted(t, "profiles")
	changed(a, "calls")
	run := runs.next(t)
	for range maxGroupDataReplacements {
		changed(a, "calls")
		replacement := runs.next(t)
		run.finish <- groupResult("replaced")
		run = replacement
	}
	changed(a, "calls")
	time.Sleep(50 * time.Millisecond)
	last := time.Now()
	changed(a, "calls")
	time.Sleep(50 * time.Millisecond)
	run.finish <- groupResult("at the limit")
	receive(t, member)
	next := runs.next(t)
	shared, _ := next.ctx.Value(sharedAfterKey{}).(time.Time)
	if shared.Before(last) {
		t.Errorf("may share a read from %s, before the last change at %s", shared, last)
	}
	next.finish <- groupResult("done")
}

// A subscription runs its released plan, so it watches what that plan reads, however
// the draft has changed since.
func TestSubscriptionWatchFollowsTheActiveRelease(t *testing.T) {
	l := newLiveFixture(t)
	draft := strings.Replace(livePlan, `"optional": true, "otherwise": {"collection": "grants"`, `"optional": true, "otherwise": {"collection": "other_grants"`, 1)
	if draft == livePlan {
		t.Fatal("the draft is the same")
	}
	draft = strings.Replace(draft, `{"as": "o", "collection": "offers", "kind": "left", "on": [{"local": "s.offer_id", "foreign": "id"}]}`, `{"as": "o", "collection": "other_offers", "kind": "left", "on": [{"local": "s.offer_id", "foreign": "id"}]}`, 1)
	if _, err := upsertResolverForAPI(l.fixture.app.ctx.AppDB(), "p1", "default", "Subscription", "live", databasePipelineOperation, l.fixture.source, decodeConfig(t, draft)); err != nil {
		t.Fatal(err)
	}
	client := l.connect()
	before := client.next()
	l.commit("grants")
	if got := client.next(); got == before {
		t.Errorf("the released plan's grant source did not refresh: %s", got)
	}
	l.fixture.db.mu.Lock()
	defer l.fixture.db.mu.Unlock()
	if got := fmt.Sprint(l.fixture.db.watches[0]["collections"]); got != "[calls grants offers profiles prospects sales]" {
		t.Errorf("watched: %s", got)
	}
}

// Replaced reads that won't unwind are capped; the newest change waits for one of
// them, and nothing old is delivered meanwhile.
func TestStuckReplacedReadsAreBounded(t *testing.T) {
	a, runs, member := joinScripted(t, "profiles")
	stuck := []*scriptedRun{}
	changed(a, "profiles")
	stuck = append(stuck, runs.next(t))
	for len(stuck) < maxGroupLiveRuns {
		changed(a, "profiles")
		stuck = append(stuck, runs.next(t))
	}
	changed(a, "profiles")
	runs.none(t, "past the live-run limit")
	noDelivery(t, member, "an old read was delivered")
	stuck[0].finish <- groupResult("stale")
	fresh := runs.next(t)
	noDelivery(t, member, "the released read was delivered")
	fresh.finish <- groupResult("fresh")
	if got := receive(t, member).result.Data["v"]; got != "fresh" {
		t.Fatalf("delivered %v", got)
	}
	for _, run := range stuck[1:] {
		run.finish <- groupResult("stale")
	}
	noDelivery(t, member, "a released read was delivered")
}

// An event that can't say what changed may have been about grants, and so may one
// that was dropped because the coalescing buffer was full.
func TestUnknownOrDroppedEventsCountAsGrantChanges(t *testing.T) {
	g := &subscriptionGroup{authority: map[string]bool{"profiles": true}}
	for name, c := range map[string]struct {
		event map[string]any
		want  bool
	}{
		"names a grant source":  {map[string]any{"collections": []string{"calls", "profiles"}}, true},
		"decoded names":         {map[string]any{"collections": []any{"profiles"}}, true},
		"names only other data": {map[string]any{"collections": []string{"calls"}}, false},
		"doesn't say":           {map[string]any{}, true},
		"a retry, not a change": {nil, false},
	} {
		if got := g.touchesAuthority(c.event); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
	if (&subscriptionGroup{}).touchesAuthority(map[string]any{}) {
		t.Error("a plan with no grant sources has no grant changes")
	}

	a, runs, member := joinScripted(t, "profiles")
	changed(a, "calls")
	run := runs.next(t)
	for range maxGroupDataReplacements {
		changed(a, "calls")
		replacement := runs.next(t)
		run.finish <- groupResult("old")
		run = replacement
	}
	// A write that was dropped might have named a grant source, so it replaces the
	// refresh even past the limit.
	a.hub.mu.Lock()
	for sub := range a.hub.subs[a.hub.apiKey("p1", "default", groupTopic)] {
		sub.dropped.Store(true)
	}
	a.hub.mu.Unlock()
	changed(a, "calls")
	replacement := runs.next(t)
	run.finish <- groupResult("old")
	noDelivery(t, member, "a run that may predate a grant change was delivered")
	replacement.finish <- groupResult("new")
	if got := receive(t, member).result.Data["v"]; got != "new" {
		t.Fatalf("delivered %v", got)
	}

	// The hub reports what it drops.
	sub, leave := a.hub.subscribeCoalescedForAPI("p1", "default", "other")
	defer leave()
	a.hub.publishAllAPIs("p1", "other", map[string]any{"n": 1})
	a.hub.publishAllAPIs("p1", "other", map[string]any{"n": 2})
	if !sub.dropped.Load() {
		t.Error("the hub dropped an event without saying so")
	}
}

// A member's own first read is only skipped when a newer refresh will reach it.
func TestOvertakenOnlyAfterARefreshStartsSinceJoining(t *testing.T) {
	a, runs, first := joinScripted(t)
	if first.overtaken() {
		t.Fatal("nothing has started")
	}
	changed(a, "calls")
	running := runs.next(t)
	if !first.overtaken() {
		t.Fatal("a refresh started after the member joined")
	}
	second, leave := a.joinSubscriptionGroupWatching(context.Background(), "k", "p1", "default", groupTopic, nil, runs.run)
	defer leave()
	if second.overtaken() {
		t.Fatal("that refresh is older than this member")
	}
	running.finish <- groupResult("older")
	if got := receive(t, first).result.Data["v"]; got != "older" {
		t.Fatalf("first member got %v", got)
	}
	noDelivery(t, second, "an older refresh reached a newer member")
}

func TestFirstReadGivesWayToANewerRefresh(t *testing.T) {
	a, runs, member := joinScripted(t)
	reading := make(chan context.Context, 1)
	type answer struct {
		result executeResult
		send   bool
		err    error
	}
	answers := make(chan answer, 1)
	go func() {
		result, send, err := firstSubscriptionRead(context.Background(), member, func(ctx context.Context) (executeResult, error) {
			reading <- ctx
			<-ctx.Done()
			return groupResult("old"), nil
		})
		answers <- answer{result, send, err}
	}()
	readCtx := <-reading
	changed(a, "calls")
	runs.next(t).finish <- groupResult("new")
	got := <-answers
	// The newer result becomes the first one. It isn't put back in the mailbox, where
	// a refresh finishing in between could replace it with nothing to show for it.
	if !got.send || got.err != nil || got.result.Data["v"] != "new" {
		t.Fatalf("the newer result should go first: %+v", got)
	}
	if readCtx.Err() == nil {
		t.Error("the old read was left running")
	}
	noDelivery(t, member, "the first result was delivered twice")

	// A read that finishes while a newer refresh runs isn't sent; the refresh's result is.
	a2, runs2, overtaken := joinScripted(t)
	release2 := make(chan struct{})
	reading2 := make(chan struct{})
	answers2 := make(chan answer, 1)
	go func() {
		result, send, err := firstSubscriptionRead(context.Background(), overtaken, func(context.Context) (executeResult, error) {
			close(reading2)
			<-release2
			return groupResult("old"), nil
		})
		answers2 <- answer{result, send, err}
	}()
	<-reading2
	changed(a2, "calls")
	refresh := runs2.next(t)
	close(release2)
	if got := <-answers2; got.send || got.err != nil {
		t.Fatalf("the read was kept while a newer refresh ran: %+v", got)
	}
	refresh.finish <- groupResult("newer")
	if delivered := receive(t, overtaken).result.Data["v"]; delivered != "newer" {
		t.Fatalf("delivered %v", delivered)
	}

	// A failed refresh doesn't take the place of the read.
	failing := &groupMember{mailbox: make(chan groupDelivery, 1), group: &subscriptionGroup{}}
	failing.mailbox <- groupDelivery{err: fmt.Errorf("refresh failed")}
	release := make(chan struct{})
	go func() { time.Sleep(50 * time.Millisecond); close(release) }()
	result, send, err := firstSubscriptionRead(context.Background(), failing, func(context.Context) (executeResult, error) {
		<-release
		return groupResult("first"), nil
	})
	if !send || err != nil || result.Data["v"] != "first" {
		t.Fatalf("got %v %v %v", result.Data, send, err)
	}

	// A read that fails still ends the subscription, as before.
	if _, send, err := firstSubscriptionRead(context.Background(), &groupMember{mailbox: make(chan groupDelivery, 1), group: &subscriptionGroup{}}, func(context.Context) (executeResult, error) {
		return executeResult{}, fmt.Errorf("denied")
	}); send || err == nil {
		t.Fatalf("a failed first read: %v %v", send, err)
	}
	// With no group, the read is simply the result.
	if result, send, err := firstSubscriptionRead(context.Background(), nil, func(context.Context) (executeResult, error) {
		return groupResult("plain"), nil
	}); !send || err != nil || result.Data["v"] != "plain" {
		t.Fatalf("no group: %v %v %v", result.Data, send, err)
	}
}
