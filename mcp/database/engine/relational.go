package engine

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Relational reads run in Go over the transaction's indexed lookups, so every
// adapter gives the same rows from one snapshot. related_read, joined_page and
// joined_aggregate share the join stages below. Database applies the filters it
// is given: who may see what is the caller's decision.

const (
	opRelatedRead     = "related_read"
	opJoinedPage      = "joined_page"
	opJoinedAggregate = "joined_aggregate"

	maxRelJoins     = 8
	maxRelSources   = 4
	maxRelComputed  = 32
	maxRelOffset    = 100000
	maxRelFanout    = 10000
	relProbeWork    = 12
	relBatch        = 1000
	relDefaultAlias = "root"
	relHiddenPrefix = "\x00"
	relCursorTTL    = 24 * time.Hour
	relMemoBytes    = 16 << 20
)

// Tests change these.
var (
	relationalWork     = int64(16 << 20)
	relationalRetained = 128 << 20
)

// isRelationalRead reports the relational read operations.
func isRelationalRead(op string) bool {
	return op == opRelatedRead || op == opJoinedPage || op == opJoinedAggregate
}

// RelOn compares a field of an earlier row, named "alias.field", with a field of
// the joined collection. Comparison is "" for one type, "text" when an integer's
// decimal text must equal the text exactly ("1" matches 1, "01" does not), or
// "numeric" when any decimal form of the integer matches ("01" matches 1).
type RelOn struct {
	Local      string `json:"local"`
	Foreign    string `json:"foreign"`
	Comparison string `json:"comparison,omitempty"`
}
type RelOnGroup struct {
	On []RelOn `json:"on"`
}

// RelJoin adds rows of a collection to each row so far. Kind is inner (default),
// left, semi, anti, or aggregate, which adds one row's Metrics over its related
// records as "as.metric" without multiplying the row. Where belongs to the join: a left join keeps its row when
// nothing satisfies it. One left row matches a physical record once, however
// many alternatives of anyOf find it.
type RelJoin struct {
	As         string       `json:"as"`
	Collection string       `json:"collection"`
	Kind       string       `json:"kind,omitempty"`
	On         []RelOn      `json:"on,omitempty"`
	AnyOf      []RelOnGroup `json:"anyOf,omitempty"`
	Where      *Filter      `json:"where,omitempty"`
	Metrics    []Metric     `json:"metrics,omitempty"`
}

// RelExpr is exactly one of its forms; an empty one is null.
type RelExpr struct {
	resultType  string
	Op          string    `json:"op,omitempty"`
	Args        []RelExpr `json:"args,omitempty"`
	Round       *int      `json:"round,omitempty"`
	RoundMode   string    `json:"roundMode,omitempty"`
	Field       string    `json:"field,omitempty"`
	Const       any       `json:"const,omitempty"`
	Coalesce    []RelExpr `json:"coalesce,omitempty"`
	NullIfEmpty *RelExpr  `json:"nullIfEmpty,omitempty"`
	Trim        *RelExpr  `json:"trim,omitempty"`
	Strip       *RelExpr  `json:"strip,omitempty"`
	Chars       string    `json:"chars,omitempty"`
	Concat      []RelExpr `json:"concat,omitempty"`
	Separator   string    `json:"separator,omitempty"`
	JSON        *RelExpr  `json:"json,omitempty"`
	Path        string    `json:"path,omitempty"`
	If          *Filter   `json:"if,omitempty"`
	Then        *RelExpr  `json:"then,omitempty"`
	Else        *RelExpr  `json:"else,omitempty"`
}

// RelComputed adds a named value to each row. Later stages can use the name.
type RelComputed struct {
	Name string  `json:"name"`
	Type string  `json:"type,omitempty"`
	Expr RelExpr `json:"expr"`
}

// RelPick keeps the first row per key under OrderBy, among rows satisfying Where.
type RelPick struct {
	By      []string `json:"by"`
	OrderBy []Order  `json:"orderBy"`
	Where   *Filter  `json:"where,omitempty"`
}

// RelGroupOrder sorts groups by one value of their rows. Agg is min or max; a
// grouping key needs none.
type RelGroupOrder struct {
	Field     string `json:"field"`
	Direction string `json:"direction,omitempty"`
	Agg       string `json:"agg,omitempty"`
}
type RelGroup struct {
	By      []string        `json:"by"`
	OrderBy []RelGroupOrder `json:"orderBy,omitempty"`
}

// RelSource is one branch of a relation. Filter decides which rows take a slot,
// and Distinct collapses rows that are the same physical records.
type RelSource struct {
	Name     string        `json:"name"`
	Joins    []RelJoin     `json:"joins"`
	Computed []RelComputed `json:"computed,omitempty"`
	Filter   *Filter       `json:"filter,omitempty"`
	Distinct []string      `json:"distinct,omitempty"`
	OrderBy  []Order       `json:"orderBy,omitempty"`
	Select   []string      `json:"select"`
}
type RelRelation struct {
	As      string      `json:"as"`
	Limit   int         `json:"limit"`
	Sources []RelSource `json:"sources"`
}

// RelBranch is one input of a joined_aggregate over several branches.
type RelBranch struct {
	Collection string        `json:"collection"`
	As         string        `json:"as,omitempty"`
	Where      *Filter       `json:"where,omitempty"`
	Joins      []RelJoin     `json:"joins,omitempty"`
	Computed   []RelComputed `json:"computed,omitempty"`
	Filter     *Filter       `json:"filter,omitempty"`
	Distinct   []string      `json:"distinct,omitempty"`
	Pick       *RelPick      `json:"pick,omitempty"`
	Select     []string      `json:"select"`
}

// RelRollup aggregates by key before the outer aggregate.
type RelRollup struct {
	By       []string      `json:"by"`
	Metrics  []Metric      `json:"metrics"`
	Computed []RelComputed `json:"computed,omitempty"`
	Filter   *Filter       `json:"filter,omitempty"`
}
type RelDerived struct {
	Name        string `json:"name"`
	Op          string `json:"op"`
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
	Round       *int   `json:"round,omitempty"`
}

type relBudget struct {
	ctx      context.Context
	work     int64
	retained int
}

// relContext is the deadline of the transaction's request.
func relContext(tx transaction) context.Context {
	switch t := tx.(type) {
	case pipelineSchemaTx:
		return relContext(t.transaction)
	case cachedSchemaTx:
		return relContext(t.transaction)
	case *sqliteTx:
		return t.ctx
	case *pebbleTx:
		return t.ctx
	case *aptevaTx:
		return t.ctx
	}
	return context.Background()
}

func (b *relBudget) spend(n int64) error {
	if b.work += n; b.work > relationalWork {
		return fail("query_too_expensive", "relational read exceeded its work budget")
	}
	return b.ctx.Err()
}
func (b *relBudget) retain(n int) error {
	if b.retained += n; b.retained > relationalRetained {
		return fail("resource_limit", "relational read retained more than %d MiB", relationalRetained>>20)
	}
	return nil
}

type relPair struct {
	on             RelOn
	local, foreign Field
}
type relAlt struct {
	pairs []relPair
	index string
}
type relJoinPlan struct {
	join    RelJoin
	foreign Collection
	alts    []relAlt
	state   *existsState
	fields  []string
	names   map[string]string
	memo    map[string][]Record
	// memoBytes is released when the memo is dropped: it is only a cache.
	memoBytes int
	aggInput  Request
	aggOut    Collection
	aggMemo   map[string]Record
	hidden    string
}

// relChain is the row pipeline after the first row: joins, computed values, a
// filter and distinct.
type relChain struct {
	tx       transaction
	budget   *relBudget
	view     Collection
	joins    []*relJoinPlan
	computed []RelComputed
	fields   []Field
	filter   *Filter
	distinct []string
	aliases  []string
	used     map[string]bool
	seen     map[string]struct{}
	seenSize int
}

func relKinds(kind string) bool {
	return kind == "" || kind == "inner" || kind == "left" || kind == "semi" || kind == "anti" || kind == "aggregate"
}

