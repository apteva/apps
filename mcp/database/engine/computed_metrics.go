package engine

import (
	"context"
	"errors"
	"slices"
	"sync"

	native "github.com/apteva/db"
)

// ComputedInput describes contributions from one ordinary collection. Group
// fields default to their output names. Reusing a measure name combines its
// contributions from the declared inputs.
type ComputedInput struct {
	Collection  string            `json:"collection"`
	Where       *Filter           `json:"where,omitempty"`
	GroupFields map[string]string `json:"groupFields,omitempty"`
	Metrics     []Metric          `json:"metrics"`
}

type ComputedBuild struct {
	State   string            `json:"state"`
	Scanned map[string]uint64 `json:"scanned"`
	Error   string            `json:"error,omitempty"`
}

func (c Collection) onlineComputed() bool {
	return c.ComputedCollection != nil && len(c.ComputedCollection.Sources) != 0
}

func (c Collection) computedDistinct(name string) bool {
	if c.onlineComputed() {
		for _, input := range c.ComputedCollection.Sources {
			for _, metric := range input.Metrics {
				if metric.Name == name {
					return metric.Op == "count_distinct"
				}
			}
		}
	}
	return false
}

func newComputedMetrics(tx transaction, r Request, d ComputedCollectionDefinition) (Collection, error) {
	if d.Source != "" || d.Where != nil || len(d.Select) != 0 || len(d.Metrics) != 0 ||
		len(d.Sources) < 1 || len(d.Sources) > 16 || len(d.GroupBy) < 1 || len(d.GroupBy) > 8 {
		return Collection{}, Invalid("sources requires 1–16 inputs and 1–8 groupBy fields; define filters and metrics per input")
	}
	dimensions := make([]Field, len(d.GroupBy))
	seen := map[string]bool{}
	type reducer struct{ op, fieldType string }
	reducers := map[string]reducer{}
	for i, input := range d.Sources {
		if input.Collection == r.Collection || seen[input.Collection] || len(input.Metrics) < 1 || len(input.Metrics) > 64 {
			return Collection{}, Invalid("computed inputs must be distinct ordinary collections with 1–64 measures")
		}
		seen[input.Collection] = true
		source, err := tx.collection(input.Collection)
		if err != nil {
			return Collection{}, err
		}
		if source.ComputedCollection != nil {
			return Collection{}, Invalid("computed inputs must be ordinary collections")
		}
		for name := range input.GroupFields {
			if !slices.Contains(d.GroupBy, name) {
				return Collection{}, Invalid("unknown mapped group field %q", name)
			}
		}
		for j, name := range d.GroupBy {
			f, err := source.field(mappedComputedField(input, name))
			if err != nil {
				return Collection{}, err
			}
			if f.Name[0] == '_' || f.Nullable || f.Type == "json" {
				return Collection{}, Invalid("computed group fields must be non-null stored scalars")
			}
			f.Name = name
			if i == 0 {
				dimensions[j] = f
			} else if f != dimensions[j] {
				return Collection{}, Invalid("input types disagree for group field %q", name)
			}
		}
		if err := validateComputedInputFilter(source, input.Where); err != nil {
			return Collection{}, err
		}
		local := map[string]bool{}
		for _, m := range input.Metrics {
			if err := nameOK(m.Name); err != nil {
				return Collection{}, err
			}
			if local[m.Name] || slices.Contains(d.GroupBy, m.Name) {
				return Collection{}, Invalid("duplicate computed measure %q", m.Name)
			}
			local[m.Name] = true
			if err := validateComputedInputFilter(source, m.Where); err != nil {
				return Collection{}, err
			}
			spec := reducer{op: m.Op}
			switch m.Op {
			case "count", "count_distinct", "sum":
				if m.Field != "" || m.Op != "count" {
					f, err := source.field(m.Field)
					if err != nil {
						return Collection{}, err
					}
					if f.Name[0] == '_' || f.Type == "json" {
						return Collection{}, Invalid("computed measures require stored scalar fields")
					}
					if m.Op == "sum" && (f.Type != "integer" || f.Nullable) {
						return Collection{}, Invalid("computed sum requires a non-null integer field")
					}
					spec.fieldType = f.Type
				}
			default:
				return Collection{}, Invalid("computed measures support count, sum and count_distinct")
			}
			if old, ok := reducers[m.Name]; ok && old != spec {
				return Collection{}, Invalid("input reducers disagree for %q", m.Name)
			}
			reducers[m.Name] = spec
		}
	}
	if len(reducers) > 64 {
		return Collection{}, Invalid("computed collection has more than 64 measures")
	}
	r.Fields = dimensions
	r.PrimaryKey = d.GroupBy
	names := make([]string, 0, len(reducers))
	for name := range reducers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		r.Fields = append(r.Fields, Field{Name: name, Type: "integer", Nullable: reducers[name].op == "sum"})
	}
	c, err := newCollection(r)
	if err != nil {
		return Collection{}, err
	}
	c.ComputedCollection = &d
	return c, nil
}

