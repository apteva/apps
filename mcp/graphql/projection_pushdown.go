package main

import (
	"sort"

	gql "github.com/graphql-go/graphql"
	gast "github.com/graphql-go/graphql/language/ast"
)

// applyTablesProjection derives the Tables select list from the validated
// GraphQL selection. It is an internal source optimization: result completion,
// aliases, fragments and all public GraphQL behavior stay unchanged.
func applyTablesProjection(p gql.ResolveParams, state *standardRequest, operation string, config map[string]any) {
	switch operation {
	case "find", "list", "search", "get":
	default:
		return
	}
	if enabled, configured := config["selection_pushdown"].(bool); configured && !enabled {
		return
	}
	runtime := &p.Info.Schema
	fragments := map[string]*gast.FragmentDefinition{}
	for name, definition := range p.Info.Fragments {
		if fragment, ok := definition.(*gast.FragmentDefinition); ok {
			fragments[name] = fragment
		}
	}
	sets := make([]*gast.SelectionSet, 0, len(p.Info.FieldASTs))
	for _, field := range p.Info.FieldASTs {
		sets = append(sets, field.SelectionSet)
	}
	rowObject, rowSets, ok := projectionRowSelection(runtime, p.Info.ReturnType, sets, fragments, operation)
	if !ok {
		return
	}
	groups, ok := fastCollect(runtime, rowObject, rowSets, fragments)
	if !ok {
		return
	}
	columns := map[string]bool{}
	if values, ok := config["distinct_by"].([]any); ok {
		for _, value := range values {
			if key, ok := value.(string); ok && graphqlName(key) {
				columns[key] = true
			}
		}
	}
	for _, group := range groups {
		if group.name == "__typename" {
			continue
		}
		resolver, resolved := state.bindings.resolvers[rowObject.Name()+"."+group.name]
		if resolved {
			if source, found := state.bindings.sources[resolver.SourceID]; found && source.Kind == "tables" {
				childConfig := mergeMaps(source.Config, resolver.Config)
				if relation, ok := childConfig["relation"].(map[string]any); ok {
					if parentKey, ok := relation["parent_key"].(string); ok && graphqlName(parentKey) {
						columns[parentKey] = true
					}
				}
			}
			continue
		}
		column := group.name
		if graphqlName(column) {
			columns[column] = true
		}
	}
	allowed, restricted := stringSet(config["select"])
	selected := make([]string, 0, len(columns))
	for column := range columns {
		if !restricted || allowed[column] {
			selected = append(selected, column)
		}
	}
	if len(selected) == 0 {
		if !restricted || allowed["id"] {
			selected = append(selected, "id")
		} else {
			return
		}
	}
	sort.Strings(selected)
	values := make([]any, len(selected))
	for index := range selected {
		values[index] = selected[index]
	}
	config["select"] = values
}

func projectionRowSelection(runtime *gql.Schema, output gql.Output, sets []*gast.SelectionSet, fragments map[string]*gast.FragmentDefinition, operation string) (*gql.Object, []*gast.SelectionSet, bool) {
	unwrap := func(value gql.Output) gql.Output {
		for {
			if required, ok := value.(*gql.NonNull); ok {
				value = required.OfType
				continue
			}
			return value
		}
	}
	value := unwrap(output)
	if operation != "search" {
		if list, ok := value.(*gql.List); ok {
			value = unwrap(list.OfType)
		}
		object, ok := value.(*gql.Object)
		return object, sets, ok
	}
	page, ok := value.(*gql.Object)
	if !ok {
		return nil, nil, false
	}
	groups, ok := fastCollect(runtime, page, sets, fragments)
	if !ok {
		return nil, nil, false
	}
	for _, group := range groups {
		if group.name != "rows" {
			continue
		}
		definition := page.Fields()[group.name]
		if definition == nil {
			return nil, nil, false
		}
		rowType := unwrap(definition.Type)
		list, ok := rowType.(*gql.List)
		if !ok {
			return nil, nil, false
		}
		rowType = unwrap(list.OfType)
		object, ok := rowType.(*gql.Object)
		if !ok {
			return nil, nil, false
		}
		rowSets := make([]*gast.SelectionSet, 0, len(group.fields))
		for _, field := range group.fields {
			rowSets = append(rowSets, field.SelectionSet)
		}
		return object, rowSets, true
	}
	return nil, nil, false
}

func stringSet(raw any) (map[string]bool, bool) {
	out := map[string]bool{}
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok {
				out[text] = true
			}
		}
		return out, true
	case []string:
		for _, value := range values {
			out[value] = true
		}
		return out, true
	default:
		return out, false
	}
}
