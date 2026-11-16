package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const pid = "proj-1"

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(1)", strings.ReplaceAll(t.Name(), "/", "_")))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	migrate(t, db, "")
	return db
}

// migrate applies the migrations in order, up to and including `upto`
// ("" = all), the way the SDK runner does: foreign keys off during the
// run, then checked.
func migrate(t *testing.T, db *sql.DB, upto string) {
	t.Helper()
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	applied := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name='_test_migrations'`)
	if err == nil {
		has := rows.Next()
		rows.Close()
		if has {
			r2, _ := db.Query(`SELECT filename FROM _test_migrations`)
			for r2.Next() {
				var f string
				_ = r2.Scan(&f)
				applied[f] = true
			}
			r2.Close()
		}
	}
	mustExec := func(q string) {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`CREATE TABLE IF NOT EXISTS _test_migrations(filename TEXT PRIMARY KEY)`)
	mustExec(`PRAGMA foreign_keys=OFF`)
	for _, f := range files {
		name := filepath.Base(f)
		if applied[name] {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		mustExec(`INSERT INTO _test_migrations VALUES('` + name + `')`)
		if name == upto {
			break
		}
	}
	mustExec(`PRAGMA foreign_keys=ON`)
	check, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	if check.Next() {
		t.Fatal("migrations left foreign key violations")
	}
}

// Monday 16 Nov 2026, 08:00 UTC.
var testNow = time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)

func fixClock(t *testing.T, at time.Time) {
	old := clock
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = old })
}

func install(t *testing.T, db *sql.DB, key string) {
	t.Helper()
	packs, err := builtinPacks()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installPack(db, pid, packs[key], 0); err != nil {
		t.Fatal(err)
	}
}

// must panics on error; a panic fails the running test with a stack trace.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func ids(xs ...int64) []any {
	out := []any{}
	for _, x := range xs {
		out = append(out, float64(x))
	}
	return out
}

func shift(t *testing.T, db *sql.DB, resID int64, start, end string) {
	t.Helper()
	must(addBlocks(db, pid, resID, []any{map[string]any{"kind": "shift", "starts_at": start, "ends_at": end}}, "", ""))
}

func TestBuiltinPacksValidate(t *testing.T) {
	db := testDB(t)
	packs := must(builtinPacks())
	for _, k := range []string{"plumber", "salon", "restaurant", "cleaning"} {
		p, ok := packs[k]
		if !ok {
			t.Fatalf("pack %s missing", k)
		}
		if errs := validatePack(db, pid, &p, 0); len(errs) > 0 {
			t.Fatalf("pack %s invalid: %v", k, errs)
		}
	}
	// All three install side by side in one project: nothing is trade-specific.
	for _, k := range []string{"plumber", "salon", "restaurant", "cleaning"} {
		install(t, db, k)
	}
	wts := must(listWorkTypes(db, pid, 0))
	if len(wts) < 14 {
		t.Fatalf("expected the three packs' work types, got %d", len(wts))
	}
}

func TestFormula(t *testing.T) {
	f := must(parseFormula("max(rooms * 1800 + bathrooms * 2500, 6000) + ceil(qty / 2) * 100"))
	v := must(f(map[string]float64{"rooms": 3, "bathrooms": 1, "qty": 3}))
	if v != 8100 {
		t.Fatalf("formula = %v, want 8100", v)
	}
	if _, err := f(map[string]float64{"rooms": 3}); err == nil || !strings.Contains(err.Error(), "bathrooms") {
		t.Fatalf("missing answer must error, got %v", err)
	}
	for _, bad := range []string{"", "1 +", "foo(1)", "(1", "2 ** 3"} {
		if _, err := parseFormula(bad); err == nil {
			t.Fatalf("formula %q should not parse", bad)
		}
	}
}

func TestPlumberEmergencyFlow(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow)
	install(t, db, "plumber")
	p1 := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Sam", "quals": []any{"plumber"}, "home_lat": 48.85, "home_lng": 2.35, "cost_rate_cents": 3000}))
	p2 := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Lea", "quals": []any{"plumber"}, "home_lat": 48.90, "home_lng": 2.45}))
	shift(t, db, p1.ID, "2026-11-16T08:00:00Z", "2026-11-16T18:00:00Z")
	shift(t, db, p2.ID, "2026-11-16T08:00:00Z", "2026-11-16T18:00:00Z")

	res := must(intakeWork(db, pid, nil, map[string]any{
		"work_type": "emergency", "summary": "Burst pipe flooding the kitchen",
		"customer": map[string]any{"name": "Ms Durand", "email": "durand@example.com", "phone": "+33600000000"},
		"address":  "12 rue X, Paris", "lat": 48.851, "lng": 2.351,
		"answers": map[string]any{"issue": "burst_pipe", "description": "water everywhere", "water_shut_off": true},
		"items":   []any{"CALLOUT_EMERGENCY", "LEAK_REPAIR"},
	}))
	w := res.Work
	if w.Priority != "p1" || w.SLADueAt == "" || w.Phase != "intake" || w.SiteID == nil {
		t.Fatalf("bad intake: %+v", w)
	}
	if res.Quote.TotalCents != 29900 || res.Quote.DurationMin != 90 {
		t.Fatalf("price = %d, duration %d", res.Quote.TotalCents, res.Quote.DurationMin)
	}

	wt := must(getWorkType(db, pid, w.WorkTypeID, ""))
	opts, _ := must2(suggestSlots(db, pid, w, wt, suggestParams{Limit: 3}))
	if len(opts) == 0 || opts[0].Resources[0].ID != p1.ID {
		t.Fatalf("expected Sam (nearest) first, got %+v", opts)
	}
	br := must(bookSlot(db, pid, w, map[string]any{"starts_at": opts[0].StartsAt}))
	if br.Work.Phase != "execute" || br.Work.Status != "scheduled" {
		t.Fatalf("booking should move work to execute/scheduled, got %s/%s", br.Work.Phase, br.Work.Status)
	}
	// Sam is now busy at that time (with travel buffer): next suggestion uses Lea.
	w2 := must(intakeWork(db, pid, nil, map[string]any{"work_type": "emergency", "summary": "leak under sink", "address": "1 rue Y", "lat": 48.86, "lng": 2.36,
		"answers": map[string]any{"issue": "leak", "description": "drip"}})).Work
	opts2, _ := must2(suggestSlots(db, pid, w2, wt, suggestParams{From: mustParse(opts[0].StartsAt), Exact: true}))
	if len(opts2) != 1 || opts2[0].Resources[0].ID != p2.ID {
		t.Fatalf("expected Lea at the same time, got %+v", opts2)
	}

	// Completion is refused until the job report is in, and nothing changes.
	_, err := completeWork(db, pid, br.Work, map[string]any{})
	var ge *GuardError
	if !errors.As(err, &ge) || !strings.Contains(ge.Error(), "job_report") {
		t.Fatalf("want job_report guard, got %v", err)
	}
	if countSlots(db, w.ID, "booked") != 1 {
		t.Fatal("refused completion must leave the slot booked")
	}
	if _, err := submitForm(db, pid, map[string]any{"form_key": "job_report", "work_id": float64(w.ID), "data": map[string]any{"diagnosis": "split joint"}}); err == nil {
		t.Fatal("incomplete job report must be rejected")
	}
	must(submitForm(db, pid, map[string]any{"form_key": "job_report", "work_id": float64(w.ID),
		"data": map[string]any{"diagnosis": "split joint", "work_done": "replaced joint", "after_photos": []any{float64(77)}}}))
	cr := must(completeWork(db, pid, br.Work, map[string]any{"items": []any{"CALLOUT_EMERGENCY", "LEAK_REPAIR", map[string]any{"code": "LABOUR_HOUR", "qty": 0.5}}}))
	if cr.Work.Phase != "bill" || cr.Actual.TotalCents != 29900+3250 {
		t.Fatalf("complete: phase %s total %d", cr.Work.Phase, cr.Actual.TotalCents)
	}
	if len(cr.Materials) != 1 || cr.Materials[0].SKU != "PIPE-FITTING-15MM" {
		t.Fatalf("materials = %+v", cr.Materials)
	}
	// Billing app unreachable: must say so; external ref records it.
	if _, _, err := invoiceWork(db, pid, nil, cr.Work, map[string]any{}); err == nil {
		t.Fatal("invoice without billing should error")
	}
	nw, info := must2(invoiceWork(db, pid, nil, cr.Work, map[string]any{"external_ref": "INV-1"}))
	if nw.Status != "invoiced" || info["total_cents"].(int64) != 33150 {
		t.Fatalf("invoice: %s %v", nw.Status, info)
	}
	tr := must(transition(db, pid, nw, "closed", "owner", "paid", nil))
	if tr.Work.Phase != "closed" || tr.Event != "work.closed" {
		t.Fatalf("close: %+v", tr)
	}
	ev := must(listEvents(db, pid, w.ID))
	if len(ev) < 7 {
		t.Fatalf("timeline too short: %d", len(ev))
	}
}

func must2[A, B any](a A, b B, err error) (A, B) {
	if err != nil {
		panic(err)
	}
	return a, b
}

func TestQuotePolicyNeedsOwnerAndCrew(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow)
	install(t, db, "plumber")
	sam := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Sam", "quals": []any{"plumber"}}))
	shift(t, db, sam.ID, "2026-11-17T08:00:00Z", "2026-11-17T18:00:00Z")

	w := must(intakeWork(db, pid, nil, map[string]any{"work_type": "install", "summary": "new water heater", "address": "5 av Z",
		"answers": map[string]any{"issue": "install", "description": "200L heater"}})).Work
	wt := must(getWorkType(db, pid, w.WorkTypeID, ""))
	pr := must(buildEstimate(db, pid, w, wt, []ItemRequest{{Code: "WATER_HEATER_INSTALL"}, {Code: "LABOUR_HOUR", Qty: ptr(5)}}, map[string]any{}))
	if !pr.NeedsOwner || !strings.HasPrefix(pr.Flags[0], "above_auto_approve") {
		t.Fatalf("expected owner flag, got %+v", pr.Flags)
	}
	w = must(getWork(db, pid, w.ID))
	if _, _, err := quoteDecision(db, pid, w, "accept", "customer", ""); err == nil {
		t.Fatal("accept must be refused while the quote needs the owner")
	}
	if _, err := bookSlot(db, pid, w, map[string]any{"starts_at": "2026-11-17T09:00:00Z"}); err == nil {
		t.Fatal("booking before acceptance must fail")
	}
	w, _ = must2(quoteDecision(db, pid, w, "approve", "owner", "ok"))
	w, _ = must2(quoteDecision(db, pid, w, "send", "agent", ""))
	if w.Status != "quote_sent" {
		t.Fatalf("status after send = %s", w.Status)
	}
	w, _ = must2(quoteDecision(db, pid, w, "accept", "customer", ""))
	if w.Phase != "schedule" {
		t.Fatalf("accepted quote should move to schedule, got %s", w.Phase)
	}
	// The install needs two plumbers: one is not enough.
	opts, reqs := must2(suggestSlots(db, pid, w, wt, suggestParams{From: mustParse("2026-11-17T08:00:00Z"), To: mustParse("2026-11-17T18:00:00Z")}))
	if len(opts) != 0 || reqs[0].Count != 2 {
		t.Fatalf("expected no slot with one plumber, reqs %+v opts %d", reqs, len(opts))
	}
	lea := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Lea", "quals": []any{"plumber"}}))
	shift(t, db, lea.ID, "2026-11-17T08:00:00Z", "2026-11-17T18:00:00Z")
	opts, _ = must2(suggestSlots(db, pid, w, wt, suggestParams{From: mustParse("2026-11-17T08:00:00Z"), To: mustParse("2026-11-17T18:00:00Z")}))
	if len(opts) == 0 || len(opts[0].Resources) != 2 || opts[0].StartsAt != "2026-11-17T08:00:00Z" {
		t.Fatalf("expected a two-person slot at 08:00, got %+v", opts)
	}
}

func TestSalonAppointments(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow)
	install(t, db, "salon")
	salon := must(upsertSite(db, pid, map[string]any{"kind": "own", "name": "Salon", "timezone": "Europe/Paris",
		"hours": map[string]any{"tue": []any{[]any{"09:00", "19:00"}}, "wed": []any{[]any{"09:00", "19:00"}}}}))
	sid := float64(salon.ID)
	for _, n := range []string{"Chair 1", "Chair 2"} {
		must(upsertResource(db, pid, map[string]any{"kind": "space", "name": n, "zone": "chairs", "site_id": sid}))
	}
	ana := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Ana", "quals": []any{"cut", "colour"}, "site_id": sid}))
	ben := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Ben", "quals": []any{"cut"}, "site_id": sid}))
	cleo := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Cleo", "quals": []any{"cut"}, "site_id": sid}))
	for _, r := range []*Resource{ana, ben, cleo} {
		shift(t, db, r.ID, "2026-11-17T08:00:00Z", "2026-11-17T18:00:00Z") // Tue 09:00–19:00 Paris
	}
	book := func(items []any, at string, flags []any) (*Work, *BookResult, error) {
		res, err := intakeWork(db, pid, nil, map[string]any{"work_type": "appointment", "customer": map[string]any{"name": "Client"}, "items": items})
		if err != nil {
			return nil, nil, err
		}
		if flags != nil {
			wt := must(getWorkType(db, pid, res.Work.WorkTypeID, ""))
			must(buildEstimate(db, pid, res.Work, wt, parseItems(items), map[string]any{"flags": flags}))
		}
		br, err := bookSlot(db, pid, res.Work, map[string]any{"starts_at": at})
		return res.Work, br, err
	}
	// Cut + colour needs one person with both skills → Ana, for 135 min, on a chair.
	w1, br1, err := book([]any{"CUT_LONG", "COLOUR_ROOTS"}, "2026-11-17T09:00:00Z", nil)
	if err != nil {
		t.Fatal(err)
	}
	if w1.SiteID == nil || *w1.SiteID != salon.ID {
		t.Fatal("premises work should default to the only own site")
	}
	got := map[string]bool{}
	for _, r := range br1.Slot.Resources {
		got[r.Name] = true
	}
	if !got["Ana"] || len(br1.Slot.Resources) != 2 || br1.Slot.EndsAt != "2026-11-17T11:15:00Z" {
		t.Fatalf("slot = %+v", br1.Slot)
	}
	// Same time, a cut: Ana is busy, Ben takes it on the second chair.
	_, br2, err := book([]any{"CUT_SHORT"}, "2026-11-17T09:00:00Z", nil)
	if err != nil || br2.Slot.Resources[1].Name != "Ben" && br2.Slot.Resources[0].Name != "Ben" {
		t.Fatalf("second booking: %v %+v", err, br2)
	}
	// Third client at the same time: Cleo is free but no chair is.
	if _, _, err := book([]any{"CUT_SHORT"}, "2026-11-17T09:00:00Z", nil); err == nil {
		t.Fatal("no chair left: booking must fail")
	}
	// Closed on Monday.
	if _, _, err := book([]any{"CUT_SHORT"}, "2026-11-16T10:00:00Z", nil); err == nil {
		t.Fatal("salon is closed on Monday")
	}
	// Student discount applies through a flag.
	w4, _, err := book([]any{"CUT_SHORT"}, "2026-11-17T12:00:00Z", []any{"student"})
	if err != nil {
		t.Fatal(err)
	}
	w4 = must(getWork(db, pid, w4.ID))
	if w4.EstimateCents != 3150 {
		t.Fatalf("student price = %d, want 3150", w4.EstimateCents)
	}
	// Visit done, paid at the desk → closed.
	cr := must(completeWork(db, pid, br1.Work, map[string]any{}))
	if cr.Work.Phase != "bill" {
		t.Fatalf("phase = %s", cr.Work.Phase)
	}
	if _, err := transition(db, pid, cr.Work, "closed", "desk", "", nil); err == nil {
		t.Fatal("cannot close unpaid")
	}
	if _, err := db.Exec(`UPDATE work SET paid_at=? WHERE id=?`, nowUTC(), w1.ID); err != nil {
		t.Fatal(err)
	}
	w1 = must(getWork(db, pid, w1.ID))
	if tr := must(transition(db, pid, w1, "closed", "desk", "", nil)); tr.Work.Phase != "closed" {
		t.Fatal("paid visit should close")
	}
}

func TestRestaurantReservationsTakeawayAndLogs(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow)
	install(t, db, "restaurant")
	days := map[string]any{}
	for _, d := range []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"} {
		days[d] = []any{[]any{"12:00", "14:30"}, []any{"19:00", "23:00"}}
	}
	r := must(upsertSite(db, pid, map[string]any{"kind": "own", "name": "Trattoria", "timezone": "UTC", "hours": days}))
	sid := float64(r.ID)
	tables := map[string]int64{}
	for name, seats := range map[string]int{"T2": 2, "T4": 4, "T6": 6} {
		tables[name] = must(upsertResource(db, pid, map[string]any{"kind": "space", "name": name, "quals": []any{"table"}, "zone": "dining", "capacity": seats, "site_id": sid})).ID
	}
	reserve := func(party int, at string) (*BookResult, error) {
		res, err := intakeWork(db, pid, nil, map[string]any{"work_type": "reservation", "customer": map[string]any{"name": "Guest", "phone": "+33611"},
			"answers": map[string]any{"party_size": party}})
		if err != nil {
			return nil, err
		}
		if res.Work.PartySize != party {
			t.Fatalf("party size from answers = %d", res.Work.PartySize)
		}
		return bookSlot(db, pid, res.Work, map[string]any{"starts_at": at})
	}
	b1 := must(reserve(3, "2026-11-16T19:00:00Z"))
	if b1.Slot.Resources[0].ID != tables["T4"] {
		t.Fatalf("party of 3 should get the 4-seat table, got %+v", b1.Slot.Resources)
	}
	b2 := must(reserve(4, "2026-11-16T19:00:00Z"))
	if b2.Slot.Resources[0].ID != tables["T6"] {
		t.Fatalf("party of 4 should get T6 when T4 is taken, got %+v", b2.Slot.Resources)
	}
	if _, err := reserve(5, "2026-11-16T19:30:00Z"); err == nil {
		t.Fatal("no table for 5 left")
	}
	if _, err := reserve(2, "2026-11-16T16:00:00Z"); err == nil {
		t.Fatal("closed between services")
	}
	// Table turn: T4 is free again after 105 min + 15 min buffer.
	if _, err := reserve(4, "2026-11-16T20:50:00Z"); err == nil {
		t.Fatal("T4/T6 still in their buffer at 20:50")
	}
	b5 := must(reserve(4, "2026-11-16T21:00:00Z"))
	if b5.Slot.Resources[0].ID != tables["T4"] {
		t.Fatalf("T4 should be free again at 21:00, got %+v", b5.Slot.Resources)
	}
	// Guests leave: closing the reservation needs no billing (POS handles it).
	cr := must(completeWork(db, pid, b1.Work, map[string]any{}))
	if cr.Work.Phase != "closed" {
		t.Fatalf("reservation should close on completion, got %s", cr.Work.Phase)
	}

	// Takeaway: a cook and the oven; two pizzas take 8 minutes.
	cook := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Marco", "quals": []any{"cook"}, "site_id": sid}))
	shift(t, db, cook.ID, "2026-11-16T18:30:00Z", "2026-11-16T23:30:00Z")
	must(upsertResource(db, pid, map[string]any{"kind": "equipment", "name": "Pizza oven", "quals": []any{"oven"}, "site_id": sid}))
	order := must(intakeWork(db, pid, nil, map[string]any{"work_type": "takeaway_order", "customer": map[string]any{"name": "Paul"},
		"items": []any{map[string]any{"code": "PIZZA_MARGHERITA", "qty": 1}, map[string]any{"code": "PIZZA_DIAVOLA", "qty": 1}}}))
	if order.Quote.TotalCents != 2400 || order.Quote.DurationMin != 8 {
		t.Fatalf("order price %d duration %d", order.Quote.TotalCents, order.Quote.DurationMin)
	}
	fixClock(t, time.Date(2026, 11, 16, 19, 2, 0, 0, time.UTC))
	wt := must(getWorkType(db, pid, order.Work.WorkTypeID, ""))
	opts, _ := must2(suggestSlots(db, pid, order.Work, wt, suggestParams{StepMin: 5, Limit: 1}))
	if len(opts) == 0 || opts[0].StartsAt != "2026-11-16T19:05:00Z" || opts[0].EndsAt != "2026-11-16T19:13:00Z" {
		t.Fatalf("pickup slot = %+v", opts)
	}

	// Food-safety log on the site, validated.
	if _, err := submitForm(db, pid, map[string]any{"form_key": "temperature_log", "site_id": sid,
		"data": map[string]any{"unit": "Fridge 1", "temp_c": 35, "within_limits": false}}); err == nil {
		t.Fatal("35°C is outside the field's range")
	}
	must(submitForm(db, pid, map[string]any{"form_key": "temperature_log", "site_id": sid,
		"data": map[string]any{"unit": "Fridge 1", "temp_c": 3.5, "within_limits": true}}))
	logs := must(listFormResponses(db, pid, map[string]any{"site_id": sid, "form_key": "temperature_log"}))
	if len(logs) != 1 {
		t.Fatalf("logs = %d", len(logs))
	}
}

func TestAgreementsGenerateAndBook(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow)
	install(t, db, "restaurant")
	site := must(upsertSite(db, pid, map[string]any{"kind": "own", "name": "Trattoria", "timezone": "Europe/Paris"}))
	opener := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Ines", "site_id": float64(site.ID)}))
	for d := 16; d <= 18; d++ {
		shift(t, db, opener.ID, fmt.Sprintf("2026-11-%02dT08:00:00Z", d), fmt.Sprintf("2026-11-%02dT16:00:00Z", d))
	}
	ag := must(createAgreement(db, pid, map[string]any{"name": "Opening", "work_type": "opening", "site_id": float64(site.ID),
		"recurrence": map[string]any{"unit": "day", "time": "10:00"}, "preferred_resource_ids": ids(opener.ID), "starts_on": "2026-11-16"}))
	occ := must(generateAgreements(db, pid, "2026-11-18", 0))
	if len(occ) != 3 {
		t.Fatalf("occurrences = %d", len(occ))
	}
	for _, o := range occ {
		if o.SlotID == 0 || o.BookError != "" {
			t.Fatalf("occurrence not booked: %+v", o)
		}
	}
	// 10:00 Paris = 09:00 UTC in November.
	s := must(getSlot(db, pid, occ[0].SlotID))
	if s.StartsAt != "2026-11-16T09:00:00Z" {
		t.Fatalf("slot time = %s", s.StartsAt)
	}
	// Idempotent: nothing new until the next day is due.
	if again := must(generateAgreements(db, pid, "2026-11-18", 0)); len(again) != 0 {
		t.Fatalf("re-run generated %d", len(again))
	}
	ag = must(getAgreement(db, pid, ag.ID))
	if ag.NextOn != "2026-11-19" {
		t.Fatalf("next_on = %s", ag.NextOn)
	}
	// Closing needs the checklist; the opening routine has no bill phase.
	w := must(getWork(db, pid, occ[0].WorkID))
	if _, err := completeWork(db, pid, w, map[string]any{}); err == nil {
		t.Fatal("opening checklist required")
	}
	must(submitForm(db, pid, map[string]any{"form_key": "opening_checklist", "work_id": float64(w.ID),
		"data": map[string]any{"fridges_checked": true, "surfaces_sanitised": true, "allergen_board": true}}))
	if cr := must(completeWork(db, pid, w, map[string]any{})); cr.Work.Phase != "closed" {
		t.Fatalf("phase = %s", cr.Work.Phase)
	}
}

func TestWeeklyRecurrence(t *testing.T) {
	r := Recurrence{Every: 2, Unit: "week", Weekdays: []int{2, 5}} // Tue + Fri, every other week
	if got := firstOccurrence("2026-11-16", r); got != "2026-11-17" {
		t.Fatalf("first = %s", got)
	}
	seq := []string{"2026-11-17"}
	for i := 0; i < 4; i++ {
		seq = append(seq, nextOccurrence(seq[len(seq)-1], r))
	}
	want := []string{"2026-11-17", "2026-11-20", "2026-12-01", "2026-12-04", "2026-12-15"}
	if strings.Join(seq, ",") != strings.Join(want, ",") {
		t.Fatalf("seq = %v, want %v", seq, want)
	}
}

func TestLifecycleGuards(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow)
	install(t, db, "plumber")
	w := must(intakeWork(db, pid, nil, map[string]any{"work_type": "repair", "address": "x", "answers": map[string]any{"issue": "tap", "description": "drip"}})).Work
	// Can't skip phases.
	if _, err := transition(db, pid, w, "scheduled", "agent", "", nil); err == nil {
		t.Fatal("intake → execute must be refused")
	}
	if _, err := transition(db, pid, w, "to_schedule", "agent", "", nil); err == nil {
		t.Fatal("repair needs the quote phase first")
	}
	// Hold and resume.
	tr := must(transition(db, pid, w, statusOnHold, "agent", "waiting for photos", nil))
	if tr.Work.HoldFromStatus != "new" {
		t.Fatalf("hold_from = %q", tr.Work.HoldFromStatus)
	}
	tr = must(transition(db, pid, tr.Work, "resume", "agent", "", nil))
	if tr.Work.Status != "new" {
		t.Fatalf("resumed to %s", tr.Work.Status)
	}
	tr = must(transition(db, pid, tr.Work, "cancelled", "customer", "fixed it myself", nil))
	if tr.Work.Phase != statusCancelled || tr.Work.ClosedAt == "" {
		t.Fatalf("cancel: %+v", tr.Work)
	}
	if _, err := transition(db, pid, tr.Work, "resume", "agent", "", nil); err == nil {
		t.Fatal("cancelled work cannot move")
	}
	m := must(metrics(db, pid, testNow.AddDate(0, 0, -1), testNow.AddDate(0, 0, 1), 0))
	if m["cancelled"].(int) != 1 {
		t.Fatalf("metrics = %+v", m)
	}
}

func ptr(f float64) *float64 { return &f }

func TestBrandsShareResources(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow)
	ff := must(upsertBrand(db, pid, map[string]any{"key": "flowfix", "name": "FlowFix", "number_prefix": "FF"}))
	bc := must(upsertBrand(db, pid, map[string]any{"key": "brightclean", "name": "BrightClean"}))
	if bc.NumberPrefix != "BC" && bc.NumberPrefix != "BRI" {
		t.Fatalf("derived prefix = %s", bc.NumberPrefix)
	}
	if _, err := upsertBrand(db, pid, map[string]any{"key": "other", "name": "Other", "number_prefix": "FF"}); err == nil {
		t.Fatal("duplicate prefix must be refused")
	}
	packs := must(builtinPacks())
	must(installPack(db, pid, packs["plumber"], ff.ID))
	must(installPack(db, pid, packs["cleaning"], bc.ID))
	// Sam does both trades for both brands; Max is limited to FlowFix.
	sam := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Sam", "quals": []any{"plumber", "cleaner"}}))
	max := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Max", "quals": []any{"plumber", "cleaner"}, "brands": []any{"flowfix"}}))
	shift(t, db, sam.ID, "2026-11-16T08:00:00Z", "2026-11-16T18:00:00Z")
	shift(t, db, max.ID, "2026-11-16T08:00:00Z", "2026-11-16T18:00:00Z")

	// The work type settles the brand when only one brand has it.
	clean := must(intakeWork(db, pid, nil, map[string]any{"work_type": "regular_clean", "address": "1 rue A",
		"answers": map[string]any{"property_type": "flat", "hours": 2}})).Work
	if clean.Brand != "brightclean" || !strings.HasPrefix(clean.Number, bc.NumberPrefix+"-") {
		t.Fatalf("clean brand %q number %s", clean.Brand, clean.Number)
	}
	leak := must(intakeWork(db, pid, nil, map[string]any{"work_type": "emergency", "address": "2 rue B",
		"answers": map[string]any{"issue": "leak", "description": "drip"}})).Work
	if leak.Number != "FF-00001" {
		t.Fatalf("leak number %s", leak.Number)
	}
	// Sam takes the FlowFix job at 09:00 ...
	must(bookSlot(db, pid, leak, map[string]any{"starts_at": "2026-11-16T09:00:00Z", "resource_ids": ids(sam.ID)}))
	// ... so the BrightClean job at 09:00 has nobody: Max is FlowFix only.
	if _, err := bookSlot(db, pid, clean, map[string]any{"starts_at": "2026-11-16T09:00:00Z"}); err == nil {
		t.Fatal("Max must not be booked for BrightClean, and Sam is busy")
	}
	// A brand passed explicitly that does not have the work type is refused.
	if _, err := intakeWork(db, pid, nil, map[string]any{"work_type": "regular_clean", "brand": "flowfix", "address": "x"}); err == nil {
		t.Fatal("flowfix has no regular_clean")
	}
	m := must(metrics(db, pid, testNow.AddDate(0, 0, -1), testNow.AddDate(0, 0, 1), 0))
	if bb, ok := m["by_brand"].([]map[string]any); !ok || len(bb) != 2 {
		t.Fatalf("by_brand = %v", m["by_brand"])
	}
}

func TestUpgradeFromV01KeepsData(t *testing.T) {
	db, err := sql.Open("sqlite", "file:upgrade?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	fixClock(t, testNow)
	migrate(t, db, "001_init.sql")
	// A 0.1 project: pack, policy, a job and an agreement, written with
	// the 0.1 schema.
	now := nowUTC()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO work_types(project_id,key,name,mode,phases_json,updated_at) VALUES(?,?,?,?,?,?)`,
		pid, "visit", "Visit", "at_site", `["intake","schedule","execute","closed"]`, now)
	mustExec(`INSERT INTO rate_items(project_id,code,name,pricing_json,updated_at) VALUES(?,?,?,?,?)`,
		pid, "BASIC", "Basic", `{"mode":"flat","unit_cents":5000}`, now)
	mustExec(`INSERT INTO policies(project_id,key,value_json,updated_at) VALUES(?,?,?,?)`, pid, "max_discount_pct", "10", now)
	mustExec(`INSERT INTO work(project_id,number,work_type_id,mode,phase,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`,
		pid, "W-00001", 1, "at_site", "intake", "new", now, now)
	mustExec(`INSERT INTO agreements(project_id,name,work_type_id,recurrence_json,starts_on,next_on,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)`, pid, "Weekly", 1, `{"every":1,"unit":"week","time":"09:00"}`, "2026-11-16", "2026-11-16", now, now)

	migrate(t, db, "")

	w := must(getWork(db, pid, 1))
	if w.WorkType != "visit" || w.BrandID != 0 || w.Brand != "" {
		t.Fatalf("work after upgrade: %+v", w)
	}
	ag := must(getAgreement(db, pid, 1))
	if ag.WorkType != "visit" {
		t.Fatalf("agreement after upgrade: %+v", ag)
	}
	if v := policyInt(db, pid, 0, "max_discount_pct", 0); v != 10 {
		t.Fatalf("policy = %d", v)
	}
	if ri := must(getRateItem(db, pid, 0, "BASIC", 0)); ri.Pricing.UnitCents != 5000 {
		t.Fatal("rate item lost")
	}
	// No brands: new work still numbers in the W series.
	nw := must(intakeWork(db, pid, nil, map[string]any{"work_type": "visit"})).Work
	if nw.Number != "W-00002" {
		t.Fatalf("number = %s", nw.Number)
	}
}

