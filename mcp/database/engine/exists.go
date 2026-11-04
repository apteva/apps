package engine

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// An exists or not_exists filter node tests related records of another
// collection in the same database without returning them. A record is related
// when every On pair matches, as in SQL's ON ... AND ...; AnyOf instead lists
// alternative On lists, any of which may match. The clause holds when a related
// record also satisfies Where, which may hold further exists nodes; their local
// fields belong to this clause's collection. Nulls never match. A failed
// evaluation reads as "no match", which a parent not_exists turns into a match,
// so the operation must return the recorded error instead of any result.
type ExistsClause struct {
	Collection string        `json:"collection"`
	On         []ExistsOn    `json:"on,omitempty"`
	AnyOf      []ExistsMatch `json:"anyOf,omitempty"`
	Where      *Filter       `json:"where,omitempty"`
	plan       *existsPlan
}

// ExistsMatch is one alternative of AnyOf.
type ExistsMatch struct {
	On []ExistsOn `json:"on"`
}

// alternatives lists the On lists, one of which must match.
func (c *ExistsClause) alternatives() [][]ExistsOn {
	if c.AnyOf == nil {
		return [][]ExistsOn{c.On}
	}
	out := make([][]ExistsOn, len(c.AnyOf))
	for i, m := range c.AnyOf {
		out[i] = m.On
	}
	return out
}

// pairs lists every pair of every alternative.
func (c *ExistsClause) pairs() []ExistsOn {
	out := []ExistsOn{}
	for _, on := range c.alternatives() {
		out = append(out, on...)
	}
	return out
}

// ExistsOn pairs a local field with a related field. Comparison "text" compares
// text and integer fields by their decimal text, with no trimming or other
// coercion; without it both fields must have the same type.
type ExistsOn struct {
	LocalField   string `json:"localField"`
	ForeignField string `json:"foreignField"`
	Comparison   string `json:"comparison,omitempty"`
}

const (
	maxExistsNodes        = 8 // across the whole tree
	maxExistsDepth        = 4
	maxExistsAlternatives = 16
	maxExistsPairs        = 8
	// A build reads a related row in about 2 µs and a probe costs about 25 µs,
	// so an abandoned build wastes at most about 2.5 times what the probes spent,
	// and the attempts double.
	existsRowsPerProbe = 32
	// Work is counted in related rows read; a probe costs about this many.
	existsProbeWork = 12
)

// Variables so tests can force each strategy.
var (
	existsBuildAfter = 128 // probes before the first attempt to build key sets
	existsSetBytes   = MaxQueryWorkBytes
	existsSetsBuilt  func() // test hook
	// Nested relationships multiply work, so they get a budget of about 30 s of
	// reads on top of the operation's deadline.
	existsNestedWork = int64(16 << 20)
)

// recordScanner streams records of a collection matching a filter. Adapters
// that evaluate exists in Go implement it; the visitor must not retain r.
type recordScanner interface {
	scanRecords(c Collection, q Query, fields []string, consume func(r Record) bool) error
}

type existsPair struct {
	on      ExistsOn
	local   Field
	foreign Field
}

type existsAlternative struct {
	pairs []existsPair
	index string // an index a probe seeks through
	set   string // key set name: the related fields, in order
}

type existsPlan struct {
	tx      transaction
	foreign Collection
	alts    []existsAlternative
	where   *Filter
	state   *existsState
	// SQL names for the related table and the record the pairs read local fields
	// from; an empty parent is the query's own table.
	alias  string
	parent string
	// Probes run until a one-pass build of the matching related keys is likely
	// to be cheaper. sets is nil until a build succeeds; noSets ends the attempts.
	probes    int
	nextBuild int
	sets      map[string]map[string]struct{}
	noSets    bool
}

