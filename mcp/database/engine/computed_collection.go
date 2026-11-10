package engine

import (
	"slices"
	"strconv"

	native "github.com/apteva/db"
)

// ComputedCollectionDefinition describes a maintained projection or rollup
// over one ordinary collection in the same database.
type ComputedCollectionDefinition struct {
	Source  string   `json:"source"`
	Where   *Filter  `json:"where,omitempty"`
	Select  []string `json:"select,omitempty"`
	GroupBy []string `json:"groupBy,omitempty"`
	Metrics []Metric `json:"metrics,omitempty"`
}

func (c Collection) computedGroup() bool {
	return c.ComputedCollection != nil && len(c.ComputedCollection.GroupBy) > 0
}

func newComputedCollection(tx transaction, r Request) (Collection, error) {
	if cached, ok := tx.(cachedSchemaTx); ok {
		tx = cached.transaction
	}
	if _, ok := tx.(*aptevaTx); !ok {
		return Collection{}, fail("unsupported_capability", "computed collections require the AptevaDB adapter")
	}
	if r.Fields != nil || r.PrimaryKey != nil {
		return Collection{}, Invalid("computed collections infer fields and primaryKey from their definition")
	}
	var definition ComputedCollectionDefinition
	if err := decode(marshal(r.ComputedCollection), &definition); err != nil {
		return Collection{}, Invalid("invalid computed collection definition")
	}
	if err := nameOK(definition.Source); err != nil {
		return Collection{}, err
	}
	if definition.Source == r.Collection {
		return Collection{}, Invalid("computed collection must have a different source")
	}
	source, err := tx.collection(definition.Source)
	if err != nil {
		return Collection{}, err
	}
	if source.ComputedCollection != nil {
		return Collection{}, Invalid("computed collections require an ordinary source")
	}
	terms := 0
	if err := validateFilter(source, definition.Where, 0, &terms); err != nil {
		return Collection{}, err
	}
	if !computedFilterSupported(source, definition.Where) {
		return Collection{}, Invalid("computed collection filters require a conjunction of scalar comparisons on stored source fields")
	}

	if len(definition.GroupBy) == 0 {
		if len(definition.Metrics) != 0 {
			return Collection{}, Invalid("computed rollups require groupBy")
		}
		if len(definition.Select) == 0 {
			for _, field := range source.Fields {
				definition.Select = append(definition.Select, field.Name)
			}
		}
		r.PrimaryKey = source.PrimaryKey
		for _, name := range r.PrimaryKey {
			if !slices.Contains(definition.Select, name) {
				return Collection{}, Invalid("projection must retain source primary key field %q", name)
			}
		}
		for _, name := range definition.Select {
			if name == "" || name[0] == '_' {
				return Collection{}, Invalid("computed projection requires stored source fields")
			}
			field, err := source.field(name)
			if err != nil {
				return Collection{}, err
			}
			r.Fields = append(r.Fields, field)
		}
	} else {
		if len(definition.Select) != 0 || len(definition.Metrics) == 0 || len(definition.Metrics) > 64 {
			return Collection{}, Invalid("computed rollups require 1–64 metrics and no select")
		}
		r.PrimaryKey = append([]string(nil), definition.GroupBy...)
		for _, name := range definition.GroupBy {
			if name == "" || name[0] == '_' {
				return Collection{}, Invalid("computed group keys require stored source fields")
			}
			field, err := source.field(name)
			if err != nil {
				return Collection{}, err
			}
			r.Fields = append(r.Fields, field)
		}
		for _, metric := range definition.Metrics {
			if metric.Where != nil || (metric.Op != "count" && metric.Op != "sum") {
				return Collection{}, Invalid("computed metrics support count and integer sum without a metric filter")
			}
			if metric.Field != "" || metric.Op == "sum" {
				field, err := source.field(metric.Field)
				if err != nil {
					return Collection{}, err
				}
				if field.Name[0] == '_' {
					return Collection{}, Invalid("computed metrics require stored source fields")
				}
				if metric.Op == "sum" && (field.Type != "integer" || field.Nullable) {
					return Collection{}, Invalid("computed sum requires a non-null integer field")
				}
			}
			r.Fields = append(r.Fields, Field{Name: metric.Name, Type: "integer"})
		}
	}
	c, err := newCollection(r)
	if err != nil {
		return Collection{}, err
	}
	c.ComputedCollection = &definition
	if _, err := computedNativePipeline(source, c); err != nil {
		return Collection{}, err
	}
	return c, nil
}

func computedFilterSupported(source Collection, f *Filter) bool {
	if f == nil {
		return true
	}
	if f.Or != nil {
		return false
	}
	if f.And != nil {
		for _, child := range f.And {
			if !computedFilterSupported(source, &child) {
				return false
			}
		}
		return true
	}
	field, err := source.field(f.Field)
	if err != nil || f.Field[0] == '_' || field.Type == "json" {
		return false
	}
	switch f.Op {
	case "eq", "ne", "gt", "gte", "lt", "lte", "in", "not_in", "is_null", "is_not_null", "starts_with":
		return true
	}
	return false
}

func computedNativePipeline(source, target Collection) ([]byte, error) {
	d := target.ComputedCollection
	stages := []any{}
	// Native computed collections express conjunction as consecutive matches.
	// Keep the portable null guards emitted by nativeFilter as separate stages.
	var addMatch func(any)
	addMatch = func(value any) {
		if children, ok := value.(map[string]any)["$and"].([]any); ok {
			for _, child := range children {
				addMatch(child)
			}
			return
		}
		stages = append(stages, map[string]any{"$match": value})
	}
	if d.Where != nil {
		addMatch(nativeFilter(source, d.Where))
	}
	if len(stages) > 15 {
		return nil, Invalid("computed collection filter exceeds 15 native match stages")
	}
	if !target.computedGroup() {
		projection := map[string]int{}
		for _, f := range target.Fields {
			projection[f.Name] = 1
		}
		return marshal(append(stages, map[string]any{"$project": projection})), nil
	}
	keys := map[string]string{}
	for _, name := range d.GroupBy {
		keys[name] = "$" + name
	}
	group := map[string]any{"_id": keys}
	for _, metric := range d.Metrics {
		switch {
		case metric.Op == "sum":
			group[metric.Name] = map[string]any{"$sum": "$" + metric.Field}
		case metric.Field != "":
			group[metric.Name] = map[string]any{"$countNonNull": "$" + metric.Field}
		default:
			group[metric.Name] = map[string]any{"$count": map[string]any{}}
		}
	}
	return marshal(append(stages, map[string]any{"$group": group})), nil
}

func nativeReadKey(c Collection, r Record) ([]byte, error) {
	if !c.computedGroup() {
		key, _, err := indexTuple(c, primaryIndex(c), r)
		return key, err
	}
	names := slices.Clone(c.PrimaryKey)
	slices.Sort(names)
	parts := make([]any, len(names))
	for i, name := range names {
		field, _ := c.field(name)
		v := r[name]
		if field.Type == "integer" {
			n, err := strconv.ParseInt(v.(string), 10, 64)
			if err != nil {
				return nil, err
			}
			v = n
		}
		parts[i] = v
	}
	return native.ComputedGroupTupleKey(parts...)
}