func validateComputedInputFilter(c Collection, f *Filter) error {
	terms := 0
	if err := validateFilter(c, f, 0, &terms); err != nil {
		return err
	}
	if !computedInputFilterSupported(c, f) {
		return Invalid("computed input filters require stored scalar comparisons")
	}
	return nil
}

func computedInputFilterSupported(c Collection, f *Filter) bool {
	if f == nil {
		return true
	}
	if f.And != nil || f.Or != nil {
		for _, child := range f.And {
			if !computedInputFilterSupported(c, &child) {
				return false
			}
		}
		for _, child := range f.Or {
			if !computedInputFilterSupported(c, &child) {
				return false
			}
		}
		return true
	}
	return computedFilterSupported(c, f)
}

func mappedComputedField(input ComputedInput, name string) string {
	if f, ok := input.GroupFields[name]; ok {
		return f
	}
	return name
}

func computedInputPipeline(source Collection, input ComputedInput, dimensions []string) []byte {
	var stages []any
	if input.Where != nil {
		stages = append(stages, map[string]any{"$match": nativeFilter(source, input.Where)})
	}
	keys := map[string]string{}
	for _, name := range dimensions {
		keys[name] = "$" + mappedComputedField(input, name)
	}
	group := map[string]any{"_id": keys}
	for _, m := range input.Metrics {
		spec := map[string]any{}
		switch {
		case m.Op == "sum":
			spec["$sum"] = "$" + m.Field
		case m.Op == "count_distinct":
			spec["$countDistinct"] = "$" + m.Field
		case m.Field != "":
			spec["$countNonNull"] = "$" + m.Field
		default:
			spec["$count"] = map[string]any{}
		}
		if m.Where != nil {
			spec["where"] = nativeFilter(source, m.Where)
		}
		group[m.Name] = spec
	}
	return marshal(append(stages, map[string]any{"$group": group}))
}

func (b *aptevaBackend) beginComputedMetrics(ctx context.Context, target Collection) (native.ComputedBuildStatus, error) {
	d := target.ComputedCollection
	inputs := make([]native.ComputedCollectionInput, 0, len(d.Sources))
	for _, input := range d.Sources {
		data, found, err := b.db.Find(nativeSchemaKey(input.Collection), nil)
		if err != nil {
			return native.ComputedBuildStatus{}, err
		}
		if !found {
			return native.ComputedBuildStatus{}, fail("not_found", "computed input %s does not exist", input.Collection)
		}
		var c Collection
		if err := decode(data, &c); err != nil {
			return native.ComputedBuildStatus{}, err
		}
		inputs = append(inputs, native.ComputedCollectionInput{Source: nativeCollectionName(input.Collection), Pipeline: computedInputPipeline(c, input, d.GroupBy)})
	}
	return b.db.BeginComputedCollection(ctx, nativeCollectionName(target.Name), inputs)
}

