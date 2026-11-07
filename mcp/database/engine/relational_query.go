package engine

import (
	"fmt"
	"math"
)

// QueryNode is a named, request-local relation, not a stored collection. Fields
// are operation-specific and compiled before the first record is read.
type QueryNode struct {
	Name       string        `json:"name"`
	Op         string        `json:"op"`
	From       string        `json:"from,omitempty"`
	Collection string        `json:"collection,omitempty"`
	Where      *Filter       `json:"where,omitempty"`
	Select     []string      `json:"select,omitempty"`
	Computed   []RelComputed `json:"computed,omitempty"`
	Predicate  *RelExpr      `json:"predicate,omitempty"`
	GroupBy    []string      `json:"groupBy,omitempty"`
	Metrics    []Metric      `json:"metrics,omitempty"`
	As         string        `json:"as,omitempty"`
	Joins      []QueryJoin   `json:"joins,omitempty"`
	MaxRows    int           `json:"maxRows,omitempty"`
}
type QueryJoin struct {
	Relation    string  `json:"relation"`
	As          string  `json:"as"`
	Kind        string  `json:"kind,omitempty"`
	On          []RelOn `json:"on"`
	Cardinality string  `json:"cardinality,omitempty"`
	Where       *Filter `json:"where,omitempty"`
}
type QueryResult struct {
	From    string  `json:"from"`
	OrderBy []Order `json:"orderBy,omitempty"`
	Limit   int     `json:"limit,omitempty"`
}
type QueryLimits struct {
	IntermediateRows int   `json:"intermediateRows,omitempty"`
	MemoryBytes      int   `json:"memoryBytes,omitempty"`
	Work             int64 `json:"work,omitempty"`
}

const maxQueryNodes = 32
const maxQueryRows = 100000

type queryNodePlan struct {
	joinedView Collection
	node       QueryNode
	view       Collection
	source     Collection
	input      *queryNodePlan
	joins      []queryJoinPlan
	chain      *relChain
	agg        Request
	state      *existsState
	refs       int
}
type queryJoinPlan struct {
	join  QueryJoin
	right *queryNodePlan
	pairs []relPair
}
type queryPlan struct {
	tx     transaction
	nodes  map[string]*queryNodePlan
	result *queryNodePlan
	output QueryResult
	budget *relBudget
	limits QueryLimits
}