// newRelChain types every stage before any row is read.
func newRelChain(tx transaction, base Collection, aliases []string, budget *relBudget, joins []RelJoin, computed []RelComputed, filter *Filter, distinct []string) (*relChain, error) {
	if len(joins) > maxRelJoins {
		return nil, Invalid("at most %d joins", maxRelJoins)
	}
	if len(computed) > maxRelComputed {
		return nil, Invalid("at most %d computed values", maxRelComputed)
	}
	p := &relChain{tx: tx, budget: budget, view: base, computed: computed, filter: filter, distinct: distinct, aliases: append([]string{}, aliases...), used: map[string]bool{}, seen: map[string]struct{}{}}
	p.view.Fields = append([]Field{}, base.Fields...)
	taken := map[string]bool{}
	for _, a := range aliases {
		taken[a] = true
	}
	for i := range joins {
		j, e := p.addJoin(joins[i], taken)
		if e != nil {
			return nil, e
		}
		p.joins = append(p.joins, j)
	}
	for i := range computed {
		field, e := p.typeComputed(&computed[i])
		if e != nil {
			return nil, e
		}
		if _, e := p.view.field(field.Name); e == nil || taken[field.Name] {
			return nil, Invalid("%s is already an alias or field name", field.Name)
		}
		p.view.Fields = append(p.view.Fields, field)
		p.fields = append(p.fields, field)
	}
	if e := p.checkFilter(filter); e != nil {
		return nil, e
	}
	for _, name := range distinct {
		if e := p.checkDistinct(name); e != nil {
			return nil, e
		}
	}
	return p, nil
}

func (p *relChain) checkFilter(f *Filter) error {
	if hasExists(f) {
		return Invalid("a filter over joined rows cannot use exists; put it in a join's where, or add a semi or anti join")
	}
	n := 0
	return validateFilter(p.view, f, 0, &n)
}

func (p *relChain) checkDistinct(name string) error {
	for _, a := range p.aliases {
		if a == name {
			return nil
		}
	}
	if _, e := p.view.field(name); e != nil {
		return Invalid("distinct needs an alias or a field; %s is neither", name)
	}
	return nil
}

func (p *relChain) addJoin(j RelJoin, taken map[string]bool) (*relJoinPlan, error) {
	if e := nameOK(j.As); e != nil {
		return nil, e
	}
	if taken[j.As] {
		return nil, Invalid("alias %s is used twice", j.As)
	}
	if !relKinds(j.Kind) {
		return nil, Invalid("unknown join kind %s", j.Kind)
	}
	if e := nameOK(j.Collection); e != nil {
		return nil, e
	}
	foreign, e := p.tx.collection(j.Collection)
	if e != nil {
		return nil, e
	}
	groups := j.AnyOf
	if (j.On == nil) == (j.AnyOf == nil) {
		return nil, Invalid("join %s requires exactly one of on, anyOf", j.As)
	}
	if j.On != nil {
		groups = []RelOnGroup{{On: j.On}}
	}
	if len(groups) == 0 || len(groups) > maxExistsAlternatives {
		return nil, Invalid("anyOf requires 1–%d alternatives", maxExistsAlternatives)
	}
	plan := &relJoinPlan{join: j, foreign: foreign, names: map[string]string{}, memo: map[string][]Record{}, hidden: relHiddenPrefix + j.As}
	n := 0
	if e := validateFilter(foreign, j.Where, 0, &n); e != nil {
		return nil, e
	}
	if plan.state, e = prepareExists(p.tx, foreign, j.Where); e != nil {
		return nil, e
	}
	for _, g := range groups {
		if len(g.On) == 0 || len(g.On) > maxExistsPairs {
			return nil, Invalid("a join alternative requires 1–%d pairs", maxExistsPairs)
		}
		alt := relAlt{}
		for _, on := range g.On {
			local, e := p.view.field(on.Local)
			if e != nil {
				return nil, Invalid("join %s: %s must name an earlier alias.field", j.As, on.Local)
			}
			remote, e := foreign.field(on.Foreign)
			if e != nil {
				return nil, e
			}
			if local.Type == "json" || remote.Type == "json" {
				return nil, Invalid("join %s cannot compare JSON fields", j.As)
			}
			switch on.Comparison {
			case "":
				if local.Type != remote.Type {
					return nil, Invalid("join %s compares %s (%s) with %s (%s); use a comparison or fields of one type", j.As, on.Local, local.Type, on.Foreign, remote.Type)
				}
			case "text":
				if !textComparable(local) || !textComparable(remote) {
					return nil, Invalid("comparison text requires text or integer fields")
				}
			case "numeric":
				if !textComparable(local) || remote.Type != "integer" {
					return nil, Invalid("comparison numeric requires a text or integer local field and an integer related field")
				}
			default:
				return nil, Invalid("unknown join comparison %s", on.Comparison)
			}
			if alt.index == "" {
				alt.index = leadingIndex(foreign, on.Foreign)
			}
			p.use(on.Local)
			plan.use(on.Foreign)
			alt.pairs = append(alt.pairs, relPair{on: on, local: local, foreign: remote})
		}
		if alt.index == "" {
			return nil, fail("query_too_expensive", "join %s needs an index on %s starting with one of its related fields", j.As, foreign.Name)
		}
		plan.alts = append(plan.alts, alt)
	}
	taken[j.As] = true
	if j.Kind == "aggregate" {
		if len(j.Metrics) == 0 {
			return nil, Invalid("aggregate join %s requires metrics", j.As)
		}
		if e := noExistsInMetrics(j.Metrics); e != nil {
			return nil, e
		}
		plan.aggInput = Request{Metrics: j.Metrics}
		out, e := validateAggregate(foreign, &plan.aggInput)
		if e != nil {
			return nil, e
		}
		plan.aggOut, plan.aggMemo = out, map[string]Record{}
		for _, m := range j.Metrics {
			if m.Field != "" {
				plan.use(m.Field)
			}
			visitFilterFields(m.Where, plan.use)
			f, e := out.field(m.Name)
			if e != nil {
				return nil, e
			}
			f.Name, f.Nullable = j.As+"."+m.Name, true
			p.view.Fields = append(p.view.Fields, f)
		}
		return plan, nil
	}
	if len(j.Metrics) > 0 {
		return nil, Invalid("metrics require an aggregate join")
	}
	if j.Kind == "semi" || j.Kind == "anti" {
		return plan, nil
	}
	for _, f := range foreign.Fields {
		name := j.As + "." + f.Name
		plan.names[f.Name] = name
		f.Name, f.Nullable = name, true
		p.view.Fields = append(p.view.Fields, f)
	}
	p.aliases = append(p.aliases, j.As)
	return plan, nil
}

func (j *relJoinPlan) use(field string) {
	for _, f := range j.fields {
		if f == field {
			return
		}
	}
	j.fields = append(j.fields, field)
}

// use marks a view name as needed, so joins read only the fields a request uses.
func (p *relChain) use(name string) { p.used[name] = true }

// useAll marks every view field as needed.
func (p *relChain) useAll() {
	for _, f := range p.view.Fields {
		p.used[f.Name] = true
	}
}

// finish decides what each join reads, once every stage has named its fields.
func (p *relChain) finish() {
	for i := range p.computed {
		p.computed[i].Expr.visit(p.use)
	}
	visitFilterFields(p.filter, p.use)
	for _, name := range p.distinct {
		p.use(name)
	}
	for _, j := range p.joins {
		for _, pk := range j.foreign.PrimaryKey {
			j.use(pk)
		}
		for field, name := range j.names {
			if p.used[name] {
				j.use(field)
			}
		}
	}
}

// physical identifies a record by its primary key.
func physical(c Collection, r Record) string {
	values := make([]any, len(c.PrimaryKey))
	for i, name := range c.PrimaryKey {
		values[i] = r[name]
	}
	return tupleKey(values)
}

