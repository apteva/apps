package main

import (
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"
)

func validateSDL(sdl string) (*ast.Schema, []string) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: sdl})
	if err == nil {
		return schema, []string{}
	}
	return nil, gqlErrors(err)
}

func parseAndValidateQuery(schema *ast.Schema, query string) (*ast.QueryDocument, []string) {
	doc, err := parser.ParseQuery(&ast.Source{Name: "query.graphql", Input: query})
	if err != nil {
		return nil, gqlErrors(err)
	}
	errs := validator.Validate(schema, doc)
	if len(errs) > 0 {
		return nil, gqlErrors(errs)
	}
	return doc, nil
}

func queryErrorObjects(schema *ast.Schema, query string) []map[string]any {
	_, errors := gqlparser.LoadQuery(schema, query)
	out := []map[string]any{}
	for _, err := range errors {
		item := map[string]any{"message": err.Message, "extensions": map[string]any{"code": "query_validation_failed"}}
		if len(err.Locations) > 0 {
			item["locations"] = err.Locations
		}
		out = append(out, item)
	}
	return out
}

func gqlErrors(err any) []string {
	switch e := err.(type) {
	case gqlerror.List:
		out := make([]string, 0, len(e))
		for _, item := range e {
			if item != nil {
				out = append(out, item.Error())
			}
		}
		return out
	case error:
		return []string{e.Error()}
	default:
		return []string{fmt.Sprint(e)}
	}
}

func operationFor(doc *ast.QueryDocument, name string) (*ast.OperationDefinition, error) {
	if len(doc.Operations) == 0 {
		return nil, invalid("query contains no operation")
	}
	if name != "" {
		for _, op := range doc.Operations {
			if op.Name == name {
				return op, nil
			}
		}
		return nil, invalid("operation %q was not found", name)
	}
	if len(doc.Operations) > 1 {
		return nil, invalid("operationName is required when a document has multiple operations")
	}
	return doc.Operations[0], nil
}

func operationType(op *ast.OperationDefinition) string {
	switch strings.ToLower(string(op.Operation)) {
	case "mutation":
		return "Mutation"
	case "subscription":
		return "Subscription"
	default:
		return "Query"
	}
}

func queryCost(set ast.SelectionSet, depth int) (fields, maxDepth int) {
	maxDepth = depth
	for _, selection := range set {
		var field *ast.Field
		switch item := selection.(type) {
		case *ast.Field:
			field = item
		case *ast.FragmentSpread:
			if item.Definition != nil {
				n, d := queryCost(item.Definition.SelectionSet, depth)
				fields += n
				maxDepth = max(maxDepth, d)
			}
		case *ast.InlineFragment:
			n, d := queryCost(item.SelectionSet, depth)
			fields += n
			maxDepth = max(maxDepth, d)
		}
		if fields > 10000 {
			return fields, maxDepth
		}
		if field == nil {
			continue
		}
		fields++
		childFields, childDepth := queryCost(field.SelectionSet, depth+1)
		fields += childFields
		if childDepth > maxDepth {
			maxDepth = childDepth
		}
	}
	return fields, maxDepth
}