// existsState keeps the first probe failure and the limits of one operation,
// shared by every node of its filter tree.
type existsState struct {
	mu  sync.Mutex
	err error
	// Key set memory and work of this operation; exists runs on its scan's goroutine.
	setBytes  int
	probes    int64
	setsBuilt int64
	rowsRead  int64
	examined  int64 // documents key set scans actually read
	nodes     int
	nested    bool
}

func (s *existsState) work() int64 { return s.rowsRead + s.probes*existsProbeWork }

// overBudget fails a nested operation past its work budget.
func (s *existsState) overBudget() error {
	if !s.nested || s.work() <= existsNestedWork {
		return nil
	}
	e := fail("query_too_expensive", "nested exists exceeded its work budget; narrow the filter or the related collections")
	s.fail(e)
	return e
}

func (s *existsState) fail(e error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = e
	}
	s.mu.Unlock()
}

func (s *existsState) failure() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func hasExists(f *Filter) bool {
	if f == nil {
		return false
	}
	if f.Exists != nil || f.NotExists != nil {
		return true
	}
	for i := range f.And {
		if hasExists(&f.And[i]) {
			return true
		}
	}
	for i := range f.Or {
		if hasExists(&f.Or[i]) {
			return true
		}
	}
	return false
}

// stripExists returns a filter every matching record also satisfies once the
// exists nodes are removed: the part a native scan can bound. The caller must
// evaluate the complete filter on each candidate. An or with an exists branch
// cannot bound anything.
func stripExists(f *Filter) *Filter {
	if !hasExists(f) {
		return f
	}
	if f.And == nil {
		return nil
	}
	kept := []Filter{}
	for i := range f.And {
		if child := stripExists(&f.And[i]); child != nil {
			kept = append(kept, *child)
		}
	}
	return conjunction(kept)
}

// prepareExists binds every exists node of an already validated filter to this
// transaction, each nested node to its immediate parent. Probes read through tx,
// so the whole tree shares the operation's snapshot.
func prepareExists(tx transaction, c Collection, where *Filter) (*existsState, error) {
	if !hasExists(where) {
		return nil, nil
	}
	state := &existsState{}
	if e := state.bind(tx, c, where, "", 1); e != nil {
		return nil, e
	}
	return state, nil
}

func (s *existsState) bind(tx transaction, c Collection, f *Filter, parent string, depth int) error {
	if f == nil {
		return nil
	}
	for i := range f.And {
		if e := s.bind(tx, c, &f.And[i], parent, depth); e != nil {
			return e
		}
	}
	for i := range f.Or {
		if e := s.bind(tx, c, &f.Or[i], parent, depth); e != nil {
			return e
		}
	}
	clause := f.Exists
	if clause == nil {
		clause = f.NotExists
	}
	if clause == nil {
		return nil
	}
	if s.nodes++; s.nodes > maxExistsNodes {
		return Invalid("at most %d exists filters", maxExistsNodes)
	}
	if depth > maxExistsDepth {
		return Invalid("exists filters nest at most %d levels", maxExistsDepth)
	}
	s.nested = s.nested || depth > 1
	p, e := planExists(tx, c, clause, s)
	if e != nil {
		return e
	}
	p.alias, p.parent = existsAlias+strconv.Itoa(s.nodes), parent
	clause.plan = p
	return s.bind(tx, p.foreign, clause.Where, p.alias, depth+1)
}