// matches returns the related records of one row, each once.
func (j *relJoinPlan) matches(tx transaction, budget *relBudget, row Record, firstOnly bool) ([]Record, error) {
	var out []Record
	var seen map[string]struct{}
	for i, alt := range j.alts {
		keys := make([]any, len(alt.pairs))
		ok := true
		for k, pair := range alt.pairs {
			v := row[pair.on.Local]
			if v == nil {
				ok = false
				break
			}
			if keys[k], ok = pair.key(v); !ok {
				break
			}
		}
		if !ok {
			continue
		}
		memo := strconv.Itoa(i) + "\x00" + tupleKey(keys)
		rows, cached := j.memo[memo]
		if !cached {
			if e := budget.spend(relProbeWork); e != nil {
				return nil, e
			}
			terms := make([]Filter, 0, len(keys)+1)
			for k, key := range keys {
				terms = append(terms, Filter{Field: alt.pairs[k].on.Foreign, Op: "eq", Value: key})
			}
			if j.join.Where != nil {
				terms = append(terms, *j.join.Where)
			}
			limit := maxRelFanout + 1
			if firstOnly {
				limit = 1
			}
			var e error
			rows, e = relProbeRows(tx, j.foreign, Query{Where: conjunction(terms), Select: j.fields, hint: alt.index}, limit, budget)
			if e == nil {
				e = j.state.failure()
			}
			if e != nil {
				return nil, e
			}
			if len(rows) > maxRelFanout {
				return nil, fail("query_too_expensive", "join %s matched more than %d records for one row", j.join.As, maxRelFanout)
			}
			if e := budget.spend(int64(len(rows))); e != nil {
				return nil, e
			}
			// Distinct keeps a row's first match, so every adapter must list them
			// in one order.
			sort.Slice(rows, func(a, b int) bool { return physical(j.foreign, rows[a]) < physical(j.foreign, rows[b]) })
			size := 64 + retainedBytes(memo) + retainedBytes(rows)
			if j.memoBytes+size > relMemoBytes {
				budget.retained -= j.memoBytes
				j.memo, j.memoBytes = map[string][]Record{}, 0
			}
			if e := budget.retain(size); e != nil {
				return nil, e
			}
			j.memo[memo] = rows
			j.memoBytes += size
		}
		if firstOnly && len(rows) > 0 {
			return rows[:1], nil
		}
		if len(j.alts) == 1 {
			return rows, nil
		}
		for _, r := range rows {
			key := physical(j.foreign, r)
			if seen == nil {
				seen = map[string]struct{}{}
			}
			if _, dup := seen[key]; !dup {
				seen[key] = struct{}{}
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func (a relPair) key(v any) (any, bool) {
	if a.on.Comparison == "numeric" && a.local.Type == "text" {
		n, e := strconv.ParseInt(v.(string), 10, 64)
		if e != nil || strings.HasPrefix(v.(string), "+") {
			return nil, false
		}
		return strconv.FormatInt(n, 10), true
	}
	if a.on.Comparison == "text" && a.local.Type == "text" && a.foreign.Type == "integer" {
		n, e := strconv.ParseInt(v.(string), 10, 64)
		if e != nil || strconv.FormatInt(n, 10) != v.(string) {
			return nil, false
		}
	}
	return v, true
}

// run sends every row one starting row produces to yield.
func (p *relChain) run(row Record, yield func(Record) (bool, error)) (bool, error) {
	return p.step(0, row, yield)
}

func (p *relChain) step(i int, row Record, yield func(Record) (bool, error)) (bool, error) {
	if i == len(p.joins) {
		return p.final(row, yield)
	}
	j := p.joins[i]
	kind := j.join.Kind
	if kind == "aggregate" {
		values, e := j.aggregate(p.tx, p.budget, row)
		if e != nil {
			return false, e
		}
		next := make(Record, len(row)+len(j.join.Metrics))
		for k, v := range row {
			next[k] = v
		}
		for _, m := range j.join.Metrics {
			next[j.join.As+"."+m.Name] = values[m.Name]
		}
		return p.step(i+1, next, yield)
	}
	rows, e := j.matches(p.tx, p.budget, row, kind == "semi" || kind == "anti")
	if e != nil {
		return false, e
	}
	switch kind {
	case "semi":
		if len(rows) == 0 {
			return true, nil
		}
		return p.step(i+1, row, yield)
	case "anti":
		if len(rows) > 0 {
			return true, nil
		}
		return p.step(i+1, row, yield)
	case "left":
		if len(rows) == 0 {
			next := make(Record, len(row)+1)
			for k, v := range row {
				next[k] = v
			}
			next[j.hidden] = ""
			return p.step(i+1, next, yield)
		}
	}
	for _, r := range rows {
		next := make(Record, len(row)+len(j.fields)+1)
		for k, v := range row {
			next[k] = v
		}
		for field, value := range r {
			if name, ok := j.names[field]; ok {
				next[name] = value
			}
		}
		next[j.hidden] = physical(j.foreign, r)
		more, e := p.step(i+1, next, yield)
		if !more || e != nil {
			return more, e
		}
	}
	return true, nil
}

func (p *relChain) computeValues(row Record, reserve func(int) error) error {
	for i := range p.computed {
		c := &p.computed[i]
		value, e := c.Expr.evalChecked(p.view, row)
		if e != nil {
			return e
		}
		v, e := relTyped(p.fields[i], value)
		if e != nil {
			return e
		}
		if reserve != nil {
			if e := reserve(64 + len(c.Name) + retainedBytes(v)); e != nil {
				return e
			}
		}
		row[c.Name] = v
	}
	return nil
}

func (p *relChain) final(row Record, yield func(Record) (bool, error)) (bool, error) {
	if e := p.computeValues(row, nil); e != nil {
		return false, e
	}
	if !matches(p.view, row, p.filter) {
		return true, nil
	}
	if len(p.distinct) > 0 {
		key := p.distinctKey(row)
		if _, dup := p.seen[key]; dup {
			return true, nil
		}
		if e := p.budget.retain(64 + len(key)); e != nil {
			return false, e
		}
		p.seenSize += 64 + len(key)
		p.seen[key] = struct{}{}
	}
	return yield(row)
}

func (p *relChain) distinctKey(row Record) string {
	values := make([]any, len(p.distinct))
	for i, name := range p.distinct {
		if hidden, ok := row[relHiddenPrefix+name]; ok {
			values[i] = hidden
		} else {
			values[i] = row[name]
		}
	}
	return string(marshal(values))
}

// forget ends a deduplication scope, such as one parent's related rows.
func (p *relChain) forget() {
	p.budget.retained -= p.seenSize
	p.seen, p.seenSize = map[string]struct{}{}, 0
}

// relTyped gives a computed value its declared type.
func relTyped(f Field, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if r, ok := v.(*big.Rat); ok {
		switch f.Type {
		case "integer":
			if !r.IsInt() {
				return nil, Invalid("computed %s requires explicit rounding to integer", f.Name)
			}
			if !r.Num().IsInt64() {
				return nil, fail("resource_limit", "computed %s integer overflow", f.Name)
			}
			v = r.Num().String()
		case "number":
			v, _ = r.Float64()
			if math.IsInf(v.(float64), 0) || math.IsNaN(v.(float64)) {
				return nil, fail("resource_limit", "computed %s numeric overflow", f.Name)
			}
		default:
			return nil, Invalid("computed %s numeric expression requires integer or number type", f.Name)
		}
	}
	if f.Type == "text" {
		s, _ := relText(v)
		return s, nil
	}
	if number, ok := v.(json.Number); ok && f.Type == "integer" {
		v = number.String()
	}
	n, e := normalize(f, v)
	if e != nil {
		return nil, Invalid("computed %s: %v", f.Name, e)
	}
	return n, nil
}

// rowTie orders rows that every sort key ties, by the physical records in them.
func (p *relChain) rowTie(row Record) string { return p.tieFrom(row, 0) }

// tieFrom is rowTie from one alias on, so a row's root can be kept apart from it.
func (p *relChain) tieFrom(row Record, first int) string {
	var b strings.Builder
	for _, alias := range p.aliases[first:] {
		if v, ok := row[relHiddenPrefix+alias].(string); ok {
			b.WriteString(strconv.Itoa(len(v)))
			b.WriteByte(':')
			b.WriteString(v)
		}
		b.WriteByte(0)
	}
	return b.String()
}

func (x *RelExpr) forms() int {
	n := 0
	for _, set := range []bool{x.Op != "", x.Field != "", x.Const != nil, x.Coalesce != nil, x.NullIfEmpty != nil, x.Trim != nil, x.Strip != nil, x.Concat != nil, x.JSON != nil, x.If != nil} {
		if set {
			n++
		}
	}
	return n
}

func (x *RelExpr) visit(use func(string)) {
	if x == nil {
		return
	}
	if x.Field != "" {
		use(x.Field)
	}
	for i := range x.Args {
		x.Args[i].visit(use)
	}
	for i := range x.Coalesce {
		x.Coalesce[i].visit(use)
	}
	for i := range x.Concat {
		x.Concat[i].visit(use)
	}
	for _, child := range []*RelExpr{x.NullIfEmpty, x.Trim, x.Strip, x.JSON, x.Then, x.Else} {
		child.visit(use)
	}
	visitFilterFields(x.If, use)
}

func (p *relChain) checkExpr(x *RelExpr, depth int) error {
	if x == nil {
		return nil
	}
	if depth > 8 {
		return Invalid("computed expressions nest at most 8 levels")
	}
	if x.forms() > 1 {
		return Invalid("a computed expression has exactly one form")
	}
	if x.Field != "" {
		if _, e := p.view.field(x.Field); e != nil {
			return e
		}
	}
	if x.Op == "" && (x.Args != nil || x.Round != nil || x.RoundMode != "") {
		return Invalid("args, round and roundMode require op")
	}
	if x.Op != "" {
		typ, e := numericExprType(p.view, x, depth)
		if e != nil {
			return e
		}
		x.resultType = typ
	}
	for _, list := range [][]RelExpr{x.Args, x.Coalesce, x.Concat} {
		for i := range list {
			if e := p.checkExpr(&list[i], depth+1); e != nil {
				return e
			}
		}
	}
	for _, child := range []*RelExpr{x.NullIfEmpty, x.Trim, x.Strip, x.JSON, x.Then, x.Else} {
		if e := p.checkExpr(child, depth+1); e != nil {
			return e
		}
	}
	if (x.If == nil) && (x.Then != nil || x.Else != nil) {
		return Invalid("then and else require if")
	}
	if x.JSON != nil && x.Path == "" {
		return Invalid("json requires path")
	}
	if x.Strip != nil && x.Chars == "" {
		return Invalid("strip requires chars")
	}
	if x.If != nil {
		return p.checkFilter(x.If)
	}
	return nil
}

func (p *relChain) typeComputed(c *RelComputed) (Field, error) {
	if e := nameOK(c.Name); e != nil {
		return Field{}, e
	}
	if e := p.checkExpr(&c.Expr, 0); e != nil {
		return Field{}, e
	}
	t := c.Type
	if t == "" && c.Expr.Op != "" {
		var e error
		t, e = numericExprType(p.view, &c.Expr, 0)
		if e != nil {
			return Field{}, e
		}
		if t == "rational" {
			return Field{}, Invalid("computed %s requires explicit integer or number type for fractional arithmetic", c.Name)
		}
	}
	if t == "" {
		t = "text"
		if c.Expr.Field != "" {
			f, _ := p.view.field(c.Expr.Field)
			t = f.Type
		}
	}
	if c.Expr.Op != "" {
		inferred, e := numericExprType(p.view, &c.Expr, 0)
		if e != nil {
			return Field{}, e
		}
		if numericType(inferred) && t != "integer" && t != "number" || !numericType(inferred) && t != inferred {
			return Field{}, Invalid("computed %s has incompatible type %s", c.Name, t)
		}
	}
	switch t {
	case "text", "integer", "number", "boolean", "datetime":
	default:
		return Field{}, Invalid("computed %s cannot have type %s", c.Name, t)
	}
	return Field{Name: c.Name, Type: t, Nullable: true}, nil
}

func relText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case nil:
		return "", false
	}
	return strings.TrimSpace(string(marshal(v))), true
}

// eval returns null for a null input, except coalesce, concat (null reads as "")
// and if.
func (x *RelExpr) evalChecked(view Collection, row Record) (any, error) {
	if x == nil {
		return nil, nil
	}
	if x.Op != "" {
		return x.evalNumeric(view, row)
	}
	switch {
	case x.Field != "":
		return row[x.Field], nil
	case x.Const != nil:
		return x.Const, nil
	case x.Coalesce != nil:
		for i := range x.Coalesce {
			v, e := x.Coalesce[i].evalChecked(view, row)
			if e != nil || v != nil {
				return v, e
			}
		}
		return nil, nil
	case x.If != nil:
		if matches(view, row, x.If) {
			return x.Then.evalChecked(view, row)
		}
		return x.Else.evalChecked(view, row)
	case x.Concat != nil:
		parts := make([]string, len(x.Concat))
		size := max(0, len(parts)-1) * len(x.Separator)
		for i := range x.Concat {
			v, e := x.Concat[i].evalChecked(view, row)
			if e != nil {
				return nil, e
			}
			parts[i], _ = relText(v)
			size += len(parts[i])
			if size > MaxBytes {
				return nil, fail("resource_limit", "computed text exceeds 4 MiB")
			}
		}
		return strings.Join(parts, x.Separator), nil
	}
	var child *RelExpr
	switch {
	case x.NullIfEmpty != nil:
		child = x.NullIfEmpty
	case x.Trim != nil:
		child = x.Trim
	case x.Strip != nil:
		child = x.Strip
	case x.JSON != nil:
		child = x.JSON
	default:
		return nil, nil
	}
	v, e := child.evalChecked(view, row)
	if e != nil {
		return nil, e
	}
	switch {
	case x.NullIfEmpty != nil:
		if v == "" {
			return nil, nil
		}
		return v, nil
	case x.JSON != nil:
		return relJSON(v, x.Path), nil
	}
	text, ok := relText(v)
	if !ok {
		return nil, nil
	}
	if x.Trim != nil {
		return strings.Trim(text, " "), nil
	}
	return strings.Map(func(c rune) rune {
		if strings.ContainsRune(x.Chars, c) {
			return -1
		}
		return c
	}, text), nil
}

// relJSON reads a dotted path. Text that is not valid JSON has no fields.
func relJSON(v any, path string) any {
	if s, ok := v.(string); ok {
		var parsed any
		if decode([]byte(s), &parsed) != nil {
			return nil
		}
		v = parsed
	}
	for _, part := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[part]
	}
	switch v.(type) {
	case map[string]any, []any:
		return string(marshal(v))
	}
	return v
}

// keysetFrom bounds a query to rows after one row's values under its order.
func keysetFrom(c Collection, order []Order, values Record) *Filter {
	q := Query{OrderBy: order}
	return keysetFilter(c, &q, values)
}

// eachRoot reads the root records in order, in batches, on the tx's snapshot.
func eachRoot(tx transaction, c Collection, where *Filter, state *existsState, order []Order, fields []string, budget *relBudget, yield func(Record) (bool, error)) error {
	var after Record
	for {
		q := Query{Where: where, OrderBy: order, Select: fields, after: after, Limit: relBatch}
		if after != nil {
			q.Where = and(where, keysetFrom(c, order, after))
		}
		rows, e := tx.find(c, q, relBatch)
		if e == nil {
			e = state.failure()
		}
		if e != nil {
			return e
		}
		if e := budget.spend(int64(len(rows))); e != nil {
			return e
		}
		for _, r := range rows {
			more, e := yield(r)
			if !more || e != nil {
				return e
			}
		}
		if len(rows) < relBatch {
			return nil
		}
		last := rows[len(rows)-1]
		after = Record{}
		for _, o := range order {
			after[o.Field] = last[o.Field]
		}
	}
}

// unorderedRootReader marks a transaction that reads every matching root once, in
// whatever order costs least, more cheaply than in batches of primary key order, and
// that can read other records while that scan is open.
type unorderedRootReader interface {
	unorderedRoots() bool
}

// eachRootOnce gives yield every matching root once, in no particular order, on the
// tx's snapshot. Batches re-read what an index can't return in primary key order and
// test each row against the last key, so they cost more than the scan itself.
func eachRootOnce(tx transaction, c Collection, where *Filter, state *existsState, order []Order, fields []string, budget *relBudget, yield func(Record) (bool, error)) error {
	reader, unordered := tx.(unorderedRootReader)
	scanner, scans := tx.(recordScanner)
	if !unordered || !scans || !reader.unorderedRoots() {
		return eachRoot(tx, c, where, state, order, fields, budget, yield)
	}
	q := Query{Where: where, Select: fields}
	var failed error
	e := scanner.scanRecords(c, q, fields, func(r Record) bool {
		// A failure under not_exists reads as a match, so check before the row counts.
		if failed = state.failure(); failed != nil {
			return false
		}
		if failed = budget.spend(1); failed != nil {
			return false
		}
		more, e := yield(retainSelected(r, q))
		failed = e
		return more && e == nil
	})
	if failed != nil {
		return failed
	}
	if e != nil {
		return e
	}
	return state.failure()
}

// rootView types the root's fields as "alias.field".
func rootView(c Collection, alias string) Collection {
	view := Collection{Name: c.Name, PrimaryKey: c.PrimaryKey, Version: c.Version}
	for _, f := range c.Fields {
		f.Name = alias + "." + f.Name
		view.Fields = append(view.Fields, f)
	}
	return view
}

func rootRow(c Collection, alias string, r Record) Record {
	row := make(Record, len(c.Fields)+1)
	for _, f := range c.Fields {
		if v, ok := r[f.Name]; ok {
			row[alias+"."+f.Name] = v
		}
	}
	row[relHiddenPrefix+alias] = physical(c, r)
	return row
}

func relAlias(r Request) (string, error) {
	if r.As == "" {
		return relDefaultAlias, nil
	}
	return r.As, nameOK(r.As)
}

func checkOrder(view Collection, order []Order) error {
	if len(order) > 8 {
		return Invalid("at most 8 ordering fields")
	}
	for i, o := range order {
		f, e := view.field(o.Field)
		if e != nil {
			return e
		}
		if f.Type == "json" {
			return Invalid("order fields must be scalars")
		}
		if o.Direction == "" {
			order[i].Direction = "asc"
		} else if o.Direction != "asc" && o.Direction != "desc" {
			return Invalid("invalid order direction")
		}
	}
	return nil
}

func relRequestHash(c Collection, r Request) string {
	r.Cursor = ""
	r.Offset = 0
	r.TimeoutMS = 0
	digest := sha256.Sum256(marshal(struct {
		C string
		R Request
	}{c.Name, r}))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func relCursor(c Collection, r Request, values Record, key []byte) string {
	return signCursor(key, cursorData{Hash: relRequestHash(c, r), Version: c.Version, Expires: time.Now().Add(relCursorTTL).Unix(), Values: values})
}

func relReadCursor(c Collection, r Request, key []byte) (Record, error) {
	p, e := readCursor(r.Cursor, key)
	if e != nil {
		return nil, e
	}
	if p.Version != c.Version || p.Hash != relRequestHash(c, r) || p.Expires < time.Now().Unix() {
		return nil, Invalid("expired or incompatible cursor")
	}
	return p.Values, nil
}

func relPaging(r *Request) error {
	if r.Limit == 0 {
		r.Limit = 100
	}
	if r.Limit < 1 || r.Limit > MaxRecords {
		return Invalid("limit must be 1–1000")
	}
	if r.Offset < 0 || r.Offset > maxRelOffset {
		return Invalid("offset must be 0–%d", maxRelOffset)
	}
	if r.Offset > 0 && r.Cursor != "" {
		return Invalid("use offset or cursor, not both")
	}
	return nil
}

func project(row Record, names []string) Record {
	out := make(Record, len(names))
	for _, n := range names {
		out[n] = row[n]
	}
	return out
}

// outputNames is the select list, or every view field.
func outputNames(p *relChain, sel []string) ([]string, error) {
	if len(sel) == 0 {
		names := make([]string, 0, len(p.view.Fields))
		for _, f := range p.view.Fields {
			names = append(names, f.Name)
		}
		p.useAll()
		return names, nil
	}
	seen := map[string]bool{}
	for _, name := range sel {
		if _, e := p.view.field(name); e != nil {
			return nil, e
		}
		if seen[name] {
			return nil, Invalid("duplicate selected field")
		}
		seen[name] = true
		p.use(name)
	}
	return sel, nil
}

// rootFields lists the root's stored fields a chain uses.
func rootFields(c Collection, alias string, p *relChain) []string {
	out := append([]string{}, c.PrimaryKey...)
	have := map[string]bool{}
	for _, n := range out {
		have[n] = true
	}
	for _, f := range c.Fields {
		if !have[f.Name] && p.used[alias+"."+f.Name] {
			out = append(out, f.Name)
		}
	}
	return out
}

// relInput prepares the shared stages of joined_page and joined_aggregate.
func relInput(tx transaction, c Collection, r *Request, budget *relBudget) (*relChain, string, *existsState, error) {
	alias, e := relAlias(*r)
	if e != nil {
		return nil, "", nil, e
	}
	n := 0
	if e := validateFilter(c, r.Where, 0, &n); e != nil {
		return nil, "", nil, e
	}
	state, e := prepareExists(tx, c, r.Where)
	if e != nil {
		return nil, "", nil, e
	}
	p, e := newRelChain(tx, rootView(c, alias), []string{alias}, budget, r.Joins, r.Computed, r.Filter, r.Distinct)
	if e != nil {
		return nil, "", nil, e
	}
	if r.Pick != nil {
		if len(r.Pick.By) == 0 || len(r.Pick.By) > 8 {
			return nil, "", nil, Invalid("pick requires 1–8 by fields")
		}
		for _, name := range r.Pick.By {
			if _, e := p.view.field(name); e != nil {
				return nil, "", nil, e
			}
			p.use(name)
		}
		if len(r.Pick.OrderBy) == 0 {
			return nil, "", nil, Invalid("pick requires orderBy")
		}
		if e := checkOrder(p.view, r.Pick.OrderBy); e != nil {
			return nil, "", nil, e
		}
		for _, o := range r.Pick.OrderBy {
			p.use(o.Field)
		}
		if e := p.checkFilter(r.Pick.Where); e != nil {
			return nil, "", nil, e
		}
		visitFilterFields(r.Pick.Where, p.use)
	}
	return p, alias, state, nil
}

// relStream runs the whole input, sending each surviving row to yield: rows pass
// through pick last, so it sees joined, filtered, deduplicated rows. With keep, a
// picked row holds only those fields until it is yielded; pick's own order fields and
// the row's identity must be among them. anyOrder says the caller orders the rows
// itself. Distinct still reads in primary key order, because the first row it sees wins.
func relStream(tx transaction, c Collection, r Request, alias string, state *existsState, p *relChain, budget *relBudget, keep []string, anyOrder bool, yield func(Record) (bool, error)) error {
	order := []Order{}
	for _, pk := range c.PrimaryKey {
		order = append(order, Order{pk, "asc"})
	}
	sink := yield
	var best map[string]Record
	bestBytes := 0
	if r.Pick != nil {
		best = map[string]Record{}
		pickFields := relFields(p, r.Pick.OrderBy)
		sink = func(row Record) (bool, error) {
			if !matches(p.view, row, r.Pick.Where) {
				return true, nil
			}
			key := string(marshal(project(row, r.Pick.By)))
			old, ok := best[key]
			if ok && !relLess(p, row, old, r.Pick.OrderBy, pickFields) {
				return true, nil
			}
			if !ok {
				bestBytes += len(key)
				if e := budget.retain(len(key)); e != nil {
					return false, e
				}
			} else {
				n := retainedBytes(old)
				bestBytes -= n
				budget.retained -= n
			}
			if keep != nil {
				row = project(row, keep)
			}
			n := retainedBytes(row)
			bestBytes += n
			if e := budget.retain(n); e != nil {
				return false, e
			}
			best[key] = row
			return true, nil
		}
	}
	read := eachRoot
	if anyOrder && len(p.distinct) == 0 {
		read = eachRootOnce
	}
	e := read(tx, c, r.Where, state, order, rootFields(c, alias, p), budget, func(rec Record) (bool, error) {
		return p.run(rootRow(c, alias, rec), sink)
	})
	if e != nil || best == nil {
		return e
	}
	defer func() { budget.retained -= bestBytes }()
	keys := make([]string, 0, len(best))
	for key := range best {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		more, e := yield(best[key])
		if !more || e != nil {
			return e
		}
	}
	return nil
}

// relFields resolves an order's fields once. A lookup walks the collection's fields, so
// doing it per comparison costs more than the comparison on a wide collection.
func relFields(p *relChain, order []Order) []Field {
	fields := make([]Field, len(order))
	for i, o := range order {
		fields[i], _ = p.view.field(o.Field)
	}
	return fields
}

func relLess(p *relChain, a, b Record, order []Order, fields []Field) bool {
	for i, o := range order {
		if d := compare(fields[i], a[o.Field], b[o.Field]); d != 0 {
			if o.Direction == "desc" {
				return d > 0
			}
			return d < 0
		}
	}
	return p.rowTie(a) < p.rowTie(b)
}

// A page item holds only what paging needs until the page is known: its sort values,
// its tie and which rows are its legs. relItemBytes and relLegBytes estimate the
// overhead of keeping one, including their places in the map and slices.
const (
	relItemBytes = 160
	relLegBytes  = 64
)

type relItem struct {
	sort []any
	tie  string
	key  Record
	legs []relLeg
	rows []Record
}

// relLeg identifies one row of an item: its root record's primary key, and the joined
// records it came from.
type relLeg struct{ root, joined string }

func (p *relChain) leg(c Collection, alias string, row Record) (relLeg, error) {
	key := make(Record, len(c.PrimaryKey))
	for _, name := range c.PrimaryKey {
		key[name] = row[alias+"."+name]
	}
	root, e := primary(c, key)
	return relLeg{root, p.tieFrom(row, 1)}, e
}

// relReadLegs fills a page's items with their rows, on the snapshot the first pass
// read. That pass already decided which rows survive distinct and pick, so neither runs
// again: only the page's root records are read and joined, and only their legs are kept.
func relReadLegs(tx transaction, c Collection, alias string, p *relChain, budget *relBudget, page []*relItem, groupFields []string) error {
	p.forget()
	p.distinct = nil
	wanted := map[string]*relItem{}
	roots := []string{}
	seen := map[string]bool{}
	legs := 0
	for _, item := range page {
		for _, leg := range item.legs {
			id := leg.root + "\x00" + leg.joined
			if e := budget.retain(relLegBytes + len(id)); e != nil {
				return e
			}
			wanted[id] = item
			legs++
			if !seen[leg.root] {
				seen[leg.root] = true
				roots = append(roots, leg.root)
			}
		}
		item.legs = nil
	}
	fields := rootFields(c, alias, p)
	found := 0
	for _, root := range roots {
		if e := budget.spend(1); e != nil {
			return e
		}
		rec, e := tx.get(c, root)
		if e != nil {
			return e
		}
		if rec == nil {
			return fail("internal", "joined page lost a record within one snapshot")
		}
		used := make(Record, len(fields))
		for _, name := range fields {
			if v, ok := rec[name]; ok {
				used[name] = v
			}
		}
		_, e = p.run(rootRow(c, alias, used), func(row Record) (bool, error) {
			item := wanted[root+"\x00"+p.tieFrom(row, 1)]
			if item == nil {
				return true, nil
			}
			if e := budget.retain(retainedBytes(row)); e != nil {
				return false, e
			}
			if item.key == nil {
				item.key = project(row, groupFields)
			}
			item.rows = append(item.rows, row)
			found++
			return true, nil
		})
		if e != nil {
			return e
		}
	}
	if found != legs {
		return fail("internal", "joined page found %d of its %d rows within one snapshot", found, legs)
	}
	return nil
}

func relItemCompare(order []Order, fields []Field, a, b *relItem) int {
	for i, o := range order {
		if d := compare(fields[i], a.sort[i], b.sort[i]); d != 0 {
			if o.Direction == "desc" {
				return -d
			}
			return d
		}
	}
	return strings.Compare(a.tie, b.tie)
}

// executeJoinedPage counts and pages at the requested grain: one row, or one
// group of rows, is one item.
func executeJoinedPage(tx transaction, c Collection, r Request, key []byte) (any, error) {
	if e := relPaging(&r); e != nil {
		return nil, e
	}
	budget := &relBudget{ctx: relContext(tx)}
	p, alias, state, e := relInput(tx, c, &r, budget)
	if e != nil {
		return nil, e
	}
	if e := checkOrder(p.view, r.OrderBy); e != nil {
		return nil, e
	}
	names, e := outputNames(p, r.Select)
	if e != nil {
		return nil, e
	}
	for _, o := range r.OrderBy {
		p.use(o.Field)
	}
	var itemOrder []Order
	groupFields := []string{}
	if r.Group != nil {
		if len(r.Group.By) == 0 || len(r.Group.By) > 8 {
			return nil, Invalid("group requires 1–8 by fields")
		}
		groupFields = r.Group.By
		for _, name := range groupFields {
			if _, e := p.view.field(name); e != nil {
				return nil, e
			}
			p.use(name)
		}
		for _, o := range r.Group.OrderBy {
			if o.Agg != "" && o.Agg != "min" && o.Agg != "max" {
				return nil, Invalid("group order agg must be min or max")
			}
			if o.Agg == "" && !contains(groupFields, o.Field) {
				return nil, Invalid("group order %s needs agg min or max, or must be a group field", o.Field)
			}
			itemOrder = append(itemOrder, Order{o.Field, o.Direction})
			p.use(o.Field)
		}
	} else {
		itemOrder = r.OrderBy
	}
	if e := checkOrder(p.view, itemOrder); e != nil {
		return nil, e
	}
	p.finish()
	itemFields := relFields(p, itemOrder)
	var keep []string
	if r.Pick != nil {
		keep = append(keep, groupFields...)
		for _, o := range r.Pick.OrderBy {
			keep = append(keep, o.Field)
		}
		for _, o := range itemOrder {
			keep = append(keep, o.Field)
		}
		for _, name := range c.PrimaryKey {
			keep = append(keep, alias+"."+name)
		}
		for _, name := range p.aliases {
			keep = append(keep, relHiddenPrefix+name)
		}
	}
	// The first pass keeps each item's order and identity, never its rows, however
	// narrow or wide the rows are.
	groups := map[string]*relItem{}
	items := []*relItem{}
	itemBytes := 0
	hold := func(n int) error {
		itemBytes += n
		return budget.retain(n)
	}
	e = relStream(tx, c, r, alias, state, p, budget, keep, true, func(row Record) (bool, error) {
		leg, e := p.leg(c, alias, row)
		if e != nil {
			return false, e
		}
		if e := hold(relLegBytes + len(leg.root) + len(leg.joined)); e != nil {
			return false, e
		}
		if r.Group == nil {
			s := make([]any, len(itemOrder))
			for i, o := range itemOrder {
				s[i] = row[o.Field]
			}
			tie := p.rowTie(row)
			if e := hold(relItemBytes + len(tie) + retainedBytes(s)); e != nil {
				return false, e
			}
			items = append(items, &relItem{sort: s, tie: tie, legs: []relLeg{leg}})
			return true, nil
		}
		tie := string(marshal(project(row, groupFields)))
		g := groups[tie]
		if g == nil {
			g = &relItem{sort: make([]any, len(r.Group.OrderBy)), tie: tie}
			for i, o := range r.Group.OrderBy {
				g.sort[i] = row[o.Field]
			}
			if e := hold(relItemBytes + len(tie) + retainedBytes(g.sort)); e != nil {
				return false, e
			}
			groups[tie] = g
			items = append(items, g)
		}
		g.legs = append(g.legs, leg)
		for i, o := range r.Group.OrderBy {
			d := compare(itemFields[i], row[o.Field], g.sort[i])
			if o.Agg == "min" && d < 0 || o.Agg == "max" && d > 0 {
				if e := hold(retainedBytes(row[o.Field]) - retainedBytes(g.sort[i])); e != nil {
					return false, e
				}
				g.sort[i] = row[o.Field]
			}
		}
		return true, nil
	})
	if e != nil {
		return nil, e
	}
	sort.Slice(items, func(i, j int) bool { return relItemCompare(itemOrder, itemFields, items[i], items[j]) < 0 })
	start := r.Offset
	if r.Cursor != "" {
		values, e := relReadCursor(c, r, key)
		if e != nil {
			return nil, e
		}
		at, e := relCursorItem(p, itemOrder, values)
		if e != nil {
			return nil, e
		}
		start = sort.Search(len(items), func(i int) bool { return relItemCompare(itemOrder, itemFields, items[i], at) > 0 })
	}
	if start > len(items) {
		start = len(items)
	}
	selected := items[start:]
	more := len(selected) > r.Limit
	if more {
		selected = selected[:r.Limit]
	}
	// Past here only the page matters, so everything else stops counting.
	total := len(items)
	selected = append([]*relItem{}, selected...)
	items, groups = nil, nil
	budget.retained -= itemBytes
	if e := relReadLegs(tx, c, alias, p, budget, selected, groupFields); e != nil {
		return nil, e
	}
	out := []any{}
	size := 0
	var last *relItem
	rowFields := relFields(p, r.OrderBy)
	for _, item := range selected {
		sort.SliceStable(item.rows, func(i, j int) bool { return relLess(p, item.rows[i], item.rows[j], r.OrderBy, rowFields) })
		rows := make([]Record, len(item.rows))
		for i, row := range item.rows {
			rows[i] = project(row, names)
		}
		var entry any = rows[0]
		if r.Group != nil {
			entry = map[string]any{"key": item.key, "rows": rows}
		}
		n := len(marshal(entry))
		if size+n > MaxBytes {
			if last == nil {
				return nil, fail("resource_limit", "one item exceeds 4 MiB; select fewer fields")
			}
			more = true
			break
		}
		size += n
		out = append(out, entry)
		last = item
	}
	result := map[string]any{"total": strconv.Itoa(total), "hasMore": more}
	if r.Group != nil {
		result["groups"] = out
	} else {
		result["records"] = out
	}
	if more && last != nil {
		result["nextCursor"] = relCursor(c, r, Record{"sort": last.sort, "tie": last.tie}, key)
	}
	return result, nil
}

// relCursorItem restores a cursor's position with the sort values' stored types.
func relCursorItem(p *relChain, order []Order, values Record) (*relItem, error) {
	raw, ok := values["sort"].([]any)
	tie, tok := values["tie"].(string)
	if !ok || !tok || len(raw) != len(order) {
		return nil, Invalid("invalid cursor")
	}
	at := &relItem{sort: make([]any, len(raw)), tie: tie}
	for i, v := range raw {
		if v == nil {
			continue
		}
		f, _ := p.view.field(order[i].Field)
		f.Nullable = true
		n, e := normalize(f, v)
		if e != nil {
			return nil, Invalid("invalid cursor")
		}
		at.sort[i] = n
	}
	return at, nil
}

// executeJoinedAggregate groups the joined, deduplicated rows with the same
// accumulators as aggregate: exact integers, and null, not zero, over no values.
func executeJoinedAggregate(tx transaction, c Collection, r Request) (any, error) {
	if r.Relation != nil {
		return executeRelatedReadMode(tx, c, r, nil, true)
	}

	budget := &relBudget{ctx: relContext(tx)}
	base, e := relAggregateInput(tx, c, &r, budget)
	if e != nil {
		return nil, e
	}
	view, stream := base.view, base.stream
	if r.Rollup != nil {
		view, stream, e = relRollup(tx, base, r.Rollup, budget)
		if e != nil {
			return nil, e
		}
	} else {
		base.use(r.GroupBy, r.Metrics)
	}
	var enriched *relationAnalytics
	if len(r.Lookups) > 0 || len(r.Buckets) > 0 {
		for _, f := range base.view.Fields {
			base.use(nil, []Metric{{Name: "input", Op: "min", Field: f.Name}})
		}
		var err error
		enriched, err = prepareRelationAnalytics(tx, view, r)
		if err != nil {
			return nil, err
		}
		view = enriched.view
		prior := stream
		stream = func(yield func(Record) (bool, error)) error { return enriched.stream(prior, yield, budget) }
	}
	base.finish()
	if e := noExistsInMetrics(r.Metrics); e != nil {
		return nil, e
	}
	input := Request{GroupBy: r.GroupBy, Metrics: r.Metrics}
	input.OrderBy, input.Limit = r.OrderBy, r.Limit
	outSchema, e := validateAggregate(view, &input)
	if e != nil {
		return nil, e
	}
	if e := validateDerived(outSchema, r.Derived); e != nil {
		return nil, e
	}

	rows, e := relRunAggregate(budget, view, input, outSchema, stream)
	if e != nil {
		return nil, e
	}
	return finishAggregate(rows, input.Limit, r.Derived, budget.retain)
}

// executeRelatedRead selects each parent's related rows in a fixed order, takes
// the first N, and only then tests Match. Nothing past N can match.
func executeRelatedRead(tx transaction, c Collection, r Request, key []byte) (any, error) {
	return executeRelatedReadMode(tx, c, r, key, false)
}

// A related aggregate consumes each matching parent once. Related entries only
// determine eligibility: their number never weights the parent's metrics.
func executeRelatedReadMode(tx transaction, c Collection, r Request, key []byte, aggregate bool) (any, error) {
	finalOrder, finalLimit := r.OrderBy, r.Limit
	if aggregate {
		r.OrderBy, r.Select, r.Limit = nil, nil, 1
	}

	rel := r.Relation
	if rel == nil {
		return nil, Invalid("related_read requires relation")
	}
	if e := nameOK(rel.As); e != nil {
		return nil, e
	}
	if rel.Limit < 1 || rel.Limit > MaxRecords {
		return nil, Invalid("relation limit must be 1–1000")
	}
	if len(rel.Sources) == 0 || len(rel.Sources) > maxRelSources {
		return nil, Invalid("relation requires 1–%d sources", maxRelSources)
	}
	if r.Offset < 0 || r.Offset > maxRelOffset {
		return nil, Invalid("offset must be 0–%d", maxRelOffset)
	}
	if r.Offset > 0 && r.Cursor != "" {
		return nil, Invalid("use offset or cursor, not both")
	}
	if e := validateQuery(c, &r.Query); e != nil {
		return nil, e
	}
	state, e := prepareExists(tx, c, r.Where)
	if e != nil {
		return nil, e
	}
	alias, e := relAlias(r)
	if e != nil {
		return nil, e
	}
	budget := &relBudget{ctx: relContext(tx)}
	chains := make([]*relChain, len(rel.Sources))
	matchView := Collection{Name: rel.As, Fields: []Field{{Name: "source", Type: "text"}}}
	for i := range rel.Sources {
		s := &rel.Sources[i]
		if e := nameOK(s.Name); e != nil {
			return nil, e
		}
		p, e := newRelChain(tx, rootView(c, alias), []string{alias}, budget, s.Joins, s.Computed, s.Filter, s.Distinct)
		if e != nil {
			return nil, e
		}
		if e := checkOrder(p.view, s.OrderBy); e != nil {
			return nil, e
		}
		for _, o := range s.OrderBy {
			p.use(o.Field)
		}
		if len(s.Select) == 0 {
			return nil, Invalid("source %s requires select", s.Name)
		}
		if _, e := outputNames(p, s.Select); e != nil {
			return nil, e
		}
		for _, name := range s.Select {
			f, _ := p.view.field(name)
			if name == "source" {
				return nil, Invalid("source is a reserved context field")
			}
			if old, e := matchView.field(name); e == nil {
				if old.Type != f.Type {
					return nil, Invalid("context field %s has types %s and %s", name, old.Type, f.Type)
				}
				continue
			}
			f.Nullable = true
			matchView.Fields = append(matchView.Fields, f)
		}
		p.finish()
		chains[i] = p
	}
	if hasExists(r.Match) {
		return nil, Invalid("match cannot use exists")
	}
	terms := 0
	if e := validateFilter(matchView, r.Match, 0, &terms); e != nil {
		return nil, e
	}
	parentFields := append([]string{}, r.Select...)
	if len(r.Select) == 0 {
		parentFields = nil
	} else {
		for _, o := range r.OrderBy {
			if !contains(parentFields, o.Field) {
				parentFields = append(parentFields, o.Field)
			}
		}
		for _, p := range chains {
			for _, name := range rootFields(c, alias, p) {
				if !contains(parentFields, name) {
					parentFields = append(parentFields, name)
				}
			}
		}
	}
	if _, e := c.field(rel.As); e == nil || contains(r.Select, rel.As) {
		return nil, Invalid("relation %s has the name of a field", rel.As)
	}
	context := func(parent Record) ([]Record, error) {
		out := []Record{}
		base := rootRow(c, alias, parent)
		for i := range chains {
			if len(out) >= rel.Limit {
				break
			}
			p, s := chains[i], &rel.Sources[i]
			rows := []Record{}
			size := 0
			if _, e := p.run(base, func(row Record) (bool, error) {
				n := retainedBytes(row)
				size += n
				rows = append(rows, row)
				return true, budget.retain(n)
			}); e != nil {
				return nil, e
			}
			p.forget()
			budget.retained -= size
			fields := relFields(p, s.OrderBy)
			sort.SliceStable(rows, func(a, b int) bool { return relLess(p, rows[a], rows[b], s.OrderBy, fields) })
			for _, row := range rows {
				if len(out) >= rel.Limit {
					break
				}
				entry := project(row, s.Select)
				entry["source"] = s.Name
				out = append(out, entry)
			}
		}
		return out, nil
	}
	if aggregate {
		return aggregateRelatedParents(tx, c, r, alias, budget, state, context, matchView, finalOrder, finalLimit, chains)
	}
	var after Record
	if r.Cursor != "" {
		if after, e = relReadCursor(c, r, key); e != nil {
			return nil, e
		}
	}
	where := r.Where
	stream := Query{OrderBy: r.OrderBy}
	var resume *Filter
	if after != nil {
		// A total counts the parents before the cursor too, so only then is the
		// resume position tested row by row.
		resume = keysetFilter(c, &stream, after)
		if !r.Count {
			where, resume = and(where, resume), nil
		}
	}
	matched, skipped := 0, 0
	records := []Record{}
	var last Record
	more, size := false, 0
	yield := func(parent Record) (bool, error) {
		before := resume != nil && !matches(c, parent, resume)
		onPage := !before && !more && len(records) < r.Limit && skipped >= r.Offset
		var entries []Record
		if r.Match != nil || onPage {
			var e error
			if entries, e = context(parent); e != nil {
				return false, e
			}
		}
		if r.Match != nil {
			hit := false
			for _, entry := range entries {
				if matches(matchView, entry, r.Match) {
					hit = true
					break
				}
			}
			if !hit {
				return true, nil
			}
		}
		matched++
		if before {
			return true, nil
		}
		if skipped < r.Offset {
			skipped++
			return true, nil
		}
		if !onPage {
			more = true
			return r.Count, nil
		}
		output := parent
		if len(r.Select) > 0 {
			output = project(parent, r.Select)
		}
		output[rel.As] = entries
		n := len(marshal(output))
		if size+n > MaxBytes {
			if last == nil {
				return false, fail("resource_limit", "one record exceeds 4 MiB; lower the limit or select fewer fields")
			}
			more = true
			return r.Count, nil
		}
		size += n
		records = append(records, output)
		last = project(parent, orderNames(r.OrderBy))
		return true, nil
	}
	if e := eachRoot(tx, c, where, state, r.OrderBy, parentFields, budget, yield); e != nil {
		return nil, e
	}
	result := map[string]any{"records": records, "hasMore": more}
	if r.Count {
		result["total"] = strconv.Itoa(matched)
	}
	if more && last != nil {
		result["nextCursor"] = relCursor(c, r, last, key)
	}
	return result, nil
}

func orderNames(order []Order) []string {
	out := make([]string, len(order))
	for i, o := range order {
		out[i] = o.Field
	}
	return out
}

// relDivide is exact to the nearest float when both sides are integers. A null
// side or a zero denominator is null, which differs from a zero numerator's 0.
func relDivide(a, b any, round *int) any {
	x, ok := relRat(a)
	y, oky := relRat(b)
	if !ok || !oky || y.Sign() == 0 {
		return nil
	}
	q := new(big.Rat).Quo(x, y)
	if round != nil {
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(*round)), nil)
		scaled := new(big.Rat).Mul(q, new(big.Rat).SetInt(scale))
		half := big.NewRat(1, 2)
		if scaled.Sign() < 0 {
			half.Neg(half)
		}
		scaled.Add(scaled, half)
		whole := new(big.Int).Quo(scaled.Num(), scaled.Denom())
		q = new(big.Rat).SetFrac(whole, scale)
	}
	v, _ := q.Float64()
	return v
}

