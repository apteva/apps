package main

// The public contract is GraphQL. This is a guarded execution optimization for
// concrete Tables read selections, not a second client API. Inputs have already
// been validated/coerced by executeRuntime. Any unsupported shape or completion
// error falls back to the standard engine using the same source-read cache.
import (
	"context"
	"math"

	gql "github.com/graphql-go/graphql"
	gast "github.com/graphql-go/graphql/language/ast"
)

type disableFastProjectionKey struct{}

type fastValuePlan struct {
	nonNull  bool
	list     *fastValuePlan
	object   *gql.Object
	fields   []*fastFieldPlan
	leaf     *gql.Scalar
	hasReads bool
}
type fastFieldPlan struct {
	name, key  string
	asts       []*gast.Field
	definition *gql.FieldDefinition
	value      *fastValuePlan
	read       bool
	typename   bool
}
type fastGroup struct {
	name, key string
	fields    []*gast.Field
}

func fastIncluded(directives []*gast.Directive) bool {
	for _, d := range directives {
		if d.Name.Value != "skip" && d.Name.Value != "include" {
			continue
		}
		for _, arg := range d.Arguments {
			if arg.Name.Value == "if" {
				v, ok := arg.Value.(*gast.BooleanValue)
				if !ok {
					return false
				}
				if d.Name.Value == "skip" && v.Value || d.Name.Value == "include" && !v.Value {
					return false
				}
			}
		}
	}
	return true
}