// One worker per database advances native builds fairly in bounded steps.
// The native catalog is the durable progress journal. A backend reopen
// schedules all incomplete definitions already in the app catalog.
type computedBuildWorker struct {
	step     sync.Mutex // serializes a step with app DDL and error publication
	wake     chan struct{}
	done     chan struct{}
	cancel   context.CancelFunc
	failures sync.Map
}

func (b *aptevaBackend) startComputedBuilds() {
	ctx, cancel := context.WithCancel(context.Background())
	b.builds.wake = make(chan struct{}, 1)
	b.builds.done = make(chan struct{})
	b.builds.cancel = cancel
	go b.runComputedBuilds(ctx)
	b.wakeComputedBuilds()
}

func (b *aptevaBackend) wakeComputedBuilds() {
	select {
	case b.builds.wake <- struct{}{}:
	default:
	}
}

func (b *aptevaBackend) runComputedBuilds(ctx context.Context) {
	defer close(b.builds.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.builds.wake:
		}
		var names []string
		if err := b.db.Scan([]byte("schema/"), []byte("schema0"), func(_, data []byte) bool {
			var c Collection
			if decode(data, &c) == nil && c.onlineComputed() {
				names = append(names, c.Name)
			}
			return ctx.Err() == nil
		}); err != nil {
			continue
		}
		for len(names) != 0 && ctx.Err() == nil {
			pending := names[:0]
			for _, name := range names {
				if ctx.Err() != nil {
					return
				}
				b.builds.step.Lock()
				if _, failed := b.builds.failures.Load(name); !failed {
					status, err := b.db.AdvanceComputedCollection(ctx, nativeCollectionName(name), 256)
					if err == nil && status.Ready {
						err = b.finalizeComputedIndexes(ctx, name)
					}
					switch {
					case err == nil && !status.Ready:
						pending = append(pending, name)
					case err != nil && ctx.Err() == nil && !errors.Is(err, native.ErrNoSuchCollection):
						b.builds.failures.Store(name, nativeError(err).Error())
					}
				}
				b.builds.step.Unlock()
			}
			names = pending
		}
	}
}

// Runs under builds.step. Reload the schema after taking that lock so a
// scheduled build cannot reconcile an older declaration after app DDL.
func (b *aptevaBackend) finalizeComputedIndexes(ctx context.Context, name string) error {
	data, found, err := b.db.Find(nativeSchemaKey(name), nil)
	if err != nil || !found {
		return err
	}
	var c Collection
	if err = decode(data, &c); err != nil {
		return err
	}
	if !c.onlineComputed() {
		return nil
	}
	h, err := b.db.OpenCollection(nativeCollectionName(name))
	if err != nil {
		return err
	}
	if err = reconcileNativeIndexes(ctx, h, c); err != nil {
		return err
	}
	return requireComputedIndexes(h, c)
}

func (t *aptevaTx) describeComputedBuild(c Collection) (*ComputedBuild, error) {
	b := t.backend
	status, err := b.db.ComputedCollectionBuild(nativeCollectionName(c.Name))
	if err != nil {
		return nil, nativeError(err)
	}
	out := &ComputedBuild{State: "building", Scanned: map[string]uint64{}}
	if reason, ok := b.builds.failures.Load(c.Name); ok {
		out.State, out.Error = "failed", reason.(string)
	} else if status.Ready {
		if _, err := t.readCollection(c); err == nil {
			out.State = "ready"
		} else if !errors.Is(err, native.ErrComputedCollectionBuilding) {
			return nil, nativeError(err)
		}
	}
	for _, input := range c.ComputedCollection.Sources {
		out.Scanned[input.Collection] = status.Scanned[nativeCollectionName(input.Collection)]
	}
	return out, nil
}