func relRat(v any) (*big.Rat, bool) {
	switch x := v.(type) {
	case string:
		return new(big.Rat).SetString(x)
	case float64:
		r := new(big.Rat).SetFloat64(x)
		return r, r != nil
	case json.Number:
		return new(big.Rat).SetString(x.String())
	}
	return nil, false
}

// relAggInput is the rows an aggregate reads: one joined chain, or branches.
type relAggInput struct {
	view   Collection
	use    func(groupBy []string, metrics []Metric)
	finish func()
	stream func(yield func(Record) (bool, error)) error
}

func noExistsInMetrics(metrics []Metric) error {
	for _, m := range metrics {
		if hasExists(m.Where) {
			return Invalid("a metric filter cannot use exists; add a semi or anti join")
		}
	}
	return nil
}

// relRunAggregate sends a stream of rows through the aggregate accumulators.
func relRunAggregate(budget *relBudget, view Collection, input Request, out Collection, stream func(func(Record) (bool, error)) error) ([]Record, error) {
	return aggregateRecordsWithBudget(budget.ctx, view, input, out, MaxQueryWorkBytes, func(_ Query, _ []string, visit func(Record, []byte) bool) error {
		return stream(func(row Record) (bool, error) { return visit(row, nil), nil })
	})
}

func relAggregateInput(tx transaction, c Collection, r *Request, budget *relBudget) (*relAggInput, error) {
	if len(r.Branches) > 0 {
		return relBranches(tx, r, budget)
	}
	p, alias, state, e := relInput(tx, c, r, budget)
	if e != nil {
		return nil, e
	}
	req := *r
	return &relAggInput{
		view: p.view,
		use: func(groupBy []string, metrics []Metric) {
			for _, name := range groupBy {
				p.use(name)
			}
			for _, m := range metrics {
				if m.Field != "" {
					p.use(m.Field)
				}
				visitFilterFields(m.Where, p.use)
			}
		},
		finish: p.finish,
		stream: func(yield func(Record) (bool, error)) error {
			return relStream(tx, c, req, alias, state, p, budget, nil, false, yield)
		},
	}, nil
}

