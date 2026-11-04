package engine

import (
	"strconv"
	"strings"
	"sync"
)

// An exists or not_exists filter node tests related records of another
// collection in the same database without returning them. Each On entry is one
// way a related record can match, so the clause holds when any alternative
// finds a record that also satisfies Where. Local nulls never match. The
// filter tree has no general negation, so a failed evaluation can always read
// as "no match" and the operation then returns the recorded error.
type ExistsClause struct {
	Collection string     `json:"collection"`
	On         []ExistsOn `json:"on"`
	Where      *Filter    `json:"where,omitempty"`
	plan       *existsPlan
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
	maxExistsNodes        = 8
	maxExistsAlternatives = 16
	// A build reads a related row in about 2 µs and a probe costs about 25 µs,
	// so an abandoned build wastes at most about 2.5 times what the probes spent,
	// and the attempts double.
	existsRowsPerProbe = 32
)

// Variables so tests can force each strategy.
var (
	existsBuildAfter = 128 // probes before the first attempt to build key sets
	existsSetBytes   = MaxQueryWorkBytes
	existsSetsBuilt  func() // test hook
)

// recordScanner streams records of a collection matching a filter. Adapters
// that evaluate exists in Go implement it; the visitor must not retain r.
type recordScanner interface {
	scanRecords(c Collection, q Query, fields []string, consume func(r Record) bool) error
}

type existsAlternative struct {
	on      ExistsOn
	local   Field
	foreign Field
	index   string
}

type existsPlan struct {
	tx      transaction
	foreign Collection
	alts    []existsAlternative
	where   *Filter
	state   *existsState
	// Probes run until a one-pass build of the matching related keys is likely
	// to be cheaper. sets is nil until a build succeeds; noSets ends the attempts.
	probes    int
	nextBuild int
	sets      map[string]map[string]struct{}
	noSets    bool
}

