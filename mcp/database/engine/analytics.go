package engine

import (
	"strings"
	"time"
	_ "time/tzdata" // IANA zones without relying on the host (static/Alpine builds)
)

// Lookup attaches at most one record of another collection in the same
// database: the one whose single-field primary key equals localField. Being
// many-to-one, it never multiplies the rows being counted or summed.
type Lookup struct {
	As         string   `json:"as"`
	Collection string   `json:"collection"`
	LocalField string   `json:"localField"`
	Select     []string `json:"select,omitempty"`
}

// Bucket derives a calendar grouping key from a datetime in an IANA timezone.
type Bucket struct {
	Name      string `json:"name"`
	Field     string `json:"field"`
	Unit      string `json:"unit"`
	Timezone  string `json:"timezone,omitempty"`
	WeekStart string `json:"weekStart,omitempty"`
}

const maxLookups = 4
const maxBuckets = 4

type lookupPlan struct {
	Lookup
	target Collection
	fields []Field
	names  []string // "as.field" for each of fields
}
type bucketPlan struct {
	Bucket
	loc       *time.Location
	weekStart time.Weekday
	hours     map[string]any // stored UTC hour -> bucket, or splitHour
}

// splitHour marks a UTC hour that spans two buckets (half-hour offsets).
type splitHour struct{}

// analyticsPlan extends an aggregate beyond plain stored fields. view
// describes every addressable field: stored fields, "as.field" lookup fields
// and bucket keys. Adapters evaluate pushed while scanning; residual needs
// derived fields and runs after enrichment.
type analyticsPlan struct {
	view     Collection
	lookups  []lookupPlan
	buckets  []bucketPlan
	pushed   *Filter
	residual *Filter
	fields   []string
	get      func(Collection, string) (Record, error)
}

func needsAnalytics(r Request) bool {
	if len(r.Lookups) > 0 || len(r.Buckets) > 0 {
		return true
	}
	for _, m := range r.Metrics {
		if m.Op == "count_distinct" || m.Where != nil {
			return true
		}
	}
	return false
}

// derived reports whether rows need fields that no adapter stores.
func (a *analyticsPlan) derived() bool {
	return a != nil && (len(a.lookups) > 0 || len(a.buckets) > 0)
}

func prepareAnalytics(tx transaction, c Collection, r *Request) error {
	if len(r.Lookups) > maxLookups {
		return Invalid("at most %d lookups", maxLookups)
	}
	if len(r.Buckets) > maxBuckets {
		return Invalid("at most %d buckets", maxBuckets)
	}
	a := &analyticsPlan{get: tx.get}
	view := c
	view.Fields = append([]Field{}, c.Fields...)
	taken := map[string]bool{"_created_at": true, "_updated_at": true, "_version": true}
	for _, f := range c.Fields {
		taken[f.Name] = true
	}
	claim := func(name string) error {
		if e := nameOK(name); e != nil {
			return e
		}
		if taken[name] {
			return Invalid("%s is already a field, lookup or bucket name", name)
		}
		taken[name] = true
		return nil
	}
	for _, l := range r.Lookups {
		if e := claim(l.As); e != nil {
			return e
		}
		if e := nameOK(l.Collection); e != nil {
			return e
		}
		target, e := tx.collection(l.Collection)
		if e != nil {
			return e
		}
		if len(target.PrimaryKey) != 1 {
			return Invalid("lookup %s needs a collection with a single-field primary key", l.As)
		}
		local, e := view.field(l.LocalField)
		if e != nil {
			return e
		}
		key, _ := target.field(target.PrimaryKey[0])
		if local.Type != key.Type {
			return Invalid("lookup %s: %s is %s but %s.%s is %s", l.As, l.LocalField, local.Type, target.Name, key.Name, key.Type)
		}
		p := lookupPlan{Lookup: l, target: target}
		if len(l.Select) == 0 {
			p.fields = target.Fields
		}
		seen := map[string]bool{}
		for _, name := range l.Select {
			f, e := target.field(name)
			if e != nil {
				return e
			}
			if seen[name] {
				return Invalid("duplicate lookup field %s", name)
			}
			seen[name] = true
			p.fields = append(p.fields, f)
		}
		for _, f := range p.fields {
			name := l.As + "." + f.Name
			p.names = append(p.names, name)
			view.Fields = append(view.Fields, Field{Name: name, Type: f.Type, Nullable: true})
		}
		a.lookups = append(a.lookups, p)
	}
	for _, b := range r.Buckets {
		if e := claim(b.Name); e != nil {
			return e
		}
		src, e := view.field(b.Field)
		if e != nil {
			return e
		}
		if src.Type != "datetime" {
			return Invalid("bucket %s requires a datetime field", b.Name)
		}
		p := bucketPlan{Bucket: b, weekStart: time.Monday, hours: map[string]any{}}
		if b.Timezone == "" {
			p.Timezone = "UTC"
		}
		if p.Timezone == "Local" {
			return Invalid("bucket timezone must be an IANA name such as Europe/Paris")
		}
		if p.loc, e = time.LoadLocation(p.Timezone); e != nil {
			return Invalid("unknown timezone %q", p.Timezone)
		}
		switch b.WeekStart {
		case "", "monday":
		case "sunday":
			p.weekStart = time.Sunday
		default:
			return Invalid("weekStart must be monday or sunday")
		}
		out := Field{Name: b.Name, Type: "text", Nullable: true}
		switch b.Unit {
		case "hour":
			out.Type = "datetime"
		case "day", "week", "month", "year":
		default:
			return Invalid("bucket unit must be hour, day, week, month or year")
		}
		view.Fields = append(view.Fields, out)
		a.buckets = append(a.buckets, p)
	}
	a.view = view
	r.analytics = a
	return nil
}