// relBranches concatenates independently joined inputs with one output shape.
// Top-level distinct then removes what several branches found.
func relBranches(tx transaction, r *Request, budget *relBudget) (*relAggInput, error) {
	if len(r.Branches) > maxRelSources {
		return nil, Invalid("at most %d branches", maxRelSources)
	}
	if r.Collection != "" || r.Where != nil || r.As != "" || len(r.Joins) > 0 || len(r.Computed) > 0 || r.Filter != nil || r.Pick != nil {
		return nil, Invalid("with branches, collection, where, joins, computed, filter and pick belong in each branch")
	}
	type branch struct {
		c     Collection
		req   Request
		alias string
		state *existsState
		p     *relChain
		names []string
	}
	view := Collection{Name: "branches"}
	branches := make([]*branch, len(r.Branches))
	for i, b := range r.Branches {
		if e := nameOK(b.Collection); e != nil {
			return nil, e
		}
		c, e := tx.collection(b.Collection)
		if e != nil {
			return nil, e
		}
		if len(b.Select) == 0 {
			return nil, Invalid("branch %d requires select", i)
		}
		req := Request{As: b.As, Joins: b.Joins, Computed: b.Computed, Filter: b.Filter, Distinct: b.Distinct, Pick: b.Pick}
		req.Where = b.Where
		p, alias, state, e := relInput(tx, c, &req, budget)
		if e != nil {
			return nil, e
		}
		if _, e := outputNames(p, b.Select); e != nil {
			return nil, e
		}
		if i > 0 && len(b.Select) != len(view.Fields) {
			return nil, Invalid("every branch must select the same names")
		}
		for _, name := range b.Select {
			f, _ := p.view.field(name)
			old, e := view.field(name)
			switch {
			case e == nil && old.Type != f.Type:
				return nil, Invalid("branch field %s has types %s and %s", name, old.Type, f.Type)
			case e != nil && i > 0:
				return nil, Invalid("every branch must select the same names")
			case e != nil:
				f.Nullable = true
				view.Fields = append(view.Fields, f)
			}
		}
		p.finish()
		branches[i] = &branch{c, req, alias, state, p, b.Select}
	}
	for _, name := range r.Distinct {
		if _, e := view.field(name); e != nil {
			return nil, Invalid("distinct over branches needs a selected field; %s is not one", name)
		}
	}
	seen := map[string]struct{}{}
	return &relAggInput{
		view:   view,
		use:    func([]string, []Metric) {},
		finish: func() {},
		stream: func(yield func(Record) (bool, error)) error {
			stopped := false
			for _, b := range branches {
				if stopped {
					return nil
				}
				e := relStream(tx, b.c, b.req, b.alias, b.state, b.p, budget, nil, false, func(row Record) (bool, error) {
					out := project(row, b.names)
					if len(r.Distinct) > 0 {
						key := string(marshal(project(out, r.Distinct)))
						if _, dup := seen[key]; dup {
							return true, nil
						}
						if e := budget.retain(64 + len(key)); e != nil {
							return false, e
						}
						seen[key] = struct{}{}
					}
					more, e := yield(out)
					stopped = !more
					return more, e
				})
				if e != nil {
					return e
				}
			}
			return nil
		},
	}, nil
}