func queryError(name string, e error) error {
	if e == nil {
		return nil
	}
	if known, ok := e.(*Error); ok {
		return &Error{known.Code, fmt.Sprintf("node %s: %s", name, known.Message)}
	}
	return fmt.Errorf("node %s: %w", name, e)
}
func queryFields(c Collection, names []string) (Collection, error) {
	if len(names) == 0 {
		return c, nil
	}
	out := Collection{}
	seen := map[string]bool{}
	for _, name := range names {
		f, e := queryField(c, name)
		if e != nil {
			return out, e
		}
		if seen[name] {
			return out, Invalid("duplicate selected field %s", name)
		}
		seen[name] = true
		out.Fields = append(out.Fields, f)
	}
	return out, nil
}
func compileQuery(tx transaction, r Request) (*queryPlan, error) {
	if r.Version != 1 {
		return nil, Invalid("query requires version: 1")
	}
	if len(r.Nodes) == 0 || len(r.Nodes) > maxQueryNodes {
		return nil, Invalid("query requires 1–%d nodes", maxQueryNodes)
	}
	if r.Result == nil {
		return nil, Invalid("query requires result")
	}
	limits := QueryLimits{maxQueryRows, 128 << 20, 16 << 20}
	if r.Limits != nil {
		if r.Limits.IntermediateRows < 0 || r.Limits.IntermediateRows > limits.IntermediateRows || r.Limits.MemoryBytes < 0 || r.Limits.MemoryBytes > limits.MemoryBytes || r.Limits.Work < 0 || r.Limits.Work > limits.Work {
			return nil, Invalid("query limits may only lower server limits")
		}
		if r.Limits.IntermediateRows > 0 {
			limits.IntermediateRows = r.Limits.IntermediateRows
		}
		if r.Limits.MemoryBytes > 0 {
			limits.MemoryBytes = r.Limits.MemoryBytes
		}
		if r.Limits.Work > 0 {
			limits.Work = r.Limits.Work
		}
	}
	p := &queryPlan{tx: tx, nodes: map[string]*queryNodePlan{}, output: *r.Result, limits: limits, budget: &relBudget{ctx: relContext(tx)}}
	exprs := 0
	for _, n := range r.Nodes {
		if e := nameOK(n.Name); e != nil {
			return nil, e
		}
		if p.nodes[n.Name] != nil {
			return nil, Invalid("duplicate relation %s", n.Name)
		}
		if n.MaxRows < 0 || n.MaxRows > limits.IntermediateRows {
			return nil, Invalid("node %s maxRows exceeds query limit", n.Name)
		}
		if n.MaxRows == 0 {
			n.MaxRows = limits.IntermediateRows
		}
		for i := range n.Computed {
			exprs += queryExprSize(&n.Computed[i].Expr)
		}
		exprs += queryExprSize(n.Predicate)
		if exprs > 1024 {
			return nil, Invalid("query expressions exceed 1024 nodes")
		}
		p.nodes[n.Name] = &queryNodePlan{node: n}
	}
	states := map[string]int{}
	var compile func(string) (*queryNodePlan, error)
	compile = func(name string) (*queryNodePlan, error) {
		n := p.nodes[name]
		if n == nil {
			return nil, Invalid("unknown relation %s", name)
		}
		if states[name] == 1 {
			return nil, Invalid("cyclic relation dependency at %s", name)
		}
		if states[name] == 2 {
			return n, nil
		}
		states[name] = 1
		bad := func(e error) (*queryNodePlan, error) { return nil, queryError(name, e) }
		if e := queryNodeFields(n.node); e != nil {
			return bad(e)
		}
		if n.node.Op != "scan" {
			input, e := compile(n.node.From)
			if e != nil {
				return nil, e
			}
			n.input = input
			input.refs++
		}
		switch n.node.Op {
		case "scan":
			if e := nameOK(n.node.Collection); e != nil {
				return bad(e)
			}
			c, e := tx.collection(n.node.Collection)
			if e != nil {
				return bad(e)
			}
			q := Query{Where: n.node.Where, Select: n.node.Select}
			if e := validateQuery(c, &q); e != nil {
				return bad(e)
			}
			n.source = c
			available := c
			if len(n.node.Select) > 0 {
				available.Fields = allFields(c)
			}
			n.view, e = queryFields(available, n.node.Select)
			if e != nil {
				return bad(e)
			}
			n.state, e = prepareExists(tx, c, n.node.Where)
			if e != nil {
				return bad(e)
			}
		case "compute":
			if e := queryComputeFields(n.input.view, n.node); e != nil {
				return bad(e)
			}
			chain, e := newRelChain(tx, n.input.view, nil, p.budget, nil, n.node.Computed, n.node.Where, nil)
			if e != nil {
				return bad(e)
			}
			n.chain = chain
			n.view = chain.view
			if n.node.Predicate != nil {
				if e := chain.checkExpr(n.node.Predicate, 0); e != nil {
					return bad(e)
				}
				typ, e := numericExprType(chain.view, n.node.Predicate, 0)
				if e != nil {
					return bad(e)
				}
				if typ != "boolean" && typ != "null" {
					return bad(Invalid("predicate must be boolean"))
				}
			}
			n.view, e = queryFields(n.view, n.node.Select)
			if e != nil {
				return bad(e)
			}
		case "aggregate":
			if e := queryAggregateFields(n.input.view, n.node); e != nil {
				return bad(e)
			}
			n.agg = Request{GroupBy: n.node.GroupBy, Metrics: n.node.Metrics, Query: Query{Where: n.node.Where}}
			out, e := validateAggregate(n.input.view, &n.agg)
			if e != nil {
				return bad(e)
			}
			n.view = out
			n.agg.Limit = math.MaxInt32
		case "join":
			alias := n.node.As
			if alias == "" {
				alias = "root"
			}
			if e := nameOK(alias); e != nil {
				return bad(e)
			}
			n.node.As = alias
			n.view = rootView(n.input.view, alias)
			taken := map[string]bool{alias: true}
			if len(n.node.Joins) == 0 || len(n.node.Joins) > maxRelJoins {
				return bad(Invalid("join requires 1–%d joins", maxRelJoins))
			}
			for _, j := range n.node.Joins {
				if e := nameOK(j.As); e != nil {
					return bad(e)
				}
				if taken[j.As] {
					return bad(Invalid("duplicate alias %s", j.As))
				}
				taken[j.As] = true
				if j.Kind != "" && j.Kind != "inner" && j.Kind != "left" && j.Kind != "semi" && j.Kind != "anti" {
					return bad(Invalid("unsupported relation join kind %s", j.Kind))
				}
				if j.Cardinality != "" && j.Cardinality != "many_to_one" {
					return bad(Invalid("cardinality must be many_to_one or omitted"))
				}
				right, e := compile(j.Relation)
				if e != nil {
					return nil, e
				}
				right.refs++
				if len(j.On) == 0 || len(j.On) > 8 {
					return bad(Invalid("join requires 1–8 on pairs"))
				}
				if e := queryFilterFields(right.view, j.Where); e != nil {
					return bad(e)
				}
				terms := 0
				if hasExists(j.Where) {
					return bad(Invalid("relation join where cannot use exists"))
				}
				if e := validateFilter(right.view, j.Where, 0, &terms); e != nil {
					return bad(e)
				}
				jp := queryJoinPlan{join: j, right: right}
				for _, on := range j.On {
					pair, e := queryJoinPair(n.view, right.view, on)
					if e != nil {
						return bad(e)
					}
					jp.pairs = append(jp.pairs, pair)
				}
				n.joins = append(n.joins, jp)
				if j.Kind != "semi" && j.Kind != "anti" {
					for _, f := range right.view.Fields {
						f.Name = j.As + "." + f.Name
						f.Nullable = true
						n.view.Fields = append(n.view.Fields, f)
					}
				}
			}
			if e := queryFilterFields(n.view, n.node.Where); e != nil {
				return bad(e)
			}
			count := 0
			if hasExists(n.node.Where) {
				return bad(Invalid("joined relation filter cannot use exists"))
			}
			if e := validateFilter(n.view, n.node.Where, 0, &count); e != nil {
				return bad(e)
			}
			n.joinedView = n.view
			var e error
			n.view, e = queryFields(n.view, n.node.Select)
			if e != nil {
				return bad(e)
			}
		}
		// Relation schemas have no implicit physical-key or metadata fields.
		n.view.PrimaryKey = nil
		if len(n.view.Fields) > 512 {
			return bad(Invalid("relation exceeds 512 fields"))
		}
		states[name] = 2
		return n, nil
	}
	// Validate even unused nodes, so malformed plans never run partially.
	for _, n := range r.Nodes {
		if _, e := compile(n.Name); e != nil {
			return nil, e
		}
	}
	result, e := compile(r.Result.From)
	if e != nil {
		return nil, e
	}
	p.result = result
	if p.output.Limit == 0 {
		p.output.Limit = MaxRecords
	}
	if p.output.Limit < 1 || p.output.Limit > MaxRecords {
		return nil, Invalid("result limit must be 1–1000 (overflow fails, it never truncates)")
	}
	for _, o := range p.output.OrderBy {
		if _, e := queryField(result.view, o.Field); e != nil {
			return nil, e
		}
	}
	if e := checkOrder(result.view, p.output.OrderBy); e != nil {
		return nil, e
	}
	return p, nil
}
func queryExprSize(x *RelExpr) int { return queryExprSizeAt(x, 0) }
func queryExprSizeAt(x *RelExpr, depth int) int {
	if depth > 8 {
		return 1025
	}
	if x == nil {
		return 0
	}
	n := 1
	for _, list := range [][]RelExpr{x.Args, x.Coalesce, x.Concat} {
		for i := range list {
			n += queryExprSizeAt(&list[i], depth+1)
		}
	}
	for _, child := range []*RelExpr{x.NullIfEmpty, x.Trim, x.Strip, x.JSON, x.Then, x.Else} {
		n += queryExprSizeAt(child, depth+1)
	}
	return n
}
func queryNodeFields(n QueryNode) error {
	// Reject valid-but-irrelevant fields as well as unknown JSON properties.
	allowed := map[string]bool{"scan": true, "compute": true, "join": true, "aggregate": true}
	if !allowed[n.Op] {
		return Invalid("unknown node op %s", n.Op)
	}
	if n.Op == "scan" {
		if n.From != "" {
			return Invalid("scan cannot have from")
		}
	} else if n.From == "" || n.Collection != "" {
		return Invalid("%s requires from and cannot name collection", n.Op)
	}
	if n.Op != "compute" && (len(n.Computed) > 0 || n.Predicate != nil) {
		return Invalid("computed/predicate require compute")
	}
	if n.Op != "aggregate" && (len(n.Metrics) > 0 || len(n.GroupBy) > 0) {
		return Invalid("metrics/groupBy require aggregate")
	}
	if n.Op != "join" && (len(n.Joins) > 0 || n.As != "") {
		return Invalid("joins/as require join")
	}
	if n.Op == "aggregate" && len(n.Select) > 0 {
		return Invalid("aggregate cannot have select")
	}
	if n.Op != "scan" && hasExists(n.Where) {
		return Invalid("exists belongs in scan where")
	}
	return nil
}
func queryJoinPair(local, foreign Collection, on RelOn) (relPair, error) {
	a, e := queryField(local, on.Local)
	if e != nil {
		return relPair{}, e
	}
	b, e := queryField(foreign, on.Foreign)
	if e != nil {
		return relPair{}, e
	}
	if a.Type == "json" || b.Type == "json" {
		return relPair{}, Invalid("cannot join JSON fields")
	}
	switch on.Comparison {
	case "":
		if a.Type != b.Type {
			return relPair{}, Invalid("join fields must have matching types")
		}
	case "text":
		if !textComparable(a) || !textComparable(b) {
			return relPair{}, Invalid("text comparison requires text/integer")
		}
	case "numeric":
		if !textComparable(a) || b.Type != "integer" {
			return relPair{}, Invalid("numeric comparison requires text/integer local and integer foreign")
		}
	default:
		return relPair{}, Invalid("unknown join comparison %s", on.Comparison)
	}
	return relPair{on: on, local: a, foreign: b}, nil
}