// existsState keeps the first probe failure of one operation.
type existsState struct {
	mu       sync.Mutex
	err      error
	setBytes int // key set memory of this operation; exists runs on its scan's goroutine
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
// transaction. Probes read through tx, so they share the operation's snapshot.
func prepareExists(tx transaction, c Collection, where *Filter) (*existsState, error) {
	if !hasExists(where) {
		return nil, nil
	}
	state := &existsState{}
	nodes := 0
	var walk func(*Filter) error
	walk = func(f *Filter) error {
		if f == nil {
			return nil
		}
		for i := range f.And {
			if e := walk(&f.And[i]); e != nil {
				return e
			}
		}
		for i := range f.Or {
			if e := walk(&f.Or[i]); e != nil {
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
		if nodes++; nodes > maxExistsNodes {
			return Invalid("at most %d exists filters", maxExistsNodes)
		}
		p, e := planExists(tx, c, clause, state)
		if e != nil {
			return e
		}
		clause.plan = p
		return nil
	}
	if e := walk(where); e != nil {
		return nil, e
	}
	return state, nil
}

func planExists(tx transaction, c Collection, clause *ExistsClause, state *existsState) (*existsPlan, error) {
	if clause.Collection == "" {
		return nil, Invalid("exists requires collection")
	}
	if len(clause.On) == 0 || len(clause.On) > maxExistsAlternatives {
		return nil, Invalid("exists requires 1–%d on pairs", maxExistsAlternatives)
	}
	foreign, e := tx.collection(clause.Collection)
	if e != nil {
		return nil, e
	}
	if hasExists(clause.Where) {
		return nil, Invalid("exists filters cannot be nested")
	}
	terms := 0
	if e := validateFilter(foreign, clause.Where, 0, &terms); e != nil {
		return nil, e
	}
	p := &existsPlan{tx: tx, foreign: foreign, where: clause.Where, state: state, nextBuild: existsBuildAfter}
	for _, on := range clause.On {
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
		index := leadingIndex(foreign, on.ForeignField)
		if index == "" {
			return nil, fail("query_too_expensive", "exists needs an index on %s.%s", foreign.Name, on.ForeignField)
		}
		p.alts = append(p.alts, existsAlternative{on: on, local: local, foreign: remote, index: index})
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
func (a existsAlternative) probeKey(v any) (any, bool) {
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
		v := r[alt.on.LocalField]
		if v == nil {
			continue
		}
		key, ok := alt.probeKey(v)
		if !ok {
			continue
		}
		if p.sets != nil {
			if _, hit := p.sets[alt.on.ForeignField][key.(string)]; hit {
				return true, nil
			}
			continue
		}
		p.probes++
		where := &Filter{Field: alt.on.ForeignField, Op: "eq", Value: key}
		if p.where != nil {
			where = &Filter{And: []Filter{*where, *p.where}}
		}
		rows, e := p.tx.find(p.foreign, Query{Where: where, Select: []string{alt.on.ForeignField}, hint: alt.index}, 1)
		if e != nil {
			return false, e
		}
		if len(rows) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// buildSets reads the related records satisfying Where once and keeps their key
// values per related field. It gives up, keeping probes, when the pass exceeds
// its row allowance (retrying after twice the probes) or the memory budget.
func (p *existsPlan) buildSets() error {
	scanner, ok := p.tx.(recordScanner)
	if !ok {
		p.noSets = true
		return nil
	}
	fields := []string{}
	sets := map[string]map[string]struct{}{}
	for _, alt := range p.alts {
		if alt.foreign.Type != "text" && alt.foreign.Type != "integer" || alt.local.Type != "text" && alt.local.Type != "integer" {
			p.noSets = true
			return nil
		}
		if sets[alt.on.ForeignField] == nil {
			sets[alt.on.ForeignField] = map[string]struct{}{}
			fields = append(fields, alt.on.ForeignField)
		}
	}
	// Every row read counts against the allowance, so Where is applied here.
	read := append([]string{}, fields...)
	visitFilterFields(p.where, func(name string) { read = append(read, name) })
	allowance := p.probes * existsRowsPerProbe
	rows, bytes := 0, 0
	tooMany, tooLarge := false, false
	e := scanner.scanRecords(p.foreign, Query{}, read, func(r Record) bool {
		if rows++; rows > allowance {
			tooMany = true
			return false
		}
		if !matches(p.foreign, r, p.where) {
			return true
		}
		for _, name := range fields {
			key, isText := r[name].(string)
			if !isText {
				continue
			}
			set := sets[name]
			if _, seen := set[key]; seen {
				continue
			}
			set[strings.Clone(key)] = struct{}{}
			bytes += len(key) + 64
			if p.state.setBytes+bytes > existsSetBytes {
				tooLarge = true
				return false
			}
		}
		return true
	})
	switch {
	case e != nil:
		return e
	case tooLarge:
		p.noSets = true
	case tooMany:
		p.nextBuild = p.probes * 2
	default:
		p.sets = sets
		p.state.setBytes += bytes
		if existsSetsBuilt != nil {
			existsSetsBuilt()
		}
	}
	return nil
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

// compileExists writes the node as a correlated subquery. An unprepared clause
// matches nothing.
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
		local := table(c) + "." + quote(alt.on.LocalField)
		foreign := existsAlias + "." + quote(alt.on.ForeignField)
		switch {
		case alt.on.Comparison == "text" && alt.local.Type == "text" && alt.foreign.Type == "integer":
			// Seek by the integer, then require the exact decimal text.
			number := "CAST(" + local + " AS INTEGER)"
			alternatives[i] = foreign + "=" + number + " AND CAST(" + number + " AS TEXT)=" + local
		case alt.on.Comparison == "text" && alt.local.Type == "integer" && alt.foreign.Type == "text":
			alternatives[i] = foreign + "=CAST(" + local + " AS TEXT)"
		default:
			alternatives[i] = foreign + "=" + local
		}
		alternatives[i] = "(" + alternatives[i] + ")"
	}
	where, args := compileFilter(clause.plan.foreign, clause.Where)
	return prefix + " (SELECT 1 FROM " + table(clause.plan.foreign) + " AS " + existsAlias + " WHERE (" + strings.Join(alternatives, " OR ") + ") AND (" + where + "))", args
}