// relRollup aggregates by key first, then gives the outer aggregate one row per
// key, with computed values and a filter over the rollup's own metrics.
func relRollup(tx transaction, base *relAggInput, rollup *RelRollup, budget *relBudget) (Collection, func(func(Record) (bool, error)) error, error) {
	var none Collection
	if len(rollup.By) == 0 || len(rollup.By) > 8 {
		return none, nil, Invalid("rollup requires 1–8 by fields")
	}
	if len(rollup.Metrics) == 0 {
		return none, nil, Invalid("rollup requires metrics")
	}
	if e := noExistsInMetrics(rollup.Metrics); e != nil {
		return none, nil, e
	}
	input := Request{GroupBy: rollup.By, Metrics: rollup.Metrics}
	out, e := validateAggregate(base.view, &input)
	if e != nil {
		return none, nil, e
	}
	base.use(rollup.By, rollup.Metrics)
	input.Limit = math.MaxInt32
	outer, e := newRelChain(tx, out, nil, budget, nil, rollup.Computed, rollup.Filter, nil)
	if e != nil {
		return none, nil, e
	}
	return outer.view, func(yield func(Record) (bool, error)) error {
		rows, e := relRunAggregate(budget, base.view, input, out, base.stream)
		if e != nil {
			return e
		}
		for _, row := range rows {
			if more, e := outer.run(row, yield); !more || e != nil {
				return e
			}
		}
		return nil
	}, nil
}