// finish runs after validation has normalized filter values.
func (a *analyticsPlan) finish(c Collection, r Request) {
	stored := func(name string) bool { _, e := c.field(name); return e == nil }
	a.pushed, a.residual = splitFilter(r.Where, stored)
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] && stored(name) {
			seen[name] = true
			a.fields = append(a.fields, name)
		}
	}
	for _, g := range r.GroupBy {
		add(g)
	}
	for _, m := range r.Metrics {
		add(m.Field)
		visitFilterFields(m.Where, add)
	}
	visitFilterFields(a.residual, add)
	for _, l := range a.lookups {
		add(l.LocalField)
	}
	for _, b := range a.buckets {
		add(b.Field)
	}
}

// splitFilter pushes the conjuncts an adapter can evaluate on stored fields.
func splitFilter(f *Filter, stored func(string) bool) (pushed, residual *Filter) {
	if f == nil {
		return nil, nil
	}
	if onlyFields(f, stored) {
		return f, nil
	}
	if f.And == nil {
		return nil, f
	}
	var p, r []Filter
	for _, child := range f.And {
		if onlyFields(&child, stored) {
			p = append(p, child)
		} else {
			r = append(r, child)
		}
	}
	return conjunction(p), conjunction(r)
}
func conjunction(fs []Filter) *Filter {
	switch len(fs) {
	case 0:
		return nil
	case 1:
		return &fs[0]
	}
	return &Filter{And: fs}
}
func onlyFields(f *Filter, ok func(string) bool) bool {
	all := true
	visitFilterFields(f, func(name string) { all = all && ok(name) })
	return all
}
func visitFilterFields(f *Filter, visit func(string)) {
	if f == nil {
		return
	}
	if f.Field != "" {
		visit(f.Field)
	}
	for _, name := range f.Fields {
		visit(name)
	}
	for i := range f.And {
		visitFilterFields(&f.And[i], visit)
	}
	for i := range f.Or {
		visitFilterFields(&f.Or[i], visit)
	}
}

// lookupCache keeps resolved targets per lookup. It is only a cache: when it
// outgrows its share of the work budget it starts over instead of failing.
type lookupCache struct {
	rows  []map[any]Record
	bytes int
	limit int
}

func newLookupCache(lookups, limit int) *lookupCache {
	c := &lookupCache{limit: limit}
	c.reset(lookups)
	return c
}
func (c *lookupCache) reset(lookups int) {
	c.rows = make([]map[any]Record, lookups)
	for i := range c.rows {
		c.rows[i] = map[any]Record{}
	}
	c.bytes = 0
}