// Collect once per object selection, not once per returned row. Field merging,
// aliases, fragment type conditions and directives match the normal executor.
func fastCollect(runtime *gql.Schema, parent *gql.Object, sets []*gast.SelectionSet, fragments map[string]*gast.FragmentDefinition) ([]*fastGroup, bool) {
	groups := map[string]*fastGroup{}
	out := []*fastGroup{}
	visited := map[string]bool{}
	matches := func(name string) bool {
		if name == "" || name == parent.Name() {
			return true
		}
		if abstract, ok := runtime.Type(name).(gql.Abstract); ok {
			return runtime.IsPossibleType(abstract, parent)
		}
		return false
	}
	var collect func(*gast.SelectionSet) bool
	collect = func(set *gast.SelectionSet) bool {
		if set == nil {
			return true
		}
		for _, item := range set.Selections {
			switch f := item.(type) {
			case *gast.Field:
				if !fastIncluded(f.Directives) {
					continue
				}
				key := f.Name.Value
				if f.Alias != nil {
					key = f.Alias.Value
				}
				group := groups[key]
				if group == nil {
					group = &fastGroup{name: f.Name.Value, key: key}
					groups[key] = group
					out = append(out, group)
				}
				group.fields = append(group.fields, f)
			case *gast.FragmentSpread:
				if !fastIncluded(f.Directives) || visited[f.Name.Value] {
					continue
				}
				visited[f.Name.Value] = true
				fragment := fragments[f.Name.Value]
				if fragment == nil {
					return false
				}
				if matches(fragment.TypeCondition.Name.Value) && !collect(fragment.SelectionSet) {
					return false
				}
			case *gast.InlineFragment:
				if !fastIncluded(f.Directives) {
					continue
				}
				name := ""
				if f.TypeCondition != nil {
					name = f.TypeCondition.Name.Value
				}
				if matches(name) && !collect(f.SelectionSet) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	for _, set := range sets {
		if !collect(set) {
			return nil, false
		}
	}
	return out, true
}

func compileFastProjection(ctx context.Context, runtime *gql.Schema, state *standardRequest, operation *gast.OperationDefinition, document *gast.Document) (*fastValuePlan, bool) {
	fragments := map[string]*gast.FragmentDefinition{}
	for _, d := range document.Definitions {
		if f, ok := d.(*gast.FragmentDefinition); ok {
			fragments[f.Name.Value] = f
		}
	}
	var compile func(gql.Output, []*gast.SelectionSet, bool) (*fastValuePlan, bool)
	compile = func(output gql.Output, sets []*gast.SelectionSet, root bool) (*fastValuePlan, bool) {
		p := &fastValuePlan{}
		if required, ok := output.(*gql.NonNull); ok {
			p.nonNull = true
			output = required.OfType
		}
		switch t := output.(type) {
		case *gql.List:
			var ok bool
			p.list, ok = compile(t.OfType, sets, false)
			if !ok {
				return nil, false
			}
			p.hasReads = p.list.hasReads
		case *gql.Scalar:
			if t != gql.ID && t != gql.String && t != gql.Int && t != gql.Float && t != gql.Boolean {
				return nil, false
			}
			p.leaf = t
		case *gql.Object:
			p.object = t
			groups, ok := fastCollect(runtime, t, sets, fragments)
			if !ok {
				return nil, false
			}
			for _, group := range groups {
				field := &fastFieldPlan{name: group.name, key: group.key, asts: group.fields}
				if err := requirePermissions(securityIdentity(ctx), state.policy.Fields[t.Name()+"."+group.name]); err != nil {
					return nil, false
				}
				if group.name == "__typename" {
					field.typename = true
					p.fields = append(p.fields, field)
					continue
				}
				if group.name == "__schema" || group.name == "__type" {
					return nil, false
				}
				field.definition = t.Fields()[group.name]
				if field.definition == nil {
					return nil, false
				}
				if resolver, exists := state.bindings.resolvers[t.Name()+"."+group.name]; exists {
					source, exists := state.bindings.sources[resolver.SourceID]
					if !exists || source.Kind != "tables" {
						return nil, false
					}
					switch resolver.Operation {
					case "find", "list", "search", "get", "count", "aggregate":
					default:
						return nil, false
					}
					field.read = true
				} else if root {
					return nil, false
				}
				children := []*gast.SelectionSet{}
				for _, ast := range group.fields {
					children = append(children, ast.SelectionSet)
				}
				field.value, ok = compile(field.definition.Type, children, false)
				if !ok {
					return nil, false
				}
				p.hasReads = p.hasReads || field.read || field.value.hasReads
				p.fields = append(p.fields, field)
			}
		default:
			return nil, false // abstract types, enums and custom scalars use normal execution
		}
		return p, true
	}
	return compile(runtime.QueryType(), []*gast.SelectionSet{operation.SelectionSet}, true)
}

type fastPendingRead struct {
	read   func() (any, error)
	plan   *fastValuePlan
	path   *gql.ResponsePath
	object map[string]any
	key    string
	raw    any
}

func tryFastProjection(ctx context.Context, runtime *gql.Schema, document *gast.Document, operation *gast.OperationDefinition) (*gql.Result, bool) {
	state, ok := ctx.Value(standardRequestKey{}).(*standardRequest)
	if !ok || state.mutation || operation.Operation != "query" || ctx.Value(disableFastProjectionKey{}) == true {
		return nil, false
	}
	plan, ok := compileFastProjection(ctx, runtime, state, operation, document)
	if !ok {
		return nil, false
	}
	args, _ := ctx.Value(argumentValuesKey{}).(argumentValues)
	pending := []*fastPendingRead{}
	var project func(*fastValuePlan, any, *gql.ResponsePath, bool) (any, bool)
	project = func(p *fastValuePlan, raw any, path *gql.ResponsePath, root bool) (any, bool) {
		if !root && raw == nil {
			return nil, !p.nonNull
		}
		if p.list != nil {
			rows, ok := raw.([]any)
			if !ok {
				return nil, false
			}
			out := make([]any, len(rows))
			for i, row := range rows {
				if i%256 == 0 && ctx.Err() != nil {
					return nil, false
				}
				next := path
				if p.hasReads {
					next = path.WithKey(i)
				}
				var ok bool
				out[i], ok = project(p.list, row, next, false)
				if !ok {
					return nil, false
				}
			}
			return out, true
		}
		if p.leaf != nil {
			// Tables returns JSON values. Other Go representations and invalid
			// scalars go through normal completion, including its error rules.
			switch v := raw.(type) {
			case string, bool, int, int64:
			case float64:
				if math.IsNaN(v) || math.IsInf(v, 0) {
					return nil, false
				}
			default:
				return nil, false
			}
			value := p.leaf.Serialize(raw)
			if value == nil {
				return nil, false
			}
			return value, true
		}
		row, ok := raw.(map[string]any)
		if !root && !ok {
			return nil, false
		}
		out := make(map[string]any, len(p.fields))
		for _, f := range p.fields {
			if f.typename {
				out[f.key] = p.object.Name()
				continue
			}
			next := path
			if f.read || f.value.hasReads {
				next = path.WithKey(f.key)
			}
			if f.read {
				params := gql.ResolveParams{Source: raw, Args: args[f.asts[0]], Context: ctx, Info: gql.ResolveInfo{FieldName: f.name, FieldASTs: f.asts, Path: next, ParentType: p.object, ReturnType: f.definition.Type, Schema: *runtime, Operation: operation}}
				value, err := state.loader.app.standardResolve(params)
				if err != nil {
					return nil, false
				}
				read, ok := value.(func() (any, error))
				if !ok {
					return nil, false
				}
				pending = append(pending, &fastPendingRead{read: read, plan: f.value, path: next, object: out, key: f.key})
				continue
			}
			value, ok := project(f.value, row[f.name], next, false)
			if !ok {
				return nil, false
			}
			out[f.key] = value
		}
		return out, true
	}
	data, ok := project(plan, nil, nil, true)
	if !ok {
		return nil, false
	}
	for len(pending) > 0 {
		level := pending
		pending = nil
		// Resolve the whole level before expanding children: otherwise the
		// first parent's future would prematurely flush sibling relationships.
		for _, job := range level {
			raw, err := job.read()
			if err != nil {
				return nil, false
			}
			job.raw = raw
		}
		for _, job := range level {
			value, ok := project(job.plan, job.raw, job.path, false)
			if !ok {
				return nil, false
			}
			job.object[job.key] = value
		}
	}
	if ctx.Err() != nil {
		return nil, false
	}
	state.fastProjectionUsed = true
	return &gql.Result{Data: data}, true
}