func planExists(tx transaction, c Collection, clause *ExistsClause, state *existsState) (*existsPlan, error) {
	if clause.Collection == "" {
		return nil, Invalid("exists requires collection")
	}
	if (clause.On == nil) == (clause.AnyOf == nil) {
		return nil, Invalid("exists requires exactly one of on, anyOf")
	}
	if clause.AnyOf != nil && (len(clause.AnyOf) == 0 || len(clause.AnyOf) > maxExistsAlternatives) {
		return nil, Invalid("anyOf requires 1–%d alternatives", maxExistsAlternatives)
	}
	foreign, e := tx.collection(clause.Collection)
	if e != nil {
		return nil, e
	}
	terms := 0
	if e := validateFilter(foreign, clause.Where, 0, &terms); e != nil {
		return nil, e
	}
	p := &existsPlan{tx: tx, foreign: foreign, where: clause.Where, state: state, nextBuild: existsBuildAfter}
	for _, list := range clause.alternatives() {
		if len(list) == 0 || len(list) > maxExistsPairs {
			return nil, Invalid("exists on requires 1–%d pairs", maxExistsPairs)
		}
		alt := existsAlternative{}
		foreignFields := make([]string, len(list))
		for i, on := range list {
			local, e := storedField(c, on.LocalField)
			if e != nil {
				return nil, e
			}
			remote, e := foreign.field(on.ForeignField)
			if e != nil {
				return nil, e
			}
			if remote.Type == "json" || local.Type == "json" {
				return nil, Invalid("exists cannot compare JSON fields")
			}
			switch on.Comparison {
			case "":
				if local.Type != remote.Type {
					return nil, Invalid("exists compares %s (%s) with %s (%s); use comparison text or fields of one type", on.LocalField, local.Type, on.ForeignField, remote.Type)
				}
			case "text":
				if !textComparable(local) || !textComparable(remote) {
					return nil, Invalid("comparison text requires text or integer fields")
				}
			default:
				return nil, Invalid("unknown exists comparison %s", on.Comparison)
			}
			if alt.index == "" {
				alt.index = leadingIndex(foreign, on.ForeignField)
			}
			foreignFields[i] = on.ForeignField
			alt.pairs = append(alt.pairs, existsPair{on: on, local: local, foreign: remote})
		}
		if alt.index == "" {
			return nil, fail("query_too_expensive", "exists needs an index on %s starting with one of: %s", foreign.Name, strings.Join(foreignFields, ", "))
		}
		alt.set = strings.Join(foreignFields, "\x00")
		p.alts = append(p.alts, alt)
	}
	return p, nil
}

func textComparable(f Field) bool { return f.Type == "text" || f.Type == "integer" }

func storedField(c Collection, name string) (Field, error) {
	if strings.Contains(name, ".") {
		return Field{}, Invalid("exists localField %s must be a stored field", name)
	}
	return c.field(name)
}

// leadingIndex names an index a probe can seek by equality on field.
func leadingIndex(c Collection, field string) string {
	for _, idx := range indexes(c) {
		if len(idx.Fields) > 0 && idx.Fields[0].Field == field {
			return idx.Name
		}
	}
	return ""
}

// probeKey converts a local value to the related field's stored form. A text
// value that is not exactly an integer's decimal text matches no integer.
func (a existsPair) probeKey(v any) (any, bool) {
	if a.on.Comparison == "text" && a.local.Type == "text" && a.foreign.Type == "integer" {
		n, e := strconv.ParseInt(v.(string), 10, 64)
		if e != nil || strconv.FormatInt(n, 10) != v.(string) {
			return nil, false
		}
	}
	return v, true
}

