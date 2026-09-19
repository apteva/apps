package main

// gqlparser validates the current SDL/query language. This boundary coerces
// request inputs before passing a variable-free AST to the completion engine.
// In particular, explicit null must not be confused with an omitted value.
import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"

	gql "github.com/graphql-go/graphql"
	"github.com/graphql-go/graphql/gqlerrors"
	gast "github.com/graphql-go/graphql/language/ast"
	"github.com/graphql-go/graphql/language/source"
	"github.com/vektah/gqlparser/v2/ast"
)

type argumentValuesKey struct{}
type argumentValues map[*gast.Field]map[string]any

func coerceInput(schema *ast.Schema, t *ast.Type, value any) (any, error) {
	if value == nil {
		if t.NonNull {
			return nil, fmt.Errorf("%s cannot be null", t)
		}
		return nil, nil
	}
	if t.Elem != nil {
		v := reflect.ValueOf(value)
		if v.Kind() != reflect.Slice {
			value = []any{value}
			v = reflect.ValueOf(value)
		}
		out := make([]any, v.Len())
		for i := range out {
			x, err := coerceInput(schema, t.Elem, v.Index(i).Interface())
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			out[i] = x
		}
		return out, nil
	}
	def := schema.Types[t.NamedType]
	if def.Kind == ast.InputObject {
		input, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected input object %s", t)
		}
		for name := range input {
			if def.Fields.ForName(name) == nil {
				return nil, fmt.Errorf("unknown input field %s.%s", def.Name, name)
			}
		}
		out := map[string]any{}
		for _, f := range def.Fields {
			v, present := input[f.Name]
			if !present && f.DefaultValue != nil {
				v, _ = f.DefaultValue.Value(nil)
				present = true
			}
			if !present {
				if f.Type.NonNull {
					return nil, fmt.Errorf("missing required input %s.%s", def.Name, f.Name)
				}
				continue
			}
			x, err := coerceInput(schema, f.Type, v)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", def.Name, f.Name, err)
			}
			out[f.Name] = x
		}
		return out, nil
	}
	if def.Kind == ast.Enum {
		v, ok := value.(string)
		if ok && def.EnumValues.ForName(v) != nil {
			return v, nil
		}
		return nil, fmt.Errorf("invalid %s enum value", t)
	}
	switch t.NamedType {
	case "String":
		if v, ok := value.(string); ok {
			return v, nil
		}
	case "Boolean":
		if v, ok := value.(bool); ok {
			return v, nil
		}
	case "ID":
		if v, ok := value.(string); ok {
			return v, nil
		}
		if n, ok := inputNumber(value); ok && n == math.Trunc(n) {
			return strconv.FormatFloat(n, 'f', 0, 64), nil
		}
	case "Int":
		if n, ok := inputNumber(value); ok && n == math.Trunc(n) && n >= math.MinInt32 && n <= math.MaxInt32 {
			return int(n), nil
		}
	case "Float":
		if n, ok := inputNumber(value); ok {
			return n, nil
		}
	default:
		return value, nil
	}
	return nil, fmt.Errorf("invalid value for %s", t)
}
func inputNumber(value any) (float64, bool) {
	var n float64
	switch v := value.(type) {
	case int:
		n = float64(v)
	case int32:
		n = float64(v)
	case int64:
		n = float64(v)
	case float64:
		n = v
	case float32:
		n = float64(v)
	case json.Number:
		var err error
		n, err = v.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return n, !math.IsNaN(n) && !math.IsInf(n, 0)
}

func inputLiteral(v *ast.Value, vars map[string]any) (any, bool) {
	if v.Kind == ast.Variable {
		value, present := vars[v.Raw]
		return value, present
	}
	if v.Kind == ast.ObjectValue {
		out := map[string]any{}
		for _, c := range v.Children {
			if value, present := inputLiteral(c.Value, vars); present {
				out[c.Name] = value
			}
		}
		return out, true
	}
	if v.Kind == ast.ListValue {
		out := []any{}
		for _, c := range v.Children {
			value, _ := inputLiteral(c.Value, vars)
			out = append(out, value)
		}
		return out, true
	}
	value, _ := v.Value(vars)
	return value, true
}

func executeRuntime(ctx context.Context, runtime *gql.Schema, schema *ast.Schema, doc *ast.QueryDocument, op *ast.OperationDefinition, variables map[string]any) (*gql.Result, error) {
	vars := map[string]any{}
	for _, v := range op.VariableDefinitions {
		value, present := variables[v.Variable]
		if !present && v.DefaultValue != nil {
			value, _ = v.DefaultValue.Value(nil)
			present = true
		}
		if !present {
			if v.Type.NonNull {
				return nil, fmt.Errorf("variable $%s is required", v.Variable)
			}
			continue
		}
		coerced, err := coerceInput(schema, v.Type, value)
		if err != nil {
			return nil, fmt.Errorf("variable $%s: %w", v.Variable, err)
		}
		vars[v.Variable] = coerced
	}
	args := argumentValues{}
	usedFragments := map[string]bool{}
	sources := map[*ast.Source]*source.Source{}
	name := func(s string) *gast.Name { return gast.NewName(&gast.Name{Value: s}) }
	loc := func(p *ast.Position) *gast.Location {
		if p == nil {
			return nil
		}
		s := sources[p.Src]
		if s == nil {
			s = source.NewSource(&source.Source{Name: p.Src.Name, Body: []byte(p.Src.Input)})
			sources[p.Src] = s
		}
		return &gast.Location{Start: p.Start, End: p.End, Source: s}
	}
	directives := func(ds ast.DirectiveList) []*gast.Directive {
		out := []*gast.Directive{}
		for _, d := range ds {
			if d.Name != "skip" && d.Name != "include" {
				continue
			}
			v, _ := inputLiteral(d.Arguments.ForName("if").Value, vars)
			out = append(out, gast.NewDirective(&gast.Directive{Name: name(d.Name), Arguments: []*gast.Argument{gast.NewArgument(&gast.Argument{Name: name("if"), Value: executionLiteral(v)})}}))
		}
		return out
	}
	var selection func(ast.SelectionSet) (*gast.SelectionSet, error)
	selection = func(set ast.SelectionSet) (*gast.SelectionSet, error) {
		out := gast.NewSelectionSet(&gast.SelectionSet{})
		for _, item := range set {
			switch f := item.(type) {
			case *ast.Field:
				child, err := selection(f.SelectionSet)
				if err != nil {
					return nil, err
				}
				field := gast.NewField(&gast.Field{Name: name(f.Name), Loc: loc(f.Position), Directives: directives(f.Directives), SelectionSet: child})
				if f.Alias != "" && f.Alias != f.Name {
					field.Alias = name(f.Alias)
				}
				values := map[string]any{}
				for _, def := range f.Definition.Arguments {
					var value any
					present := false
					if arg := f.Arguments.ForName(def.Name); arg != nil {
						value, present = inputLiteral(arg.Value, vars)
					}
					if !present && def.DefaultValue != nil {
						value, _ = def.DefaultValue.Value(nil)
						present = true
					}
					if !present {
						if def.Type.NonNull {
							return nil, fmt.Errorf("argument %s.%s is required", f.Name, def.Name)
						}
						continue
					}
					x, err := coerceInput(schema, def.Type, value)
					if err != nil {
						return nil, fmt.Errorf("argument %s.%s: %w", f.Name, def.Name, err)
					}
					values[def.Name] = x
					field.Arguments = append(field.Arguments, gast.NewArgument(&gast.Argument{Name: name(def.Name), Value: executionLiteral(x)}))
				}
				args[field] = values
				out.Selections = append(out.Selections, field)
			case *ast.FragmentSpread:
				usedFragments[f.Name] = true
				out.Selections = append(out.Selections, gast.NewFragmentSpread(&gast.FragmentSpread{Name: name(f.Name), Loc: loc(f.Position), Directives: directives(f.Directives)}))
			case *ast.InlineFragment:
				child, err := selection(f.SelectionSet)
				if err != nil {
					return nil, err
				}
				node := gast.NewInlineFragment(&gast.InlineFragment{Loc: loc(f.Position), Directives: directives(f.Directives), SelectionSet: child})
				if f.TypeCondition != "" {
					node.TypeCondition = gast.NewNamed(&gast.Named{Name: name(f.TypeCondition)})
				}
				out.Selections = append(out.Selections, node)
			}
		}
		return out, nil
	}
	set, err := selection(op.SelectionSet)
	if err != nil {
		return nil, err
	}
	operation := gast.NewOperationDefinition(&gast.OperationDefinition{Operation: string(op.Operation), Name: name(op.Name), SelectionSet: set, Loc: loc(op.Position)})
	document := gast.NewDocument(&gast.Document{Definitions: []gast.Node{operation}})
	added := map[string]bool{}
	for len(added) < len(usedFragments) {
		for _, f := range doc.Fragments {
			if !usedFragments[f.Name] || added[f.Name] {
				continue
			}
			added[f.Name] = true
			set, err := selection(f.SelectionSet)
			if err != nil {
				return nil, err
			}
			document.Definitions = append(document.Definitions, gast.NewFragmentDefinition(&gast.FragmentDefinition{Name: name(f.Name), TypeCondition: gast.NewNamed(&gast.Named{Name: name(f.TypeCondition)}), SelectionSet: set, Loc: loc(f.Position)}))
		}
	}
	ctx = context.WithValue(ctx, argumentValuesKey{}, args)
	if result, ok := tryFastProjection(ctx, runtime, document, operation); ok {
		return result, nil
	}
	params := gql.ExecuteParams{Schema: *runtime, AST: document, OperationName: op.Name, Context: ctx}
	result := gql.Execute(params)
	// The completion engine cannot retain nullable ancestors across deferred
	// non-null failures. On the error path only, complete synchronously using
	// the same request-local source futures. A completed or failed source call
	// is never repeated. Mutations are synchronous from the start, never replayed.
	if state, ok := ctx.Value(standardRequestKey{}).(*standardRequest); ok && op.Operation == ast.Query && len(result.Errors) > 0 && ctx.Err() == nil {
		state.synchronous = true
		result = gql.Execute(params)
	}
	return result, nil
}

func executionLiteral(value any) gast.Value {
	switch v := value.(type) {
	case nil:
		return gast.NewEnumValue(&gast.EnumValue{Value: "null"})
	case string:
		return gast.NewStringValue(&gast.StringValue{Value: v})
	case bool:
		return gast.NewBooleanValue(&gast.BooleanValue{Value: v})
	case int:
		return gast.NewIntValue(&gast.IntValue{Value: strconv.Itoa(v)})
	case float64:
		return gast.NewFloatValue(&gast.FloatValue{Value: strconv.FormatFloat(v, 'g', -1, 64)})
	case []any:
		out := gast.NewListValue(&gast.ListValue{})
		for _, x := range v {
			out.Values = append(out.Values, executionLiteral(x))
		}
		return out
	case map[string]any:
		out := gast.NewObjectValue(&gast.ObjectValue{})
		for key, x := range v {
			out.Fields = append(out.Fields, gast.NewObjectField(&gast.ObjectField{Name: gast.NewName(&gast.Name{Value: key}), Value: executionLiteral(x)}))
		}
		return out
	default:
		return gast.NewStringValue(&gast.StringValue{Value: fmt.Sprint(v)})
	}
}

func standardArguments(resolve gql.FieldResolveFn) gql.FieldResolveFn {
	return func(p gql.ResolveParams) (any, error) {
		if values, ok := p.Context.Value(argumentValuesKey{}).(argumentValues); ok && len(p.Info.FieldASTs) > 0 {
			p.Args = values[p.Info.FieldASTs[0]]
		}
		if resolve == nil {
			return gql.DefaultResolveFn(p)
		}
		return resolve(p)
	}
}

func inputError(err error) *gql.Result {
	return &gql.Result{Errors: []gqlerrors.FormattedError{gqlerrors.NewFormattedError(err.Error())}}
}