func TestBrandOverridesPoliciesAndAmbiguity(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow)
	install(t, db, "plumber") // shared by both brands
	a := must(upsertBrand(db, pid, map[string]any{"key": "alpha", "name": "Alpha Plumbing", "number_prefix": "AP"}))
	b := must(upsertBrand(db, pid, map[string]any{"key": "beta", "name": "Beta Pipes", "number_prefix": "BP"}))

	// Several brands, shared work type, no brand given: ask.
	_, err := intakeWork(db, pid, nil, map[string]any{"work_type": "repair", "address": "x", "answers": map[string]any{"issue": "tap", "description": "drip"}})
	var be *BrandError
	if !errors.As(err, &be) || len(be.Options) != 2 {
		t.Fatalf("want brand question with 2 options, got %v", err)
	}

	// Beta charges more for the call-out and allows less discount.
	ri := must(getRateItem(db, pid, 0, "CALLOUT", 0))
	ri.BrandID, ri.Pricing.UnitCents = b.ID, 9900
	if err := upsertRateItem(db, pid, *ri); err != nil {
		t.Fatal(err)
	}
	if err := setPolicy(db, pid, b.ID, "max_discount_pct", 5); err != nil {
		t.Fatal(err)
	}
	price := func(brand int64) *PriceResult {
		return must(priceItems(db, pid, []ItemRequest{{Code: "CALLOUT"}}, PriceContext{BrandID: brand, DiscountPct: 8, DefaultDur: 60}))
	}
	pa, pb := price(a.ID), price(b.ID)
	if pa.SubtotalCents != 8900 || pb.SubtotalCents != 9900 {
		t.Fatalf("subtotals alpha %d beta %d", pa.SubtotalCents, pb.SubtotalCents)
	}
	if pa.NeedsOwner || !pb.NeedsOwner {
		t.Fatalf("8%% discount: alpha flags %v, beta flags %v", pa.Flags, pb.Flags)
	}
	// Removing the override falls back to the project value.
	if err := setPolicy(db, pid, b.ID, "max_discount_pct", nil); err != nil {
		t.Fatal(err)
	}
	if price(b.ID).NeedsOwner {
		t.Fatal("override removed: 8% is within the project's 10%")
	}

	// Each brand installs its own copy of a pack: same keys, no clash.
	packs := must(builtinPacks())
	must(installPack(db, pid, packs["cleaning"], a.ID))
	must(installPack(db, pid, packs["cleaning"], b.ID))
	if _, err := findWorkType(db, pid, "deep_clean", 0); !errors.As(err, &be) {
		t.Fatalf("deep_clean exists for two brands: want brand question, got %v", err)
	}
	if wt := must(findWorkType(db, pid, "deep_clean", b.ID)); wt.BrandID != b.ID {
		t.Fatal("brand lookup must return the brand's own type")
	}

	// A brand's own site settles the brand; a mismatch is refused.
	shop := must(upsertSite(db, pid, map[string]any{"kind": "customer", "name": "Beta client", "address": "y", "brand": "beta"}))
	w := must(intakeWork(db, pid, nil, map[string]any{"work_type": "repair", "site_id": float64(shop.ID),
		"answers": map[string]any{"issue": "tap", "description": "drip"}})).Work
	if w.Brand != "beta" || w.Number != "BP-00001" {
		t.Fatalf("site brand: %s %s", w.Brand, w.Number)
	}
	if _, err := intakeWork(db, pid, nil, map[string]any{"work_type": "repair", "brand": "alpha", "site_id": float64(shop.ID)}); !errors.As(err, &be) {
		t.Fatalf("alpha work at beta's site must be refused, got %v", err)
	}

	// Prefix rules.
	if _, err := upsertBrand(db, pid, map[string]any{"key": "gamma", "name": "Gamma", "number_prefix": "W"}); err == nil {
		t.Fatal("prefix W is reserved")
	}
	if _, err := upsertBrand(db, pid, map[string]any{"key": "beta", "number_prefix": "BX"}); err == nil {
		t.Fatal("beta has work: prefix cannot change")
	}

	// Archived brands take no new work.
	must(upsertBrand(db, pid, map[string]any{"key": "alpha", "status": "archived"}))
	if _, err := intakeWork(db, pid, nil, map[string]any{"work_type": "repair", "brand": "alpha", "address": "z"}); err == nil {
		t.Fatal("archived brand must refuse work")
	}
}