func (p *existsPlan) related(r Record) (bool, error) {
	if p.sets == nil && !p.noSets && p.probes >= p.nextBuild {
		if e := p.buildSets(); e != nil {
			return false, e
		}
	}
	for _, alt := range p.alts {
		keys, ok := alt.keys(r)
		if !ok {
			continue
		}
		if p.sets != nil {
			if _, hit := p.sets[alt.set][tupleKey(keys)]; hit {
				return true, nil
			}
			continue
		}
		p.probes++
		p.state.probes++
		if e := p.state.overBudget(); e != nil {
			return false, e
		}
		terms := make([]Filter, 0, len(keys)+1)
		for i, key := range keys {
			terms = append(terms, Filter{Field: alt.pairs[i].on.ForeignField, Op: "eq", Value: key})
		}
		if p.where != nil {
			terms = append(terms, *p.where)
		}
		rows, e := p.tx.find(p.foreign, Query{Where: conjunction(terms), Select: []string{alt.pairs[0].on.ForeignField}, hint: alt.index}, 1)
		if e != nil {
			return false, e
		}
		if len(rows) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// keys reads the probe values of one alternative. A null, or a value no related
// field can equal, rules the alternative out.
func (a existsAlternative) keys(r Record) ([]any, bool) {
	out := make([]any, len(a.pairs))
	for i, pair := range a.pairs {
		v := r[pair.on.LocalField]
		if v == nil {
			return nil, false
		}
		key, ok := pair.probeKey(v)
		if !ok {
			return nil, false
		}
		out[i] = key
	}
	return out, true
}

// tupleKey is unambiguous: ("ab","c") and ("a","bc") differ.
func tupleKey(values []any) string {
	if len(values) == 1 {
		return keyText(values[0])
	}
	var b strings.Builder
	for _, v := range values {
		text := keyText(v)
		b.WriteString(strconv.Itoa(len(text)))
		b.WriteByte(':')
		b.WriteString(text)
	}
	return b.String()
}

func keyText(v any) string {
	if text, ok := v.(string); ok {
		return text
	}
	return fmt.Sprint(v)
}

// buildSets reads the related records satisfying Where once and keeps their key
// tuples per list of related fields. It gives up, keeping probes, when the pass exceeds
// its row allowance (retrying after twice the probes) or the memory budget.
func (p *existsPlan) buildSets() error {
	scanner, ok := p.tx.(recordScanner)
	if !ok {
		p.noSets = true
		return nil
	}
	fields := []string{}
	sets := map[string]map[string]struct{}{}
	builds := []existsAlternative{}
	for _, alt := range p.alts {
		for _, pair := range alt.pairs {
			if !textComparable(pair.foreign) || !textComparable(pair.local) {
				p.noSets = true
				return nil
			}
		}
		if sets[alt.set] == nil {
			sets[alt.set] = map[string]struct{}{}
			builds = append(builds, alt)
			for _, pair := range alt.pairs {
				fields = append(fields, pair.on.ForeignField)
			}
		}
	}
	// Every row read counts against the allowance, so Where is applied here.
	read := append([]string{}, fields...)
	visitFilterFields(p.where, func(name string) { read = append(read, name) })
	// A nested Where spends work on each row, and that counts too.
	allowance := int64(p.probes) * existsRowsPerProbe
	spent := p.state.work()
	bytes := 0
	tooMany, tooLarge := false, false
	e := scanner.scanRecords(p.foreign, p.setScan(), read, func(r Record) bool {
		p.state.rowsRead++
		if p.state.work()-spent > allowance {
			tooMany = true
			return false
		}
		if p.state.overBudget() != nil {
			return false
		}
		matched := matches(p.foreign, r, p.where)
		if p.state.failure() != nil {
			return false
		}
		if !matched {
			return true
		}
		for _, alt := range builds {
			values := make([]any, len(alt.pairs))
			complete := true
			for k, pair := range alt.pairs {
				values[k] = r[pair.on.ForeignField]
				complete = complete && values[k] != nil
			}
			if !complete {
				continue
			}
			key := tupleKey(values)
			set := sets[alt.set]
			if _, seen := set[key]; seen {
				continue
			}
			set[strings.Clone(key)] = struct{}{}
			bytes += len(key) + 64
			p.state.setBytes += len(key) + 64
			if p.state.setBytes > existsSetBytes {
				tooLarge = true
				return false
			}
		}
		return true
	})
	if e == nil {
		// A set read past a failure is missing keys; it must never be kept.
		e = p.state.failure()
	}
	if e != nil || tooLarge || tooMany {
		p.state.setBytes -= bytes
	}
	switch {
	case e != nil:
		return e
	case tooLarge:
		p.noSets = true
	case tooMany:
		p.nextBuild = p.probes * 2
	default:
		p.sets = sets
		p.state.setsBuilt++
		if existsSetsBuilt != nil {
			existsSetsBuilt()
		}
	}
	return nil
}

// setScan bounds a key set scan by the index prefix the ordinary part of Where
// can seek. The complete Where still decides every key. Terms outside the prefix
// stay out of the bound: a row they reject would never reach the callback, so it
// would escape the build's row allowance. Without a usable index the scan is full
// and the allowance stops it, as it always has.
func (p *existsPlan) setScan() Query {
	q := Query{examined: &p.state.examined}
	terms := conjuncts(stripExists(p.where))
	chosen := plan(p.foreign, Query{Where: conjunction(terms)})
	if !chosen.IndexedLookup {
		return q
	}
	for _, idx := range indexes(p.foreign) {
		if idx.Name != chosen.Index {
			continue
		}
		bound := indexPrefixTerms(idx, terms)
		if plan(p.foreign, Query{Where: conjunction(bound)}).IndexedLookup {
			q.Where = conjunction(bound)
		}
		break
	}
	return q
}

// indexPrefixTerms keeps the terms an index seeks: equalities along its leading
// fields, then the range on the first field without one.
func indexPrefixTerms(idx Index, terms []Filter) []Filter {
	kept := []Filter{}
	for _, o := range idx.Fields {
		exact, ranged := []Filter{}, []Filter{}
		for _, t := range terms {
			if t.Field != o.Field {
				continue
			}
			switch t.Op {
			case "eq", "in", "is_null":
				exact = append(exact, t)
			case "gt", "gte", "lt", "lte":
				ranged = append(ranged, t)
			}
		}
		if len(exact) == 0 {
			return append(kept, ranged...)
		}
		kept = append(kept, exact...)
	}
	return kept
}

// matchesExists evaluates an exists or not_exists node. An unprepared clause or
// a failed probe never matches; the failure aborts the operation.
func matchesExists(r Record, f *Filter) bool {
	clause, want := f.Exists, true
	if clause == nil {
		clause, want = f.NotExists, false
	}
	if clause.plan == nil {
		return false
	}
	found, e := clause.plan.related(r)
	if e != nil {
		clause.plan.state.fail(e)
		return false
	}
	return found == want
}

const existsAlias = "__exists"

// compileExists writes the node as a correlated subquery over its own alias,
// reading local fields from its parent's. An unprepared clause matches nothing.
func compileExists(c Collection, f *Filter) (string, []any) {
	clause, prefix := f.Exists, "EXISTS"
	if clause == nil {
		clause, prefix = f.NotExists, "NOT EXISTS"
	}
	if clause.plan == nil {
		return "0", nil
	}
	alternatives := make([]string, len(clause.plan.alts))
	for i, alt := range clause.plan.alts {
		pairs := make([]string, len(alt.pairs))
		for k, pair := range alt.pairs {
			pairs[k] = compileExistsPair(c, clause.plan, pair)
		}
		alternatives[i] = "(" + strings.Join(pairs, " AND ") + ")"
	}
	where, args := compileFilter(clause.plan.foreign, clause.Where)
	return prefix + " (SELECT 1 FROM " + table(clause.plan.foreign) + " AS " + clause.plan.alias + " WHERE (" + strings.Join(alternatives, " OR ") + ") AND (" + where + "))", args
}

func compileExistsPair(c Collection, p *existsPlan, pair existsPair) string {
	parent := p.parent
	if parent == "" {
		parent = table(c)
	}
	local := parent + "." + quote(pair.on.LocalField)
	foreign := p.alias + "." + quote(pair.on.ForeignField)
	switch {
	case pair.on.Comparison == "text" && pair.local.Type == "text" && pair.foreign.Type == "integer":
		// Seek by the integer, then require the exact decimal text.
		number := "CAST(" + local + " AS INTEGER)"
		return foreign + "=" + number + " AND CAST(" + number + " AS TEXT)=" + local
	case pair.on.Comparison == "text" && pair.local.Type == "integer" && pair.foreign.Type == "text":
		return foreign + "=CAST(" + local + " AS TEXT)"
	}
	return foreign + "=" + local
}