type queryRuntime struct {
	plan  *queryPlan
	cache map[*queryNodePlan][]Record
}

func (q *queryRuntime) spend(n int64) error {
	b := q.plan.budget
	b.work += n
	if b.work > q.plan.limits.Work {
		return fail("query_too_expensive", "query work budget exceeded")
	}
	return b.ctx.Err()
}
func (q *queryRuntime) retain(n int) error {
	b := q.plan.budget
	if n < 0 {
		b.retained += n
		return nil
	}
	if n > q.plan.limits.MemoryBytes-b.retained {
		return fail("resource_limit", "query retained-memory budget exceeded")
	}
	b.retained += n
	return nil
}
func queryCopy(row Record) Record {
	out := make(Record, len(row))
	for k, v := range row {
		out[k] = v
	}
	return out
}
func queryProject(c Collection, row Record) Record {
	out := make(Record, len(c.Fields))
	for _, f := range c.Fields {
		out[f.Name] = row[f.Name]
	}
	return out
}
func (q *queryRuntime) materialize(n *queryNodePlan) ([]Record, error) {
	if rows, ok := q.cache[n]; ok {
		return rows, nil
	}
	rows := []Record{}
	e := q.produce(n, func(row Record) (bool, error) {
		size := 64 + retainedBytes(row)
		if e := q.retain(size); e != nil {
			return false, e
		}
		rows = append(rows, queryCopy(row))
		return true, nil
	})
	if e != nil {
		return nil, e
	}
	q.cache[n] = rows
	return rows, nil
}
func (q *queryRuntime) stream(n *queryNodePlan, yield func(Record) (bool, error)) error {
	if n.refs > 1 {
		rows, e := q.materialize(n)
		if e != nil {
			return e
		}
		for _, r := range rows {
			if e := q.spend(1); e != nil {
				return queryError(n.node.Name, e)
			}
			more, e := yield(r)
			if e != nil || !more {
				return e
			}
		}
		return nil
	}
	return q.produce(n, yield)
}
func (q *queryRuntime) produce(n *queryNodePlan, yield func(Record) (bool, error)) error {
	count := 0
	emit := func(row Record) (bool, error) {
		if e := q.spend(1); e != nil {
			return false, e
		}
		count++
		if count > n.node.MaxRows {
			return false, fail("resource_limit", "intermediate rows exceed %d", n.node.MaxRows)
		}
		size := 64 + retainedBytes(row)
		if e := q.retain(size); e != nil {
			return false, e
		}
		defer q.retain(-size)
		return yield(row)
	}
	e := q.run(n, emit)
	return queryError(n.node.Name, e)
}
func (q *queryRuntime) run(n *queryNodePlan, yield func(Record) (bool, error)) error {
	switch n.node.Op {
	case "scan":
		scanner, ok := q.plan.tx.(recordScanner)
		if !ok {
			return fail("internal", "adapter cannot stream records")
		}
		fields := make([]string, len(n.view.Fields))
		for i, f := range n.view.Fields {
			fields[i] = f.Name
		}
		var failed error
		e := scanner.scanRecords(n.source, Query{Where: n.node.Where, Select: fields}, fields, func(row Record) bool {
			if failed = n.state.failure(); failed != nil {
				return false
			}
			more, e := yield(queryProject(n.view, row))
			failed = e
			return more && e == nil
		})
		if failed != nil {
			return failed
		}
		if e != nil {
			return e
		}
		return n.state.failure()
	case "compute":
		return q.stream(n.input, func(row Record) (bool, error) {
			out := queryCopy(row)
			allocated := 0
			defer func() { q.retain(-allocated) }()
			reserve := func(size int) error {
				if e := q.retain(size); e != nil {
					return e
				}
				allocated += size
				return nil
			}
			if e := n.chain.computeValues(out, reserve); e != nil {
				return false, e
			}
			if !matches(n.chain.view, out, n.node.Where) {
				return true, nil
			}
			if n.node.Predicate != nil {
				v, e := n.node.Predicate.evalChecked(n.chain.view, out)
				if e != nil {
					return false, e
				}
				if v != true {
					return true, nil
				}
			}
			return yield(queryProject(n.view, out))
		})
	case "aggregate":
		rows, e := aggregateRecordsSharedBudget(q.plan.budget.ctx, n.input.view, n.agg, n.view, MaxQueryWorkBytes, func(query Query, _ []string, visit func(Record, []byte) bool) error {
			return q.stream(n.input, func(row Record) (bool, error) {
				if !matches(n.input.view, row, query.Where) {
					return true, nil
				}
				return visit(row, nil), nil
			})
		}, q.retain)
		if e != nil {
			return e
		}
		size := queryRowsBytes(rows)
		if e := q.retain(size); e != nil {
			return e
		}
		defer q.retain(-size)
		for _, row := range rows {
			more, e := yield(row)
			if e != nil || !more {
				return e
			}
		}
		return nil
	case "join":
		return q.runJoin(n, yield)
	}
	return Invalid("unknown query node")
}
func queryJoinKey(pairs []relPair, row Record, local bool) (string, bool) {
	keys := make([]any, len(pairs))
	for i, p := range pairs {
		name := p.on.Foreign
		if local {
			name = p.on.Local
		}
		v := row[name]
		if v == nil {
			return "", false
		}
		if local {
			var ok bool
			v, ok = p.key(v)
			if !ok {
				return "", false
			}
		}
		// Number fields normalize -0/+0; tupleKey uses the same decimal form.
		keys[i] = v
	}
	return tupleKey(keys), true
}
func (q *queryRuntime) runJoin(n *queryNodePlan, yield func(Record) (bool, error)) error {
	tables := make([]map[string][]Record, len(n.joins))
	bytes := 0
	defer func() { q.retain(-bytes) }()
	for i, j := range n.joins {
		rows, e := q.materialize(j.right)
		if e != nil {
			return e
		}
		table := map[string][]Record{}
		tables[i] = table
		for _, row := range rows {
			if e := q.spend(1); e != nil {
				return e
			}
			if !matches(j.right.view, row, j.join.Where) {
				continue
			}
			key, ok := queryJoinKey(j.pairs, row, false)
			if !ok {
				continue
			}
			if j.join.Cardinality == "many_to_one" && len(table[key]) > 0 {
				return fail("cardinality_violation", "join %s has duplicate key", j.join.As)
			}
			size := 32
			if len(table[key]) == 0 {
				size += 96 + len(key)
			}
			if e := q.retain(size); e != nil {
				return e
			}
			bytes += size
			table[key] = append(table[key], row)
			if len(table[key]) > maxRelFanout {
				return fail("query_too_expensive", "join %s fanout exceeded", j.join.As)
			}
		}
	}
	var step func(int, Record) (bool, error)
	step = func(i int, row Record) (bool, error) {
		if e := q.spend(1); e != nil {
			return false, e
		}
		if i == len(n.joins) {
			if !matches(n.joinedView, row, n.node.Where) {
				return true, nil
			}
			return yield(queryProject(n.view, row))
		}
		j := n.joins[i]
		key, ok := queryJoinKey(j.pairs, row, true)
		var rows []Record
		if ok {
			rows = tables[i][key]
		}
		switch j.join.Kind {
		case "semi":
			if len(rows) == 0 {
				return true, nil
			}
			return step(i+1, row)
		case "anti":
			if len(rows) > 0 {
				return true, nil
			}
			return step(i+1, row)
		case "left":
			if len(rows) == 0 {
				return step(i+1, row)
			}
		}
		for _, right := range rows {
			next := queryCopy(row)
			for _, f := range j.right.view.Fields {
				next[j.join.As+"."+f.Name] = right[f.Name]
			}
			more, e := step(i+1, next)
			if e != nil || !more {
				return more, e
			}
		}
		return true, nil
	}
	return q.stream(n.input, func(row Record) (bool, error) { return step(0, rootRow(n.input.view, n.node.As, row)) })
}
func executeQuery(tx transaction, r Request) (any, error) {
	// Validation mutates filters; isolate the plan from caller reuse and batch peers.
	var copy Request
	if e := decode(marshal(r), &copy); e != nil {
		return nil, e
	}
	p, e := compileQuery(tx, copy)
	if e != nil {
		return nil, e
	}
	runtime := &queryRuntime{plan: p, cache: map[*queryNodePlan][]Record{}}
	rows := []Record{}
	size := 0
	e = runtime.stream(p.result, func(row Record) (bool, error) {
		if len(rows) >= p.output.Limit {
			return false, fail("resource_limit", "result exceeds %d rows", p.output.Limit)
		}
		row = queryProject(p.result.view, row)
		encoded := len(marshal(row)) + 1
		if size += encoded; size+32 > MaxBytes {
			return false, fail("resource_limit", "query response exceeds 4 MiB")
		}
		if e := runtime.retain(64 + retainedBytes(row)); e != nil {
			return false, e
		}
		rows = append(rows, row)
		return true, nil
	})
	if e != nil {
		return nil, e
	}
	sorted(p.result.view, rows, p.output.OrderBy)
	return map[string]any{"rows": rows, "truncated": false}, nil
}