// aggregate gives one row the metrics of all its related records.
func (j *relJoinPlan) aggregate(tx transaction, budget *relBudget, row Record) (Record, error) {
	values := []any{}
	for _, alt := range j.alts {
		for _, pair := range alt.pairs {
			values = append(values, row[pair.on.Local])
		}
	}
	key := string(marshal(values))
	if out, ok := j.aggMemo[key]; ok {
		return out, nil
	}
	rows, e := j.matches(tx, budget, row, false)
	if e != nil {
		return nil, e
	}
	out, e := aggregateRecordsWithBudget(budget.ctx, j.foreign, j.aggInput, j.aggOut, MaxQueryWorkBytes, func(_ Query, _ []string, visit func(Record, []byte) bool) error {
		for _, r := range rows {
			if !visit(r, nil) {
				break
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	if e := budget.retain(64 + len(key) + retainedBytes(out[0])); e != nil {
		return nil, e
	}
	j.aggMemo[key] = out[0]
	return out[0], nil
}

func validateDerived(outSchema Collection, derived []RelDerived) error {
	derivedNames := map[string]bool{}
	for _, d := range derived {
		if e := nameOK(d.Name); e != nil {
			return e
		}
		if d.Op != "divide" {
			return Invalid("unknown derived op %s", d.Op)
		}
		if _, e := outSchema.field(d.Name); e == nil || derivedNames[d.Name] {
			return Invalid("%s is already a result name", d.Name)
		}
		derivedNames[d.Name] = true
		for _, name := range []string{d.Numerator, d.Denominator} {
			if _, e := outSchema.field(name); e != nil {
				return Invalid("derived %s reads %s, which is not a group key or metric", d.Name, name)
			}
		}
		if d.Round != nil && (*d.Round < 0 || *d.Round > 12) {
			return Invalid("round must be 0–12")
		}
	}
	return nil
}