func TestCleaningPrices(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow) // Monday
	install(t, db, "cleaning")
	res := must(intakeWork(db, pid, nil, map[string]any{"work_type": "deep_clean", "address": "3 rue C",
		"answers": map[string]any{"property_type": "flat", "bedrooms": 2, "bathrooms": 1}, "items": []any{"DEEP_CLEAN", "OVEN_CLEAN"}}))
	if res.Quote.SubtotalCents != 16000+6500 || res.Quote.DurationMin != 360 {
		t.Fatalf("deep clean %d, %d min", res.Quote.SubtotalCents, res.Quote.DurationMin)
	}
	if res.Quote.Requirements[0].Count != 2 {
		t.Fatalf("deep clean needs two cleaners, got %+v", res.Quote.Requirements)
	}
	reg := must(intakeWork(db, pid, nil, map[string]any{"work_type": "regular_clean", "address": "4 rue D",
		"answers": map[string]any{"property_type": "flat", "hours": 3}, "items": []any{"REGULAR_CLEAN"}}))
	if reg.Quote.TotalCents != 8400 || reg.Quote.DurationMin != 180 {
		t.Fatalf("regular clean %d, %d min", reg.Quote.TotalCents, reg.Quote.DurationMin)
	}
	sameDay := must(intakeWork(db, pid, nil, map[string]any{"work_type": "regular_clean", "address": "5 rue E", "summary": "need it today please",
		"answers": map[string]any{"property_type": "flat", "hours": 2}, "items": []any{"REGULAR_CLEAN"}}))
	if sameDay.Work.Priority != "p1" || sameDay.Quote.TotalCents != 5600+1680 {
		t.Fatalf("same day: %s %d", sameDay.Work.Priority, sameDay.Quote.TotalCents)
	}
}

func TestOnlyOwnSiteSettlesBrand(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow)
	install(t, db, "restaurant") // shared
	must(upsertBrand(db, pid, map[string]any{"key": "trattoria", "name": "Trattoria", "number_prefix": "TR"}))
	must(upsertBrand(db, pid, map[string]any{"key": "brightclean", "name": "BrightClean", "number_prefix": "BC"}))
	r := must(upsertSite(db, pid, map[string]any{"kind": "own", "name": "Trattoria", "timezone": "UTC"}))
	// The only own site is shared: the brand is still a question.
	if _, err := intakeWork(db, pid, nil, map[string]any{"work_type": "reservation", "answers": map[string]any{"party_size": 2}}); err == nil {
		t.Fatal("shared site, two brands: brand needed")
	}
	must(upsertSite(db, pid, map[string]any{"id": float64(r.ID), "brand": "trattoria"}))
	w := must(intakeWork(db, pid, nil, map[string]any{"work_type": "reservation", "answers": map[string]any{"party_size": 2}})).Work
	if w.Brand != "trattoria" || w.Number != "TR-00001" || w.SiteID == nil || *w.SiteID != r.ID {
		t.Fatalf("work %s %s site %v", w.Brand, w.Number, w.SiteID)
	}
}