func queryField(c Collection, name string) (Field, error) {
	for _, f := range c.Fields {
		if f.Name == name {
			return f, nil
		}
	}
	return Field{}, Invalid("unknown relation field %q", name)
}
func queryFilterFields(c Collection, f *Filter) error {
	var failed error
	visitFilterFields(f, func(name string) {
		if failed == nil {
			_, failed = queryField(c, name)
		}
	})
	return failed
}
func queryComputeFields(c Collection, n QueryNode) error {
	c.Fields = append([]Field(nil), c.Fields...)
	for _, computed := range n.Computed {
		var failed error
		computed.Expr.visit(func(name string) {
			if failed == nil {
				_, failed = queryField(c, name)
			}
		})
		if failed != nil {
			return failed
		}
		// Types are validated by newRelChain; add names here only for dependencies.
		c.Fields = append(c.Fields, Field{Name: computed.Name})
	}
	if e := queryFilterFields(c, n.Where); e != nil {
		return e
	}
	var failed error
	if n.Predicate != nil {
		n.Predicate.visit(func(name string) {
			if failed == nil {
				_, failed = queryField(c, name)
			}
		})
	}
	return failed
}
func queryAggregateFields(c Collection, n QueryNode) error {
	for _, name := range n.GroupBy {
		if _, e := queryField(c, name); e != nil {
			return e
		}
	}
	for _, metric := range n.Metrics {
		if metric.Field != "" {
			if _, e := queryField(c, metric.Field); e != nil {
				return e
			}
		}
		if e := queryFilterFields(c, metric.Where); e != nil {
			return e
		}
	}
	return queryFilterFields(c, n.Where)
}

func queryRowsBytes(rows []Record) int {
	size := 24 + 16*len(rows)
	for _, r := range rows {
		size += retainedBytes(r)
	}
	return size
}