func (a *analyticsPlan) lookup(i int, v any, cache *lookupCache) (Record, error) {
	if v == nil {
		return nil, nil
	}
	if hit, ok := cache.rows[i][v]; ok {
		return hit, nil
	}
	l := a.lookups[i]
	pk, e := primary(l.target, Record{l.target.PrimaryKey[0]: v})
	if e != nil {
		return nil, e
	}
	hit, e := a.get(l.target, pk)
	if e != nil {
		return nil, e
	}
	n := 96 + retainedBytes(v) + retainedBytes(hit)
	if cache.bytes+n > cache.limit {
		cache.reset(len(a.lookups))
	}
	cache.bytes += n
	cache.rows[i][v] = hit
	return hit, nil
}

// enrich adds lookup fields and bucket keys to a scanned row in place. Every
// derived key is written for every row, so adapters may reuse the row map.
func (a *analyticsPlan) enrich(row Record, cache *lookupCache) error {
	for i, l := range a.lookups {
		hit, e := a.lookup(i, row[l.LocalField], cache)
		if e != nil {
			return e
		}
		for j, f := range l.fields {
			var v any
			if hit != nil {
				v = hit[f.Name]
			}
			row[l.names[j]] = v
		}
	}
	for _, b := range a.buckets {
		row[b.Name] = b.key(row[b.Field])
	}
	return nil
}

// attach nests each lookup's selected target fields under its name on found
// records, or null when the reference is null or dangling.
func (a *analyticsPlan) attach(records []Record) error {
	cache := newLookupCache(len(a.lookups), MaxQueryWorkBytes/4)
	for _, rec := range records {
		for i, l := range a.lookups {
			hit, e := a.lookup(i, rec[l.LocalField], cache)
			if e != nil {
				return e
			}
			if hit == nil {
				rec[l.As] = nil
				continue
			}
			nested := make(Record, len(l.fields))
			for _, f := range l.fields {
				nested[f.Name] = hit[f.Name]
			}
			rec[l.As] = nested
		}
	}
	return nil
}

// key returns the bucket containing a stored datetime. Stored datetimes are
// normalized UTC ("2006-01-02T15:04:05.000000Z"). Buckets are contiguous
// in absolute time, so when both ends of a UTC hour share a bucket, every
// instant in it does: cache per hour instead of converting every row.
func (b bucketPlan) key(v any) any {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	if len(s) == 27 && s[26] == 'Z' {
		hour := s[:13]
		if k, ok := b.hours[hour]; ok {
			if _, split := k.(splitHour); !split {
				return k
			}
		} else if start, e := time.Parse(time.RFC3339, hour+":00:00Z"); e == nil {
			k := b.at(start)
			var cached any = splitHour{}
			if b.at(start.Add(time.Hour-time.Microsecond)) == k {
				cached = k
			}
			if len(b.hours) < 1<<16 {
				b.hours[hour] = cached
			}
			if cached == k {
				return k
			}
		}
	}
	t, e := time.Parse(time.RFC3339Nano, s)
	if e != nil {
		return nil
	}
	return b.at(t)
}

// at is the bucket of t. Calendar units are the local start date
// ("2026-03-29"); hour is the UTC instant the local hour starts, which stays
// unambiguous across daylight-saving changes.
func (b bucketPlan) at(t time.Time) any {
	local := t.In(b.loc)
	if b.Unit == "hour" {
		_, offset := local.Zone()
		shift := time.Duration(offset) * time.Second
		return t.UTC().Add(shift).Truncate(time.Hour).Add(-shift).Format("2006-01-02T15:04:05.000000Z")
	}
	y, m, d := local.Date()
	switch b.Unit {
	case "week":
		d -= (int(local.Weekday()) - int(b.weekStart) + 7) % 7
	case "month":
		d = 1
	case "year":
		m, d = time.January, 1
	}
	// Calendar arithmetic in UTC: local midnight may not exist on DST days.
	return time.Date(y, m, d, 12, 0, 0, 0, time.UTC).Format("2006-01-02")
}

func lookupLocalIsStored(l Lookup) bool { return !strings.Contains(l.LocalField, ".") }
